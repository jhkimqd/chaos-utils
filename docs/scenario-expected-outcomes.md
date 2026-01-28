# Scenario Expected Outcomes

This document describes the expected behavior and outcomes for each chaos engineering scenario. Use this guide to interpret test results and understand what "normal" looks like during chaos tests.

## Understanding Test Outcomes

### Test Result Types

- **PASSED**: All critical success criteria met, system behaved as expected
- **FAILED**: One or more critical success criteria failed, indicating a resilience issue
- **COMPLETED**: Test finished but some non-critical criteria not met (expected behavior)

### Metric Behavior Categories

- **Must Continue**: Critical operations that must not stop (block production, consensus)
- **May Degrade**: Operations that can slow down or temporarily fail (checkpoint submission)
- **Should Stop**: Operations that should cease on the affected service (victim validator checkpoints)
- **Should Recover**: Operations that should resume after fault is removed

---

## 1. Validator Partition (validator-partition.yaml)

### Test Objective
Validate that the network can survive a complete isolation of a single validator while maintaining consensus and block production.

### Fault Injection Phase (5 minutes)

**What happens**:
- Target validator loses all network connectivity on consensus ports (26656, 26657)
- TCP and UDP traffic blocked bidirectionally
- Validator continues running but cannot communicate with peers

**Expected metrics during fault**:

| Metric | Expected Value | Explanation |
|--------|---------------|-------------|
| `rate(bor_blockchain_head_block[1m])` | **> 0** | Other validators continue producing blocks |
| `rate(heimdall_consensus_height[1m])` | **> 0** | Consensus progresses with majority |
| `heimdall_p2p_peers{service="victim"}` | **→ 0** | Partitioned validator loses all peers |
| `rate(heimdall_checkpoint_submitted{service="victim"}[5m])` | **→ 0** | Victim stops submitting checkpoints |
| `rate(heimdall_checkpoint_submitted_total[5m])` | **> 0** | Other validators continue checkpoints |

**Expected logs**:
```
[victim-validator] Connection lost to peer: <peer-id>
[victim-validator] No active peers, waiting for connections...
[other-validators] Peer disconnected: <victim-id>
[other-validators] Consensus round completed with X/Y validators
```

### Recovery Phase (30s cooldown)

**What happens**:
- Network partition is removed
- Validator immediately starts reconnecting to peers
- Validator synchronizes missed blocks and state

**Expected metrics during recovery**:

| Metric | Expected Value | Explanation |
|--------|---------------|-------------|
| `heimdall_p2p_peers{service="victim"}` | **> 0 (increasing)** | Victim reconnects to network |
| `heimdall_consensus_height{service="victim"}` | **Catching up** | Syncing missed consensus states |
| `rate(heimdall_checkpoint_submitted{service="victim"}[5m])` | **> 0 (after sync)** | Resumes checkpoints after sync |

**Expected logs**:
```
[victim-validator] Peer connected: <peer-id>
[victim-validator] Syncing state from height X to Y
[victim-validator] Sync complete, resuming normal operation
```

### Success Indicators
- ✅ Network never stopped producing blocks
- ✅ Consensus continued with N-1 validators
- ✅ Victim validator successfully re-joined after partition removed
- ✅ No permanent state divergence

### Failure Indicators
- ❌ Block production stopped during partition
- ❌ Consensus could not progress with N-1 validators
- ❌ Victim validator could not re-join after partition removed
- ❌ State divergence or fork detected

---

## 2. L1 RPC Latency Spike (latency-spike-l1-rpc.yaml)

### Test Objective
Ensure that L2 operations continue normally when Ethereum L1 connectivity is degraded, and checkpoint submission handles latency gracefully.

### Fault Injection Phase (10 minutes)

**What happens**:
- 500ms latency added to L1 RPC endpoint
- L1 calls timeout more frequently

**Expected metrics during fault**:

| Metric | Expected Value | Explanation |
|--------|---------------|-------------|
| `rate(bor_blockchain_head_block[1m])` | **> 0 (unchanged)** | L2 block production independent of L1 |
| `rate(heimdall_consensus_height[1m])` | **> 0 (unchanged)** | Consensus independent of L1 |
| `rate(heimdall_checkpoint_submitted_total[5m])` | **> 0 (may decrease)** | Checkpoints delayed but still succeed |
| `histogram_quantile(0.95, rate(bor_rpc_duration_seconds_bucket[5m]))` | **< 2.0s** | RPC latency increases but remains functional |

