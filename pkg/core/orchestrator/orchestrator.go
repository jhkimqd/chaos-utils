package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/jihwankim/chaos-utils/pkg/config"
	"github.com/jihwankim/chaos-utils/pkg/core/cleanup"
	"github.com/jihwankim/chaos-utils/pkg/core/logcollector"
	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
	"github.com/jihwankim/chaos-utils/pkg/emergency"
	"github.com/jihwankim/chaos-utils/pkg/injection"
	"github.com/jihwankim/chaos-utils/pkg/injection/sidecar"
	"github.com/jihwankim/chaos-utils/pkg/injection/verification"
	"github.com/jihwankim/chaos-utils/pkg/monitoring/collector"
	"github.com/jihwankim/chaos-utils/pkg/monitoring/detector"
	"github.com/jihwankim/chaos-utils/pkg/monitoring/prometheus"
	"github.com/jihwankim/chaos-utils/pkg/scenario"
	"github.com/jihwankim/chaos-utils/pkg/scenario/parser"
	"github.com/jihwankim/chaos-utils/pkg/scenario/validator"
)

// TestState represents the current state of a chaos test execution
type TestState int

const (
	StateInit TestState = iota // zero value — orchestrator not yet executing
	StateParse
	StateDiscover
	StatePrepare
	StateWarmup
	StateInject
	StateMonitor
	StateDetect
	StateCooldown
	StateTeardown
	StateReport
	StateCompleted
	StateFailed
)

func (s TestState) String() string {
	switch s {
	case StateInit:
		return "INIT"
	case StateParse:
		return "PARSE"
	case StateDiscover:
		return "DISCOVER"
	case StatePrepare:
		return "PREPARE"
	case StateWarmup:
		return "WARMUP"
	case StateInject:
		return "INJECT"
	case StateMonitor:
		return "MONITOR"
	case StateDetect:
		return "DETECT"
	case StateCooldown:
		return "COOLDOWN"
	case StateTeardown:
		return "TEARDOWN"
	case StateReport:
		return "REPORT"
	case StateCompleted:
		return "COMPLETED"
	case StateFailed:
		return "FAILED"
	default:
		return "UNKNOWN"
	}
}

// TargetInfo contains information about a discovered target
type TargetInfo struct {
	Alias       string
	ContainerID string
	Name        string
	IP          string
}

// Orchestrator coordinates the chaos test lifecycle
type Orchestrator struct {
	cfg              *config.Config
	currentState     TestState
	startTime        time.Time
	stopRequested    atomic.Bool
	sidecarMgr       *sidecar.Manager
	verifier         *verification.Verifier
	cleanupCoord     *cleanup.Coordinator
	emergencyCtrl    *emergency.Controller
	emergencyStopCtx context.Context
	emergencyCancel  context.CancelFunc

	// Components for test execution
	parser       *parser.Parser
	validator    *validator.Validator
	dockerClient *docker.Client
	promClient   *prometheus.Client
	detector     *detector.FailureDetector
	collector    *collector.Collector
	logCollector *logcollector.Collector
	injector     *injection.Injector

	// Test data
	scenario      *scenario.Scenario
	targets       []TargetInfo
	scenarioPath  string
	testID        string
	injectTime    time.Time         // set at INJECT start; used to scope log capture to fault window
	injectedFaults  map[string]string       // track which targets have faults injected (containerID -> faultType)
	criteriaResults []CriterionOutcome      // populated during DETECT phase
}

// CriterionOutcome captures the result of a single success criterion evaluation.
type CriterionOutcome struct {
	Name        string
	Description string
	Type        string
	Query       string
	Threshold   string
	Passed      bool
	Value       float64
	Message     string
	Critical    bool
}

// TestResult represents the result of a chaos test execution
type TestResult struct {
	TestID           string
	ScenarioName     string
	StartTime        time.Time
	EndTime          time.Time
	Duration         time.Duration
	State            TestState
	Success          bool
	Message          string
	Errors           []error
	Targets          []TargetInfo
	FaultCount       int
	CriteriaResults  []CriterionOutcome
}

