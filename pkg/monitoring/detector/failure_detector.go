package detector

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/monitoring/prometheus"
	"github.com/jihwankim/chaos-utils/pkg/scenario"
)

// FailureDetector evaluates success criteria during chaos tests
type FailureDetector struct {
	promClient *prometheus.Client
	rpcCli     *rpcClient
	results    map[string]*CriterionResult
}

// CriterionResult represents the evaluation result of a success criterion
type CriterionResult struct {
	Criterion    scenario.SuccessCriterion
	Passed       bool
	LastValue    float64
	LastChecked  time.Time
	Evaluations  int
	Failures     int
	Message      string
}

// New creates a new failure detector.
func New(promClient *prometheus.Client) *FailureDetector {
	return &FailureDetector{
		promClient: promClient,
		results:    make(map[string]*CriterionResult),
	}
}

// NewWithRPC creates a failure detector that can evaluate both Prometheus and rpc criteria.
// rpcURL is the EVM JSON-RPC endpoint; pass "" to disable rpc criterion evaluation.
func NewWithRPC(promClient *prometheus.Client, rpcURL string) *FailureDetector {
	fd := New(promClient)
	if rpcURL != "" {
		fd.rpcCli = newRPCClient(rpcURL)
	}
	return fd
}

// Evaluate evaluates a single success criterion
func (fd *FailureDetector) Evaluate(ctx context.Context, criterion scenario.SuccessCriterion) (*CriterionResult, error) {
	result := &CriterionResult{
		Criterion:   criterion,
		LastChecked: time.Now(),
	}

	// Initialize if not exists
	if _, exists := fd.results[criterion.Name]; !exists {
		fd.results[criterion.Name] = result
	} else {
		result = fd.results[criterion.Name]
		result.LastChecked = time.Now()
	}

	result.Evaluations++

	// Evaluate based on criterion type
	switch criterion.Type {
	case "prometheus":
		return fd.evaluatePrometheus(ctx, criterion, result)

	case "health_check":
		return fd.evaluateHealthCheck(ctx, criterion, result)

	case "rpc":
		return fd.evaluateRPC(ctx, criterion, result)

	default:
		result.Passed = false
		result.Message = fmt.Sprintf("unsupported criterion type: %s", criterion.Type)
		return result, fmt.Errorf("unsupported criterion type: %s", criterion.Type)
	}
}

// evaluatePrometheus evaluates a Prometheus-based criterion
func (fd *FailureDetector) evaluatePrometheus(ctx context.Context, criterion scenario.SuccessCriterion, result *CriterionResult) (*CriterionResult, error) {
	if criterion.Query == "" {
		result.Passed = false
		result.Message = "query is empty"
		return result, fmt.Errorf("query is empty")
	}

	// Execute query
	queryResults, err := fd.promClient.QueryLatest(ctx, criterion.Query)
	if err != nil {
		result.Passed = false
		result.Message = fmt.Sprintf("query failed: %v", err)
		result.Failures++
		return result, err
	}

	// Check if we got results
	if len(queryResults) == 0 {
		result.Passed = false
		result.LastValue = 0
		result.Message = "query returned no results"
		result.Failures++
		return result, nil
	}

	// Get the first result value
	value := queryResults[0].Value
	result.LastValue = value

	// Parse and evaluate threshold
	passed, err := fd.evaluateThreshold(value, criterion.Threshold)
	if err != nil {
		result.Passed = false
		result.Message = fmt.Sprintf("threshold evaluation failed: %v", err)
		result.Failures++
		return result, err
	}

	result.Passed = passed
	if passed {
		result.Message = fmt.Sprintf("value %.2f meets threshold %s", value, criterion.Threshold)
	} else {
		result.Message = fmt.Sprintf("value %.2f does not meet threshold %s", value, criterion.Threshold)
		result.Failures++
	}

	return result, nil
}

// evaluateHealthCheck evaluates an HTTP health check criterion
func (fd *FailureDetector) evaluateHealthCheck(_ context.Context, _ scenario.SuccessCriterion, result *CriterionResult) (*CriterionResult, error) {
	// TODO: Implement HTTP health check
	result.Passed = false
	result.Message = "health check not yet implemented"
	return result, fmt.Errorf("health check not yet implemented")
}

// evaluateRPC evaluates an rpc-type criterion by calling eth_call and checking the result.
func (fd *FailureDetector) evaluateRPC(ctx context.Context, criterion scenario.SuccessCriterion, result *CriterionResult) (*CriterionResult, error) {
	if fd.rpcCli == nil {
		result.Passed = false
		result.Message = "EVM RPC client not configured but rpc criterion is defined"
		return result, fmt.Errorf("EVM RPC endpoint not configured — cannot evaluate rpc criterion %q", criterion.Name)
	}

	// Only eth_call is supported for now; RPCMethod is validated but defaults to eth_call.
	callData := criterion.RPCCallData
	if callData == "" {
		callData = "0x"
	}

	got, err := fd.rpcCli.EthCall(ctx, criterion.URL, callData)
	if err != nil {
		result.Passed = false
		result.Failures++
		// Return nil error — the RPC endpoint may be under fault injection and
		// temporarily unavailable. This is a non-fatal soft-fail so the experiment
		// continues; only the criterion is marked as failed.
		result.Message = fmt.Sprintf("eth_call unavailable (RPC node may be under fault): %v", err)
		return result, nil
	}

	switch criterion.RPCCheck {
	case "exact":
		result.Passed = (got == criterion.RPCExpected)
		if result.Passed {
			result.Message = fmt.Sprintf("eth_call returned expected value %s", got)
		} else {
			result.Message = fmt.Sprintf("eth_call returned %s; expected %s", got, criterion.RPCExpected)
			result.Failures++
		}

	case "non_empty":
		result.Passed = (got != "" && got != "0x")
		if result.Passed {
			result.Message = fmt.Sprintf("eth_call returned non-empty value %s", got)
		} else {
			result.Message = "eth_call returned empty result — precompile not active or address has no code"
			result.Failures++
		}

	case "empty":
		result.Passed = (got == "" || got == "0x")
		if result.Passed {
			result.Message = fmt.Sprintf("eth_call correctly returned empty for address %s", criterion.URL)
		} else {
			result.Message = fmt.Sprintf("eth_call returned unexpected non-empty value %s", got)
			result.Failures++
		}

	default:
		result.Passed = false
		result.Message = fmt.Sprintf("unknown rpc_check mode: %s", criterion.RPCCheck)
		return result, fmt.Errorf("unknown rpc_check mode: %s", criterion.RPCCheck)
	}

	return result, nil
}