**Expected logs**:
```
[checkpoint-submitter] L1 RPC call timeout, retrying...
[checkpoint-submitter] L1 submission successful after 3 retries (1.2s)
[checkpoint-submitter] Checkpoint queue depth: 5 pending
```

### Recovery Phase (30s cooldown)

**What happens**:
- L1 latency returns to normal
- Checkpoint submission rate returns to baseline
- Pending checkpoint queue drains

**Expected metrics during recovery**:

| Metric | Expected Value | Explanation |
|--------|---------------|-------------|
| `rate(heimdall_checkpoint_submitted_total[5m])` | **Increasing** | Processing queued checkpoints |
| `histogram_quantile(0.95, rate(bor_rpc_duration_seconds_bucket[5m]))` | **< 0.5s** | RPC latency returns to normal |

### Success Indicators
- ✅ L2 operations completely unaffected by L1 latency
- ✅ Checkpoints continued (even if slower)
- ✅ No checkpoint permanently failed
- ✅ System recovered quickly when latency removed

### Failure Indicators
- ❌ L2 block production slowed or stopped
- ❌ Checkpoints completely stopped being submitted
- ❌ System did not recover after latency removed
- ❌ Checkpoint queue grew unbounded

---

## 3. RabbitMQ Total Failure (rabbitmq-failure.yaml)

### Test Objective
Verify that core blockchain operations (block production, consensus) do not depend on message queue availability.

### Fault Injection Phase (3 minutes)

**What happens**:
- All traffic to RabbitMQ blocked (100% packet loss)
- Services cannot publish or consume messages
- Message queue appears completely unavailable

**Expected metrics during fault**:

| Metric | Expected Value | Explanation |
|--------|---------------|-------------|
| `rate(bor_blockchain_head_block[1m])` | **> 0 (unchanged)** | Bor operates independently |
| `rate(heimdall_consensus_height[1m])` | **> 0 (unchanged)** | Heimdall operates independently |
| `rabbitmq_queue_connections` | **→ 0** | All connections dropped |
| `rabbitmq_queue_messages_ready` | **Increasing** | Messages queuing up |

**Expected logs**:
```
[bor] RabbitMQ connection lost, continuing operations
[heimdall] Message queue unavailable, operating without cross-layer events
[bridge] Cannot publish to RabbitMQ, events will be queued
```

### Recovery Phase (30s cooldown)

**What happens**:
- RabbitMQ becomes accessible again
- Services reconnect to message queue
- Queued messages are processed

**Expected metrics during recovery**:

| Metric | Expected Value | Explanation |
|--------|---------------|-------------|
| `rabbitmq_queue_connections` | **> 0** | Services reconnecting |
| `rabbitmq_queue_messages_ready` | **Decreasing** | Processing queued messages |
| `rate(rabbitmq_queue_messages_delivered[1m])` | **High (catching up)** | Delivering queued messages |

### Success Indicators
- ✅ Block production never affected
- ✅ Consensus never affected
- ✅ Services reconnected automatically
- ✅ Queued messages processed after recovery

### Failure Indicators
- ❌ Block production stopped or slowed
- ❌ Consensus affected by RabbitMQ failure
- ❌ Services could not reconnect
- ❌ Message loss detected

---

## 4. Checkpoint Delay Cascade (checkpoint-delay-cascade.yaml)

### Test Objective
Test system behavior under progressive network degradation affecting all validators simultaneously.

### Phase 1: Initial Delay (0-3 minutes)

**What happens**:
- 200ms latency added to all validators

**Expected metrics**:

| Metric | Expected Value | Explanation |
|--------|---------------|-------------|
| `rate(bor_blockchain_head_block[1m])` | **> 0 (may slightly decrease)** | Minimal impact on block production |
| `rate(heimdall_consensus_height[1m])` | **> 0 (may slightly decrease)** | Consensus rounds take longer |
| `rate(heimdall_checkpoint_submitted_total[5m])` | **> 0 (decreasing)** | Checkpoints slower but continue |

### Phase 2: Progressive Delay (3-6 minutes)

**What happens**:
- Latency increased to 500ms
- All validators affected simultaneously

**Expected metrics**:

| Metric | Expected Value | Explanation |
|--------|---------------|-------------|
| `rate(bor_blockchain_head_block[1m])` | **> 0 (possibly decreased)** | Some impact on block production |
| `rate(heimdall_consensus_height[1m])` | **> 0 (decreased)** | Consensus slower but continues |
| `rate(heimdall_checkpoint_submitted_total[10m])` | **> 0** | Checkpoints significantly delayed |
| `histogram_quantile(0.95, rate(bor_rpc_duration_seconds_bucket[5m]))` | **< 10s** | RPC latency high but functional |

