# Chaos Runner

A comprehensive chaos engineering framework for testing the resilience of networks using Kurtosis Devnets. Provides declarative scenario definitions, automated fault injection, integrated observability, and emergency stop mechanisms.

## What is Chaos Runner?

Chaos Runner helps you systematically test your Polygon Chain network by:
- Injecting faults (network degradation, container restarts, CPU/memory stress, disk I/O) into specific services
- Monitoring system behavior via Prometheus metrics
- Evaluating success criteria automatically
- Generating detailed test reports

### Key Features

- **Two execution modes**: `run` for declarative YAML scenarios, `fuzz` for randomized fault generation
- **Steady-state hypothesis**: Pre-fault health check verifies the system is healthy before injection; post-fault evaluation confirms recovery after faults are removed
- **Prometheus-safe**: Prometheus is enforced as observability-only — any selector that resolves to a monitoring container is rejected at runtime
- **EVM precompile invariant testing**: Every fuzz round automatically verifies that known EVM precompiles (0x01–0x09) return correct outputs and that non-precompile addresses return empty — detecting silent regressions caused by hard-fork upgrades
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
# Clone repository
cd chaos-utils

# Build the binary
make build-runner

# Binary available at: ./bin/chaos-runner
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

### Components

```
chaos-utils/
├── cmd/chaos-runner/          # CLI entry point (run + fuzz subcommands)
├── pkg/
│   ├── core/orchestrator/     # State machine (PARSE → WARMUP → [pre-check] → INJECT → MONITOR → TEARDOWN → DETECT)
│   ├── fuzz/                  # Randomized scenario generation and execution
│   │   └── precompile/        # EVM precompile registry and invariant checks
│   ├── discovery/             # Kurtosis & Docker service discovery
│   ├── injection/             # Fault injection (network, container, stress, disk, DNS)
│   ├── monitoring/            # Prometheus integration & metrics collection
│   ├── scenario/              # YAML parser & validator
│   ├── reporting/             # Test results & logging
│   └── emergency/             # Emergency stop & cleanup
├── scenarios/polygon-chain/   # Built-in test scenarios (network/, applications/, cpu-memory/, filesystem/)
└── reports/                   # Test execution reports (auto-generated)
```

### How It Works

1. **Service Discovery**: Finds target containers in Kurtosis enclave by pattern matching. Any selector that resolves to `prometheus` or `grafana` is rejected — monitoring infrastructure is never a fault target.
2. **Sidecar Creation**: Attaches chaos-utils sidecar to target's network namespace
3. **Pre-fault health check**: Evaluates all success criteria before injection. If any critical criterion fails the experiment aborts — the system must be in steady state before faults are applied.
4. **Fault Injection**: Injects faults via sidecar (comcast/tc for network, Docker API for container lifecycle, stress for resources)
5. **Monitoring**: Collects Prometheus metrics during test execution
6. **Teardown**: Removes all faults and sidecars *before* evaluating criteria, ensuring Prometheus can scrape cleanly without network faults on the scrape path
7. **Post-fault evaluation**: Checks success criteria after recovery (e.g., "validators resumed block production")
8. **Cleanup**: Destroys sidecars and verifies no tc/iptables rules remain

**Safety Features**:
- Pre-flight cleanup removes remnants from previous tests
- Automatic `comcast --stop` before each fault injection
- Emergency stop via Ctrl+C (SIGINT/SIGTERM handling)
- Cleanup verification ensures no tc/iptables rules remain
- Prometheus is never a fault target — enforced at container resolution time for both `run` and `fuzz` modes
- Prometheus misconfiguration is a hard failure — if success criteria are defined but Prometheus is unreachable, the experiment fails rather than silently passing
- RPC node unavailability is a soft failure — EVM precompile criteria log a warning but never abort an experiment (the RPC node may itself be under fault injection)

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

### `fuzz` — Randomized fault injection

```bash
# Run 10 random fault rounds (default)
./bin/chaos-runner fuzz --enclave pos

# Compound mode: 2 simultaneous faults per round
./bin/chaos-runner fuzz --enclave pos --compound-only

# Up to 3 simultaneous faults
./bin/chaos-runner fuzz --enclave pos --compound-only --max-faults 3

# Inject only during active checkpoint signing
./bin/chaos-runner fuzz --enclave pos --trigger checkpoint

# Reproduce a specific run
./bin/chaos-runner fuzz --enclave pos --seed 42 --rounds 5

# Preview without executing
./bin/chaos-runner fuzz --enclave pos --dry-run
```

