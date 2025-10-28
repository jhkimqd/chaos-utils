#!/bin/bash
set -euo pipefail

# ------------------------------------------------------------------
# CONFIG
# ------------------------------------------------------------------
NETWORK="kt-op"
NAME_PATTERN="agglayer--"
ENVOY_ADMIN="9901"

PORTS=(
  "4443:aglr-grpc:54443"
  "4444:aglr-readrpc:54444"
  "4446:aglr-admin:54446"
)

# ------------------------------------------------------------------
# Find agglayer container
# ------------------------------------------------------------------
TARGET_NAME=$(docker network inspect "$NETWORK" | jq -r '.[] | .Containers | to_entries[] | select(.value.Name | test("'"$NAME_PATTERN"'")) | .value.Name')
if [[ -z "$TARGET_NAME" ]]; then
  echo "Error: No container found with name matching $NAME_PATTERN on network $NETWORK"
  exit 1
fi
TARGET_ID=$(docker network inspect "$NETWORK" | jq -r '.[] | .Containers | to_entries[] | select(.value.Name | test("'"$NAME_PATTERN"'")) | .key')
TARGET_IP=$(docker network inspect "$NETWORK" | jq -r '.[] | .Containers | to_entries[] | select(.value.Name | test("'"$NAME_PATTERN"'")) | .value.IPv4Address | split("/")[0]')

echo "Target: $TARGET_NAME ($TARGET_ID), IP: $TARGET_IP"

# Get interface
PID=$(docker inspect -f '{{.State.Pid}}' $TARGET_ID)
INTERFACE=$(sudo nsenter -t $PID -n ip link | grep -oP '^\d+: \Keth\d+')
if [[ -z "$INTERFACE" ]]; then
  echo "Error: No eth interface found"
  exit 1
fi
echo "Interface: $INTERFACE"

# ------------------------------------------------------------------
# Clean old side-car
# ------------------------------------------------------------------
docker rm -f envoy-sidecar-agglayer 2>/dev/null || true

# ------------------------------------------------------------------
# Generate Envoy config with fault injection
# ------------------------------------------------------------------
ENVOY_CONFIG=$(cat <<EOF
static_resources:
  listeners:
$(for p in "${PORTS[@]}"; do
  IFS=: read -r tport sname pport <<<"$p"
  echo "  - name: ${sname}_listener"
  echo "    address:"
  echo "      socket_address:"
  echo "        address: 0.0.0.0"
  echo "        port_value: $pport"
  echo "    filter_chains:"
  echo "    - filters:"
  echo "      - name: envoy.filters.network.http_connection_manager"
  echo "        typed_config:"
  echo "          \"@type\": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager"
  echo "          stat_prefix: ${sname}_proxy"
  echo "          http2_protocol_options: {}"
  echo "          route_config:"
  echo "            name: local_route"
  echo "            virtual_hosts:"
  echo "            - name: backend"
  echo "              domains: [\"*\"]"
  echo "              routes:"
  echo "              - match:"
  echo "                  prefix: \"/\""
  echo "                route:"
  echo "                  cluster: ${sname}_cluster"
  echo "          http_filters:"
  echo "          - name: envoy.filters.http.fault"
  echo "            typed_config:"
  echo "              \"@type\": type.googleapis.com/envoy.extensions.filters.http.fault.v3.HTTPFault"
  echo "              delay:"
  echo "                fixed_delay: 5s"
  echo "                percentage:"
  echo "                  numerator: 100"
  echo "              abort:"
  echo "                http_status: 503"
  echo "                percentage:"
  echo "                  numerator: 50"
  echo "          - name: envoy.filters.http.router"
done)
  clusters:
$(for p in "${PORTS[@]}"; do
  IFS=: read -r tport sname pport <<<"$p"
  echo "  - name: ${sname}_cluster"
  echo "    type: LOGICAL_DNS"
  echo "    lb_policy: ROUND_ROBIN"
  echo "    load_assignment:"
  echo "      cluster_name: ${sname}_cluster"
  echo "      endpoints:"
  echo "      - lb_endpoints:"
  echo "        - endpoint:"
  echo "            address:"
  echo "              socket_address:"
  echo "                address: $TARGET_IP"
  echo "                port_value: $tport"
done)
admin:
  address:
    socket_address:
      address: 0.0.0.0
      port_value: $ENVOY_ADMIN
EOF
)

