package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/tylertreat/comcast/throttler"
)

const version = "1.0.0"

func main() {
	// TODO: Add support for other options like packet reordering, duplication, etc.
	var (
		device      = flag.String("device", "", "Interface (device) to use (defaults to eth0 where applicable)")
		stop        = flag.Bool("stop", false, "Stop packet controls")
		latency     = flag.Int("latency", -1, "Latency to add in ms")
		targetbw    = flag.Int("target-bw", -1, "Target bandwidth limit in kbit/s (slow-lane)")
		defaultbw   = flag.Int("default-bw", -1, "Default bandwidth limit in kbit/s (fast-lane)")
		packetLoss  = flag.String("packet-loss", "0", "Packet loss percentage (e.g. 0.1%)")
		targetaddr  = flag.String("target-addr", "", "Target addresses (optional, auto-detected if using --net=container). Format: IP or CIDR (e.g. 10.0.0.1 or 10.0.0.0/24)")
		targetport  = flag.String("target-port", "", "Target port(s) (e.g. 80 or 1:65535 or 22,80,443,1000:1010)")
		targetproto = flag.String("target-proto", "tcp,udp,icmp", "Target protocol TCP/UDP (e.g. tcp or tcp,udp or icmp)")
		dryrun      = flag.Bool("dry-run", false, "Specifies whether or not to actually commit the rule changes")
		// New L7 flags
		l7delay      = flag.String("l7-delay", "", "L7 fixed delay (e.g. 2s) for HTTP/gRPC requests")
		l7abort      = flag.Int("l7-abort-percent", 0, "L7 abort percentage (e.g. 75) for HTTP/gRPC requests")
		l7httpstatus = flag.Int("l7-http-status", 503, "L7 abort HTTP status code (e.g. 429, default 503)")
		l7grpcstatus = flag.Int("l7-grpc-status", 0, "L7 abort gRPC status code (e.g. 14 for UNAVAILABLE, 0 to disable)")
		l7http       = flag.String("l7-http-ports", "", "Target HTTP ports for L7 interception (e.g. 4444,4446)") // New flag
		l7grpcp      = flag.String("l7-grpc-ports", "", "Target gRPC ports for L7 interception (e.g. 4443)")      // New flag
		//icmptype  = flag.String("icmp-type", "", "icmp message type (e.g. reply or reply,request)") //TODO: Maybe later :3
		vers = flag.Bool("version", false, "Print Comcast's version")
	)
	flag.Parse()

	if *vers {
		fmt.Printf("Comcast version %s\n", version)
		return
	}

	targetIPv4, targetIPv6 := parseAddrs(*targetaddr)

	// Auto-detect container IP if not specified
	autoDetectedIP := ""
	if *targetaddr == "" {
		autoDetectedIP = getContainerIP(*device)
		if autoDetectedIP != "" {
			fmt.Printf("Auto-detected container IP: %s\n", autoDetectedIP)
			// Parse the auto-detected IP
			ipv4, ipv6 := parseAddrs(autoDetectedIP)
			targetIPv4 = ipv4
			targetIPv6 = ipv6
		}
	}

	throttler.Run(&throttler.Config{
		Device:           *device,
		Stop:             *stop,
		Latency:          *latency,
		TargetBandwidth:  *targetbw,
		DefaultBandwidth: *defaultbw,
		PacketLoss:       parseLoss(*packetLoss),
		TargetIps:        targetIPv4,
		TargetIps6:       targetIPv6,
		TargetPorts:      parsePorts(*targetport),
		TargetProtos:     parseProtos(*targetproto),
		DryRun:           *dryrun,
		// New L7 fields
		L7Delay:        *l7delay,
		L7AbortPercent: *l7abort,
		L7HttpStatus:   *l7httpstatus,
		L7GrpcStatus:   *l7grpcstatus,
		L7HttpPorts:    parsePorts(*l7http),
		L7GrpcPorts:    parsePorts(*l7grpcp),
		TargetIP:       autoDetectedIP, // Use auto-detected IP for both L3/L4 and L7
	})
}

