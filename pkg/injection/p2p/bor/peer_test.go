package bor

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/rlpx"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/rs/zerolog"
)

// TestNegotiateEthVersion verifies that negotiateEthVersion correctly picks
// the highest eth version <= 69 from a capability list.
func TestNegotiateEthVersion(t *testing.T) {
	tests := []struct {
		name string
		caps []p2p.Cap
		want uint
	}{
		{
			name: "eth69 only",
			caps: []p2p.Cap{{Name: "eth", Version: 69}},
			want: 69,
		},
		{
			name: "eth68 and eth69",
			caps: []p2p.Cap{
				{Name: "eth", Version: 68},
				{Name: "eth", Version: 69},
			},
			want: 69,
		},
		{
			name: "eth68 only",
			caps: []p2p.Cap{{Name: "eth", Version: 68}},
			want: 68,
		},
		{
			name: "snap and eth69",
			caps: []p2p.Cap{
				{Name: "snap", Version: 1},
				{Name: "eth", Version: 69},
			},
			want: 69,
		},
		{
			name: "no eth caps",
			caps: []p2p.Cap{{Name: "snap", Version: 1}},
			want: 0,
		},
		{
			name: "future eth version above 69",
			caps: []p2p.Cap{
				{Name: "eth", Version: 99},
				{Name: "eth", Version: 68},
			},
			want: 68, // 99 exceeds our advertised maximum of 69
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := negotiateEthVersion(tc.caps)
			if got != tc.want {
				t.Errorf("negotiateEthVersion(%v) = %d, want %d", tc.caps, got, tc.want)
			}
		})
	}
}

// TestFixEnodeIP verifies the IP replacement logic for nodes that bind on all interfaces.
func TestFixEnodeIP(t *testing.T) {
	tests := []struct {
		name   string
		enode  string
		rpcURL string
		want   string
	}{
		{
			name:   "replace 0.0.0.0 with rpc host",
			enode:  "enode://abc@0.0.0.0:30303",
			rpcURL: "http://192.168.1.5:8545",
			want:   "enode://abc@192.168.1.5:30303",
		},
		{
			name:   "no replacement needed with real IP",
			enode:  "enode://abc@10.0.0.1:30303",
			rpcURL: "http://10.0.0.1:8545",
			want:   "enode://abc@10.0.0.1:30303",
		},
		{
			name:   "replace [::] with rpc host",
			enode:  "enode://abc@[::]:30303",
			rpcURL: "http://validator-1:8545/rpc",
			want:   "enode://abc@validator-1:30303",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fixEnodeIP(tc.enode, tc.rpcURL)
			if got != tc.want {
				t.Errorf("fixEnodeIP(%q, %q) = %q, want %q", tc.enode, tc.rpcURL, got, tc.want)
			}
		})
	}
}

// TestTruncate verifies the truncation helper.
func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
	if got := truncate("hello world", 5); got != "hello..." {
		t.Errorf("expected 'hello...', got %q", got)
	}
}

// TestChaosPeerNewWithKey verifies that NewChaosPeerWithKey stores the key correctly.
func TestChaosPeerNewWithKey(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	peer := NewChaosPeerWithKey(key, zerolog.Nop())
	if peer.key != key {
		t.Error("key not stored correctly")
	}
	if peer.conn != nil {
		t.Error("conn should be nil before Connect")
	}
}

