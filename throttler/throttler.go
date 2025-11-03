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
	L7Delay        string
	L7AbortPercent int
	L7HttpStatus   int
	L7GrpcStatus   int
	L7HttpPorts    []string
	L7GrpcPorts    []string
	TargetIP       string
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
		// Always attempt to teardown L7 faults on stop, regardless of flags
		// This ensures cleanup works even if user doesn't specify the same ports
		teardownL7(cfg)
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

	// Check if any L7 faults are actually configured
	hasL7Faults := false
	if cfg.L7Delay != "" && cfg.L7Delay != "0" && cfg.L7Delay != "0s" {
		hasL7Faults = true
	}
	if cfg.L7AbortPercent > 0 {
		hasL7Faults = true
	}

	if !hasL7Faults {
		fmt.Println("No L7 faults configured (delay is 0 or empty, abort percent is 0). Skipping L7 setup.")
		return
	}

	// Add sanity check for L7HttpStatus
	if cfg.L7AbortPercent > 0 && (cfg.L7HttpStatus < 200 || cfg.L7HttpStatus >= 600) {
		fmt.Printf("Error: Invalid L7 abort status %d. Must be >= 200 and < 600\n", cfg.L7HttpStatus)
		os.Exit(1)
	}
	// Add sanity check for L7GrpcStatus
	if cfg.L7AbortPercent > 0 && cfg.L7GrpcStatus != 0 && (cfg.L7GrpcStatus < 0 || cfg.L7GrpcStatus > 16) {
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

// teardownL7 stops Envoy and cleans up
func teardownL7(cfg *Config) {
	fmt.Println("Tearing down L7 faults...")

	// Step 1: Kill Envoy aggressively
	fmt.Println("Stopping Envoy process...")
	for i := 0; i < 3; i++ {
		exec.Command("pkill", "-9", "envoy").Run()
		time.Sleep(500 * time.Millisecond)

		// Check if envoy is still running (excluding defunct/zombie processes)
		checkCmd := exec.Command("sh", "-c", "ps aux | grep '[e]nvoy' | grep -v defunct")
		if output, _ := checkCmd.Output(); len(output) == 0 {
			fmt.Println("Envoy stopped successfully")
			break
		}
		if i == 2 {
			fmt.Println("Warning: Envoy may still be running")
		}
	}

	// Step 2: Remove all nftables tables related to chaos injection
	fmt.Println("Removing chaos-related nftables tables...")
	// Remove chaos_utils table (new name)
	if err := exec.Command("nft", "delete", "table", "ip", "chaos_utils").Run(); err != nil {
		fmt.Println("Note: chaos_utils table not found (may not exist)")
	}
	// Remove envoy table (legacy name, for backward compatibility)
	if err := exec.Command("nft", "delete", "table", "inet", "envoy").Run(); err != nil {
		fmt.Println("Note: envoy table not found (may not exist)")
	}

	// Step 3: Clean up temp files
	fmt.Println("Removing Envoy config files...")
	exec.Command("sh", "-c", "rm -f /tmp/envoy-config-*.yaml /tmp/envoy.log /tmp/tcpdump.log").Run()

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

	// Determine if we have any faults to apply
	hasDelay := cfg.L7Delay != "" && cfg.L7Delay != "0" && cfg.L7Delay != "0s"
	hasAbort := cfg.L7AbortPercent > 0

	// Generate listeners for HTTP ports
	for _, port := range cfg.L7HttpPorts {
		abortConfig := ""
		if hasAbort {
			abortConfig = fmt.Sprintf(`
              abort:
                http_status: %d
                percentage:
                  numerator: %d
                  denominator: HUNDRED`, cfg.L7HttpStatus, cfg.L7AbortPercent)
		}

		delayConfig := ""
		if hasDelay {
			delayConfig = fmt.Sprintf(`
              delay:
                fixed_delay: %s
                percentage:
                  numerator: 100
                  denominator: HUNDRED`, cfg.L7Delay)
		}

		// Only add fault filter if we have actual faults
		faultFilterConfig := ""
		if hasDelay || hasAbort {
			faultFilterConfig = fmt.Sprintf(`
          - name: envoy.filters.http.fault
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.fault.v3.HTTPFault%s%s`, delayConfig, abortConfig)
		}

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
          http_filters:%s
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
`, port, port, port, port, faultFilterConfig))
	}

	// Generate listeners for gRPC ports with HTTP/2 support
	for _, port := range cfg.L7GrpcPorts {
		// Build abort config for gRPC - note the different field name
		abortConfig := ""
		if hasAbort {
			abortConfig = fmt.Sprintf(`
              abort:
                grpc_status: %d
                percentage:
                  numerator: %d
                  denominator: HUNDRED`, cfg.L7GrpcStatus, cfg.L7AbortPercent)
		}

		delayConfig := ""
		if hasDelay {
			delayConfig = fmt.Sprintf(`
              delay:
                fixed_delay: %s
                percentage:
                  numerator: 100
                  denominator: HUNDRED`, cfg.L7Delay)
		}

		// Only add fault filter if we have actual faults
		faultFilterConfig := ""
		if hasDelay || hasAbort {
			faultFilterConfig = fmt.Sprintf(`
          - name: envoy.filters.http.fault
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.fault.v3.HTTPFault%s%s`, delayConfig, abortConfig)
		}

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
          codec_type: AUTO
          http2_protocol_options: {}
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
                  retry_policy:
                    retry_on: ""
                    num_retries: 0
          http_filters:%s
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
`, port, port, port, port, faultFilterConfig))
	}

	return listeners.String()
}

