# CLAUDE.md — AI Context Primer for chaos-utils

> This file is the single entry point for AI agents working in this repo.
> Read this first. Follow the pointers for deeper context. Keep this file
> in sync when you change architecture, schemas, binaries, or invariants.

---

## 1. What this repo is

Chaos-engineering framework that injects faults into a Polygon PoS (or CDK)
Kurtosis devnet and checks resilience via Prometheus queries. It is **not** a
library — it is three binaries orchestrated by YAML scenarios. User-facing
intro and usage live in [`README.md`](README.md); everything below is the
map/guardrails for agents.

## 2. Binaries (never change these roles)

| Binary             | Where it runs               | Source                    | Role                                                                            |
| ------------------ | --------------------------- | ------------------------- | ------------------------------------------------------------------------------- |
| `chaos-runner`    | Host (never containerized)  | `cmd/chaos-runner/`      | Parses YAML scenarios, discovers containers, injects via sidecars, checks Prom. |
| `corruption-proxy` | Sidecar image               | `cmd/corruption-proxy/`  | JSON-aware HTTP reverse proxy for semantic corruption.                          |
| `chaos-peer`      | Sidecar image               | `cmd/chaos-peer/`        | Fake devp2p peer for Bor RLPx-level attacks.                                    |

Sidecar image: `jhkimqd/chaos-utils:latest` built from `Dockerfile.chaos-utils`
(Ubuntu + Envoy + tc + iptables + nftables + the two sidecar binaries).

## 3. Authoritative sources — cite these, don't guess

When unsure about a schema, ALWAYS read the Go source first. The README can
drift; the types cannot.

| Concern                           | Authoritative file                                             |
| --------------------------------- | --------------------------------------------------------------- |
| Scenario YAML schema              | `pkg/scenario/types.go`                                        |
| Scenario validation & fault types | `pkg/scenario/validator/validator.go`                          |
| Corruption rule schema            | `pkg/injection/http/corruption/rules.go`                       |
| Corruption operation semantics    | `pkg/injection/http/corruption/mutations.go`                   |
| Corruption proxy matching/gating  | `pkg/injection/http/corruption/proxy.go`                       |
| Orchestrator state machine        | `pkg/core/orchestrator/`                                       |
| Service discovery rules           | `pkg/discovery/`                                               |
| Network fault params              | `pkg/scenario/types.go::NetworkFaultParams` + `pkg/injection/l3l4/` |

If a README statement contradicts one of the files above, the file wins —
update the README rather than the source.

## 4. Repo layout

```
chaos-utils/
├── cmd/                        Binaries. See §2.
├── pkg/
│   ├── core/orchestrator/      PARSE → WARMUP → pre-check → INJECT →
│   │                           MONITOR → TEARDOWN → DETECT state machine.
│   ├── discovery/              Kurtosis/Docker lookup. Rejects prometheus+grafana.
│   ├── injection/
│   │   ├── container/          restart, kill, pause
│   │   ├── disk/               disk_io, disk_fill, file_delete, file_corrupt
│   │   ├── dns/                DNS failure
│   │   ├── firewall/           connection_drop
│   │   ├── http/corruption/    corruption_proxy (see _REFERENCE.yaml below)
│   │   ├── l3l4/               network (tc netem / iptables)
│   │   ├── p2p/bor/            p2p_attack (chaos-peer implementations)
│   │   ├── process/            process_kill
│   │   ├── safeshell/          shell-exec guardrails
│   │   ├── sidecar/            sidecar lifecycle
│   │   ├── stress/             cpu_stress, memory_stress
│   │   ├── time/               clock_skew
│   │   └── verification/       post-teardown cleanup audit
│   ├── monitoring/             Prometheus client, metric collection
│   ├── scenario/               Parser, validator, types
│   ├── reporting/              JSON test reports → reports/
│   ├── emergency/              SIGINT/SIGTERM handling
│   └── config/                 config.yaml schema + auto-gen
├── scenarios/                  Built-in chaos tests (YAML).
│   ├── polygon-chain/          Polygon PoS: network, disk, compound,
│   │                           semantic, applications, boundary,
│   │                           bor-bug-bounty. See scenarios/CLAUDE.md.
│   └── polygon-cdk/            CDK chains: erigon-rollup, validium,
│                               sovereign-*, op-geth, op-succinct.
├── docs/                       Deep-dive docs (see §7).
├── reports/                    Auto-generated test reports (gitignored).
├── generated/                  Auto-generated scenarios (gitignored-ish).
├── Dockerfile.chaos-utils      Sidecar image build.
└── Makefile                    Build targets. See §8.
```

## 5. Hard invariants — do not break these

1. **Prometheus & Grafana are never fault targets.** `pkg/discovery/` rejects
   any selector that resolves to a monitoring container. If you add a new
   fault type or discovery path, enforce this. Silent bypass is a bug.
2. **Validator 4 is the default fault target.** Devnet chaos runs exclude
   validator 4 from Prometheus queries so the experiment still has a healthy
   reference. When writing success-criteria queries, scope to other validator
   indices (`l2-cl-[1235678]` style). See existing scenarios for the pattern.