// evaluateThreshold parses and evaluates a threshold expression
// Supports: "> 0", "< 100", ">= 50", "<= 75", "== 0", "!= 0"
func (fd *FailureDetector) evaluateThreshold(value float64, threshold string) (bool, error) {
	threshold = strings.TrimSpace(threshold)

	// Parse the threshold expression
	var operator string
	var thresholdValue float64

	if strings.HasPrefix(threshold, ">=") {
		operator = ">="
		thresholdValue, _ = strconv.ParseFloat(strings.TrimSpace(threshold[2:]), 64)
	} else if strings.HasPrefix(threshold, "<=") {
		operator = "<="
		thresholdValue, _ = strconv.ParseFloat(strings.TrimSpace(threshold[2:]), 64)
	} else if strings.HasPrefix(threshold, "!=") {
		operator = "!="
		thresholdValue, _ = strconv.ParseFloat(strings.TrimSpace(threshold[2:]), 64)
	} else if strings.HasPrefix(threshold, "==") {
		operator = "=="
		thresholdValue, _ = strconv.ParseFloat(strings.TrimSpace(threshold[2:]), 64)
	} else if strings.HasPrefix(threshold, ">") {
		operator = ">"
		thresholdValue, _ = strconv.ParseFloat(strings.TrimSpace(threshold[1:]), 64)
	} else if strings.HasPrefix(threshold, "<") {
		operator = "<"
		thresholdValue, _ = strconv.ParseFloat(strings.TrimSpace(threshold[1:]), 64)
	} else {
		return false, fmt.Errorf("invalid threshold format: %s (expected: >, <, >=, <=, ==, !=)", threshold)
	}

	// Evaluate
	switch operator {
	case ">":
		return value > thresholdValue, nil
	case "<":
		return value < thresholdValue, nil
	case ">=":
		return value >= thresholdValue, nil
	case "<=":
		return value <= thresholdValue, nil
	case "==":
		return value == thresholdValue, nil
	case "!=":
		return value != thresholdValue, nil
	default:
		return false, fmt.Errorf("unknown operator: %s", operator)
	}
}

// EvaluateAll evaluates all success criteria
func (fd *FailureDetector) EvaluateAll(ctx context.Context, criteria []scenario.SuccessCriterion) (map[string]*CriterionResult, error) {
	results := make(map[string]*CriterionResult)

	for _, criterion := range criteria {
		result, err := fd.Evaluate(ctx, criterion)
		if err != nil {
			// Continue evaluating other criteria even if one fails
			fmt.Printf("Warning: criterion %s evaluation error: %v\n", criterion.Name, err)
		}
		results[criterion.Name] = result
	}

	return results, nil
}

// GetResults returns all criterion results
func (fd *FailureDetector) GetResults() map[string]*CriterionResult {
	return fd.results
}

// GetResult returns a specific criterion result
func (fd *FailureDetector) GetResult(name string) *CriterionResult {
	return fd.results[name]
}

// AllPassed returns true if all criteria passed
func (fd *FailureDetector) AllPassed() bool {
	for _, result := range fd.results {
		if !result.Passed {
			return false
		}
	}
	return true
}

// CriticalPassed returns true if all critical criteria passed
func (fd *FailureDetector) CriticalPassed() bool {
	for _, result := range fd.results {
		if result.Criterion.Critical && !result.Passed {
			return false
		}
	}
	return true
}

// GetSummary returns a summary of all evaluations
func (fd *FailureDetector) GetSummary() string {
	var sb strings.Builder

	total := len(fd.results)
	passed := 0
	failed := 0
	critical := 0
	criticalFailed := 0

	for _, result := range fd.results {
		if result.Passed {
			passed++
		} else {
			failed++
		}

		if result.Criterion.Critical {
			critical++
			if !result.Passed {
				criticalFailed++
			}
		}
	}

	sb.WriteString(fmt.Sprintf("Success Criteria Summary:\n"))
	sb.WriteString(fmt.Sprintf("  Total: %d\n", total))
	sb.WriteString(fmt.Sprintf("  Passed: %d\n", passed))
	sb.WriteString(fmt.Sprintf("  Failed: %d\n", failed))
	sb.WriteString(fmt.Sprintf("  Critical: %d (failed: %d)\n", critical, criticalFailed))

	return sb.String()
}

// MonitorContinuous starts continuous monitoring of criteria
func (fd *FailureDetector) MonitorContinuous(ctx context.Context, criteria []scenario.SuccessCriterion, interval time.Duration, callback func(map[string]*CriterionResult)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			results, err := fd.EvaluateAll(ctx, criteria)
			if err != nil {
				fmt.Printf("Error in continuous monitoring: %v\n", err)
			}
			if callback != nil {
				callback(results)
			}
		}
	}
}
