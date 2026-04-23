# Chaos Runner

A chaos engineering framework for testing Polygon Chain (PoS) and Polygon
CDK resilience on Kurtosis devnets. Declarative YAML scenarios, automated
fault injection, Prometheus-based success criteria, and safe teardown.

> AI agents: start with [`CLAUDE.md`](CLAUDE.md) and [`scenarios/CLAUDE.md`](scenarios/CLAUDE.md).

## What it does

- Injects faults (network, container lifecycle, CPU/memory stress, disk I/O,
  DNS, clock skew, HTTP corruption, devp2p attacks) into specific services.
- Monitors system behaviour via Prometheus.
- Evaluates declared success criteria automatically.
- Generates JSON test reports.

### Design principles

- **Steady-state hypothesis**: pre-fault health check must pass before
  injection; post-fault evaluation confirms recovery after teardown.
- **Prometheus-safe**: monitoring services (Prometheus, Grafana) are
  rejected as fault targets at discovery time — never a silent bypass.
- **Teardown before evaluation**: faults are removed before success
  criteria run, so Prometheus can scrape cleanly. Use `during_fault:
  true` on a criterion that must observe active injection.
- **Clean exit**: post-run verification asserts no tc/iptables/sidecar
  residue remains.
- **Emergency stop**: Ctrl+C triggers ordered teardown.

## Quick Start

### Prerequisites

- Docker (sidecars need `NET_ADMIN`, `NET_RAW`).
- A running Kurtosis-managed Polygon Chain or CDK devnet.
- Prometheus reachable (auto-discovered from Kurtosis when possible).

### Install

```bash
cd chaos-utils

# Build all three binaries → ./bin/
make

# Or just the host CLI
make build-runner
```

### Run

```bash
# First run auto-creates config.yaml with defaults
./bin/chaos-runner run --scenario scenarios/polygon-chain/network/single-node-isolation.yaml

# Customise (enclave name, Prometheus URL)
vim config.yaml

# Run a bandwidth-throttle scenario
./bin/chaos-runner run --scenario scenarios/polygon-chain/network/bor-p2p-bandwidth-throttle.yaml
```

First-run message:

```
⚠️  Config file not found, creating default configuration at: config.yaml
   You can edit this file to customize settings (enclave name, Prometheus URL, etc.)
```

## Architecture

### Binaries

**Host (your machine / CI)**

| Binary         | Source                | Purpose                                                                                  |
| -------------- | --------------------- | ---------------------------------------------------------------------------------------- |
| `chaos-runner` | `cmd/chaos-runner/`   | Parses YAML, discovers containers, orchestrates injection, queries Prometheus, reports.  |

**Sidecar (inside `jhkimqd/chaos-utils` image)**

| Binary             | Source                    | Purpose                                                                                               |
| ------------------ | ------------------------- | ----------------------------------------------------------------------------------------------------- |
| `corruption-proxy` | `cmd/corruption-proxy/`   | JSON-aware HTTP reverse proxy. Mutates Heimdall REST / Bor JSON-RPC responses per rules.              |
| `chaos-peer`       | `cmd/chaos-peer/`         | Fake devp2p peer. Speaks RLPx to a Bor node and sends crafted malicious messages (malformed blocks, hash floods, conflicting chains). |

`chaos-runner` is never containerized. `corruption-proxy` and `chaos-peer`
live in the sidecar image alongside Envoy, iproute2 (tc), iptables, and
nftables. The runner starts sidecars, injects through them, and tears
everything down after the test.

### Project Structure

```
chaos-utils/
├── cmd/
│   ├── chaos-runner/              Host CLI
│   ├── corruption-proxy/          Sidecar: HTTP corruption proxy
│   └── chaos-peer/                Sidecar: devp2p fake peer
├── pkg/
│   ├── core/orchestrator/         State machine: PARSE → WARMUP →
│   │                              [pre-check] → INJECT → MONITOR →
│   │                              TEARDOWN → DETECT
│   ├── discovery/                 Kurtosis & Docker service discovery
│   ├── injection/                 Fault injectors
│   │   ├── container/             restart, kill, pause
│   │   ├── disk/                  disk_io, disk_fill, file_delete, file_corrupt
│   │   ├── dns/                   DNS delay / failure
│   │   ├── firewall/              connection_drop
│   │   ├── http/                  http_fault (Envoy)
│   │   │   └── corruption/        corruption_proxy (rules, mutations, control API)
│   │   ├── l3l4/                  network (tc netem / iptables)
│   │   ├── p2p/bor/               p2p_attack (chaos-peer implementations)
│   │   ├── process/               process_kill
│   │   ├── stress/                cpu_stress, memory_stress
│   │   ├── time/                  clock_skew
│   │   └── verification/          post-teardown cleanup audit
│   ├── monitoring/                Prometheus client
│   ├── scenario/                  Parser + validator + types
│   ├── reporting/                 JSON reports
│   └── emergency/                 SIGINT/SIGTERM handling
├── scenarios/
│   ├── polygon-chain/             Polygon PoS scenarios
│   └── polygon-cdk/               Polygon CDK scenarios
├── docs/                          Deep-dive documentation
├── Dockerfile.chaos-utils         Sidecar image build
├── Makefile                       Build targets
├── CLAUDE.md                      AI-context primer
└── reports/                       Auto-generated run reports (gitignored)
```

