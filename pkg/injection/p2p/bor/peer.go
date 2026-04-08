// Package bor implements a lightweight chaos peer for Bor's devp2p network.
// It connects directly to Bor nodes via RLPx, completes the eth/68-69 handshake,
// and then sends crafted malicious protocol messages for fault injection.
package bor

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/rlpx"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/rs/zerolog"
)

// Protocol offset constants, mirroring bor/cmd/devp2p/internal/ethtest/protocol.go.
// These define where each sub-protocol's message codes begin in the multiplexed stream.
const (
	baseProtoLen = 16 // devp2p base messages (hello, disconnect, ping, pong, etc.)
	ethProtoLen  = 18 // eth/68 or eth/69 messages

	// devp2p base message codes
	handshakeMsgCode = 0x00
	discMsgCode      = 0x01
	pingMsgCode      = 0x02
	pongMsgCode      = 0x03

	// handshakeTimeout is the deadline for completing the full RLPx + eth handshake.
	handshakeTimeout = 5 * time.Second
	// msgTimeout is the deadline for a single read or write operation.
	msgTimeout = 2 * time.Second
)

// ethProtoOffset is where eth protocol message codes begin after the base protocol.
const ethProtoOffset = uint64(baseProtoLen)

// protoHandshake is the devp2p Hello message (p2p/peer.go protoHandshake).
// We replicate the structure here to avoid importing the unexported type.
type protoHandshake struct {
	Version    uint64
	Name       string
	Caps       []p2p.Cap
	ListenPort uint64
	ID         []byte
	Rest       []rlp.RawValue `rlp:"tail"`
}

// ChaosPeer is a fake devp2p peer that connects to a Bor node and sends
// malicious eth-protocol messages. It is not a full Ethereum node — it only
// implements enough of the protocol to pass the handshake and inject faults.
//
// Lifecycle: Connect → (attack methods) → Close.
// The peer handles graceful disconnection from Bor: if Bor sends a disconnect
// message in response to a malicious payload, the error is propagated but the
// peer can still be safely closed.
type ChaosPeer struct {
	conn *rlpx.Conn
	key  *ecdsa.PrivateKey
	log  zerolog.Logger

	// negotiated protocol info learned during the eth handshake
	protoVersion uint32
	networkID    uint64
	genesis      common.Hash
	head         common.Hash
	td           *big.Int
	headBlock    uint64 // head block number from the remote's status (eth/69 LatestBlock)

	// negotiatedVersion is the highest eth version both sides agree on
	negotiatedVersion uint

	// AllowProduction disables the production network safety check when set to
	// true. Must be explicitly opted in via --allow-production.
	AllowProduction bool
}

// productionNetworkIDs maps well-known production chain IDs to their names.
var productionNetworkIDs = map[uint64]string{
	1:     "Ethereum Mainnet",
	137:   "Polygon Mainnet",
	56:    "BSC Mainnet",
	10:    "Optimism Mainnet",
	42161: "Arbitrum One",
}

// rejectProductionNetwork refuses to attack known production networks unless
// AllowProduction is explicitly set.
func (cp *ChaosPeer) rejectProductionNetwork() error {
	if cp.AllowProduction {
		return nil
	}
	if name, isProduction := productionNetworkIDs[cp.networkID]; isProduction {
		return fmt.Errorf("refusing to attack production network %s (networkID=%d) — use --allow-production to override", name, cp.networkID)
	}
	return nil
}

// NewChaosPeer creates a ChaosPeer with a freshly generated secp256k1 key.
// The key has no special meaning — Bor accepts any valid RLPx peer.
func NewChaosPeer(log zerolog.Logger) (*ChaosPeer, error) {
	key, err := crypto.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	return &ChaosPeer{
		key: key,
		log: log.With().Str("component", "chaos-peer").Logger(),
	}, nil
}

// NewChaosPeerWithKey creates a ChaosPeer using a pre-existing private key.
// Useful for deterministic testing.
func NewChaosPeerWithKey(key *ecdsa.PrivateKey, log zerolog.Logger) *ChaosPeer {
	return &ChaosPeer{
		key: key,
		log: log.With().Str("component", "chaos-peer").Logger(),
	}
}

