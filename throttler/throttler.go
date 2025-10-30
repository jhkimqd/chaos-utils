package throttler

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	linux           = "linux"
	darwin          = "darwin"
	freebsd         = "freebsd"
	windows         = "windows"
	checkOSXVersion = "sw_vers -productVersion"
	ipfw            = "ipfw"
	pfctl           = "pfctl"
)

// Config specifies options for configuring packet filter rules.
type Config struct {
	Device           string
	Stop             bool
	Latency          int
	TargetBandwidth  int
	DefaultBandwidth int
	PacketLoss       float64
	TargetIps        []string
	TargetIps6       []string
	TargetPorts      []string
	TargetProtos     []string
	DryRun           bool
	// New L7 fields
	L7Delay         string
	L7AbortPercent  int
	L7AbortStatus   int
	L7GrpcStatus    int
	L7HttpPorts     []string
	L7GrpcPorts     []string
	TargetContainer string
	TargetIP        string
}

type throttler interface {
	setup(*Config) error
	teardown(*Config) error
	exists() bool
	check() string
}

type commander interface {
	execute(string) error
	executeGetLines(string) ([]string, error)
	commandExists(string) bool
}

type dryRunCommander struct{}

type shellCommander struct{}

var dry bool

func setup(t throttler, cfg *Config) {
	if t.exists() {
		fmt.Println("It looks like the packet rules are already setup")
		os.Exit(1)
	}

	if err := t.setup(cfg); err != nil {
		fmt.Println("I couldn't setup the packet rules:", err.Error())
		os.Exit(1)
	}

	fmt.Println("Packet rules setup...")
	fmt.Printf("Run `%s` to double check\n", t.check())
	fmt.Printf("Run `%s --device %s --stop` to reset\n", os.Args[0], cfg.Device)
}

func teardown(t throttler, cfg *Config) {
	if !t.exists() {
		fmt.Println("It looks like the packet rules aren't setup")
		os.Exit(1)
	}

	if err := t.teardown(cfg); err != nil {
		fmt.Println("Failed to stop packet controls")
		os.Exit(1)
	}

	fmt.Println("Packet rules stopped...")
	fmt.Printf("Run `%s` to double check\n", t.check())
	fmt.Printf("Run `%s` to start\n", os.Args[0])
}

// Run executes the packet filter operation, either setting it up or tearing
// it down.
func Run(cfg *Config) {
	dry = cfg.DryRun
	var t throttler
	var c commander

	if cfg.DryRun {
		c = &dryRunCommander{}
	} else {
		c = &shellCommander{}
	}

	switch runtime.GOOS {
	case freebsd:
		if cfg.Device == "" {
			fmt.Println("Device not specified, unable to default to eth0 on FreeBSD.")
			os.Exit(1)
		}

		t = &ipfwThrottler{c}
	case darwin:
		// Avoid OS version pinning and choose based on what's available
		if c.commandExists(pfctl) {
			t = &pfctlThrottler{c}
		} else if c.commandExists(ipfw) {
			t = &ipfwThrottler{c}
		} else {
			fmt.Println("Could not determine an appropriate firewall tool for OSX (tried pfctl, ipfw), exiting")
			os.Exit(1)
		}

		if cfg.Device == "" {
			cfg.Device = "eth0"
		}

	case linux:
		if cfg.Device == "" {
			cfg.Device = "eth0"
		}

		t = &tcThrottler{c}
	default:
		fmt.Printf("I don't support your OS: %s\n", runtime.GOOS)
		os.Exit(1)
	}

	if !cfg.Stop {
		setup(t, cfg)
		if len(cfg.L7HttpPorts) > 0 || len(cfg.L7GrpcPorts) > 0 {
			setupL7(cfg)
		}
	} else {
		teardown(t, cfg)
		if len(cfg.L7HttpPorts) > 0 || len(cfg.L7GrpcPorts) > 0 {
			teardownL7(cfg)
		}
	}
}

func (c *dryRunCommander) execute(cmd string) error {
	fmt.Println(cmd)
	return nil
}

func (c *dryRunCommander) executeGetLines(cmd string) ([]string, error) {
	fmt.Println(cmd)
	return []string{}, nil
}

func (c *dryRunCommander) commandExists(cmd string) bool {
	return true
}

func (c *shellCommander) execute(cmd string) error {
	fmt.Println(cmd)
	return exec.Command("/bin/sh", "-c", cmd).Run()
}