// getContainerIP auto-detects the container's IP address from the network interface
func getContainerIP(device string) string {
	if device == "" {
		device = "eth0"
	}

	// Try using standard approach with net package
	ifaces, err := net.Interfaces()
	if err != nil {
		fmt.Printf("Error listing network interfaces: %v\n", err)
		return ""
	}

	for _, iface := range ifaces {
		if iface.Name == device {
			addrs, err := iface.Addrs()
			if err != nil {
				fmt.Printf("Error getting addresses for interface %s: %v\n", device, err)
				continue
			}
			for _, addr := range addrs {
				if ipnet, ok := addr.(*net.IPNet); ok {
					if ipv4 := ipnet.IP.To4(); ipv4 != nil && !ipv4.IsLoopback() {
						fmt.Printf("Detected IP %s from interface %s\n", ipv4.String(), device)
						return ipv4.String()
					}
				}
			}
		}
	}

	fmt.Printf("No valid IP found on interface %s\n", device)
	// Print available interfaces for debugging
	fmt.Println("Available interfaces:")
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		fmt.Printf("  %s: %v\n", iface.Name, addrs)
	}
	return ""
}

func parseLoss(loss string) float64 {
	val := loss
	if strings.Contains(loss, "%") {
		val = loss[:len(loss)-1]
	}
	l, err := strconv.ParseFloat(val, 64)
	if err != nil {
		fmt.Println("Incorrectly specified packet loss:", loss)
		os.Exit(1)
	}
	return l
}

func parseAddrs(addrs string) ([]string, []string) {
	adrs := strings.Split(addrs, ",")
	parsedIPv4 := []string{}
	parsedIPv6 := []string{}

	if addrs != "" {
		for _, adr := range adrs {
			ip := net.ParseIP(adr)
			if ip != nil {
				if ip.To4() != nil {
					parsedIPv4 = append(parsedIPv4, adr)
				} else {
					parsedIPv6 = append(parsedIPv6, adr)
				}
			} else { //Not a valid single IP, could it be a CIDR?
				parsedIP, net, err := net.ParseCIDR(adr)
				if err == nil {
					if parsedIP.To4() != nil {
						parsedIPv4 = append(parsedIPv4, net.String())
					} else {
						parsedIPv6 = append(parsedIPv6, net.String())
					}
				} else {
					fmt.Println("Incorrectly specified target IP or CIDR:", adr)
					os.Exit(1)
				}
			}
		}
	}

	return parsedIPv4, parsedIPv6
}

func parsePorts(ports string) []string {
	prts := strings.Split(ports, ",")
	parsed := []string{}

	if ports != "" {
		for _, prt := range prts {
			if strings.Contains(prt, ":") {
				if validRange(prt) {
					parsed = append(parsed, prt)
				} else {
					fmt.Println("Incorrectly specified port range:", prt)
					os.Exit(1)
				}
			} else { //Isn't a range, check if just a single port
				if validPort(prt) {
					parsed = append(parsed, prt)
				} else {
					fmt.Println("Incorrectly specified port:", prt)
					os.Exit(1)
				}
			}
		}
	}

	return parsed
}

func parsePort(port string) int {
	prt, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}

	return prt
}

func validPort(port string) bool {
	prt := parsePort(port)
	return prt > 0 && prt < 65536
}

func validRange(ports string) bool {
	pr := strings.Split(ports, ":")

	if len(pr) == 2 {
		if !validPort(pr[0]) || !validPort(pr[1]) {
			return false
		}

		if portHigher(pr[0], pr[1]) {
			return false
		}
	} else {
		return false
	}

	return true
}

func portHigher(prt1, prt2 string) bool {
	p1 := parsePort(prt1)
	p2 := parsePort(prt2)

	return p1 > p2
}

func parseProtos(protos string) []string {
	ptcs := strings.Split(protos, ",")
	parsed := []string{}

	if protos != "" {
		for _, ptc := range ptcs {
			p := strings.ToLower(ptc)
			if p == "udp" ||
				p == "tcp" ||
				p == "icmp" {
				parsed = append(parsed, p)
			} else {
				fmt.Println("Incorrectly specified protocol:", p)
				os.Exit(1)
			}
		}
	}

	return parsed
}