func generateClusters(cfg *Config, targetIP string) string {
	var clusters strings.Builder

	// Since we're in the same network namespace as the target container,
	// connect to localhost to reach the upstream service
	// This avoids nftables redirect loops
	upstreamIP := "127.0.0.1"

	// Generate clusters for HTTP ports (no HTTP/2)
	for _, port := range cfg.L7HttpPorts {
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
                address: %s
                port_value: %s
`, port, port, upstreamIP, port))
	}

	// Generate clusters for gRPC ports with HTTP/2 support
	for _, port := range cfg.L7GrpcPorts {
		clusters.WriteString(fmt.Sprintf(`
  - name: cluster_%s
    type: STATIC
    lb_policy: ROUND_ROBIN
    http2_protocol_options: {}
    load_assignment:
      cluster_name: cluster_%s
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: %s
                port_value: %s
`, port, port, upstreamIP, port))
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

// setupL7Interception adds rules for interception using nftables
func setupL7Interception(cfg *Config, targetIP, interfaceName string) {
	fmt.Println("Setting up nftables rules for L7 interception...")

	// Create nftables table and chains
	commands := []string{
		"nft add table ip chaos_utils",
		"nft add chain ip chaos_utils prerouting { type nat hook prerouting priority -100 \\; }",
		"nft add chain ip chaos_utils output { type nat hook output priority -100 \\; }",
	}

	for _, cmd := range commands {
		if err := exec.Command("sh", "-c", cmd).Run(); err != nil {
			fmt.Printf("Error creating nftables structure: %v\n", err)
		}
	}

	// Add rules for each port
	allPorts := append(cfg.L7HttpPorts, cfg.L7GrpcPorts...)
	for _, port := range allPorts {
		proxyPort := "5" + port

		// PREROUTING: Intercept incoming traffic from other containers
		// Skip traffic from Envoy's source port to avoid loops
		preroutingRule := fmt.Sprintf(
			"nft add rule ip chaos_utils prerouting ip daddr %s tcp dport %s tcp sport != %s dnat to %s:%s",
			targetIP, port, proxyPort, targetIP, proxyPort)
		fmt.Printf("Adding PREROUTING rule for port %s -> %s\n", port, proxyPort)
		if err := exec.Command("sh", "-c", preroutingRule).Run(); err != nil {
			fmt.Printf("Error adding nftables PREROUTING rule: %v\n", err)
			os.Exit(1)
		}

		// OUTPUT: Intercept local traffic from within container
		// Skip traffic from Envoy's source port to avoid loops
		outputRule := fmt.Sprintf(
			"nft add rule ip chaos_utils output ip daddr %s tcp dport %s tcp sport != %s dnat to %s:%s",
			targetIP, port, proxyPort, targetIP, proxyPort)
		fmt.Printf("Adding OUTPUT rule for port %s -> %s\n", port, proxyPort)
		if err := exec.Command("sh", "-c", outputRule).Run(); err != nil {
			fmt.Printf("Error adding nftables OUTPUT rule: %v\n", err)
			os.Exit(1)
		}
	}

	// Verify rules
	fmt.Println("\nVerifying nftables rules:")
	cmd := exec.Command("nft", "list", "table", "ip", "chaos_utils")
	output, _ := cmd.CombinedOutput()
	fmt.Println(string(output))
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
