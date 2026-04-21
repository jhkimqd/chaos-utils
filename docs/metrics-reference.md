# Prometheus Metrics Reference

This document lists the Prometheus metrics actually exposed by the live
Polygon Chain (heimdall-v2 + Bor) devnet and the ones success criteria
should use. It is audited against the Kurtosis `pos` enclave — every
metric in the tables below was confirmed present via
`/api/v1/label/__name__/values`.

> **Warning**: older versions of this doc referenced metric names like
> `bor_blockchain_head_block`, `heimdall_consensus_height`,
> `heimdall_p2p_peers`, and `heimdall_validator_voting_power`. Those
> metrics **do not exist** in heimdall-v2 / Bor. Using them produces
> silent no-op criteria (query returns empty → detector reports
> "query returned no results"). All scenarios have been migrated to
> the names in the tables below.

---

## Job labels

Prometheus scrapes each container under a distinct `job` label — most
criteria select a subset by regex on `job=~`. The live convention is:

| Job label (job=) | Component |
|---|---|
| `l2-el-1-bor-heimdall-v2-validator` … `l2-el-8-bor-heimdall-v2-validator` | Bor EL on validator 1–8 |
| `l2-el-9-bor-heimdall-v2-rpc` | Bor EL on RPC node 9 |
| `l2-el-10-erigon-heimdall-v2-rpc` | Erigon EL on RPC node 10 |
| `l2-cl-1-heimdall-v2-bor-validator` … `l2-cl-8-heimdall-v2-bor-validator` | Heimdall-v2 CL on validator 1–8 |
| `l2-cl-9-heimdall-v2-bor-rpc` | Heimdall-v2 CL on RPC node 9 |
| `l2-cl-10-heimdall-v2-erigon-rpc` | Heimdall-v2 CL on RPC node 10 |
| `panoptichain` | Network-wide aggregator |

### The validator 4 devnet quirk

Validator 4 in this devnet sporadically reports late blocks and
lagging peers — it's a known local-setup artefact, not something
chaos scenarios should assert against. Broad queries across all
validators should exclude `l2-(cl|el)-4-...` unless the scenario is
intentionally testing v4's behaviour. The idiomatic regex is
`job=~"l2-cl-[1235678]-heimdall-v2-bor-validator"` (or the `l2-el-`
equivalent for Bor).

---

## Bor Metrics (L2 Execution Layer)

Scraped from `l2-el-*-bor-heimdall-v2-validator` and
`l2-el-9-bor-heimdall-v2-rpc`.

### Block production and chain state

| Metric | Type | Notes |
|---|---|---|
| `chain_head_block` | Gauge | Current head block number. Use `rate(chain_head_block[1m])` for blocks/sec. |
| `chain_head_header` | Gauge | Latest imported header number. |
| `chain_reorg_drop` | Counter | Blocks dropped in reorgs. Use `increase(chain_reorg_drop[5m])` to bound depth per window. |
| `chain_reorg_add` | Counter | Blocks added in reorgs. |
| `chain_bor_consensus` | Counter | Bor consensus events. |
| `chain_checkpoint_latest` | Gauge | Last checkpoint number observed by Bor. |

> **Note**: Prometheus emits an info warning (`metric might not be a
> counter, name does not end in _total/_sum/_count/_bucket`) for
> `rate(chain_head_block[...])`. This is cosmetic — chain_head_block
> is monotonic so `rate()` works correctly.

### P2P and runtime

| Metric | Type | Notes |
|---|---|---|
| `system_cpu_goroutines` | Gauge | Goroutine count. **Bor uses this name**, not `go_goroutines`. |
| `chain_execution` | Counter | Block execution events. |
| `chain_execution_parallel` / `_serial` / `_parallel_error` | Counter | Block execution parallelism stats. |

### RPC and bridge (Heimdall → Bor span fetch)

| Metric | Type | Notes |
|---|---|---|
| `client_requests_span_valid` | Counter | Bor-side span fetch success count. |
| `client_requests_span_invalid` | Counter | Bor-side span fetch failure count. |

---

## Heimdall-v2 (CL) Metrics

Scraped from `l2-cl-*-heimdall-v2-bor-validator` and the RPC variants.
Heimdall-v2 exports standard CometBFT + Cosmos-SDK metrics plus a
`heimdallv2_*` family for bridge/API state.

### CometBFT consensus

