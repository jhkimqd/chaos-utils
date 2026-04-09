#!/usr/bin/env bash
# Chaos suite test runner for CI.
# Extracted from the chaos-suite GitHub Actions workflow to stay under
# GitHub's 21 KB inline-script expression limit.
#
# Required env vars:
#   ENCLAVE_NAME        Kurtosis enclave name
#   GITHUB_WORKSPACE    Repo root (for kurtosis-pos checkout)
#   GITHUB_ENV          GitHub Actions env file
#   GITHUB_STEP_SUMMARY GitHub Actions step summary file
#
# Optional env vars (with defaults):
#   INPUT_SCENARIO_FILTER        Scenario category filter (default: all)
#   INPUT_SAMPLES_PER_CATEGORY   Scenarios to sample per category (default: 2, 0 = all)
#   INPUT_FAST_MODE              Compress timings for faster CI (default: true)

set -euo pipefail

FILTER="${INPUT_SCENARIO_FILTER:-all}"
SAMPLES="${INPUT_SAMPLES_PER_CATEGORY:-2}"
if ! [[ "${SAMPLES}" =~ ^[0-9]+$ ]]; then
  echo "::warning::Invalid samples_per_category '${SAMPLES}', defaulting to 2"
  SAMPLES=2
fi
FAST_MODE="${INPUT_FAST_MODE:-true}"
REPORT_DIR="reports/ci-$(date +%Y%m%d-%H%M%S)"
mkdir -p "${REPORT_DIR}"

# ── Resolve RPC URLs for health checks and recovery ──
# Health gate checks validators 1, 3, 6 — spread across the set so a
# single faulted validator doesn't false-alarm.
# Recovery scan resolves all 8 upfront so it can identify and restart
# exactly the stuck nodes, regardless of which scenario caused the issue.
HEALTH_CHECK_VALIDATORS="1 3 6"
ALL_VALIDATORS="1 2 3 4 5 6 7 8"
declare -A VALIDATOR_RPCS
for v in ${ALL_VALIDATORS}; do
  raw="$(timeout 30 kurtosis port print "${ENCLAVE_NAME}" "l2-el-${v}-bor-heimdall-v2-validator" rpc 2>/dev/null || echo "")"
  raw="${raw#http://}"; raw="${raw#https://}"
  if [[ -n "${raw}" ]]; then
    VALIDATOR_RPCS[${v}]="http://${raw}"
  fi
done

# ═══════════════════════════════════════════════════════════════════════
#  HEALTH CHECK & RECOVERY FUNCTIONS
# ═══════════════════════════════════════════════════════════════════════

# ── Health gate: verify devnet is alive (blocks advancing) ──
# Checks multiple validators; returns 0 if ANY show block advancement.
# On total failure, dumps last 50 log lines from each checked validator.
check_devnet_health() {
  local any_advanced=false
  local failed_validators=""

  # Snapshot block heights
  declare -A blocks_before
  for v in ${HEALTH_CHECK_VALIDATORS}; do
    local url="${VALIDATOR_RPCS[${v}]:-}"
    if [[ -z "${url}" ]]; then
      blocks_before[${v}]="0"
      continue
    fi
    blocks_before[${v}]=$(timeout 5 cast bn --rpc-url "${url}" 2>/dev/null || echo "0")
  done

  sleep 30

  # Re-check and compare
  for v in ${HEALTH_CHECK_VALIDATORS}; do
    local url="${VALIDATOR_RPCS[${v}]:-}"
    local before="${blocks_before[${v}]}"
    if [[ -z "${url}" || "${before}" == "0" ]]; then
      echo "  validator ${v}: ⚠ RPC unreachable"
      failed_validators="${failed_validators} ${v}"
      continue
    fi
    local after
    after=$(timeout 5 cast bn --rpc-url "${url}" 2>/dev/null || echo "0")
    if [[ "${after}" == "0" ]]; then
      echo "  validator ${v}: ⚠ RPC unreachable after wait"
      failed_validators="${failed_validators} ${v}"
      continue
    fi
    local delta=$((after - before))
    echo "  validator ${v}: blocks ${before} → ${after} (Δ${delta} in 30s)"
    if [[ "${delta}" -gt 0 ]]; then
      any_advanced=true
    else
      failed_validators="${failed_validators} ${v}"
    fi
  done

  if [[ "${any_advanced}" == "true" ]]; then
    return 0
  fi

  # Total failure — collect diagnostic state instead of dumping INFO logs
  echo ""
  echo "  ╔══════════════════════════════════════════════════════╗"
  echo "  ║  ALL HEALTH-CHECK VALIDATORS UNRESPONSIVE           ║"
  echo "  ╚══════════════════════════════════════════════════════╝"
  diagnose_network_state "${failed_validators}"
  echo ""
  return 1
}