func (c *shellCommander) executeGetLines(cmd string) ([]string, error) {
	lines := []string{}
	child := exec.Command("/bin/sh", "-c", cmd)

	out, err := child.StdoutPipe()
	if err != nil {
		return []string{}, err
	}

	err = child.Start()
	if err != nil {
		return []string{}, err
	}

	scanner := bufio.NewScanner(out)

	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return []string{}, errors.New(fmt.Sprint("Error reading standard input:", err))
	}

	err = child.Wait()
	if err != nil {
		return []string{}, err
	}

	return lines, nil
}

func (c *shellCommander) commandExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

// setupL7 generates Envoy config, runs the sidecar, and sets up nftables
func setupL7(cfg *Config) {
	if cfg.TargetIP == "" {
		fmt.Println("Error: --target-ip required for L7 faults")
		os.Exit(1)
	}
	// Add sanity check for L7AbortStatus
	if cfg.L7AbortStatus < 200 || cfg.L7AbortStatus >= 600 {
		fmt.Printf("Error: Invalid L7 abort status %d. Must be >= 200 and < 600\n", cfg.L7AbortStatus)
		os.Exit(1)
	}
	// Add sanity check for L7GrpcStatus
	if cfg.L7GrpcStatus != 0 && (cfg.L7GrpcStatus < 0 || cfg.L7GrpcStatus > 16) {
		fmt.Printf("Error: Invalid L7 gRPC abort status %d. Must be 0-16 or 0 to disable\n", cfg.L7GrpcStatus)
		os.Exit(1)
	}

	targetIP := cfg.TargetIP
	interfaceName := getContainerInterface("")
	if interfaceName == "" {
		fmt.Println("Error: Failed to get interface in current namespace")
		os.Exit(1)
	}

	configFile := generateEnvoyConfig(cfg, targetIP)
	// Validate Envoy config
	configContent, _ := os.ReadFile(configFile)
	err := exec.Command("envoy", "--config-yaml", string(configContent), "--mode", "validate").Run()
	if err != nil {
		fmt.Println("Envoy config validation failed:", err)
		os.Exit(1)
	}
	runEnvoySidecar(cfg, configFile, targetIP, interfaceName)
	setupL7Interception(cfg, targetIP, interfaceName)
	startTcpdump(cfg)

	fmt.Println("L7 faults setup via Envoy sidecar")
}

// teardownL7 stops Envoy and cleans up
func teardownL7(cfg *Config) {
	fmt.Println("Tearing down L7 faults...")

	// Step 1: Kill Envoy aggressively
	fmt.Println("Stopping Envoy process...")
	for i := 0; i < 3; i++ {
		exec.Command("pkill", "-9", "envoy").Run()
		time.Sleep(500 * time.Millisecond)

		// Check if envoy is still running
		checkCmd := exec.Command("sh", "-c", "ps aux | grep '[e]nvoy'")
		if output, _ := checkCmd.Output(); len(output) == 0 {
			fmt.Println("Envoy stopped successfully")
			break
		}
		if i == 2 {
			fmt.Println("Warning: Envoy may still be running")
		}
	}

	// Step 2: Flush iptables (both PREROUTING and OUTPUT)
	fmt.Println("Flushing iptables rules...")
	for _, table := range []string{"nat", "mangle", "filter"} {
		cmd := exec.Command("iptables", "-t", table, "-F")
		if err := cmd.Run(); err != nil {
			fmt.Printf("Warning: iptables -t %s -F failed: %v\n", table, err)
		}
	}

	// Step 3: Flush nftables (if any were created)
	fmt.Println("Flushing nftables rules...")
	if err := exec.Command("nft", "flush", "ruleset").Run(); err != nil {
		fmt.Println("Warning: nft flush failed:", err)
	}

	// Step 4: Clear connection tracking
	fmt.Println("Clearing connection tracking state...")
	if err := exec.Command("conntrack", "-F").Run(); err != nil {
		fmt.Println("Warning: conntrack flush failed (may not be critical):", err)
	}

	// Step 5: Clean up temp files
	fmt.Println("Removing Envoy config files...")
	exec.Command("sh", "-c", "rm -f /tmp/envoy-config-*.yaml /tmp/envoy.log").Run()

	fmt.Println("L7 faults torn down")
}

// Helper: Get interface
func getContainerInterface(container string) string {
	cmd := exec.Command("ip", "link")
	output, err := cmd.Output()
	if err != nil {
		fmt.Println("Error getting interface:", err)
		return ""
	}
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "eth") {
			fields := strings.Fields(line)
			if len(fields) > 1 {
				return strings.TrimSuffix(fields[1], ":")
			}
		}
	}
	fmt.Println("No eth interface found")
	return ""
}