// Connect dials the target enode URL, completes the RLPx crypto handshake,
// and exchanges eth Status messages. After Connect returns without error the
// peer is ready to send attack messages.
func (cp *ChaosPeer) Connect(enodeURL string) error {
	node, err := enode.ParseV4(enodeURL)
	if err != nil {
		return fmt.Errorf("parse enode URL: %w", err)
	}

	tcpEndpoint, ok := node.TCPEndpoint()
	if !ok {
		return fmt.Errorf("enode has no TCP endpoint: %s", enodeURL)
	}

	cp.log.Info().
		Str("target", tcpEndpoint.String()).
		Str("enode", enodeURL[:min(len(enodeURL), 80)]).
		Msg("dialing target")

	dialer := net.Dialer{Timeout: handshakeTimeout}
	fd, err := dialer.Dial("tcp", tcpEndpoint.String())
	if err != nil {
		return fmt.Errorf("tcp dial %s: %w", tcpEndpoint, err)
	}

	cp.conn = rlpx.NewConn(fd, node.Pubkey())
	cp.conn.SetDeadline(time.Now().Add(handshakeTimeout))

	if _, err := cp.conn.Handshake(cp.key); err != nil {
		cp.conn.Close()
		cp.conn = nil
		return fmt.Errorf("RLPx handshake: %w", err)
	}

	// Reset deadline — subsequent operations use per-call deadlines.
	cp.conn.SetDeadline(time.Time{})

	if err := cp.devp2pHandshake(); err != nil {
		cp.conn.Close()
		cp.conn = nil
		return fmt.Errorf("devp2p hello: %w", err)
	}

	if err := cp.statusExchange(); err != nil {
		cp.conn.Close()
		cp.conn = nil
		return fmt.Errorf("eth status exchange: %w", err)
	}

	if err := cp.rejectProductionNetwork(); err != nil {
		cp.Close()
		return err
	}

	cp.log.Info().
		Uint64("networkID", cp.networkID).
		Hex("genesis", cp.genesis[:]).
		Uint("ethVersion", cp.negotiatedVersion).
		Msg("peered successfully")

	return nil
}

// Close sends a devp2p disconnect message and closes the underlying TCP connection.
// It is safe to call Close on an already-closed peer.
func (cp *ChaosPeer) Close() error {
	if cp.conn == nil {
		return nil
	}
	// Best-effort disconnect message — ignore errors since we're closing anyway.
	cp.conn.SetWriteDeadline(time.Now().Add(msgTimeout))
	cp.writeRaw(discMsgCode, []byte{0xc1, 0x00}) // RLP-encoded [DiscRequested]
	// Don't nil out cp.conn — concurrent goroutines (readLoop) may still hold
	// a reference. Closing the connection causes their Read() to return an error.
	return cp.conn.Close()
}

// devp2pHandshake performs the devp2p Hello message exchange (protocol negotiation).
// We advertise eth/69 and eth/68 so Bor picks the highest version it supports.
func (cp *ChaosPeer) devp2pHandshake() error {
	pub := crypto.FromECDSAPub(&cp.key.PublicKey)[1:] // 64-byte uncompressed pubkey without prefix

	ourHello := &protoHandshake{
		Version: 5,
		Name:    "chaos-peer/1.0",
		Caps: []p2p.Cap{
			{Name: "eth", Version: 69},
			{Name: "eth", Version: 68},
		},
		ListenPort: 0,
		ID:         pub,
	}

	cp.conn.SetWriteDeadline(time.Now().Add(msgTimeout))
	payload, err := rlp.EncodeToBytes(ourHello)
	if err != nil {
		return fmt.Errorf("encode hello: %w", err)
	}
	if _, err := cp.conn.Write(handshakeMsgCode, payload); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	// Read their hello.
	cp.conn.SetReadDeadline(time.Now().Add(msgTimeout))
	code, data, _, err := cp.conn.Read()
	if err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	if code == discMsgCode {
		return fmt.Errorf("peer disconnected during hello")
	}
	if code != handshakeMsgCode {
		return fmt.Errorf("expected hello (code 0), got code %d", code)
	}

	var theirHello protoHandshake
	if err := rlp.DecodeBytes(data, &theirHello); err != nil {
		return fmt.Errorf("decode their hello: %w", err)
	}

	// Enable snappy compression if peer supports protocol version >= 5.
	if theirHello.Version >= 5 {
		cp.conn.SetSnappy(true)
	}

	// Negotiate the highest mutually supported eth version.
	cp.negotiatedVersion = negotiateEthVersion(theirHello.Caps)
	if cp.negotiatedVersion == 0 {
		return fmt.Errorf("could not negotiate eth protocol (remote caps: %v)", theirHello.Caps)
	}

	cp.log.Debug().
		Str("peer_name", theirHello.Name).
		Uint("eth_version", cp.negotiatedVersion).
		Msg("hello exchanged")

	return nil
}

