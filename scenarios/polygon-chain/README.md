# Chaos Engineering Scenarios

This directory contains chaos engineering test scenarios specifically designed for networks running on Kurtosis. These scenarios follow O'Reilly Chaos Engineering principles to validate system resilience under various failure conditions.

## Overview

Each scenario is defined in YAML format and tests a specific failure mode or degradation pattern. The scenarios target key components of the Polygon Chain stack including:

- **Heimdall** (Tendermint-based consensus layer)
- **Bor** (EVM-compatible execution layer)
- **L1 RPC endpoints** (Ethereum mainnet connectivity)
- **RabbitMQ** (Message queue for cross-layer communication)
- **Network infrastructure** (P2P communication, RPC endpoints)

## Available Scenarios

### 1. Validator Partition (validator-partition.yaml)

**Severity**: High
**Duration**: 5 minutes
**Type**: Network partition

**Description**: Isolates a single validator from the network by blocking all consensus communication (ports 26656, 26657). Tests whether the remaining validators can maintain consensus and continue block production.

**Expected Behavior**:
- ✅ Other validators continue producing blocks without interruption
- ✅ Network consensus progresses normally with the majority
- ✅ Checkpoint submission continues from healthy validators
- ⚠️ Partitioned validator stops submitting checkpoints
- ⚠️ Partitioned validator loses peer connections
- When partition is removed, validator re-syncs and rejoins

**Success Criteria**:
- ✅ Block production rate remains > 0
- ✅ Consensus height continues increasing
- ✅ Checkpoint submission continues from healthy validators
- ⚠️ Partitioned validator peer count drops to 0

**Use Case**: Validates Byzantine fault tolerance and ensures network survives validator isolation.

---

### 2. L1 RPC Latency Spike (latency-spike-l1-rpc.yaml)

**Severity**: Medium
**Duration**: 10 minutes
**Type**: Network latency

**Description**: Introduces 500ms latency to L1 RPC communication. Tests checkpoint submission resilience when Ethereum mainnet connectivity is degraded.

**Expected Behavior**:
- ✅ L2 block production continues unaffected
- ✅ Consensus progresses without interruption
- ⚠️ Checkpoint submission may slow down but should not stop
- ⚠️ RPC request timeout handling activates
- System tolerates L1 delays gracefully
- Checkpoint queue may build up temporarily

**Success Criteria**:
- ✅ Block production continues normally
- ✅ Consensus progresses without interruption
- ⚠️ Checkpoint submission rate may decrease
- ⚠️ RPC latency increases but remains functional (< 2s P95)

**Use Case**: Tests system behavior during Ethereum mainnet congestion or network issues.

---

### 3. RabbitMQ Total Failure (rabbitmq-failure.yaml)

**Severity**: High
**Duration**: 3 minutes
**Type**: Dependency failure

**Description**: Simulates complete RabbitMQ failure by blocking all traffic to the message queue service. Tests cross-layer communication resilience.

**Expected Behavior**:
- ✅ Block production continues (Bor operates independently)
- ✅ Consensus continues (Heimdall operates independently)
- ⚠️ Cross-layer event propagation stops
- Services handle message queue unavailability
- System recovers when RabbitMQ becomes available

**Success Criteria**:
- ✅ Block production rate remains > 0
- ✅ Consensus continues progressing
- ⚠️ RabbitMQ connectivity drops to 0
- ⚠️ Message queue depth may grow

**Use Case**: Validates that critical functions (block production, consensus) don't depend on RabbitMQ availability.

---

### 4. Checkpoint Delay Cascade (checkpoint-delay-cascade.yaml)

**Severity**: Medium
**Duration**: 8 minutes
**Type**: Progressive latency

**Description**: Progressively increases latency on all validators to simulate cascading checkpoint delays. Two phases: 200ms initial delay, then 500ms after 3 minutes.

**Expected Behavior**:
- ✅ Block production continues throughout all phases
- ✅ Consensus remains operational
- ⚠️ Checkpoint submission slows progressively
- System handles increased latency gracefully
- RPC latency increases but remains functional

**Success Criteria**:
- ✅ Block production continues
- ✅ Consensus progresses
- ⚠️ Checkpoint submission eventually succeeds (within 10m window)
- ⚠️ RPC P95 latency < 10s

**Use Case**: Tests system behavior under progressive network degradation and cascading delays.