# ── Recovery: scan all validators and docker-restart unresponsive ones ──
recover_unresponsive_validators() {
  echo "  Scanning all validators for responsiveness..."
  local unresponsive=""

  for v in ${ALL_VALIDATORS}; do
    local url="${VALIDATOR_RPCS[${v}]:-}"
    if [[ -z "${url}" ]]; then
      continue
    fi
    local height
    height=$(timeout 5 cast bn --rpc-url "${url}" 2>/dev/null || echo "0")
    if [[ "${height}" == "0" ]]; then
      echo "  validator ${v}: ✗ unreachable"
      unresponsive="${unresponsive} ${v}"
    else
      echo "  validator ${v}: ✓ responsive (block ${height})"
    fi
  done

  if [[ -z "$(echo "${unresponsive}" | tr -d ' ')" ]]; then
    echo "  All validators reachable — waiting 60s for consensus recovery..."
    sleep 60
    return 0
  fi

  echo "  Restarting unresponsive validators:${unresponsive}"
  for v in ${unresponsive}; do
    for svc in "l2-el-${v}-bor-heimdall-v2-validator" "l2-cl-${v}-heimdall-v2-bor-validator"; do
      ctr=$(docker ps -a -q --filter "name=${ENCLAVE_NAME}--${svc}" 2>/dev/null | head -1)
      [[ -z "${ctr}" ]] && ctr=$(docker ps -a -q --filter "name=${svc}" 2>/dev/null | head -1)
      if [[ -n "${ctr}" ]]; then
        echo "  → restarting ${svc} (${ctr})"
        docker restart "${ctr}" 2>/dev/null \
          || echo "  ⚠ restart failed for ${svc}"
      else
        echo "  ⚠ container not found: ${svc}"
      fi
    done
  done

  echo "  Waiting 90s for restarted validators to rejoin network..."
  sleep 90
}

# ── Escalation: force-restart ALL validators to clear stale consensus state ──
force_restart_all_validators() {
  echo "  Force-restarting ALL validators to clear stale CometBFT state..."
  for v in ${ALL_VALIDATORS}; do
    for svc in "l2-el-${v}-bor-heimdall-v2-validator" "l2-cl-${v}-heimdall-v2-bor-validator"; do
      ctr=$(docker ps -a -q --filter "name=${ENCLAVE_NAME}--${svc}" 2>/dev/null | head -1)
      [[ -z "${ctr}" ]] && ctr=$(docker ps -a -q --filter "name=${svc}" 2>/dev/null | head -1)
      if [[ -n "${ctr}" ]]; then
        echo "  → restarting ${svc} (${ctr})"
        docker restart "${ctr}" 2>/dev/null \
          || echo "  ⚠ restart failed for ${svc}"
      else
        echo "  ⚠ container not found: ${svc}"
      fi
    done
  done

  echo "  Waiting 120s for full cluster cold-start and CometBFT P2P mesh re-establishment..."
  sleep 120
}

# ── Quick liveness probe: instant RPC reachability check (~3s) ──
quick_liveness_probe() {
  local reachable=0
  local total=0
  for v in ${HEALTH_CHECK_VALIDATORS}; do
    local url="${VALIDATOR_RPCS[${v}]:-}"
    [[ -z "${url}" ]] && continue
    total=$((total + 1))
    local height
    height=$(timeout 5 cast bn --rpc-url "${url}" 2>/dev/null || echo "0")
    if [[ "${height}" != "0" ]]; then
      reachable=$((reachable + 1))
    fi
  done
  if [[ ${reachable} -eq 0 ]]; then
    echo "  ⚠ All probed validators unreachable (0/${total})"
    return 1
  fi
  echo "  Liveness probe: ${reachable}/${total} validators reachable"
  return 0
}

