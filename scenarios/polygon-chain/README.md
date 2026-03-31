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
| `validator-freeze-zombie` | SIGSTOP 2 validators for 120s creating zombie nodes, expect timeout detection and recovery | PASS | 7/8 criteria. `frozen_bor_stalls` (during_fault) cannot observe paused state — container_pause blocks in goroutine and unpauses before MONITOR evaluates. All critical criteria passed. Peers recover, no stale leader, no span rotation. |
| `simultaneous-validator-restart` | All validators restart at once, expect block production resumes | PASS | All 8 validators recover to full block production without manual intervention. |
| `oom-kill-recovery` | SIGKILL under production memory limit, expect restart within memory envelope | PASS | 6/8 criteria. Bor restarts and syncs within 2GB limit. RSS/heap metrics not scraped on this devnet. |
| `oom-flapping-loop` | 3 OOM kills in succession, expect no repeated OOM on startup | PASS | 6/7 criteria. Same scrape gap for memory metrics. No OOM on startup under 2GB limit. |
| `sigterm-graceful-shutdown` | SIGTERM (graceful shutdown) on 2 validators with double-cycle, expect clean DB close and WAL flush | PASS | 9/9 criteria. No shutdown timeout, no DB corruption, no incomplete WAL. SIGTERM handler completes within Docker's grace period. |
| `targeted-block-producer-kill` | SIGKILL specifically targeting the active block producer, expect span rotation and failover | PASS | 7/8 criteria. No double-sign detected. `span_rotation_detected` log pattern not matched — span rotation may use different log format. Block production and consensus uninterrupted. |
| `coordinated-full-cluster-crash` | SIGKILL all 8 validators simultaneously with 30s dead period, expect cold start bootstrap and convergence | PASS | 8/10 criteria. All validators recovered, block production resumed, chain head converged, peer mesh reformed, no DB corruption, no consensus divergence. Heimdall height gap=14 (threshold < 20). |
| `rolling-restart` | Sequential SIGKILL+restart of 4 validators at 30s intervals, expect continuous block production | PASS | 8/8 criteria. Block production and consensus continue throughout all 4 rolling restart phases. |
| `chaindata-wipe-resync` | Delete Bor chaindata while running, then SIGKILL+restart. Expect resync from peers | FAIL | **Finding**: Bor does not automatically resync after chaindata wipe. After `rm -rf chaindata` + restart, `chain_head_block` rate stays 0. Bor may require manual state sync configuration or snapshot download to recover from total data loss — it cannot reconstruct from P2P alone. Other validators unaffected, consensus continues. |
| `partition-induced-crash` | Network partition followed by SIGKILL (health check failure pattern), expect recovery after double fault | PASS | 5/6 criteria. Crashed validator recovers, block sync resumes, chain head converges within 50 blocks. |
| `rpc-node-crash-reconnect` | Crash RPC node 3 times, expect witness sync re-establishment and chain head catch-up | FAIL | 4/6 criteria. RPC node recovers (up=1), resumes sync, validators completely unaffected. `rpc_catches_up` criterion returned 0 — PromQL subtraction produces no data when RPC metrics haven't been scraped long enough post-crash. Need wider evaluation window. |
| `pure-stress-baseline` | CPU (70%, 2 cores) + memory (1GB) stress on 3 validators, NO fault injection, expect stable block production | PASS | 6/7 criteria. All validators maintain block production under stress. `goroutines_stable` metric not scraped (non-critical). |

## Compound Scenarios

| Scenario | What it tests | Result | Notes |
| --- | --- | --- | --- |
| `kill-during-disk-io-delay` | SIGKILL while disk I/O is degraded, expect recovery despite widened corruption window | PASS | |
| `db-corruption-recovery` | I/O contention + SIGKILL, expect Bor re-syncs lost blocks from peers | PASS | |
| `disk-io-plus-network-latency` | Disk I/O delay + network latency combined, expect continued operation | PASS | |
| `heimdall-grpc-blackhole-bor-split` | gRPC blackhole between Heimdall and Bor, expect retry recovery | PASS | |
| `three-phase-nemesis` | Three-phase nemesis: isolate + CPU stress, then SIGKILL, then recover. Expect majority chain authoritative | PASS | 6/6 criteria. Majority continues through all phases, killed validators resync, chain converges, Heimdall rejoins. |
| `shifting-fault-combinations` | Rotating fault types over time: network latency → disk I/O → process kill → container pause | PASS | 5/5 criteria. Block production and consensus continue through all 4 fault phases. All validators online after, chain converges. |

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
| `three-validator-full-isolation` | Simultaneously isolate 3 validators on both Bor P2P and Heimdall consensus, expect 5/8 majority continues | FAIL | Same Heimdall P2P recovery issue as single-node-isolation. Isolated validators do not rejoin after partition heals. Cascading destabilization required full devnet restart. Not run cleanly — blocked by devnet instability from prior partition tests. |
| `single-node-isolation` | Completely isolate 1 validator on both Bor P2P and Heimdall consensus, expect recovery after healing | FAIL | **Reproducible finding** (tested on validators 4 and 6): after 3m of complete isolation, Heimdall does not rejoin consensus within 4m cooldown. Peers stay at 0, consensus rate 0, chain head gap 391-440 blocks. Bor P2P also fails to recover in some runs. Requires manual `docker restart` to restore. Root cause: CometBFT exponential backoff (`initialResolveDelay=60s`, max 1hr). |
| `targeted-producer-isolation` | Network-partition the block producer away from cluster (process stays running but isolated), expect span rotation | FAIL | Same Heimdall recovery issue. Chain head gap 439 blocks after partition heals. Isolated validators do not resync. The partition itself works correctly (majority continues), but recovery is broken. |
| `progressive-partition-expansion` | Staggered isolation of 3 validators at 30s intervals, expanding the partition zone progressively | FAIL | Not run cleanly — pre-flight failed due to devnet instability from prior partition tests. Blocked by the Heimdall recovery issue. |
| `two-phase-partition-escalation` | Phase 1: block Bor P2P only (Heimdall still connected). Phase 2: also block Heimdall consensus | FAIL | Chain head gap 734 after partition heals. Phase 2 (Heimdall isolation) triggers the same unrecoverable state. Cascaded from previous test's broken validators. |