### How it works

1. **Service Discovery** — Finds targets in the Kurtosis enclave by regex
   pattern. Monitoring containers (prometheus/grafana) are rejected here.
2. **Sidecar Creation** — Attaches the chaos-utils sidecar to each target's
   network namespace.
3. **Pre-fault health check** — Evaluates every non-`post_fault_only`
   criterion. Any `critical: true` failure aborts the run.
4. **Fault Injection** — Runs the fault handler for each declared fault
   (tc netem, Docker API, stress-ng, Envoy, corruption-proxy, etc.).
5. **Monitoring** — Polls Prometheus throughout the active-fault window.
6. **Teardown** — Removes faults and sidecars before evaluating
   non-`during_fault` criteria.
7. **Post-fault evaluation** — Runs the success-criteria sweep.
8. **Cleanup verification** — Asserts no residual tc qdisc, iptables
   rule, or chaos sidecar remains.

## Usage

### `run` — execute a YAML scenario

```bash
./bin/chaos-runner run --scenario <path>
./bin/chaos-runner run --scenario <path> --dry-run              # validate only
./bin/chaos-runner run --scenario <path> --enclave <name>       # override enclave
./bin/chaos-runner run --scenario <path> --set duration=10m     # override a field
./bin/chaos-runner run --scenario <path> --format json          # text | json | tui
./bin/chaos-runner run --scenario <path> --verbose              # debug logging
./bin/chaos-runner run --scenario <path> --config <path>        # custom config
# Emergency stop: Ctrl+C
```

### Example output

```
[INJECT] Injecting faults...
  ✓ Fault injected successfully
[MONITOR] Monitoring for: 3m0s
[DETECT] Evaluating success criteria...
  ✓ PASSED: healthy_validators_continue (74.67 blocks/min)
  ✓ PASSED: chain_head_advances (60.00 blocks/min)
[TEST SUMMARY] PASSED
  Targets: 1, Faults: 1, Cleanup: 5 succeeded
```

## Scenario Definition

Scenarios are YAML files parsed by `pkg/scenario/`. Minimal shape:

```yaml
apiVersion: chaos.polygon.io/v1
kind: ChaosScenario
metadata:
  name: validator-network-partition
  description: Isolate a validator to test BFT consensus.
  tags: [network, partition]

spec:
  targets:
    - selector:
        type: kurtosis_service
        enclave: "${ENCLAVE_NAME}"
        pattern: "l2-cl-1-heimdall-v2-bor-validator"
      alias: victim_validator

  duration: 5m
  warmup: 30s
  cooldown: 30s

  faults:
    - phase: partition_consensus
      description: Block Heimdall consensus traffic
      target: victim_validator
      type: network
      params:
        device: eth0
        packet_loss: 100
        target_ports: "26656,26657"
        target_proto: tcp,udp

  success_criteria:
    - name: healthy_validators_continue
      description: Remaining validators keep producing blocks
      type: prometheus
      query: increase(cometbft_consensus_height{job=~"l2-cl-[2-8]-heimdall-v2-bor-validator"}[3m])
      threshold: "> 0"
      critical: true
```

See [`scenarios/CLAUDE.md`](scenarios/CLAUDE.md) for the authoring rules
(PromQL conventions, success-criteria idioms, per-fault-type guidance).

### Fault types

Authoritative registration: `pkg/scenario/validator/validator.go::validateFaultType`.
Dispatch: `pkg/injection/injector.go::InjectFault`.

