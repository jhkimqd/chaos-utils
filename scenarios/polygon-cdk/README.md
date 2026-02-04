# Polygon CDK Chaos Scenarios

Chaos engineering scenarios for Kurtosis-CDK deployments, organized by deployment type.

## Directory Structure

```
polygon-cdk/
├── cdk-erigon-rollup/
├── cdk-erigon-sovereign-ecdsa-multisig/
├── cdk-erigon-sovereign-pessimistic/
├── cdk-erigon-validium/
├── op-geth/
└── op-succinct/
```

Each deployment type contains 4 fault categories:
- **applications/** - Service failures (crashes, restarts, pauses)
- **cpu-memory/** - Resource exhaustion (CPU stress, memory pressure)
- **network/** - Network chaos (latency, packet loss, partitions)
- **filesystem/** - Storage failures (disk I/O, disk space)

## Deployment Types

### CDK-Erigon Variants

**cdk-erigon-rollup**
- Basic rollup deployment
- Core services: sequencer, postgres, L1

**cdk-erigon-sovereign-ecdsa-multisig**
- Sovereign chain with ECDSA multisig consensus
- Includes: agglayer, aggkit, bridge

**cdk-erigon-sovereign-pessimistic**
- Sovereign chain with pessimistic consensus
- Includes: agglayer, aggkit, bridge

**cdk-erigon-validium**
- Validium deployment
- Includes: bridge (DAC if configured)

### OP Stack Variants

**op-geth**
- OP Stack with agglayer integration
- Includes: op-batcher, agglayer, aggkit

**op-succinct**
- OP Stack with ZK proof generation
- Includes: op-batcher, op-succinct-proposer

## Quick Start

```bash
# Run a scenario
./bin/chaos-runner run --scenario scenarios/polygon-cdk/<deployment>/<category>/<scenario>.yaml --enclave cdk

# Examples
./bin/chaos-runner run --scenario scenarios/polygon-cdk/cdk-erigon-rollup/applications/sequencer-crash-recovery.yaml --enclave cdk
./bin/chaos-runner run --scenario scenarios/polygon-cdk/op-geth/applications/op-batcher-crash-during-submission.yaml --enclave cdk
```

## Finding Scenarios

```bash
# List scenarios for a deployment
find scenarios/polygon-cdk/cdk-erigon-rollup -name "*.yaml"

# Search by service name
find scenarios/polygon-cdk -name "*agglayer*.yaml"

# Count scenarios per deployment
for d in scenarios/polygon-cdk/*/; do echo "$d: $(find $d -name '*.yaml' | wc -l)"; done
```