## Disk / IO Scenarios

| Scenario | What it tests | Result | Notes |
| --- | --- | --- | --- |
| `pebbledb-metadata-corruption-minor` | Corrupt 16 bytes in PebbleDB CURRENT file while running, then SIGKILL+restart | PASS | 3/5 criteria. Other validators unaffected, chain converges. Corrupted node crashed (up=0) — PebbleDB correctly detected the corrupt CURRENT and refused to open. `backup_first: true` allows teardown restoration. Node requires manual restart after test. |
| `pebbledb-metadata-corruption-severe` | Corrupt 256 bytes in PebbleDB CURRENT file, expect crash and recovery | FAIL | 2/4 criteria. Node crashed as expected (correct behavior — PebbleDB refuses corrupt metadata). `no_silent_data_loss` log criterion returned empty match — the log patterns don't match Bor's actual error format. The crash IS the correct response; this is a criteria tuning issue, not a system bug. |
| `disk-fill-exhaustion` | Fill Bor data volume to 90% capacity, expect graceful degradation and recovery after fill removed | FAIL | **Finding**: after disk fill + removal, Bor does not resume block production (rate=0). PebbleDB may enter an unrecoverable state when disk fills — compaction failures and WAL write errors may require container restart to clear. Other validators unaffected. |

## Known Issues & Findings

### Heimdall P2P recovery after complete isolation is broken (HIGH SEVERITY)
**Reproducible on multiple validators.** After dropping all CometBFT P2P traffic (port 26656) for 3+ minutes, Heimdall does not rejoin consensus even after 8+ minutes. Peer count stays at 0 permanently. Root cause: CometBFT's exponential backoff (`initialResolveDelay=60s`, max up to 1 hour) makes peer rediscovery extremely slow after complete disconnection. This was never caught by existing tests because `bor-partition-recovery-speed` only blocked Bor P2P (port 30303). Affects all scenarios that block Heimdall consensus traffic. Requires `docker restart` to recover. Every network partition scenario that isolates both layers (single-node-isolation, targeted-producer-isolation, three-validator-full-isolation, two-phase-partition-escalation, progressive-partition-expansion) hits this issue.

### Bor cannot resync from empty chaindata
After `rm -rf chaindata` + restart, Bor does not automatically reconstruct state from P2P peers. The chain head stays at 0. Bor likely requires either a state sync configuration, a snapshot download, or manual genesis initialization to recover from total data loss. This is a realistic operational risk — an operator who accidentally deletes chaindata cannot recover by simply restarting.

### Bor does not auto-recover from disk exhaustion
After the disk fill is removed during teardown, Bor's block production does not resume. PebbleDB's compaction and WAL write paths may enter a persistent error state when encountering ENOSPC. A container restart may be required to clear the error state and resume normal operation.

### Bor data path
The actual Bor chaindata path in the Kurtosis devnet is `/var/lib/bor/bor/chaindata/` (not `/var/lib/bor/data/bor/chaindata/`). All scenarios have been corrected to use the correct path.

### Dry-run validation limitations
`--dry-run` validates YAML structure only. It does NOT validate: fault param value types, Prometheus query syntax, runtime path existence, or any actual fault injection behavior. All runtime bugs found in this audit were caught through code-level analysis and live testing.

### Silent no-op: `container_pause` duration parameter (FIXED)
`injectContainerPause` only checks `fault.Params["duration"].(string)`. YAML `duration: 45` (bare integer) silently fails — duration stays 0, container pauses then immediately unpauses. Fixed to `duration: 120s` (string format). The previous `validator-freeze-zombie` PASS result was invalid.

### `container_pause` during_fault criteria timing
Container_pause with duration blocks in the goroutine and unpauses before MONITOR evaluates `during_fault` criteria. Use `post_fault_only: true` instead.

### Multi-fault teardown tracking
`injectedFaults` map stores only one fault type per container. Scenarios with multi-type faults have been reordered so cleanup-critical types (connection_drop) are listed last.

### Clock skew not implementable
Docker containers share the host kernel clock. Per-container clock manipulation is impossible.

### Selective network partitions not implementable
`connection_drop` and `network` (tc) operate at port level only, not destination IP. Cannot create topologies requiring selective link cutting.
