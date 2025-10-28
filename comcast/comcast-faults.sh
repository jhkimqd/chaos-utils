#!/bin/bash
# Script to start chaos utility sidecars (jhkimqd/comcast:latest) for agglayer-- container on kt-op network,
set -e

docker rm -f comcast-sidecar-agglayer

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

# Start Comcast sidecar (L1-L4 faults) and keep it running
docker run --rm -d \
  --name comcast-sidecar-agglayer \
  --net=container:$TARGET_ID \
  --cap-add=NET_ADMIN --cap-add=NET_RAW \
  jhkimqd/comcast:latest \
  tail -f /dev/null

# Apply initial Comcast faults (no --target-port to affect ICMP)
docker exec comcast-sidecar-agglayer comcast --device=$INTERFACE --latency=100 --target-bw=10000 --default-bw=1000000 --packet-loss=75% --target-proto=tcp,udp,icmp --target-port=4443,4444,4446
# comcast --device=eth0 --latency=250 --target-bw=1000 --default-bw=1000000 --packet-loss=75% --target-proto=tcp,udp,icmp --target-port=4443,4444,4446

# Verify rules
echo "Checking Comcast tc rules..."
sudo nsenter -t $PID -n tc qdisc show dev $INTERFACE

# Test network faults
echo "=== Testing Comcast faults (L1-L4) ==="
echo "Testing ping with packet loss..."
docker run --rm --network $NETWORK busybox ping -c 10 $TARGET_IP

# Stop Comcast fault injection
docker exec comcast-sidecar-agglayer comcast --device=$INTERFACE --stop
