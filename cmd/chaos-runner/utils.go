package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jihwankim/chaos-utils/pkg/config"
)

// loadConfig loads the configuration from file, auto-generating if needed
func loadConfig() (*config.Config, error) {
	// Determine config file path
	configPath := cfgFile
	if configPath == "" {
		configPath = "config.yaml"
	}

	// Check if config exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Auto-generate default config
		fmt.Printf("⚠️  Config file not found, creating default configuration at: %s\n", configPath)
		fmt.Println("   You can edit this file to customize settings (enclave name, Prometheus URL, etc.)")
		fmt.Println()

		cfg := config.DefaultConfig()
		if err := cfg.Save(configPath); err != nil {
			return nil, fmt.Errorf("failed to create default config: %w", err)
		}

		return cfg, nil
	}

	// Load existing configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config from %s: %w", configPath, err)
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// rediscoverPrometheus attempts to re-discover Prometheus endpoint for the configured enclave
func rediscoverPrometheus(cfg *config.Config) error {
	if cfg.Kurtosis.EnclaveName == "" {
		return fmt.Errorf("enclave name is empty")
	}

	// Try multiple Prometheus service name patterns
	serviceNames := []string{
		"prometheus-001", // kurtosis-cdk uses prometheus-001
		"prometheus",     // kurtosis-pos uses prometheus
	}

	var lastErr error
	for _, serviceName := range serviceNames {
		cmd := exec.Command("kurtosis", "port", "print", cfg.Kurtosis.EnclaveName, serviceName, "http")
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

		// Success! Update config and return
		cfg.Prometheus.URL = endpoint
		fmt.Printf("✓ Discovered Prometheus endpoint for enclave '%s': %s\n", cfg.Kurtosis.EnclaveName, endpoint)
		return nil
	}

	if lastErr != nil {
		return fmt.Errorf("failed to discover Prometheus endpoint (tried: %v): %w", serviceNames, lastErr)
	}
	return fmt.Errorf("failed to discover Prometheus endpoint (tried: %v)", serviceNames)
}
