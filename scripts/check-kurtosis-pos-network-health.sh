#!/bin/bash
# Polygon PoS Network Health Check Script
# Verifies Kurtosis-deployed Polygon PoS network is in steady state before chaos testing
#
# Usage: ./check-kurtosis-pos-network-health.sh [enclave]
# Example: ./check-kurtosis-pos-network-health.sh pos

ENCLAVE="${1:-pos}"
DEBUG="${DEBUG:-0}"

echo "🔍 Checking network health for enclave: $ENCLAVE"

# Step 1: Get Prometheus URL
echo -n "   Getting Prometheus endpoint... "
PROMETHEUS_URL=$(kurtosis port print "$ENCLAVE" prometheus http 2>/dev/null)
if [[ -z "$PROMETHEUS_URL" ]]; then
    echo "❌"
    echo "   Error: Could not get Prometheus URL"
    echo "   Try: kurtosis port print $ENCLAVE prometheus http"
    exit 1
fi
echo "✅"
[[ "$DEBUG" = "1" ]] && echo "   URL: $PROMETHEUS_URL"

# Step 2: Test connectivity
echo -n "   Testing connectivity... "
CONN_TEST=$(curl -s --max-time 5 "${PROMETHEUS_URL}/api/v1/query?query=up" 2>&1)
if ! echo "$CONN_TEST" | grep -q "success"; then
    echo "❌"
    echo "   Error: Prometheus not responding"
    [[ "$DEBUG" = "1" ]] && echo "   Response: $CONN_TEST"
    exit 1
fi
echo "✅"

# Step 3: Check block production
echo -n "   Checking block production... "
BLOCK_DATA=$(curl -s --max-time 10 "${PROMETHEUS_URL}/api/v1/query" --data-urlencode "query=rate(chain_head_block[1m])" 2>&1)
if echo "$BLOCK_DATA" | grep -q '"result":\['; then
    # Extract the value if possible
    BLOCK_RATE=$(echo "$BLOCK_DATA" | grep -o '"value":\[[^]]*\]' | head -1 | grep -o '[0-9.eE+-]\+' | tail -1)
    if [[ -n "$BLOCK_RATE" && "$BLOCK_RATE" != "0" ]]; then
        echo "✅ (rate: $BLOCK_RATE)"
    else
        echo "⚠️  (rate is 0 or unknown)"
    fi
else
    echo "⚠️  (no data)"
fi
[[ "$DEBUG" = "1" ]] && echo "   Data: ${BLOCK_DATA:0:200}"

# Step 4: Check consensus
echo -n "   Checking consensus... "
CONSENSUS_DATA=$(curl -s --max-time 10 "${PROMETHEUS_URL}/api/v1/query" --data-urlencode "query=rate(cometbft_consensus_height[1m])" 2>&1)
if echo "$CONSENSUS_DATA" | grep -q '"result":\['; then
    CONSENSUS_RATE=$(echo "$CONSENSUS_DATA" | grep -o '"value":\[[^]]*\]' | head -1 | grep -o '[0-9.eE+-]\+' | tail -1)
    if [[ -n "$CONSENSUS_RATE" && "$CONSENSUS_RATE" != "0" ]]; then
        echo "✅ (rate: $CONSENSUS_RATE)"
    else
        echo "⚠️  (rate is 0 or unknown)"
    fi
else
    echo "⚠️  (no data)"
fi
[[ "$DEBUG" = "1" ]] && echo "   Data: ${CONSENSUS_DATA:0:200}"

# Step 5: Check validator count
echo -n "   Checking validators... "
VALIDATOR_DATA=$(curl -s --max-time 10 "${PROMETHEUS_URL}/api/v1/query" --data-urlencode "query=cometbft_consensus_validators" 2>&1)
if echo "$VALIDATOR_DATA" | grep -q '"result":\['; then
    VALIDATOR_COUNT=$(echo "$VALIDATOR_DATA" | grep -o '"value":\[[^]]*\]' | head -1 | grep -o '[0-9]\+' | tail -1)
    if [[ -n "$VALIDATOR_COUNT" ]]; then
        echo "✅ ($VALIDATOR_COUNT active)"
    else
        echo "⚠️  (count unknown)"
    fi
else
    echo "⚠️  (no data)"
fi

# Step 6: Check for chaos artifacts
echo -n "   Checking for chaos artifacts... "
SIDECAR_COUNT=$(docker ps --filter "name=chaos-sidecar" --format "{{.ID}}" 2>/dev/null | wc -l)
if [[ "$SIDECAR_COUNT" -eq 0 ]]; then
    echo "✅ (none)"
else
    echo "⚠️  ($SIDECAR_COUNT sidecar(s) found)"
    echo "      Run: docker rm -f \$(docker ps -q --filter 'name=chaos-sidecar')"
fi

echo ""
echo "✅ Health check complete"