| `type:`                                            | Handler                          | Sidecar tool           |
| -------------------------------------------------- | -------------------------------- | ---------------------- |
| `network`                                          | `pkg/injection/l3l4/`           | tc netem + iptables    |
| `connection_drop`                                  | `pkg/injection/firewall/`       | iptables               |
| `dns`                                              | `pkg/injection/dns/`            | iptables + resolv.conf |
| `container_restart`, `container_kill`, `container_pause` | `pkg/injection/container/` | Docker API             |
| `process_kill`                                     | `pkg/injection/process/`        | kill in namespace      |
| `cpu_stress` (alias `cpu`)                        | `pkg/injection/stress/`         | stress-ng              |
| `memory_stress` (aliases `memory`, `memory_pressure`) | `pkg/injection/stress/`     | stress-ng              |
| `disk_io`, `disk_fill`, `file_delete`, `file_corrupt` | `pkg/injection/disk/`       | dd / truncate / rm     |
| `clock_skew`                                       | `pkg/injection/time/`           | libfaketime / date     |
| `http_fault`                                       | `pkg/injection/http/`           | Envoy                  |
| `corruption_proxy`                                 | `pkg/injection/http/corruption/`| corruption-proxy       |
| `p2p_attack`                                       | `pkg/injection/p2p/bor/`        | chaos-peer             |

Legacy umbrella types `disk`, `process`, `custom` are accepted by the
validator but prefer the specific type.

### Fault parameters

Keys are passed via `params:` on each fault. Only the listed keys are
recognised; unknown keys are silently ignored. Numeric values accept
either int or float (YAML decodes `5` as int and `5.0` as float).

#### `network` — tc netem + iptables

| Param                 | Type    | Default  | Notes                                                   |
| --------------------- | ------- | -------- | ------------------------------------------------------- |
| `device`              | string  | `eth0`   | Interface inside the target netns.                      |
| `latency`             | int ms  | 0        | Fixed delay per packet.                                 |
| `packet_loss`         | float % | 0        | 0–100. Accepts `"50%"` string too.                      |
| `bandwidth`           | int     | 0        | Rate cap, kbit/s.                                       |
| `reorder`             | int %   | 0        | Reorder probability. Requires `latency > 0`.            |
| `reorder_correlation` | int %   | 0        | Correlation for the reorder distribution.               |
| `corrupt`             | float % | 0        | Packet corruption probability.                          |
| `duplicate`           | float % | 0        | Packet duplication probability.                         |
| `target_ports`        | string  | —        | CSV ports (e.g., `"26656,26657"`).                     |
| `target_proto`        | string  | —        | `tcp`, `udp`, or `tcp,udp`.                            |

At least one of latency / packet_loss / bandwidth / reorder / corrupt /
duplicate must be set (validated in `pkg/injection/l3l4/tc_params.go`).

#### `connection_drop` — iptables

| Param          | Type    | Default | Notes                                               |
| -------------- | ------- | ------- | --------------------------------------------------- |
| `rule_type`    | string  | `drop`  | `drop` or `reject`.                                 |
| `target_ports` | string  | —       | CSV ports.                                          |
| `target_proto` | string  | `tcp`   | `tcp`, `udp`, or `tcp,udp`.                        |
| `probability`  | float   | 0.1     | 0.0–1.0 per-packet drop probability.                |

#### `dns`

| Param          | Type    | Default | Notes                                   |
| -------------- | ------- | ------- | --------------------------------------- |
| `delay_ms`     | int     | 2000    | DNS query delay.                        |
| `failure_rate` | float % | 0       | 0–100, chance of DNS failure response.  |

#### `container_restart`

| Param           | Type | Default | Notes                                                     |
| --------------- | ---- | ------- | --------------------------------------------------------- |
| `grace_period`  | int  | 10      | Seconds before forced stop.                               |
| `restart_delay` | int  | 0       | Seconds to wait between stop and start.                   |
| `stagger`       | int  | 0       | Seconds between targets; 0 = truly simultaneous restart.  |

#### `container_kill`

| Param           | Type   | Default    | Notes                                   |
| --------------- | ------ | ---------- | --------------------------------------- |
| `signal`        | string | `SIGKILL`  | Any signal name Docker accepts.         |
| `restart`       | bool   | true       | Start the container back up after kill. |
| `restart_delay` | int    | 0          | Seconds to wait before restart.         |

#### `container_pause`

| Param      | Type              | Default | Notes                                                        |
| ---------- | ----------------- | ------- | ------------------------------------------------------------ |
| `duration` | string / int / float | 0    | `"45s"`, `"2m"`, or seconds as a number. Required if `unpause: true`. |
| `unpause`  | bool              | true    | Automatically unpause after `duration`.                      |

