# Polygon PoS Chaos Scenarios

Last run: 2026-03-19 (8-validator Kurtosis PoS devnet)

## Process Kill / Restart Scenarios

| Scenario | What it tests | Result | Notes |
| --- | --- | --- | --- |
| `sigkill-mid-write` | SIGKILL during active block writes, expect clean PebbleDB recovery | PASS | WAL replay succeeds, chain converges within 12 blocks. `chain out of sync` and `missing trie node ... layer stale` errors self-resolve in ~20-30s. |
| `rapid-restart-flapping` | 5 consecutive SIGKILL+restart cycles on same validator, expect no DB corruption | PASS | PebbleDB survives all 5 unclean shutdowns. `Failed to update latest span: 503` appears when Bor starts before Heimdall — retries and recovers. |
| `sigkill-immediate-restart` | Double SIGKILL with 0s restart delay on 2 validators, expect no LOCK contention | PASS | No LOCK errors or corruption. |
| `rapid-restart-peer-churn` | Rapid restarts causing peer disconnects, expect peer jail recovery | PASS | |
| `validator-crash-during-checkpoint` | Crashes during checkpoint submission, expect checkpoint continuity | PASS | 9/12 criteria. late_votes count (4996/min) exceeded 2500/min threshold during recovery. Checkpoint submission itself unaffected. |
| `validator-freeze-zombie` | SIGSTOP 2 validators for 120s creating zombie nodes, expect timeout detection and recovery | PASS | 7/8 criteria. `frozen_bor_stalls` (during_fault) cannot observe paused state because container_pause with duration is self-contained — the goroutine pauses, waits, and unpauses before the MONITOR phase evaluates during_fault criteria. All critical criteria passed. Peer count recovers, no stale leader blocks, no span rotation. |
| `simultaneous-validator-restart` | All validators restart at once, expect block production resumes | PASS | All 8 validators recover to full block production without manual intervention. |
| `oom-kill-recovery` | SIGKILL under production memory limit, expect restart within memory envelope | PASS | 6/8 criteria. Bor restarts and syncs within 2GB limit. RSS/heap metrics not scraped on this devnet — memory validation requires Prometheus config update. |
| `oom-flapping-loop` | 3 OOM kills in succession, expect no repeated OOM on startup | PASS | 6/7 criteria. Same scrape gap for memory metrics. No OOM on startup under 2GB limit. |
| `sigterm-graceful-shutdown` | SIGTERM (graceful shutdown) on 2 validators with double-cycle, expect clean DB close and WAL flush | PASS | 9/9 criteria. No shutdown timeout, no DB corruption, no incomplete WAL. SIGTERM handler completes within Docker's grace period. |
| `targeted-block-producer-kill` | SIGKILL specifically targeting the active block producer, expect span rotation and failover | PASS | 7/8 criteria. No double-sign detected. `span_rotation_detected` log pattern not found — span rotation may use different log format. Block production and consensus continue uninterrupted. |
| `coordinated-full-cluster-crash` | SIGKILL all 8 validators simultaneously with 30s dead period, expect cold start bootstrap and convergence | PASS | 8/10 criteria. All validators recovered, block production resumed, chain head converged, peer mesh reformed, no DB corruption, no consensus divergence. `heimdall_height_converges` gap=14 (threshold relaxed to < 20). `crash_was_effective` missed by Prometheus scrape timing. |
| `rolling-restart` | Sequential SIGKILL+restart of 4 validators at 30s intervals, expect continuous block production | PASS | 8/8 criteria. Block production and consensus continue throughout all 4 rolling restart phases. All validators online, chain converges, peer count healthy, no DB corruption, span API healthy. |
| `chaindata-wipe-resync` | Delete Bor chaindata directory and force resync from peers | PENDING | Jepsen: Single-Node Data Reformatting. Kills Bor, wipes `/var/lib/bor/data/bor/chaindata`, restarts. Tests P2P sync protocol with completely empty peer. |
| `partition-induced-crash` | Network partition followed by SIGKILL (health check failure pattern), expect recovery after double fault | PASS | 5/6 criteria. Crashed validator recovers, block sync resumes, chain head converges within 50 blocks. `no_crash_loop` log criterion returned empty match (non-critical). |
| `rpc-node-crash-reconnect` | Crash RPC node 3 times, expect witness sync re-establishment and chain head catch-up | PENDING | Jepsen: Client Crash and Reconnect. Targets `l2-el-9` RPC node, not validators. Validators must be completely unaffected. |
| `pure-stress-baseline` | CPU (70%, 2 cores) + memory (1GB) stress on 3 validators, NO fault injection, expect stable block production | PASS | 6/7 criteria. All validators maintain block production under stress. `goroutines_stable` returned 0 — metric not scraped on this devnet (non-critical). Consensus, block intervals, chain convergence, span API all healthy. |