# ------------------------------------------------------------------
# Start Envoy side-car with lo up and rp_filter disabled
# ------------------------------------------------------------------
echo "$ENVOY_CONFIG" | docker run --rm -d \
  --name envoy-sidecar-agglayer \
  --net=container:$TARGET_NAME \
  --cap-add=NET_ADMIN --cap-add=NET_RAW \
  --sysctl net.ipv4.conf.all.rp_filter=0 \
  --sysctl net.ipv4.conf.default.rp_filter=0 \
  --sysctl net.ipv4.conf.lo.rp_filter=0 \
  --sysctl "net.ipv4.conf.$INTERFACE.rp_filter=0" \
  envoyproxy/envoy:v1.28-latest -c /dev/stdin

# Wait for Envoy
for i in {1..10}; do
  if docker exec envoy-sidecar-agglayer curl -s http://localhost:$ENVOY_ADMIN/server_info | grep -q "state.*LIVE"; then
    echo 'Envoy server ready'
    break
  fi
  echo 'Waiting for Envoy server...'
  sleep 1
done
if [[ $i -eq 10 ]]; then
  echo "Error: Envoy not ready"
  exit 1
fi

# ------------------------------------------------------------------
# nftables - raw for notrack, nat for DNAT to TARGET_IP, filter for drop on external interface
# ------------------------------------------------------------------
docker exec envoy-sidecar-agglayer bash -c '
  set -euo pipefail
  TARGET_IP='"$TARGET_IP"'
  INTERFACE='"$INTERFACE"'

  nft flush ruleset 2>/dev/null || true
  nft add table inet envoy

  # Raw prerouting for notrack (avoid conntrack/rp_filter issues)
  nft add chain inet envoy raw_preroute "{ type filter hook prerouting priority raw ; policy accept ; }"

  # Nat prerouting for DNAT
  nft add chain inet envoy nat_preroute "{ type nat hook prerouting priority dstnat ; policy accept ; }"

  # Nat output for local DNAT (use 127.0.0.1 to avoid loop)
  nft add chain inet envoy nat_output "{ type nat hook output priority 100 ; policy accept ; }"

  # Filter input for drop
  nft add chain inet envoy filter_input "{ type filter hook input priority filter ; policy accept ; }"

  # Notrack in raw
  '"$(for p in "${PORTS[@]}"; do
        IFS=: read -r tport _ pport <<<"$p"
        echo "nft add rule inet envoy raw_preroute ip daddr \$TARGET_IP tcp dport $tport notrack"
      done)"'

  # DNAT in nat prerouting to TARGET_IP:pport (for external)
  '"$(for p in "${PORTS[@]}"; do
        IFS=: read -r tport _ pport <<<"$p"
        echo "nft add rule inet envoy nat_preroute ip daddr \$TARGET_IP tcp dport $tport dnat to \$TARGET_IP:$pport"
      done)"'

  # DNAT in nat output to 127.0.0.1:pport (for local)
  '"$(for p in "${PORTS[@]}"; do
        IFS=: read -r tport _ pport <<<"$p"
        echo "nft add rule inet envoy nat_output ip daddr \$TARGET_IP tcp dport $tport dnat to 127.0.0.1:$pport"
      done)"'

  # Drop in filter for original port on external interface
  '"$(for p in "${PORTS[@]}"; do
        IFS=: read -r tport _ _ <<<"$p"
        echo "nft add rule inet envoy filter_input iifname \"\$INTERFACE\" tcp dport $tport drop"
      done)"'

  echo "NFTables rules:"
  nft list ruleset
'

# ------------------------------------------------------------------
# Verify listeners
# ------------------------------------------------------------------
echo "Listeners:"
docker exec envoy-sidecar-agglayer netstat -tuln | grep ':54' || echo 'No Envoy listeners'