**Expected logs**:
```
[validators] Consensus round timeout, extending deadline
[validators] Block proposal delayed, waiting for votes
[checkpoint-submitter] Checkpoint submission taking longer than expected
```

### Recovery Phase (30s cooldown)

**What happens**:
- Latency returns to normal
- Validators synchronize quickly
- Checkpoint submission rate recovers

### Success Indicators
- ✅ System continued operating under high latency
- ✅ No permanent failures despite degradation
- ✅ Graceful performance degradation
- ✅ Quick recovery when latency removed

### Failure Indicators
- ❌ Complete stop in block production or consensus
- ❌ Validators unable to maintain consensus with latency
- ❌ System did not recover after latency removed

---

## 5. Bandwidth Throttle (bandwidth-throttle.yaml)

### Test Objective
Verify that validators can maintain operations under severe bandwidth constraints.

### Fault Injection Phase (6 minutes)

**What happens**:
- Network bandwidth limited to 1 Mbps
- Simulates constrained cloud environments or congested networks
- Affects both TCP and UDP traffic

**Expected metrics during fault**:

| Metric | Expected Value | Explanation |
|--------|---------------|-------------|
| `rate(bor_blockchain_head_block[1m])` | **> 0** | Network continues producing blocks |
| `heimdall_p2p_peers{service="throttled"}` | **> 0** | Maintains peer connections |
| `rate(heimdall_checkpoint_submitted{service="throttled"}[5m])` | **≥ 0** | May submit checkpoints slower |
| `bor_p2p_peers` | **Stable** | Peer count remains relatively stable |

**Expected logs**:
```
[throttled-validator] Slow network detected, adjusting sync strategy
[throttled-validator] Block propagation delayed, catching up...
[other-validators] Peer <throttled> experiencing slow responses
```

### Success Indicators
- ✅ Validator maintained connectivity despite bandwidth limit
- ✅ Network continued without the throttled validator's full participation
- ✅ Throttled validator caught up after bandwidth restored
- ✅ No permanent network splits or forks

### Failure Indicators
- ❌ Validator completely dropped from network
- ❌ Network could not progress without throttled validator
- ❌ Validator could not resync after bandwidth restored

---

## 6. Progressive Network Degradation (progressive-network-degradation.yaml)

### Test Objective
Test network resilience against gradually increasing failures on a single validator.

### Phase 1: Light Latency (0-3 minutes)

**What happens**:
- 100ms latency
- Minimal impact expected

**Expected metrics**:

| Metric | Expected Value | Explanation |
|--------|---------------|-------------|
| `rate(bor_blockchain_head_block[1m])` | **> 0 (normal)** | No significant impact |
| `heimdall_p2p_peers{service="degraded"}` | **Normal** | Maintains all peers |

### Phase 2: Moderate Degradation (3-6 minutes)

**What happens**:
- 300ms latency, 5% packet loss
- Noticeable impact on degraded validator

**Expected metrics**:

| Metric | Expected Value | Explanation |
|--------|---------------|-------------|
| `rate(bor_blockchain_head_block[1m])` | **> 0** | Other validators compensate |
| `heimdall_p2p_peers{service="degraded"}` | **< 5** | Losing peer connections |
| `rate(heimdall_checkpoint_submitted{service="degraded"}[5m])` | **Decreasing** | Checkpoint submission slowing |

### Phase 3: Heavy Degradation (6-9 minutes)

**What happens**:
- 500ms latency, 10% packet loss
- Degraded validator effectively offline

**Expected metrics**:

| Metric | Expected Value | Explanation |
|--------|---------------|-------------|
| `rate(bor_blockchain_head_block[1m])` | **> 0** | Network continues with majority |
| `heimdall_p2p_peers{service="degraded"}` | **→ 0** | Lost all peers |
| `rate(heimdall_checkpoint_submitted{service="degraded"}[5m])` | **→ 0** | No longer submitting |
| `rate(heimdall_checkpoint_submitted_total[5m])` | **> 0** | Other validators continue |

### Success Indicators
- ✅ Network resilient throughout all degradation phases
- ✅ Graceful degradation of affected validator
- ✅ Other validators compensated for failed validator
- ✅ Degraded validator recovered after faults removed

### Failure Indicators
- ❌ Network affected by single validator degradation
- ❌ Cascading failures to other validators
- ❌ System did not recover after faults removed