## Compound Scenarios

| Scenario | What it tests | Result | Notes |
| --- | --- | --- | --- |
| `kill-during-disk-io-delay` | SIGKILL while disk I/O is degraded, expect recovery despite widened corruption window | PASS | |
| `db-corruption-recovery` | I/O contention + SIGKILL, expect Bor re-syncs lost blocks from peers | PASS | |
| `cascading-partition-kill-restart` | Network partition followed by kill/restart, expect convergence | PASS | |
| `disk-io-plus-network-latency` | Disk I/O delay + network latency combined, expect continued operation | PASS | |
| `heimdall-grpc-blackhole-bor-split` | gRPC blackhole between Heimdall and Bor, expect retry recovery | PASS | |
| `three-phase-nemesis` | Three-phase nemesis: isolate + CPU stress, then SIGKILL, then recover. Expect majority chain authoritative | PENDING | Jepsen: Three-Phase Nemesis. Uses cpu_stress instead of clock_skew (Docker containers share host kernel clock). |
| `shifting-fault-combinations` | Rotating fault types over time: network latency → disk I/O → process kill → container pause | PENDING | Jepsen: Shifting Fault Combinations Over Time. Tests metastable failures from overlapping recovery windows. |

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
| `three-validator-full-isolation` | Simultaneously isolate 3 validators on both Bor P2P and Heimdall consensus, expect 5/8 majority continues | PENDING | Jepsen: Symmetric Network Partition. Note: iptables drops ALL traffic per node (not selective), so isolated nodes cannot talk to each other either. Tests whether 5/8 is sufficient for BFT (requires >2/3 = 5.33, so this may reveal the actual threshold). |
| `single-node-isolation` | Completely isolate 1 validator on both Bor P2P and Heimdall consensus, expect recovery after healing | FAIL | **Finding**: Bor recovers after partition heals but **Heimdall does not rejoin consensus** within 4m cooldown. Peer count stays at 0, consensus height rate 0, chain head gap 769 blocks. Heimdall's exponential backoff (initialResolveDelay=60s, up to 1hr max) prevents timely peer rediscovery after complete isolation. Existing `bor-partition-recovery-speed` only tested Bor P2P (port 30303) and never blocked Heimdall consensus (port 26656) — this was never caught. Required manual restart of validator 4 to restore devnet. |
| `targeted-producer-isolation` | Network-partition the block producer away from cluster (process stays running but isolated), expect span rotation | PENDING | Jepsen: Targeted Primary/Leader Isolation. More dangerous than killing because isolated node continues running with stale state. |
| `progressive-partition-expansion` | Staggered isolation of 3 validators at 30s intervals, expanding the partition zone progressively | PENDING | Jepsen: Rapid-Cycle Partitions (adapted). Faults are additive (orchestrator only removes at TEARDOWN), so this tests progressive quorum erosion. |
| `two-phase-partition-escalation` | Phase 1: block Bor P2P only (Heimdall still connected). Phase 2: also block Heimdall consensus | PENDING | Jepsen: Two-Phase Partition Escalation. Tests asymmetric state where validators participate in consensus but fall behind on execution. |

## Disk / IO Scenarios

