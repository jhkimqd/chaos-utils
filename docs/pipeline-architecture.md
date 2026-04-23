# Complete Pipeline Architecture

This document explains the complete flow from YAML scenario file to fault injection to metrics evaluation.

## Pipeline Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                    1. COMMAND LINE ENTRY                         │
│  ./bin/chaos-runner run --scenario memory-pressure-validator.yaml│
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│                    2. YAML PARSING                               │
│  File: cmd/chaos-runner/run.go                                  │
│  • Reads YAML file                                              │
│  • parser.ParseFile() → scenario.Scenario struct                │
│  • validator.Validate() checks schema                           │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│                 3. ORCHESTRATOR CREATION                         │
│  File: pkg/core/orchestrator/orchestrator.go                   │
│  • Creates Docker client                                        │
│  • Creates Prometheus client                                    │
│  • Creates Injector, Collector, Detector                        │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│              4. STATE MACHINE EXECUTION                          │
│  orchestrator.Execute() runs through states                     │
│                                                                  │
│  PARSE → DISCOVER → PREPARE → WARMUP → INJECT                  │
│    ↓                                                            │
│  MONITOR → DETECT → COOLDOWN → TEARDOWN → REPORT               │
└─────────────────────────────────────────────────────────────────┘
```

## Detailed State Flow

### State 1: PARSE
**File**: `pkg/core/orchestrator/orchestrator.go:383-403`

```go
func (o *Orchestrator) executeParse(ctx context.Context, scenarioPath string) error {
    // Parse YAML → scenario.Scenario struct
    scen, err := o.parser.ParseFile(scenarioPath)

    // Validate scenario
    err := o.validator.Validate(scen)

    o.scenario = scen
}
```

**Example YAML Input**:
```yaml
spec:
  targets:
    - selector:
        type: kurtosis_service
        pattern: "l2-cl-2-heimdall-v2-bor-validator"
      alias: memory_constrained_heimdall

  faults:
    - phase: memory_limit_heimdall
      target: memory_constrained_heimdall
      type: memory_pressure
      params:
        memory_mb: 512
        method: limit
```

**Output**: `scenario.Scenario` struct with parsed data

---

### State 2: DISCOVER
**File**: `pkg/core/orchestrator/orchestrator.go:408-454`

```go
func (o *Orchestrator) executeDiscover(ctx context.Context) error {
    // List all Docker containers
    containers, err := o.dockerClient.ContainerList(ctx, types.ContainerListOptions{})

    // Filter by pattern matching
    for _, container := range containers {
        if matchPattern(container.Names, targetSpec.Selector.Pattern) {
            target := TargetInfo{
                Alias:       targetSpec.Alias,
                ContainerID: container.ID,
                Name:        getContainerName(container.Names),
                IP:          getContainerIP(container),
            }
            o.targets = append(o.targets, target)
        }
    }
}
```

**What it does**:
1. Calls Docker API: `docker ps` equivalent
2. Matches container names against regex patterns from YAML
3. Stores matched containers in `o.targets[]`

**Example Output**:
```
✓ Found: l2-cl-2-heimdall-v2-bor-validator--abc123 (ffe8f3091e1f)
✓ Discovered 6 target(s)
```

---

### State 3: PREPARE
**File**: `pkg/core/orchestrator/orchestrator.go:457-514`

```go
func (o *Orchestrator) executePrepare(ctx context.Context) error {
    // Create sidecars for network faults (not used for CPU/memory)
    for _, target := range o.targets {
        sidecarID, err := o.sidecarMgr.CreateSidecar(ctx, target.ContainerID)
    }
}
```

**What it does**:
- Creates sidecar containers for network fault injection
- Sidecars share network namespace with targets
- CPU/Memory stress don't use sidecars (they use Docker API directly)

---

### State 4: WARMUP
**File**: `pkg/core/orchestrator/orchestrator.go:516-527`

```go
func (o *Orchestrator) executeWarmup(ctx context.Context) error {
    warmup := o.scenario.Spec.Warmup
    fmt.Printf("Warmup period: %s\n", warmup)
    time.Sleep(warmup)
}
```

**What it does**:
- Waits for systems to stabilize before injecting faults
- Default: 2 minutes (configurable in YAML)

---

### State 5: INJECT 🎯
**File**: `pkg/core/orchestrator/orchestrator.go:529-578`

This is where the actual fault injection happens!

```go
func (o *Orchestrator) executeInject(ctx context.Context) error {
    // For each fault in the scenario
    for i, fault := range o.scenario.Spec.Faults {
        // Find targets matching this fault's alias
        var targets []TargetInfo
        for _, target := range o.targets {
            if target.Alias == fault.Target {
                targets = append(targets, target)
            }
        }

        // Inject fault using unified injector
        err := o.injector.InjectFault(ctx, &fault, injectionTargets)
    }
}
```

#### Injection Flow for Memory Pressure

**Step 1**: Orchestrator calls `injector.InjectFault()`
**File**: `pkg/injection/injector.go:39-56`

```go
func (i *Injector) InjectFault(ctx context.Context, fault *scenario.Fault, targets []Target) error {
    switch fault.Type {
    case "memory_stress", "memory_pressure", "memory":
        return i.injectMemoryStress(ctx, fault, targets)
    }
}
```

**Step 2**: Parse YAML parameters
**File**: `pkg/injection/injector.go:252-288`

```go
func (i *Injector) injectMemoryStress(ctx context.Context, fault *scenario.Fault, targets []Target) error {
    // Parse parameters from YAML
    params := stress.StressParams{
        Method:   "limit",      // from params.method
        MemoryMB: 512,          // from params.memory_mb
        Duration: "5m",
    }

    // Extract from fault.Params map
    if fault.Params != nil {
        if memoryMB, ok := fault.Params["memory_mb"].(int); ok {
            params.MemoryMB = memoryMB
        }
    }

    // Inject on all targets
    for _, target := range targets {
        err := i.stressInjector.InjectMemoryStress(ctx, target.ContainerID, params)
    }
}
```

**Step 3**: Apply cgroup limits via Docker API
**File**: `pkg/injection/stress/stress_wrapper.go:182-260`

```go
func (sw *StressWrapper) InjectMemoryStress(ctx context.Context, targetContainerID string, params StressParams) error {
    // Always use limit method for memory
    return sw.injectMemoryLimit(ctx, targetContainerID, params)
}

