---
name: author-scenario
description: Synthesize a chaos-utils scenario YAML from a hypothesis. Trigger when the user says "write a chaos scenario", "design a chaos test for X", "test hypothesis Y on the devnet", "draft a scenario that proves Z", "scenario for <fault> on <target>", or asks to author/produce/scaffold a ChaosScenario YAML.
---

# author-scenario

Walks an LLM through producing a valid `apiVersion: chaos.polygon.io/v1` /
`kind: ChaosScenario` YAML, grounded in the actual Go schema and validator —
not guesswork.

## Step 0 — Read the authoritative sources first (non-negotiable)

Do not write a single line of YAML before reading, in this order:

1. **`pkg/scenario/types.go`** — canonical YAML schema. Every key you emit
   must appear in `ScenarioSpec`, `Target`, `Fault`, or `SuccessCriterion`.
   Unknown keys are silently ignored, so guessing produces a dead scenario.
2. **`pkg/scenario/validator/validator.go`** lines 97-400 — 19 validation
   rules including: required `apiVersion`/`kind`/`metadata.name`,
   DNS-style name regex, target alias uniqueness, valid selector types
   (`kurtosis_service`, `docker_container`), the registered fault-type
   list, network-param ranges, and the four allowed criterion types
   (`prometheus`, `log`, `state_root_consensus`, `command`).
3. **`scenarios/polygon-chain/network/single-node-isolation.yaml`** — the
   imitation target. Mirror its shape, comment density, and use of
   `during_fault` / `post_fault_only`.
4. **`CLAUDE.md`** §5 (hard invariants) and §6 (registered fault types).
5. **`scenarios/CLAUDE.md`** — directory convention, PromQL rules, the
   success-criteria pattern table.

If any of these contradict each other, the Go source wins. Update the
README/docs in a follow-up — never bend the schema to match a stale doc.

## Step 1 — Map the hypothesis to scenario structure

A chaos experiment encodes three things. Translate them in order:

| Hypothesis component | YAML home | Notes |
| -------------------- | --------- | ----- |
| **Steady-state hypothesis** ("under normal conditions, X holds") | `success_criteria` with `critical: true` and **no** `post_fault_only` flag. These run in pre-check. | If the steady state cannot be observed, the experiment aborts before injection — exactly what you want. |
| **Fault** ("we believe injecting Y will…") | `spec.faults[]` entry with `type:` from the registered list and `target:` referencing a `spec.targets[].alias`. | Pick the most specific registered type; do not invent. |
| **Fault-effectiveness check** ("…cause Z to happen *while the fault is live*") | `success_criterion` with `during_fault: true`. Evaluated at end of MONITOR, before teardown. | Required because TEARDOWN runs before DETECT — without this flag, you can't observe the fault. |
| **Recovery hypothesis** ("after the fault clears, the system returns to steady state") | `success_criterion` with `post_fault_only: true` and `critical: true`. Skipped in pre-check; evaluated in DETECT. | This is what tells you the system *recovered* rather than just survived. |

## Step 2 — Hard rules (will silently ruin your scenario if violated)

These come from `CLAUDE.md` §5 and `scenarios/CLAUDE.md`:

1. **Never target Prometheus or Grafana.** `pkg/discovery/` rejects them,
   but you should also never write a selector pattern that could match
   them (e.g. `prometheus.*`, `grafana.*`).
2. **Validator 4 is the reserved fault target.** Any "system is healthy"
   PromQL must scope to other indices. Use the established pattern:
   `{job=~"l2-el-[1235678]-bor-heimdall-v2-validator"}` (or the `l2-cl-`
   variant for Heimdall metrics). Do **not** use `.*` or `[0-9]+` here —
   v4 will pollute the result.
3. **Faults teardown before success-criteria evaluation.** That means a
   criterion that checks "validator stalled" *must* set
   `during_fault: true`, otherwise it evaluates after the validator has
   already recovered and falsely passes/fails.