---

### 5. Bandwidth Throttle (bandwidth-throttle.yaml)

**Severity**: Medium
**Duration**: 6 minutes
**Type**: Network capacity

**Description**: Limits network bandwidth to 1 Mbps for a validator to simulate congested network conditions.

**Expected Behavior**:
- ✅ Validator maintains P2P connections despite limited bandwidth
- ✅ Block production continues across the network
- ⚠️ Throttled validator may experience slower sync
- System adapts to bandwidth constraints
- Checkpoint submission may be slower

**Success Criteria**:
- ✅ Block production continues
- ✅ Validator maintains peer connections (> 0)
- ⚠️ Checkpoint submission continues (may be slower)
- ⚠️ Peer count remains stable

**Use Case**: Validates behavior in bandwidth-constrained environments (cloud egress limits, network congestion).

---

### 6. Progressive Network Degradation (progressive-network-degradation.yaml)

**Severity**: High
**Duration**: 12 minutes
**Type**: Progressive failure

**Description**: Gradually increases network issues in three phases:
- Phase 1 (0-3m): 100ms latency
- Phase 2 (3-6m): 300ms latency, 5% packet loss
- Phase 3 (6-9m): 500ms latency, 10% packet loss

**Expected Behavior**:
- ✅ Network continues operating with majority of healthy validators
- ⚠️ Degraded validator progressively loses functionality
- ⚠️ Degraded validator loses peer connections
- ⚠️ Checkpoint submission from degraded validator stops
- Overall network remains operational

**Success Criteria**:
- ✅ Overall block production continues
- ✅ Consensus progresses
- ⚠️ Degraded validator loses peers (< 5)
- ⚠️ Degraded validator stops checkpoints
- ⚠️ Overall checkpoint submission continues from healthy validators

**Use Case**: Tests resilience against progressive network failures and validates graceful degradation.

---

### 7. Split-Brain Partition (split-brain-partition.yaml)

**Severity**: Critical
**Duration**: 5 minutes
**Type**: Network partition

**Description**: Creates a bidirectional network partition splitting validators into two groups. Each group cannot communicate with the other on consensus ports (26656, 26657).

**Expected Behavior**:
- ✅ Network with majority continues block production
- ✅ Majority partition maintains consensus
- ⚠️ Minority partition loses consensus
- ⚠️ Partitioned validators lose peer connections
- ✅ Checkpoint submission continues from majority
- Network recovers when partition is removed

**Success Criteria**:
- ✅ Block production continues (from majority)
- ✅ Consensus progresses (in majority partition)
- ⚠️ Partitioned validators lose peers (< 3)
- ✅ Checkpoint submission continues from majority
- ⚠️ RPC remains functional (P95 < 5s)

**Use Case**: Tests Byzantine fault tolerance and validates that the network can survive a split-brain scenario.

---

## Prerequisites

### 1. Kurtosis Environment

Deploy a Polygon Chain devnet using Kurtosis:

```bash
cd /path/to/kurtosis-pos
kurtosis run . --enclave polygon-chain
```

Verify all services are running:
```bash
kurtosis enclave inspect polygon-chain
```

### 2. Prometheus

Ensure Prometheus is accessible and scraping metrics from the Polygon Chain network. Default URL: `http://localhost:9090`

Verify Prometheus connectivity:
```bash
curl http://localhost:9090/api/v1/query?query=up
```

### 3. Chaos Runner

Build and configure the chaos-runner CLI:

```bash
cd /path/to/chaos-utils
make build-runner
./bin/chaos-runner init --enclave polygon-chain
```

### 4. Docker

Ensure Docker is installed and the chaos-utils sidecar image is available:

```bash
docker pull jhkimqd/chaos-utils:latest
```

## Running Scenarios

### Basic Execution

Run a single scenario:

```bash
./bin/chaos-runner run --scenario scenarios/polygon-chain/validator-partition.yaml
```

### With Custom Duration

Override the default duration:

```bash
./bin/chaos-runner run \
  --scenario scenarios/polygon-chain/validator-partition.yaml \
  --set duration=10m
```

### With Custom Enclave

Specify a different Kurtosis enclave:

```bash
./bin/chaos-runner run \
  --scenario scenarios/polygon-chain/validator-partition.yaml \
  --set spec.targets[0].selector.enclave=my-enclave
```

