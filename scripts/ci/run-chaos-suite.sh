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
# Health gate checks ALL 8 validators so a stuck-but-reachable validator
# (common after scenarios that legitimately catch fork/divergence bugs)
# is detected before it poisons the next scenario's pre-fault query.
# Require a majority to advance; a single stuck node is tolerated so a
# scenario still cooling down its target doesn't false-fail the gate.
ALL_VALIDATORS="1 2 3 4 5 6 7 8"
HEALTH_CHECK_VALIDATORS="${ALL_VALIDATORS}"
# Minimum validators that must advance for the devnet to be considered healthy.
# 7/8 tolerates one lagging node (typical scenario target recovering); fewer
# advancing indicates real consensus trouble that will break the next scenario.
HEALTH_CHECK_MIN_ADVANCING=7
declare -A VALIDATOR_RPCS
declare -A VALIDATOR_HEIMDALL_RPCS
for v in ${ALL_VALIDATORS}; do
  raw="$(timeout 30 kurtosis port print "${ENCLAVE_NAME}" "l2-el-${v}-bor-heimdall-v2-validator" rpc 2>/dev/null || echo "")"
  raw="${raw#http://}"; raw="${raw#https://}"
  if [[ -n "${raw}" ]]; then
    VALIDATOR_RPCS[${v}]="http://${raw}"
  fi
  cl_raw="$(timeout 30 kurtosis port print "${ENCLAVE_NAME}" "l2-cl-${v}-heimdall-v2-bor-validator" rpc 2>/dev/null || echo "")"
  cl_raw="${cl_raw#http://}"; cl_raw="${cl_raw#https://}"
  if [[ -n "${cl_raw}" ]]; then
    VALIDATOR_HEIMDALL_RPCS[${v}]="http://${cl_raw}"
  fi
done

# ═══════════════════════════════════════════════════════════════════════
#  HEALTH CHECK & RECOVERY FUNCTIONS
# ═══════════════════════════════════════════════════════════════════════

# ── Probe a single validator's current height ──
# Tries Bor RPC first with its cached Kurtosis URL. On failure, re-resolves
# the port via `kurtosis port print` (docker restart and Kurtosis proxy
# re-mapping can invalidate the startup-cached URL, producing false
# "unreachable" readings even though the validator is healthy — this is
# the failure mode that halted the 2026-04-21 chaos-suite run). On further
# failure, falls back to Heimdall /status, which is the consensus-layer
# source of truth and independent of Bor RPC port state.
#
# Echoes a SINGLE line on stdout describing the probe result:
#   "B <height>"  — Bor RPC reachable, height from Bor
#   "H <height>"  — Bor RPC failed but Heimdall reachable at this height
#   ""            — neither reachable
# Source marker lets the caller compare heights only against the SAME
# source on both snapshots, so a fallback switch doesn't look like a jump.
probe_validator_height() {
  local v="$1"
  local url="${VALIDATOR_RPCS[${v}]:-}"
  local h

  # 1. Try cached Bor RPC URL
  if [[ -n "${url}" ]]; then
    h=$(timeout 5 cast bn --rpc-url "${url}" 2>/dev/null || echo "")
    if [[ -n "${h}" && "${h}" != "0" ]]; then
      echo "B ${h}"; return 0
    fi
  fi

  # 2. Re-resolve Kurtosis port mapping in case it changed after a
  # container restart, then retry Bor RPC.
  local raw
  raw="$(timeout 10 kurtosis port print "${ENCLAVE_NAME}" "l2-el-${v}-bor-heimdall-v2-validator" rpc 2>/dev/null || echo "")"
  raw="${raw#http://}"; raw="${raw#https://}"
  if [[ -n "${raw}" ]]; then
    local fresh_url="http://${raw}"
    if [[ "${fresh_url}" != "${url}" ]]; then
      VALIDATOR_RPCS[${v}]="${fresh_url}"
    fi
    h=$(timeout 5 cast bn --rpc-url "${fresh_url}" 2>/dev/null || echo "")
    if [[ -n "${h}" && "${h}" != "0" ]]; then
      echo "B ${h}"; return 0
    fi
  fi

  # 3. Bor RPC is unreachable — fall back to Heimdall /status.
  local cl_url="${VALIDATOR_HEIMDALL_RPCS[${v}]:-}"
  if [[ -z "${cl_url}" ]]; then
    local cl_raw
    cl_raw="$(timeout 10 kurtosis port print "${ENCLAVE_NAME}" "l2-cl-${v}-heimdall-v2-bor-validator" rpc 2>/dev/null || echo "")"
    cl_raw="${cl_raw#http://}"; cl_raw="${cl_raw#https://}"
    if [[ -n "${cl_raw}" ]]; then
      cl_url="http://${cl_raw}"
      VALIDATOR_HEIMDALL_RPCS[${v}]="${cl_url}"
    fi
  fi
  if [[ -n "${cl_url}" ]]; then
    local cl_h
    cl_h=$(timeout 5 curl -s "${cl_url}/status" 2>/dev/null \
      | jq -r '.result.sync_info.latest_block_height // empty' 2>/dev/null || echo "")
    if [[ -n "${cl_h}" && "${cl_h}" != "0" ]]; then
      echo "H ${cl_h}"; return 0
    fi
  fi

  echo ""; return 1
}