// New creates a new Orchestrator instance
func New(cfg *config.Config) (*Orchestrator, error) {
	// Create Docker client
	dockerClient, err := docker.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	// Create sidecar manager
	sidecarMgr := sidecar.New(dockerClient, cfg.Docker.SidecarImage)

	// Create verifier
	verifier := verification.New(dockerClient)

	// Create cleanup coordinator
	cleanupCoord := cleanup.New(sidecarMgr)

	// Create emergency controller
	emergencyCtrl := emergency.New(emergency.Config{
		StopFile:             cfg.Emergency.StopFile,
		PollInterval:         1 * time.Second,
		EnableSignalHandlers: true,
	})

	// Create context for emergency controller
	emergencyCtx, emergencyCancel := context.WithCancel(context.Background())

	// Create scenario parser and validator
	p := parser.New(nil)
	v := validator.New()

	// Create Prometheus client — required for metrics collection and success criteria evaluation.
	promClient, err := prometheus.New(prometheus.Config{
		URL:             cfg.Prometheus.URL,
		Timeout:         cfg.Prometheus.Timeout,
		RefreshInterval: cfg.Prometheus.RefreshInterval,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Prometheus client (url=%s): %w", cfg.Prometheus.URL, err)
	}

	// Create failure detector
	det := detector.New(promClient)

	// Create metrics collector (will be reconfigured per-scenario)
	col := collector.New(collector.Config{
		PrometheusClient: promClient,
		Interval:         cfg.Prometheus.RefreshInterval,
	})

	// Create unified fault injector
	injector := injection.New(sidecarMgr, dockerClient)

	// Create log collector for post-failure diagnosis
	logCol := logcollector.New(dockerClient)

	return &Orchestrator{
		cfg:        cfg,
		sidecarMgr: sidecarMgr,
		verifier:         verifier,
		cleanupCoord:     cleanupCoord,
		emergencyCtrl:    emergencyCtrl,
		emergencyStopCtx: emergencyCtx,
		emergencyCancel:  emergencyCancel,
		parser:           p,
		validator:        v,
		dockerClient:     dockerClient,
		promClient:       promClient,
		detector:         det,
		collector:        col,
		logCollector:     logCol,
		injector:         injector,
		injectedFaults:   make(map[string]string),
	}, nil
}

// Execute runs the complete chaos test lifecycle
func (o *Orchestrator) Execute(ctx context.Context, scenarioPath string) (*TestResult, error) {
	o.startTime = time.Now()
	o.testID = generateTestID()
	o.scenarioPath = scenarioPath

	result := &TestResult{
		TestID:    o.testID,
		StartTime: o.startTime,
		State:     o.currentState,
	}

	// Start emergency controller
	o.emergencyCtrl.Start(o.emergencyStopCtx)
	defer o.emergencyCancel() // Stop emergency controller when test completes

	// Register cleanup callback with emergency controller
	o.emergencyCtrl.OnStop(func() {
		fmt.Println("🛑 Emergency stop triggered, running cleanup...")
		o.stopRequested.Store(true)
		if err := o.cleanupCoord.CleanupAll(ctx); err != nil {
			fmt.Printf("Emergency cleanup errors: %v\n", err)
		}
		o.cleanupCoord.PrintAuditLog()
	})

	// Ensure cleanup runs on panic or normal exit
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("PANIC during execution: %v\n", r)
			fmt.Println("Running emergency cleanup...")
			if err := o.cleanupCoord.CleanupAll(ctx); err != nil {
				fmt.Printf("Panic cleanup errors: %v\n", err)
			}
			o.cleanupCoord.PrintAuditLog()
			result.State = StateFailed
			result.Success = false
			result.Message = fmt.Sprintf("panic: %v", r)
		}
	}()

	// Always run cleanup on exit
	defer func() {
		fmt.Println("Running cleanup...")
		if err := o.cleanupCoord.CleanupAll(ctx); err != nil {
			fmt.Printf("Cleanup errors: %v\n", err)
		}
		o.cleanupCoord.PrintAuditLog()
	}()

	// PRE-FLIGHT CLEANUP: Remove remnants from previous failed/interrupted tests
	if err := o.preFlightCleanup(ctx); err != nil {
		fmt.Printf("⚠ Pre-flight cleanup warning: %v\n", err)
		// Don't fail the test, just warn
	}

	// State machine execution
	var err error

	// PARSE state
	o.transitionState(StateParse)
	if err = o.executeParse(ctx, scenarioPath); err != nil {
		return o.failTest(result, err)
	}
	result.ScenarioName = o.scenario.Metadata.Name

	// Check for stop
	if o.stopRequested.Load() {
		return o.failTest(result, fmt.Errorf("stopped before discovery"))
	}

	// DISCOVER state
	o.transitionState(StateDiscover)
	if err = o.executeDiscover(ctx); err != nil {
		return o.failTest(result, err)
	}

	// Check for stop
	if o.stopRequested.Load() {
		return o.failTest(result, fmt.Errorf("stopped before prepare"))
	}

	// PREPARE state
	o.transitionState(StatePrepare)
	if err = o.executePrepare(ctx); err != nil {
		return o.failTest(result, err)
	}

	// Check for stop
	if o.stopRequested.Load() {
		return o.failTest(result, fmt.Errorf("stopped before warmup"))
	}

	// WARMUP state
	o.transitionState(StateWarmup)
	if err = o.executeWarmup(ctx); err != nil {
		return o.failTest(result, err)
	}

	// Check for stop
	if o.stopRequested.Load() {
		return o.failTest(result, fmt.Errorf("stopped before inject"))
	}

	// Pre-fault health check: verify steady state before injection.
	// Aborts if any critical criterion fails — system must be healthy before we break it.
	if err = o.executePreCheck(ctx); err != nil {
		return o.failTest(result, err)
	}

	// INJECT state
	o.transitionState(StateInject)
	if err = o.executeInject(ctx); err != nil {
		return o.failTest(result, err)
	}

	// Check for stop
	if o.stopRequested.Load() {
		return o.failTest(result, fmt.Errorf("stopped before monitor"))
	}

	// MONITOR state
	o.transitionState(StateMonitor)
	if err = o.executeMonitor(ctx); err != nil {
		return o.failTest(result, err)
	}

	// Check for stop
	if o.stopRequested.Load() {
		return o.failTest(result, fmt.Errorf("stopped before cooldown"))
	}

	// COOLDOWN state — wait for the system to stabilise before removing faults
	o.transitionState(StateCooldown)
	if err = o.executeCooldown(ctx); err != nil {
		return o.failTest(result, err)
	}

	// Check for stop
	if o.stopRequested.Load() {
		return o.failTest(result, fmt.Errorf("stopped before teardown"))
	}

	// TEARDOWN state — remove faults and sidecars before evaluating criteria.
	// This ensures Prometheus can scrape cleanly and criteria are not affected
	// by network faults blocking the scrape path.
	o.transitionState(StateTeardown)
	if err = o.executeTeardown(ctx); err != nil {
		return o.failTest(result, err)
	}

	// Check for stop
	if o.stopRequested.Load() {
		return o.failTest(result, fmt.Errorf("stopped before detect"))
	}

	// DETECT state — evaluate success criteria now that faults are removed
	o.transitionState(StateDetect)
	if err = o.executeDetect(ctx); err != nil {
		return o.failTest(result, err)
	}

	// REPORT state
	o.transitionState(StateReport)
	if err = o.executeReport(ctx, result); err != nil {
		return o.failTest(result, err)
	}

	// Success!
	o.transitionState(StateCompleted)
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)
	result.State = StateCompleted
	result.Success = true
	result.Message = "Test completed successfully"
	result.Targets = o.targets
	result.FaultCount = len(o.injectedFaults)
	result.CriteriaResults = o.criteriaResults

	return result, nil
}