#### `process_kill`

| Param             | Type    | Default | Notes                                         |
| ----------------- | ------- | ------- | --------------------------------------------- |
| `process_pattern` | string  | —       | Regex matched against `ps` output.            |
| `signal`          | string  | `TERM`  | Signal to send.                               |
| `kill_children`   | bool    | false   | Also kill descendant processes.               |

#### `cpu_stress`

| Param         | Type | Default    | Notes                                  |
| ------------- | ---- | ---------- | -------------------------------------- |
| `method`      | string | `stress` | `stress` or `stress-ng`.              |
| `cpu_percent` | int  | 50         | Per-core load percentage.              |
| `cores`       | int  | 1          | Cores to stress.                       |

#### `memory_stress` / `memory_pressure`

| Param       | Type   | Default    | Notes                                |
| ----------- | ------ | ---------- | ------------------------------------ |
| `method`    | string | `stress`   | `stress` or `stress-ng`.             |
| `memory_mb` | int    | 512        | Memory to allocate.                  |

#### `disk_io`

| Param           | Type    | Default | Notes                                                                  |
| --------------- | ------- | ------- | ---------------------------------------------------------------------- |
| `io_latency_ms` | int     | 200     | Legacy name — controls `dd` worker count. Higher = more contention.    |
| `target_path`   | string  | —       | Filesystem path inside the container (e.g., `/var/lib/bor/bor/chaindata`). |
| `operation`     | string  | `all`   | `read`, `write`, or `all`.                                             |
| `method`        | string  | —       | Injector-specific variant (see `pkg/injection/disk/`).                 |

#### `disk_fill`

| Param           | Type    | Default | Notes                                            |
| --------------- | ------- | ------- | ------------------------------------------------ |
| `fill_percent`  | int %   | —       | Target fill level for the filesystem.            |
| `target_path`   | string  | —       | Directory to fill.                               |
| `size_mb`       | int     | —       | Absolute size (used when `fill_percent` not set).|

#### `file_delete` / `file_corrupt`

| Param            | Type    | Default | Notes                                                   |
| ---------------- | ------- | ------- | ------------------------------------------------------- |
| `target_path`    | string  | —       | Path to operate on.                                     |
| `file_name`      | string  | —       | File (or glob) inside `target_path`.                    |
| `recursive`      | bool    | false   | Recursive delete.                                       |
| `backup_first`   | bool    | true    | Snapshot the file before corrupting (for restore).      |
| `corrupt_bytes`  | int     | —       | `file_corrupt`: how many bytes to overwrite.            |
| `corrupt_offset` | int     | 0       | `file_corrupt`: byte offset at which to overwrite.      |

#### `clock_skew`

| Param          | Type    | Default | Notes                                   |
| -------------- | ------- | ------- | --------------------------------------- |
| `offset`       | string  | —       | Duration like `"+5m"`, `"-30s"`.        |
| `disable_ntp`  | bool    | true    | Stop NTP so skew doesn't drift back.    |

#### `http_fault` — Envoy-based L7

| Param              | Type    | Default | Notes                                                 |
| ------------------ | ------- | ------- | ----------------------------------------------------- |
| `target_port`      | int     | —       | Upstream port to intercept.                           |
| `abort_code`       | int     | —       | HTTP status to return for aborted requests.           |
| `abort_percent`    | float % | 0       | 0–100 probability of abort.                           |
| `delay_ms`         | int     | 0       | Injected request delay.                               |
| `delay_percent`    | float % | 0       | 0–100 probability of delay.                           |
| `body_override`    | string  | —       | Response body replacement.                            |
| `header_overrides` | map     | —       | Response header overrides.                            |

#### `corruption_proxy` — JSON-aware semantic corruption

| Param         | Type   | Default | Notes                                                           |
| ------------- | ------ | ------- | --------------------------------------------------------------- |
| `target_port` | int    | —       | `1317` for Heimdall REST, `8545` for Bor JSON-RPC.              |
| `rules_yaml`  | string | —       | Inline rule list. See the full reference: [`scenarios/polygon-chain/semantic/rules/_REFERENCE.yaml`](scenarios/polygon-chain/semantic/rules/_REFERENCE.yaml). |

#### `p2p_attack` — chaos-peer devp2p attacks

