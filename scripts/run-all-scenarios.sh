#!/bin/bash

# Chaos Engineering Framework - Scenario Test Runner
# Automates execution of all built-in scenarios and generates reports

set -euo pipefail

# Script configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SCENARIOS_DIR="$PROJECT_ROOT/scenarios/polygon-chain"
CHAOS_RUNNER="$PROJECT_ROOT/bin/chaos-runner"
REPORTS_DIR="$PROJECT_ROOT/reports"
TEST_RUN_DIR=""

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Test configuration
ENCLAVE_NAME="${ENCLAVE_NAME:-pos}"
PROMETHEUS_URL="${PROMETHEUS_URL:-$(kurtosis port print $ENCLAVE_NAME prometheus http 2>/dev/null || echo "http://localhost:9090")}"
export PROMETHEUS_URL
DRY_RUN="${DRY_RUN:-false}"
STOP_ON_FAILURE="${STOP_ON_FAILURE:-false}"
SKIP_VALIDATION="${SKIP_VALIDATION:-false}"

# Test results tracking
declare -A test_results
declare -a test_order

# Usage information
usage() {
    cat <<EOF
Usage: $0 [OPTIONS] [SCENARIO_NAMES...]

Run chaos engineering scenarios for Polygon Chain networks.

OPTIONS:
    -h, --help              Show this help message
    -e, --enclave NAME      Kurtosis enclave name (default: polygon-chain)
    -p, --prometheus URL    Prometheus URL (default: http://localhost:9090)
    -d, --dry-run           Validate scenarios without executing
    -s, --stop-on-failure   Stop execution on first failure
    -f, --skip-validation   Skip validation step
    -l, --list              List available scenarios
    -o, --output DIR        Custom output directory for reports
    -v, --verbose           Enable verbose logging

SCENARIO_NAMES:
    If provided, only run specified scenarios. Otherwise, run all scenarios.
    Example: $0 validator-partition latency-spike-l1-rpc

EXAMPLES:
    # Run all scenarios
    $0

    # Run specific scenarios
    $0 validator-partition split-brain-partition

    # Dry run (validation only)
    $0 --dry-run

    # Run with custom enclave
    $0 --enclave my-polygon-chain

    # Stop on first failure
    $0 --stop-on-failure

ENVIRONMENT VARIABLES:
    ENCLAVE_NAME         Kurtosis enclave name
    PROMETHEUS_URL       Prometheus API URL
    DRY_RUN             Set to 'true' for dry run
    STOP_ON_FAILURE     Set to 'true' to stop on first failure
    SKIP_VALIDATION     Set to 'true' to skip validation

EOF
    exit 0
}

# Logging functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $*"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*"
}

log_section() {
    echo ""
    echo -e "${BLUE}===================================================${NC}"
    echo -e "${BLUE}$*${NC}"
    echo -e "${BLUE}===================================================${NC}"
    echo ""
}

# Check prerequisites
check_prerequisites() {
    log_section "Checking Prerequisites"

    # Check if chaos-runner binary exists
    if [[ ! -f "$CHAOS_RUNNER" ]]; then
        log_error "chaos-runner binary not found at $CHAOS_RUNNER"
        log_info "Build it with: cd $PROJECT_ROOT && make build-runner"
        exit 1
    fi
    log_success "chaos-runner binary found"

    # Check Docker connectivity
    if ! docker ps &>/dev/null; then
        log_error "Docker is not accessible"
        log_info "Ensure Docker daemon is running and you have permissions"
        exit 1
    fi
    log_success "Docker is accessible"

    # Check if scenarios directory exists
    if [[ ! -d "$SCENARIOS_DIR" ]]; then
        log_error "Scenarios directory not found: $SCENARIOS_DIR"
        exit 1
    fi
    log_success "Scenarios directory found"

    # Check Kurtosis enclave (only if not dry run)
    if [[ "$DRY_RUN" != "true" ]]; then
        if ! kurtosis enclave inspect "$ENCLAVE_NAME" &>/dev/null; then
            log_warning "Kurtosis enclave '$ENCLAVE_NAME' not found or not accessible"
            log_info "Deploy it with: kurtosis run . --enclave $ENCLAVE_NAME"
            read -p "Continue anyway? (y/N) " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Yy]$ ]]; then
                exit 1
            fi
        else
            log_success "Kurtosis enclave '$ENCLAVE_NAME' is accessible"
        fi

        # Check Prometheus connectivity
        if ! curl -s "$PROMETHEUS_URL/api/v1/query?query=up" | grep -q "success"; then
            log_warning "Prometheus not accessible at $PROMETHEUS_URL"
            log_info "Some success criteria may fail if Prometheus is unavailable"
        else
            log_success "Prometheus is accessible at $PROMETHEUS_URL"
        fi
    fi

    # Check sidecar image
    if [[ "$DRY_RUN" != "true" ]]; then
        if ! docker image ls | grep -i "jhkimqd/chaos-utils"; then
            log_warning "Sidecar image jhkimqd/chaos-utils not found locally"
            log_info "Pull it with: docker pull jhkimqd/chaos-utils:latest"
            read -p "Try to pull now? (y/N) " -n 1 -r
            echo
            if [[ $REPLY =~ ^[Yy]$ ]]; then
                docker pull jhkimqd/chaos-utils:latest
            fi
        else
            log_success "Sidecar image found"
        fi
    fi
}