// State transition method
func (o *Orchestrator) transitionState(newState TestState) {
	fmt.Printf("[%s] → [%s]\n", o.currentState, newState)
	o.currentState = newState
}

// executeParse parses and validates the scenario file
func (o *Orchestrator) executeParse(ctx context.Context, scenarioPath string) error {
	fmt.Printf("Parsing scenario: %s\n", scenarioPath)

	// Parse scenario YAML
	scen, err := o.parser.ParseFile(scenarioPath)
	if err != nil {
		return fmt.Errorf("failed to parse scenario: %w", err)
	}

	// Validate scenario
	if err := o.validator.Validate(scen); err != nil {
		return fmt.Errorf("scenario validation failed: %w", err)
	}

	o.scenario = scen
	fmt.Printf("✓ Parsed scenario: %s\n", scen.Metadata.Name)
	fmt.Printf("  Duration: %s, Warmup: %s, Cooldown: %s\n",
		scen.Spec.Duration, scen.Spec.Warmup, scen.Spec.Cooldown)
	fmt.Printf("  Targets: %d, Faults: %d, Success Criteria: %d\n",
		len(scen.Spec.Targets), len(scen.Spec.Faults), len(scen.Spec.SuccessCriteria))

	return nil
}

// observabilityBlocklist contains container name substrings that must never be
// fault targets. Prometheus and Grafana are observability infrastructure — they
// must remain reachable throughout every experiment.
var observabilityBlocklist = []string{
	"prometheus",
	"grafana",
}

// executeDiscover discovers target containers matching scenario selectors
func (o *Orchestrator) executeDiscover(ctx context.Context) error {
	fmt.Println("Discovering target containers...")

	if len(o.scenario.Spec.Targets) == 0 {
		return fmt.Errorf("no targets defined in scenario")
	}

	// For Kurtosis selectors, validate the enclave exists before listing containers.
	// Kurtosis container names are "<service>--<uuid>" with no enclave prefix, so
	// pattern matching alone cannot distinguish containers across different enclaves.
	// Fail fast here with a clear error rather than silently injecting faults into
	// the wrong enclave's services.
	//
	// We validate o.cfg.Kurtosis.EnclaveName (the value from --enclave flag) rather
	// than targetSpec.Selector.Enclave, because the YAML field may still contain the
	// unexpanded template literal "${ENCLAVE_NAME}" at this point in the lifecycle.
	hasKurtosisTarget := false
	for _, targetSpec := range o.scenario.Spec.Targets {
		if targetSpec.Selector.Type == "kurtosis_service" {
			hasKurtosisTarget = true
			break
		}
	}
	if hasKurtosisTarget && o.cfg.Kurtosis.EnclaveName != "" {
		if err := validateKurtosisEnclave(o.cfg.Kurtosis.EnclaveName); err != nil {
			return err
		}
	}

	o.targets = []TargetInfo{}

	for _, targetSpec := range o.scenario.Spec.Targets {
		fmt.Printf("  Looking for targets matching pattern: %s\n", targetSpec.Selector.Pattern)

		// List all containers using the underlying Docker API
		containers, err := o.dockerClient.ContainerList(ctx, types.ContainerListOptions{})
		if err != nil {
			return fmt.Errorf("failed to list containers: %w", err)
		}

		// Filter by pattern
		matched := false
		for _, container := range containers {
			// Match against container name
			if matchPattern(container.Names, targetSpec.Selector.Pattern) {
				name := getContainerName(container.Names)
				// Observability infrastructure must never be a fault target.
				for _, blocked := range observabilityBlocklist {
					if contains(name, blocked) {
						return fmt.Errorf(
							"selector pattern %q resolved to observability container %q — refusing to inject faults into monitoring infrastructure",
							targetSpec.Selector.Pattern, name,
						)
					}
				}
				target := TargetInfo{
					Alias:       targetSpec.Alias,
					ContainerID: container.ID,
					Name:        name,
					IP:          getContainerIP(container),
				}
				o.targets = append(o.targets, target)
				fmt.Printf("    ✓ Found: %s (%s)\n", target.Name, target.ContainerID[:12])
				matched = true
			}
		}

		if !matched {
			fmt.Printf("    ⚠ No containers found matching pattern: %s\n", targetSpec.Selector.Pattern)
		}
	}

	if len(o.targets) == 0 {
		return fmt.Errorf("no target containers found matching any selector patterns")
	}

	fmt.Printf("✓ Discovered %d target(s)\n", len(o.targets))
	return nil
}