### Validate Before Running

Check scenario validity before execution:

```bash
./bin/chaos-runner validate scenarios/polygon-chain/validator-partition.yaml
```

### List All Scenarios

View available scenarios:

```bash
./bin/chaos-runner list scenarios
```

## Interpreting Results

### Test Status

- **PASSED**: All critical success criteria met
- **FAILED**: One or more critical success criteria failed
- **COMPLETED**: Test finished but some non-critical criteria not met

### Success Criteria Types

- **Critical (✅)**: Must pass for test to succeed
- **Non-critical (⚠️)**: Expected behavior but not required for success

### Report Generation

Generate HTML report after test completion:

```bash
./bin/chaos-runner report <test-id> --format html --output report.html
```

Generate text summary:

```bash
./bin/chaos-runner report <test-id> --format text
```

Compare multiple test runs:

```bash
./bin/chaos-runner report <test-id-1> <test-id-2> --compare --format html --output comparison.html
```

## Monitoring During Tests

### Real-time Metrics

Monitor key metrics during test execution using Prometheus:

```bash
# Block production rate
curl 'http://localhost:9090/api/v1/query?query=rate(bor_blockchain_head_block[1m])'

# Consensus height
curl 'http://localhost:9090/api/v1/query?query=rate(heimdall_consensus_height[1m])'

# Peer counts
curl 'http://localhost:9090/api/v1/query?query=heimdall_p2p_peers'

# Checkpoint submission
curl 'http://localhost:9090/api/v1/query?query=rate(heimdall_checkpoint_submitted_total[5m])'
```

### Logs

Watch chaos-runner logs:
```bash
./bin/chaos-runner run --scenario <scenario> --log-level debug
```

Watch target service logs:
```bash
kurtosis service logs polygon-chain <service-name>
```

## Emergency Stop

If a test needs to be stopped immediately:

### Method 1: Ctrl+C
Press `Ctrl+C` in the terminal running chaos-runner

### Method 2: Emergency Stop File
```bash
touch /tmp/chaos-emergency-stop
```

### Method 3: Stop Command
```bash
./bin/chaos-runner stop --all
```

### Verify Cleanup

After emergency stop, verify all faults are removed:

```bash
# Get target container ID
CONTAINER=$(docker ps --filter name=<service-name> --format "{{.ID}}")

# Check for tc rules (should only show default pfifo_fast)
PID=$(docker inspect -f '{{.State.Pid}}' $CONTAINER)
sudo nsenter -t $PID -n tc qdisc show dev eth0

# Check for nftables rules (should not contain "chaos_utils")
sudo nsenter -t $PID -n nft list tables
```

## Creating Custom Scenarios

### Scenario Template

```yaml
apiVersion: chaos.polygon.io/v1
kind: ChaosScenario
metadata:
  name: my-custom-scenario
  description: Brief description of what this tests
  tags: [category, severity]
  author: your-name
  version: "0.0.1"

spec:
  targets:
    - selector:
        type: kurtosis_service
        enclave: "${ENCLAVE_NAME}"
        pattern: "service-name-pattern"
      alias: target_alias

  duration: 5m
  warmup: 30s
  cooldown: 30s

  faults:
    - phase: fault_phase_name
      description: What this fault does
      target: target_alias
      type: network
      params:
        device: eth0
        # Fault parameters here

  success_criteria:
    - name: criterion_name
      description: What this checks
      type: prometheus
      query: prometheus_query_here
      threshold: "> 0"
      critical: true

  metrics:
    - metric_name_1
    - metric_name_2
```

### Available Fault Parameters

**Latency**:
- `latency: <ms>` - Added latency in milliseconds (fixed delay)

**Packet Loss**:
- `packet_loss: <percent>` - Packet loss percentage (0-100)
- `packet_loss_correlation: <percent>` - Loss correlation

**Bandwidth**:
- `bandwidth: <kbps>` - Bandwidth limit in kilobits per second

**Targeting**:
- `target_ports: "port1,port2"` - Specific ports to target
- `target_proto: "tcp,udp"` - Protocols to affect
- `target_ips: "ip1,ip2"` - Specific IPs (not yet implemented)

**Duration** (per fault):
- `duration: <duration>` - How long to apply fault (overrides scenario duration)
- `delay: <duration>` - Delay before applying fault

