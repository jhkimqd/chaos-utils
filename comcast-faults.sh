#!/bin/bash
# Script to start chaos utility sidecars (jhkimqd/chaos-utils:latest) for agglayer-- container on kt-op network,
# testing both L1-L4 faults via Comcast and L7 faults via Envoy.
set -e

docker rm -f chaos-utils-sidecar-agglayer

# Variables
NETWORK="kt-op"
NAME_PATTERN="agglayer--"

# Find the agglayer-- container name and ID
TARGET_NAME=$(docker network inspect $NETWORK | jq -r '.[] | .Containers | to_entries[] | select(.value.Name | test("'"$NAME_PATTERN"'")) | .value.Name')
if [[ -z "$TARGET_NAME" ]]; then
  echo "Error: No container found with name matching $NAME_PATTERN on network $NETWORK"
  exit 1
fi
echo "Target container name: $TARGET_NAME"

TARGET_ID=$(docker network inspect $NETWORK | jq -r '.[] | .Containers | to_entries[] | select(.value.Name | test("'"$NAME_PATTERN"'")) | .key')
if [[ -z "$TARGET_ID" ]]; then
  echo "Error: Could not get container ID for $TARGET_NAME"
  exit 1
fi
echo "Target container ID: $TARGET_ID"

TARGET_IP=$(docker network inspect $NETWORK | jq -r '.[] | .Containers | to_entries[] | select(.value.Name | test("'"$NAME_PATTERN"'")) | .value.IPv4Address | split("/")[0]')
if [[ -z "$TARGET_IP" ]]; then
  echo "Error: Could not get IP for $TARGET_NAME"
  exit 1
fi
echo "Target IP: $TARGET_IP"

# Clean up existing rules
PID=$(docker inspect -f '{{.State.Pid}}' $TARGET_ID)
if [[ -z "$PID" ]]; then
  echo "Error: Could not get PID for $TARGET_NAME"
  exit 1
fi
sudo nsenter -t $PID -n tc qdisc del dev eth0 root 2>/dev/null
sudo nsenter -t $PID -n iptables -F 2>/dev/null

# Check for network interface
INTERFACE=$(sudo nsenter -t $PID -n ip link | grep -oP '^\d+: \Keth\d+')
if [[ -z "$INTERFACE" ]]; then
  echo "Error: No eth interface found in container's namespace"
  sudo nsenter -t $PID -n ip link
  exit 1
fi
echo "Using interface: $INTERFACE"

# Start chaos-utils sidecar (L1-L4 and L7 faults) and keep it running
docker run --rm -d \
  --name chaos-utils-sidecar-agglayer \
  --net=container:$TARGET_ID \
  --cap-add=NET_ADMIN --cap-add=NET_RAW \
  --sysctl net.ipv4.conf.all.rp_filter=0 \
  jhkimqd/chaos-utils:latest \
  tail -f /dev/null

# Apply initial Comcast faults (no --target-port to affect ICMP)
docker exec chaos-utils-sidecar-agglayer comcast --device=$INTERFACE --latency=100 --target-bw=10000 --default-bw=1000000 --packet-loss=75% --target-proto=tcp,udp,icmp --target-port=4443,4444,4446

# Verify rules
echo "Checking Comcast tc rules..."
sudo nsenter -t $PID -n tc qdisc show dev $INTERFACE

# Test network faults
echo "=== Testing Comcast faults (L1-L4) ==="
echo "Testing ping with packet loss..."
docker run --rm --network $NETWORK busybox ping -c 10 $TARGET_IP

# Stop Comcast fault injection
docker exec chaos-utils-sidecar-agglayer comcast --device=$INTERFACE --stop

# Apply L7 faults via Envoy (no --target-container needed, sidecar shares namespace)
docker exec chaos-utils-sidecar-agglayer comcast --target-ip=$TARGET_IP --l7-http-ports=4444,4446 --l7-grpc-ports=4443 --l7-delay=6s --l7-abort-percent=100

# Check envoy filters
docker exec chaos-utils-sidecar-agglayer curl -s http://localhost:9901/config_dump | jq '.configs[0].bootstrap.static_resources.listeners[]'

# Verify Envoy is running and nftables rules
echo "Checking Envoy process..."
docker exec chaos-utils-sidecar-agglayer ps aux | grep envoy
echo "Checking all listening ports..."
docker exec chaos-utils-sidecar-agglayer ss -tuln | grep -E ':(4443|4444|4446|54443|54444|54446)'
echo "Checking nftables rules..."
docker exec chaos-utils-sidecar-agglayer nft list ruleset

# Test network delays
echo ""
echo "=== Testing HTTP-level faults ==="
echo ""
echo "Making HTTP request to port 4443..."
time docker run --rm --network $NETWORK curlimages/curl:latest -v --max-time 10 http://$TARGET_IP:4443/ 2>&1 | grep -E "(HTTP|503|Connection|delay)"
echo ""
echo "Checking Envoy stats to verify traffic is being proxied..."
docker exec chaos-utils-sidecar-agglayer curl -s http://localhost:9901/stats | grep -E "(downstream_rq_total|upstream_rq_total|fault)" | head -20

# Stop L7 faults
docker exec chaos-utils-sidecar-agglayer comcast --target-ip=$TARGET_IP --l7-http-ports=4444,4446 --l7-grpc-ports=4443 --stop

# Kill all Envoy processes
docker exec chaos-utils-sidecar-agglayer pkill -9 envoy 2>/dev/null || true
sleep 2

# Flush ALL network rules in order
echo "Flushing all network rules..."
docker exec chaos-utils-sidecar-agglayer nft flush ruleset 2>/dev/null || true
docker exec chaos-utils-sidecar-agglayer iptables -t nat -F 2>/dev/null || true
docker exec chaos-utils-sidecar-agglayer iptables -t mangle -F 2>/dev/null || true
docker exec chaos-utils-sidecar-agglayer iptables -t filter -F 2>/dev/null || true

# Remove tc rules
docker exec chaos-utils-sidecar-agglayer tc qdisc del dev $INTERFACE root 2>/dev/null || true

# Verify cleanup
echo ""
echo "Verifying cleanup in sidecar namespace..."
echo "TC rules:"
docker exec chaos-utils-sidecar-agglayer tc qdisc show dev $INTERFACE
echo "Envoy processes:"
docker exec chaos-utils-sidecar-agglayer ps aux | grep envoy || echo "No Envoy running"
echo "nftables:"
docker exec chaos-utils-sidecar-agglayer nft list ruleset || echo "No nft rules"

# Remove sidecar to ensure clean state
echo "Removing sidecar container..."
docker rm -f chaos-utils-sidecar-agglayer

echo ""
echo "Cleanup complete! Target container should be functional now."
echo "To reapply faults, restart the target container for a truly clean slate:"
echo " kurtosis service stop op agglayer $TARGET_NAME"
echo " kurtosis service start op agglayer $TARGET_NAME"
# kurtosis service stop op agglayer
# kurtosis service start op agglayer