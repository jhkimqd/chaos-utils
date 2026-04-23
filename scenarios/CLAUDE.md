# scenarios/CLAUDE.md — Scenario Authoring Guide

> Narrower companion to the root [`CLAUDE.md`](../CLAUDE.md). Read this
> before creating, renaming, or significantly refactoring scenario YAMLs.

## Directory convention

```
scenarios/
├── polygon-chain/
│   ├── network/           tc netem faults (latency, loss, bandwidth, partition)
│   ├── disk/              disk_io, disk_fill, file_delete, file_corrupt
│   ├── compound/          multi-fault composite scenarios
│   ├── semantic/          corruption_proxy scenarios (HTTP-level corruption)
│   │   └── rules/         rule fragments; _REFERENCE.yaml is the catalogue
│   ├── applications/      service-level faults (rabbitmq, restart-all, etc.)
│   └── boundary/          validator-count edge cases
└── polygon-cdk/
    └── <chain>/<category>/  mirrors polygon-chain structure per CDK variant
```

- Put a scenario in the directory matching its **primary** fault type.
  Compound scenarios go in `compound/`.
- Keep filenames kebab-case, descriptive, and stable — CI and runbooks
  reference them by path.

## Required shape (enforced by `pkg/scenario/validator/`)

```yaml
apiVersion: chaos.polygon.io/v1
kind: ChaosScenario
metadata:
  name: <matches-filename>
  description: >
    1-2 paragraphs: what is corrupted/restricted, what the system should
    do about it, what would constitute failure.
  tags: [<category>, <fault-type>, <severity>]
  author: <team-or-handle>
  version: "0.1.0"

spec:
  targets:
    - selector:
        type: kurtosis_service
        enclave: "${ENCLAVE_NAME}"
        pattern: "l2-cl-<N>-heimdall-v2-bor-validator"
      alias: <short-alias>

  duration: <Xm>
  warmup: 30s
  cooldown: <Ym>     # cooldown == faults-active period for detection

  preconditions:     # optional but recommended
    min_validators: 4

  faults:
    - phase: <kebab-case-phase-name>
      description: <one line>
      target: <alias>
      type: <registered-fault-type>
      params: { ... }    # fault-specific

  success_criteria:
    - name: <snake_case>
      description: <one line>
      type: prometheus     # or: log, state_root_consensus
      query: <PromQL>
      threshold: "> 0"     # string: > < >= <= == !=
      critical: true
      post_fault_only: false   # true when criterion measures fault effectiveness
      during_fault: false      # true when must evaluate while faults are live

  metrics:
    - chain_head_block
    - cometbft_consensus_height
    - <others used by queries>
```

## PromQL query rules

These are battle-tested conventions. Violating them produces flaky tests.

1. **No subqueries.** `rate(x[1m])[5m:1m]` is not supported by our runner.
2. **Exclude validator 4** from any "chain is healthy" query. Validator 4
   is the reserved fault target — example: `chain_head_block{job=~"l2-el-[1235678]-bor-heimdall-v2-validator"}`.
3. **`cooldown` == the active-fault window.** Set it large enough that
   the metric window inside your PromQL fits inside the fault period.
4. **`system_cpu_goroutines`** is a usable proxy for container liveness
   on Heimdall/Bor — prefer it over raw `up` when `up` is noisy.
5. **Log criteria** can only target containers that exist at
   scan time. Recreated containers lose their logs — pair log criteria
   with container-restart faults carefully.
6. **Widen `rate(...[Xm])` windows** (prefer `[3m]` over `[1m]`) for
   cold-start-sensitive queries at cooldown boundaries.

## Fault-type specific guidance

### `network` (tc netem)
- `packet_loss: 100` + no `target_ports` partitions the whole container.
  The validator will log-spam — consider `target_ports` for a targeted
  partition of e.g. consensus (`26656,26657`) or RPC (`1317` or `8545`).
- Supported params: `device`, `latency`, `packet_loss` (0-100),
  `bandwidth` (kbit/s), `target_ports` (CSV), `target_proto` (tcp/udp/both).
- `reorder` exists historically in some YAMLs but is not in the current
  `NetworkFaultParams` struct — check `pkg/injection/l3l4/` before using it.

### `corruption_proxy`
See `scenarios/polygon-chain/semantic/rules/_REFERENCE.yaml` — the
single-source reference for rule schema and operation types. Embed
rules either inline via `rules_yaml:` or with a separate rule file.

### `disk_io`
- `io_latency_ms` is actually the `dd` worker count, not a latency.
  The name is a legacy artefact — don't rename without migrating every
  scenario that uses it.
- `target_path` must exist in the container. Default Polygon paths:
  `/var/lib/bor/bor/chaindata`, `/heimdall-home/data`.

### `container_*`
- `stagger: 0` restarts all targets simultaneously — common for
  "all validators restart" scenarios.
- `grace_period: 0` with `container_kill` simulates SIGKILL / crash.

## Success-criteria patterns

| Intent                                         | Pattern                                                                 |
| ---------------------------------------------- | ----------------------------------------------------------------------- |
| "Healthy validators must keep producing"       | `min(rate(chain_head_block{job=~"l2-el-[healthy-indices]-..."}[3m])) > 0` |
| "Fault was actually applied" (during fault)    | set `during_fault: true`, query for the expected effect                |
| "System recovered after fault"                 | set `post_fault_only: true`, query for healthy steady state            |
| "Proposition X was rejected"                   | `type: log`, pattern matches log line, `absence: false`                |
| "No panic anywhere"                            | `type: log`, pattern: `"panic"`, `absence: true`                       |

## Lifecycle when you add a new scenario

1. Create YAML under the correct category directory.
2. Run `./bin/chaos-runner run --scenario <path> --dry-run` — this
   executes the validator in `pkg/scenario/validator/`.
3. Run it live against your devnet once before committing.
4. If the scenario exercises a new fault-type or params path, update:
   - `pkg/scenario/validator/validator.go` (fault type registration)
   - Root [`CLAUDE.md`](../CLAUDE.md) §6 (fault types table)
   - [`README.md`](../README.md) `## Fault Parameters`
   - [`docs/scenario-expected-outcomes.md`](../docs/scenario-expected-outcomes.md) (expectations)
5. Add to CI if it's part of the regression suite (see `.github/` workflows).

## When in doubt

- Read a sibling scenario in the same directory — conventions there are
  the authoritative pattern.
- Check `pkg/scenario/types.go` for the exact YAML key spellings.
- Don't invent a new success-criterion `type:` — only `prometheus`,
  `log`, and `state_root_consensus` are supported.
