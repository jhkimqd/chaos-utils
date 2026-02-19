package config

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the chaos framework configuration
type Config struct {
	Framework  FrameworkConfig  `yaml:"framework"`
	Kurtosis   KurtosisConfig   `yaml:"kurtosis"`
	Docker     DockerConfig     `yaml:"docker"`
	Prometheus PrometheusConfig `yaml:"prometheus"`
	EVMRPC     EVMRPCConfig     `yaml:"evm_rpc"`
	Reporting  ReportingConfig  `yaml:"reporting"`
	Emergency  EmergencyConfig  `yaml:"emergency"`
	Execution  ExecutionConfig  `yaml:"execution"`
	Safety     SafetyConfig     `yaml:"safety"`
}

// EVMRPCConfig contains EVM JSON-RPC endpoint settings used for precompile checks.
type EVMRPCConfig struct {
	// URL is the EVM JSON-RPC endpoint (e.g. "http://127.0.0.1:8545").
	// Auto-discovered from the Kurtosis enclave if empty.
	URL string `yaml:"url"`
}

// FrameworkConfig contains general framework settings
type FrameworkConfig struct {
	Version   string `yaml:"version"`
	LogLevel  string `yaml:"log_level"`
	LogFormat string `yaml:"log_format"`
}

// KurtosisConfig contains Kurtosis connection settings
type KurtosisConfig struct {
	EnclaveName string `yaml:"enclave_name"`
}

// DockerConfig contains Docker settings for sidecar management
type DockerConfig struct {
	SidecarImage string `yaml:"sidecar_image"`
	PullPolicy   string `yaml:"pull_policy"`
}

// PrometheusConfig contains Prometheus connection settings
type PrometheusConfig struct {
	URL             string        `yaml:"url"`
	Timeout         time.Duration `yaml:"timeout"`
	RefreshInterval time.Duration `yaml:"refresh_interval"`
}

// ReportingConfig contains reporting and output settings
type ReportingConfig struct {
	OutputDir string   `yaml:"output_dir"`
	KeepLastN int      `yaml:"keep_last_n"`
	Formats   []string `yaml:"formats"`
}

// EmergencyConfig contains emergency stop settings
type EmergencyConfig struct {
	StopFile           string        `yaml:"stop_file"`
	AutoCleanupTimeout time.Duration `yaml:"auto_cleanup_timeout"`
}

// ExecutionConfig contains test execution settings
type ExecutionConfig struct {
	DefaultMode         string        `yaml:"default_mode"`
	DefaultWarmup       time.Duration `yaml:"default_warmup"`
	DefaultCooldown     time.Duration `yaml:"default_cooldown"`
	MaxConcurrentFaults int           `yaml:"max_concurrent_faults"`
}

// SafetyConfig contains safety limits
type SafetyConfig struct {
	MaxDuration         time.Duration `yaml:"max_duration"`
	RequireConfirmation bool          `yaml:"require_confirmation"`
}

// DefaultConfig returns a default configuration
func DefaultConfig() *Config {
	return &Config{
		Framework: FrameworkConfig{
			Version:   "v1",
			LogLevel:  "info",
			LogFormat: "text",
		},
		Kurtosis: KurtosisConfig{
			EnclaveName: "pos",
		},
		Docker: DockerConfig{
			SidecarImage: "jhkimqd/chaos-utils:latest",
			PullPolicy:   "if_not_present",
		},
		Prometheus: PrometheusConfig{
			URL:             "http://localhost:9090",
			Timeout:         30 * time.Second,
			RefreshInterval: 15 * time.Second,
		},
		Reporting: ReportingConfig{
			OutputDir: "./reports",
			KeepLastN: 50,
			Formats:   []string{"json", "html"},
		},
		Emergency: EmergencyConfig{
			StopFile:           "/tmp/chaos-emergency-stop",
			AutoCleanupTimeout: 5 * time.Minute,
		},
		Execution: ExecutionConfig{
			DefaultMode:         "sequential",
			DefaultWarmup:       30 * time.Second,
			DefaultCooldown:     30 * time.Second,
			MaxConcurrentFaults: 5,
		},
		Safety: SafetyConfig{
			MaxDuration:         1 * time.Hour,
			RequireConfirmation: true,
		},
	}
}

