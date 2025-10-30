# Chaos-Utils

Based on https://github.com/tylertreat/comcast

Chaos-Utils is a tool for injecting network faults into Docker containers, supporting both Layer 1-4 (L1-L4) faults via Comcast and Layer 7 (L7) faults via Envoy proxy. It uses a sidecar container approach to simulate network issues like latency, packet loss, delays, and aborts without modifying the target application.

## How It Works

### NFTables Rerouting
NFTables intercepts and reroutes network traffic at the kernel level for L7 fault injection:
- A new `inet envoy` table is created in the shared network namespace.
- Chains (`nat_preroute`, `nat_output`, etc.) redirect TCP traffic destined for specified ports (e.g., 4443) to proxy ports (e.g., 54443) via DNAT.
- Incoming and local traffic is transparently forwarded through Envoy, ensuring all connections to the target ports are intercepted.

#### Table and Chain Setup:

- A new nftables table named inet envoy is created in the shared network namespace.
- Chains are added for different hooks (entry points in packet processing):
    - raw_preroute: For raw packet filtering (not heavily used here).
    - nat_preroute: For destination NAT (DNAT) on incoming packets.
    - nat_output: For DNAT on locally generated packets.
    - filter_input: For input filtering (policy accept, so it allows traffic).

#### Traffic Interception Rules:

- For each port in cfg.L7Ports (e.g., 4443, 4444, 4446), rules are added to redirect traffic:
    - Incoming Traffic (nat_preroute chain): Packets destined for targetIP:port (e.g., the target's IP and original port) are DNAT'd to targetIP:proxyPort (e.g., 54443). This catches traffic from other containers on the Docker network.
    - Local Traffic (nat_output chain): Packets originating from within the container (e.g., loopback or self-connections) destined for targetIP:port are DNAT'd to 127.0.0.1:proxyPort. This prevents loops and ensures internal traffic is also intercepted.
- The proxy port is calculated as "5" + port (e.g., 4443 → 54443), matching Envoy's listeners.

### Fault Injection Logic
- **Envoy Proxy**: Runs as a sidecar with dynamically generated config, listening on proxy ports and routing to original destinations.
- **Faults**: Applied via Envoy's fault filter:
  - Delays: Adds fixed delays (e.g., 60s) to requests.
  - Aborts: Returns HTTP 503 errors for a percentage of requests.
- **L1-L4 Faults**: Handled by Comcast, affecting latency, bandwidth, and packet loss on the network interface.
- Traffic is intercepted at L4 and processed at L7, simulating real-world network issues.

## Usage
Run the `comcast-faults.sh` script to start a sidecar container, apply faults, and test them. Example:
- Applies L1-L4 faults (e.g., 100ms latency, 75% packet loss).
- Switches to L7 faults (e.g., 600s delay, 100% abort).
- Verifies with ping, curl, and Envoy stats.

### Clean up
Docker creates a brand new network namespace for the container, which:

- Has no tc rules
- Has no iptables/nftables rules
- Has no conntrack entries
- Has a fresh network stack

But it seems like Kurtosis itself has another layer of networking abstraction - so we'll need to rely on restarting the Kurtosis services using:

```
kurtosis service stop <enclave_name> <service_name>
kurtosis service start <enclave_name> <service_name>
```

## Requirements
- Docker
- jq
- sudo access for nsenter
- Container image: `jhkimqd/chaos-utils:latest`