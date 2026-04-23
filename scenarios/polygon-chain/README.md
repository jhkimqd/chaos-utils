# Polygon PoS Chaos Scenarios

Comprehensive chaos engineering scenarios for Polygon PoS networks deployed on Kurtosis.

## Network Fault Scenarios (✅ Implemented)

### Basic Network Faults
- **`quick-test.yaml`** - 30s latency test for quick validation
- **`bandwidth-throttle.yaml`** - Bandwidth constraints on validators
- **`latency-spike-l1-rpc.yaml`** - L1 RPC latency

### Validator Isolation & Partitions
- **`validator-partition.yaml`** - Complete network isolation of single validator
- **`regional-network-outage.yaml`** - Single validator isolation (maintains consensus)
- **`split-brain-partition.yaml`** - Network split causing two validator groups
- **`asymmetric-network-partition.yaml`** - One-way network partition

### Progressive & Cascading Failures
- **`progressive-network-degradation.yaml`** - Progressive packet loss increase
- **`cascading-latency-spike.yaml`** - Cascading latency L1→L2→Validator
- **`intermittent-checkpoint-failures.yaml`** - Progressive packet loss on checkpoints

### Dependency Failures
- **`mixed-dependency-failure.yaml`** - Combined L1 RPC failure + RabbitMQ latency
- **`total-dependency-isolation.yaml`** - Complete L1 + RabbitMQ isolation

### Service Degradation
- **`rpc-node-degradation.yaml`** - RPC service latency
- **`monitoring-stress-test.yaml`** - Progressive degradation to test alerting

---

## Container Lifecycle Scenarios (🚧 Requires Implementation)

### ⭐ HIGH PRIORITY: simultaneous-validator-restart.yaml
**Purpose:** All validators restart at once - simulates upgrade gone wrong
**Real Bug:** Block production stopped after simultaneous validator restart during upgrade

This scenario caught a **real production bug**. Running this regularly ensures the bug doesn't reoccur.

See `docs/IMPLEMENTATION_PLAN_NEW_FAULTS.md` for implementation details.

