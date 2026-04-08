// chaos-peer is a standalone CLI tool for running P2P chaos attacks against
// Bor devp2p nodes. It connects directly to a target Bor node via RLPx,
// completes the eth handshake, and then sends malicious protocol messages.
//
// Usage:
//
//	chaos-peer --target enode://...@host:30303 --attack malformed-block --count 10
//	chaos-peer --target enode://...@host:30303 --attack flood-hashes --count 1000
//	chaos-peer --rpc http://bor:8545 --attack invalid-range
//
// The --rpc flag can be used instead of --target: the tool will call
// admin_nodeInfo on the given Bor RPC endpoint to discover the enode URL.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/injection/p2p/bor"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

const (
	attackMalformedBlock    = "malformed-block"
	attackConflictingChain  = "conflicting-chain"
	attackInvalidTxs        = "invalid-txs"
	attackMaliciousStatus   = "malicious-status"
	attackInvalidRange      = "invalid-range"
	attackFloodHashes       = "flood-hashes"
	attackHeaderFlood       = "header-flood"
)

var (
	flagTarget          string
	flagRPC             string
	flagAttack          string
	flagCount           int
	flagForkBlock       uint64
	flagDuration        time.Duration
	flagVerbose         bool
	flagAllowProduction bool
)

func main() {
	root := &cobra.Command{
		Use:   "chaos-peer",
		Short: "Bor devp2p chaos peer for P2P fault injection",
		Long: `chaos-peer connects to a Bor node's devp2p port and sends crafted malicious
Ethereum protocol messages. It is Phase 3 of chaos-utils semantic fault injection.

Unlike HTTP-level faults, P2P attacks bypass any proxy and reach Bor's
internal block fetcher, transaction pool, and protocol handler directly.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          run,
	}

	root.Flags().StringVar(&flagTarget, "target", "", "enode URL of the target Bor node (enode://...@host:port)")
	root.Flags().StringVar(&flagRPC, "rpc", "", "Bor JSON-RPC URL to auto-discover enode (e.g. http://host:8545)")
	root.Flags().StringVar(&flagAttack, "attack", attackMalformedBlock,
		fmt.Sprintf("attack type: %s | %s | %s | %s | %s | %s | %s",
			attackMalformedBlock, attackConflictingChain, attackInvalidTxs,
			attackMaliciousStatus, attackInvalidRange, attackFloodHashes, attackHeaderFlood))
	root.Flags().IntVar(&flagCount, "count", 1, "number of malicious messages to send")
	root.Flags().Uint64Var(&flagForkBlock, "fork-block", 100, "fork block height for conflicting-chain attack")
	root.Flags().DurationVar(&flagDuration, "duration", 30*time.Second, "how long to run the attack before disconnecting")
	root.Flags().BoolVarP(&flagVerbose, "verbose", "v", false, "enable debug logging")
	root.Flags().BoolVar(&flagAllowProduction, "allow-production", false, "allow attacking known production networks (use with extreme caution)")

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, _ []string) error {
	// Configure logger.
	level := zerolog.InfoLevel
	if flagVerbose {
		level = zerolog.DebugLevel
	}
	log := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}).
		Level(level).
		With().
		Timestamp().
		Logger()

	// Resolve target enode URL.
	target, err := resolveTarget(cmd.Context(), log)
	if err != nil {
		return fmt.Errorf("resolve target: %w", err)
	}
	log.Info().Str("target", target).Str("attack", flagAttack).Msg("starting chaos-peer")

	// Connect to the target.
	peer, err := bor.NewChaosPeer(log)
	if err != nil {
		return fmt.Errorf("create peer: %w", err)
	}
	peer.AllowProduction = flagAllowProduction

	if err := peer.Connect(target); err != nil {
		return fmt.Errorf("connect to %s: %w", target, err)
	}
	log.Info().Msg("connected and peered")
	defer peer.Close()

	// Set up a context with timeout and signal handling.
	ctx, cancel := context.WithTimeout(cmd.Context(), flagDuration)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			log.Info().Msg("received signal, stopping")
			cancel()
		case <-ctx.Done():
		}
	}()

	// Execute the chosen attack.
	if err := executeAttack(ctx, peer, log); err != nil {
		// A disconnect from the target is expected for some attacks — log it
		// but exit 0 because "Bor disconnected the malicious peer" is a valid
		// and good outcome.
		log.Warn().Err(err).Msg("attack ended (peer may have disconnected)")
	}

	log.Info().Str("attack", flagAttack).Msg("chaos-peer finished")
	return nil
}

// resolveTarget returns the enode URL to attack, either from --target directly
// or by querying admin_nodeInfo via --rpc.
func resolveTarget(ctx context.Context, log zerolog.Logger) (string, error) {
	if flagTarget != "" {
		return flagTarget, nil
	}
	if flagRPC != "" {
		log.Info().Str("rpc", flagRPC).Msg("discovering enode via admin_nodeInfo")
		enodeURL, err := bor.DiscoverSingleEnode(ctx, flagRPC)
		if err != nil {
			return "", fmt.Errorf("admin_nodeInfo from %s: %w", flagRPC, err)
		}
		log.Info().Str("enode", enodeURL).Msg("discovered enode")
		return enodeURL, nil
	}
	return "", fmt.Errorf("one of --target or --rpc is required")
}

// executeAttack dispatches to the appropriate attack method on the peer.
func executeAttack(ctx context.Context, peer *bor.ChaosPeer, log zerolog.Logger) error {
	switch flagAttack {
	case attackMalformedBlock:
		return runRepeat(ctx, log, flagCount, func(i int) error {
			return peer.SendMalformedBlock(uint64(i + 1))
		})

	case attackConflictingChain:
		return runRepeat(ctx, log, flagCount, func(_ int) error {
			return peer.SendConflictingChain(flagForkBlock)
		})

	case attackInvalidTxs:
		return peer.SendInvalidTransactions(flagCount)

	case attackMaliciousStatus:
		return runRepeat(ctx, log, flagCount, func(_ int) error {
			return peer.SendMaliciousStatus()
		})

	case attackInvalidRange:
		return runRepeat(ctx, log, flagCount, func(_ int) error {
			return peer.SendInvalidBlockRange()
		})

	case attackFloodHashes:
		return peer.FloodNewBlockHashes(ctx, flagCount)

	case attackHeaderFlood:
		return peer.SendGetBlockHeadersFlood(ctx, flagCount)

	default:
		return fmt.Errorf("unknown attack type: %q (valid: %s|%s|%s|%s|%s|%s|%s)",
			flagAttack,
			attackMalformedBlock, attackConflictingChain, attackInvalidTxs,
			attackMaliciousStatus, attackInvalidRange, attackFloodHashes, attackHeaderFlood)
	}
}

// runRepeat calls fn count times, stopping early if ctx is cancelled or fn
// returns an error. A 10ms pause between iterations avoids sending at line-rate.
func runRepeat(ctx context.Context, log zerolog.Logger, count int, fn func(int) error) error {
	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			log.Info().Int("sent", i).Msg("stopped by context")
			return ctx.Err()
		default:
		}
		if err := fn(i); err != nil {
			return fmt.Errorf("iteration %d: %w", i, err)
		}
		log.Debug().Int("iteration", i+1).Int("total", count).Msg("sent")
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}
