package main

import (
	"fmt"
	"os"

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