func (sw *StressWrapper) injectMemoryLimit(ctx context.Context, targetContainerID string, params StressParams) error {
    // 1. Get current container config (to save for restore)
    inspect, err := sw.dockerClient.ContainerInspect(ctx, targetContainerID)

    // 2. Save original resources
    sw.originalResources[targetContainerID] = container.Resources{
        Memory:     inspect.HostConfig.Memory,
        MemorySwap: inspect.HostConfig.MemorySwap,
        // ...
    }

    // 3. Calculate new limit
    memoryBytes := int64(params.MemoryMB) * 1024 * 1024

    // 4. Update container with memory limits
    updateConfig := container.UpdateConfig{
        Resources: container.Resources{
            Memory:     memoryBytes,
            MemorySwap: memoryBytes,  // Must be >= Memory
        },
    }

    // 5. Call Docker API
    _, err = sw.dockerClient.ContainerUpdate(ctx, targetContainerID, updateConfig)
}
```

**Actual Docker API Call**:
```
POST /containers/{id}/update
{
  "Memory": 536870912,     // 512MB in bytes
  "MemorySwap": 536870912
}
```

This is equivalent to:
```bash
docker update --memory 512M --memory-swap 512M <container-id>
```

**Result**:
```
✓ Injected faults on 6 target(s)
```

---

### State 6: MONITOR 📊
**File**: `pkg/core/orchestrator/orchestrator.go:581-610`

```go
func (o *Orchestrator) executeMonitor(ctx context.Context) error {
    duration := o.scenario.Spec.Duration

    if o.collector != nil && o.promClient != nil {
        // Reconfigure collector with scenario metrics
        o.collector = collector.New(collector.Config{
            PrometheusClient: o.promClient,
            Interval:         o.cfg.Prometheus.RefreshInterval,
            MetricNames:      o.scenario.Spec.Metrics,  // From YAML
        })

        // Start collecting metrics
        o.collector.Start(ctx)

        // Monitor for the duration (e.g., 8 minutes)
        time.Sleep(duration)

        // Stop collection
        o.collector.Stop()
    }
}
```

**What happens**:
1. Starts background goroutine collecting Prometheus metrics
2. Queries metrics every `RefreshInterval` (default: 15s)
3. Stores time-series data in memory
4. Runs for `spec.duration` (e.g., 8 minutes)

**Example Prometheus Query** (from YAML):
```yaml
metrics:
  - chain_head_block
  - cometbft_consensus_height
  - process_resident_memory_bytes