# ── Health gate: verify devnet is alive (blocks advancing) ──
# Snapshots heights on all validators, waits 30s, requires at least
# HEALTH_CHECK_MIN_ADVANCING to advance. Any-advancing was misleading:
# after a scenario that legitimately stalls one validator's consensus,
# the next scenario's pre-fault query (which usually spans multiple
# validators with min()) would fail even though this gate said "OK".
#
# Uses probe_validator_height() so a validator whose Bor RPC port got
# re-mapped by Kurtosis still counts as advancing when Heimdall confirms
# consensus participation. Requires the probe source to match across both
# snapshots (no mixed Bor/Heimdall comparisons — their block spaces differ).
check_devnet_health() {
  local advanced_count=0
  local failed_validators=""
  local total_validators=0
  local min_delta=999999 max_delta=0
  local heimdall_fallback_count=0

  # Snapshot heights and record source (Bor or Heimdall) per validator.
  declare -A probe_before
  for v in ${HEALTH_CHECK_VALIDATORS}; do
    total_validators=$((total_validators + 1))
    probe_before[${v}]="$(probe_validator_height "${v}")"
  done

  sleep 30

  # Re-check and compare. Collect per-validator detail silently and only
  # emit it if the gate FAILS — the healthy path prints a one-line summary.
  local detail=""
  for v in ${HEALTH_CHECK_VALIDATORS}; do
    local before="${probe_before[${v}]}"
    local after
    after="$(probe_validator_height "${v}")"
    if [[ -z "${before}" || -z "${after}" ]]; then
      detail+=$'\n'"  validator ${v}: ⚠ unreachable (Bor RPC + Heimdall /status both down)"
      failed_validators="${failed_validators} ${v}"
      continue
    fi
    local before_src="${before%% *}" before_h="${before#* }"
    local after_src="${after%% *}" after_h="${after#* }"
    if [[ "${before_src}" != "${after_src}" ]]; then
      # Mixed sources (Bor recovered mid-wait, or lost Bor mid-wait). Don't
      # compare raw heights across consensus layers; count as advancing
      # since we got a response from both snapshots.
      detail+=$'\n'"  validator ${v}: probe source shifted ${before_src}→${after_src} — counting as advancing"
      advanced_count=$((advanced_count + 1))
      continue
    fi
    local delta=$((after_h - before_h))
    local src_tag=""
    [[ "${after_src}" == "H" ]] && { src_tag=" [Heimdall fallback]"; heimdall_fallback_count=$((heimdall_fallback_count + 1)); }
    detail+=$'\n'"  validator ${v}: blocks ${before_h} → ${after_h} (Δ${delta} in 30s)${src_tag}"
    if [[ "${delta}" -gt 0 ]]; then
      advanced_count=$((advanced_count + 1))
      [[ ${delta} -lt ${min_delta} ]] && min_delta=${delta}
      [[ ${delta} -gt ${max_delta} ]] && max_delta=${delta}
    else
      failed_validators="${failed_validators} ${v}"
    fi
  done

  if [[ ${advanced_count} -ge ${HEALTH_CHECK_MIN_ADVANCING} ]]; then
    local spread="Δ${min_delta}"
    [[ ${min_delta} -ne ${max_delta} ]] && spread="Δ${min_delta}..${max_delta}"
    local fb_note=""
    [[ ${heimdall_fallback_count} -gt 0 ]] && fb_note=" (${heimdall_fallback_count} via Heimdall fallback)"
    echo "  ✓ Health gate: ${advanced_count}/${total_validators} validators advancing (${spread} in 30s)${fb_note}"
    return 0
  fi

  # Unhealthy — now it matters. Emit the full per-validator detail we collected.
  printf '%s\n' "${detail#$'\n'}"
  echo ""
  echo "  ╔══════════════════════════════════════════════════════╗"
  echo "  ║  DEVNET UNHEALTHY: ${advanced_count} advancing, need ${HEALTH_CHECK_MIN_ADVANCING}"
  echo "  ╚══════════════════════════════════════════════════════╝"
  diagnose_network_state "${failed_validators}"
  echo ""
  return 1
}

