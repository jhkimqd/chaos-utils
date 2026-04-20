# Chaos Runner

A comprehensive chaos engineering framework for testing the resilience of networks using Kurtosis Devnets. Provides declarative scenario definitions, automated fault injection, integrated observability, and emergency stop mechanisms.

## What is Chaos Runner?

Chaos Runner helps you systematically test your Polygon Chain network by:
- Injecting faults (network degradation, container restarts, CPU/memory stress, disk I/O) into specific services
- Monitoring system behavior via Prometheus metrics
- Evaluating success criteria automatically
- Generating detailed test reports

### Key Features

- **Declarative execution**: Define fault injection scenarios in YAML and execute with `run`
- **Steady-state hypothesis**: Pre-fault health check verifies the system is healthy before injection; post-fault evaluation confirms recovery after faults are removed
- **Prometheus-safe**: Prometheus is enforced as observability-only — any selector that resolves to a monitoring container is rejected at runtime
- **Auto-Configuration**: Config auto-generated on first run
- **Declarative Scenarios**: Define tests in YAML
- **Safe by Default**: Automatic cleanup and emergency stop
- **Integrated Monitoring**: Built-in Prometheus integration
- **Production-Ready**: Sidecar isolation prevents interference


## Quick Start

### Prerequisites
- Docker (for sidecar containers)
- Kurtosis-managed Polygon Chain network running
- Prometheus endpoint accessible (for success criteria evaluation)

### Installation

```bash
cd chaos-utils

# Build everything (chaos-runner, chaos-peer, corruption-proxy)
make

# Or build just the host CLI
make build-runner

# Binaries available at: ./bin/
```

### Run Your First Test

```bash
# First run auto-generates config.yaml with defaults
./bin/chaos-runner run --scenario scenarios/polygon-chain/network/validator-partition.yaml

# Edit config.yaml to customize (enclave name, Prometheus URL, etc.)
vim config.yaml

# Run bandwidth throttle test (6 minutes)
./bin/chaos-runner run --scenario scenarios/polygon-chain/network/bandwidth-throttle.yaml
```

**Note**: Config is auto-generated on first run. You'll see:
```
⚠️  Config file not found, creating default configuration at: config.yaml
   You can edit this file to customize settings (enclave name, Prometheus URL, etc.)
```

## Architecture

### Binaries

The project produces three binaries split across two deployment contexts:

**Host (your machine / CI)**

| Binary | Source | Purpose |
|--------|--------|---------|
| `chaos-runner` | `cmd/chaos-runner/` | Main CLI. Reads YAML scenarios, discovers Kurtosis/Docker containers, orchestrates fault injection, collects Prometheus metrics, evaluates success criteria, and generates reports. Runs on the host and reaches into containers via Docker exec. |

**Sidecar (inside `jhkimqd/chaos-utils` Docker image)**

| Binary | Source | Purpose |
|--------|--------|---------|
| `corruption-proxy` | `cmd/corruption-proxy/` | HTTP reverse proxy that intercepts JSON-RPC/REST traffic between Bor and Heimdall. Applies semantic corruption rules (mutate block numbers, delete fields, inject wrong hashes) with a runtime control API to toggle rules on the fly. |
| `chaos-peer` | `cmd/chaos-peer/` | Fake devp2p peer that connects to a Bor node's RLPx port, completes the eth handshake, and sends crafted malicious protocol messages (malformed blocks, conflicting chains, hash floods, etc.). Attacks Bor's P2P layer directly, bypassing any HTTP proxy. |

`chaos-runner` is never containerized. `corruption-proxy` and `chaos-peer` live in the sidecar image alongside Envoy and Linux network tools (tc, iptables). The runner starts sidecars, injects faults through them, and tears everything down after the test.

### Project Structure

```
chaos-utils/
├── cmd/
│   ├── chaos-runner/              # Host CLI (run subcommand)
│   ├── corruption-proxy/          # Sidecar: HTTP corruption proxy
│   └── chaos-peer/                # Sidecar: devp2p fake peer
├── pkg/
│   ├── core/orchestrator/         # State machine (PARSE → WARMUP → [pre-check] → INJECT → MONITOR → TEARDOWN → DETECT)
│   ├── discovery/                 # Kurtosis & Docker service discovery
│   ├── injection/                 # Fault injectors (network, container, stress, disk, DNS, HTTP, P2P)
│   │   ├── http/corruption/       # Corruption proxy engine (rules, mutations, control API)
│   │   └── p2p/bor/               # devp2p RLPx peer + attack implementations
│   ├── monitoring/                # Prometheus integration & metrics collection
│   ├── scenario/                  # YAML parser & validator
│   ├── reporting/                 # Test results & logging
│   └── emergency/                 # Emergency stop & cleanup
├── scenarios/polygon-chain/       # Built-in test scenarios
├── Dockerfile.chaos-utils         # Sidecar image (corruption-proxy + chaos-peer + Envoy + network tools)
└── reports/                       # Test execution reports (auto-generated)
```

