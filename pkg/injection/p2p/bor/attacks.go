package bor

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
)

// SendMalformedBlock constructs a NewBlockPacket with a valid-looking header
// but with a garbage (all-0xff) state root, making the block unverifiable.
// Bor should log a validation error and disconnect or ignore the block.
//
// The block number is set to height, one above the target's current head,
// so it looks like a genuine announcement of the next block.
func (cp *ChaosPeer) SendMalformedBlock(height uint64) error {
	cp.log.Info().Uint64("height", height).Msg("sending malformed block")

	// Build a header that looks superficially valid but has a poisoned state root.
	var poisonedRoot common.Hash
	for i := range poisonedRoot {
		poisonedRoot[i] = 0xff
	}

	// Random parent hash to make Bor chase its tail trying to insert it.
	var parentHash common.Hash
	rand.Read(parentHash[:])

	header := &types.Header{
		ParentHash:  parentHash,
		UncleHash:   types.EmptyUncleHash,
		Coinbase:    common.Address{},
		Root:        poisonedRoot, // invalid state root
		TxHash:      types.EmptyTxsHash,
		ReceiptHash: types.EmptyReceiptsHash,
		Bloom:       types.Bloom{},
		Difficulty:  big.NewInt(1),
		Number:      new(big.Int).SetUint64(height),
		GasLimit:    30_000_000,
		GasUsed:     0,
		Time:        uint64(time.Now().Unix()),
		Extra:       make([]byte, 0),
	}

	block := types.NewBlockWithHeader(header)
	packet := &eth.NewBlockPacket{
		Block: block,
		// TD must be <= 100 bits to pass sanityCheck in some geth versions,
		// but large enough to look like it's ahead of the target.
		TD: new(big.Int).Add(cp.td, big.NewInt(1)),
	}

	if err := cp.writeEthMsg(eth.NewBlockMsg, packet); err != nil {
		return fmt.Errorf("send malformed block at height %d: %w", height, err)
	}
	return nil
}

// SendConflictingChain announces a sequence of block hashes that conflict with
// the canonical chain starting at forkBlock. Each announced hash is random,
// so Bor will attempt to fetch the headers (GetBlockHeaders request) and fail.
// This stresses the block fetcher and may cause header sync to stall.
func (cp *ChaosPeer) SendConflictingChain(forkBlock uint64) error {
	const announceCount = 10
	cp.log.Info().
		Uint64("fork_block", forkBlock).
		Int("count", announceCount).
		Msg("sending conflicting chain announcement")

	hashes := make(eth.NewBlockHashesPacket, announceCount)
	for i := range hashes {
		rand.Read(hashes[i].Hash[:])
		hashes[i].Number = forkBlock + uint64(i)
	}

	if err := cp.writeEthMsg(eth.NewBlockHashesMsg, hashes); err != nil {
		return fmt.Errorf("send conflicting chain from block %d: %w", forkBlock, err)
	}
	return nil
}

// SendInvalidTransactions sends a TransactionsPacket containing count malformed
// transactions. Each transaction has an invalid signature (random v/r/s bytes) and
// an impossibly high gas limit. Bor should reject them in the tx pool.
//
// This tests the mempool's input validation and whether invalid tx processing
// causes goroutine leaks or panics in the tx processing path.
func (cp *ChaosPeer) SendInvalidTransactions(count int) error {
	cp.log.Info().Int("count", count).Msg("sending invalid transactions")

	txs := make(eth.TransactionsPacket, count)
	for i := 0; i < count; i++ {
		// Create a legacy tx with nonsense values.
		// The signature (V, R, S) is random bytes — signature verification will fail.
		var r, s [32]byte
		rand.Read(r[:])
		rand.Read(s[:])

		inner := &types.LegacyTx{
			Nonce:    uint64(i),
			GasPrice: big.NewInt(1_000_000_000),
			Gas:      ^uint64(0), // max uint64 gas — impossibly high
			To:       nil,        // contract creation
			Value:    big.NewInt(0),
			Data:     []byte{0xde, 0xad, 0xbe, 0xef},
			// V=27 with garbage R and S — won't pass ecrecover
			V: big.NewInt(27),
			R: new(big.Int).SetBytes(r[:]),
			S: new(big.Int).SetBytes(s[:]),
		}
		txs[i] = types.NewTx(inner)
	}

	if err := cp.writeEthMsg(eth.TransactionsMsg, txs); err != nil {
		return fmt.Errorf("send %d invalid txs: %w", count, err)
	}
	return nil
}