# ------------------------------------------------------------------
# tcpdump for all ports
# ------------------------------------------------------------------
FILTER=$(printf '(tcp port %s or tcp port %s) or ' \
          "$(for p in "${PORTS[@]}"; do IFS=: read -r t _ x <<<"$p"; echo "$t $x"; done)" | sed 's/ or $//')
echo "tcpdump filter: $FILTER"
docker exec -d envoy-sidecar-agglayer bash -c "tcpdump -i any '$FILTER' -nn -s0 > /tmp/tcpdump_agglayer.log 2>&1" || echo "Warning: tcpdump failed to start"

# ------------------------------------------------------------------
# Helper: nc with timeout
# ------------------------------------------------------------------
nc_test() {
  local ip=$1 port=$2
  docker run --rm --network "$NETWORK" busybox nc -zv -w 5 $ip $port && echo "OK" || echo "FAIL"
}

nc_test_latency() {
  local ip=$1 port=$2
  docker run --rm --network "$NETWORK" busybox sh -c "start=\$(date +%s); nc -zv -w 10 $ip $port; result=\$?; end=\$(date +%s); duration=\$((end - start)); echo \"\$duration \$result\""
}

# ------------------------------------------------------------------
# Interception test
# ------------------------------------------------------------------
echo "=== Proxy Interception Test ==="
echo "Initial NFT rules:"
docker exec envoy-sidecar-agglayer nft list ruleset

for p in "${PORTS[@]}"; do
  IFS=: read -r tport sname _ <<<"$p"
  echo "Testing interception for port $tport ($sname)..."

  # Note: Envoy doesn't have a simple enable/disable like Toxiproxy.
  # For testing, we assume faults are always applied. To simulate disable,
  # you could reload Envoy with a config without fault filters (not implemented here).

  echo "Testing with Envoy proxy (faults enabled)..."
  if [[ $(nc_test $TARGET_IP $tport) == "OK" ]]; then
    echo "Connection succeeded (but may be delayed/aborted due to faults)"
  else
    echo "Connection failed (possibly due to faults)"
  fi

  # Sidecar test
  echo "Testing from sidecar with Envoy proxy..."
  docker exec envoy-sidecar-agglayer bash -c "nc -zv -w 5 $TARGET_IP $tport && echo 'Connection succeeded' || echo 'Connection failed'"
done

echo "NAT rules after interception:"
docker exec envoy-sidecar-agglayer nft list ruleset

# ------------------------------------------------------------------
# Fault testing (faults are pre-configured in Envoy)
# ------------------------------------------------------------------
echo "=== Testing Envoy faults ==="
echo "Faults configured: 5s delay on 100% of requests, 503 abort on 50% of requests (layer 7 simulation)"
echo "For layer 6 (data corruption), Envoy doesn't have built-in support; consider custom Lua filters for advanced use."

for p in "${PORTS[@]}"; do
  IFS=: read -r tport sname _ <<<"$p"
  echo "Testing faults for port $tport ($sname)..."
  output=$(nc_test_latency $TARGET_IP $tport)
  duration=$(echo "$output" | cut -d' ' -f1)
  result=$(echo "$output" | cut -d' ' -f2)
  echo "Connection took $duration seconds, result: $result"
  if [[ $result -eq 0 && $duration -ge 4 ]]; then
    echo "SUCCESS: Delay applied (~5s)"
  elif [[ $result -ne 0 ]]; then
    echo "SUCCESS: Abort applied (connection failed)"
  else
    echo "UNEXPECTED: No fault detected"
  fi
done

# ------------------------------------------------------------------
# Cleanup
# ------------------------------------------------------------------
docker exec envoy-sidecar-agglayer bash -c "pkill tcpdump || true"
echo "tcpdump output:"
docker exec envoy-sidecar-agglayer cat /tmp/tcpdump_agglayer.log || echo "No tcpdump log"

echo -e "\n=== Dynamic Management ==="
echo "Envoy admin: docker exec envoy-sidecar-agglayer curl -s http://localhost:$ENVOY_ADMIN/"
echo "To modify faults, update the config and reload Envoy (e.g., via SIGUSR1 or restart with new config)"
echo "Stop sidecar: docker stop envoy-sidecar-agglayer"
