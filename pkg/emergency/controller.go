package emergency

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Controller manages emergency stop functionality
type Controller struct {
	stopFile       string
	stopped        bool
	mutex          sync.RWMutex
	callbacks      []func()
	pollInterval   time.Duration
	signalHandlers bool
}

// Config contains emergency controller configuration
type Config struct {
	// StopFile is the path to watch for emergency stop
	StopFile string

	// PollInterval for checking stop file
	PollInterval time.Duration

	// EnableSignalHandlers enables SIGINT/SIGTERM handling
	EnableSignalHandlers bool
}

// New creates a new emergency controller
func New(config Config) *Controller {
	if config.StopFile == "" {
		config.StopFile = "/tmp/chaos-emergency-stop"
	}

	if config.PollInterval == 0 {
		config.PollInterval = 1 * time.Second
	}

	return &Controller{
		stopFile:       config.StopFile,
		callbacks:      make([]func(), 0),
		pollInterval:   config.PollInterval,
		signalHandlers: config.EnableSignalHandlers,
	}
}

// Start begins monitoring for emergency stop conditions
func (c *Controller) Start(ctx context.Context) {
	// Watch for stop file
	go c.watchStopFile(ctx)

	// Watch for signals if enabled
	if c.signalHandlers {
		go c.watchSignals(ctx)
	}
}

// watchStopFile polls for the existence of the stop file
func (c *Controller) watchStopFile(ctx context.Context) {
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c.checkStopFile() {
				fmt.Printf("🛑 Emergency stop file detected: %s\n", c.stopFile)
				c.triggerStop("stop file detected")
				return
			}
		}
	}
}

// watchSignals listens for OS signals
func (c *Controller) watchSignals(ctx context.Context) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-ctx.Done():
		signal.Stop(sigCh)
		return
	case sig := <-sigCh:
		fmt.Printf("🛑 Emergency stop signal received: %v\n", sig)
		c.triggerStop(fmt.Sprintf("signal: %v", sig))
		signal.Stop(sigCh)
		return
	}
}

// checkStopFile checks if the stop file exists
func (c *Controller) checkStopFile() bool {
	_, err := os.Stat(c.stopFile)
	return err == nil
}

// triggerStop triggers the emergency stop
func (c *Controller) triggerStop(reason string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.stopped {
		return // Already stopped
	}

	c.stopped = true

	fmt.Printf("🚨 EMERGENCY STOP TRIGGERED: %s\n", reason)

	// Execute all registered callbacks
	for i, callback := range c.callbacks {
		fmt.Printf("   Executing emergency callback %d/%d...\n", i+1, len(c.callbacks))
		callback()
	}
}

// OnStop registers a callback to execute when stop is triggered
func (c *Controller) OnStop(callback func()) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.callbacks = append(c.callbacks, callback)
}