# ── Recovery: scan all validators and docker-restart unresponsive ones ──
# A validator is "unresponsive" only when BOTH Bor RPC (after port re-resolve)
# AND Heimdall /status fail — a stale Kurtosis port alone must not trigger
# a disruptive restart of a healthy node.
recover_unresponsive_validators() {
  local unresponsive="" responsive_count=0

  # Scan quietly; only call out problem validators.
  for v in ${ALL_VALIDATORS}; do
    local probe
    probe="$(probe_validator_height "${v}")"
    if [[ -z "${probe}" ]]; then
      echo "  validator ${v}: ✗ unreachable (Bor RPC + Heimdall both down)"
      unresponsive="${unresponsive} ${v}"
    else
      responsive_count=$((responsive_count + 1))
    fi
  done

  if [[ -z "$(echo "${unresponsive}" | tr -d ' ')" ]]; then
    echo "  All ${responsive_count} validators RPC-reachable — waiting 60s for consensus recovery..."
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
echo "::group::Scenario selection"
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
echo "::endgroup::"

# ── Scenarios with hard timing floors that cannot be compressed ──
SLOW_SCENARIOS="validator-freeze-zombie shifting-fault-combinations rolling-restart oom-flapping-loop"

# ── Scenarios that need their full YAML cooldown for post-fault recovery ──
# rpc-node-crash-reconnect: 3 rapid SIGKILLs leave the RPC hundreds of blocks
# behind; the default 90s CI cooldown isn't enough for witness-sync to close
# the gap, so use the scenario's yaml cooldown (3m) instead.
LONG_COOLDOWN_SCENARIOS="simultaneous-validator-restart three-validator-full-isolation coordinated-full-cluster-crash span-boundary-partition kill-resync-under-peer-loss rpc-node-crash-reconnect"

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

# Render a compact one-liner OUTSIDE the scenario group so operators can
# skim results without expanding each group. Called after every ::endgroup::
# in the scenario loop. Reads EXIT_CODE and SCENARIO_ELAPSED from caller.
emit_scenario_summary() {
  local label="$1" n="$2" dur="${3:-?s}" extra="${4:-}"
  local suffix=""
  [[ -n "${extra}" ]] && suffix=" ${extra}"
  case "${label}" in
    PASSED)    echo "✅ PASSED  ${n} (${dur})${suffix}" ;;
    FAILED)    echo "❌ FAILED  ${n} (${dur})${suffix}" ;;
    TIMEOUT)   echo "⏱  TIMEOUT ${n} (${dur})${suffix}" ;;
    ERROR)     echo "⚠️  ERROR   ${n} (${dur})${suffix}" ;;
    SKIPPED)   echo "⏭  SKIPPED ${n}${suffix}" ;;
    CONFIRMED) echo "🔥 CONFIRMED-BUG ${n}${suffix}" ;;
  esac
}

