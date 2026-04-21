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
	Criterion   scenario.SuccessCriterion
	Passed      bool
	LastValue   float64
	LastChecked time.Time
	Evaluations int
	Failures    int
	Message     string
	// SeriesCount is the number of Prometheus series returned by the last
	// evaluation. >1 means the query did not aggregate and the detector
	// reduced them deterministically (see evaluatePrometheus).
	SeriesCount int
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

// EvaluateOnce performs a single evaluation of a criterion WITHOUT mutating
// the detector's persistent results map. Callers (e.g. the during-fault
// sampler in the orchestrator) use this to poll repeatedly and aggregate
// worst-observed results themselves, rather than having each evaluation
// clobber the prior one in fd.results.
//
// The returned CriterionResult is a fresh object — Evaluations/Failures
// counters are always 0 or 1 and must not be merged with persistent ones.
func (fd *FailureDetector) EvaluateOnce(ctx context.Context, criterion scenario.SuccessCriterion) (*CriterionResult, error) {
	result := &CriterionResult{
		Criterion:   criterion,
		LastChecked: time.Now(),
		Evaluations: 1,
	}

	switch criterion.Type {
	case "prometheus":
		return fd.evaluatePrometheus(ctx, criterion, result)
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
		result.SeriesCount = 0
		result.Message = "query returned no results"
		result.Failures++
		return result, nil
	}

	result.SeriesCount = len(queryResults)

	// For directional thresholds (<, <=, >, >=), aggregate to the worst-case
	// value so a single offending series trips the check. For equality
	// thresholds (==, !=) aggregation is DANGEROUS: e.g. `== 0` with series
	// [0, 100] would reduce to min=0 and silently pass even though one node
	// violates. Instead, evaluate every series and require ALL to pass —
	// any series that fails the threshold fails the whole criterion.
	t := strings.TrimSpace(criterion.Threshold)
	isEquality := strings.HasPrefix(t, "==") || strings.HasPrefix(t, "!=")

	if isEquality && len(queryResults) > 1 {
		// Find the first series that fails the threshold (if any).
		var failingValue float64
		var failingLabels map[string]string
		allPassed := true
		for _, qr := range queryResults {
			ok, err := fd.evaluateThreshold(qr.Value, criterion.Threshold)
			if err != nil {
				result.Passed = false
				result.Message = fmt.Sprintf("threshold evaluation failed: %v", err)
				result.Failures++
				return result, err
			}
			if !ok {
				allPassed = false
				failingValue = qr.Value
				failingLabels = qr.Labels
				break
			}
		}

		// LastValue reports the offending series so callers see the worst
		// observed — otherwise a passing aggregate hides the failing node.
		if !allPassed {
			result.LastValue = failingValue
			result.Passed = false
			result.Message = fmt.Sprintf(
				"at least one of %d series fails threshold %s — offending value %.4f (labels=%v); per-series eval required for equality thresholds to prevent silent pass",
				len(queryResults), criterion.Threshold, failingValue, failingLabels)
			result.Failures++
			return result, nil
		}
		// All series passed. Record the first value for reporting.
		result.LastValue = queryResults[0].Value
		result.Passed = true
		result.Message = fmt.Sprintf("all %d series meet threshold %s (first value %.4f)", len(queryResults), criterion.Threshold, queryResults[0].Value)
		return result, nil
	}

	// Directional threshold: aggregate worst-case so a single offending
	// series trips the check. Scenario authors should still wrap queries in
	// min()/max()/avg() — this is a safety net, not a workaround.
	value := aggregateSeries(queryResults, criterion.Threshold)
	result.LastValue = value

	if len(queryResults) > 1 {
		fmt.Printf("    [detector] warning: criterion %q returned %d series; aggregated to %.4f for threshold %q — consider wrapping the query in min()/max()/avg()\n",
			criterion.Name, len(queryResults), value, criterion.Threshold)
	}

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
		if len(queryResults) > 1 {
			result.Message = fmt.Sprintf("aggregated value %.2f (across %d series) meets threshold %s", value, len(queryResults), criterion.Threshold)
		} else {
			result.Message = fmt.Sprintf("value %.2f meets threshold %s", value, criterion.Threshold)
		}
	} else {
		if len(queryResults) > 1 {
			result.Message = fmt.Sprintf("aggregated value %.2f (across %d series) does not meet threshold %s", value, len(queryResults), criterion.Threshold)
		} else {
			result.Message = fmt.Sprintf("value %.2f does not meet threshold %s", value, criterion.Threshold)
		}
		result.Failures++
	}

	return result, nil
}