// statusExchange performs the eth Status message exchange.
// We first read the remote status to learn genesis hash and network ID,
// then reply with a matching status so Bor accepts us as a peer.
func (cp *ChaosPeer) statusExchange() error {
	// Read status from target (they send first per eth protocol spec).
	for {
		cp.conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
		code, data, _, err := cp.conn.Read()
		if err != nil {
			return fmt.Errorf("read status: %w", err)
		}

		switch code {
		case discMsgCode:
			return fmt.Errorf("peer disconnected before status")

		case pingMsgCode:
			// Respond to pings to stay alive during handshake.
			cp.conn.SetWriteDeadline(time.Now().Add(msgTimeout))
			cp.writeRaw(pongMsgCode, []byte{0xc0}) // RLP empty list
			continue

		case ethProtoOffset + eth.StatusMsg:
			// Decode based on negotiated version.
			if cp.negotiatedVersion >= 69 {
				var status eth.StatusPacket69
				if err := rlp.DecodeBytes(data, &status); err != nil {
					return fmt.Errorf("decode StatusPacket69: %w", err)
				}
				cp.protoVersion = status.ProtocolVersion
				cp.networkID = status.NetworkID
				cp.genesis = status.Genesis
				cp.head = status.LatestBlockHash
				cp.td = status.TD
				cp.headBlock = status.LatestBlock
			} else {
				var status eth.StatusPacket68
				if err := rlp.DecodeBytes(data, &status); err != nil {
					return fmt.Errorf("decode StatusPacket68: %w", err)
				}
				cp.protoVersion = status.ProtocolVersion
				cp.networkID = status.NetworkID
				cp.genesis = status.Genesis
				cp.head = status.Head
				cp.td = status.TD
				cp.headBlock = 0 // eth/68 status carries no block number; TD != block number
			}

		default:
			// Ignore unexpected messages before status.
			continue
		}

		// We got the status; break out.
		break
	}

	// Send our status reply, mirroring what we learned from the remote.
	// We use a legitimate-looking status so Bor doesn't immediately drop us.
	cp.conn.SetWriteDeadline(time.Now().Add(msgTimeout))
	if err := cp.writeEthMsg(eth.StatusMsg, cp.buildReplyStatus()); err != nil {
		return fmt.Errorf("send status: %w", err)
	}

	return nil
}

// buildReplyStatus constructs our status message. We echo back the genesis,
// networkID, and ForkID from the target so we pass validation. We use a
// plausible-but-slightly-behind head so we appear as a normal syncing peer.
func (cp *ChaosPeer) buildReplyStatus() interface{} {
	if cp.negotiatedVersion >= 69 {
		return &eth.StatusPacket69{
			ProtocolVersion: uint32(cp.negotiatedVersion),
			NetworkID:       cp.networkID,
			TD:              new(big.Int).Set(cp.td),
			Genesis:         cp.genesis,
			// Use zero ForkID — Bor uses checksum-based fork ID negotiation;
			// zeros pass because we're just sending 0x00000000 which is a
			// valid "unknown" value that geth accepts from syncing nodes.
			// Real fork ID injection can be added in a follow-up.
			EarliestBlock:   0,
			LatestBlock:     cp.headBlock,
			LatestBlockHash: cp.genesis, // claim we only have genesis
		}
	}
	return &eth.StatusPacket68{
		ProtocolVersion: uint32(cp.negotiatedVersion),
		NetworkID:       cp.networkID,
		TD:              new(big.Int).Set(cp.td),
		Head:            cp.genesis,
		Genesis:         cp.genesis,
	}
}

// writeEthMsg RLP-encodes msg and writes it with the correct eth protocol offset applied.
func (cp *ChaosPeer) writeEthMsg(code uint64, msg interface{}) error {
	cp.conn.SetWriteDeadline(time.Now().Add(msgTimeout))
	payload, err := rlp.EncodeToBytes(msg)
	if err != nil {
		return fmt.Errorf("rlp encode: %w", err)
	}
	if _, err := cp.conn.Write(ethProtoOffset+code, payload); err != nil {
		return err
	}
	return nil
}

// writeRaw writes a pre-encoded payload directly with the given message code.
// Used for base-protocol messages (ping, pong, disconnect) where we control the
// exact bytes.
func (cp *ChaosPeer) writeRaw(code uint64, payload []byte) error {
	_, err := cp.conn.Write(code, payload)
	return err
}

// readLoop drains incoming messages in the background to prevent Bor from
// stalling on a full receive buffer. It runs until the context is cancelled
// or the connection is closed. Call this as a goroutine while running attacks.
func (cp *ChaosPeer) readLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		cp.conn.SetReadDeadline(time.Now().Add(msgTimeout))
		code, _, _, err := cp.conn.Read()
		if err != nil {
			return
		}
		switch code {
		case pingMsgCode:
			cp.conn.SetWriteDeadline(time.Now().Add(msgTimeout))
			cp.writeRaw(pongMsgCode, []byte{0xc0})
		case discMsgCode:
			cp.log.Info().Msg("received disconnect from target")
			return
		}
	}
}

// negotiateEthVersion returns the highest eth version <= 69 from the peer's caps.
func negotiateEthVersion(caps []p2p.Cap) uint {
	var best uint
	for _, cap := range caps {
		if cap.Name == "eth" && cap.Version <= 69 && cap.Version > best {
			best = cap.Version
		}
	}
	return best
}