### How It Works

1. **Service Discovery**: Finds target containers in Kurtosis enclave by pattern matching. Any selector that resolves to `prometheus` or `grafana` is rejected — monitoring infrastructure is never a fault target.
2. **Sidecar Creation**: Attaches chaos-utils sidecar to target's network namespace
3. **Pre-fault health check**: Evaluates all success criteria before injection. If any critical criterion fails the experiment aborts — the system must be in steady state before faults are applied.
4. **Fault Injection**: Injects faults via sidecar (tc netem for network, Docker API for container lifecycle, stress-ng for resources)
5. **Monitoring**: Collects Prometheus metrics during test execution
6. **Teardown**: Removes all faults and sidecars *before* evaluating criteria, ensuring Prometheus can scrape cleanly without network faults on the scrape path
7. **Post-fault evaluation**: Checks success criteria after recovery (e.g., "validators resumed block production")
8. **Cleanup**: Destroys sidecars and verifies no tc/iptables rules remain

**Safety Features**:
- Pre-flight cleanup removes remnants from previous tests (remnant sidecars, stale tc qdiscs)
- Emergency stop via Ctrl+C (SIGINT/SIGTERM handling)
- Cleanup verification ensures no tc/iptables rules remain
- Prometheus is never a fault target — enforced at container resolution time
- Prometheus misconfiguration is a hard failure — if success criteria are defined but Prometheus is unreachable, the experiment fails rather than silently passing

## Usage

### `run` — Execute a YAML scenario

```bash
# Run a pre-written chaos scenario
./bin/chaos-runner run --scenario <path-to-yaml>

# Dry-run: validate without executing
./bin/chaos-runner run --scenario <path> --dry-run

# Override enclave or duration
./bin/chaos-runner run --scenario <path> --enclave <name> --set duration=10m

# Emergency stop: Ctrl+C
```

### Example: Validator Partition Test

```bash
./bin/chaos-runner run \
  --scenario scenarios/polygon-chain/network/validator-partition.yaml \
  --enclave pos
```

**Expected Output**:
```
[INJECT] Injecting faults...
  ✓ Fault injected successfully
[MONITOR] Monitoring for: 5m0s
[DETECT] Evaluating success criteria...
  ✓ PASSED: other_validators_continue (74.67 blocks/min)
  ✓ PASSED: bor_chain_continues (60.00 blocks/min)
[TEST SUMMARY] PASSED
  Targets: 1, Faults: 1, Cleanup: 5 succeeded
```

## Scenario Definition

Scenarios are defined in YAML and describe:
- **Targets**: Which services to affect (by pattern, label, or ID)
- **Faults**: What failures to inject (network latency, packet loss, etc.)
- **Success Criteria**: How to measure system resilience (Prometheus queries)
- **Duration**: Test timing (warmup, active fault, cooldown)

### Example Scenario

```yaml
apiVersion: chaos.polygon.io/v1
kind: ChaosScenario
metadata:
  name: validator-network-partition
  description: Isolate validator from network to test BFT consensus

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
      description: Block Heimdall consensus communication
      target: victim_validator
      type: network
      params:
        device: eth0
        packet_loss: 100
        target_ports: "26656,26657"
        target_proto: tcp,udp

  success_criteria:
    - name: other_validators_continue
      description: Other validators continue producing blocks
      type: prometheus
      query: increase(cometbft_consensus_height{job=~"l2-cl-[2-4]-heimdall-v2-bor-validator"}[1m])
      threshold: "> 0"
      window: 5m
      critical: true
```

### Fault Parameters

**Network Faults** (`type: network`, via tc netem):
- `latency`: Delay in milliseconds (e.g., 500)
- `packet_loss`: Percentage 0-100 (e.g., 50.0)
- `bandwidth`: Limit in kbit/s (e.g., 1000)
- `target_ports`: Comma-separated ports (e.g., "26656,26657")
- `target_proto`: Protocol(s) - tcp, udp, or "tcp,udp"
- `reorder`: Packet reorder percentage (via tc)

