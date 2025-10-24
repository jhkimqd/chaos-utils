#!/bin/bash
# Script to start chaos utility sidecars (jhkimqd/comcast:latest) for agglayer-- container on kt-op network,
# apply initial Comcast and Toxiproxy faults, and keep containers running for dynamic fault management via docker exec.
set -e

# Variables
NETWORK="kt-op"
NAME_PATTERN="agglayer--"
ADMIN_PORT="8474"   # Toxiproxy admin API port

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

# Get current IP for testing and Toxiproxy
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

# Start Toxiproxy sidecar (L4-L7 faults) and keep it running
# Attach to the same network namespace as the target container
docker run --rm -d \
  --name toxiproxy-sidecar-agglayer \
  --net=container:$TARGET_ID \
  --cap-add=NET_ADMIN --cap-add=NET_RAW \
  jhkimqd/comcast:latest \
  tail -f /dev/null

# Configure Toxiproxy and apply initial faults
docker exec toxiproxy-sidecar-agglayer bash -c "
  # Check if toxiproxy is available
  which toxiproxy-server || echo 'toxiproxy-server not found'
  
  # Start toxiproxy server
  echo 'Starting Toxiproxy server...'
  toxiproxy-server -host 0.0.0.0 -port $ADMIN_PORT > /tmp/toxiproxy.log 2>&1 &
  sleep 5
  
  # Wait for toxiproxy server to be ready
  for i in {1..5}; do
    if curl -s http://localhost:$ADMIN_PORT/version > /dev/null 2>&1; then
      echo 'Toxiproxy server is ready'
      break
    fi
    sleep 2
  done
  
  # Show what ports are actually listening
  echo 'Current listening ports:'
  netstat -tlnp | grep LISTEN
  
  # Create a simple proxy for port 4446 (adjust as needed)
  echo 'Creating admin proxy for port 4446...'
  curl -X POST http://localhost:$ADMIN_PORT/proxies -d '{
    \"name\": \"admin_proxy\",
    \"listen\": \"0.0.0.0:9446\",
    \"upstream\": \"127.0.0.1:4446\",
    \"enabled\": true
  }' 2>/dev/null || echo 'Failed to create proxy'
  
  # Set up simple iptables redirect
  iptables -t nat -A OUTPUT -p tcp --dport 4446 -j REDIRECT --to-port 9446
  
  echo 'Toxiproxy setup complete'
"

# Get Toxiproxy container info for admin access
echo "Toxiproxy container is running in the same network namespace as $TARGET_NAME"

# Verify rules
echo "Checking Comcast tc rules..."
sudo nsenter -t $PID -n tc qdisc show dev $INTERFACE
echo "Checking Toxiproxy proxies..."
docker exec toxiproxy-sidecar-agglayer curl -s http://localhost:$ADMIN_PORT/proxies | jq .

# Test network faults
echo "=== Testing Comcast faults (L1-L4) ==="
echo "Testing ping with packet loss..."
docker run --rm --network $NETWORK busybox ping -c 10 $TARGET_IP

# Stop Comcast fault injection
docker exec comcast-sidecar-agglayer comcast --device=$INTERFACE --stop

echo -e "\n=== Testing Toxiproxy faults (L4-L7) ==="

# Test basic connection
echo "Testing connection to target IP:4446..."
docker run --rm --network $NETWORK curlimages/curl -v -m 10 http://$TARGET_IP:4446/ 2>&1 || echo "Connection failed"

# Add a timeout toxic to test fault injection
echo "Adding timeout toxic to admin proxy..."
docker exec toxiproxy-sidecar-agglayer curl -X POST http://localhost:$ADMIN_PORT/proxies/admin_proxy/toxics -d '{"name":"admin_timeout","type":"timeout","toxicity":1.0,"attributes":{"timeout":0}}'

echo "Testing with 100% timeout toxic (should fail immediately):"
docker run --rm --network $NETWORK curlimages/curl -v -m 5 http://$TARGET_IP:4446/ 2>&1 || echo "Connection failed as expected due to timeout toxic"

# Remove the toxic for further testing
echo "Removing timeout toxic..."
docker exec toxiproxy-sidecar-agglayer curl -X DELETE http://localhost:$ADMIN_PORT/proxies/admin_proxy/toxics/admin_timeout

echo "Testing after removing toxic (should work again):"
docker run --rm --network $NETWORK curlimages/curl -v -m 10 http://$TARGET_IP:4446/ 2>&1 || echo "Connection still failing"

# Instructions for dynamic fault management
echo -e "\nTo manage faults dynamically:"
echo "1. Comcast (L1-L4):"
echo "   Add/Update: docker exec comcast-sidecar-agglayer comcast --device=$INTERFACE --latency=100 --packet-loss=20% --target-proto=tcp,udp,icmp"
echo "   Remove: docker exec comcast-sidecar-agglayer comcast --device=$INTERFACE --stop"

echo "2. Toxiproxy (L4-L7):"
echo "   List all proxies: docker exec toxiproxy-sidecar-agglayer curl http://localhost:$ADMIN_PORT/proxies"
echo "   List toxics: docker exec toxiproxy-sidecar-agglayer curl http://localhost:$ADMIN_PORT/proxies/admin_proxy/toxics"
echo "   Add timeout toxic: docker exec toxiproxy-sidecar-agglayer curl -X POST http://localhost:$ADMIN_PORT/proxies/admin_proxy/toxics -d '{\"name\":\"timeout\",\"type\":\"timeout\",\"toxicity\":0.9,\"attributes\":{\"timeout\":0}}'"
echo "   Add bandwidth limit: docker exec toxiproxy-sidecar-agglayer curl -X POST http://localhost:$ADMIN_PORT/proxies/admin_proxy/toxics -d '{\"name\":\"bandwidth\",\"type\":\"bandwidth\",\"toxicity\":1.0,\"attributes\":{\"rate\":1000}}'"
echo "   Remove toxic: docker exec toxiproxy-sidecar-agglayer curl -X DELETE http://localhost:$ADMIN_PORT/proxies/admin_proxy/toxics/timeout"

echo "3. Test commands:"
echo "   Test endpoint: docker run --rm --network $NETWORK curlimages/curl -v http://$TARGET_IP:4446/"

echo "4. Stop sidecars:"
echo "   docker stop comcast-sidecar-agglayer toxiproxy-sidecar-agglayer"