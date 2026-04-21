package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
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

// CriteriaFailureError marks a test that completed orchestration cleanly but
// had one or more critical success criteria fail. Callers use errors.As to
// distinguish these legitimate test findings (exit 1) from true infrastructure
// errors (exit 2) so CI doesn't halt the suite on a plain criteria miss.
type CriteriaFailureError struct {
	Msg string
}

func (e *CriteriaFailureError) Error() string { return e.Msg }

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
	heimdallAPI  string
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
	// injectedFaults tracks every fault currently installed on a container
	// as an ordered slice so that:
	//   - multiple faults on the same container are not conflated (a single
	//     map[containerID]faultType loses all but the last one, leaving
	//     earlier faults installed on teardown — F-02),
	//   - teardown can iterate in reverse injection order so stacked tc
	//     qdiscs / iptables rules come off in LIFO order.
	injectedFaults  []injectedFault
	criteriaResults []CriterionOutcome      // populated during DETECT phase

	// duringFaultSampler runs concurrently with INJECT/MONITOR and samples
	// during_fault criteria repeatedly. Required because some inject calls
	// (e.g. container_pause with Duration set) self-terminate inside INJECT
	// and are already over by the time MONITOR completes — if we only
	// evaluated once at end of MONITOR, the criterion would see post-fault
	// state and report a misleading pass/fail.
	dfSampler *duringFaultSampler

	// faultVerificationWarnings counts faults that passed InjectFault's own
	// error check but failed the orchestrator's post-injection verification.
	// Non-zero means the test ran with at least one fault whose observable
	// side effect could not be confirmed.
	faultVerificationWarnings int
}

// injectedFault records one fault installed on one container during INJECT.
// A container can appear more than once when multiple faults target it
// (e.g. disk_io + network). Teardown iterates the slice in reverse so
// stacked tc/iptables rules are removed in LIFO order.
type injectedFault struct {
	ContainerID string
	FaultType   string
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
	TestID                    string
	ScenarioName              string
	StartTime                 time.Time
	EndTime                   time.Time
	Duration                  time.Duration
	State                     TestState
	Success                   bool
	Message                   string
	Errors                    []error
	Targets                   []TargetInfo
	FaultCount                int
	CriteriaResults           []CriterionOutcome
	FaultVerificationWarnings int
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
		emergencyCancel()
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
		injectedFaults:   nil, // lazily appended during INJECT
	}, nil
}