// executePrepare creates sidecars for all targets
func (o *Orchestrator) executePrepare(ctx context.Context) error {
	// Check and clean target namespaces before creating sidecars
	fmt.Println("Checking target namespaces for remnant artifacts...")
	for _, target := range o.targets {
		// First check if there are tc rules
		result, err := o.verifier.VerifyNamespaceClean(ctx, target.ContainerID)
		if err != nil {
			fmt.Printf("  ⚠ Failed to verify %s: %v\n", target.Name, err)
			continue
		}

		if !result.Clean && result.TCRulesFound {
			fmt.Printf("  Found remnant tc rules on %s, running comcast --stop...\n", target.Name)

			// Create temporary sidecar to run comcast --stop
			tempSidecarID, err := o.sidecarMgr.CreateSidecar(ctx, target.ContainerID)
			if err != nil {
				fmt.Printf("  ⚠ Failed to create temp sidecar for %s: %v\n", target.Name, err)
				continue
			}

			// Execute comcast --stop in the sidecar
			stopCmd := []string{"comcast", "--stop"}
			_, execErr := o.dockerClient.ExecCommand(ctx, tempSidecarID, stopCmd)

			// Destroy temp sidecar
			removeOptions := types.ContainerRemoveOptions{
				Force:         true,
				RemoveVolumes: true,
			}
			o.dockerClient.ContainerRemove(ctx, tempSidecarID, removeOptions)

			if execErr != nil {
				fmt.Printf("  ⚠ Failed to run comcast --stop: %v\n", execErr)
			} else {
				fmt.Printf("  ✓ Cleaned tc rules on %s\n", target.Name)
			}
		}
	}
	fmt.Println("✓ Target namespace check complete")

	fmt.Println("Preparing sidecars...")

	for _, target := range o.targets {
		fmt.Printf("  Creating sidecar for %s (%s)...\n", target.Name, target.ContainerID[:12])

		sidecarID, err := o.sidecarMgr.CreateSidecar(ctx, target.ContainerID)
		if err != nil {
			return fmt.Errorf("failed to create sidecar for %s: %w", target.Name, err)
		}

		fmt.Printf("    ✓ Sidecar created: %s\n", sidecarID[:12])
	}

	fmt.Printf("✓ Created %d sidecar(s)\n", len(o.targets))
	return nil
}

// executeWarmup waits for the warmup period
func (o *Orchestrator) executeWarmup(ctx context.Context) error {
	warmup := o.scenario.Spec.Warmup
	if warmup == 0 {
		warmup = o.cfg.Execution.DefaultWarmup
	}

	fmt.Printf("Warmup period: %s\n", warmup)
	if err := o.interruptibleSleep(ctx, warmup); err != nil {
		return err
	}
	fmt.Println("✓ Warmup complete")
	return nil
}

// executePreCheck evaluates only the CRITICAL success criteria before fault injection
// to verify the system is in steady state. Non-critical criteria are intentionally
// skipped because:
//   - They cannot abort the experiment regardless of their result.
//   - Many measure rare events (e.g. span rotation every ~3.5h) or post-fault recovery
//     conditions that will always return 0 in a healthy pre-fault window, producing
//     misleading noise without actionable information.
func (o *Orchestrator) executePreCheck(ctx context.Context) error {
	if len(o.scenario.Spec.SuccessCriteria) == 0 {
		return nil
	}
	if o.detector == nil || o.promClient == nil {
		return fmt.Errorf("Prometheus is not configured but success criteria are defined — cannot validate experiment")
	}

	// Collect only critical criteria that verify steady-state health.
	// Skip criteria marked post_fault_only — they verify fault effectiveness
	// and are expected to fail before injection.
	var critical []int
	for i, c := range o.scenario.Spec.SuccessCriteria {
		if !c.Critical || c.PostFaultOnly || c.DuringFault {
			continue
		}
		critical = append(critical, i)
	}
	if len(critical) == 0 {
		fmt.Println("Pre-fault health check: no steady-state criteria to check, skipping")
		return nil
	}

	fmt.Printf("Pre-fault health check: verifying steady state before injection (%d critical criteria)...\n",
		len(critical))

	for n, i := range critical {
		criterion := o.scenario.Spec.SuccessCriteria[i]
		fmt.Printf("  [%d/%d] Checking: %s\n", n+1, len(critical), criterion.Name)

		result, err := o.detector.Evaluate(ctx, criterion)
		if err != nil {
			return fmt.Errorf("pre-check query failed for %q: %w", criterion.Name, err)
		}

		if result.Passed {
			fmt.Printf("    ✓ %s\n", result.Message)
		} else {
			fmt.Printf("    ✗ FAILED (CRITICAL): %s\n", result.Message)
			if criterion.Description != "" {
				fmt.Printf("      → %s\n", criterion.Description)
			}
			return fmt.Errorf("pre-fault health check failed: system not in steady state — aborting experiment")
		}
	}

	fmt.Println("✓ System in steady state — proceeding with fault injection")
	return nil
}

