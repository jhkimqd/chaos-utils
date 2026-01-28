#!/bin/bash

# Quick Test Script - Fast scenario validation and testing
# For rapid iteration during development

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CHAOS_RUNNER="$PROJECT_ROOT/bin/chaos-runner"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

usage() {
    cat <<EOF
Quick Test - Fast scenario validation and execution

Usage: $0 <scenario-name> [options]

OPTIONS:
    -v, --validate-only    Only validate, don't execute
    -e, --enclave NAME     Kurtosis enclave (default: polygon-chain)
    -d, --duration TIME    Override duration (e.g., 1m, 30s)
    -h, --help             Show this help

EXAMPLES:
    # Validate only
    $0 validator-partition --validate-only

    # Quick test with 1 minute duration
    $0 validator-partition --duration 1m

    # Test with custom enclave
    $0 split-brain-partition --enclave my-enclave

EOF
    exit 0
}

log_info() { echo -e "${BLUE}[INFO]${NC} $*"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }

# Parse arguments
SCENARIO=""
VALIDATE_ONLY=false
ENCLAVE="polygon-chain"
DURATION=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help) usage ;;
        -v|--validate-only) VALIDATE_ONLY=true; shift ;;
        -e|--enclave) ENCLAVE="$2"; shift 2 ;;
        -d|--duration) DURATION="$2"; shift 2 ;;
        -*) log_error "Unknown option: $1"; usage ;;
        *) SCENARIO="$1"; shift ;;
    esac
done

if [[ -z "$SCENARIO" ]]; then
    log_error "Scenario name required"
    usage
fi

SCENARIO_PATH="$PROJECT_ROOT/scenarios/polygon-chain/${SCENARIO}.yaml"

if [[ ! -f "$SCENARIO_PATH" ]]; then
    log_error "Scenario not found: $SCENARIO_PATH"
    exit 1
fi

if [[ ! -f "$CHAOS_RUNNER" ]]; then
    log_error "Build chaos-runner first: make build-runner"
    exit 1
fi

# Validate
log_info "Validating $SCENARIO..."
if ! "$CHAOS_RUNNER" validate "$SCENARIO_PATH"; then
    log_error "Validation failed"
    exit 1
fi
log_success "Validation passed"

if [[ "$VALIDATE_ONLY" == true ]]; then
    log_info "Validate-only mode - stopping here"
    exit 0
fi

# Execute
log_info "Executing $SCENARIO on enclave $ENCLAVE..."

CMD=("$CHAOS_RUNNER" "run" "--scenario" "$SCENARIO_PATH")
[[ -n "$DURATION" ]] && CMD+=("--set" "duration=$DURATION")

if "${CMD[@]}"; then
    log_success "Test completed successfully"

    # Show last test report
    log_info "Generating report..."
    "$CHAOS_RUNNER" list runs | head -5

    exit 0
else
    log_error "Test failed"
    exit 1
fi