```

The collector queries:
```promql
chain_head_block{job=~"l2-el-.*-bor-heimdall-v2-validator"}
```

Every 15 seconds for 8 minutes = ~32 data points

---

### State 7: DETECT ✅
**File**: `pkg/core/orchestrator/orchestrator.go:613-663`

```go
func (o *Orchestrator) executeDetect(ctx context.Context) error {
    fmt.Println("Evaluating success criteria...")

    // Evaluate each criterion
    for i, criterion := range o.scenario.Spec.SuccessCriteria {
        result, err := o.detector.Evaluate(ctx, criterion)

        if result.Passed {
            fmt.Printf("    ✓ PASSED: %s\n", result.Message)
        } else {
            if criterion.Critical {
                fmt.Printf("    ✗ FAILED (CRITICAL): %s\n", result.Message)
                criticalFailed = true
            } else {
                fmt.Printf("    ⚠ FAILED (non-critical): %s\n", result.Message)
            }
        }
    }
}
```

#### Criterion Evaluation Details
**File**: `pkg/monitoring/detector/failure_detector.go:39-110`

```go
func (fd *FailureDetector) Evaluate(ctx context.Context, criterion scenario.SuccessCriterion) (*CriterionResult, error) {
    // Execute Prometheus query
    queryResults, err := fd.promClient.QueryLatest(ctx, criterion.Query)

    // Get the metric value
    value := queryResults[0].Value
    result.LastValue = value

    // Evaluate threshold
    passed := fd.evaluateThreshold(value, criterion.Threshold)

    if passed {
        result.Passed = true
        result.Message = fmt.Sprintf("value %.2f meets threshold %s", value, criterion.Threshold)
    } else {
        result.Passed = false
        result.Message = fmt.Sprintf("value %.2f fails threshold %s", value, criterion.Threshold)
        result.Failures++
    }
}
```

**Example from YAML**:
```yaml
success_criteria:
  - name: network_continues_under_pressure
    description: Block production continues despite memory pressure
    type: prometheus
    query: increase(chain_head_block{job=~"l2-el-.*-bor-heimdall-v2-validator"}[1m])
    threshold: "> 0"
    window: 8m
    critical: true
```

**Evaluation**:
1. Query Prometheus: `increase(chain_head_block{...}[1m])`
2. Get result value: e.g., `12.5` (blocks in last minute)
3. Check threshold: `12.5 > 0` ✅ **PASSED**
4. If threshold was `> 20`: `12.5 > 20` ❌ **FAILED**

---

### State 8: COOLDOWN
**File**: `pkg/core/orchestrator/orchestrator.go:666-677`

```go
func (o *Orchestrator) executeCooldown(ctx context.Context) error {
    cooldown := o.scenario.Spec.Cooldown
    fmt.Printf("Cooldown period: %s\n", cooldown)
    time.Sleep(cooldown)
}
```

**What it does**:
- Waits before cleanup to let systems stabilize
- Default: 2 minutes

---

### State 9: TEARDOWN 🧹
**File**: `pkg/core/orchestrator/orchestrator.go:679-720`

```go
func (o *Orchestrator) executeTeardown(ctx context.Context) error {
    fmt.Println("Tearing down faults...")

    // Remove faults from each target
    for containerID, faultType := range o.injectedFaults {
        err := o.injector.RemoveFault(ctx, faultType, containerID)
    }

    // Clean up sidecars
    o.cleanupCoord.CleanupAll(ctx)
}
```

#### Cleanup Flow for Memory Limits

**Step 1**: Orchestrator calls `injector.RemoveFault()`
**File**: `pkg/injection/injector.go:290-308`

```go
func (i *Injector) RemoveFault(ctx context.Context, faultType string, containerID string) error {
    switch faultType {
    case "memory_stress", "memory_pressure", "memory":
        return i.stressInjector.RemoveFault(ctx, containerID)
    }
}
```

**Step 2**: Restore original cgroup limits
**File**: `pkg/injection/stress/stress_wrapper.go:262-321`

```go
func (sw *StressWrapper) RemoveFault(ctx context.Context, targetContainerID string) error {
    // Kill any active stress processes
    killCmds := []string{"pkill -9 yes", "pkill -9 dd", ...}
    for _, cmd := range killCmds {
        sw.dockerClient.ExecCommand(ctx, targetContainerID, []string{"sh", "-c", cmd})
    }

    // Restore original resource limits
    originalRes := sw.originalResources[targetContainerID]

    restoreConfig := container.Resources{}

    if originalRes.Memory == 0 {
        // Original had no limit - set to 1TB (effectively unlimited)
        restoreConfig.Memory = 1024 * 1024 * 1024 * 1024
        restoreConfig.MemorySwap = restoreConfig.Memory
    } else {
        // Restore exact original values
        restoreConfig.Memory = originalRes.Memory
        restoreConfig.MemorySwap = originalRes.MemorySwap
    }

    updateConfig := container.UpdateConfig{
        Resources: restoreConfig,
    }

    // Call Docker API to restore
    _, err := sw.dockerClient.ContainerUpdate(ctx, targetContainerID, updateConfig)

    // Remove from tracking
    delete(sw.originalResources, targetContainerID)
}
```

**Result**:
```
✓ Removed faults from 6 target(s)
```

---

### State 10: REPORT
**File**: `pkg/core/orchestrator/orchestrator.go:722-736`

```go
func (o *Orchestrator) executeReport(ctx context.Context, result *TestResult) error {
    fmt.Println("Generating report...")

    // Report is generated by cmd/chaos-runner/run.go
    // Includes: test results, target info, fault summary, cleanup audit

    return nil
}
```

---

## Data Flow Summary

### 1. YAML → Go Structs

**Input** (`memory-pressure-validator.yaml`):
```yaml
faults:
  - phase: memory_limit_heimdall
    type: memory_pressure
    params:
      memory_mb: 512