---

## 7. Split-Brain Partition (split-brain-partition.yaml)

### Test Objective
Verify that the network maintains consensus despite a complete network split, and that the majority partition continues operations.

### Fault Injection Phase (5 minutes)

**What happens**:
- Validators split into two groups
- Bidirectional packet loss between groups
- Each group cannot see the other

**Expected metrics during fault**:

| Metric | Expected Value | Explanation |
|--------|---------------|-------------|
| `rate(bor_blockchain_head_block[1m])` | **> 0** | Majority partition continues |
| `rate(heimdall_consensus_height[1m])` | **> 0** | Majority maintains consensus |
| `min(heimdall_p2p_peers{service=~"validator.*"})` | **< 3** | Validators see reduced peer count |
| `rate(heimdall_checkpoint_submitted_total[5m])` | **> 0** | Majority continues checkpoints |
| `histogram_quantile(0.95, rate(bor_rpc_duration_seconds_bucket[5m]))` | **< 5s** | RPC remains functional |

**Expected logs**:
```
[group-a] Lost connection to validators in group B
[group-b] Lost connection to validators in group A
[majority-group] Consensus proceeding with X/Y validators
[minority-group] Cannot reach consensus quorum
```

### Recovery Phase (1 minute cooldown)

**What happens**:
- Network partition healed
- Both groups reconnect
- Minority synchronizes with majority

**Expected metrics during recovery**:

| Metric | Expected Value | Explanation |
|--------|---------------|-------------|
| `heimdall_p2p_peers` | **Increasing** | Groups reconnecting |
| `heimdall_consensus_height` | **Converging** | State synchronization |

### Success Indicators
- ✅ Majority partition continued without interruption
- ✅ Minority partition gracefully handled lack of quorum
- ✅ Network converged to single state after partition healed
- ✅ No permanent forks or state divergence

### Failure Indicators
- ❌ Both partitions stopped (no majority)
- ❌ Minority partition continued despite lack of quorum
- ❌ State divergence after partition healed
- ❌ Permanent fork created

---

## General Patterns Across All Scenarios

### Healthy System Behaviors

1. **Resilient Operations**
   - Critical operations (block production, consensus) never stop
   - Non-critical operations may degrade but recover

2. **Graceful Degradation**
   - Performance decreases before failing completely
   - System provides warnings before critical failures

3. **Automatic Recovery**
   - Services reconnect automatically when faults removed
   - State synchronization happens without manual intervention
   - Queued operations process after recovery

4. **Isolation**
   - Failures isolated to affected services
   - No cascading failures to healthy services

### Warning Signs

1. **Cascading Failures**
   - Fault in one service affecting others
   - Failures spreading beyond target

2. **Incomplete Recovery**
   - Services don't reconnect after fault removed
   - Metrics don't return to baseline
   - Permanent state divergence

3. **Critical Operation Failures**
   - Block production stops
   - Consensus cannot progress
   - Network splits into forks

### Debugging Failed Tests

If a test fails, investigate in this order:

1. **Check Test Logs**
   - Review `<scenario>-execution.log` in test run directory
   - Look for ERROR level messages

2. **Check Service Logs**
   - `kurtosis service logs polygon-chain <service-name>`
   - Look for connection errors, timeouts, panics

3. **Check Prometheus Metrics**
   - Query metrics that failed success criteria
   - Look at timeline around fault injection

4. **Verify Cleanup**
   - Check for remaining sidecar containers
   - Verify no tc/iptables rules remain
   - Confirm services are healthy

5. **Reproduce**
   - Re-run scenario to confirm failure is consistent
   - Try with shorter duration to isolate issue

---

## Appendix: Metric Interpretation Guide

### Rate Metrics

```prometheus
rate(metric[1m])
```

- Measures change per second over 1-minute window
- `> 0` means metric is increasing (operations happening)
- `== 0` means metric is flat (operations stopped)

### Histogram Quantiles

```prometheus
histogram_quantile(0.95, rate(metric_bucket[5m]))
```

- P95 value: 95% of requests faster than this
- Higher values indicate slower performance
- Sharp increases indicate degradation

### Peer Counts

```prometheus
heimdall_p2p_peers
```

- Number of connected P2P peers
- Healthy: >= 3-5 peers
- Warning: 1-2 peers
- Critical: 0 peers (isolated)

### Success Criteria Evaluation

- **Critical criteria**: Must pass for PASSED status
- **Non-critical criteria**: Expected behavior, not required for PASSED
- Review both to understand complete system behavior