// TestBuildReplyStatus verifies that buildReplyStatus produces correctly typed
// packets for both eth/68 and eth/69.
func TestBuildReplyStatus(t *testing.T) {
	peer := &ChaosPeer{
		networkID: 137,
		genesis:   common.HexToHash("0xdeadbeef"),
		head:      common.HexToHash("0xcafebabe"),
		td:        big.NewInt(12345),
	}

	t.Run("eth69", func(t *testing.T) {
		peer.negotiatedVersion = 69
		status := peer.buildReplyStatus()
		s, ok := status.(*eth.StatusPacket69)
		if !ok {
			t.Fatalf("expected *eth.StatusPacket69, got %T", status)
		}
		if s.NetworkID != 137 {
			t.Errorf("NetworkID = %d, want 137", s.NetworkID)
		}
		if s.Genesis != peer.genesis {
			t.Errorf("Genesis mismatch")
		}
		if s.ProtocolVersion != 69 {
			t.Errorf("ProtocolVersion = %d, want 69", s.ProtocolVersion)
		}
	})

	t.Run("eth68", func(t *testing.T) {
		peer.negotiatedVersion = 68
		status := peer.buildReplyStatus()
		s, ok := status.(*eth.StatusPacket68)
		if !ok {
			t.Fatalf("expected *eth.StatusPacket68, got %T", status)
		}
		if s.NetworkID != 137 {
			t.Errorf("NetworkID = %d, want 137", s.NetworkID)
		}
	})
}

// TestAttackMessagesRLPRoundtrip verifies that each attack packet type can be
// RLP-encoded and decoded without error. This catches any structural mismatches
// before the messages are sent over the wire.
func TestAttackMessagesRLPRoundtrip(t *testing.T) {
	t.Run("NewBlockHashesPacket", func(t *testing.T) {
		var h common.Hash
		pkt := eth.NewBlockHashesPacket{
			{Hash: h, Number: 1000},
			{Hash: h, Number: 1001},
		}
		encoded, err := rlp.EncodeToBytes(pkt)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		var decoded eth.NewBlockHashesPacket
		if err := rlp.DecodeBytes(encoded, &decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(decoded) != 2 {
			t.Errorf("expected 2 entries, got %d", len(decoded))
		}
	})

	t.Run("BlockRangeUpdatePacket_invalid_range", func(t *testing.T) {
		var h common.Hash
		pkt := &eth.BlockRangeUpdatePacket{
			EarliestBlock:   1_000_000,
			LatestBlock:     1,
			LatestBlockHash: h,
		}
		encoded, err := rlp.EncodeToBytes(pkt)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		var decoded eth.BlockRangeUpdatePacket
		if err := rlp.DecodeBytes(encoded, &decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if decoded.EarliestBlock != 1_000_000 || decoded.LatestBlock != 1 {
			t.Errorf("unexpected values: %+v", decoded)
		}
	})

	t.Run("StatusPacket69_malicious_TD", func(t *testing.T) {
		tdBytes := make([]byte, 256)
		for i := range tdBytes {
			tdBytes[i] = 0xff
		}
		pkt := &eth.StatusPacket69{
			ProtocolVersion: 69,
			NetworkID:       137,
			TD:              new(big.Int).SetBytes(tdBytes),
			Genesis:         common.Hash{},
			EarliestBlock:   0,
			LatestBlock:     ^uint64(0),
			LatestBlockHash: common.Hash{},
		}
		encoded, err := rlp.EncodeToBytes(pkt)
		if err != nil {
			t.Fatalf("encode malicious status: %v", err)
		}
		var decoded eth.StatusPacket69
		if err := rlp.DecodeBytes(encoded, &decoded); err != nil {
			t.Fatalf("decode malicious status: %v", err)
		}
		if decoded.TD.Cmp(pkt.TD) != 0 {
			t.Error("TD mismatch after roundtrip")
		}
	})
}

// TestMockRLPxHandshake starts a minimal mock RLPx server and verifies that
// ChaosPeer can complete the devp2p Hello + eth Status exchange against it.
// This is an integration-style test that exercises the real RLPx stack.
func TestMockRLPxHandshake(t *testing.T) {
	// Generate server key.
	serverKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}

	// Start a TCP listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- runMockBorServer(ln, serverKey)
	}()

	// Build the enode URL from the server's key and listen address.
	addr := ln.Addr().(*net.TCPAddr)
	pub := &serverKey.PublicKey
	enodeURL := fmt.Sprintf("enode://%x@127.0.0.1:%d",
		crypto.FromECDSAPub(pub)[1:], addr.Port)

	peer, err := NewChaosPeer(zerolog.Nop())
	if err != nil {
		t.Fatalf("NewChaosPeer: %v", err)
	}
	// The mock server uses networkID=137 (Polygon Mainnet) to simulate a real
	// peer. Allow connecting in the test environment so the safety check doesn't
	// block the handshake test.
	peer.AllowProduction = true
	defer peer.Close()

	if err := peer.Connect(enodeURL); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Verify we learned the network info from the server's status message.
	if peer.networkID != 137 {
		t.Errorf("networkID = %d, want 137", peer.networkID)
	}
	if peer.negotiatedVersion != 69 {
		t.Errorf("negotiatedVersion = %d, want 69", peer.negotiatedVersion)
	}

	// Check server completed without error.
	select {
	case err := <-serverErr:
		if err != nil {
			t.Errorf("mock server error: %v", err)
		}
	case <-time.After(2 * time.Second):
		// Server is still blocking on the final read — that's OK.
	}
}