| Param        | Type    | Default | Notes                                                        |
| ------------ | ------- | ------- | ------------------------------------------------------------ |
| `attack`     | string  | —       | Attack name (malformed_blocks, hash_flood, fork_equivocation, …). |
| `enode_url`  | string  | —       | Target Bor enode URL (auto-discovered when empty).           |
| `rpc_url`    | string  | —       | Bor JSON-RPC URL for enode discovery.                        |
| `fork_block` | int     | —       | Block height to fork at (attack-specific).                   |
| `count`      | int     | —       | Attack-specific volume.                                      |
| `interval`   | string  | —       | Duration like `"100ms"` between packets.                     |

## Built-in scenarios

Scenarios live under `scenarios/polygon-chain/` (PoS) and
`scenarios/polygon-cdk/` (CDK chains). List everything with:

```bash
find scenarios/polygon-chain -name '*.yaml' | sort
find scenarios/polygon-cdk   -name '*.yaml' | sort
```

### Polygon PoS categories

| Directory         | Focus                                                                  | Representative scenarios                                                          |
| ----------------- | ---------------------------------------------------------------------- | --------------------------------------------------------------------------------- |
| `network/`        | L3/L4 faults: partition, latency, packet loss, reorder, throttle.      | `single-node-isolation`, `three-validator-full-isolation`, `bor-p2p-bandwidth-throttle`, `progressive-partition-expansion`, `two-phase-partition-escalation` |
| `applications/`   | Container lifecycle, crash, restart, OOM, operator mistakes.           | `simultaneous-validator-restart`, `rolling-restart`, `sigkill-mid-write`, `oom-kill-recovery`, `heimdall-restart-bor-running`, `bor-restart-heimdall-running` |
| `disk/`           | Disk space / metadata corruption.                                      | `disk-fill-exhaustion`, `pebbledb-metadata-corruption-minor`, `pebbledb-metadata-corruption-severe` |
| `semantic/`       | `corruption_proxy` app-level HTTP corruption.                          | `checkpoint-hash-corruption`, `span-empty-producers`, `span-wrong-chain-id`, `state-sync-truncation`, `bor-rpc-stale-height`, `ve-*` |
| `compound/`       | Multi-fault composites.                                                | `disk-io-plus-network-latency`, `kill-during-disk-io-delay`, `heimdall-grpc-blackhole-bor-split`, `three-phase-nemesis`, `shifting-fault-combinations` |
| `boundary/`       | Sprint / span / epoch boundary edge cases.                             | `span-boundary-partition`, `rapid-span-transitions`, `fork-at-sprint-span-collision`, `validator-exit-during-checkpoint` |

### Polygon CDK categories

Each CDK chain variant (`cdk-erigon-rollup`, `cdk-erigon-validium`,
`cdk-erigon-sovereign-pessimistic`, `cdk-erigon-sovereign-ecdsa-multisig`,
`op-geth`, `op-succinct`) carries a parallel structure:

```
scenarios/polygon-cdk/<variant>/
├── applications/   container and service faults
├── cpu-memory/     stress tests
├── filesystem/     disk faults
└── network/        L3/L4 faults
```

## Prometheus metrics

Canonical metrics used by built-in success criteria. Full reference in
[`docs/metrics-reference.md`](docs/metrics-reference.md).

**Consensus (Heimdall)**
- `cometbft_consensus_height`
- `cometbft_consensus_validators`
- `cometbft_consensus_validator_missed_blocks`

**Execution (Bor)**
- `chain_head_block`
- `chain_checkpoint_latest`
- `rpc_duration_eth_blockNumber_success`

**Checkpoints / Heimdall API**
- `heimdallv2_checkpoint_api_calls_total`
- `heimdallv2_checkpoint_api_calls_success_total`
- `heimdallv2_bor_api_calls_total`
- `heimdallv2_bor_api_calls_success_total`

**Process / liveness**
- `up`
- `system_cpu_goroutines` (more reliable than `up` for some Heimdall builds)

### Query conventions

Validator 4 is the reserved fault target — exclude it from "is the
chain healthy?" queries so the reference set stays noise-free.

```promql
# Chain is producing blocks (healthy set only)
min(rate(chain_head_block{job=~"l2-el-[1235678]-bor-heimdall-v2-validator"}[3m])) > 0

# Consensus is advancing (healthy set only)
sum(increase(cometbft_consensus_height{job=~"l2-cl-[1235678]-heimdall-v2-bor-validator"}[2m])) > 0

# Average block interval
rate(cometbft_consensus_block_interval_seconds_sum[5m])
  / rate(cometbft_consensus_block_interval_seconds_count[5m])
```

Avoid subqueries (`[X:Y]`) — the runner does not support them.