// DiscoverPrometheusEndpoint attempts to discover Prometheus endpoint from Kurtosis enclave
func DiscoverPrometheusEndpoint(enclaveName string) (string, error) {
	if enclaveName == "" {
		return "", fmt.Errorf("enclave name is empty")
	}

	// Try multiple Prometheus service name patterns (different Kurtosis deployments use different naming)
	serviceNames := []string{
		"prometheus-001", // kurtosis-cdk uses prometheus-001
		"prometheus",     // kurtosis-pos uses prometheus
	}

	var lastErr error
	for _, serviceName := range serviceNames {
		// Run: kurtosis port print <enclave> <service> http
		// Use Output() instead of CombinedOutput() to ignore stderr (Kurtosis warnings)
		cmd := exec.Command("kurtosis", "port", "print", enclaveName, serviceName, "http")
		output, err := cmd.Output()
		if err != nil {
			lastErr = err
			continue // Try next service name
		}

		// Parse the output (e.g., "http://127.0.0.1:33066")
		endpoint := strings.TrimSpace(string(output))
		if endpoint == "" {
			continue // Try next service name
		}

		// Validate it looks like a URL
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			continue // Try next service name
		}

		return endpoint, nil
	}

	// All attempts failed
	if lastErr != nil {
		return "", fmt.Errorf("failed to discover Prometheus endpoint (tried: %v): %w", serviceNames, lastErr)
	}
	return "", fmt.Errorf("failed to discover Prometheus endpoint (tried: %v)", serviceNames)
}

// DiscoverEVMRPCEndpoint attempts to discover the Bor EVM JSON-RPC endpoint from a Kurtosis enclave.
func DiscoverEVMRPCEndpoint(enclaveName string) (string, error) {
	if enclaveName == "" {
		return "", fmt.Errorf("enclave name is empty")
	}

	// Try EVM RPC service names in order: dedicated RPC node first, then first validator.
	serviceNames := []string{
		"l2-el-1-bor-heimdall-v2-rpc",       // dedicated RPC-only node
		"l2-el-1-bor-heimdall-v2-validator",  // fallback: validator 1
	}

	var lastErr error
	for _, serviceName := range serviceNames {
		cmd := exec.Command("kurtosis", "port", "print", enclaveName, serviceName, "rpc")
		output, err := cmd.Output()
		if err != nil {
			lastErr = err
			continue
		}
		endpoint := strings.TrimSpace(string(output))
		if endpoint == "" {
			continue
		}
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			continue
		}
		return endpoint, nil
	}

	if lastErr != nil {
		return "", fmt.Errorf("failed to discover EVM RPC endpoint (tried: %v): %w", serviceNames, lastErr)
	}
	return "", fmt.Errorf("failed to discover EVM RPC endpoint (tried: %v)", serviceNames)
}

// Load loads configuration from a YAML file
func Load(path string) (*Config, error) {
	// Start with defaults
	cfg := DefaultConfig()

	// If no path provided, look for config.yaml in current directory
	if path == "" {
		path = "config.yaml"
	}

	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Return default config if file doesn't exist
		return cfg, nil
	}

	// Read file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Check if PROMETHEUS_URL environment variable is set
	prometheusURLEnvSet := os.Getenv("PROMETHEUS_URL") != ""
	prometheusURLEnv := os.Getenv("PROMETHEUS_URL")

	// Expand environment variables in the YAML content
	expandedData := []byte(os.ExpandEnv(string(data)))

	// Parse YAML
	if err := yaml.Unmarshal(expandedData, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Apply PROMETHEUS_URL env var if set (takes priority over config file)
	if prometheusURLEnvSet {
		cfg.Prometheus.URL = prometheusURLEnv
	}

	return cfg, nil
}

// Save writes configuration to a YAML file
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if c.Kurtosis.EnclaveName == "" {
		return fmt.Errorf("kurtosis.enclave_name is required")
	}

	if c.Docker.SidecarImage == "" {
		return fmt.Errorf("docker.sidecar_image is required")
	}

	if c.Reporting.OutputDir == "" {
		return fmt.Errorf("reporting.output_dir is required")
	}

	if c.Execution.MaxConcurrentFaults < 1 {
		return fmt.Errorf("execution.max_concurrent_faults must be at least 1")
	}

	return nil
}