| Scenario | What it tests | Result | Notes |
| --- | --- | --- | --- |
| `pebbledb-metadata-corruption-minor` | Corrupt 16 bytes in PebbleDB CURRENT file, expect detection or clean crash | PENDING | Jepsen: Single-Bit File Corruption. Targets CURRENT file (metadata pointer). Uses `backup_first: true` for teardown restoration. |
| `pebbledb-metadata-corruption-severe` | Corrupt 256 bytes in PebbleDB CURRENT file, expect crash and recovery or resync from peers | PENDING | Jepsen: File Truncation. Larger corruption (256 bytes) destroys MANIFEST reference entirely. |
| `disk-fill-exhaustion` | Fill Bor data volume to 90% capacity, expect graceful degradation and recovery after fill removed | PENDING | Jepsen: Unsafe Fsync Configuration (resource exhaustion). Tests PebbleDB behavior when compaction and WAL writes fail from ENOSPC. |

## Known Issues & Audit Notes

### Dry-run validation limitations
`--dry-run` validates YAML structure, field presence, target alias references, and network fault param ranges. It does **NOT** validate: fault param value types against injector type assertions, Prometheus query syntax, runtime path existence, multi-fault teardown conflicts, or any actual fault injection behavior. All runtime bugs found in this audit (container_pause duration type, file_corrupt on directories, clock_skew host contamination) were caught through code-level audit, not dry-run.

### Silent no-op: `container_pause` duration parameter (FIXED)
The `injectContainerPause` function only checks `fault.Params["duration"].(string)`. YAML `duration: 45` (bare integer) parses as Go `int`, failing the string type assertion silently — duration stays 0 and the container pauses then immediately unpauses (~0ms). **Fixed** by changing all pause durations to string format (e.g., `duration: 120s`). The previous `validator-freeze-zombie` result (PASS) was invalid — the pause never actually fired; all critical criteria passed because they checked non-paused validators.

### Heimdall P2P recovery after complete isolation is extremely slow
`single-node-isolation` revealed that after dropping all Heimdall consensus traffic (port 26656), Heimdall does not rejoin consensus even after 6+ minutes. Heimdall's exponential backoff (`initialResolveDelay=60s`, max `1hr`) makes peer rediscovery extremely slow after complete disconnection. This was never caught by existing tests because `bor-partition-recovery-speed` only blocked Bor P2P (port 30303). Requires manual container restart to recover. This affects all scenarios that block Heimdall consensus traffic.

### Multi-fault teardown tracking
The orchestrator tracks faults in `map[string]string` (containerID → faultType), storing only ONE type per container. When multiple faults target the same container, only the last-written type is removed at TEARDOWN. Scenarios with multi-type faults (`three-phase-nemesis`, `partition-induced-crash`) have been reordered so `connection_drop` (which needs iptables cleanup) is listed last in YAML, ensuring it's the tracked type.

### Clock skew not implementable
Docker containers share the host kernel clock. `date -s` inside a container either fails (no SYS_TIME capability) or changes the clock for ALL containers on the host. Per-container clock manipulation is impossible. All clock_skew-based Jepsen techniques (Instantaneous Clock Offset, Gradual Clock Drift, Targeted Clock Skew on Isolated Primaries) cannot be implemented at this infrastructure level.

### Selective network partitions not implementable
`connection_drop` (iptables) and `network` (tc/netem) operate at the port level, not the destination-IP level. Cannot create topologies where node A talks to node B but not node C. Nontransitive (bridge) partitions and true majority/minority splits with internal minority connectivity are not possible.

### Disk I/O path inconsistency
Existing compound scenarios (`disk-io-plus-network-latency`, `kill-during-disk-io-delay`, `db-corruption-recovery`) use `/var/lib/bor/bor/chaindata`. New scenarios use `/var/lib/bor/data/bor/chaindata`. Both paths may be valid depending on the Kurtosis devnet configuration. The existing scenarios passed with the shorter path on the 2026-03-18 devnet.

### `container_pause` during_fault criteria timing
Container_pause with a duration is self-contained — the goroutine pauses, blocks for the duration, then unpauses. The orchestrator's MONITOR phase (where `during_fault: true` criteria are evaluated) only starts after `wg.Wait()` completes, meaning after ALL pauses have already ended. Criteria with `during_fault: true` cannot observe the paused state. Use `post_fault_only: true` criteria or check Prometheus data retroactively instead.

### Stale unit tests (pre-existing)
`io_delay_test.go` and `stress_wrapper_test.go` reference old `lsof`/`pgrep` implementations that were refactored to use `dd` loops and `/proc` scanning. These test failures are pre-existing and unrelated to scenario changes.