// Execute runs the complete chaos test lifecycle against an already-parsed
// scenario. Callers (cmd/chaos-runner) are responsible for parsing, applying
// any --set overrides, and validating before handing the struct to Execute.
// scenarioPath is purely for reporting/logging — the orchestrator does NOT
// re-read the file, because doing so would silently discard any overrides
// the caller applied to the in-memory struct (F-04).
func (o *Orchestrator) Execute(ctx context.Context, scen *scenario.Scenario, scenarioPath string) (*TestResult, error) {
	if scen == nil {
		return nil, fmt.Errorf("orchestrator.Execute: scenario is nil")
	}
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

	// Always run cleanup on exit. This path runs on success, failure, and
	// panic. Besides sidecar destruction, it also tears down any faults
	// that were recorded in injectedFaults but never went through the
	// normal executeTeardown state (inject-error, early abort, emergency
	// stop). Without this, a partial-inject failure leaves tc / iptables /
	// stress state installed on the target kernel namespace until the next
	// run's pre-flight tries to sweep it — and pre-flight only handles tc.
	defer func() {
		if len(o.injectedFaults) > 0 && o.currentState != StateCompleted {
			fmt.Println("Cleaning up faults recorded before abort...")
			o.removeTrackedFaults(ctx)
		}
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

	// PARSE state — validate the pre-parsed scenario. We do NOT re-read
	// scenarioPath here: the caller has already parsed it, applied --set
	// overrides, and validated. Re-parsing would discard the overrides
	// (historical bug: --set duration=1m silently became the in-file value).
	o.transitionState(StateParse)
	if err = o.executeParse(ctx, scen); err != nil {
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

	// Topology preconditions: a scenario may require a minimum number of
	// validators to exercise its fault path meaningfully. Fail fast here,
	// before we start creating sidecars, so the operator gets a clear error.
	if err = o.executePreconditions(ctx); err != nil {
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

	// Start the during-fault sampler BEFORE inject. Some fault types
	// (notably container_pause with Duration set) block their InjectFault
	// call for the full fault window and self-terminate inside INJECT.
	// If we only evaluate during_fault criteria after MONITOR, those
	// faults are already gone and the criteria observe post-fault state.
	// The sampler polls every 15s throughout INJECT + MONITOR and keeps
	// the worst reading per criterion, so a single violation is recorded
	// no matter when it occurs.
	o.dfSampler = newDuringFaultSampler(o.detector, o.scenario.Spec.SuccessCriteria, 15*time.Second)
	o.dfSampler.Start(ctx)

	// INJECT state
	o.transitionState(StateInject)
	if err = o.executeInject(ctx); err != nil {
		o.dfSampler.Stop()
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
	//
	// Capture the install count BEFORE teardown runs: removeTrackedFaults
	// nils injectedFaults on exit (for idempotency w.r.t. the deferred
	// abort-path cleanup), so reading len(o.injectedFaults) at success
	// time would always see 0 (F-11).
	faultInstallCount := len(o.injectedFaults)
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

	// REPORT state — the caller (cmd/chaos-runner) emits the final report
	// after Execute returns, so there is no orchestrator-side work here
	// beyond the state transition.
	o.transitionState(StateReport)

	// Success!
	o.transitionState(StateCompleted)
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)
	result.State = StateCompleted
	result.Success = true
	result.Message = "Test completed successfully"
	result.Targets = o.targets
	result.FaultCount = faultInstallCount
	result.CriteriaResults = o.criteriaResults
	result.FaultVerificationWarnings = o.faultVerificationWarnings

	return result, nil
}

// State transition method
func (o *Orchestrator) transitionState(newState TestState) {
	fmt.Printf("[%s] → [%s]\n", o.currentState, newState)
	o.currentState = newState
}

// executeParse attaches the pre-parsed scenario to the orchestrator and
// re-runs validation as a defence-in-depth check. It does NOT read the
// scenarioPath file — the caller has already parsed it and applied any
// --set overrides to the struct. Re-reading here would silently discard
// those overrides, which was the original F-04 bug.
func (o *Orchestrator) executeParse(ctx context.Context, scen *scenario.Scenario) error {
	fmt.Printf("Loading scenario: %s\n", o.scenarioPath)

	if scen == nil {
		return fmt.Errorf("executeParse: scenario is nil")
	}

	// Re-validate (cheap, catches programmatic callers that skip validation).
	if err := o.validator.Validate(scen); err != nil {
		return fmt.Errorf("scenario validation failed: %w", err)
	}

	o.scenario = scen
	fmt.Printf("✓ Loaded scenario: %s\n", scen.Metadata.Name)
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
					if strings.Contains(name, blocked) {
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

// defaultValidatorPattern is the Polygon PoS Kurtosis naming convention for
// Heimdall consensus-layer validators. Used as the fallback when a scenario
// declares Preconditions without an explicit ValidatorPattern.
const defaultValidatorPattern = `l2-cl-[0-9]+-heimdall-v2-bor-validator`

// executePreconditions enforces topology requirements declared in the
// scenario. Runs after target discovery and before sidecar preparation.
// A no-op when the scenario does not declare any preconditions.
func (o *Orchestrator) executePreconditions(ctx context.Context) error {
	pre := o.scenario.Spec.Preconditions
	if pre == nil || pre.MinValidators <= 0 {
		return nil
	}

	pattern := pre.ValidatorPattern
	if pattern == "" {
		pattern = defaultValidatorPattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("preconditions: invalid validator_pattern %q: %w", pattern, err)
	}

	containers, err := o.dockerClient.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		return fmt.Errorf("preconditions: failed to list containers: %w", err)
	}

	// Deduplicate by container ID so a container with multiple names is
	// counted once.
	matched := make(map[string]string)
	for _, c := range containers {
		for _, n := range c.Names {
			if len(n) > 0 && n[0] == '/' {
				n = n[1:]
			}
			if re.MatchString(n) {
				matched[c.ID] = n
				break
			}
		}
	}

	if len(matched) < pre.MinValidators {
		return fmt.Errorf(
			"preconditions: scenario requires at least %d validators (matching %q), "+
				"but only %d are deployed — expand the devnet or run a scenario with lower topology requirements",
			pre.MinValidators, pattern, len(matched),
		)
	}

	fmt.Printf("✓ Preconditions satisfied: %d validator(s) found (min: %d)\n",
		len(matched), pre.MinValidators)
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
			fmt.Printf("  Found remnant tc rules on %s, clearing...\n", target.Name)

			// Create temporary sidecar to clear tc rules
			tempSidecarID, err := o.sidecarMgr.CreateSidecar(ctx, target.ContainerID)
			if err != nil {
				fmt.Printf("  ⚠ Failed to create temp sidecar for %s: %v\n", target.Name, err)
				continue
			}

			// Remove tc rules directly
			clearCmd := []string{"tc", "qdisc", "del", "dev", "eth0", "root"}
			_, execErr := o.dockerClient.ExecCommand(ctx, tempSidecarID, clearCmd)

			// Destroy temp sidecar
			removeOptions := types.ContainerRemoveOptions{
				Force:         true,
				RemoveVolumes: true,
			}
			o.dockerClient.ContainerRemove(ctx, tempSidecarID, removeOptions)

			if execErr != nil {
				fmt.Printf("  ⚠ Failed to clear tc rules: %v\n", execErr)
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
	if o.detector == nil {
		return fmt.Errorf("detector is not configured but success criteria are defined — cannot validate experiment")
	}

	// Wire up log context for log-based criteria
	if o.dockerClient != nil {
		var logTargets []detector.LogTarget
		for _, t := range o.targets {
			logTargets = append(logTargets, detector.LogTarget{
				Alias:       t.Alias,
				ContainerID: t.ContainerID,
				Name:        t.Name,
			})
		}
		o.detector.SetLogContext(o.dockerClient, logTargets, o.startTime)
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

	// Exclude current block producer from jobs that request it.
	for i, job := range jobs {
		if !job.fault.ExcludeProducer {
			continue
		}
		producerName, err := o.resolveCurrentProducer(ctx)
		if err != nil {
			fmt.Printf("  ⚠ Could not resolve current producer for fault %q: %v\n", job.fault.Phase, err)
			continue
		}
		var filtered []TargetInfo
		for _, t := range job.targets {
			if t.Name == producerName {
				fmt.Printf("  ⊘ Excluding current block producer %s from fault %q\n", producerName, job.fault.Phase)
			} else {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			fmt.Printf("  ⚠ No targets remain after excluding producer for fault %q — skipping\n", job.fault.Phase)
		}
		jobs[i].targets = filtered
	}

	// Remove jobs with no targets after producer exclusion.
	{
		var remaining []faultJob
		for _, job := range jobs {
			if len(job.targets) > 0 {
				remaining = append(remaining, job)
			}
		}
		jobs = remaining
	}

	// Refuse any scenario that co-injects dns + network on the same container.
	// Both install a root tc qdisc; the second one silently wipes or clobbers
	// the first. Detect at plan time rather than debugging a missing fault.
	{
		perContainer := map[string]map[string]bool{}
		for _, job := range jobs {
			for _, t := range job.targets {
				if perContainer[t.ContainerID] == nil {
					perContainer[t.ContainerID] = map[string]bool{}
				}
				perContainer[t.ContainerID][job.fault.Type] = true
			}
		}
		for cid, faultTypes := range perContainer {
			if faultTypes["dns"] && faultTypes["network"] {
				name := cid[:12]
				for _, t := range o.targets {
					if t.ContainerID == cid {
						name = t.Name
						break
					}
				}
				return fmt.Errorf("dns and network faults cannot share a container (both install a root tc qdisc on eth0) — target %q has both", name)
			}
		}
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

			// Honor per-fault delay if specified (e.g., delay: 2m)
			if job.fault.Delay > 0 {
				fmt.Printf("  ⏳ %s: waiting %s before injection...\n", job.fault.Phase, job.fault.Delay)
				select {
				case <-time.After(job.fault.Delay):
				case <-ctx.Done():
					results[i] = injectResult{job: job, err: ctx.Err()}
					return
				}
			}

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

	// Collect outcomes (all goroutines finished — no races on injectedFaults).
	//
	// IMPORTANT (F-09): we must iterate EVERY result and record EVERY
	// success before we decide to return an error. An earlier short-circuit
	// that returned on the first failure dropped any later successes on the
	// floor — those injections had already run, installed real tc / iptables
	// / stress state on their targets, but teardown had no way to find them
	// because they were never appended to injectedFaults. The symptom was
	// leaked kernel-level chaos state surviving a partial-inject failure.
	//
	// Each (container, faultType) pair is recorded independently. Multiple
	// faults can share a container (e.g. compound disk_io + network), and
	// teardown will remove them in reverse order of injection.
	distinctContainers := map[string]struct{}{}
	var injectErrs []error
	for _, r := range results {
		if r.err != nil {
			injectErrs = append(injectErrs, fmt.Errorf("inject %q: %w", r.job.fault.Phase, r.err))
			continue
		}
		for _, t := range r.job.targets {
			o.injectedFaults = append(o.injectedFaults, injectedFault{
				ContainerID: t.ContainerID,
				FaultType:   r.job.fault.Type,
			})
			distinctContainers[t.ContainerID] = struct{}{}
			fmt.Printf("  ✓ %s on %s (%s)\n", r.job.fault.Phase, t.Name, t.ContainerID[:12])
		}
	}

	if len(injectErrs) > 0 {
		// Return the aggregated error AFTER every successful inject has
		// been appended to injectedFaults. The caller (Execute) will route
		// to failTest, which defers to the cleanup coordinator + teardown
		// path to remove whatever did install cleanly.
		return errors.Join(injectErrs...)
	}

	fmt.Printf("✓ %d fault(s) injected on %d distinct container(s)\n",
		len(o.injectedFaults), len(distinctContainers))

	// Post-injection verification: confirm tc rules are actually in place.
	if err := o.verifyFaultsActive(ctx); err != nil {
		return err
	}

	return nil
}

// verifyFaultsActive confirms each injected fault left observable state in
// the target's namespace/sidecar. Without this step a silently-failed exec
// (sidecar returned success but the rule was not applied) would be
// indistinguishable from a real injection.
//
// Verification failures are logged and counted but do NOT abort the run,
// because we may be inspecting a fault that has already produced the desired
// side effect and self-terminated (e.g. a short-lived p2p attack). The
// returned count of warnings is stored on the orchestrator so the final
// report can flag experiments where verification did not pass cleanly.
//
// Fault types whose implementations return synchronous errors on failure and
// have no separate post-install side effect to inspect (container lifecycle,
// process_kill, p2p_attack, file_delete, file_corrupt) are skipped here.
func (o *Orchestrator) verifyFaultsActive(ctx context.Context) error {
	fmt.Println("Verifying faults are active...")

	o.faultVerificationWarnings = 0
	verified := 0
	// Deduplicate: if two faults share a container and the same verify
	// family (e.g. network + dns), we only need to inspect once. But
	// semantically different faults (network + disk_io) must both be
	// verified, so we deduplicate on (containerID, faultType) pair.
	seen := map[string]struct{}{}
	for _, f := range o.injectedFaults {
		key := f.ContainerID + "\x00" + f.FaultType
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		containerID := f.ContainerID
		faultType := f.FaultType
		targetName := containerID[:12]
		for _, t := range o.targets {
			if t.ContainerID == containerID {
				targetName = t.Name
				break
			}
		}

		var verifyErr error
		switch faultType {
		case "network":
			verifyErr = o.verifyNetworkFault(ctx, containerID, targetName)
		case "dns":
			verifyErr = o.verifyDNSFault(ctx, containerID, targetName)
		case "connection_drop":
			verifyErr = o.verifyConnectionDropFault(ctx, containerID, targetName)
		case "http_fault", "corruption_proxy":
			verifyErr = o.verifyHTTPRedirect(ctx, containerID, targetName, faultType)
		case "disk_fill":
			verifyErr = o.verifyDiskFillFault(ctx, containerID, targetName)
		case "disk_io":
			verifyErr = o.verifyDiskIOFault(ctx, containerID, targetName)
		case "cpu_stress", "cpu", "memory_stress", "memory_pressure", "memory":
			verifyErr = o.verifyStressFault(ctx, containerID, targetName, faultType)
		}

		if verifyErr != nil {
			o.faultVerificationWarnings++
			fmt.Printf("  ⚠ %s: %v\n", targetName, verifyErr)
		}
		verified++
	}

	if o.faultVerificationWarnings > 0 {
		fmt.Printf("⚠ Verified %d fault(s), %d with verification warnings — see above\n", verified, o.faultVerificationWarnings)
	} else {
		fmt.Printf("✓ Verified %d fault(s) active\n", verified)
	}
	return nil
}

// verifyNetworkFault inspects tc qdisc/filters in the target's sidecar.
func (o *Orchestrator) verifyNetworkFault(ctx context.Context, containerID, targetName string) error {
	output, err := o.sidecarMgr.ExecInSidecar(ctx, containerID, []string{"tc", "qdisc", "show", "dev", "eth0"})
	if err != nil {
		return fmt.Errorf("could not inspect tc rules: %w", err)
	}
	if !strings.Contains(output, "netem") && !strings.Contains(output, "tbf") {
		return fmt.Errorf("no netem/tbf rules found after injection (tc output: %s)", strings.TrimSpace(output))
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "netem") || strings.Contains(line, "tbf") {
			fmt.Printf("  ✓ %s: %s\n", targetName, line)
		}
	}
	filterOutput, _ := o.sidecarMgr.ExecInSidecar(ctx, containerID, []string{"tc", "filter", "show", "dev", "eth0"})
	if filterOutput != "" && strings.Contains(filterOutput, "u32") {
		fmt.Printf("  ✓ %s: %d u32 port filter(s) active\n", targetName, strings.Count(filterOutput, "match"))
	}
	return nil
}

// verifyDNSFault confirms the DNS-specific prio band + filter are installed.
func (o *Orchestrator) verifyDNSFault(ctx context.Context, containerID, targetName string) error {
	output, err := o.sidecarMgr.ExecInSidecar(ctx, containerID, []string{"tc", "qdisc", "show", "dev", "eth0"})
	if err != nil {
		return fmt.Errorf("could not inspect tc rules: %w", err)
	}
	if !strings.Contains(output, "netem") {
		return fmt.Errorf("no netem qdisc found (tc output: %s)", strings.TrimSpace(output))
	}
	fmt.Printf("  ✓ %s: DNS netem qdisc active\n", targetName)
	return nil
}

// verifyConnectionDropFault confirms the CHAOS_DROP chain is populated and
// linked from INPUT.
func (o *Orchestrator) verifyConnectionDropFault(ctx context.Context, containerID, targetName string) error {
	output, err := o.sidecarMgr.ExecInSidecar(ctx, containerID, []string{"iptables", "-L", "CHAOS_DROP", "-n"})
	if err != nil {
		return fmt.Errorf("CHAOS_DROP chain not found: %w", err)
	}
	if !strings.Contains(output, "DROP") && !strings.Contains(output, "REJECT") {
		return fmt.Errorf("CHAOS_DROP chain has no rules (%s)", strings.TrimSpace(output))
	}
	fmt.Printf("  ✓ %s: CHAOS_DROP chain active\n", targetName)
	return nil
}

// verifyHTTPRedirect confirms PREROUTING contains a redirect rule with the
// expected chaos comment.
func (o *Orchestrator) verifyHTTPRedirect(ctx context.Context, containerID, targetName, faultType string) error {
	comment := "chaos-http-fault"
	if faultType == "corruption_proxy" {
		comment = "chaos-corruption-proxy"
	}
	output, err := o.sidecarMgr.ExecInSidecar(ctx, containerID, []string{"sh", "-c",
		fmt.Sprintf("iptables-save -t nat 2>/dev/null | grep -c %s || true", comment),
	})
	if err != nil {
		return fmt.Errorf("%s: %w", faultType, err)
	}
	if strings.TrimSpace(output) == "0" || strings.TrimSpace(output) == "" {
		return fmt.Errorf("no PREROUTING rule tagged %q", comment)
	}
	fmt.Printf("  ✓ %s: %s PREROUTING redirect active\n", targetName, faultType)
	return nil
}

// verifyDiskFillFault confirms a fill file exists somewhere under the target.
func (o *Orchestrator) verifyDiskFillFault(ctx context.Context, containerID, targetName string) error {
	output, err := o.dockerClient.ExecCommand(ctx, containerID, []string{"sh", "-c",
		"find /tmp /root /var/lib /home -maxdepth 6 -name 'chaos_fill_data' 2>/dev/null | head -1",
	})
	if err != nil {
		return fmt.Errorf("could not search for fill file: %w", err)
	}
	if strings.TrimSpace(output) == "" {
		return fmt.Errorf("no chaos_fill_data file found")
	}
	fmt.Printf("  ✓ %s: disk fill file %s\n", targetName, strings.TrimSpace(output))
	return nil
}

// verifyDiskIOFault confirms chaos dd stress workers are running in the target.
func (o *Orchestrator) verifyDiskIOFault(ctx context.Context, containerID, targetName string) error {
	output, err := o.dockerClient.ExecCommand(ctx, containerID, []string{"sh", "-c",
		"COUNT=0; for p in /proc/[0-9]*/cmdline; do " +
			"if tr '\\0' ' ' < $p 2>/dev/null | grep -q 'chaos_io_stress'; then COUNT=$((COUNT+1)); fi; " +
			"done; echo $COUNT",
	})
	if err != nil {
		return fmt.Errorf("could not count chaos_io_stress processes: %w", err)
	}
	count := strings.TrimSpace(output)
	if count == "" || count == "0" {
		return fmt.Errorf("no chaos_io_stress workers running")
	}
	fmt.Printf("  ✓ %s: %s disk I/O stress worker(s) active\n", targetName, count)
	return nil
}

// verifyStressFault confirms a stress mechanism is active. The stress
// injector supports two methods: active `yes` processes (method="stress")
// and cgroup limits (method="limit"). Either "yes" processes OR an updated
// cgroup (non-zero CPUQuota/Memory) counts as success.
func (o *Orchestrator) verifyStressFault(ctx context.Context, containerID, targetName, faultType string) error {
	output, err := o.dockerClient.ExecCommand(ctx, containerID, []string{"sh", "-c",
		"COUNT=0; for p in /proc/[0-9]*/cmdline; do " +
			"if tr '\\0' ' ' < $p 2>/dev/null | grep -q '^yes'; then COUNT=$((COUNT+1)); fi; " +
			"done; echo $COUNT",
	})
	if err != nil {
		return fmt.Errorf("could not count stress processes: %w", err)
	}
	count := strings.TrimSpace(output)
	if count != "" && count != "0" {
		fmt.Printf("  ✓ %s: %s stress worker(s) active (%s)\n", targetName, count, faultType)
		return nil
	}
	// No active stress processes; method may be "limit" (cgroup-only). In
	// that case there is no in-container side effect to inspect — accept it.
	inspect, inspectErr := o.dockerClient.ContainerInspect(ctx, containerID)
	if inspectErr != nil {
		return fmt.Errorf("no stress processes found and container inspect failed: %w", inspectErr)
	}
	if strings.HasPrefix(faultType, "memory") && inspect.HostConfig != nil && inspect.HostConfig.Memory > 0 {
		fmt.Printf("  ✓ %s: memory limit %d bytes applied (%s)\n", targetName, inspect.HostConfig.Memory, faultType)
		return nil
	}
	if strings.HasPrefix(faultType, "cpu") && inspect.HostConfig != nil && inspect.HostConfig.CPUQuota > 0 {
		fmt.Printf("  ✓ %s: cpu quota %d applied (%s)\n", targetName, inspect.HostConfig.CPUQuota, faultType)
		return nil
	}
	return fmt.Errorf("no stress workers and no cgroup limit visible (method=limit may not have been applied)")
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

// evaluateDuringFaultCriteria consumes the background sampler's results.
// The sampler has been running since before INJECT, so it will have captured
// observations even when the fault self-terminates inside INJECT (which
// blocks the caller — see startup in Execute). This method:
//   - stops the sampler and retrieves the worst-observed result per criterion
//   - records those outcomes into criteriaResults
//   - returns CriteriaFailureError if any critical during_fault criterion
//     failed at any point in the sampling window.
//
// If a criterion was NEVER sampled (sampler never got a chance to run, or
// criterion is log-based and needed log context that wasn't available until
// now) it is re-evaluated ONCE at end-of-monitor as a fallback. This catches
// the "sampler saw no data" case instead of silently dropping the criterion.
func (o *Orchestrator) evaluateDuringFaultCriteria(ctx context.Context) error {
	if o.dfSampler == nil {
		return nil
	}

	duringFault := o.dfSampler.indices
	if len(duringFault) == 0 {
		o.dfSampler.Stop()
		return nil
	}

	if o.detector == nil {
		o.dfSampler.Stop()
		return fmt.Errorf("detector is not configured but during_fault criteria are defined — cannot validate")
	}

	// Wire up log context for log-based criteria (sampler goroutine already
	// captured Prometheus-based readings; log criteria become evaluable here).
	if o.dockerClient != nil {
		var logTargets []detector.LogTarget
		for _, t := range o.targets {
			logTargets = append(logTargets, detector.LogTarget{
				Alias:       t.Alias,
				ContainerID: t.ContainerID,
				Name:        t.Name,
			})
		}
		since := o.injectTime
		if since.IsZero() {
			since = o.startTime
		}
		o.detector.SetLogContext(o.dockerClient, logTargets, since)
	}

	// Stop the sampler and collect worst-observed results.
	worst := o.dfSampler.StopAndCollect()

	fmt.Printf("\nEvaluating during-fault criteria (%d) — using worst-observed reading from %d samples across fault window...\n",
		len(duringFault), o.dfSampler.SampleCount())

	criticalFailed := false
	for j, idx := range duringFault {
		criterion := o.scenario.Spec.SuccessCriteria[idx]
		fmt.Printf("  [%d/%d] Evaluating: %s\n", j+1, len(duringFault), criterion.Name)

		result, haveSample := worst[criterion.Name]
		if !haveSample {
			// Fallback: sampler never got a reading (e.g. log criterion
			// needing log context, or sampler stopped before first tick).
			// Evaluate once now. This is LESS reliable than a during-fault
			// sample but better than silently skipping the criterion.
			fmt.Printf("      [fallback] no during-fault sample — evaluating once now (faults may already be torn down)\n")
			r, err := o.detector.EvaluateOnce(ctx, criterion)
			if err != nil {
				return fmt.Errorf("during-fault criteria query failed for %q: %w", criterion.Name, err)
			}
			result = r
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
		return &CriteriaFailureError{Msg: "one or more critical during-fault criteria failed"}
	}

	return nil
}

// universalSafetyCriteria returns criteria that every chaos scenario should
// evaluate regardless of what the YAML defines. These catch safety violations
// (double-signing, deep reorgs, state divergence) that individual scenario
// authors might forget to include.
//
// All universal criteria are post_fault_only (meaningless before injection)
// and non-critical (informational — they surface problems without overriding
// the scenario's own pass/fail logic). The one exception is byzantine
// detection which is always critical: double-signing is never acceptable.
func universalSafetyCriteria() []scenario.SuccessCriterion {
	return []scenario.SuccessCriterion{
		{
			Name:          "[universal] no_byzantine_validators",
			Description:   "No double-signing detected — any non-zero value is a critical safety violation",
			Type:          "prometheus",
			Query:         `max(cometbft_consensus_byzantine_validators{job=~"l2-cl-.*-heimdall-v2-bor-validator"}) or vector(0)`,
			Threshold:     "== 0",
			Critical:      true,
			PostFaultOnly: true,
		},
		{
			Name:          "[universal] reorg_depth_bounded",
			Description:   "No deep chain reorganizations (> 20 blocks dropped) during or after chaos",
			Type:          "prometheus",
			Query:         `sum(increase(chain_reorg_drop{job=~"l2-el-.*-bor-heimdall-v2-validator"}[5m])) or vector(0)`,
			Threshold:     "< 20",
			Critical:      false,
			PostFaultOnly: true,
		},
		{
			Name:          "[universal] no_panic_or_consensus_failure_bor",
			Description:   "No panic, fatal error, or consensus failure in any Bor validator",
			Type:          "log",
			Pattern:       `(panic:|fatal:|CONSENSUS FAILURE)`,
			Absence:       true,
			Critical:      true,
			PostFaultOnly: true,
			ContainerPattern: "bor-heimdall-v2-validator",
		},
		{
			Name:          "[universal] no_panic_or_consensus_failure_heimdall",
			Description:   "No panic, fatal error, or consensus failure in any Heimdall validator",
			Type:          "log",
			Pattern:       `(panic:|fatal:|CONSENSUS FAILURE)`,
			Absence:       true,
			Critical:      true,
			PostFaultOnly: true,
			ContainerPattern: "heimdall-v2-bor-validator",
		},
		{
			Name:             "[universal] no_db_corruption_bor",
			Description:      "No database corruption detected in any Bor validator",
			Type:             "log",
			Pattern:          `(corruption detected|MANIFEST.*error|pebble.*panic|leveldb.*corruption)`,
			Absence:          true,
			Critical:         true,
			PostFaultOnly:    true,
			ContainerPattern: "bor-heimdall-v2-validator",
		},
		{
			Name:             "[universal] no_db_corruption_heimdall",
			Description:      "No database corruption detected in any Heimdall validator",
			Type:             "log",
			Pattern:          `(corruption detected|MANIFEST.*error|pebble.*panic|leveldb.*corruption)`,
			Absence:          true,
			Critical:         true,
			PostFaultOnly:    true,
			ContainerPattern: "heimdall-v2-bor-validator",
		},
		{
			Name:          "[universal] state_root_consensus",
			Description:   "All validators converge on the same state root after chaos — catches silent state divergence",
			Type:          "state_root_consensus",
			Critical:      true,
			PostFaultOnly: true,
			ContainerPattern: "bor-heimdall-v2-validator",
		},
		{
			Name:          "[universal] sidetx_consensus_healthy",
			Description:   "Heimdall side-tx consensus failures remain low (< 20% of approvals)",
			Type:          "prometheus",
			Query:         `(sum(rate(heimdallv2_sidetx_consensus_failures_total[3m])) / clamp_min(sum(rate(heimdallv2_sidetx_consensus_approved_total[3m])), 0.001)) or vector(0)`,
			Threshold:     "< 0.2",
			Critical:      false,
			PostFaultOnly: true,
		},
	}
}

// executeDetect evaluates success criteria
func (o *Orchestrator) executeDetect(ctx context.Context) error {
	fmt.Println("Evaluating success criteria...")

	// Inject universal safety criteria that apply to every scenario.
	// These are appended so they don't interfere with scenario-specific
	// criteria ordering. Duplicates (same name) are skipped.
	existing := make(map[string]bool)
	for _, c := range o.scenario.Spec.SuccessCriteria {
		existing[c.Name] = true
	}
	for _, uc := range universalSafetyCriteria() {
		if !existing[uc.Name] {
			o.scenario.Spec.SuccessCriteria = append(o.scenario.Spec.SuccessCriteria, uc)
		}
	}

	if len(o.scenario.Spec.SuccessCriteria) == 0 {
		fmt.Println("  ⚠ No success criteria defined")
		return nil
	}

	// Check if any criteria need prometheus
	hasPromCriteria := false
	for _, c := range o.scenario.Spec.SuccessCriteria {
		if c.Type == "prometheus" {
			hasPromCriteria = true
			break
		}
	}
	if hasPromCriteria && (o.detector == nil || o.promClient == nil) {
		return fmt.Errorf("Prometheus is not configured but prometheus criteria are defined — cannot validate experiment")
	}

	// Wire up log context for log-based criteria
	if o.detector != nil && o.dockerClient != nil {
		var logTargets []detector.LogTarget
		for _, t := range o.targets {
			logTargets = append(logTargets, detector.LogTarget{
				Alias:       t.Alias,
				ContainerID: t.ContainerID,
				Name:        t.Name,
			})
		}
		since := o.injectTime
		if since.IsZero() {
			since = o.startTime
		}
		o.detector.SetLogContext(o.dockerClient, logTargets, since)
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
		return &CriteriaFailureError{Msg: "one or more critical success criteria failed"}
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

// removeTrackedFaults iterates o.injectedFaults in reverse insertion order
// and calls injector.RemoveFault for each entry. Returns the count of
// successful removals. Errors are logged but not aggregated — a single
// failure must not leak the rest, since each call touches independent
// kernel state (distinct tc qdiscs / iptables chains / stress processes).
//
// Called from:
//   - executeTeardown (normal success path),
//   - Execute's deferred cleanup when the state machine aborted after
//     any faults were recorded — covers partial-inject failures (F-09)
//     and early-exit paths that never reach StateTeardown.
//
// After this call injectedFaults is cleared so the deferred cleanup and
// executeTeardown cannot double-remove. Idempotent by construction: if
// called twice the second call is a no-op. The clear is deferred (F-12)
// so a mid-loop panic also nils the slice on the way out and cannot
// trigger a redundant outer-defer retry over the same entries.
func (o *Orchestrator) removeTrackedFaults(ctx context.Context) int {
	defer func() { o.injectedFaults = nil }()
	removed := 0
	for i := len(o.injectedFaults) - 1; i >= 0; i-- {
		f := o.injectedFaults[i]
		containerID := f.ContainerID
		faultType := f.FaultType
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
			// Continue — one removal failure must not leak the rest.
		} else {
			fmt.Printf("    ✓ Fault removed\n")
			removed++
		}
	}
	return removed
}

// executeTeardown removes all faults
func (o *Orchestrator) executeTeardown(ctx context.Context) error {
	fmt.Println("Tearing down faults...")

	if len(o.injectedFaults) == 0 {
		fmt.Println("  No faults to remove")
	} else {
		removed := o.removeTrackedFaults(ctx)
		fmt.Printf("✓ Removed %d fault(s)\n", removed)
	}

	// Sidecar cleanup (cleanupCoord.CleanupAll) is intentionally NOT
	// called here — Execute's outer deferred cleanup runs CleanupAll on
	// every exit path (success, failure, panic, emergency stop), so
	// firing it from here too produced duplicate audit-log entries on
	// the success path (F-13). The coordinator is idempotent, so this
	// was cosmetic only, but having a single authoritative cleanup call
	// site makes the audit log easier to read.

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

// SetHeimdallAPI sets the Heimdall API endpoint URL for producer discovery.
func (o *Orchestrator) SetHeimdallAPI(url string) {
	o.heimdallAPI = url
}

// resolveCurrentProducer queries the Heimdall API for the current block producer
// and returns the container name that should be excluded from fault injection.
func (o *Orchestrator) resolveCurrentProducer(ctx context.Context) (string, error) {
	if o.heimdallAPI == "" {
		return "", fmt.Errorf("heimdall API endpoint not configured")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	url := strings.TrimRight(o.heimdallAPI, "/") + "/bor/spans/latest"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s returned status %d: %s", url, resp.StatusCode, string(body))
	}

	// Parse JSON: {"span": {"selected_producers": [{"val_id": "2", ...}]}}
	var result struct {
		Span struct {
			SelectedProducers []struct {
				ValID string `json:"val_id"`
			} `json:"selected_producers"`
		} `json:"span"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse JSON: %w", err)
	}

	if len(result.Span.SelectedProducers) == 0 {
		return "", fmt.Errorf("no selected producers in span response")
	}

	valIDStr := result.Span.SelectedProducers[0].ValID
	valID, err := strconv.Atoi(valIDStr)
	if err != nil {
		return "", fmt.Errorf("parse val_id %q: %w", valIDStr, err)
	}

	pattern := fmt.Sprintf("l2-el-%d-bor", valID)

	for _, t := range o.targets {
		if strings.Contains(t.Name, pattern) {
			return t.Name, nil
		}
	}

	return "", fmt.Errorf("no target container matching pattern %q found", pattern)
}

// GetCleanupSummary returns the cleanup summary from the coordinator.
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
	result.FaultVerificationWarnings = o.faultVerificationWarnings
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
	return len(pattern) > 0 && strings.Contains(name, pattern)
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

// getContainerIP extracts the first non-empty network IP from a container
// listing. Returns empty string if no Docker networks are attached.
func getContainerIP(container types.Container) string {
	if container.NetworkSettings == nil {
		return ""
	}
	for _, net := range container.NetworkSettings.Networks {
		if net != nil && net.IPAddress != "" {
			return net.IPAddress
		}
	}
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
