#!/bin/bash
# Script to start chaos utility sidecars (jhkimqd/chaos-utils:latest) for agglayer-- container on kt-op network,
# testing both L3,L4 faults via Comcast and L7 faults via Envoy.
set -e

docker rm -f chaos-utils-sidecar-agglayer

##############################################################
# Set up container detection
##############################################################
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

##############################################################
# Start up chaos-utils container
##############################################################
# Start chaos-utils sidecar (L1-L4 and L7 faults) and keep it running
docker run --rm -d \
  --name chaos-utils-sidecar-agglayer \
  --net=container:$TARGET_ID \
  --cap-add=NET_ADMIN --cap-add=NET_RAW \
  --sysctl net.ipv4.conf.all.rp_filter=0 \
  jhkimqd/chaos-utils:latest \
  tail -f /dev/null

##############################################################
# Comcast L3,L4 faults injection + testing
##############################################################
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

##############################################################
# Envoy L7 faults injection + testing
##############################################################
# Apply L7 faults via Envoy (no --target-container needed, sidecar shares namespace)
# NOTE: For gRPC, only delay works reliably. Abort has limitations due to how gRPC handles errors over HTTP/2.
# For gRPC error injection, use L1-L4 faults (packet loss, connection drops) instead.
docker exec chaos-utils-sidecar-agglayer comcast --target-ip=$TARGET_IP --l7-http-ports=4444,4446 --l7-http-status=404 --l7-abort-percent=50 --l7-grpc-status=15 --l7-grpc-ports=4443 --l7-delay=2s

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
echo "Making HTTP request to port 4444 (should show 6s delay + 503 error)..."
time docker run --rm --network $NETWORK curlimages/curl:latest -v --max-time 5 http://$TARGET_IP:4444/ 2>&1 | grep -E "(HTTP|503|Connection|delay|timeout)"
echo "Making HTTP request to port 4446 (should show 6s delay + 503 error)..."
time docker run --rm --network $NETWORK curlimages/curl:latest -v --max-time 5 http://$TARGET_IP:4446/ 2>&1 | grep -E "(HTTP|503|Connection|delay|timeout)"
echo "For gRPC error injection, use connection-level faults instead (packet loss, etc.)"
time docker run --rm --network $NETWORK fullstorydev/grpcurl:latest -plaintext -max-time 10 $TARGET_IP:4443 list 2>&1

# Stop L7 faults
docker exec chaos-utils-sidecar-agglayer comcast --target-ip=$TARGET_IP --l7-http-ports=4444,4446 --l7-grpc-ports=4443 --stop

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
docker rm -f chaos-utils-sidecar-agglayer

# Restart agglayer service
kurtosis service stop op agglayer
kurtosis service start op agglayer