# ── Diagnostic panel: rich state dump when validators are unresponsive ──
# Replaces the old "dump last 50 INFO lines" approach with structured
# diagnostics: container status, block heights, peer counts, Heimdall
# health, and only ERROR/WARN/FATAL/PANIC log lines.
diagnose_network_state() {
  local problem_validators="${1:-${ALL_VALIDATORS}}"
  echo ""
  echo "  ┌─────────────────────────────────────────────────────────────┐"
  echo "  │  DIAGNOSTIC REPORT                                         │"
  echo "  └─────────────────────────────────────────────────────────────┘"

  # ── 1. Container status (running/exited/dead?) ──
  echo ""
  echo "  ── Container Status ──"
  for v in ${ALL_VALIDATORS}; do
    for layer in "l2-el-${v}-bor-heimdall-v2-validator" "l2-cl-${v}-heimdall-v2-bor-validator"; do
      local ctr state
      ctr=$(docker ps -a -q --filter "name=${ENCLAVE_NAME}--${layer}" 2>/dev/null | head -1)
      [[ -z "${ctr}" ]] && ctr=$(docker ps -a -q --filter "name=${layer}" 2>/dev/null | head -1)
      if [[ -n "${ctr}" ]]; then
        state=$(docker inspect --format='{{.State.Status}} ({{.State.Health.Status}})' "${ctr}" 2>/dev/null || docker inspect --format='{{.State.Status}}' "${ctr}" 2>/dev/null || echo "unknown")
        local short_layer="${layer#l2-*-}"
        echo "    ${layer}: ${state}"
      else
        echo "    ${layer}: NOT FOUND"
      fi
    done
  done

  # ── 2. Block heights across all validators ──
  echo ""
  echo "  ── Block Heights (Bor RPC) ──"
  local max_block=0
  local min_block=999999999
  for v in ${ALL_VALIDATORS}; do
    local url="${VALIDATOR_RPCS[${v}]:-}"
    local height="unreachable"
    if [[ -n "${url}" ]]; then
      height=$(timeout 5 cast bn --rpc-url "${url}" 2>/dev/null || echo "unreachable")
    fi
    if [[ "${height}" != "unreachable" && "${height}" -gt 0 ]] 2>/dev/null; then
      [[ "${height}" -gt "${max_block}" ]] && max_block="${height}"
      [[ "${height}" -lt "${min_block}" ]] && min_block="${height}"
      echo "    validator ${v}: block ${height}"
    else
      echo "    validator ${v}: ${height}"
    fi
  done
  if [[ "${max_block}" -gt 0 && "${min_block}" -lt 999999999 ]]; then
    local gap=$((max_block - min_block))
    echo "    ── spread: ${min_block}..${max_block} (Δ${gap} blocks)"
  fi

  # ── 3. Heimdall consensus heights ──
  echo ""
  echo "  ── Heimdall Consensus Heights ──"
  for v in ${ALL_VALIDATORS}; do
    local cl_port
    cl_port="$(timeout 10 kurtosis port print "${ENCLAVE_NAME}" "l2-cl-${v}-heimdall-v2-bor-validator" rpc 2>/dev/null || echo "")"
    cl_port="${cl_port#http://}"; cl_port="${cl_port#https://}"
    if [[ -n "${cl_port}" ]]; then
      local hd_status
      hd_status=$(timeout 5 curl -s "http://${cl_port}/status" 2>/dev/null || echo "")
      if [[ -n "${hd_status}" ]]; then
        local hd_block hd_catching_up
        hd_block=$(echo "${hd_status}" | jq -r '.result.sync_info.latest_block_height // "n/a"' 2>/dev/null || echo "n/a")
        hd_catching_up=$(echo "${hd_status}" | jq -r '.result.sync_info.catching_up // "n/a"' 2>/dev/null || echo "n/a")
        local hd_peers
        hd_peers=$(echo "${hd_status}" | jq -r '.result.node_info.other.peers // "n/a"' 2>/dev/null || echo "n/a")
        echo "    validator ${v}: height=${hd_block} catching_up=${hd_catching_up}"
      else
        echo "    validator ${v}: Heimdall RPC unreachable"
      fi
    else
      echo "    validator ${v}: Heimdall port not found"
    fi
  done

  # ── 4. Peer counts (Bor P2P) ──
  echo ""
  echo "  ── Bor Peer Counts ──"
  for v in ${ALL_VALIDATORS}; do
    local url="${VALIDATOR_RPCS[${v}]:-}"
    if [[ -n "${url}" ]]; then
      local peers
      peers=$(timeout 5 curl -s -X POST -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"net_peerCount","params":[],"id":1}' \
        "${url}" 2>/dev/null | jq -r '.result // "n/a"' 2>/dev/null || echo "n/a")
      if [[ "${peers}" != "n/a" && "${peers}" != "null" && -n "${peers}" ]]; then
        local peer_dec=$((16#${peers#0x})) 2>/dev/null || peer_dec="${peers}"
        echo "    validator ${v}: ${peer_dec} peers"
      else
        echo "    validator ${v}: unreachable"
      fi
    else
      echo "    validator ${v}: no RPC URL"
    fi
  done

  # ── 5. Error/warning logs only (not INFO) ──
  echo ""
  echo "  ── Error Logs (last 20 ERROR/WARN/FATAL/PANIC lines per problem validator) ──"
  for v in ${problem_validators}; do
    echo ""
    echo "    ── Bor validator ${v} ──"
    local bor_errors
    bor_errors=$(timeout 30 kurtosis service logs "${ENCLAVE_NAME}" "l2-el-${v}-bor-heimdall-v2-validator" 2>/dev/null \
      | grep -iE '(ERR|error|WARN|warning|FATAL|PANIC|CRIT)' | tail -20 || echo "")
    if [[ -n "${bor_errors}" ]]; then
      echo "${bor_errors}" | sed 's/^/      /'
    else
      echo "      (no error-level logs found)"
    fi

    echo "    ── Heimdall validator ${v} ──"
    local hd_errors
    hd_errors=$(timeout 30 kurtosis service logs "${ENCLAVE_NAME}" "l2-cl-${v}-heimdall-v2-bor-validator" 2>/dev/null \
      | grep -iE '(ERR|error|WARN|warning|FATAL|PANIC|CRIT)' | tail -20 || echo "")
    if [[ -n "${hd_errors}" ]]; then
      echo "${hd_errors}" | sed 's/^/      /'
    else
      echo "      (no error-level logs found)"
    fi
  done

  echo ""
  echo "  ┌─────────────────────────────────────────────────────────────┐"
  echo "  │  END DIAGNOSTIC REPORT                                     │"
  echo "  └─────────────────────────────────────────────────────────────┘"
}

# ── Full devnet redeploy: kurtosis clean + fresh deploy ──
redeploy_devnet() {
  echo "  Tearing down enclave..."
  timeout 30 kurtosis enclave stop "${ENCLAVE_NAME}" 2>/dev/null || true
  timeout 30 kurtosis enclave rm "${ENCLAVE_NAME}" 2>/dev/null || true

  echo "  Redeploying devnet from scratch..."
  pushd "${GITHUB_WORKSPACE}/kurtosis-pos" > /dev/null
  if ! timeout 900 kurtosis run --enclave="${ENCLAVE_NAME}" --args-file /tmp/chaos-params.yml . 2>&1 | tee ../redeploy.log; then
    echo "  ✗ Redeploy failed"
    popd > /dev/null
    return 1
  fi
  popd > /dev/null

  echo "  Waiting for network readiness (40 blocks)..."
  local rpc_raw
  rpc_raw="$(timeout 30 kurtosis port print "${ENCLAVE_NAME}" l2-el-1-bor-heimdall-v2-validator rpc 2>/dev/null || echo "")"
  rpc_raw="${rpc_raw#http://}"; rpc_raw="${rpc_raw#https://}"
  if [[ -z "${rpc_raw}" ]]; then
    echo "  ✗ Cannot resolve validator-1 RPC after redeploy"
    return 1
  fi
  local rpc_url="http://${rpc_raw}"

  for i in $(seq 1 120); do
    local block
    block=$(timeout 5 cast bn --rpc-url "${rpc_url}" 2>/dev/null || echo "0")
    echo "  Block: ${block}/40 (attempt ${i}/120)"
    if [[ "${block}" -ge 40 ]]; then
      echo "  ✓ Network ready"
      break
    fi
    if [[ "${i}" -eq 120 ]]; then
      echo "  ✗ Network did not reach 40 blocks after redeploy"
      return 1
    fi
    sleep 5
  done

  # Re-resolve all validator RPC URLs (ports change after redeploy)
  echo "  Re-resolving validator RPC endpoints..."
  for v in ${ALL_VALIDATORS}; do
    raw="$(timeout 30 kurtosis port print "${ENCLAVE_NAME}" "l2-el-${v}-bor-heimdall-v2-validator" rpc 2>/dev/null || echo "")"
    raw="${raw#http://}"; raw="${raw#https://}"
    if [[ -n "${raw}" ]]; then
      VALIDATOR_RPCS[${v}]="http://${raw}"
    fi
  done

  # Verify multi-validator participation (not just validator-1 producing blocks)
  echo "  Verifying multi-validator consensus..."
  if ! check_devnet_health; then
    echo "  ✗ Devnet deployed but validators not in consensus"
    return 1
  fi

  return 0
}

# ═══════════════════════════════════════════════════════════════════════
#  SCENARIO SELECTION
# ═══════════════════════════════════════════════════════════════════════

SCENARIO_DIRS=""
case "${FILTER}" in
  all)  SCENARIO_DIRS="scenarios/polygon-chain/applications scenarios/polygon-chain/boundary scenarios/polygon-chain/compound scenarios/polygon-chain/disk scenarios/polygon-chain/network scenarios/polygon-chain/semantic" ;;
  *)    SCENARIO_DIRS="scenarios/polygon-chain/${FILTER}" ;;
