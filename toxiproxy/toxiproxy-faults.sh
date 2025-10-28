#!/bin/bash
set -euo pipefail

# ------------------------------------------------------------------
# CONFIG
# ------------------------------------------------------------------
NETWORK="kt-op"
NAME_PATTERN="agglayer--"
TOXIPROXY_ADMIN="8474"

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
docker rm -f toxiproxy-sidecar-agglayer 2>/dev/null || true

# ------------------------------------------------------------------
# Start Toxiproxy side-car with lo up and rp_filter disabled
# ------------------------------------------------------------------
docker run --rm -d \
  --name toxiproxy-sidecar-agglayer \
  --net=container:$TARGET_NAME \
  --cap-add=NET_ADMIN --cap-add=NET_RAW \
  --sysctl net.ipv4.conf.all.rp_filter=0 \
  --sysctl net.ipv4.conf.default.rp_filter=0 \
  --sysctl net.ipv4.conf.lo.rp_filter=0 \
  --sysctl "net.ipv4.conf.$INTERFACE.rp_filter=0" \
  jhkimqd/toxiproxy:latest sh -c "ip link set lo up && toxiproxy-server"

# Wait for Toxiproxy
for i in {1..10}; do
  if docker exec toxiproxy-sidecar-agglayer curl -s http://localhost:$TOXIPROXY_ADMIN/version > /dev/null; then
    echo 'Toxiproxy server ready'
    break
  fi
  echo 'Waiting for Toxiproxy server...'
  sleep 1
done
if [[ $i -eq 10 ]]; then
  echo "Error: Toxiproxy not ready"
  exit 1
fi

# ------------------------------------------------------------------
# Create proxies
# ------------------------------------------------------------------
for p in "${PORTS[@]}"; do
  IFS=: read -r tport sname pport <<<"$p"
  docker exec toxiproxy-sidecar-agglayer curl -s -X POST http://localhost:$TOXIPROXY_ADMIN/proxies -d '{
    "name": "port_'"$tport"'_proxy",
    "listen": "0.0.0.0:'"$pport"'",
    "upstream": "'"$TARGET_IP"':'"$tport"'",
    "enabled": true
  }' && echo "Created proxy for $tport" || echo "Failed to create proxy for $tport"
done

# ------------------------------------------------------------------
# nftables - raw for notrack, nat for DNAT to TARGET_IP, filter for drop on external interface
# ------------------------------------------------------------------
docker exec toxiproxy-sidecar-agglayer bash -c '
  set -euo pipefail
  TARGET_IP='"$TARGET_IP"'
  INTERFACE='"$INTERFACE"'

  nft flush ruleset 2>/dev/null || true
  nft add table inet toxiproxy

  # Raw prerouting for notrack (avoid conntrack/rp_filter issues)
  nft add chain inet toxiproxy raw_preroute "{ type filter hook prerouting priority raw ; policy accept ; }"

  # Nat prerouting for DNAT
  nft add chain inet toxiproxy nat_preroute "{ type nat hook prerouting priority dstnat ; policy accept ; }"

  # Nat output for local DNAT (use 127.0.0.1 to avoid loop)
  nft add chain inet toxiproxy nat_output "{ type nat hook output priority 100 ; policy accept ; }"

  # Filter input for drop
  nft add chain inet toxiproxy filter_input "{ type filter hook input priority filter ; policy accept ; }"

  # Notrack in raw
  '"$(for p in "${PORTS[@]}"; do
        IFS=: read -r tport _ pport <<<"$p"
        echo "nft add rule inet toxiproxy raw_preroute ip daddr \$TARGET_IP tcp dport $tport notrack"
      done)"'

  # DNAT in nat prerouting to TARGET_IP:pport (for external)
  '"$(for p in "${PORTS[@]}"; do
        IFS=: read -r tport _ pport <<<"$p"
        echo "nft add rule inet toxiproxy nat_preroute ip daddr \$TARGET_IP tcp dport $tport dnat to \$TARGET_IP:$pport"
      done)"'

  # DNAT in nat output to 127.0.0.1:pport (for local)
  '"$(for p in "${PORTS[@]}"; do
        IFS=: read -r tport _ pport <<<"$p"
        echo "nft add rule inet toxiproxy nat_output ip daddr \$TARGET_IP tcp dport $tport dnat to 127.0.0.1:$pport"
      done)"'

  # Drop in filter for original port on external interface
  '"$(for p in "${PORTS[@]}"; do
        IFS=: read -r tport _ _ <<<"$p"
        echo "nft add rule inet toxiproxy filter_input iifname \"\$INTERFACE\" tcp dport $tport drop"
      done)"'

  echo "NFTables rules:"
  nft list ruleset
'