**Fuzz flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--enclave` | — | Kurtosis enclave name (required) |
| `--rounds` | 10 | Number of fuzz rounds |
| `--compound-only` | false | Only generate multi-fault scenarios |
| `--single-only` | false | Only generate single-fault scenarios |
| `--max-faults` | 2 | Max simultaneous faults in compound mode |
| `--trigger` | `any` | Prometheus condition before injecting |
| `--seed` | 0 (auto) | Seed for reproducibility |
| `--dry-run` | false | Print without executing |
| `--log` | `reports/fuzz_log.jsonl` | JSONL run log path |

**Available triggers:**

| Trigger | Description |
|---------|-------------|
| `any` | Inject immediately (default) |
| `checkpoint` | Wait for active Heimdall checkpoint signing |
| `post_restart` | Wait until just after a service restart |
| `high_load` | Wait for sustained Bor block production |

**Session behaviour:**

- Each round applies the same steady-state hypothesis as `run`: pre-fault health check → inject → monitor → teardown → post-fault evaluation
- Every round automatically includes two EVM precompile checks (see [EVM Precompile Invariant Testing](#evm-precompile-invariant-testing) below)
- If a **critical** success criterion fails (e.g. block production stops), the session halts immediately and prints a reproduction command
- Ctrl+C (SIGINT) cleanly stops the session after the current round completes cleanup
- A session summary is written to `reports/fuzz_summary_<timestamp>.json` with per-round results, pass/fail counts, and a `--seed` reproduce command if any round failed

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

## EVM Precompile Invariant Testing

Every fuzz round automatically appends two non-critical `rpc`-type success criteria to the generated scenario:

1. **Known precompile check** — a randomly selected precompile from the registry (addresses 0x01–0x09 plus any Polygon-specific entries) is called with a fixed test vector; the return value is verified against the expected output.
2. **Unknown address check** — a randomly generated address in the range `[0x0a, 0xffff]` is called; the result must be empty (`"0x"`), confirming no contract or system precompile was silently deployed there.

These checks are evaluated in the DETECT phase (after fault teardown), the same as all other success criteria. They are marked `critical: false` so a failure logs a warning but does not abort the experiment — the RPC node may itself be under fault injection.

### Why This Matters

A new Polygon PoS EVM precompile may be introduced in a hard fork. Precompile invariant testing detects:
- **Regression**: a precompile that previously worked now returns wrong data
- **Silent activation**: a new precompile deployed at an address that was previously empty
- **Missing activation**: a precompile that should be active post-fork but returns empty

The EVM RPC endpoint is auto-discovered from the Kurtosis enclave (same mechanism as Prometheus). If discovery fails or the RPC node is unreachable, the checks are skipped gracefully.

### Adding New Polygon Precompiles

Edit `pkg/fuzz/precompile/registry.go` and add an entry to `PolygonPrecompiles`:

```go
var PolygonPrecompiles = []Entry{
    {
        Address:  "0x0000000000000000000000000000000000001000",
        Name:     "bor-validator-set",
        Input:    "0x",
        Check:    "non_empty",   // use "exact" if you know the expected return value
        Critical: true,
    },
}
```

### `rpc` Success Criterion Type

You can also write `rpc`-type criteria in hand-authored YAML scenarios:

```yaml
success_criteria:
  - name: sha256-precompile-intact
    description: SHA-256 precompile returns correct digest after fault removal
    type: rpc
    url: "0x0000000000000000000000000000000000000002"
    rpc_method: eth_call
    rpc_call_data: "0x61"       # ASCII "a"
    rpc_check: exact
    rpc_expected: "0xca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb"
    critical: false
```

**`rpc_check` values:**

| Value | Meaning |
|-------|---------|
| `exact` | Return value must equal `rpc_expected` byte-for-byte |
| `non_empty` | Return value must be non-empty (not `""` or `"0x"`) |
| `empty` | Return value must be empty (`""` or `"0x"`) |

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

**Network Faults** (`type: network`, via comcast/tc):
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
- `delay_ms`: I/O delay in milliseconds

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
| `cpu-memory/` | `cpu-starved-validator.yaml` | 8m | Validator under CPU stress |
| `filesystem/` | `slow-disk-io-validator.yaml` | 8m | Disk I/O latency injection |

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
# Single-run JSON report
reports/test-20260128-154326-test-1769582606.json
# Contains: test metadata, targets, faults injected, success criteria results, cleanup summary

# Fuzz per-round JSONL log (one line per round, appended live)
reports/fuzz_log.jsonl

# Fuzz session summary (written at end of each fuzz session)
reports/fuzz_summary_2026-02-19_16-13-09+09-00.json
# Contains: seed, enclave, total/passed/failed rounds, per-round results, stop reason,
#           reproduce command (if any round failed)
```

To reproduce a failed fuzz session exactly:

```bash
# Printed at end of session and included in fuzz_summary_*.json
chaos-runner fuzz --enclave pos --seed 42 --rounds 10
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

evm_rpc:
  url: ""                # Auto-discovered from Kurtosis enclave if empty
                         # e.g. "http://127.0.0.1:8545"

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

### Project Structure

```
chaos-utils/
├── cmd/chaos-runner/          # CLI entry point (run + fuzz subcommands)
├── pkg/
│   ├── core/orchestrator/     # State machine: PARSE → WARMUP → [pre-check] → INJECT → MONITOR → TEARDOWN → DETECT
│   ├── fuzz/                  # Randomized fault generation (sampler, generator, runner)
│   │   └── precompile/        # EVM precompile registry (0x01–0x09 + Polygon-specific)
│   ├── discovery/             # Kurtosis & Docker service discovery
│   ├── injection/             # Fault injection (network, container, stress, disk, DNS)
│   ├── monitoring/            # Prometheus integration
│   ├── scenario/              # YAML parser & validator
│   ├── reporting/             # Test results & logging
│   ├── emergency/             # Emergency stop & cleanup
│   └── config/                # Configuration management
├── scenarios/polygon-chain/   # Built-in test scenarios (network/, applications/, cpu-memory/, filesystem/)
└── reports/                   # Test execution reports (auto-generated)
```

### Build from Source

```bash
# Build the binary
make build-runner  # Creates ./bin/chaos-runner

# Clean build artifacts
make clean

# Run tests
make test
```

### Docker Image

The sidecar uses `jhkimqd/chaos-utils:latest` which includes:
- comcast (network fault injection tool)
- Standard Linux network tools (tc, iptables, nsenter)
- Envoy proxy (for future L7 fault support)

## License

Based on [tylertreat/comcast](https://github.com/tylertreat/comcast)

## References

- [O'Reilly Chaos Engineering](https://www.oreilly.com/library/view/chaos-engineering/9781491988459/)
- [Comcast Network Fault Injection](https://github.com/tylertreat/comcast)
- [Kurtosis](https://docs.kurtosis.com/)
- [Prometheus PromQL](https://prometheus.io/docs/prometheus/latest/querying/basics/)
