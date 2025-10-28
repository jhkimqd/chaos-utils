#!/bin/bash
# Script to start chaos utility sidecars for agglayer container on kt-op network,
# apply initial Comcast and Toxiproxy faults, and keep containers running for dynamic fault management.
set -e

# Remove existing containers
docker rm -f toxiproxy-sidecar-agglayer
docker rm -f comcast-sidecar-agglayer

# Variables
NETWORK="kt-op"
NAME_PATTERN="agglayer--"
TOXIPROXY_ADMIN_PORT="8474"

# Find the agglayer container
TARGET_NAME=$(docker network inspect $NETWORK | jq -r '.[] | .Containers | to_entries[] | select(.value.Name | test("'"$NAME_PATTERN"'")) | .value.Name')
if [[ -z "$TARGET_NAME" ]]; then
  echo "Error: No container found with name matching $NAME_PATTERN on network $NETWORK"
  exit 1
fi
TARGET_ID=$(docker network inspect $NETWORK | jq -r '.[] | .Containers | to_entries[] | select(.value.Name | test("'"$NAME_PATTERN"'")) | .key')
TARGET_IP=$(docker network inspect $NETWORK | jq -r '.[] | .Containers | to_entries[] | select(.value.Name | test("'"$NAME_PATTERN"'")) | .value.IPv4Address | split("/")[0]')

echo "Target: $TARGET_NAME ($TARGET_ID), IP: $TARGET_IP"

# Get PID and interface
PID=$(docker inspect -f '{{.State.Pid}}' $TARGET_ID)
INTERFACE=$(sudo nsenter -t $PID -n ip link | grep -oP '^\d+: \Keth\d+')
if [[ -z "$INTERFACE" ]]; then
  echo "Error: No eth interface found"
  exit 1
fi
echo "Interface: $INTERFACE"

# Clean up existing rules
sudo nsenter -t $PID -n iptables -F 2>/dev/null || true
sudo nsenter -t $PID -n iptables -t nat -F 2>/dev/null || true
sudo nsenter -t $PID -n iptables -t filter -F FORWARD 2>/dev/null || true

# Start Toxiproxy sidecar
docker run --rm -d \
  --name toxiproxy-sidecar-agglayer \
  --net=container:$TARGET_ID \
  --cap-add=NET_ADMIN --cap-add=NET_RAW \
  jhkimqd/toxiproxy:latest

# Configure Toxiproxy and iptables
docker exec toxiproxy-sidecar-agglayer bash -c "
  TARGET_IP='$TARGET_IP'
  # Wait for Toxiproxy server
  for i in {1..5}; do
    if curl -s http://localhost:$TOXIPROXY_ADMIN_PORT/version > /dev/null; then
      echo 'Toxiproxy server ready'
      break
    fi
    echo 'Waiting for Toxiproxy server...'
    sleep 1
  done

  # Create proxy for port 4446
  curl -s -X POST http://localhost:$TOXIPROXY_ADMIN_PORT/proxies -d '{
    \"name\": \"port_4446_proxy\",
    \"listen\": \"0.0.0.0:54446\",
    \"upstream\": \"'\$TARGET_IP':4446\",
    \"enabled\": true
  }' && echo 'Created proxy for 4446' || echo 'Failed to create proxy for 4446'

  # Clear iptables
  iptables -t nat -F 2>/dev/null || true
  iptables -t filter -F FORWARD 2>/dev/null || true

  # Redirect traffic to Toxiproxy
  iptables -t nat -I PREROUTING 1 -p tcp -d \$TARGET_IP --dport 4446 -j DNAT --to-destination 127.0.0.1:54446
  # Block direct connections
  iptables -t filter -I FORWARD 1 -p tcp -d \$TARGET_IP --dport 4446 -j DROP

  # Verify iptables
  echo 'NAT rules:'
  iptables -t nat -L PREROUTING -v -n --line-numbers
  echo 'FILTER rules:'
  iptables -t filter -L FORWARD -v -n --line-numbers
  # Verify listeners
  echo 'Listeners:'
  netstat -tuln | grep ':54446' || echo 'No Toxiproxy listener on 54446'
"

# Start tcpdump
docker exec -d toxiproxy-sidecar-agglayer bash -c "tcpdump -i any 'port 4446 or port 54446' -n > /tmp/tcpdump_4446.log 2>&1"

# Test interception
echo "=== Proxy Interception Test ==="
echo "Initial NAT counters:"
docker exec toxiproxy-sidecar-agglayer iptables -t nat -L PREROUTING -v -n --line-numbers

# Disable proxy
echo "Disabling proxy for 4446..."
docker exec toxiproxy-sidecar-agglayer curl -s -X PUT http://localhost:$TOXIPROXY_ADMIN_PORT/proxies/port_4446_proxy -d '{"enabled": false}'

echo "Testing with proxy disabled (should fail)..."
docker run --rm --network $NETWORK busybox nc -zv $TARGET_IP 4446 && echo "UNEXPECTED: Connection succeeded" || echo "Connection failed as expected"

# Re-enable proxy
echo "Re-enabling proxy for 4446..."
docker exec toxiproxy-sidecar-agglayer curl -s -X PUT http://localhost:$TOXIPROXY_ADMIN_PORT/proxies/port_4446_proxy -d '{"enabled": true}'

echo "Testing with proxy enabled (should succeed)..."
docker run --rm --network $NETWORK busybox nc -zv $TARGET_IP 4446 && echo "Connection succeeded as expected" || echo "UNEXPECTED: Connection failed"

# Sidecar test
echo "Testing from sidecar with proxy enabled..."
docker exec toxiproxy-sidecar-agglayer bash -c "nc -zv $TARGET_IP 4446 && echo 'Connection succeeded as expected' || echo 'UNEXPECTED: Connection failed'"

echo "NAT counters after interception:"
docker exec toxiproxy-sidecar-agglayer iptables -t nat -L PREROUTING -v -n --line-numbers

# Add latency toxic
echo "Adding latency toxic..."
docker exec toxiproxy-sidecar-agglayer curl -s -X POST http://localhost:$TOXIPROXY_ADMIN_PORT/proxies/port_4446_proxy/toxics -d '{"name":"latency_4446","type":"latency","toxicity":1.0,"attributes":{"latency":5000}}'

# Test latency
echo "Testing latency toxic..."
docker run --rm --network $NETWORK busybox sh -c "
  start_time=\$(date +%s)
  nc -zv $TARGET_IP 4446
  result=\$?
  end_time=\$(date +%s)
  duration=\$((end_time - start_time))
  echo \"Connection took \$duration seconds, exit code: \$result\"
  if [ \$result -eq 0 ] && [ \$duration -ge 4 ] && [ \$duration -le 6 ]; then
    echo 'SUCCESS: Connection delayed by ~5s'
  else
    echo 'UNEXPECTED: Expected ~5s delay'
  fi
"

# Stop tcpdump and show output
docker exec toxiproxy-sidecar-agglayer bash -c "pkill tcpdump || true"
echo "tcpdump output:"
docker exec toxiproxy-sidecar-agglayer cat /tmp/tcpdump_4446.log

# Dynamic management commands
echo -e "\n=== Dynamic Management ==="
echo "List proxies: docker exec toxiproxy-sidecar-agglayer curl -s http://localhost:$TOXIPROXY_ADMIN_PORT/proxies | jq"
echo "Remove toxic: docker exec toxiproxy-sidecar-agglayer curl -X DELETE http://localhost:$TOXIPROXY_ADMIN_PORT/proxies/port_4446_proxy/toxics/latency_4446"
echo "Stop sidecar: docker stop toxiproxy-sidecar-agglayer"