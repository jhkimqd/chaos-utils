package detector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	dockertypes "github.com/docker/docker/api/types"

	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
	"github.com/jihwankim/chaos-utils/pkg/monitoring/prometheus"
	"github.com/jihwankim/chaos-utils/pkg/scenario"
)

// LogTarget holds the container info needed for log-based criteria.
type LogTarget struct {
	Alias       string
	ContainerID string
	Name        string
}

// FailureDetector evaluates success criteria during chaos tests
type FailureDetector struct {
	promClient   *prometheus.Client
	dockerClient *docker.Client
	logTargets   []LogTarget
	logSince     time.Time
	results      map[string]*CriterionResult
	mu           sync.RWMutex
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

// New creates a new failure detector
func New(promClient *prometheus.Client) *FailureDetector {
	return &FailureDetector{
		promClient: promClient,
		results:    make(map[string]*CriterionResult),
	}
}

// SetLogContext configures the detector for log-based criteria evaluation.
func (fd *FailureDetector) SetLogContext(dockerClient *docker.Client, targets []LogTarget, since time.Time) {
	fd.dockerClient = dockerClient
	fd.logTargets = targets
	fd.logSince = since
}

// Evaluate evaluates a single success criterion
func (fd *FailureDetector) Evaluate(ctx context.Context, criterion scenario.SuccessCriterion) (*CriterionResult, error) {
	result := &CriterionResult{
		Criterion:   criterion,
		LastChecked: time.Now(),
	}

	// Initialize if not exists
	fd.mu.Lock()
	if _, exists := fd.results[criterion.Name]; !exists {
		fd.results[criterion.Name] = result
	} else {
		result = fd.results[criterion.Name]
		result.LastChecked = time.Now()
	}
	fd.mu.Unlock()

	result.Evaluations++

	// Evaluate based on criterion type
	switch criterion.Type {
	case "prometheus":
		return fd.evaluatePrometheus(ctx, criterion, result)

	case "health_check":
		return fd.evaluateHealthCheck(ctx, criterion, result)

	case "log":
		return fd.evaluateLog(ctx, criterion, result)

	case "state_root_consensus":
		return fd.evaluateStateRootConsensus(ctx, criterion, result)

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
func (fd *FailureDetector) evaluateHealthCheck(ctx context.Context, criterion scenario.SuccessCriterion, result *CriterionResult) (*CriterionResult, error) {
	// TODO: Implement HTTP health check
	result.Passed = false
	result.Message = "health check not yet implemented"
	return result, fmt.Errorf("health check not yet implemented")
}

// evaluateLog evaluates a log-pattern-based criterion by scanning container logs.
func (fd *FailureDetector) evaluateLog(ctx context.Context, criterion scenario.SuccessCriterion, result *CriterionResult) (*CriterionResult, error) {
	if fd.dockerClient == nil {
		result.Passed = false
		result.Message = "docker client not configured for log criteria"
		result.Failures++
		return result, fmt.Errorf("docker client not configured for log criteria")
	}

	if criterion.Pattern == "" {
		result.Passed = false
		result.Message = "pattern is required for log criteria"
		result.Failures++
		return result, fmt.Errorf("pattern is required for log criteria")
	}

	re, err := regexp.Compile(criterion.Pattern)
	if err != nil {
		result.Passed = false
		result.Message = fmt.Sprintf("invalid pattern: %v", err)
		result.Failures++
		return result, fmt.Errorf("invalid log pattern %q: %w", criterion.Pattern, err)
	}

	// Determine which containers to scan
	var targets []LogTarget

	if criterion.ContainerPattern != "" {
		// Discover containers by name pattern via Docker API
		discovered, err := fd.discoverContainersByPattern(ctx, criterion.ContainerPattern)
		if err != nil {
			fmt.Printf("    [log] warning: container discovery failed: %v\n", err)
		}
		targets = discovered
	} else if criterion.TargetLog != "" {
		for _, t := range fd.logTargets {
			if t.Alias == criterion.TargetLog || t.Name == criterion.TargetLog {
				targets = append(targets, t)
			}
		}
	} else {
		targets = fd.logTargets
	}

	if len(targets) == 0 {
		result.Passed = false
		result.Message = "no targets available for log scanning"
		result.Failures++
		return result, nil
	}

	// Scan logs from each target
	totalMatches := 0
	var matchExamples []string

	for _, target := range targets {
		tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		lines, err := fd.dockerClient.ContainerLogs(tctx, target.ContainerID, 5000, fd.logSince)
		cancel()
		if err != nil {
			fmt.Printf("    [log] warning: failed to fetch logs from %s: %v\n", target.Name, err)
			continue
		}

		for _, line := range lines {
			if re.MatchString(line) {
				totalMatches++
				if len(matchExamples) < 3 {
					// Truncate long lines for display
					display := line
					if len(display) > 200 {
						display = display[:200] + "..."
					}
					matchExamples = append(matchExamples, fmt.Sprintf("[%s] %s", target.Name, display))
				}
			}
		}
	}

	result.LastValue = float64(totalMatches)

	if criterion.Threshold != "" {
		// Count threshold mode: evaluate match count against threshold
		passed, err := fd.evaluateThreshold(float64(totalMatches), criterion.Threshold)
		if err != nil {
			result.Passed = false
			result.Message = fmt.Sprintf("threshold evaluation failed: %v", err)
			result.Failures++
			return result, err
		}
		result.Passed = passed
		if passed {
			result.Message = fmt.Sprintf("pattern %q matched %d times, meets threshold %s", criterion.Pattern, totalMatches, criterion.Threshold)
		} else {
			result.Message = fmt.Sprintf("pattern %q matched %d times, does not meet threshold %s", criterion.Pattern, totalMatches, criterion.Threshold)
			result.Failures++
			for _, ex := range matchExamples {
				fmt.Printf("      match: %s\n", ex)
			}
		}
	} else if criterion.Absence {
		// Pass if pattern NOT found
		result.Passed = totalMatches == 0
		if result.Passed {
			result.Message = fmt.Sprintf("pattern %q not found in logs (expected absence)", criterion.Pattern)
		} else {
			result.Message = fmt.Sprintf("pattern %q found %d times in logs (expected absence)", criterion.Pattern, totalMatches)
			result.Failures++
			for _, ex := range matchExamples {
				fmt.Printf("      match: %s\n", ex)
			}
		}
	} else {
		// Pass if pattern IS found
		result.Passed = totalMatches > 0
		if result.Passed {
			result.Message = fmt.Sprintf("pattern %q found %d times in logs", criterion.Pattern, totalMatches)
			for _, ex := range matchExamples {
				fmt.Printf("      match: %s\n", ex)
			}
		} else {
			result.Message = fmt.Sprintf("pattern %q not found in logs", criterion.Pattern)
			result.Failures++
		}
	}

	return result, nil
}

// discoverContainersByPattern finds running containers whose name contains the pattern.
func (fd *FailureDetector) discoverContainersByPattern(ctx context.Context, pattern string) ([]LogTarget, error) {
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	containers, err := fd.dockerClient.ContainerList(tctx, dockertypes.ContainerListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var targets []LogTarget
	for _, c := range containers {
		for _, name := range c.Names {
			name = strings.TrimPrefix(name, "/")
			if strings.Contains(name, pattern) {
				targets = append(targets, LogTarget{
					ContainerID: c.ID,
					Name:        name,
				})
				break
			}
		}
	}

	fmt.Printf("    [log] discovered %d containers matching %q\n", len(targets), pattern)
	return targets, nil
}

// evaluateStateRootConsensus checks that all Bor nodes matching ContainerPattern
// agree on the stateRoot of their common latest block after chaos recovery.
//
// It queries each node via JSON-RPC on port 8545 (container-network IP).
// Waits up to 2 minutes for all nodes to converge to within 5 blocks of each other,
// then asserts that every node reports the same stateRoot for their common block.
//
// Use this criterion with post_fault_only: true so it runs after faults are removed
// and nodes have had a chance to re-sync.
func (fd *FailureDetector) evaluateStateRootConsensus(ctx context.Context, criterion scenario.SuccessCriterion, result *CriterionResult) (*CriterionResult, error) {
	if fd.dockerClient == nil {
		result.Passed = false
		result.Message = "docker client not configured — cannot perform state root consensus check"
		return result, nil
	}

	pattern := criterion.ContainerPattern
	if pattern == "" {
		pattern = "bor-heimdall-v2-validator"
	}

	// Discover target containers
	targets, err := fd.discoverContainersByPattern(ctx, pattern)
	if err != nil {
		result.Passed = false
		result.Message = fmt.Sprintf("container discovery failed: %v", err)
		result.Failures++
		return result, nil
	}
	if len(targets) < 2 {
		result.Passed = false
		result.Message = fmt.Sprintf("need at least 2 nodes for consensus check, found %d matching %q", len(targets), pattern)
		result.Failures++
		return result, nil
	}

	// Resolve container IPs via Docker inspect
	type nodeInfo struct {
		name   string
		rpcURL string
	}
	var nodes []nodeInfo
	for _, t := range targets {
		svc, err := fd.dockerClient.GetContainerByID(ctx, t.ContainerID)
		if err != nil {
			fmt.Printf("    [state_root] warning: failed to inspect %s: %v\n", t.Name, err)
			continue
		}
		if svc.IP == "" {
			fmt.Printf("    [state_root] warning: no IP for container %s\n", t.Name)
			continue
		}
		if net.ParseIP(svc.IP) == nil {
			fmt.Printf("    [state_root] warning: invalid IP %q for container %s — skipping\n", svc.IP, t.Name)
			continue
		}
		nodes = append(nodes, nodeInfo{
			name:   t.Name,
			rpcURL: fmt.Sprintf("http://%s:8545", svc.IP),
		})
	}
	if len(nodes) < 2 {
		result.Passed = false
		result.Message = fmt.Sprintf("only %d/%d containers had resolvable IPs — cannot check consensus", len(nodes), len(targets))
		result.Failures++
		return result, nil
	}

	fmt.Printf("    [state_root] checking consensus across %d nodes...\n", len(nodes))

	// Poll until all nodes are within 5 blocks of each other (max 2 minutes).
	convergeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	var commonBlock uint64
	for {
		var heights []uint64
		for _, n := range nodes {
			h, err := ethBlockNumber(convergeCtx, n.rpcURL)
			if err != nil {
				fmt.Printf("    [state_root] warning: failed to query block number from %s: %v\n", n.name, err)
				continue
			}
			heights = append(heights, h)
		}

		if len(heights) >= len(nodes) {
			var minH, maxH uint64
			minH = heights[0]
			maxH = heights[0]
			for _, h := range heights[1:] {
				if h < minH {
					minH = h
				}
				if h > maxH {
					maxH = h
				}
			}
			if maxH-minH <= 5 {
				commonBlock = minH
				break
			}
			fmt.Printf("    [state_root] waiting for convergence (min=%d max=%d gap=%d)...\n", minH, maxH, maxH-minH)
		}

		select {
		case <-convergeCtx.Done():
			result.Passed = false
			result.Message = "nodes did not converge to within 5 blocks within 2 minutes after chaos recovery"
			result.Failures++
			return result, nil
		case <-time.After(5 * time.Second):
		}
	}

	fmt.Printf("    [state_root] nodes converged at block %d, checking stateRoot...\n", commonBlock)

	// Query stateRoot at commonBlock from every node
	stateRoots := make(map[string]string) // rpcURL → stateRoot
	for _, n := range nodes {
		sr, err := ethStateRoot(ctx, n.rpcURL, commonBlock)
		if err != nil {
			result.Passed = false
			result.Message = fmt.Sprintf("failed to query stateRoot from %s at block %d: %v", n.name, commonBlock, err)
			result.Failures++
			return result, nil
		}
		stateRoots[n.name] = sr
		fmt.Printf("    [state_root] %s block=%d stateRoot=%s\n", n.name, commonBlock, sr)
	}

	// All stateRoots must be identical
	var reference string
	var divergent []string
	for name, sr := range stateRoots {
		if reference == "" {
			reference = sr
			continue
		}
		if sr != reference {
			divergent = append(divergent, fmt.Sprintf("%s: %s (expected %s)", name, sr, reference))
		}
	}

	if len(divergent) > 0 {
		result.Passed = false
		result.Message = fmt.Sprintf("stateRoot divergence at block %d: %s", commonBlock, strings.Join(divergent, "; "))
		result.Failures++
		return result, nil
	}

	result.Passed = true
	result.Message = fmt.Sprintf("all %d nodes agree on stateRoot %s at block %d", len(nodes), reference, commonBlock)
	return result, nil
}

// ethBlockNumber queries eth_blockNumber on a JSON-RPC endpoint.
func ethBlockNumber(ctx context.Context, rpcURL string) (uint64, error) {
	body := `{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`
	resp, err := jsonRPCCall(ctx, rpcURL, body)
	if err != nil {
		return 0, err
	}
	hexStr, ok := resp["result"].(string)
	if !ok {
		return 0, fmt.Errorf("unexpected result type in eth_blockNumber response")
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(hexStr, "0x"), 16, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse block number %q: %w", hexStr, err)
	}
	return n, nil
}

// ethStateRoot queries eth_getBlockByNumber and returns the stateRoot field.
func ethStateRoot(ctx context.Context, rpcURL string, blockNum uint64) (string, error) {
	blockHex := fmt.Sprintf("0x%x", blockNum)
	body := fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":[%q,false],"id":1}`, blockHex)
	resp, err := jsonRPCCall(ctx, rpcURL, body)
	if err != nil {
		return "", err
	}
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("nil or unexpected result in eth_getBlockByNumber response for block %s", blockHex)
	}
	stateRoot, ok := result["stateRoot"].(string)
	if !ok || stateRoot == "" {
		return "", fmt.Errorf("missing stateRoot in block %s response", blockHex)
	}
	return stateRoot, nil
}

// jsonRPCCall sends a JSON-RPC request and returns the decoded response map.
func jsonRPCCall(ctx context.Context, rpcURL, body string) (map[string]interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewBufferString(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	httpResp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("RPC request to %s failed: %w", rpcURL, err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(httpResp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to decode JSON-RPC response: %w", err)
	}
	if errField, ok := result["error"]; ok {
		return nil, fmt.Errorf("JSON-RPC error: %v", errField)
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

	var valueStr string
	if strings.HasPrefix(threshold, ">=") {
		operator = ">="
		valueStr = strings.TrimSpace(threshold[2:])
	} else if strings.HasPrefix(threshold, "<=") {
		operator = "<="
		valueStr = strings.TrimSpace(threshold[2:])
	} else if strings.HasPrefix(threshold, "!=") {
		operator = "!="
		valueStr = strings.TrimSpace(threshold[2:])
	} else if strings.HasPrefix(threshold, "==") {
		operator = "=="
		valueStr = strings.TrimSpace(threshold[2:])
	} else if strings.HasPrefix(threshold, ">") {
		operator = ">"
		valueStr = strings.TrimSpace(threshold[1:])
	} else if strings.HasPrefix(threshold, "<") {
		operator = "<"
		valueStr = strings.TrimSpace(threshold[1:])
	} else {
		return false, fmt.Errorf("invalid threshold format: %s (expected: >, <, >=, <=, ==, !=)", threshold)
	}

	var err error
	thresholdValue, err = strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return false, fmt.Errorf("invalid threshold value %q in expression %q: %w", valueStr, threshold, err)
	}
	if math.IsNaN(thresholdValue) || math.IsInf(thresholdValue, 0) {
		return false, fmt.Errorf("invalid threshold value %q in expression %q: NaN and Inf are not allowed", valueStr, threshold)
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
	fd.mu.RLock()
	defer fd.mu.RUnlock()
	return fd.results
}

// GetResult returns a specific criterion result
func (fd *FailureDetector) GetResult(name string) *CriterionResult {
	fd.mu.RLock()
	defer fd.mu.RUnlock()
	return fd.results[name]
}

// AllPassed returns true if all criteria passed
func (fd *FailureDetector) AllPassed() bool {
	fd.mu.RLock()
	defer fd.mu.RUnlock()
	for _, result := range fd.results {
		if !result.Passed {
			return false
		}
	}
	return true
}

// CriticalPassed returns true if all critical criteria passed
func (fd *FailureDetector) CriticalPassed() bool {
	fd.mu.RLock()
	defer fd.mu.RUnlock()
	for _, result := range fd.results {
		if result.Criterion.Critical && !result.Passed {
			return false
		}
	}
	return true
}

// GetSummary returns a summary of all evaluations
func (fd *FailureDetector) GetSummary() string {
	fd.mu.RLock()
	defer fd.mu.RUnlock()

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