## Test reports

```bash
# One JSON report per run
reports/test-20260423-154326-test-1745462606.json
# Contents: scenario metadata, resolved targets, faults injected,
# per-criterion results, cleanup summary
```

The directory is auto-created and rotated per `reporting.keep_last_n`.

## Configuration

`config.yaml` is auto-generated on first run. Authoritative schema:
[`pkg/config/config.go`](pkg/config/config.go).

```yaml
framework:
  version: "v1"
  log_level: info        # debug | info | warn | error
  log_format: text       # text | json

kurtosis:
  enclave_name: "pos"

docker:
  sidecar_image: "jhkimqd/chaos-utils:latest"

prometheus:
  url: "http://localhost:9090"   # auto-discovered from Kurtosis when empty
  timeout: 30s
  refresh_interval: 15s

reporting:
  output_dir: "./reports"
  keep_last_n: 50

emergency:
  stop_file: "/tmp/chaos-emergency-stop"

execution:
  default_warmup: 30s
  default_cooldown: 30s
```

### Priority

1. Command-line flags (`--enclave`, `--config`, `--format`, …)
2. Environment variables (`PROMETHEUS_URL`)
3. `config.yaml`
4. `DefaultConfig()` in `pkg/config/config.go`

## Troubleshooting

### Prometheus discovery

```bash
# Print the Kurtosis-exposed Prometheus URL
kurtosis port print <enclave> prometheus http

# Or override at run time
export PROMETHEUS_URL="http://127.0.0.1:<port>"
```

### Docker permission errors

```bash
sudo usermod -aG docker $USER
newgrp docker
```

### Leftover sidecars

```bash
docker ps --filter "name=chaos-sidecar"
docker rm -f $(docker ps -aq --filter "name=chaos-sidecar")
```

### Browse scenarios

```bash
ls scenarios/polygon-chain/
find scenarios/polygon-chain -name "*.yaml" | sort
cat scenarios/polygon-chain/network/single-node-isolation.yaml
```

## Development

### Build targets

```bash
make              # build all three binaries → ./bin/
make build-runner # chaos-runner only
make build-peer   # chaos-peer only
make build-proxy  # corruption-proxy only
make build-static # static Linux sidecar binaries (CGO_ENABLED=0, stripped)
make docker       # build sidecar image → jhkimqd/chaos-utils:latest
make test         # integration tests
make vet          # go vet
make fmt          # gofmt -s -w
make fmt-check    # CI gate: fails if gofmt would change anything
make clean        # rm -rf bin/
```

### Sidecar Docker image

Two-stage build (`Dockerfile.chaos-utils`):

1. **Builder** (`golang:alpine`): compiles `corruption-proxy` and
   `chaos-peer` as static binaries.
2. **Runtime** (`ubuntu:22.04`): bundles the binaries with:
   - **Envoy** — L7 fault injection for `http_fault`
   - **iproute2** (tc) — L3/L4 network faults (netem)
   - **iptables** — connection drop, PREROUTING for proxy redirect
   - **nftables** — cleanup verification
   - **curl / jq / procps** — readiness checks

Runtime needs `--cap-add=NET_ADMIN,NET_RAW`. `chaos-runner` manages the
sidecar lifecycle — just build the image once.

## Further reading

- [`CLAUDE.md`](CLAUDE.md) — AI-context primer and safety invariants
- [`scenarios/CLAUDE.md`](scenarios/CLAUDE.md) — scenario authoring guide
- [`scenarios/polygon-chain/semantic/rules/_REFERENCE.yaml`](scenarios/polygon-chain/semantic/rules/_REFERENCE.yaml) — corruption-rule catalogue
- [`docs/pipeline-architecture.md`](docs/pipeline-architecture.md) — orchestrator deep-dive
- [`docs/metrics-reference.md`](docs/metrics-reference.md) — Prometheus metrics reference
- [`docs/scenario-expected-outcomes.md`](docs/scenario-expected-outcomes.md) — per-scenario expectations
- [`docs/resource-stress-testing.md`](docs/resource-stress-testing.md) — CPU/memory/disk patterns

## External references

- [O'Reilly — Chaos Engineering](https://www.oreilly.com/library/view/chaos-engineering/9781491988459/)
- [Kurtosis docs](https://docs.kurtosis.com/)
- [Prometheus PromQL](https://prometheus.io/docs/prometheus/latest/querying/basics/)
- [`tc-netem(8)`](https://man7.org/linux/man-pages/man8/tc-netem.8.html)