# List available scenarios
list_scenarios() {
    log_section "Available Scenarios"

    local scenarios=($(find "$SCENARIOS_DIR" -name "*.yaml" -exec basename {} \; | sort))

    if [[ ${#scenarios[@]} -eq 0 ]]; then
        log_warning "No scenarios found in $SCENARIOS_DIR"
        exit 0
    fi

    printf "%-40s %-10s %-10s\n" "SCENARIO" "SEVERITY" "DURATION"
    printf "%-40s %-10s %-10s\n" "--------" "--------" "--------"

    for scenario_file in "${scenarios[@]}"; do
        local scenario_path="$SCENARIOS_DIR/$scenario_file"
        local name=$(basename "$scenario_file" .yaml)
        local severity=$(grep "severity" "$scenario_path" | head -1 | sed 's/.*\[\(.*\)\].*/\1/' | grep -oE "(high|medium|low|critical)" || echo "N/A")
        local duration=$(grep "duration:" "$scenario_path" | head -1 | awk '{print $2}' || echo "N/A")

        printf "%-40s %-10s %-10s\n" "$name" "$severity" "$duration"
    done

    echo ""
    log_info "Total: ${#scenarios[@]} scenarios"
    exit 0
}

# Validate a scenario
validate_scenario() {
    local scenario_path="$1"
    local scenario_name="$(basename "$scenario_path" .yaml)"

    log_info "Validating $scenario_name..."

    if "$CHAOS_RUNNER" validate "$scenario_path" 2>&1 | tee "$TEST_RUN_DIR/${scenario_name}-validation.log"; then
        log_success "Validation passed for $scenario_name"
        return 0
    else
        log_error "Validation failed for $scenario_name"
        return 1
    fi
}

# Run a scenario
run_scenario() {
    local scenario_path="$1"
    local scenario_name="$(basename "$scenario_path" .yaml)"

    log_section "Running Scenario: $scenario_name"

    # Validate first (unless skipped)
    if [[ "$SKIP_VALIDATION" != "true" ]]; then
        if ! validate_scenario "$scenario_path"; then
            test_results["$scenario_name"]="VALIDATION_FAILED"
            return 1
        fi
    fi

    # Skip execution if dry run
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "Dry run mode - skipping execution"
        test_results["$scenario_name"]="DRY_RUN_OK"
        return 0
    fi

    # Run the scenario
    log_info "Executing $scenario_name..."
    local start_time=$(date +%s)

    if "$CHAOS_RUNNER" run \
        --scenario "$scenario_path" \
        2>&1 | tee "$TEST_RUN_DIR/${scenario_name}-execution.log"; then

        local end_time=$(date +%s)
        local duration=$((end_time - start_time))

        log_success "Scenario $scenario_name completed successfully (${duration}s)"
        test_results["$scenario_name"]="PASSED"

        # Verify cleanup
        log_info "Verifying cleanup for $scenario_name..."
        if verify_cleanup; then
            log_success "Cleanup verification passed"
        else
            log_warning "Cleanup verification found remnants"
            test_results["$scenario_name"]="PASSED_WITH_WARNINGS"
        fi

        return 0
    else
        local end_time=$(date +%s)
        local duration=$((end_time - start_time))

        log_error "Scenario $scenario_name failed (${duration}s)"
        test_results["$scenario_name"]="FAILED"

        # Still verify cleanup after failure
        log_info "Attempting cleanup verification..."
        verify_cleanup || log_warning "Cleanup may be incomplete"

        return 1
    fi
}

# Verify cleanup after scenario execution
verify_cleanup() {
    # Check for running sidecar containers
    local sidecars=$(docker ps --filter "name=chaos-sidecar" --format "{{.ID}}" 2>/dev/null || true)

    if [[ -n "$sidecars" ]]; then
        log_warning "Found running sidecar containers:"
        docker ps --filter "name=chaos-sidecar"
        return 1
    fi

    # Check for emergency stop file
    if [[ -f /tmp/chaos-emergency-stop ]]; then
        log_warning "Emergency stop file still exists"
        rm -f /tmp/chaos-emergency-stop
        return 1
    fi

    return 0
}

# Generate summary report
generate_summary() {
    log_section "Test Execution Summary"

    local total=${#test_order[@]}
    local passed=0
    local failed=0
    local warnings=0
    local validation_failed=0

    printf "%-40s %-20s\n" "SCENARIO" "RESULT"
    printf "%-40s %-20s\n" "--------" "------"

    for scenario in "${test_order[@]}"; do
        local result="${test_results[$scenario]}"
        local color="$NC"

        case "$result" in
            PASSED|DRY_RUN_OK)
                color="$GREEN"
                ((passed++))
                ;;
            PASSED_WITH_WARNINGS)
                color="$YELLOW"
                ((warnings++))
                ;;
            FAILED)
                color="$RED"
                ((failed++))
                ;;
            VALIDATION_FAILED)
                color="$RED"
                ((validation_failed++))
                ;;
        esac

        printf "%-40s ${color}%-20s${NC}\n" "$scenario" "$result"
    done

    echo ""
    echo "================================"
    echo "Total scenarios:        $total"
    echo -e "${GREEN}Passed:                 $passed${NC}"
    [[ $warnings -gt 0 ]] && echo -e "${YELLOW}Passed with warnings:   $warnings${NC}"
    [[ $failed -gt 0 ]] && echo -e "${RED}Failed:                 $failed${NC}"
    [[ $validation_failed -gt 0 ]] && echo -e "${RED}Validation failed:      $validation_failed${NC}"
    echo "================================"
    echo ""

    log_info "Test run directory: $TEST_RUN_DIR"
    log_info "Logs and reports saved to: $TEST_RUN_DIR"

    # Generate JSON summary
    local summary_file="$TEST_RUN_DIR/summary.json"
    cat > "$summary_file" <<EOF
{
  "test_run_id": "$(basename "$TEST_RUN_DIR")",
  "timestamp": "$(date -Iseconds)",
  "enclave": "$ENCLAVE_NAME",
  "total": $total,
  "passed": $passed,
  "failed": $failed,
  "warnings": $warnings,
  "validation_failed": $validation_failed,
  "results": {
EOF

    local first=true
    for scenario in "${test_order[@]}"; do
        [[ "$first" == false ]] && echo "," >> "$summary_file"
        first=false
        echo "    \"$scenario\": \"${test_results[$scenario]}\"" >> "$summary_file"
    done

    cat >> "$summary_file" <<EOF

  }
}
EOF

    log_success "Summary saved to $summary_file"

    # Return exit code based on results
    if [[ $failed -gt 0 || $validation_failed -gt 0 ]]; then
        return 1
    else
        return 0
    fi
}