```

**Parsed to** (`scenario.Fault` struct):
```go
type Fault struct {
    Phase  string                 // "memory_limit_heimdall"
    Type   string                 // "memory_pressure"
    Target string                 // "memory_constrained_heimdall"
    Params map[string]interface{} // {"memory_mb": 512, "method": "limit"}
}
```

### 2. Docker API Calls

**Before Injection**:
```bash
$ docker inspect <container-id> --format '{{.HostConfig.Memory}}'
0  # Unlimited
```

**During Injection** (API call):
```http
POST /v1.41/containers/<id>/update
Content-Type: application/json

{
  "Memory": 536870912,
  "MemorySwap": 536870912
}
```

**After Injection**:
```bash
$ docker inspect <container-id> --format '{{.HostConfig.Memory}}'
536870912  # 512MB

$ docker stats <container-id>
MEM USAGE / LIMIT
450MiB / 512MiB
```

**After Cleanup**:
```bash
$ docker inspect <container-id> --format '{{.HostConfig.Memory}}'
1099511627776  # 1TB (effectively unlimited)
```

### 3. Prometheus Queries

**Query from YAML**:
```yaml
query: increase(chain_head_block{job=~"l2-el-.*-validator"}[1m])
threshold: "> 0"
```

**Prometheus API Call**:
```http
GET /api/v1/query?query=increase(chain_head_block{job=~"l2-el-.*-validator"}[1m])
```

**Response**:
```json
{
  "status": "success",
  "data": {
    "resultType": "vector",
    "result": [
      {
        "metric": {"job": "l2-el-1-bor-validator"},
        "value": [1738224000, "12.5"]
      }
    ]
  }
}
```

**Evaluation**:
```go
value := 12.5
threshold := "> 0"
passed := (12.5 > 0)  // true ✓
```

---

## Key Files Reference

| Phase | File | Purpose |
|-------|------|---------|
| **CLI Entry** | `cmd/chaos-runner/main.go` | Command setup |
| | `cmd/chaos-runner/run.go` | Run command handler |
| **Orchestration** | `pkg/core/orchestrator/orchestrator.go` | State machine |
| **Parsing** | `pkg/scenario/parser/parser.go` | YAML → structs |
| | `pkg/scenario/validator/validator.go` | Schema validation |
| **Discovery** | `pkg/discovery/docker/client.go` | Docker container discovery |
| **Injection** | `pkg/injection/injector.go` | Fault type router |
| | `pkg/injection/stress/stress_wrapper.go` | CPU/Memory stress |
| | `pkg/injection/l3l4/comcast_wrapper.go` | Network faults |
| | `pkg/injection/container/manager.go` | Container lifecycle |
| **Monitoring** | `pkg/monitoring/collector/collector.go` | Metric collection |
| | `pkg/monitoring/prometheus/client.go` | Prometheus queries |
| | `pkg/monitoring/detector/failure_detector.go` | Success criteria evaluation |
| **Cleanup** | `pkg/core/cleanup/coordinator.go` | Cleanup orchestration |

---

## Complete Example: Memory Pressure Test

### Input: YAML Scenario
```yaml
spec:
  targets:
    - selector:
        pattern: "l2-cl-2-heimdall-v2-bor-validator"
      alias: heimdall_validator

  duration: 8m
  warmup: 2m
  cooldown: 2m

  faults:
    - phase: memory_limit
      target: heimdall_validator
      type: memory_pressure
      params:
        memory_mb: 512
        method: limit

  success_criteria:
    - name: validator_survives
      type: prometheus
      query: up{job="l2-cl-2-heimdall-v2-bor-validator"}
      threshold: "== 1"
      critical: true