**Container Lifecycle** (`type: container_restart`, `container_kill`, `container_pause`):
- `grace_period`: Seconds before forced stop
- `stagger`: Delay between targets (0 = simultaneous)
- `restart_delay`: Seconds to wait before restart

**Resource Stress** (`type: cpu_stress`, `memory_stress`):
- `cpu_percent`: CPU load percentage (e.g., 60)
- `cores`: Number of CPU cores to stress
- `memory_mb`: Memory to consume in MB

**Disk I/O** (`type: disk_io`):
- `io_latency_ms`: controls `dd` worker count (higher = more contention)
- `target_path`: filesystem path to target (e.g., `/var/lib/bor/bor/chaindata`)
- `operation`: `read`, `write`, or `all` (default: `all`)

## Built-in Scenarios

Located in `scenarios/polygon-chain/`:

| Category | Scenario | Duration | Purpose |
|----------|----------|----------|---------|
| `network/` | `validator-partition.yaml` | 5m | Test BFT tolerance (100% packet loss) |
| `network/` | `latency-spike-l1-rpc.yaml` | 10m | L1 dependency resilience (500ms latency) |
| `network/` | `bandwidth-throttle.yaml` | 6m | Bandwidth constraint (1 Mbps limit) |
| `network/` | `checkpoint-delay-cascade.yaml` | 8m | Cascading checkpoint delays |
| `applications/` | `simultaneous-validator-restart.yaml` | 5m | All validators restart at once |
| `applications/` | `rabbitmq-failure.yaml` | 3m | Messaging failure (100% packet loss) |
| `applications/` | `bor-heimdall-link-latency.yaml` | 8m | Bor→Heimdall REST API latency (8s on port 1317) |
| `applications/` | `bor-heimdall-link-isolation.yaml` | 7m | Complete Bor→Heimdall REST isolation (100% loss on port 1317) |
| `applications/` | `span-rotation-heimdall-stall.yaml` | 10m | Heimdall P2P partition preventing span commitment |
| `cpu-memory/` | `cpu-starved-validator.yaml` | 8m | Validator under CPU stress |
| `disk/` | `disk-fill-exhaustion.yaml` | 8m | Disk space exhaustion (90% fill) |
| `compound/` | `disk-io-plus-network-latency.yaml` | 10m | Combined disk I/O + network latency |
| `compound/` | `kill-during-disk-io-delay.yaml` | 8m | SIGKILL during I/O delay (crash recovery) |

## Prometheus Metrics

### Key Metrics for Success Criteria

**Consensus Layer (Heimdall)**:
- `cometbft_consensus_height` - Consensus block height
- `cometbft_consensus_validators` - Active validator count
- `cometbft_consensus_validator_missed_blocks` - Missed blocks per validator

**Execution Layer (Bor)**:
- `chain_head_block` - Current block number
- `chain_checkpoint_latest` - Latest checkpoint number
- `rpc_duration_eth_blockNumber_success` - RPC latency

**Checkpoints**:
- `heimdallv2_checkpoint_api_calls_total` - Checkpoint API calls
- `heimdallv2_checkpoint_api_calls_success_total` - Successful calls

### Example Queries

```promql
# Check if network is producing blocks
increase(chain_head_block[1m]) > 0

# Check consensus progress (exclude validator 1)
increase(cometbft_consensus_height{job=~"l2-cl-[2-4]-heimdall-v2-bor-validator"}[1m]) > 0

# Average block time
rate(cometbft_consensus_block_interval_seconds_sum[5m]) /
rate(cometbft_consensus_block_interval_seconds_count[5m])
```

## Test Reports

Reports are auto-generated in `reports/`:

```bash
# JSON report per test run
reports/test-20260128-154326-test-1769582606.json
# Contains: test metadata, targets, faults injected, success criteria results, cleanup summary
```

## Configuration

### Auto-Generated Configuration

On first run, `config.yaml` is automatically created with sensible defaults. Edit it to customize:

```yaml
framework:
  log_level: info        # debug, info, warn, error
  log_format: text       # text or json

kurtosis:
  enclave_name: "pos"    # Your Kurtosis enclave name

prometheus:
  url: http://localhost:9090  # Auto-discovered if available
  timeout: 30s

docker:
  sidecar_image: "jhkimqd/chaos-utils:latest"  # Fault injection sidecar

reporting:
  output_dir: "./reports"
  keep_last_n: 50        # Keep last 50 test reports

execution:
  default_warmup: 30s
  default_cooldown: 30s
  max_concurrent_faults: 5

safety:
  max_duration: 1h       # Maximum test duration
  require_confirmation: true
```

