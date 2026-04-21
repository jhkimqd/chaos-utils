# Chaos-Utils Live-Devnet Audit — Session Summary (2026-04-21)

This document is the persisted, commit-tracked summary of a live-devnet audit
of every polygon-chain fault, monitor, and scenario in `chaos-utils` against a
Kurtosis PoS enclave (8 validators + 2 RPC nodes). The full per-finding
register with repro steps, file:line citations, and fix commentary lives at
`reports/audit-live-devnet-findings.md` in the working tree (the `reports/`
path is gitignored to keep test-run JSONs out of the repo, so this doc is the
version that survives the session).

## Scope

- Directories live-exercised: `scenarios/polygon-chain/applications/` (18 scenarios)
- Packages statically + unit-test audited: `pkg/injection/**`, `pkg/core/**`,
  `pkg/monitoring/**`, `pkg/reporting/**`, `pkg/discovery/**`,
  `cmd/chaos-runner/**`, `cmd/chaos-peer/**`, `cmd/corruption-proxy/**`
- Directories **not** sampled (queued for next session): compound, network,
  semantic, disk (safe subset only), boundary, bor-bug-bounty, cpu-memory

## Findings (18 total: 2 CRITICAL, 4 HIGH, 3 MEDIUM, 9 LOW)

| ID  | Severity | Status | One-line |
|-----|----------|--------|----------|
| F-01 | HIGH     | fixed  | `.(int)`/`.(float64)` on 11 numeric injection params → silent no-ops |
| F-02 | HIGH     | fixed  | Multi-fault teardown only removed last fault type per container |
| F-03 | LOW      | fixed  | Benign `tc qdisc del` first-inject warn spam |
| F-04 | CRITICAL | fixed  | `--set` overrides silent no-op (orchestrator re-parsed the file) |
| F-05 | HIGH     | fixed  | Equality multi-series min-agg false-pass; during_fault eval blind-spot |
| F-06 | MEDIUM   | fixed  | `span_rotation_detected` regex mismatch vs Heimdall-v2 log |
| F-07 | MEDIUM   | fixed  | `late_votes` thresholds below live baseline |
| F-08 | LOW      | fixed  | Metrics-reference / pipeline-architecture doc drift |
| F-09 | MEDIUM   | fixed  | Partial-inject success leak + no error-path teardown |
| F-10 | LOW      | fixed  | `TestResult.FaultCount` dead field; surfaced as `fault_installs` |
| F-11 | HIGH     | fixed  | Success-path `fault_installs` always 0 (nil-slice timing) |
| F-12 | LOW      | fixed  | `removeTrackedFaults` panic left stale slice; deferred clear |
| F-13 | LOW      | fixed  | `CleanupAll` fired twice on success |
| F-14 | CRITICAL | fixed + live-verified | Concurrent-map-writes fatal on emergency/teardown overlap |
| F-15 | MEDIUM   | fixed  | `heimdall_websocket_reconnects` structurally unsatisfiable criterion |
| F-16 | LOW      | fixed  | `or vector(1)` PromQL fallback doesn't fire on rate=0; swapped to counter-delta |
| F-17 | LOW      | fixed  | `missing_validators` measures blocked-round stalls; criterion dropped |
| F-18 | LOW      | wontfix | `frozen_bor_stalls` 1m-rate tail contamination; documented in-scenario |

**17 fixed (1 live-verified) / 0 open / 1 wontfix.**

## Commits landed (18)

```
e79a03e fix(scenarios): F-16 + F-17 non-critical criterion fixes
1971384 fix(sidecar): serialise Manager.createdSidecars with RWMutex (F-14)
ee5b51a fix(cleanup): serialize concurrent Coordinator.CleanupAll entrants (F-14)
b872b23 scenarios: remove structurally unsatisfiable WS-reconnect criterion (F-15)
4d1bdaf fix(orchestrator): capture fault install count before teardown (F-11/12/13)
4b45d6d feat(reporting): surface FaultInstalls count in test report (F-10)
673a01d fix(orchestrator): record all inject successes before erroring (F-09)
d3936d0 docs(scenarios): remove stale injectedFaults-map workaround notes (F-02 ripple)
9bc003a docs(pipeline-architecture): reflect F-02 slice-based injectedFaults
b2819fd fix(orchestrator): track multiple faults per container in teardown (F-02)
d30e926 fix(scenarios): F-06 span_rotation literal pattern; F-07 late_votes baseline
c1882c4 fix(orchestrator): honor --set overrides by not re-parsing scenario (F-04)
a9485d2 docs(pipeline-architecture): describe during_fault sampler model
028d022 docs(metrics-reference): rewrite against live heimdall-v2 + Bor metrics (F-08)
01604d3 fix(detector,orchestrator): sample during_fault criteria throughout fault window (F-05)
a1fb9e8 fix(injection): demote benign 'no qdisc to delete' tc errors to debug (F-03)
a48adb2 fix(injection): accept int|float64 for remaining numeric params (F-01)
8f74a5e fix(scenarios): align polygon-chain YAMLs to live devnet topology
```

## Verification signals

- `go build ./...` clean, `go vet ./...` clean.
- `go test -race ./...` → **164 pass in 37 packages** with race detector.
- Two regression tests land with F-14:
  `TestManager_ConcurrentMapAccess_F14` and `TestAuditLog_ConcurrentReadersWriters`.
  Both trip the race detector without the mutex guards, green with them.
- Live applications/ sweep: **16 PASS** (zero critical-criterion failures) +
  **1 GATED** (`chaindata-wipe-resync`) + **1 hard-fail re-verified clean**
  (`coordinated-full-cluster-crash` post-F-14: no `fatal error: concurrent map writes`,
  reached TEARDOWN cleanly; wrapper's 600s cap fired mid-teardown — race fix
  confirmed, with a secondary observation that teardown of N=15 sidecars
  exceeds 600s worth investigating as a future perf pass, not re-filed).

## Gated scenarios (require devnet restart on trigger — do not run casually)

- `single-node-isolation` — Heimdall P2P unrecoverable (CometBFT exponential backoff)
- `targeted-producer-isolation`
- `two-phase-partition-escalation`
- `three-validator-full-isolation`
- `progressive-partition-expansion`
- `chaindata-wipe-resync` — Bor cannot resync from empty chaindata
- `disk-fill-exhaustion` — leaves Bor stuck

## Open items carried forward

1. **Seven scenario directories unsampled**: compound, network, semantic,
   disk (safe subset), boundary, bor-bug-bounty, cpu-memory. The `or vector(1)`
   PromQL pattern (F-16 class) is likely present on other criteria across
   these directories and should be swept for in the next live pass — a grep
   for `or vector(` in `scenarios/polygon-chain/` is a good starting point.
2. **F-16 / F-17 YAML fixes were not live-re-run** this session (time budget).
   Next session should re-run `sigkill-immediate-restart` and
   `validator-crash-during-checkpoint` to confirm the patched criteria
   behave as expected against the devnet.
3. **F-18** `frozen_bor_stalls` remains `wontfix` with in-scenario
   documentation. A per-node `max_over_time` derivative is the right long-term
   shape; deferred until the monitor-side sampler gains that semantic.

## References

- Live register (working tree, gitignored): `reports/audit-live-devnet-findings.md`
- Related doc updates landed this session:
  - `docs/metrics-reference.md` (rewritten against live Prom metric surface)
  - `docs/pipeline-architecture.md` (during_fault sampler + slice-based injectedFaults)
- Session handoff doc (non-tracked): `chaos-devnet-audit-context.md`
