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
	stopCh         chan struct{}
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
		stopCh:         make(chan struct{}),
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
	close(c.stopCh)

	fmt.Printf("🚨 EMERGENCY STOP TRIGGERED: %s\n", reason)

	// Execute all registered callbacks
	for i, callback := range c.callbacks {
		fmt.Printf("   Executing emergency callback %d/%d...\n", i+1, len(c.callbacks))
		callback()
	}
}

// Stop manually triggers an emergency stop
func (c *Controller) Stop(reason string) {
	c.triggerStop(reason)
}

// IsStopped returns true if emergency stop has been triggered
func (c *Controller) IsStopped() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.stopped
}

// StopChannel returns a channel that closes when stop is triggered
func (c *Controller) StopChannel() <-chan struct{} {
	return c.stopCh
}

// OnStop registers a callback to execute when stop is triggered
func (c *Controller) OnStop(callback func()) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.callbacks = append(c.callbacks, callback)
}

// CreateStopFile creates the emergency stop file
func (c *Controller) CreateStopFile() error {
	f, err := os.Create(c.stopFile)
	if err != nil {
		return fmt.Errorf("failed to create stop file: %w", err)
	}
	defer f.Close()

	_, err = f.WriteString(fmt.Sprintf("Emergency stop requested at %s\n", time.Now().Format(time.RFC3339)))
	if err != nil {
		return fmt.Errorf("failed to write to stop file: %w", err)
	}

	return nil
}

// RemoveStopFile removes the emergency stop file
func (c *Controller) RemoveStopFile() error {
	err := os.Remove(c.stopFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove stop file: %w", err)
	}
	return nil
}

// GetStopFilePath returns the path to the stop file
func (c *Controller) GetStopFilePath() string {
	return c.stopFile
}