3. **Fault teardown runs BEFORE success-criteria evaluation.** Otherwise
   Prometheus can't scrape through the fault. If you add a criterion that
   must observe faults in-flight, set `during_fault: true` on it.
4. **Pre-fault health check must pass for steady-state hypothesis.** Any
   `critical: true` criterion failing in pre-check aborts the experiment.
   Use `post_fault_only: true` for criteria that verify *fault effectiveness*
   (e.g. "partitioned validator stalled") — those are expected to fail
   before injection.
5. **Sidecars need `--cap-add=NET_ADMIN,NET_RAW`.** This is in the image
   contract — don't try to strip capabilities to "harden" the image.
6. **Tests may NOT leave tc/iptables/sidecar residue.** `pkg/injection/verification/`
   asserts clean state post-run. If you add a fault, add its verification.
7. **Corruption proxy first-match-wins.** When authoring rules, put specific
   patterns before general ones. Don't rely on multi-rule composition —
   stack `operations` inside one rule instead. See
   `scenarios/polygon-chain/semantic/rules/_REFERENCE.yaml`.

## 6. Fault types — registered list

Maintained in `pkg/scenario/validator/validator.go::validateFaultType`.
Adding a new fault type requires:
1. Add the string to `validTypes` in that file.
2. Add a handler under `pkg/injection/<category>/`.
3. Add verification under `pkg/injection/verification/`.
4. Document it in this file + README `## Fault Parameters`.
5. Provide at least one example scenario under `scenarios/`.

Current registered types (as of the last update to this file — always
verify against the source when in doubt):

```
network                 — tc netem (latency, packet_loss, bandwidth, ports)
cpu, cpu_stress         — stress-ng CPU load
memory, memory_stress,
memory_pressure         — stress-ng memory pressure
container_restart,
container_kill,
container_pause         — Docker lifecycle
connection_drop         — iptables connection reset
dns                     — DNS failure injection
process_kill            — in-container signal delivery
disk_io, disk_fill,
file_delete,
file_corrupt            — disk I/O pressure & filesystem corruption
clock_skew              — container clock manipulation
http_fault              — Envoy L7 (abort, delay, body/header override)
corruption_proxy        — JSON-aware semantic corruption (Bor RPC / Heimdall REST)
p2p_attack              — chaos-peer devp2p attacks on Bor
disk, process, custom   — legacy/umbrella categories; prefer specific types
```

## 7. Deep-dive documentation

- [`README.md`](README.md) — user-facing intro, install, usage.
- [`docs/pipeline-architecture.md`](docs/pipeline-architecture.md) — orchestrator phases.
- [`docs/metrics-reference.md`](docs/metrics-reference.md) — canonical Prom metrics.
- [`docs/scenario-expected-outcomes.md`](docs/scenario-expected-outcomes.md) — per-scenario expectations.
- [`docs/resource-stress-testing.md`](docs/resource-stress-testing.md) — CPU/memory/disk patterns.
- [`docs/audit-live-devnet-summary.md`](docs/audit-live-devnet-summary.md) — canonical audit register.
- [`scenarios/CLAUDE.md`](scenarios/CLAUDE.md) — scenario authoring guardrails.
- [`scenarios/polygon-chain/semantic/rules/_REFERENCE.yaml`](scenarios/polygon-chain/semantic/rules/_REFERENCE.yaml) — corruption-rule catalogue.

## 8. Common commands

```bash
make                           # Build all three binaries
make build-runner              # Host CLI only
make build-static              # Static sidecar binaries (Dockerfile uses these)
make docker                    # Build sidecar image
make test                      # Integration tests
make vet                       # go vet
make fmt                       # gofmt -s -w
make fmt-check                 # CI gate

./bin/chaos-runner run --scenario scenarios/...yaml
./bin/chaos-runner run --scenario <path> --dry-run
./bin/chaos-runner run --scenario <path> --set duration=10m
```

## 9. Guardrails for AI agents

### Do
- Read the authoritative Go source before asserting a schema.
- Update this file and/or `scenarios/CLAUDE.md` whenever you add a fault
  type, change the orchestrator phases, rename a config key, or introduce
  a new subdirectory under `pkg/injection/` or `scenarios/`.
- Mirror changes into `README.md` and the relevant `docs/*.md`.
- Preserve existing scenario filenames — they are referenced by CI and
  external runbooks.
- Keep the `_REFERENCE.yaml` at `scenarios/polygon-chain/semantic/rules/`
  in sync with `rules.go` and `mutations.go`.

### Don't
- Don't introduce a silent fallback that bypasses a safety invariant
  (§5). Better to fail loudly.
- Don't create a new doc file unless the user asks or there's no
  existing doc that's a natural home — extend existing docs instead.
- Don't add a new fault type without its verification step.
- Don't rename `validTypes` entries without migrating every scenario YAML
  — the parser currently downgrades unknown types to a warning, so
  scenarios will silently stop firing.
- Don't assume the README or `scenario-expected-outcomes.md` are complete.
  If the source contradicts them, fix the doc.
- Don't delete `reports/` or `generated/` directories — they are runtime
  outputs and will be recreated.

## 10. When in doubt

Ask the user. Do not make architectural decisions that affect the
safety invariants in §5 without explicit confirmation.