// executeInject injects all faults simultaneously using goroutines.
// Each fault targets a different set of containers so concurrent injection is safe.
func (o *Orchestrator) executeInject(ctx context.Context) error {
	o.injectTime = time.Now() // record fault window start for log scoping
	fmt.Println("Injecting faults...")

	if len(o.scenario.Spec.Faults) == 0 {
		fmt.Println("  ⚠ No faults defined in scenario")
		return nil
	}

	// faultJob pairs a fault with its resolved targets.
	type faultJob struct {
		index   int
		fault   scenario.Fault
		targets []TargetInfo
	}

	// Resolve targets for every fault (sequential, cheap).
	var jobs []faultJob
	for i, fault := range o.scenario.Spec.Faults {
		var targets []TargetInfo
		for _, t := range o.targets {
			if t.Alias == fault.Target {
				targets = append(targets, t)
			}
		}
		if len(targets) == 0 {
			fmt.Printf("  ⚠ No targets found for fault %q (alias: %s)\n", fault.Phase, fault.Target)
			continue
		}
		jobs = append(jobs, faultJob{index: i, fault: fault, targets: targets})
	}

	// injectResult carries the outcome of one goroutine.
	type injectResult struct {
		job faultJob
		err error
	}

	// Fire all injections concurrently so every fault starts at the same instant.
	results := make([]injectResult, len(jobs))
	var wg sync.WaitGroup
	for i, job := range jobs {
		i, job := i, job
		wg.Add(1)
		go func() {
			defer wg.Done()
			injTargets := make([]injection.Target, len(job.targets))
			for j, t := range job.targets {
				injTargets[j] = injection.Target{Name: t.Name, ContainerID: t.ContainerID}
			}
			fmt.Printf("  → injecting %s on %d container(s)...\n", job.fault.Phase, len(injTargets))
			results[i] = injectResult{
				job: job,
				err: o.injector.InjectFault(ctx, &job.fault, injTargets),
			}
		}()
	}
	wg.Wait()

	// Collect outcomes (all goroutines finished — no races on injectedFaults map).
	for _, r := range results {
		if r.err != nil {
			return fmt.Errorf("inject %q: %w", r.job.fault.Phase, r.err)
		}
		for _, t := range r.job.targets {
			o.injectedFaults[t.ContainerID] = r.job.fault.Type
			fmt.Printf("  ✓ %s on %s (%s)\n", r.job.fault.Phase, t.Name, t.ContainerID[:12])
		}
	}

	fmt.Printf("✓ %d fault(s) injected simultaneously on %d target(s)\n",
		len(jobs), len(o.injectedFaults))

	// Post-injection verification: confirm tc rules are actually in place.
	if err := o.verifyFaultsActive(ctx); err != nil {
		return err
	}

	return nil
}

// verifyFaultsActive checks that network faults are actually applied by
// inspecting tc qdisc rules via each target's sidecar. This is the only way
// to confirm comcast/tc commands took effect — without it, a silently failed
// injection is indistinguishable from a successful one.
func (o *Orchestrator) verifyFaultsActive(ctx context.Context) error {
	fmt.Println("Verifying faults are active...")

	verified := 0
	for containerID, faultType := range o.injectedFaults {
		if faultType != "network" {
			verified++
			continue
		}

		// Find target name for display
		targetName := containerID[:12]
		for _, t := range o.targets {
			if t.ContainerID == containerID {
				targetName = t.Name
				break
			}
		}

		// Run tc qdisc show inside the sidecar (shares target's network namespace)
		output, err := o.sidecarMgr.ExecInSidecar(ctx, containerID, []string{"tc", "qdisc", "show", "dev", "eth0"})
		if err != nil {
			return fmt.Errorf("fault verification failed on %s: could not inspect tc rules: %w", targetName, err)
		}

		if !strings.Contains(output, "netem") && !strings.Contains(output, "tbf") {
			return fmt.Errorf("fault verification failed on %s: no netem/tbf rules found after injection (tc output: %s)", targetName, strings.TrimSpace(output))
		}

		// Print the active rule so operators can confirm parameters
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "netem") || strings.Contains(line, "tbf") {
				fmt.Printf("  ✓ %s: %s\n", targetName, line)
			}
		}
		verified++
	}

	fmt.Printf("✓ Verified %d fault(s) active\n", verified)
	return nil
}

