# Polygon PoS Chaos Scenarios

Last run: 2026-03-18 (8-validator Kurtosis PoS devnet)

## Process Kill / Restart Scenarios

| Scenario | What it tests | Result | Notes |
| --- | --- | --- | --- |
| `sigkill-mid-write` | SIGKILL during active block writes, expect clean PebbleDB recovery | PASS | WAL replay succeeds, chain converges within 12 blocks. `chain out of sync` and `missing trie node ... layer stale` errors self-resolve in ~20-30s. |
| `rapid-restart-flapping` | 5 consecutive SIGKILL+restart cycles on same validator, expect no DB corruption | PASS | PebbleDB survives all 5 unclean shutdowns. `Failed to update latest span: 503` appears when Bor starts before Heimdall — retries and recovers. |
| `sigkill-immediate-restart` | Double SIGKILL with 0s restart delay on 2 validators, expect no LOCK contention | PASS | No LOCK errors or corruption. |
| `rapid-restart-peer-churn` | Rapid restarts causing peer disconnects, expect peer jail recovery | PASS | |
| `validator-crash-during-checkpoint` | Crashes during checkpoint submission, expect checkpoint continuity | PASS | 9/12 criteria. late_votes count (4996/min) exceeded 2500/min threshold during recovery. Checkpoint submission itself unaffected. |
| `validator-freeze-zombie` | SIGSTOP creating zombie nodes with open TCP, expect timeout detection | PASS | |
| `simultaneous-validator-restart` | All validators restart at once, expect block production resumes | PASS | All 8 validators recover to full block production without manual intervention. |
| `oom-kill-recovery` | SIGKILL under production memory limit, expect restart within memory envelope | PASS | 6/8 criteria. Bor restarts and syncs within 2GB limit. RSS/heap metrics not scraped on this devnet — memory validation requires Prometheus config update. |
| `oom-flapping-loop` | 3 OOM kills in succession, expect no repeated OOM on startup | PASS | 6/7 criteria. Same scrape gap for memory metrics. No OOM on startup under 2GB limit. |

## Compound Scenarios

| Scenario | What it tests | Result | Notes |
| --- | --- | --- | --- |
| `kill-during-disk-io-delay` | SIGKILL while disk I/O is degraded, expect recovery despite widened corruption window | PASS | |
| `db-corruption-recovery` | I/O contention + SIGKILL, expect Bor re-syncs lost blocks from peers | PASS | |
| `cascading-partition-kill-restart` | Network partition followed by kill/restart, expect convergence | PASS | |
| `disk-io-plus-network-latency` | Disk I/O delay + network latency combined, expect continued operation | PASS | |
| `heimdall-grpc-blackhole-bor-split` | gRPC blackhole between Heimdall and Bor, expect retry recovery | PASS | |

## Network Scenarios

| Scenario | What it tests | Result | Notes |
| --- | --- | --- | --- |
| `bor-p2p-packet-corruption` | Corrupted P2P packets, expect detection and retransmission | PASS | |
| `bor-p2p-packet-reorder` | Reordered P2P packets, expect protocol handles out-of-order delivery | PASS | |
| `bor-p2p-bandwidth-throttle` | P2P bandwidth constrained, expect block propagation continues | PASS | |
| `bor-p2p-flap-with-goroutine-leak` | P2P connection flapping, expect no goroutine leak | PASS | |
| `bor-partition-recovery-speed` | Measure time to recover after partition heals | PASS | |
| `bor-dns-delay-peer-discovery` | DNS delay on peer discovery, expect eventual peer resolution | PASS | |
| `bor-witness-sync-rpc-impact` | Witness sync under RPC load, expect both continue | PASS | |
| `bor-rpc-corrupted-responses` | Corrupted RPC responses, expect client-side error handling | PASS | |
| `bor-rpc-error-injection` | HTTP error codes injected on RPC, expect retry logic works | PASS | |
| `heimdall-api-unexpected-responses` | Unexpected API response codes, expect graceful handling | PASS | |