### Configuration Priority

1. Command-line flags (highest priority)
2. Environment variables (e.g., `PROMETHEUS_URL`)
3. config.yaml values
4. Default values (lowest priority)

## Troubleshooting

### First Run Setup

If auto-generated config needs adjustment:

```bash
# Edit config.yaml
vim config.yaml

# Update enclave name
kurtosis:
  enclave_name: "your-enclave-name"

# Update Prometheus URL (auto-discovered by default)
prometheus:
  url: "http://127.0.0.1:PORT"
```

### Prometheus Connection Issues

```bash
# Find Prometheus port
kurtosis port print <enclave> prometheus http

# Edit config.yaml or use environment variable
export PROMETHEUS_URL="http://127.0.0.1:32906"
```

### Docker Permission Errors

```bash
# Add user to docker group
sudo usermod -aG docker $USER
newgrp docker
```

### Cleanup Verification

```bash
# Check for remnant sidecars (should be empty)
docker ps --filter "name=chaos-sidecar"

# Manual cleanup if needed
docker rm -f $(docker ps -aq --filter "name=chaos-sidecar")
```

### View Available Scenarios

```bash
# List scenario categories
ls scenarios/polygon-chain/

# List all scenario files
find scenarios/polygon-chain/ -name "*.yaml" | sort

# View scenario details
cat scenarios/polygon-chain/network/validator-partition.yaml
```

## Advanced Usage

### Override Scenario Values

```bash
# Override duration and warmup period
./bin/chaos-runner run \
  --scenario scenarios/polygon-chain/network/validator-partition.yaml \
  --set duration=10m \
  --set warmup=1m
```

### Custom Enclave

```bash
# Use a different Kurtosis enclave
./bin/chaos-runner run \
  --scenario scenarios/polygon-chain/network/validator-partition.yaml \
  --enclave my-custom-enclave
```

### Different Output Format

```bash
# Change output format
./bin/chaos-runner run \
  --scenario scenarios/polygon-chain/network/validator-partition.yaml \
  --format json  # Options: text, json, tui
```

### Verbose Logging

```bash
# Enable debug logging
./bin/chaos-runner run \
  --scenario scenarios/polygon-chain/network/validator-partition.yaml \
  --verbose
```

### Custom Config File

```bash
# Use a different config file
./bin/chaos-runner run \
  --scenario scenarios/polygon-chain/network/validator-partition.yaml \
  --config /path/to/custom-config.yaml
```

## Development

### Building

```bash
# Build everything (all three binaries) — this is the default target
make

# Build individual binaries
make build-runner    # → bin/chaos-runner
make build-peer      # → bin/chaos-peer
make build-proxy     # → bin/corruption-proxy

# Build static Linux binaries (for Docker / CI, matches Dockerfile)
make build-static    # → bin/corruption-proxy, bin/chaos-peer (CGO_ENABLED=0, stripped)

# Build the sidecar Docker image
make docker          # → jhkimqd/chaos-utils:latest

# One command: build all binaries + sidecar image
make && make docker

# Other targets
make test            # Run integration tests
make vet             # Go vet
make fmt             # gofmt -s -w
make fmt-check       # CI check: fails if gofmt would change anything
make clean           # rm -rf bin/
```

### Sidecar Docker Image

The sidecar image (`jhkimqd/chaos-utils:latest`, built from `Dockerfile.chaos-utils`) is a two-stage build:

1. **Builder stage** (golang:alpine): compiles `corruption-proxy` and `chaos-peer` as static binaries
2. **Runtime stage** (ubuntu:22.04): copies the binaries alongside:
   - **Envoy** — L7 HTTP fault injection (abort, delay, body/header override)
   - **iproute2** — tc (traffic control) for L3/L4 network faults (netem)
   - **iptables** — connection drop, port redirect (proxy PREROUTING)
   - **nftables** — verification checks for leftover rules
   - **curl / jq / procps** — readiness checks and debugging

The image requires `--cap-add=NET_ADMIN,NET_RAW` at runtime. `chaos-runner` manages the sidecar lifecycle automatically — you only need to build the image.

## References

- [O'Reilly Chaos Engineering](https://www.oreilly.com/library/view/chaos-engineering/9781491988459/)
- [Kurtosis](https://docs.kurtosis.com/)
- [Prometheus PromQL](https://prometheus.io/docs/prometheus/latest/querying/basics/)
- [tc-netem(8)](https://man7.org/linux/man-pages/man8/tc-netem.8.html)