## Common Issues and Troubleshooting

### Issue: "No matching services found"

**Cause**: Service name pattern doesn't match any Kurtosis services

**Solution**:
```bash
# List available services
./bin/chaos-runner discover --enclave polygon-chain

# Update pattern in scenario YAML
```

### Issue: "Prometheus query returned no data"

**Cause**: Metric not available or Prometheus not scraping target

**Solution**:
```bash
# Check if metric exists
curl 'http://localhost:9090/api/v1/label/__name__/values' | grep <metric>

# Check Prometheus targets
curl 'http://localhost:9090/api/v1/targets'
```

### Issue: "Cleanup verification failed"

**Cause**: tc/iptables rules still present after cleanup

**Solution**:
```bash
# Manual cleanup
./chaos-utils/comcast --device eth0 --stop

# Or restart the target container
kurtosis service stop polygon-chain <service-name>
kurtosis service start polygon-chain <service-name>
```

### Issue: "Sidecar creation failed"

**Cause**: Docker permissions or image not available

**Solution**:
```bash
# Check Docker permissions
docker ps

# Pull sidecar image
docker pull jhkimqd/chaos-utils:latest

# Check available images
docker images | grep chaos-utils
```

## Best Practices

### 1. Start with Validation

Always validate scenarios before running:
```bash
./bin/chaos-runner validate <scenario-file>
```

### 2. Use Warmup Periods

Allow the system to stabilize before injecting faults:
```yaml
warmup: 30s  # Recommended minimum
```

### 3. Monitor During Tests

Keep Prometheus dashboards open to observe system behavior in real-time.

### 4. Save Reports

Always generate and save reports for later analysis:
```bash
./bin/chaos-runner report <test-id> --format html --output reports/$(date +%Y%m%d-%H%M%S).html
```

### 5. Test in Stages

- Start with low-severity scenarios
- Gradually increase severity
- Test one failure mode at a time
- Combine scenarios only after individual validation

### 6. Document Unexpected Behavior

If a test reveals unexpected behavior:
- Save the test report
- Capture relevant logs
- Document reproduction steps
- File issues with the Polygon Chain team

## Metrics Reference

### Block Production

- `bor_blockchain_head_block` - Current block height
- `rate(bor_blockchain_head_block[1m])` - Block production rate

### Consensus

- `heimdall_consensus_height` - Current consensus height
- `rate(heimdall_consensus_height[1m])` - Consensus progress rate
- `heimdall_validator_voting_power` - Validator voting power

### Checkpoints

- `heimdall_checkpoint_submitted_total` - Total checkpoints submitted
- `rate(heimdall_checkpoint_submitted_total[5m])` - Checkpoint submission rate

### Networking

- `heimdall_p2p_peers` - Number of Heimdall P2P peers
- `bor_p2p_peers` - Number of Bor P2P peers

### RPC

- `bor_rpc_duration_seconds_bucket` - RPC request duration histogram
- `histogram_quantile(0.95, rate(bor_rpc_duration_seconds_bucket[5m]))` - P95 latency

## Safety

These scenarios are designed for **testing environments only**. Running them against production systems can cause:
- Service degradation
- Data inconsistency
- Network outages

Always ensure you have:
- ✅ Monitoring in place
- ✅ Emergency stop access (`touch /tmp/chaos-emergency-stop`)
- ✅ Backup and recovery procedures
- ✅ Stakeholder approval for chaos tests

## Contributing

### Adding New Scenarios

1. Create YAML file in `scenarios/polygon-chain/`
2. Follow the template structure
3. Validate the scenario
4. Test against live Kurtosis deployment
5. Document expected outcomes
6. Update this README

### Reporting Issues

If you encounter issues with scenarios:
- Include the scenario YAML
- Attach test report JSON
- Provide Kurtosis/Prometheus versions
- Include relevant error messages

## References

- [Chaos Engineering: System Resiliency in Practice (O'Reilly)](https://www.oreilly.com/library/view/chaos-engineering/9781492043850/)
- [Polygon Chain Architecture](https://wiki.polygon.technology/)
- [Kurtosis Documentation](https://docs.kurtosis.com/)
- [Prometheus Query Language](https://prometheus.io/docs/prometheus/latest/querying/basics/)

## License

Part of the chaos-utils project. See main repository LICENSE for details.