// executeMonitor monitors system metrics during the test
func (o *Orchestrator) executeMonitor(ctx context.Context) error {
	duration := o.scenario.Spec.Duration
	fmt.Printf("Monitoring for: %s\n", duration)

	// Start real-time log watcher — streams container logs and prints
	// ERROR/CRIT/PANIC/FATAL lines to stdout as they happen.
	var logWatcher *logcollector.Watcher
	if o.logCollector != nil && len(o.targets) > 0 {
		watchTargets := make([]logcollector.WatchTarget, len(o.targets))
		for i, t := range o.targets {
			watchTargets[i] = logcollector.WatchTarget{
				ContainerID: t.ContainerID,
				Name:        t.Name,
			}
		}
		logWatcher = logcollector.NewWatcher(o.dockerClient, watchTargets)
		since := o.injectTime
		if since.IsZero() {
			since = o.startTime
		}
		fmt.Println("  Starting real-time log watcher...")
		logWatcher.Start(ctx, since)
	}

	if o.collector != nil && o.promClient != nil {
		// Reconfigure collector with scenario metrics
		o.collector = collector.New(collector.Config{
			PrometheusClient: o.promClient,
			Interval:         o.cfg.Prometheus.RefreshInterval,
			MetricNames:      o.scenario.Spec.Metrics,
		})

		// Start collecting metrics
		fmt.Println("  Starting metrics collection...")
		o.collector.Start(ctx)

		// Monitor for the duration (interruptible)
		if err := o.interruptibleSleep(ctx, duration); err != nil {
			o.collector.Stop()
			if logWatcher != nil {
				logWatcher.Stop()
			}
			return err
		}

		// Stop collection
		o.collector.Stop()
		fmt.Println("  Metrics collection stopped")
	} else {
		fmt.Println("  Prometheus not available, monitoring duration only")
		if err := o.interruptibleSleep(ctx, duration); err != nil {
			if logWatcher != nil {
				logWatcher.Stop()
			}
			return err
		}
	}

	if logWatcher != nil {
		logWatcher.Stop()
		fmt.Println("  Log watcher stopped")
	}

	fmt.Println("Monitoring complete")

	// Evaluate during-fault criteria now, while faults are still active.
	if err := o.evaluateDuringFaultCriteria(ctx); err != nil {
		return err
	}

	return nil
}

// evaluateDuringFaultCriteria evaluates criteria marked during_fault: true
// at the end of the MONITOR phase while faults are still injected.
func (o *Orchestrator) evaluateDuringFaultCriteria(ctx context.Context) error {
	var duringFault []int
	for i, c := range o.scenario.Spec.SuccessCriteria {
		if c.DuringFault {
			duringFault = append(duringFault, i)
		}
	}
	if len(duringFault) == 0 {
		return nil
	}

	if o.detector == nil || o.promClient == nil {
		return fmt.Errorf("Prometheus is not configured but during_fault criteria are defined — cannot validate")
	}

	fmt.Printf("\nEvaluating during-fault criteria (%d) while faults are active...\n", len(duringFault))

	criticalFailed := false
	for j, idx := range duringFault {
		criterion := o.scenario.Spec.SuccessCriteria[idx]
		fmt.Printf("  [%d/%d] Evaluating: %s\n", j+1, len(duringFault), criterion.Name)

		result, err := o.detector.Evaluate(ctx, criterion)
		if err != nil {
			return fmt.Errorf("during-fault criteria query failed for %q: %w", criterion.Name, err)
		}

		o.criteriaResults = append(o.criteriaResults, CriterionOutcome{
			Name:        criterion.Name,
			Description: criterion.Description,
			Type:        criterion.Type,
			Query:       criterion.Query,
			Threshold:   criterion.Threshold,
			Passed:      result.Passed,
			Value:       result.LastValue,
			Message:     result.Message,
			Critical:    criterion.Critical,
		})

		if result.Passed {
			fmt.Printf("    ✓ PASSED: %s\n", result.Message)
		} else if criterion.Critical {
			fmt.Printf("    ✗ FAILED (CRITICAL): %s\n", result.Message)
			if criterion.Description != "" {
				fmt.Printf("      → %s\n", criterion.Description)
			}
			criticalFailed = true
		} else {
			fmt.Printf("    ⚠ FAILED (non-critical): %s\n", result.Message)
			if criterion.Description != "" {
				fmt.Printf("      → %s\n", criterion.Description)
			}
		}
	}

	if criticalFailed {
		return fmt.Errorf("one or more critical during-fault criteria failed")
	}

	return nil
}

// executeDetect evaluates success criteria
func (o *Orchestrator) executeDetect(ctx context.Context) error {
	fmt.Println("Evaluating success criteria...")

	if len(o.scenario.Spec.SuccessCriteria) == 0 {
		fmt.Println("  ⚠ No success criteria defined")
		return nil
	}

	if o.detector == nil || o.promClient == nil {
		return fmt.Errorf("Prometheus is not configured but success criteria are defined — cannot validate experiment")
	}

	// Evaluate each criterion
	allPassed := true
	criticalFailed := false
	var failedCritical []string

	for i, criterion := range o.scenario.Spec.SuccessCriteria {
		// Skip during_fault criteria — they were already evaluated at end of MONITOR.
		if criterion.DuringFault {
			continue
		}

		fmt.Printf("  [%d/%d] Evaluating: %s\n", i+1, len(o.scenario.Spec.SuccessCriteria), criterion.Name)

		result, err := o.detector.Evaluate(ctx, criterion)
		if err != nil {
			return fmt.Errorf("criteria query failed for %q: %w", criterion.Name, err)
		}

		// Store for the final report
		o.criteriaResults = append(o.criteriaResults, CriterionOutcome{
			Name:        criterion.Name,
			Description: criterion.Description,
			Type:        criterion.Type,
			Query:       criterion.Query,
			Threshold:   criterion.Threshold,
			Passed:      result.Passed,
			Value:       result.LastValue,
			Message:     result.Message,
			Critical:    criterion.Critical,
		})

		if result.Passed {
			fmt.Printf("    ✓ PASSED: %s\n", result.Message)
		} else {
			if criterion.Critical {
				fmt.Printf("    ✗ FAILED (CRITICAL): %s\n", result.Message)
				if criterion.Description != "" {
					fmt.Printf("      → %s\n", criterion.Description)
				}
				criticalFailed = true
				failedCritical = append(failedCritical, criterion.Name)
			} else {
				fmt.Printf("    ⚠ FAILED (non-critical): %s\n", result.Message)
				if criterion.Description != "" {
					fmt.Printf("      → %s\n", criterion.Description)
				}
			}
			allPassed = false
		}
	}

	// Print a clear failure banner so the cause is visible above the log digest.
	if criticalFailed {
		fmt.Printf("\n╔══ CRITICAL FAILURE ══════════════════════════════════════════════════╗\n")
		for _, name := range failedCritical {
			fmt.Printf("║  ✗ %s\n", name)
		}
		fmt.Printf("╚══════════════════════════════════════════════════════════════════════╝\n")
	}

	// On any failure, collect and print service logs from fault-injected targets
	// to surface error messages from Bor/Heimdall that explain the failure.
	if !allPassed {
		o.collectAndPrintServiceLogs(ctx)
	}

	if criticalFailed {
		return fmt.Errorf("one or more critical success criteria failed")
	}

	if allPassed {
		fmt.Println("✓ All success criteria passed")
	} else {
		fmt.Println("⚠ Some non-critical criteria failed")
	}

	return nil
}