# ------------------------------------------------------------------
# Verify listeners
# ------------------------------------------------------------------
echo "Listeners:"
docker exec toxiproxy-sidecar-agglayer netstat -tuln | grep ':54' || echo 'No Toxiproxy listeners'

# ------------------------------------------------------------------
# tcpdump for all ports
# ------------------------------------------------------------------
FILTER=$(printf '(tcp port %s or tcp port %s) or ' \
          $(for p in "${PORTS[@]}"; do IFS=: read -r t _ x <<<"$p"; echo "$t $x"; done) | sed 's/ or $//')
echo "tcpdump filter: $FILTER"
docker exec -d toxiproxy-sidecar-agglayer bash -c "tcpdump -i any '$FILTER' -nn -s0 > /tmp/tcpdump_agglayer.log 2>&1" || echo "Warning: tcpdump failed to start"

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
docker exec toxiproxy-sidecar-agglayer nft list ruleset

for p in "${PORTS[@]}"; do
  IFS=: read -r tport sname _ <<<"$p"
  echo "Testing interception for port $tport ($sname)..."

  # Disable proxy
  echo "Disabling proxy for $tport..."
  docker exec toxiproxy-sidecar-agglayer curl -s -X PUT http://localhost:$TOXIPROXY_ADMIN/proxies/port_${tport}_proxy -d '{"enabled": false}'

  echo "Testing with proxy disabled (should fail)..."
  if [[ $(nc_test $TARGET_IP $tport) == "OK" ]]; then
    echo "UNEXPECTED: Connection succeeded on $tport"
  else
    echo "Connection failed as expected on $tport"
  fi

  # Re-enable proxy
  echo "Re-enabling proxy for $tport..."
  docker exec toxiproxy-sidecar-agglayer curl -s -X PUT http://localhost:$TOXIPROXY_ADMIN/proxies/port_${tport}_proxy -d '{"enabled": true}'

  echo "Testing with proxy enabled (should succeed)..."
  if [[ $(nc_test $TARGET_IP $tport) == "OK" ]]; then
    echo "Connection succeeded as expected on $tport"
  else
    echo "UNEXPECTED: Connection failed on $tport"
  fi

  # Sidecar test
  echo "Testing from sidecar with proxy enabled on $tport..."
  docker exec toxiproxy-sidecar-agglayer bash -c "nc -zv -w 5 $TARGET_IP $tport && echo 'Connection succeeded as expected' || echo 'UNEXPECTED: Connection failed'"
done

echo "NAT rules after interception:"
docker exec toxiproxy-sidecar-agglayer nft list ruleset

# ------------------------------------------------------------------
# Latency toxic
# ------------------------------------------------------------------
echo "=== Adding latency toxic ==="
for p in "${PORTS[@]}"; do
  IFS=: read -r tport sname _ <<<"$p"
  echo "Adding latency for port $tport..."
  docker exec toxiproxy-sidecar-agglayer curl -s -X POST http://localhost:$TOXIPROXY_ADMIN/proxies/port_${tport}_proxy/toxics -d '{"name":"latency_'"$tport"'","type":"latency","toxicity":1.0,"attributes":{"latency":5000}}'
done

echo "=== Testing latency toxic ==="
for p in "${PORTS[@]}"; do
  IFS=: read -r tport sname _ <<<"$p"
  echo "Testing for port $tport..."
  output=$(nc_test_latency $TARGET_IP $tport)
  duration=$(echo "$output" | cut -d' ' -f1)
  result=$(echo "$output" | cut -d' ' -f2)
  echo "Connection took $duration seconds, result: $result"
  if [[ $result -eq 0 && $duration -ge 4 && $duration -le 6 ]]; then
    echo "SUCCESS: Delayed by ~5s on $tport"
  else
    echo "UNEXPECTED: Expected ~5s delay on $tport"
  fi
done

# ------------------------------------------------------------------
# Cleanup
# ------------------------------------------------------------------
docker exec toxiproxy-sidecar-agglayer bash -c "pkill tcpdump || true"
echo "tcpdump output:"
docker exec toxiproxy-sidecar-agglayer cat /tmp/tcpdump_agglayer.log || echo "No tcpdump log"

echo -e "\n=== Dynamic Management ==="
echo "List proxies: docker exec toxiproxy-sidecar-agglayer curl -s http://localhost:$TOXIPROXY_ADMIN/proxies | jq"
for p in "${PORTS[@]}"; do
  IFS=: read -r tport _ _ <<<"$p"
  echo "Remove toxic for $tport: docker exec toxiproxy-sidecar-agglayer curl -X DELETE http://localhost:$TOXIPROXY_ADMIN/proxies/port_${tport}_proxy/toxics/latency_${tport}"
done
echo "Stop sidecar: docker stop toxiproxy-sidecar-agglayer"