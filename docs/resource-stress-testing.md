# Resource Stress Testing

This document explains how CPU and memory stress testing work in the chaos framework, and why they use different approaches.

## Overview

| Resource | Method | How it works | Observable effect |
|----------|--------|-------------|-------------------|
| **CPU** | Active stress | Runs `yes > /dev/null` in target | CPU usage spikes in `docker stats` |
| **Memory** | Cgroup limits | Sets memory cgroup limits | Memory cap visible in `docker stats` |

## CPU Stress Testing

CPU stress uses **active load generation** via shell built-ins.

### How it works:
```yaml
faults:
  - type: cpu_stress
    params:
      cpu_percent: 80    # Target CPU utilization
      cores: 2          # Number of cores to stress
      method: stress    # Active stress (default)
```

This executes `yes > /dev/null` commands in the target container, which creates actual CPU load visible in `docker stats`.

### Why active stress works for CPU:
- Shell built-in command (`yes`) available everywhere
- Creates immediate, measurable load
- Doesn't require external tools
- Easy to stop (`pkill yes`)

### Monitoring:
```bash
docker stats <container>
# You'll see CPU% spike to the target level
```

## Memory Pressure Testing

Memory pressure uses **cgroup limits** instead of active allocation.

### How it works:
```yaml
faults:
  - type: memory_pressure
    params:
      memory_mb: 512    # Memory limit in MB
      method: limit     # Only method available
```

This sets cgroup memory limits using `docker update`, which caps the maximum memory the container can use.

### Why we use limits (not active stress) for memory:

1. **Reliability**: Shell-based memory allocation is unreliable
   - `dd` allocates but doesn't hold memory in RSS
   - Variables and buffers get swapped/cached inconsistently
   - No way to guarantee steady memory pressure

2. **Tool availability**: Proper memory stress requires tools like `stress-ng`
   - Can't install tools in target containers (violates non-invasive requirement)
   - Copying binaries at runtime is fragile and complex

3. **Safety**: Active memory allocation can cause OOM kills
   - If you allocate too much, kernel kills the process
   - Defeats the purpose of chaos testing (we want to test behavior under pressure, not test recovery from kills)

4. **Realism**: Memory limits simulate constrained environments
   - Kubernetes memory limits
   - Cloud instances with fixed memory
   - Containers in memory-constrained hosts

### Setting appropriate limits:

**Too restrictive** (causes OOM kills):
```yaml
memory_mb: 128  # ❌ Too small, process gets killed
```

**Appropriate** (creates pressure without killing):
```yaml
# For Heimdall validators:
memory_mb: 512  # ✓ Creates pressure, allows operation

# For Bor validators:
memory_mb: 1024  # ✓ Creates pressure, allows operation
```

**Too generous** (no meaningful pressure):
```yaml
memory_mb: 8192  # ❌ No pressure, normal operation
```

### Finding the right limit:

1. Check normal memory usage:
   ```bash
   docker stats <container> --no-stream
   # Note the MEM USAGE column
   ```

2. Set limit 10-30% above normal:
   ```
   Normal usage: 400MB
   Test limit: 512MB (28% margin)
   ```

3. Run the test and monitor:
   ```bash
   docker stats <container>
   # Memory should stay near limit but not trigger OOM
   ```

### Monitoring:

```bash
# Check memory limit is applied:
docker inspect <container> | grep Memory

# Monitor memory usage approaching limit:
docker stats <container>
# MEM USAGE / LIMIT should show your limit
```

## Cleanup

Both methods properly restore original state:

- **CPU**: Kills stress processes (`pkill yes`)
- **Memory**: Restores original cgroup limits (or sets to 1TB for effectively unlimited)

## Example Scenarios

### CPU Stress (Active Load)
```yaml
# scenarios/polygon-chain/cpu-starved-validator.yaml
faults:
  - type: cpu_stress
    target: validator
    params:
      cpu_percent: 80
      cores: 2
      method: stress  # Active CPU load
    duration: 4m
```

**Expected observation**: CPU usage spikes to 80% of 2 cores (~160% in docker stats)

### Memory Pressure (Cgroup Limit)
```yaml
# scenarios/polygon-chain/memory-pressure-validator.yaml
faults:
  - type: memory_pressure
    target: heimdall_validator
    params:
      memory_mb: 512
      method: limit  # Cgroup limit (only option)
    duration: 4m
```

**Expected observation**:
- Memory limit set to 512MB
- Container stays online but may show performance degradation
- Memory usage stays below limit
- No OOM kills if limit is appropriate

## Trade-offs

### Active Stress (CPU only)
**Pros:**
- Visible, measurable load
- Immediate effect
- Simulates high utilization

**Cons:**
- Requires specific commands to be available
- Only works reliably for CPU

### Cgroup Limits (Memory only)
**Pros:**
- Reliable enforcement by kernel
- Non-invasive (no code execution in target)
- Simulates real resource constraints

**Cons:**
- Doesn't increase usage, just caps it
- Requires finding appropriate limit values
- Can cause OOM kills if set too low

## Recommendations

1. **For CPU chaos tests**: Use `method: stress` with realistic cpu_percent values
2. **For Memory chaos tests**: Use `method: limit` with conservative limits (10-30% above normal usage)
3. **Monitor first**: Always check normal resource usage before setting limits
4. **Start conservative**: Use higher limits initially, lower them in subsequent tests
5. **Watch for OOM**: If containers get killed, increase memory limits