// collectAndPrintServiceLogs fetches recent logs from each fault-injected target,
// filters for error-level entries, prints a digest to stdout, and saves the full
// tail to the report directory. Called only on test failure — never on success.
func (o *Orchestrator) collectAndPrintServiceLogs(ctx context.Context) {
	if o.logCollector == nil || len(o.targets) == 0 {
		return
	}

	// Scope log capture to the fault window (inject → now), not the full test.
	// Using o.startTime would pull in pre-fault and post-teardown cleanup logs
	// (e.g. normal "Demoting invalidated transaction" WARNs on reconnect) that
	// obscure the errors that actually explain the failure.
	since := o.injectTime
	if since.IsZero() {
		since = o.startTime // fallback: inject phase never started
	}

	var snapshots []*logcollector.ServiceLogSnapshot
	for _, target := range o.targets {
		snap := o.logCollector.Snapshot(ctx, target.ContainerID, target.Name, since, 300)
		if snap != nil {
			snapshots = append(snapshots, snap)
		}
	}

	logcollector.PrintSummary(snapshots)

	logDir := fmt.Sprintf("%s/logs/%s", o.cfg.Reporting.OutputDir, o.testID)
	logcollector.Save(snapshots, logDir)
}

// executeCooldown waits for the cooldown period
func (o *Orchestrator) executeCooldown(ctx context.Context) error {
	cooldown := o.scenario.Spec.Cooldown
	if cooldown == 0 {
		cooldown = o.cfg.Execution.DefaultCooldown
	}

	fmt.Printf("Cooldown period: %s\n", cooldown)
	if err := o.interruptibleSleep(ctx, cooldown); err != nil {
		return err
	}
	fmt.Println("✓ Cooldown complete")
	return nil
}

// interruptibleSleep sleeps for duration but can be interrupted by context cancellation or stop request
func (o *Orchestrator) interruptibleSleep(ctx context.Context, duration time.Duration) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	deadline := time.Now().Add(duration)

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("interrupted by context cancellation")
		case <-ticker.C:
			// Check if stop was requested
			if o.stopRequested.Load() {
				return fmt.Errorf("interrupted by emergency stop")
			}
			// Check if duration elapsed
			if time.Now().After(deadline) {
				return nil
			}
		}
	}
}

// executeTeardown removes all faults
func (o *Orchestrator) executeTeardown(ctx context.Context) error {
	fmt.Println("Tearing down faults...")

	if len(o.injectedFaults) == 0 {
		fmt.Println("  No faults to remove")
	} else {
		// Remove faults from each target
		removed := 0
		for containerID, faultType := range o.injectedFaults {
			// Find target name
			targetName := containerID[:12]
			for _, target := range o.targets {
				if target.ContainerID == containerID {
					targetName = target.Name
					break
				}
			}

			fmt.Printf("  Removing %s fault from %s...\n", faultType, targetName)

			if err := o.injector.RemoveFault(ctx, faultType, containerID); err != nil {
				fmt.Printf("    ⚠ Error removing fault: %v\n", err)
				// Continue with other targets
			} else {
				fmt.Printf("    ✓ Fault removed\n")
				removed++
			}
		}

		fmt.Printf("✓ Removed faults from %d target(s)\n", removed)
	}

	// Use cleanup coordinator to destroy sidecars and log actions
	fmt.Println("Cleaning up chaos artifacts...")
	if err := o.cleanupCoord.CleanupAll(ctx); err != nil {
		fmt.Printf("  ⚠ Cleanup completed with errors: %v\n", err)
		// Don't fail the test, but log the error
	}

	return nil
}

// executeReport generates the final test report
func (o *Orchestrator) executeReport(ctx context.Context, result *TestResult) error {
	fmt.Println("Generating report...")

	// Report is populated by the caller
	// This method can be used for additional reporting logic

	fmt.Println("✓ Report generated")
	return nil
}