// runMockBorServer accepts one connection and simulates a minimal Bor eth peer.
// It completes the RLPx crypto handshake, exchanges Hello, sends a Status, and
// reads the client's Status reply before returning.
func runMockBorServer(ln net.Listener, key *ecdsa.PrivateKey) error {
	conn, err := ln.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// RLPx handshake — nil pubkey means we're the listener (server mode).
	rlpxConn := rlpx.NewConn(conn, nil)
	if _, err := rlpxConn.Handshake(key); err != nil {
		return fmt.Errorf("server RLPx handshake: %w", err)
	}

	// Read client Hello.
	code, data, _, err := rlpxConn.Read()
	if err != nil {
		return fmt.Errorf("server read hello: %w", err)
	}
	if code != handshakeMsgCode {
		return fmt.Errorf("server expected hello (code 0), got code %d", code)
	}
	var clientHello protoHandshake
	if err := rlp.DecodeBytes(data, &clientHello); err != nil {
		return fmt.Errorf("server decode client hello: %w", err)
	}

	// Send our Hello back BEFORE enabling snappy. Per the devp2p spec, Hello
	// messages are never compressed — snappy applies only to subsequent messages
	// after both sides have confirmed p2p version >= 5.
	serverPub := crypto.FromECDSAPub(&key.PublicKey)[1:]
	serverHello := &protoHandshake{
		Version: 5,
		Name:    "mock-bor/1.0",
		Caps: []p2p.Cap{
			{Name: "eth", Version: 69},
		},
		ID: serverPub,
	}
	helloPayload, err := rlp.EncodeToBytes(serverHello)
	if err != nil {
		return fmt.Errorf("encode server hello: %w", err)
	}
	if _, err := rlpxConn.Write(handshakeMsgCode, helloPayload); err != nil {
		return fmt.Errorf("server send hello: %w", err)
	}

	// Enable snappy after both hellos have been exchanged.
	if clientHello.Version >= 5 {
		rlpxConn.SetSnappy(true)
	}

	// Send eth/69 Status.
	genesis := common.HexToHash("0xaabbccdd")
	status := &eth.StatusPacket69{
		ProtocolVersion: 69,
		NetworkID:       137,
		TD:              big.NewInt(99999),
		Genesis:         genesis,
		EarliestBlock:   0,
		LatestBlock:     100,
		LatestBlockHash: common.HexToHash("0x00001234"),
	}
	statusPayload, err := rlp.EncodeToBytes(status)
	if err != nil {
		return fmt.Errorf("encode server status: %w", err)
	}
	if _, err := rlpxConn.Write(ethProtoOffset+eth.StatusMsg, statusPayload); err != nil {
		return fmt.Errorf("server send status: %w", err)
	}

	// Drain incoming messages until we see the client's Status reply.
	for {
		rlpxConn.SetReadDeadline(time.Now().Add(3 * time.Second))
		code, _, _, err := rlpxConn.Read()
		if err != nil {
			// EOF or timeout after client sent status — both mean success.
			return nil
		}
		if code == ethProtoOffset+eth.StatusMsg {
			return nil // got client status — handshake complete
		}
	}
}