# Parse command line arguments
parse_args() {
    local scenarios_to_run=()

    while [[ $# -gt 0 ]]; do
        case "$1" in
            -h|--help)
                usage
                ;;
            -e|--enclave)
                ENCLAVE_NAME="$2"
                shift 2
                ;;
            -p|--prometheus)
                PROMETHEUS_URL="$2"
                shift 2
                ;;
            -d|--dry-run)
                DRY_RUN="true"
                shift
                ;;
            -s|--stop-on-failure)
                STOP_ON_FAILURE="true"
                shift
                ;;
            -f|--skip-validation)
                SKIP_VALIDATION="true"
                shift
                ;;
            -l|--list)
                list_scenarios
                ;;
            -o|--output)
                REPORTS_DIR="$2"
                shift 2
                ;;
            -v|--verbose)
                set -x
                shift
                ;;
            -*)
                log_error "Unknown option: $1"
                usage
                ;;
            *)
                scenarios_to_run+=("$1")
                shift
                ;;
        esac
    done

    echo "${scenarios_to_run[@]}"
}

# Main execution
main() {
    log_section "Chaos Engineering Framework - Test Runner"

    # Parse arguments
    local requested_scenarios=($(parse_args "$@"))

    # Create test run directory
    local timestamp=$(date +%Y%m%d-%H%M%S)
    TEST_RUN_DIR="$REPORTS_DIR/test-run-$timestamp"
    mkdir -p "$TEST_RUN_DIR"

    log_info "Test run ID: test-run-$timestamp"
    log_info "Enclave: $ENCLAVE_NAME"
    log_info "Prometheus: $PROMETHEUS_URL"
    [[ "$DRY_RUN" == "true" ]] && log_info "Mode: DRY RUN (validation only)"

    # Check prerequisites
    check_prerequisites

    # Determine which scenarios to run
    local scenarios=()
    if [[ ${#requested_scenarios[@]} -gt 0 ]]; then
        log_info "Running specific scenarios: ${requested_scenarios[*]}"
        for scenario in "${requested_scenarios[@]}"; do
            local scenario_path="$SCENARIOS_DIR/${scenario}.yaml"
            if [[ ! -f "$scenario_path" ]]; then
                log_error "Scenario not found: $scenario"
                exit 1
            fi
            scenarios+=("$scenario_path")
        done
    else
        log_info "Running all scenarios"
        scenarios=($(find "$SCENARIOS_DIR" -name "*.yaml" | sort))
    fi

    if [[ ${#scenarios[@]} -eq 0 ]]; then
        log_error "No scenarios to run"
        exit 1
    fi

    log_info "Total scenarios to run: ${#scenarios[@]}"

    # Run scenarios
    for scenario_path in "${scenarios[@]}"; do
        local scenario_name="$(basename "$scenario_path" .yaml)"
        test_order+=("$scenario_name")

        if ! run_scenario "$scenario_path"; then
            if [[ "$STOP_ON_FAILURE" == "true" ]]; then
                log_error "Stopping execution due to failure"
                break
            fi
        fi

        # Add delay between scenarios
        if [[ "$DRY_RUN" != "true" ]]; then
            log_info "Waiting 10 seconds before next scenario..."
            sleep 10
        fi
    done

    # Generate summary
    generate_summary
}

# Run main function
main "$@"