for scenario in ${SELECTED}; do
  name=$(basename "${scenario}" .yaml)
  SCENARIO_START=$(date +%s)
  echo "::group::${name}"

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
        emit_scenario_summary SKIPPED "${name}" "" "(devnet halted at pre-flight)"
        break
      fi
    fi
    echo "Devnet recovered after restart"
  fi

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
  # A steady-state abort almost always means the PREVIOUS scenario degraded the
  # devnet and our post-scenario probe missed it. Attribute the halt to the
  # upstream scenario (not this one) and escalate: force-restart → full
  # redeploy before giving up.
  if [[ ${EXIT_CODE} -ne 0 ]] && grep -q "pre-fault health check failed:.*steady state" "${REPORT_DIR}/${name}.log" 2>/dev/null; then
    echo "::warning::${name}: pre-fault health check failed — system not in steady state (likely upstream damage)"
    echo "Recovering network before retry..."
    force_restart_all_validators
    if ! check_devnet_health; then
      echo "Force-restart did not restore consensus — attempting full redeploy..."
      if ! redeploy_devnet; then
        echo "::error::Recovery failed after pre-fault check — devnet unrecoverable"
        echo "ERROR  ${name} (steady-state check failed, redeploy failed)" >> "${REPORT_DIR}/results.txt"
        ERRORS=$((ERRORS + 1))
        DEVNET_HALTED=true
        if [[ -n "${LAST_ERROR_SCENARIO}" ]]; then
          HALT_SUSPECT="${LAST_ERROR_SCENARIO} (degraded devnet; pre-fault check failed in ${name})"
        else
          HALT_SUSPECT="${name} (pre-fault: devnet already broken on entry)"
        fi
        echo "::endgroup::"
        emit_scenario_summary ERROR "${name}" "$(( $(date +%s) - SCENARIO_START ))s" "(steady-state check + redeploy failed)"
        break
      fi
    fi
    echo "Network recovered — retrying ${name}..."
    EXIT_CODE=0
    timeout --kill-after=30 ${SCENARIO_TIMEOUT} ./bin/chaos-runner run \
      --scenario "${scenario}" \
      ${SET_FLAGS} \
      --format text 2>&1 | tee -a "${REPORT_DIR}/${name}.log" || EXIT_CODE=$?
  fi

  # ── Network-death detection: full redeploy + verify ──
  # quick_liveness_probe only tests RPC reachability, which misses degraded
  # states where validators are reachable but consensus is stalled (e.g. after
  # a scenario that legitimately caught a fork/divergence bug). Always use the
  # full block-advancement check after exit != 0 so an upstream failure can't
  # poison the pre-fault check of the next scenario.
  if [[ ${EXIT_CODE} -ne 0 ]]; then
    NETWORK_OK=true
    echo "Post-scenario health check (full block-advancement probe)..."
    if ! check_devnet_health; then
      NETWORK_OK=false
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
        emit_scenario_summary ERROR "${name}" "$(( $(date +%s) - SCENARIO_START ))s" "(broke devnet; redeploy failed)"
        break
      fi

      echo "Re-running ${name} on fresh devnet to confirm..."
      EXIT_CODE=0
      timeout --kill-after=30 ${SCENARIO_TIMEOUT} ./bin/chaos-runner run \
        --scenario "${scenario}" \
        ${SET_FLAGS} \
        --format text 2>&1 | tee "${REPORT_DIR}/${name}-verify.log" || EXIT_CODE=$?

      echo "Verification health check (full block-advancement probe)..."
      if ! check_devnet_health; then
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
        emit_scenario_summary CONFIRMED "${name}" "" "(breaks devnet reproducibly on fresh enclave)"
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
  SCENARIO_ELAPSED="$(( $(date +%s) - SCENARIO_START ))s"
  SUMMARY_LABEL=""
  SUMMARY_EXTRA=""
  if [[ ${EXIT_CODE} -eq 124 || ${EXIT_CODE} -eq 137 ]]; then
    echo "::error::TIMEOUT: ${name} — exceeded ${SCENARIO_TIMEOUT}s (network may be hung)"
    echo "ERROR  ${name} (timeout after ${SCENARIO_TIMEOUT}s)" >> "${REPORT_DIR}/results.txt"
    ERRORS=$((ERRORS + 1))
    CONSECUTIVE_ERRORS=$((CONSECUTIVE_ERRORS + 1))
    LAST_ERROR_SCENARIO="${name}"
    SUMMARY_LABEL="TIMEOUT"
    SUMMARY_EXTRA="(exceeded ${SCENARIO_TIMEOUT}s)"
  elif [[ ${EXIT_CODE} -eq 0 ]]; then
    echo "PASSED ${name}" >> "${REPORT_DIR}/results.txt"
    PASSED=$((PASSED + 1))
    CONSECUTIVE_ERRORS=0
    LAST_ERROR_SCENARIO=""
    SUMMARY_LABEL="PASSED"
  elif [[ ${EXIT_CODE} -eq 1 ]]; then
    echo "::error::FAILED: ${name} — one or more critical success criteria did not pass"
    echo "FAILED ${name}" >> "${REPORT_DIR}/results.txt"
    FAILED=$((FAILED + 1))
    TEST_FAILURES="${TEST_FAILURES}${name}"$'\n'
    SUMMARY_LABEL="FAILED"
    SUMMARY_EXTRA="(critical criteria missed — see log ${REPORT_DIR}/${name}.log)"
  else
    echo "::error::ERROR: ${name} (exit: ${EXIT_CODE})"
    echo "ERROR  ${name}" >> "${REPORT_DIR}/results.txt"
    ERRORS=$((ERRORS + 1))
    CONSECUTIVE_ERRORS=$((CONSECUTIVE_ERRORS + 1))
    LAST_ERROR_SCENARIO="${name}"
    SUMMARY_LABEL="ERROR"
    SUMMARY_EXTRA="(exit ${EXIT_CODE})"
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
      emit_scenario_summary "${SUMMARY_LABEL}" "${name}" "${SCENARIO_ELAPSED}" "${SUMMARY_EXTRA}"
      echo "::warning::Devnet unrecoverable — halting suite after ${name}"
      break
    fi
    echo "Network recovered — resetting error counter"
    CONSECUTIVE_ERRORS=0
  fi

  echo "::endgroup::"
  emit_scenario_summary "${SUMMARY_LABEL}" "${name}" "${SCENARIO_ELAPSED}" "${SUMMARY_EXTRA}"
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
  printf '%s' "${TEST_FAILURES}" | sed 's/^/  - /'
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
    printf '%s' "${TEST_FAILURES}" | sed 's/^/- /'
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
