# Chaos Runner

A comprehensive chaos engineering framework for testing the resilience of networks using Kurtosis Devnets. Provides declarative scenario definitions, automated fault injection, integrated observability, and emergency stop mechanisms.

## What is Chaos Runner?

Chaos Runner helps you systematically test your Polygon Chain network by:
- Injecting network faults (latency, packet loss, partitions) into specific services
- Monitoring system behavior via Prometheus metrics
- Evaluating success criteria automatically
- Generating detailed test reports


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
# Set Prometheus URL
export PROMETHEUS_URL=$(kurtosis port print $ENCLAVE_NAME prometheus http)

# Run a quick 1-minute test
./bin/chaos-runner run --scenario scenarios/polygon-chain/quick-test.yaml

# Run full validator partition test (5 minutes)
./bin/chaos-runner run --scenario scenarios/polygon-chain/validator-partition.yaml
```

## Architecture

### Components

```
chaos-utils/
├── cmd/chaos-runner/          # CLI entry point
├── pkg/
│   ├── core/orchestrator/     # State machine (12 states: PARSE → COMPLETED)
│   ├── discovery/             # Kurtosis & Docker service discovery
│   ├── injection/             # Sidecar management & fault injection (comcast)
│   ├── monitoring/            # Prometheus integration & metrics collection
│   ├── scenario/              # YAML parser & validator
│   ├── reporting/             # Test results & logging
│   └── emergency/             # Emergency stop & cleanup
├── scenarios/polygon-chain/   # Built-in test scenarios
└── reports/                   # Test execution reports (auto-generated)
```

### How It Works

1. **Service Discovery**: Finds target containers in Kurtosis enclave by pattern matching
2. **Sidecar Creation**: Attaches chaos-utils sidecar to target's network namespace
3. **Fault Injection**: Executes comcast in sidecar to apply network faults (L3/L4)
4. **Monitoring**: Collects Prometheus metrics during test execution
5. **Evaluation**: Checks success criteria (e.g., "other validators continue producing blocks")
6. **Cleanup**: Removes faults and destroys sidecars automatically

**Safety Features**:
- Pre-flight cleanup removes remnants from previous tests
- Automatic `comcast --stop` before each fault injection
- Emergency stop via Ctrl+C or `/tmp/chaos-emergency-stop` file
- Cleanup verification ensures no tc/iptables rules remain

## Usage

### Available Commands

```bash
# Run a chaos test
./bin/chaos-runner run --scenario <path-to-yaml>

# Validate scenario without executing
./bin/chaos-runner run --scenario <path> --dry-run

# Discover services in enclave
./bin/chaos-runner discover --enclave <name>

# Validate scenario syntax
./bin/chaos-runner validate --scenario <path>

# Emergency stop (in another terminal)
touch /tmp/chaos-emergency-stop
```

### Example: Validator Partition Test

```bash
export PROMETHEUS_URL="http://127.0.0.1:32906"

./bin/chaos-runner run \
  --scenario scenarios/polygon-chain/validator-partition.yaml \
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

**Network Faults** (via comcast):
- `latency`: Delay in milliseconds (e.g., 500)
- `packet_loss`: Percentage 0-100 (e.g., 50.0)
- `bandwidth`: Limit in kbit/s (e.g., 1000)
- `target_ports`: Comma-separated ports (e.g., "26656,26657")
- `target_proto`: Protocol(s) - tcp, udp, or "tcp,udp"
- `target_ips`: Specific IPs to affect
- `target_cidr`: CIDR block (e.g., "10.0.0.0/8")

## Built-in Scenarios

Located in `scenarios/polygon-chain/`:

| Scenario | Duration | Fault Type | Purpose |
|----------|----------|------------|---------|
| `quick-test.yaml` | 1m | 50% packet loss | Quick validation |
| `validator-partition.yaml` | 5m | 100% packet loss | Test BFT tolerance |
| `latency-spike-l1-rpc.yaml` | 10m | 500ms latency | L1 dependency resilience |
| `rabbitmq-failure.yaml` | 3m | 100% packet loss | Messaging failure |
| `bandwidth-throttle.yaml` | 5m | 1 Mbps limit | Bandwidth constraint |
| `checkpoint-delay-cascade.yaml` | 18m | Progressive delays | Cascading failure detection |

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

Reports are auto-generated in `reports/` directory:

```bash
# JSON report
reports/test-20260128-154326-test-1769582606.json

# Contains:
# - Test metadata (ID, scenario name, duration)
# - Targets and faults injected
# - Success criteria results
# - Cleanup summary
# - Errors (if any)
```

## Configuration

Edit `config.yaml` to customize:

```yaml
framework:
  log_level: info
  log_format: text

kurtosis:
  default_enclave: "pos"

prometheus:
  url: ${PROMETHEUS_URL}
  timeout: 30s

docker:
  sidecar_image: "jhkimqd/chaos-utils:latest"

reporting:
  output_dir: "./reports"
  keep_last_n: 50

emergency:
  stop_file: "/tmp/chaos-emergency-stop"
```

## Troubleshooting

### Prometheus Connection Issues
```bash
# Check Prometheus URL
kurtosis port print <enclave> prometheus http

# Set environment variable
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

## Advanced Usage

### Override Scenario Values

```bash
./bin/chaos-runner run \
  --scenario scenarios/polygon-chain/validator-partition.yaml \
  --set duration=10m \
  --set warmup=1m
```

### Custom Enclave

```bash
./bin/chaos-runner run \
  --scenario scenarios/polygon-chain/validator-partition.yaml \
  --enclave my-custom-enclave
```

### Different Output Format

```bash
./bin/chaos-runner run \
  --scenario scenarios/polygon-chain/validator-partition.yaml \
  --format json  # or: text, tui
```

## Development

### Project Structure

- **cmd/**: CLI commands (Cobra)
- **pkg/core/**: Orchestrator & state machine
- **pkg/discovery/**: Kurtosis & Docker clients
- **pkg/injection/**: Sidecar & fault injection
- **pkg/monitoring/**: Prometheus & metrics
- **pkg/scenario/**: YAML parser & validator
- **pkg/reporting/**: Test results & logs

### Build from Source

```bash
make build-runner  # Builds ./bin/chaos-runner
make clean         # Remove binaries
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
