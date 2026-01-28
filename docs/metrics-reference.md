# Prometheus Metrics Reference

This document lists all Prometheus metrics available for monitoring Polygon Chain networks during chaos tests.

## Bor Metrics (L2 Execution Layer)

### Block Production

| Metric | Query | Description | Type |
|--------|-------|-------------|------|
| `bor_blockchain_head_block` | `bor_blockchain_head_block` | Current head block number | Gauge |
| `bor_block_production_rate` | `rate(bor_blockchain_head_block[1m])` | Blocks produced per second | Gauge |
| `bor_block_gas_used` | `rate(bor_block_gas_used[1m])` | Gas used per second | Gauge |

### RPC Performance

| Metric | Query | Description | Type |
|--------|-------|-------------|------|
| `bor_rpc_duration_seconds` | `bor_rpc_duration_seconds` | RPC call duration histogram | Histogram |
| `bor_rpc_latency_p95` | `histogram_quantile(0.95, rate(bor_rpc_duration_seconds_bucket[5m]))` | 95th percentile RPC latency | Gauge |
| `bor_rpc_latency_p99` | `histogram_quantile(0.99, rate(bor_rpc_duration_seconds_bucket[5m]))` | 99th percentile RPC latency | Gauge |

### Network

| Metric | Query | Description | Type |
|--------|-------|-------------|------|
| `bor_p2p_peers` | `bor_p2p_peers` | Number of connected P2P peers | Gauge |

## Heimdall Metrics (L2 Consensus Layer)

### Consensus

| Metric | Query | Description | Type |
|--------|-------|-------------|------|
| `heimdall_consensus_height` | `heimdall_consensus_height` | Current consensus height | Gauge |
| `heimdall_consensus_rate` | `rate(heimdall_consensus_height[1m])` | Consensus progress rate | Gauge |

### Checkpoints

| Metric | Query | Description | Type |
|--------|-------|-------------|------|
| `heimdall_checkpoint_submitted_total` | `heimdall_checkpoint_submitted_total` | Total checkpoints submitted | Counter |
| `heimdall_checkpoint_submitted` | `rate(heimdall_checkpoint_submitted_total[5m])` | Checkpoint submission rate | Gauge |
| `heimdall_checkpoint_latency` | `histogram_quantile(0.95, rate(heimdall_checkpoint_duration_seconds_bucket[5m]))` | 95th percentile checkpoint latency | Gauge |

### Validators

| Metric | Query | Description | Type |
|--------|-------|-------------|------|
| `heimdall_validator_voting_power` | `heimdall_validator_voting_power` | Validator voting power | Gauge |
| `heimdall_p2p_peers` | `heimdall_p2p_peers` | Number of connected Heimdall peers | Gauge |

## L1 Integration Metrics

| Metric | Query | Description | Type |
|--------|-------|-------------|------|
| `l1_rpc_duration_seconds` | `histogram_quantile(0.95, rate(l1_rpc_duration_seconds_bucket[5m]))` | 95th percentile L1 RPC latency | Gauge |
| `l1_checkpoint_success_total` | `rate(l1_checkpoint_success_total[1h])` | L1 checkpoint success rate | Gauge |
| `l1_state_sync_events` | `rate(l1_state_sync_events[5m])` | L1 state sync event rate | Gauge |

## RabbitMQ Metrics

| Metric | Query | Description | Type |
|--------|-------|-------------|------|
| `rabbitmq_queue_messages_ready` | `rabbitmq_queue_messages_ready` | Messages ready in queue | Gauge |
| `rabbitmq_connections` | `rabbitmq_connections` | Active RabbitMQ connections | Gauge |
| `rabbitmq_message_rate` | `rate(rabbitmq_queue_messages_published_total[1m])` | Message publication rate | Gauge |

## Using Metrics in Scenarios

### Basic Usage

```yaml
success_criteria:
  - name: block_production_continues
    type: prometheus
    query: rate(bor_blockchain_head_block[1m])
    threshold: "> 0"
    critical: true
```

### With Service Labels

```yaml
success_criteria:
  - name: validator_stopped
    type: prometheus
    query: rate(heimdall_checkpoint_submitted{service="l2-cl-1-heimdall-v2-validator"}[5m])
    threshold: "== 0"
    critical: false
```

### Rate Queries

```yaml
success_criteria:
  - name: checkpoint_rate_acceptable
    type: prometheus
    query: rate(heimdall_checkpoint_submitted_total[1h])
    threshold: "> 0.0001"  # At least 1 checkpoint per ~3 hours
    critical: true
```

### Histogram Queries

```yaml
success_criteria:
  - name: rpc_latency_acceptable
    type: prometheus
    query: histogram_quantile(0.95, rate(bor_rpc_duration_seconds_bucket[5m]))
    threshold: "< 1.0"  # Less than 1 second p95
    critical: false
```

## Threshold Operators

| Operator | Description | Example |
|----------|-------------|---------|
| `>` | Greater than | `"> 0"` |
| `<` | Less than | `"< 100"` |
| `>=` | Greater than or equal | `">= 50"` |
| `<=` | Less than or equal | `"<= 75"` |
| `==` | Equal to | `"== 0"` |
| `!=` | Not equal to | `"!= 0"` |

## Common Patterns

### Network Resilience

Check that the network continues operating despite faults:

```yaml
success_criteria:
  - name: bor_produces_blocks
    query: rate(bor_blockchain_head_block[1m])
    threshold: "> 0"

  - name: heimdall_progresses
    query: rate(heimdall_consensus_height[1m])
    threshold: "> 0"
```

### Dependency Failure Detection

Verify a dependency actually failed:

```yaml
success_criteria:
  - name: rabbitmq_isolated
    query: rabbitmq_connections
    threshold: "== 0"
```

### Performance Degradation

Detect performance impact:

```yaml
success_criteria:
  - name: latency_increased
    query: histogram_quantile(0.95, rate(bor_rpc_duration_seconds_bucket[5m]))
    threshold: "> 0.5"  # Expect latency increase
```

## Collecting Metrics

Specify metrics to collect during the test:

```yaml
spec:
  metrics:
    - bor_blockchain_head_block
    - heimdall_consensus_height
    - heimdall_checkpoint_submitted
    - bor_rpc_duration_seconds
```

These metrics will be collected at regular intervals and included in test reports.

## Best Practices

1. **Always monitor critical path metrics**
   - Block production (Bor)
   - Consensus progress (Heimdall)
   - Checkpoint submissions

2. **Use rate() for counters**
   - `rate(heimdall_checkpoint_submitted_total[5m])` not `heimdall_checkpoint_submitted_total`

3. **Choose appropriate time windows**
   - Short windows (1m) for immediate effects
   - Long windows (1h) for slow-changing metrics like checkpoints

4. **Mark critical criteria**
   - `critical: true` for must-pass conditions
   - `critical: false` for nice-to-have conditions

5. **Test your queries**
   - Validate queries in Prometheus UI first
   - Use `chaos-runner validate` to check syntax

## Troubleshooting

### Query Returns No Results

- Check that the metric exists in Prometheus
- Verify the time range is appropriate
- Check service labels match your deployment

### Threshold Always Fails

- Verify the threshold direction (>, <, etc.)
- Check the expected value range
- Test the query manually in Prometheus

### Rate Queries Return 0

- Ensure sufficient data points exist
- Use longer time windows for slow-changing metrics
- Check that the underlying counter is incrementing