// generateEnvoyConfig creates the YAML config file
func generateEnvoyConfig(cfg *Config, targetIP string) string {
	config := fmt.Sprintf(`
static_resources:
  listeners:
%s
  clusters:
%s
admin:
  address:
    socket_address:
      address: 0.0.0.0
      port_value: 9901
`, generateListeners(cfg, targetIP), generateClusters(cfg, targetIP))

	file, _ := os.CreateTemp("", "envoy-config-*.yaml")
	file.WriteString(config)
	file.Close()
	return file.Name()
}

// generateListeners with configurable faults
func generateListeners(cfg *Config, targetIP string) string {
	var listeners strings.Builder

	// Generate listeners for HTTP ports
	for _, port := range cfg.L7HttpPorts {
		abortConfig := fmt.Sprintf(`
                http_status: %d`, cfg.L7AbortStatus)
		listeners.WriteString(fmt.Sprintf(`
  - name: listener_%s
    address:
      socket_address:
        address: 0.0.0.0
        port_value: 5%s
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: proxy_%s
          route_config:
            name: local_route
            virtual_hosts:
            - name: backend
              domains: ["*"]
              routes:
              - match:
                  prefix: "/"
                route:
                  cluster: cluster_%s
          http_filters:
          - name: envoy.filters.http.fault
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.fault.v3.HTTPFault
              delay:
                fixed_delay: %s
                percentage:
                  numerator: 100
                  denominator: HUNDRED
              abort:%s
                percentage:
                  numerator: %d
                  denominator: HUNDRED
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
`, port, port, port, port, cfg.L7Delay, abortConfig, cfg.L7AbortPercent))
	}

	// Generate listeners for gRPC ports
	for _, port := range cfg.L7GrpcPorts {
		abortConfig := fmt.Sprintf(`
                grpc_status: %d`, cfg.L7GrpcStatus)
		listeners.WriteString(fmt.Sprintf(`
  - name: listener_%s
    address:
      socket_address:
        address: 0.0.0.0
        port_value: 5%s
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: proxy_%s
          route_config:
            name: local_route
            virtual_hosts:
            - name: backend
              domains: ["*"]
              routes:
              - match:
                  prefix: "/"
                route:
                  cluster: cluster_%s
          http_filters:
          - name: envoy.filters.http.fault
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.fault.v3.HTTPFault
              delay:
                fixed_delay: %s
                percentage:
                  numerator: 100
                  denominator: HUNDRED
              abort:%s
                percentage:
                  numerator: %d
                  denominator: HUNDRED
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
`, port, port, port, port, cfg.L7Delay, abortConfig, cfg.L7AbortPercent))
	}

	return listeners.String()
}

func generateClusters(cfg *Config, targetIP string) string {
	var clusters strings.Builder
	// Combine HTTP and gRPC ports for clusters
	allPorts := append(cfg.L7HttpPorts, cfg.L7GrpcPorts...)
	for _, port := range allPorts {
		// Connect to 127.0.0.1 to avoid nftables DNAT loop
		// The real service is listening on the same namespace
		clusters.WriteString(fmt.Sprintf(`
  - name: cluster_%s
    type: STATIC
    lb_policy: ROUND_ROBIN
    load_assignment:
      cluster_name: cluster_%s
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: 127.0.0.1
                port_value: %s
`, port, port, port))
	}
	return clusters.String()
}

