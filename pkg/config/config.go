package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the chaos framework configuration
type Config struct {
	Framework  FrameworkConfig  `yaml:"framework"`
	Kurtosis   KurtosisConfig   `yaml:"kurtosis"`
	Docker     DockerConfig     `yaml:"docker"`
	Prometheus PrometheusConfig `yaml:"prometheus"`
	Reporting  ReportingConfig  `yaml:"reporting"`
	Emergency  EmergencyConfig  `yaml:"emergency"`
	Execution  ExecutionConfig  `yaml:"execution"`
	Safety     SafetyConfig     `yaml:"safety"`
}

// FrameworkConfig contains general framework settings
type FrameworkConfig struct {
	Version   string `yaml:"version"`
	LogLevel  string `yaml:"log_level"`
	LogFormat string `yaml:"log_format"`
}

// KurtosisConfig contains Kurtosis connection settings
type KurtosisConfig struct {
	DefaultEnclave string `yaml:"default_enclave"`
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
			DefaultEnclave: "polygon-chain",
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

	// Expand environment variables in the YAML content
	expandedData := []byte(os.ExpandEnv(string(data)))

	// Parse YAML
	if err := yaml.Unmarshal(expandedData, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Handle empty values after environment variable expansion
	// If Prometheus URL is empty after expansion, use the default
	if cfg.Prometheus.URL == "" {
		cfg.Prometheus.URL = DefaultConfig().Prometheus.URL
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
	if c.Kurtosis.DefaultEnclave == "" {
		return fmt.Errorf("kurtosis.default_enclave is required")
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