// aggregateSeries reduces a multi-sample Prometheus result to a single value
// using worst-case semantics for the threshold direction. For `<`/`<=` it
// returns the max; for `>`/`>=` it returns the min; for `==`/`!=` (or any
// unrecognized threshold) it returns the numerically smallest value to keep
// runs reproducible.
func aggregateSeries(samples []prometheus.QueryResult, threshold string) float64 {
	t := strings.TrimSpace(threshold)
	useMax := strings.HasPrefix(t, "<=") || strings.HasPrefix(t, "<")
	useMin := strings.HasPrefix(t, ">=") || strings.HasPrefix(t, ">")

	value := samples[0].Value
	for _, s := range samples[1:] {
		switch {
		case useMax:
			if s.Value > value {
				value = s.Value
			}
		case useMin:
			if s.Value < value {
				value = s.Value
			}
		default:
			if s.Value < value {
				value = s.Value
			}
		}
	}
	return value
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

// discoverContainersByPattern finds running containers whose name matches the pattern.
// Supports both literal substring matching and regex patterns (detected by the
// presence of regex metacharacters like [, ], ., *, ^, $, |, (, )).
func (fd *FailureDetector) discoverContainersByPattern(ctx context.Context, pattern string) ([]LogTarget, error) {
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// All: true so scenarios that kill or crash-loop their target (e.g.
	// container_kill, db-corruption) can still have post-fault logs scanned
	// from the exited container. Without this, discovery returns 0 targets
	// and log criteria fail silently with a misleading "0 matches".
	containers, err := fd.dockerClient.ContainerList(tctx, dockertypes.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	// Use regex matching if the pattern contains regex metacharacters,
	// otherwise fall back to literal substring matching for backwards
	// compatibility with existing scenario files.
	useRegex := strings.ContainsAny(pattern, "[].*+?^$|()\\")
	var re *regexp.Regexp
	if useRegex {
		var err error
		re, err = regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid container pattern regex %q: %w", pattern, err)
		}
	}

	var targets []LogTarget
	for _, c := range containers {
		for _, name := range c.Names {
			name = strings.TrimPrefix(name, "/")
			matched := false
			if useRegex {
				matched = re.MatchString(name)
			} else {
				matched = strings.Contains(name, pattern)
			}
			if matched {
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

	// Resolve container IPs via Docker inspect.
	// We keep the containerID so we can exec into the container as a fallback
	// when the external HTTP RPC is unreachable (stale iptables, overload, etc.).
	type nodeInfo struct {
		name        string
		containerID string
		rpcURL      string
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
			name:        t.Name,
			containerID: t.ContainerID,
			rpcURL:      fmt.Sprintf("http://%s:8545", svc.IP),
		})
	}
	if len(nodes) < 2 {
		result.Passed = false
		result.Message = fmt.Sprintf("only %d/%d containers had resolvable IPs — cannot check consensus", len(nodes), len(targets))
		result.Failures++
		return result, nil
	}

	// queryBlockNumber tries the external HTTP RPC first; if that fails, it
	// falls back to querying via IPC socket by exec-ing into the container.
	// IPC bypasses iptables (no network stack) and the HTTP server entirely,
	// so it works even with stale PREROUTING redirects or HTTP overload.
	queryBlockNumber := func(ctx context.Context, n nodeInfo) (uint64, error) {
		h, err := ethBlockNumber(ctx, n.rpcURL)
		if err == nil {
			return h, nil
		}
		fmt.Printf("    [state_root] HTTP RPC failed for %s, falling back to IPC...\n", n.name)
		return fd.execIPCBlockNumber(ctx, n.containerID)
	}

	// queryStateRoot is the same fallback pattern for state root queries.
	queryStateRoot := func(ctx context.Context, n nodeInfo, blockNum uint64) (string, error) {
		sr, err := ethStateRoot(ctx, n.rpcURL, blockNum)
		if err == nil {
			return sr, nil
		}
		return fd.execIPCStateRoot(ctx, n.containerID, blockNum)
	}

	fmt.Printf("    [state_root] checking consensus across %d nodes...\n", len(nodes))

	// Poll until reachable nodes are within 5 blocks of each other (max 2 minutes).
	// Track which nodes are unreachable so we can report them explicitly
	// instead of masking the real problem behind a generic "didn't converge" message.
	convergeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	var commonBlock uint64
	var reachableNodes []nodeInfo
	var unreachableNames []string
	for {
		type nodeHeight struct {
			node   nodeInfo
			height uint64
		}
		var reachable []nodeHeight
		var unreachable []string
		for _, n := range nodes {
			h, err := queryBlockNumber(convergeCtx, n)
			if err != nil {
				fmt.Printf("    [state_root] warning: failed to query block number from %s: %v\n", n.name, err)
				unreachable = append(unreachable, n.name)
				continue
			}
			reachable = append(reachable, nodeHeight{node: n, height: h})
		}
		unreachableNames = unreachable

		if len(reachable) < 2 {
			// Not enough responsive nodes to check anything.
			select {
			case <-convergeCtx.Done():
				result.Passed = false
				result.Message = fmt.Sprintf(
					"%d/%d nodes unreachable (never responded to RPC during 2m recovery window): %s — cannot evaluate state root consensus",
					len(unreachable), len(nodes), strings.Join(unreachable, ", "))
				result.Failures++
				return result, nil
			case <-time.After(5 * time.Second):
				continue
			}
		}

		var minH, maxH uint64
		minH = reachable[0].height
		maxH = reachable[0].height
		for _, nh := range reachable[1:] {
			if nh.height < minH {
				minH = nh.height
			}
			if nh.height > maxH {
				maxH = nh.height
			}
		}
		if maxH-minH <= 5 {
			commonBlock = minH
			reachableNodes = make([]nodeInfo, 0, len(reachable))
			for _, nh := range reachable {
				reachableNodes = append(reachableNodes, nh.node)
			}
			break
		}
		fmt.Printf("    [state_root] waiting for convergence across %d reachable nodes (min=%d max=%d gap=%d, %d unreachable)...\n",
			len(reachable), minH, maxH, maxH-minH, len(unreachable))

		select {
		case <-convergeCtx.Done():
			result.Passed = false
			msg := fmt.Sprintf("reachable nodes did not converge to within 5 blocks within 2 minutes (min=%d max=%d gap=%d)", minH, maxH, maxH-minH)
			if len(unreachable) > 0 {
				msg += fmt.Sprintf("; additionally %d/%d nodes were unreachable: %s",
					len(unreachable), len(nodes), strings.Join(unreachable, ", "))
			}
			result.Message = msg
			result.Failures++
			return result, nil
		case <-time.After(5 * time.Second):
		}
	}

	if len(unreachableNames) > 0 {
		fmt.Printf("    [state_root] %d/%d nodes unreachable: %s\n", len(unreachableNames), len(nodes), strings.Join(unreachableNames, ", "))
	}
	fmt.Printf("    [state_root] %d reachable nodes converged at block %d, checking stateRoot...\n", len(reachableNodes), commonBlock)

	// Query stateRoot at commonBlock from reachable nodes only
	stateRoots := make(map[string]string)
	for _, n := range reachableNodes {
		sr, err := queryStateRoot(ctx, n, commonBlock)
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
		msg := fmt.Sprintf("stateRoot divergence at block %d: %s", commonBlock, strings.Join(divergent, "; "))
		if len(unreachableNames) > 0 {
			msg += fmt.Sprintf("; %d nodes were unreachable: %s", len(unreachableNames), strings.Join(unreachableNames, ", "))
		}
		result.Message = msg
		result.Failures++
		return result, nil
	}

	// Reachable nodes agree, but flag unreachable nodes as a failure — we can't
	// confirm consensus if we couldn't query all nodes.
	if len(unreachableNames) > 0 {
		result.Passed = false
		result.Message = fmt.Sprintf(
			"%d/%d reachable nodes agree on stateRoot %s at block %d, but %d nodes were unreachable and could not be verified: %s",
			len(reachableNodes), len(nodes), reference, commonBlock,
			len(unreachableNames), strings.Join(unreachableNames, ", "))
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

// borIPCPath is the default IPC socket path used by Bor inside containers.
const borIPCPath = "/var/lib/bor/bor.ipc"

// execIPCBlockNumber queries eth.blockNumber via the Bor IPC socket inside
// the container. IPC bypasses both the HTTP server and the network stack
// entirely (Unix domain socket), so it works even when iptables rules are
// stale or the HTTP RPC is overloaded.
func (fd *FailureDetector) execIPCBlockNumber(ctx context.Context, containerID string) (uint64, error) {
	cmd := []string{"bor", "attach", borIPCPath, "--exec", "eth.blockNumber"}
	out, err := fd.dockerClient.ExecCommand(ctx, containerID, cmd)
	if err != nil {
		return 0, fmt.Errorf("IPC eth.blockNumber failed: %w (output: %s)", err, strings.TrimSpace(out))
	}
	// bor attach --exec prints the result as a plain decimal number, e.g. "6495\n"
	trimmed := strings.TrimSpace(out)
	n, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse IPC block number %q: %w", trimmed, err)
	}
	return n, nil
}

// execIPCStateRoot queries the stateRoot at a given block number via the Bor
// IPC socket. Same bypass as execIPCBlockNumber.
func (fd *FailureDetector) execIPCStateRoot(ctx context.Context, containerID string, blockNum uint64) (string, error) {
	jsExpr := fmt.Sprintf("eth.getBlock(%d).stateRoot", blockNum)
	cmd := []string{"bor", "attach", borIPCPath, "--exec", jsExpr}
	out, err := fd.dockerClient.ExecCommand(ctx, containerID, cmd)
	if err != nil {
		return "", fmt.Errorf("IPC eth.getBlock(%d).stateRoot failed: %w (output: %s)", blockNum, err, strings.TrimSpace(out))
	}
	// bor attach --exec prints the result as a quoted hex string, e.g. "\"0xabcd...\"\n"
	trimmed := strings.Trim(strings.TrimSpace(out), "\"")
	if trimmed == "" || !strings.HasPrefix(trimmed, "0x") {
		return "", fmt.Errorf("unexpected IPC stateRoot response: %q", out)
	}
	return trimmed, nil
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
