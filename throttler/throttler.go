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
	L7Ports         []string
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
		if len(cfg.L7Ports) > 0 {
			setupL7(cfg)
		}
	} else {
		teardown(t, cfg)
		if len(cfg.L7Ports) > 0 {
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
	setupNftables(cfg, targetIP, interfaceName)
	startTcpdump(cfg)

	fmt.Println("L7 faults setup via Envoy sidecar")
}

// teardownL7 stops Envoy and cleans up
func teardownL7(cfg *Config) {
	// Kill Envoy directly - sidecar shares the network namespace
	exec.Command("pkill", "envoy").Run()
	// Flush nftables rules directly
	exec.Command("nft", "flush", "ruleset").Run()
	fmt.Println("L7 faults torn down from shared network namespace")
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
	for _, port := range cfg.L7Ports {
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
              abort:
                http_status: %d
                percentage:
                  numerator: %d
                  denominator: HUNDRED
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
`, port, port, port, port, cfg.L7Delay, cfg.L7AbortStatus, cfg.L7AbortPercent))
	}
	return listeners.String()
}

func generateClusters(cfg *Config, targetIP string) string {
	var clusters strings.Builder
	for _, port := range cfg.L7Ports {
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
			for _, port := range cfg.L7Ports {
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

// setupNftables adds rules for interception
func setupNftables(cfg *Config, targetIP, interfaceName string) {
	fmt.Println("Setting up nftables rules for L7 interception...")

	// Flush any existing rules first
	exec.Command("nft", "flush", "ruleset").Run()

	// Create nftables table directly - sidecar already shares the network namespace
	if err := exec.Command("nft", "add", "table", "inet", "envoy").Run(); err != nil {
		fmt.Println("Error creating nftables table:", err)
		os.Exit(1)
	}

	if err := exec.Command("nft", "add", "chain", "inet", "envoy", "raw_preroute", "{ type filter hook prerouting priority raw ; policy accept ; }").Run(); err != nil {
		fmt.Println("Error creating raw_preroute chain:", err)
		os.Exit(1)
	}

	if err := exec.Command("nft", "add", "chain", "inet", "envoy", "nat_preroute", "{ type nat hook prerouting priority dstnat ; policy accept ; }").Run(); err != nil {
		fmt.Println("Error creating nat_preroute chain:", err)
		os.Exit(1)
	}

	if err := exec.Command("nft", "add", "chain", "inet", "envoy", "nat_output", "{ type nat hook output priority 100 ; policy accept ; }").Run(); err != nil {
		fmt.Println("Error creating nat_output chain:", err)
		os.Exit(1)
	}

	if err := exec.Command("nft", "add", "chain", "inet", "envoy", "filter_input", "{ type filter hook input priority filter ; policy accept ; }").Run(); err != nil {
		fmt.Println("Error creating filter_input chain:", err)
		os.Exit(1)
	}

	// Add rules for each port
	for _, port := range cfg.L7Ports {
		proxyPort := "5" + port

		fmt.Printf("Setting up interception for port %s -> Envoy proxy port %s\n", port, proxyPort)

		// DNAT in nat prerouting: redirect incoming traffic to Envoy listening on targetIP:proxyPort
		// This catches traffic from other containers on the Docker network
		if err := exec.Command("nft", "add", "rule", "inet", "envoy", "nat_preroute",
			"ip", "daddr", targetIP, "tcp", "dport", port, "dnat", "to", targetIP+":"+proxyPort).Run(); err != nil {
			fmt.Printf("Error adding DNAT prerouting rule for port %s: %v\n", port, err)
			os.Exit(1)
		}

		// DNAT in nat output: redirect locally-generated traffic to Envoy on 127.0.0.1
		// This catches traffic originating from within the container
		if err := exec.Command("nft", "add", "rule", "inet", "envoy", "nat_output",
			"ip", "daddr", targetIP, "tcp", "dport", port, "dnat", "to", "127.0.0.1:"+proxyPort).Run(); err != nil {
			fmt.Printf("Error adding DNAT output rule for port %s: %v\n", port, err)
			os.Exit(1)
		}
	}

	// List the rules for verification
	fmt.Println("\nVerifying nftables rules:")
	cmd := exec.Command("nft", "list", "ruleset")
	output, _ := cmd.CombinedOutput()
	fmt.Println(string(output))

	fmt.Printf("nftables rules applied successfully in shared network namespace\n")
}

// startTcpdump starts monitoring
func startTcpdump(cfg *Config) {
	filter := ""
	for _, port := range cfg.L7Ports {
		filter += "(tcp port " + port + " or tcp port 5" + port + ") or "
	}
	filter = strings.TrimSuffix(filter, " or ")
	exec.Command("tcpdump", "-i", "any", filter, "-nn", "-s0", ">", "/tmp/tcpdump.log").Start()
}