| Metric | Type | Notes |
|---|---|---|
| `cometbft_consensus_height` | Gauge | Current consensus height. Use `increase(...[2m])` for progress. |
| `cometbft_consensus_latest_block_height` | Gauge | Highest block height committed. |
| `cometbft_consensus_late_votes` | Counter | Votes received after the block was committed. **Baseline on this devnet is ~4 000/min per validator** — thresholds must account for that (see "Threshold calibration" below). |
| `cometbft_consensus_missing_validators` | Gauge | Validators missing from the last commit. |
| `cometbft_consensus_byzantine_validators` | Gauge | Double-signing detection. Any non-zero value is a critical safety violation. |
| `cometbft_consensus_validator_power` | Gauge | Per-validator voting power. 0 ⇒ jailed. |
| `cometbft_consensus_validator_missed_blocks` | Counter | Missed blocks per validator. |
| `cometbft_consensus_block_interval_seconds_{sum,count}` | Histogram | Use `rate(sum)/rate(count)` for mean interval. |
| `cometbft_consensus_rounds` | Counter | Consensus round count (>1 round indicates problems). |
| `cometbft_consensus_num_txs` | Gauge | Tx count in last block. |

### CometBFT P2P

| Metric | Type | Notes |
|---|---|---|
| `cometbft_p2p_peers` | Gauge | Heimdall peer count. Use `min(cometbft_p2p_peers{job=~"..."})` for worst-case. |
| `cometbft_blocksync_syncing` | Gauge | 1 when the node is block-syncing. |
| `cometbft_blocksync_latest_block_height` | Gauge | Height of peer we're syncing against. |

### ABCI / Cosmos SDK

| Metric | Type | Notes |
|---|---|---|
| `heimdallv2_abci_prepare_proposal_duration_seconds_{sum,count}` | Histogram | PrepareProposal latency. |
| `heimdallv2_abci_process_proposal_duration_seconds_{sum,count}` | Histogram | ProcessProposal latency. |
| `heimdallv2_abci_extend_vote_duration_seconds_{sum,count}` | Histogram | ExtendVote (vote-extension) latency. |
| `heimdallv2_abci_verify_vote_extension_duration_seconds_{sum,count}` | Histogram | VerifyVoteExtension latency. |
| `heimdallv2_abci_begin_blocker_duration_seconds_{sum,count}` | Histogram | BeginBlocker latency. |

### Bridge / external API calls (Heimdall → Bor / other heimdalls)

| Metric | Type | Notes |
|---|---|---|
| `heimdallv2_bor_api_calls_total` | Counter | Total RPC calls Heimdall made to Bor. |
| `heimdallv2_bor_api_calls_success_total` | Counter | Successful Bor RPC calls. |
| `heimdallv2_bor_api_response_time_seconds_{sum,count}` | Histogram | Bor call latency. |
| `heimdallv2_checkpoint_api_calls_total` / `_success_total` | Counter | Checkpoint submission call counts. |
| `heimdallv2_clerk_api_calls_total` / `_success_total` | Counter | Clerk (state-sync) API call counts. |
| `heimdallv2_milestone_api_calls_total` / `_success_total` | Counter | Milestone API call counts. |

### Runtime

| Metric | Type | Notes |
|---|---|---|
| `go_goroutines` | Gauge | Heimdall goroutine count (via `client_golang`). **Heimdall uses this name**, not `system_cpu_goroutines`. |
| `process_resident_memory_bytes` | Gauge | RSS per process. |

---

## Which goroutine metric does what?

A common footgun — Bor (Go-Ethereum fork) and Heimdall (Cosmos-SDK)
export goroutine counts under different names:

```
Bor (l2-el-*)         → system_cpu_goroutines
Heimdall (l2-cl-*)    → go_goroutines
```

A query using the wrong name for a given job **will silently return
nothing**. The detector now treats empty series as a FAIL (see
`pkg/monitoring/detector/failure_detector.go` ~L127), but the query
still needs to target the right job. Example:

```yaml
# Bor runtime (validators 1-3): correct
query: max(system_cpu_goroutines{job=~"l2-el-[123]-bor-heimdall-v2-validator"})

# Heimdall runtime (validator 3): correct
query: max(go_goroutines{job="l2-cl-3-heimdall-v2-bor-validator"})
```

---

## Threshold calibration from live devnet baseline

The thresholds below were measured on the live `pos` enclave with no
chaos injected. Use them as sanity anchors — criteria with thresholds
tighter than baseline will produce false positives.

| Metric | Baseline (median) | Suggested alert threshold |
|---|---|---|
| `rate(chain_head_block[1m])` | ~1.0–1.2 blk/s | `< 0.1` for "stalled" check |
| `sum(rate(cometbft_consensus_late_votes{job="l2-cl-X-..."}[2m])) * 60` | ~4 000/min **per validator** | `< 8000` per-validator, `< 40000` sum-across-8 |
| `cometbft_p2p_peers` (validator) | ~7 | `> 3` for "still connected" |
| `cometbft_consensus_missing_validators` | 0–1 | `< 3` for stress |
| `system_cpu_goroutines` (Bor) | ~220 | `< 500` for "bounded" |
| `go_goroutines` (Heimdall) | ~200 | `< 500` for "bounded" |