// runEnvoySidecar runs Envoy as a subprocess in the sidecar
func runEnvoySidecar(cfg *Config, configFile, targetIP, interfaceName string) {
	configContent, _ := os.ReadFile(configFile)

	fmt.Println("Starting Envoy proxy...")

	// Run Envoy in the background using nohup to keep it running
	// Write config to a file instead of passing via --config-yaml to avoid argument length issues
	cmd := exec.Command("sh", "-c", fmt.Sprintf("nohup envoy --config-yaml '%s' > /tmp/envoy.log 2>&1 &", string(configContent)))

	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Println("Error starting Envoy:", err)
		fmt.Println("Output:", string(output))
		os.Exit(1)
	}

	fmt.Println("Envoy started in background")

	// Wait for Envoy to be ready
	fmt.Println("Waiting for Envoy listeners to be ready...")
	for i := 0; i < 30; i++ {
		time.Sleep(1 * time.Second)

		// Check if Envoy process is running and not defunct
		psCmd := exec.Command("sh", "-c", "ps aux | grep '[e]nvoy' | grep -v defunct")
		psOutput, _ := psCmd.Output()
		if len(psOutput) == 0 {
			// Check envoy logs for errors
			logCmd := exec.Command("sh", "-c", "tail -50 /tmp/envoy.log 2>/dev/null || echo 'No logs'")
			logOutput, _ := logCmd.Output()
			fmt.Println("Envoy process not found or defunct. Logs:")
			fmt.Println(string(logOutput))

			if i < 5 {
				fmt.Printf("Retrying... (%d/30)\n", i+1)
				continue
			}
			fmt.Println("Error: Envoy failed to start properly")
			os.Exit(1)
		}

		// Check if any of our proxy ports are listening
		checkCmd := exec.Command("sh", "-c", "ss -tuln | grep ':5'")
		output, err := checkCmd.Output()
		if err == nil && len(output) > 0 {
			outputStr := string(output)
			allReady := true
			allPorts := append(cfg.L7HttpPorts, cfg.L7GrpcPorts...)
			for _, port := range allPorts {
				proxyPort := "5" + port
				if !strings.Contains(outputStr, ":"+proxyPort) {
					allReady = false
					break
				}
			}
			if allReady {
				fmt.Println("All Envoy listeners are ready!")
				return
			}
		}

		if i%5 == 4 {
			fmt.Printf("Still waiting... (%d/30)\n", i+1)
		}
	}

	// Print logs for debugging
	logCmd := exec.Command("sh", "-c", "tail -100 /tmp/envoy.log 2>/dev/null || echo 'No logs'")
	logOutput, _ := logCmd.Output()
	fmt.Println("Envoy logs after timeout:")
	fmt.Println(string(logOutput))

	fmt.Println("Warning: Could not verify all Envoy listeners are ready, continuing anyway...")
}

// validateEnvoyConfig validates the Envoy config
func validateEnvoyConfig(config string) bool {
	cmd := exec.Command("envoy", "--config-yaml", config, "--mode", "validate")
	err := cmd.Run()
	return err == nil
}

// setupL7Interception adds rules for interception
func setupL7Interception(cfg *Config, targetIP, interfaceName string) {
	fmt.Println("Setting up iptables rules for L7 interception...")

	// Flush any existing rules
	exec.Command("iptables", "-t", "nat", "-F").Run()
	exec.Command("nft", "flush", "ruleset").Run()

	// Use iptables for BOTH prerouting and output
	for _, port := range append(cfg.L7HttpPorts, cfg.L7GrpcPorts...) {
		proxyPort := "5" + port

		// PREROUTING: Intercept incoming traffic from other containers
		fmt.Printf("Setting up iptables PREROUTING for port %s -> %s\n", port, proxyPort)
		if err := exec.Command("iptables", "-t", "nat", "-A", "PREROUTING",
			"-d", targetIP, "-p", "tcp", "--dport", port,
			"-j", "DNAT", "--to-destination", targetIP+":"+proxyPort).Run(); err != nil {
			fmt.Printf("Error adding iptables PREROUTING rule for port %s: %v\n", port, err)
			os.Exit(1)
		}

		// OUTPUT: Intercept local traffic from within container
		fmt.Printf("Setting up iptables OUTPUT for port %s -> 127.0.0.1:%s\n", port, proxyPort)
		if err := exec.Command("iptables", "-t", "nat", "-A", "OUTPUT",
			"-d", targetIP, "-p", "tcp", "--dport", port,
			"-j", "DNAT", "--to-destination", "127.0.0.1:"+proxyPort).Run(); err != nil {
			fmt.Printf("Error adding iptables OUTPUT rule for port %s: %v\n", port, err)
			os.Exit(1)
		}
	}

	// Verify with verbose output showing counters
	fmt.Println("\nVerifying iptables PREROUTING rules:")
	cmd := exec.Command("iptables", "-t", "nat", "-L", "PREROUTING", "-n", "-v")
	output, _ := cmd.CombinedOutput()
	fmt.Println(string(output))

	fmt.Println("\nVerifying iptables OUTPUT rules:")
	cmd2 := exec.Command("iptables", "-t", "nat", "-L", "OUTPUT", "-n", "-v")
	output2, _ := cmd2.CombinedOutput()
	fmt.Println(string(output2))
}

// startTcpdump starts monitoring
func startTcpdump(cfg *Config) {
	filter := ""
	for _, port := range cfg.L7HttpPorts {
		filter += "(tcp port " + port + " or tcp port 5" + port + ") or "
	}
	for _, port := range cfg.L7GrpcPorts {
		filter += "(tcp port " + port + " or tcp port 5" + port + ") or "
	}
	filter = strings.TrimSuffix(filter, " or ")
	exec.Command("tcpdump", "-i", "any", filter, "-nn", "-s0", ">", "/tmp/tcpdump.log").Start()
}