esac

# Stratified sampling: pick SAMPLES scenarios from each subdirectory.
# When SAMPLES=0, run all scenarios in each directory.
SELECTED=""
TOTAL_AVAILABLE=0
for dir in ${SCENARIO_DIRS}; do
  [[ -d "${dir}" ]] || continue
  category=$(basename "${dir}")
  DIR_SCENARIOS=""
  dir_count=0
  for f in "${dir}"/*.yaml; do
    [[ -f "${f}" ]] || continue
    DIR_SCENARIOS="${DIR_SCENARIOS} ${f}"
    dir_count=$((dir_count + 1))
  done
  TOTAL_AVAILABLE=$((TOTAL_AVAILABLE + dir_count))

  if [[ "${SAMPLES}" -eq 0 || ${dir_count} -le ${SAMPLES} ]]; then
    SAMPLED="${DIR_SCENARIOS}"
    picked=${dir_count}
  else
    SAMPLED=$(echo "${DIR_SCENARIOS}" | tr ' ' '\n' | grep -v '^$' | shuf | head -n "${SAMPLES}")
    picked="${SAMPLES}"
  fi
  SELECTED="${SELECTED} ${SAMPLED}"
  echo "[${category}] ${picked}/${dir_count} scenarios selected"
  echo "${SAMPLED}" | tr ' ' '\n' | grep -v '^$' | while read -r f; do echo "  - $(basename "$f" .yaml)"; done
done

SCENARIO_COUNT=$(echo "${SELECTED}" | wc -w)
echo ""
echo "Total: ${SCENARIO_COUNT} scenarios selected (from ${TOTAL_AVAILABLE} available)"

# ── Scenarios with hard timing floors that cannot be compressed ──
SLOW_SCENARIOS="validator-freeze-zombie shifting-fault-combinations rolling-restart oom-flapping-loop"

# ── Scenarios that need their full YAML cooldown for post-fault recovery ──
LONG_COOLDOWN_SCENARIOS="simultaneous-validator-restart three-validator-full-isolation coordinated-full-cluster-crash span-boundary-partition kill-resync-under-peer-loss"

# ═══════════════════════════════════════════════════════════════════════
#  MAIN TEST LOOP
# ═══════════════════════════════════════════════════════════════════════

PASSED=0
FAILED=0
ERRORS=0
SKIPPED=0
TEST_FAILURES=""
DEVNET_HALTED=false
HALT_SUSPECT=""
LAST_ERROR_SCENARIO=""
CONSECUTIVE_ERRORS=0
MAX_CONSECUTIVE_ERRORS=2
SCENARIO_TIMEOUT=900  # 15 minutes max per scenario run

for scenario in ${SELECTED}; do
  name=$(basename "${scenario}" .yaml)
  echo "::group::${name}"
  echo "────────────────────────────────────"

  # ── Pre-flight health gate ──
  echo "Pre-flight health check..."
  if ! check_devnet_health; then
    echo "Health check failed — scanning for unresponsive validators..."
    recover_unresponsive_validators
    if ! check_devnet_health; then
      echo "Consensus still stalled — force-restarting all validators..."
      force_restart_all_validators
      if ! check_devnet_health; then
        echo "::error::Devnet is unrecoverable — halting test suite"
        echo "SKIPPED ${name} (devnet halted)" >> "${REPORT_DIR}/results.txt"
        SKIPPED=$((SKIPPED + 1))
        DEVNET_HALTED=true
        if [[ -n "${LAST_ERROR_SCENARIO}" ]]; then
          HALT_SUSPECT="${LAST_ERROR_SCENARIO} (broke devnet; detected at pre-flight of ${name})"
        else
          HALT_SUSPECT="${name} (pre-flight: devnet was already broken)"
        fi
        echo "::endgroup::"
        break
      fi
    fi
    echo "Devnet recovered after restart"
  fi
  echo "Health check passed ✓"

  echo "Running: ${name}"

  # ── Build --set overrides for fast mode ──
  SET_FLAGS=""
  if [[ "${FAST_MODE}" == "true" ]]; then
    IS_SLOW=false
    for s in ${SLOW_SCENARIOS}; do
      [[ "${name}" == "${s}" ]] && IS_SLOW=true
    done

    IS_LONG_COOLDOWN=false
    for s in ${LONG_COOLDOWN_SCENARIOS}; do
      [[ "${name}" == "${s}" ]] && IS_LONG_COOLDOWN=true
    done

    if [[ "${IS_SLOW}" == "true" ]]; then
      SET_FLAGS="--set warmup=15s --set cooldown=90s"
    elif [[ "${IS_LONG_COOLDOWN}" == "true" ]]; then
      SET_FLAGS="--set warmup=15s --set duration=2m"
    else
      SET_FLAGS="--set warmup=15s --set duration=2m --set cooldown=90s"
    fi
  fi

  EXIT_CODE=0
  timeout --kill-after=30 ${SCENARIO_TIMEOUT} ./bin/chaos-runner run \
    --scenario "${scenario}" \
    ${SET_FLAGS} \
    --format text 2>&1 | tee "${REPORT_DIR}/${name}.log" || EXIT_CODE=$?

  # ── Pre-fault steady-state failure: recover and retry ──
  if [[ ${EXIT_CODE} -ne 0 ]] && grep -q "pre-fault health check failed:.*steady state" "${REPORT_DIR}/${name}.log" 2>/dev/null; then
    echo "::warning::${name}: pre-fault health check failed — system not in steady state"
    echo "Recovering network before retry..."
    force_restart_all_validators
    if ! check_devnet_health; then
      echo "::error::Recovery failed after pre-fault check — devnet unrecoverable"
      echo "ERROR  ${name} (steady-state check failed, recovery failed)" >> "${REPORT_DIR}/results.txt"
      ERRORS=$((ERRORS + 1))
      DEVNET_HALTED=true
      if [[ -n "${LAST_ERROR_SCENARIO}" ]]; then
        HALT_SUSPECT="${LAST_ERROR_SCENARIO} (degraded devnet; pre-fault check failed in ${name})"
      else
        HALT_SUSPECT="${name}"
      fi
      echo "::endgroup::"
      break
    fi
    echo "Network recovered — retrying ${name}..."
    EXIT_CODE=0
    timeout --kill-after=30 ${SCENARIO_TIMEOUT} ./bin/chaos-runner run \
      --scenario "${scenario}" \
      ${SET_FLAGS} \
      --format text 2>&1 | tee -a "${REPORT_DIR}/${name}.log" || EXIT_CODE=$?
  fi

  # ── Network-death detection: full redeploy + verify ──
  # After infra errors (exit >= 2 or timeout), use the full 30s block-advancement
  # check — quick_liveness_probe only tests RPC reachability, which misses degraded
  # states where validators are reachable but consensus is stalled.
  if [[ ${EXIT_CODE} -ne 0 ]]; then
    NETWORK_OK=true
    if [[ ${EXIT_CODE} -ge 2 ]]; then
      echo "Post-scenario health check (full — infra error detected)..."
      if ! check_devnet_health; then
        NETWORK_OK=false
      fi
    else
      echo "Post-scenario liveness probe..."
      if ! quick_liveness_probe; then
        NETWORK_OK=false
      fi
    fi
    if [[ "${NETWORK_OK}" == "false" ]]; then
      echo ""
      echo "  ╔══════════════════════════════════════════════════════════════════╗"
      echo "  ║  SUSPECT: ${name} may have broken the devnet                    ║"
      echo "  ╚══════════════════════════════════════════════════════════════════╝"
      echo ""
      echo "Redeploying fresh devnet to verify..."
      if ! redeploy_devnet; then
        echo "::error::Fresh redeploy failed — cannot verify suspect scenario"
        echo "ERROR  ${name} (devnet broken, redeploy failed)" >> "${REPORT_DIR}/results.txt"
        ERRORS=$((ERRORS + 1))
        DEVNET_HALTED=true
        HALT_SUSPECT="${name}"
        echo "::endgroup::"
        break
      fi

      echo "Re-running ${name} on fresh devnet to confirm..."
      EXIT_CODE=0
      timeout --kill-after=30 ${SCENARIO_TIMEOUT} ./bin/chaos-runner run \
        --scenario "${scenario}" \
        ${SET_FLAGS} \
        --format text 2>&1 | tee "${REPORT_DIR}/${name}-verify.log" || EXIT_CODE=$?

      echo "Verification liveness probe..."
      if ! quick_liveness_probe; then
        echo ""
        echo "  ╔══════════════════════════════════════════════════════════════════╗"
        echo "  ║  CONFIRMED: ${name} breaks the devnet reproducibly              ║"
        echo "  ╚══════════════════════════════════════════════════════════════════╝"
        echo ""
        echo "::error::CONFIRMED BUG: ${name} breaks the devnet on a fresh enclave"
        echo "CONFIRMED_BUG ${name}" >> "${REPORT_DIR}/results.txt"
        FAILED=$((FAILED + 1))
        TEST_FAILURES="${TEST_FAILURES}  - ${name} (**CONFIRMED: breaks devnet reproducibly**)\n"
        DEVNET_HALTED=true
        HALT_SUSPECT="${name}"
        echo "::endgroup::"
        break
      fi

      if [[ ${EXIT_CODE} -eq 0 ]]; then
        echo "✓ ${name} passed on fresh devnet — previous failure was a false positive"
      else
        echo "⚠ ${name} also failed on fresh devnet (exit ${EXIT_CODE}) — not a network-death issue"
      fi
      echo "Continuing tests on fresh devnet..."
      CONSECUTIVE_ERRORS=0
      LAST_ERROR_SCENARIO=""
    fi
  fi

  # ── Classify result ──
  if [[ ${EXIT_CODE} -eq 124 || ${EXIT_CODE} -eq 137 ]]; then
    echo "::error::TIMEOUT: ${name} — exceeded ${SCENARIO_TIMEOUT}s (network may be hung)"
    echo "ERROR  ${name} (timeout after ${SCENARIO_TIMEOUT}s)" >> "${REPORT_DIR}/results.txt"
    ERRORS=$((ERRORS + 1))
    CONSECUTIVE_ERRORS=$((CONSECUTIVE_ERRORS + 1))
    LAST_ERROR_SCENARIO="${name}"
  elif [[ ${EXIT_CODE} -eq 0 ]]; then
    echo "✅ PASSED: ${name}"
    echo "PASSED ${name}" >> "${REPORT_DIR}/results.txt"
    PASSED=$((PASSED + 1))
    CONSECUTIVE_ERRORS=0
    LAST_ERROR_SCENARIO=""
  elif [[ ${EXIT_CODE} -eq 1 ]]; then
    echo "::error::FAILED: ${name} — one or more critical success criteria did not pass"
    echo "FAILED ${name}" >> "${REPORT_DIR}/results.txt"
    FAILED=$((FAILED + 1))
    TEST_FAILURES="${TEST_FAILURES}  - ${name}\n"
  else
    echo "::error::ERROR: ${name} (exit: ${EXIT_CODE})"
    echo "ERROR  ${name}" >> "${REPORT_DIR}/results.txt"
    ERRORS=$((ERRORS + 1))
    CONSECUTIVE_ERRORS=$((CONSECUTIVE_ERRORS + 1))
    LAST_ERROR_SCENARIO="${name}"
  fi

  # ── Consecutive-error circuit breaker ──
  if [[ ${CONSECUTIVE_ERRORS} -ge ${MAX_CONSECUTIVE_ERRORS} ]]; then
    echo "::warning::${CONSECUTIVE_ERRORS} consecutive infra errors — attempting full validator restart"
    force_restart_all_validators
    if ! check_devnet_health; then
      echo "::error::Devnet unrecoverable after ${CONSECUTIVE_ERRORS} consecutive errors — halting suite"
      DEVNET_HALTED=true
      HALT_SUSPECT="${name}"
      echo "::endgroup::"
      break
    fi
    echo "Network recovered — resetting error counter"
    CONSECUTIVE_ERRORS=0
  fi

  echo "::endgroup::"
done

# ═══════════════════════════════════════════════════════════════════════
#  RESULTS & SUMMARY
# ═══════════════════════════════════════════════════════════════════════

# Mark remaining scenarios as skipped if halted
if [[ "${DEVNET_HALTED}" == "true" ]]; then
  REMAINING=$(echo "${SELECTED}" | tr ' ' '\n' | grep -v '^$' | tail -n +$((PASSED + FAILED + ERRORS + SKIPPED + 1)))
  for scenario in ${REMAINING}; do
    name=$(basename "${scenario}" .yaml)
    echo "SKIPPED ${name} (devnet halted)" >> "${REPORT_DIR}/results.txt"
    SKIPPED=$((SKIPPED + 1))
  done
fi

TOTAL=$((PASSED + FAILED + ERRORS + SKIPPED))
echo ""
echo "═══════════════════════════════════════"
echo "  RESULTS: ${PASSED}/${TOTAL} passed, ${FAILED} failed, ${ERRORS} errors, ${SKIPPED} skipped"
echo "═══════════════════════════════════════"

if [[ -n "${TEST_FAILURES}" ]]; then
  echo ""
  echo "Failed scenarios:"
  echo -e "${TEST_FAILURES}"
fi

if [[ "${DEVNET_HALTED}" == "true" ]]; then
  echo ""
  echo "╔══════════════════════════════════════════════════════════════════╗"
  echo "║  SUITE HALTED: DEVNET UNRECOVERABLE                           ║"
  echo "╚══════════════════════════════════════════════════════════════════╝"
  if [[ -n "${HALT_SUSPECT}" ]]; then
    echo ""
    echo "  Suspect scenario: ${HALT_SUSPECT}"
  fi
  echo ""
  echo "  Collecting final network state for diagnosis..."
  diagnose_network_state "${ALL_VALIDATORS}"
fi

echo "REPORT_DIR=${REPORT_DIR}" >> "${GITHUB_ENV}"

# Write job summary
{
  echo "### Chaos Suite Results"
  echo "| Status | Count |"
  echo "|--------|-------|"
  echo "| ✅ Passed | ${PASSED} |"
  echo "| ❌ Failed (critical criteria) | ${FAILED} |"
  echo "| ⚠️ Errors (infra) | ${ERRORS} |"
  echo "| ⏭️ Skipped | ${SKIPPED} |"
  if [[ -n "${TEST_FAILURES}" ]]; then
    echo ""
    echo "**Failed scenarios:**"
    echo -e "${TEST_FAILURES}" | sed 's/^/- /'
  fi
  if [[ "${DEVNET_HALTED}" == "true" ]]; then
    echo ""
    echo "> **Suite halted: devnet became unrecoverable**"
    if [[ -n "${HALT_SUSPECT}" ]]; then
      echo ">"
      echo "> Suspect scenario: \`${HALT_SUSPECT}\`"
    fi
    echo ">"
    echo "> See the **Diagnostic Report** in the job logs and the enclave dump artifact for root cause analysis."
  fi
} >> "$GITHUB_STEP_SUMMARY"

# Fail the job on test failures, infra errors, or unrecoverable devnet
if [[ "${FAILED}" -gt 0 ]]; then
  echo "::error::${FAILED} scenario(s) failed critical success criteria — these are real test failures, not infra issues"
  exit 1
fi
if [[ "${ERRORS}" -gt 0 ]]; then
  echo "::error::${ERRORS} scenario(s) hit infrastructure errors"
  exit 1
fi
if [[ "${DEVNET_HALTED}" == "true" ]]; then
  echo "::error::Devnet became unrecoverable after '${HALT_SUSPECT}' — ${SKIPPED} scenario(s) skipped"
  exit 1
fi