// SendMaliciousStatus sends a second StatusPacket with an astronomically large
// total difficulty (2048 random bytes). Per the eth protocol spec, peers that
// receive a status after the initial handshake should disconnect.
//
// This tests Bor's duplicate-status handling and whether the oversized TD
// integer triggers integer overflow or memory allocation issues.
func (cp *ChaosPeer) SendMaliciousStatus() error {
	cp.log.Info().Msg("sending malicious status with giant TD")

	// 2048 bytes of random data as TD — 16384 bits, far beyond the 100-bit limit
	// that sanityCheck enforces on NewBlockPacket. StatusPacket has no such check,
	// making this a useful probe for integer handling in the status processor.
	tdBytes := make([]byte, 2048)
	rand.Read(tdBytes)
	hugeTD := new(big.Int).SetBytes(tdBytes)

	if cp.negotiatedVersion >= 69 {
		status := &eth.StatusPacket69{
			ProtocolVersion: uint32(cp.negotiatedVersion),
			NetworkID:       cp.networkID,
			TD:              hugeTD,
			Genesis:         cp.genesis,
			EarliestBlock:   0,
			LatestBlock:     ^uint64(0), // max uint64
			LatestBlockHash: cp.head,
		}
		if err := cp.writeEthMsg(eth.StatusMsg, status); err != nil {
			return fmt.Errorf("send malicious status (eth69): %w", err)
		}
	} else {
		status := &eth.StatusPacket68{
			ProtocolVersion: uint32(cp.negotiatedVersion),
			NetworkID:       cp.networkID,
			TD:              hugeTD,
			Head:            cp.head,
			Genesis:         cp.genesis,
		}
		if err := cp.writeEthMsg(eth.StatusMsg, status); err != nil {
			return fmt.Errorf("send malicious status (eth68): %w", err)
		}
	}

	return nil
}

// SendInvalidBlockRange sends a BlockRangeUpdatePacket (eth/69 message 0x11)
// where EarliestBlock > LatestBlock. Per Bor's protocol handler, this is an
// invalid state and should trigger a disconnect (DiscReason "invalid block range").
//
// Ref: bor/eth/protocols/eth/protocol.go errInvalidBlockRange
//      bor/cmd/devp2p/internal/ethtest/suite.go TestBlockRangeUpdateInvalid
func (cp *ChaosPeer) SendInvalidBlockRange() error {
	cp.log.Info().Msg("sending invalid block range update (earliest > latest)")

	packet := &eth.BlockRangeUpdatePacket{
		EarliestBlock:   1_000_000,
		LatestBlock:     1,     // latest < earliest — invalid
		LatestBlockHash: cp.head,
	}

	if err := cp.writeEthMsg(eth.BlockRangeUpdateMsg, packet); err != nil {
		return fmt.Errorf("send invalid block range: %w", err)
	}
	return nil
}

// FloodNewBlockHashes rapidly sends count NewBlockHashesPacket messages, each
// announcing a batch of random block hashes just ahead of the target's head.
// This stress-tests Bor's block announcement rate-limiting and the block fetcher
// queue. Each message announces 10 hashes to maximise the announcement backlog.
func (cp *ChaosPeer) FloodNewBlockHashes(ctx context.Context, count int) error {
	cp.log.Info().Int("count", count).Msg("starting new block hash flood")

	// Start a background reader to drain Bor's responses (GetBlockHeaders
	// requests) so the connection stays alive during the flood.
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go cp.readLoop(readCtx)

	const batchSize = 10
	// Use the head block number from the status exchange as the flood base.
	// headBlock is populated from LatestBlock (eth/69) or left as 0 (eth/68),
	// so it always reflects the actual chain height rather than TD.
	headNum := cp.headBlock

	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		hashes := make(eth.NewBlockHashesPacket, batchSize)
		for j := range hashes {
			rand.Read(hashes[j].Hash[:])
			hashes[j].Number = headNum + uint64(i*batchSize+j+1)
		}

		if err := cp.writeEthMsg(eth.NewBlockHashesMsg, hashes); err != nil {
			// Log but don't abort — Bor may have disconnected us, which is
			// itself a valid test outcome (rate limiting triggered).
			cp.log.Warn().
				Int("sent", i).
				Err(err).
				Msg("write error during flood — peer may have disconnected")
			return fmt.Errorf("flood aborted after %d messages: %w", i, err)
		}
	}

	cp.log.Info().Int("total", count).Msg("flood complete")
	return nil
}

// SendGetBlockHeadersFlood sends count GetBlockHeaders requests for nonexistent
// blocks, exercising Bor's request handler under load. It mirrors
// TestGetNonexistentBlockHeaders from the bor devp2p test suite.
func (cp *ChaosPeer) SendGetBlockHeadersFlood(ctx context.Context, count int) error {
	cp.log.Info().Int("count", count).Msg("flooding with nonexistent GetBlockHeaders")

	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req := &eth.GetBlockHeadersPacket{
			RequestId: uint64(i),
			GetBlockHeadersRequest: &eth.GetBlockHeadersRequest{
				Origin:  eth.HashOrNumber{Number: ^uint64(0)}, // max uint64, nonexistent
				Amount:  1,
				Skip:    0,
				Reverse: false,
			},
		}
		if err := cp.writeEthMsg(eth.GetBlockHeadersMsg, req); err != nil {
			return fmt.Errorf("GetBlockHeaders flood aborted after %d: %w", i, err)
		}
	}

	return nil
}