**Why late_votes is ~4 000/min**: CometBFT counts every late vote
across every height. On a chain producing ~1 block/sec with 8
validators gossiping, ~4 k late votes/min is normal gossip overlap,
**not** a consensus problem. Criteria that threshold at `< 2500` will
fire in baseline.

---

## Evaluation caveats

### Multi-series queries and equality thresholds

If a PromQL query returns more than one series (e.g. `chain_head_block`
without `max()`/`min()`/`sum()` wrapping), the detector must reduce
it to a single value. For directional thresholds (`<`, `<=`, `>`,
`>=`) the detector picks the worst-case value (max or min depending
on direction) so a single offending series trips the check.

**For equality thresholds (`==`, `!=`) the detector evaluates every
series individually and fails the criterion if ANY series fails.**
This prevents a silent pass on queries like `== 0` where one of eight
validators reports 100 — aggregating via min would hide the violation.

You should still prefer wrapping queries in `max()`/`min()`/`sum()`
— the per-series eval is a safety net, not an excuse to skip
aggregation.

### Subqueries are forbidden

PromQL subqueries of the form `metric[5m:1m]` are not supported by
this detector's default Prometheus config and produce empty results
post-crash. Use range vectors with `rate()`/`increase()` instead.
Example: don't write
`avg_over_time(rate(cometbft_consensus_height[2m])[5m:])`. Write
`avg(rate(cometbft_consensus_height[2m]))`.

### during_fault timing

Criteria marked `during_fault: true` are sampled by a background
goroutine (`duringFaultSampler`) that starts **before INJECT** and
polls every 15 s (15 s warm-up) through INJECT + MONITOR. The
orchestrator keeps the **worst** reading per criterion over the
window — so a single sample showing the fault active preserves the
pass/fail signal even if the fault self-terminates inside INJECT
(as `container_pause` with a `duration:` does). See
`pkg/core/orchestrator/during_fault_sampler.go`.

### Log-based criteria

`type: log` criteria scan container stdout/stderr for a regex. The
detector's regex is **case-sensitive** (Go `regexp`). Patterns like
`span.*rotat` will **not** match `Span rotated due to the current
producer's ineffectiveness` — use the literal heimdall-v2 log string
(or case-insensitive prefix `(?i)`). Known-good span-rotation
pattern:

```yaml
type: log
pattern: "Span rotated due to the current producer's ineffectiveness"
container_pattern: "heimdall-v2-bor-validator"
```

Log-target scope: a log criterion without `container_pattern` or
`target_log` only scans the containers the orchestrator targeted for
injection. Scenarios observing post-fault state across the cluster
must set `container_pattern`.

---

## Using metrics in scenarios

### Basic usage

```yaml
success_criteria:
  - name: block_production_continues
    type: prometheus
    query: min(rate(chain_head_block{job=~"l2-el-[1235678]-bor-heimdall-v2-validator"}[1m]))
    threshold: "> 0"
    critical: true
```

### Rate queries

```yaml
- name: checkpoint_submissions_ongoing
  type: prometheus
  query: sum(increase(heimdallv2_checkpoint_api_calls_success_total[5m]))
  threshold: "> 0"
  critical: false
```

### Histogram quantiles

```yaml
- name: prepare_proposal_p95_bounded
  type: prometheus
  query: histogram_quantile(0.95, sum(rate(heimdallv2_abci_prepare_proposal_duration_seconds_bucket[5m])) by (le))
  threshold: "< 2.0"
  critical: false
```

### Log criterion with absence check

```yaml
- name: no_panics
  type: log
  pattern: "(panic|fatal|CONSENSUS FAILURE)"
  container_pattern: "bor-heimdall-v2-validator"
  absence: true
  critical: true
  post_fault_only: true
```

---

## Troubleshooting

### "query returned no results"

This is a HARD FAIL — the detector no longer silently passes on empty
responses. Common causes:

1. Metric name doesn't exist (e.g. `heimdall_consensus_height` instead
   of `cometbft_consensus_height`). Run
   `curl $PROM_URL/api/v1/label/__name__/values | jq` to list all
   scraped names.
2. `job=~` regex matches no running containers (e.g. `job="l2-el-9-..."`
   when RPC 9 is down).
3. Subquery syntax (`[5m:1m]`) — not supported; use ranges/aggregations.

### Criterion always fails in baseline

Likely a threshold calibration issue. Run the query manually against
the live Prom URL first:

```bash
PROM_URL=$(kurtosis port print pos prometheus http)
curl -sG --data-urlencode 'query=<your_query>' "$PROM_URL/api/v1/query" | jq
```

Adjust the threshold against the observed baseline (see calibration
table above).

### Rate query returns 0

- Counter may have only been incremented once → need longer window.
- Metric may be a gauge pretending to be a counter → use `delta()`
  or `max_over_time - min_over_time` instead.
- Container was just restarted → the counter reset is legitimate.