// RequestStop requests the orchestrator to stop execution
func (o *Orchestrator) RequestStop() {
	fmt.Println("Stop requested!")
	o.stopRequested.Store(true)
}

// preFlightCleanup removes remnants from previous failed/interrupted tests
func (o *Orchestrator) preFlightCleanup(ctx context.Context) error {
	fmt.Println("🔍 Pre-flight check: Looking for remnants from previous tests...")

	// Check for and remove any existing chaos-sidecar containers
	listOptions := types.ContainerListOptions{
		All: true, // Include stopped containers
	}

	allContainers, err := o.dockerClient.ContainerList(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	// Filter for chaos-sidecar containers
	var sidecars []types.Container
	for _, container := range allContainers {
		for _, name := range container.Names {
			// Docker names start with "/" prefix
			if len(name) > 0 && len(name) > 14 && name[1:14] == "chaos-sidecar" {
				sidecars = append(sidecars, container)
				break
			}
		}
	}

	if len(sidecars) > 0 {
		fmt.Printf("   Found %d remnant sidecar container(s) from previous tests\n", len(sidecars))
		for _, container := range sidecars {
			fmt.Printf("   Removing sidecar: %s...\n", container.ID[:12])
			removeOptions := types.ContainerRemoveOptions{
				Force:         true,
				RemoveVolumes: true,
			}
			if err := o.dockerClient.ContainerRemove(ctx, container.ID, removeOptions); err != nil {
				fmt.Printf("   ⚠ Failed to remove %s: %v\n", container.ID[:12], err)
			} else {
				fmt.Printf("   ✓ Removed %s\n", container.ID[:12])
			}
		}
	}

	// Check for and remove emergency stop file
	emergencyFile := o.cfg.Emergency.StopFile
	if _, err := os.Stat(emergencyFile); err == nil {
		fmt.Printf("   Found emergency stop file: %s\n", emergencyFile)
		if err := os.Remove(emergencyFile); err != nil {
			fmt.Printf("   ⚠ Failed to remove emergency stop file: %v\n", err)
		} else {
			fmt.Println("   ✓ Removed emergency stop file")
		}
	}

	fmt.Println("✓ Pre-flight check complete")
	return nil
}

// GetCleanupSummary returns the cleanup summary from the coordinator
func (o *Orchestrator) GetCleanupSummary() cleanup.CleanupSummary {
	return o.cleanupCoord.GetSummary()
}

// Helper to fail a test
func (o *Orchestrator) failTest(result *TestResult, err error) (*TestResult, error) {
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)
	result.State = StateFailed
	result.Success = false
	result.Message = err.Error()
	result.Errors = append(result.Errors, err)
	result.Targets = o.targets
	result.FaultCount = len(o.injectedFaults)
	result.CriteriaResults = o.criteriaResults
	return result, err
}

// generateTestID creates a unique test ID
func generateTestID() string {
	return fmt.Sprintf("test-%d", time.Now().Unix())
}

// Helper functions

// matchPattern checks if any name in the list matches the pattern
func matchPattern(names []string, pattern string) bool {
	for _, name := range names {
		// Simple substring matching (can be enhanced with regex)
		if len(name) > 0 && name[0] == '/' {
			name = name[1:] // Remove leading slash from Docker name
		}
		if match(name, pattern) {
			return true
		}
	}
	return false
}

// match performs pattern matching (supports * wildcard and regex)
func match(name, pattern string) bool {
	// Match all wildcard
	if pattern == "*" {
		return true
	}

	// Try regex first (if pattern looks like regex)
	// Regex patterns typically contain: \d, \w, \s, [, ], (, ), {, }, ^, $, etc.
	isRegex := strings.ContainsAny(pattern, `\[](){}^$+?.|`)
	if isRegex {
		re, err := regexp.Compile(pattern)
		if err == nil {
			return re.MatchString(name)
		}
		// If regex compilation fails, fall through to simple matching
	}

	// Simple wildcard matching with *
	if len(pattern) > 0 && pattern[0] == '*' {
		pattern = pattern[1:]
	}
	if len(pattern) > 0 && pattern[len(pattern)-1] == '*' {
		pattern = pattern[:len(pattern)-1]
	}

	// Simple contains check
	return len(pattern) > 0 && contains(name, pattern)
}

// contains checks if s contains substr
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || hasSubstring(s, substr))
}

// hasSubstring checks if s contains substr
func hasSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// getContainerName extracts a clean name from Docker names list
func getContainerName(names []string) string {
	if len(names) == 0 {
		return "unknown"
	}
	name := names[0]
	if len(name) > 0 && name[0] == '/' {
		return name[1:]
	}
	return name
}

// getContainerIP extracts IP address from container
func getContainerIP(container types.Container) string {
	// This is a simplified version - in production would extract from NetworkSettings
	return ""
}

// validateKurtosisEnclave runs "kurtosis enclave inspect <name>" to confirm the
// enclave is active. Returns a clear error if the enclave does not exist or the
// Kurtosis CLI is unavailable.
func validateKurtosisEnclave(enclaveName string) error {
	cmd := exec.Command("kurtosis", "enclave", "inspect", enclaveName)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("Kurtosis enclave %q not found or not running: %s", enclaveName, msg)
	}
	return nil
}