```

### Execution Trace

```
1. PARSE: Load YAML → scenario.Scenario struct
   ✓ Parsed scenario: memory-pressure-validator
   Duration: 8m, Warmup: 2m, Cooldown: 2m

2. DISCOVER: Find Docker containers
   Looking for: "l2-cl-2-heimdall-v2-bor-validator"
   ✓ Found: l2-cl-2-heimdall-v2-bor-validator--abc123 (ffe8f3091e1f)

3. PREPARE: Create sidecars (skipped for memory faults)

4. WARMUP: Wait 2m for stabilization

5. INJECT: Apply memory limit
   Injecting memory limit on target ffe8f3091e1f: 512MB
   Docker API: POST /containers/ffe8f3091e1f/update
   Body: {"Memory": 536870912, "MemorySwap": 536870912}
   ✓ Memory limit injected successfully

6. MONITOR: Collect metrics for 8m
   Query every 15s: up{job="l2-cl-2-heimdall-v2-bor-validator"}
   [15s] value=1 ✓
   [30s] value=1 ✓
   ...
   [480s] value=1 ✓

7. DETECT: Evaluate success criteria
   [1/1] Evaluating: validator_survives
   Query: up{job="l2-cl-2-heimdall-v2-bor-validator"}
   Result: 1
   Threshold: == 1
   ✓ PASSED: value 1 meets threshold == 1

8. COOLDOWN: Wait 2m for stabilization

9. TEARDOWN: Remove faults
   Removing memory limit from ffe8f3091e1f
   Docker API: POST /containers/ffe8f3091e1f/update
   Body: {"Memory": 1099511627776, "MemorySwap": 1099511627776}
   ✓ Stress removed and limits restored

10. REPORT: Generate test report
    ✓ Report saved: reports/test-20260130-123456.json

[TEST SUMMARY] PASSED
  Duration: 12m0s
  Targets: 1
  Faults: 1
  Success Criteria: 1/1 passed
```

---

## Architecture Patterns

### 1. State Machine Pattern
The orchestrator uses a finite state machine to ensure consistent execution:
- Each state is atomic and idempotent
- State transitions are logged
- Emergency stop can interrupt at any state
- Cleanup always runs on exit

### 2. Strategy Pattern
Different fault types use different injection strategies:
- Network faults → Sidecar + comcast
- Container lifecycle → Direct Docker API
- CPU stress → Shell commands in target
- Memory pressure → Cgroup limits via Docker API

### 3. Template Method Pattern
All fault injectors follow the same pattern:
1. Parse YAML parameters → struct
2. Validate parameters
3. Save original state
4. Apply fault via specialized method
5. Track for cleanup

### 4. Observer Pattern
Metrics collection runs independently:
- Collector queries Prometheus in background
- Detector evaluates criteria asynchronously
- No tight coupling between injection and monitoring

---

## Error Handling

### Injection Failures
If injection fails at any target:
```go
if err := o.injector.InjectFault(ctx, &fault, targets); err != nil {
    return o.failTest(result, err)  // Triggers cleanup
}
```

### Cleanup Always Runs
```go
defer func() {
    fmt.Println("Running cleanup...")
    o.cleanupCoord.CleanupAll(ctx)
    o.cleanupCoord.PrintAuditLog()
}()
```

### Emergency Stop
```bash
# Create stop file or press Ctrl+C
touch /tmp/chaos-stop

# Triggers immediate cleanup
🛑 Emergency stop triggered, running cleanup...
```