4. **`post_fault_only: true` is for fault-effectiveness criteria** —
   things expected to fail before injection (e.g., "isolated node has
   stalled"). Without this flag the pre-check aborts the run.
5. **Sidecars require `--cap-add=NET_ADMIN,NET_RAW`.** Don't write a
   scenario that assumes a hardened sidecar — the image needs those
   caps to do tc/iptables.
6. **`corruption_proxy` rules are first-match-wins.** When stacking
   semantic mutations, put specific patterns before general ones, and
   compose multiple `operations` inside a *single* rule rather than
   relying on multiple rules to layer.
7. **No PromQL subqueries.** `rate(x[1m])[5m:1m]` is unsupported by the
   runner. Use single-window aggregations.
8. **`cooldown` ≈ active-fault window.** Set it large enough that any
   `[Xm]` window inside a `during_fault` PromQL fits inside the fault
   duration. For cold-start-sensitive recovery queries, prefer `[3m]`
   over `[1m]`.

## Step 3 — Use only registered fault types

From `pkg/scenario/validator/validator.go::validateFaultType` and
`CLAUDE.md` §6. Unknown types degrade to a warning and the scenario
silently does nothing.

```
network                                    — tc netem (latency, packet_loss, bandwidth, target_ports, target_proto)
cpu, cpu_stress                            — stress-ng CPU load
memory, memory_stress, memory_pressure     — stress-ng memory pressure
container_restart, container_kill,
container_pause                            — Docker lifecycle (stagger, grace_period)
connection_drop                            — iptables (rule_type, target_ports, target_proto, probability)
dns                                        — DNS failure
process_kill                               — in-container signal delivery
disk_io, disk_fill, file_delete,
file_corrupt                               — disk pressure / FS corruption
clock_skew                                 — container clock manipulation
http_fault                                 — Envoy L7 (abort, delay, body/header override)
corruption_proxy                           — JSON-aware semantic corruption (Bor RPC / Heimdall REST)
p2p_attack                                 — chaos-peer devp2p attacks on Bor
disk, process, custom                      — legacy umbrella categories; prefer specific types above
```

When in doubt about a fault's exact param keys: read the README's
"Fault parameters" section AND the matching handler under
`pkg/injection/<category>/`. The parser ignores unknown keys silently,
so a typo is invisible — verify by grep.

## Step 4 — PromQL conventions

| Intent | Pattern |
| ------ | ------- |
| "Healthy validators keep producing" | `min(rate(chain_head_block{job=~"l2-el-[1235678]-bor-heimdall-v2-validator"}[3m]))` with `threshold: "> 0"` and `critical: true` |
| "Heimdall consensus continues" | `sum(increase(cometbft_consensus_height{job=~"l2-cl-[1235678]-heimdall-v2-bor-validator"}[2m])) or vector(0)` |
| "Faulted node has actually stalled" (during fault) | `rate(chain_head_block{job="l2-el-<idx>-..."}[1m])` `< 0.5`, set `during_fault: true` |
| "System recovered after fault" | same query as steady-state, `post_fault_only: true`, `critical: true` |
| "Chain head converged after partition heals" | `max(...) - min(...)` across healthy validators, `< 50`, `post_fault_only: true` |
| "No panic anywhere" | `type: log`, `pattern: "panic"`, `absence: true` |
| "Fault-effectiveness: log line appeared" | `type: log`, `pattern: <regex>`, `absence: false`, `post_fault_only: true` |

Container-liveness sanity check: prefer `system_cpu_goroutines` over raw
`up{}` when `up` is noisy on this devnet.

## Step 5 — Required YAML skeleton

Mirror this exactly, filling in the angle-bracketed parts:

```yaml
apiVersion: chaos.polygon.io/v1
kind: ChaosScenario
metadata:
  name: <kebab-case-matches-filename>
  description: >
    <1-2 paragraphs: what is corrupted/restricted, what the system should
    do about it, what would constitute failure. Include the hypothesis in
    plain English.>
  tags: [<category>, <fault-type>, <severity>]
  author: <team-or-handle>
  version: "0.1.0"

spec:
  targets:
    - selector:
        type: kurtosis_service
        enclave: "${ENCLAVE_NAME}"
        pattern: "l2-cl-<N>-heimdall-v2-bor-validator"   # or l2-el-<N>-bor-...
      alias: <short_alias>

  duration: <Xm>          # total experiment duration
  warmup: 30s             # steady-state observation window before injection
  cooldown: <Ym>          # active-fault window; PromQL [Xm] windows must fit inside

  preconditions:          # optional, recommended when scenario assumes >=N validators
    min_validators: 4

  faults:
    - phase: <kebab-case-phase>
      description: <one line, present tense>
      target: <alias>
      type: <registered-type>
      params:
        # fault-specific; see pkg/injection/<category>/ for exact keys

  success_criteria:
    # Steady state — runs in pre-check AND post-fault
    - name: <snake_case>
      description: <one line>
      type: prometheus
      query: <PromQL scoped to indices excluding 4>
      threshold: "> 0"
      critical: true

    # Fault effectiveness — runs while fault is live
    - name: <snake_case>
      description: <e.g., "isolated node stops advancing">
      type: prometheus
      query: <PromQL>
      threshold: "< 0.5"
      critical: false
      during_fault: true

    # Recovery — runs only after teardown
    - name: <snake_case>
      description: <e.g., "isolated node resumes block sync">
      type: prometheus
      query: <PromQL>
      threshold: "> 0"
      critical: true
      post_fault_only: true

  metrics:
    - chain_head_block
    - cometbft_consensus_height
    - <others used by your queries>
```

## Step 6 — Validate with `--dry-run` BEFORE claiming success

This is mandatory. The validator catches schema errors that are otherwise
silent at runtime.

```bash
# Set a dummy Prometheus URL so dry-run doesn't try Kurtosis discovery.
PROMETHEUS_URL=http://localhost:9090 \
  ./bin/chaos-runner run --dry-run --scenario <path/to/your/scenario.yaml>
```

Exit-code semantics (from `cmd/chaos-runner/main.go:55-66`):

- **0** — scenario is valid (or full run met all critical criteria).
- **1** — scenario invalid OR a critical success criterion failed.
- **2** — infrastructure error (Prometheus unreachable, Kurtosis enclave
  missing, Docker socket unavailable, …). Exit 2 in dry-run usually
  means you forgot to set `PROMETHEUS_URL`; it does not mean your
  scenario is wrong.

If dry-run prints warnings, address them — many warnings (e.g. unknown
fault type, unknown criterion type) signal a typo that would silently
produce a no-op scenario.

## Step 7 — Filing the scenario

- Place the file under the directory matching its **primary** fault type
  (see `scenarios/CLAUDE.md` directory convention). Compound scenarios
  go in `compound/`.
- Filename is kebab-case and stable — CI and runbooks reference it by
  path.
- After dry-run passes, run the scenario live against a devnet at least
  once before treating it as merged-quality. The dry-run only validates
  the schema; it cannot tell you whether your PromQL actually matches
  any series.

## Common LLM mistakes to avoid

- **Inventing a fault type** like `network_partition` or `slowloris` —
  use `network` with `packet_loss: 100` + `target_ports`, or
  `connection_drop`. Check the registered list above.
- **Using `>=` in the threshold without quotes.** `threshold` is a
  string; YAML parses unquoted `>= 5` weirdly. Always quote: `"> 0"`.
- **Querying `up{}` without the validator-4 exclusion** — the experiment
  will look unhealthy in pre-check because v4 is reserved.
- **Forgetting `during_fault: true`** on criteria that measure the
  fault's effect — they evaluate post-teardown when the system has
  already recovered.
- **Setting `post_fault_only: true` on the steady-state criterion** —
  pre-check then has nothing to enforce, and a broken devnet will run
  the experiment anyway.
- **Cooldown too short.** A `[3m]` rate window inside a 2-minute
  cooldown will return empty.
- **`kurtosis_service` selector with a pattern that matches the
  monitoring stack.** Always anchor the pattern to validator naming.

## Examples

See `examples/` in this skill directory for two minimal end-to-end
scenarios you can copy and adapt.
