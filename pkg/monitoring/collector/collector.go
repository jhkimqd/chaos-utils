package collector

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/monitoring/prometheus"
)

// MetricSample represents a single metric sample
type MetricSample struct {
	MetricName string
	Timestamp  time.Time
	Value      float64
	Labels     map[string]string
}

// Collector collects metrics during chaos tests
type Collector struct {
	promClient      *prometheus.Client
	samples         map[string][]MetricSample // metric name -> samples
	mutex           sync.RWMutex
	interval        time.Duration
	running         bool
	stopCh          chan struct{}
	metricNames     []string
}

// Config contains collector configuration
type Config struct {
	PrometheusClient *prometheus.Client
	Interval         time.Duration
	MetricNames      []string
}

// New creates a new metrics collector
func New(config Config) *Collector {
	if config.Interval == 0 {
		config.Interval = 15 * time.Second
	}

	return &Collector{
		promClient:  config.PrometheusClient,
		samples:     make(map[string][]MetricSample),
		interval:    config.Interval,
		stopCh:      make(chan struct{}),
		metricNames: config.MetricNames,
	}
}

// Start begins collecting metrics
func (c *Collector) Start(ctx context.Context) {
	c.mutex.Lock()
	if c.running {
		c.mutex.Unlock()
		return
	}
	c.running = true
	c.mutex.Unlock()

	go c.collectLoop(ctx)
}

// Stop stops collecting metrics
func (c *Collector) Stop() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if !c.running {
		return
	}

	close(c.stopCh)
	c.running = false
}

// collectLoop is the main collection loop
func (c *Collector) collectLoop(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	// Collect initial sample
	c.collectMetrics(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.collectMetrics(ctx)
		}
	}
}

// collectMetrics collects all configured metrics
func (c *Collector) collectMetrics(ctx context.Context) {
	for _, metricName := range c.metricNames {
		if err := c.collectMetric(ctx, metricName); err != nil {
			fmt.Printf("Warning: failed to collect metric %s: %v\n", metricName, err)
		}
	}
}

// collectMetric collects a single metric
func (c *Collector) collectMetric(ctx context.Context, metricName string) error {
	// Query Prometheus
	results, err := c.promClient.QueryLatest(ctx, metricName)
	if err != nil {
		return fmt.Errorf("query failed: %w", err)
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Initialize samples slice if needed
	if _, exists := c.samples[metricName]; !exists {
		c.samples[metricName] = make([]MetricSample, 0)
	}

	// Store all results
	for _, result := range results {
		sample := MetricSample{
			MetricName: metricName,
			Timestamp:  result.Timestamp,
			Value:      result.Value,
			Labels:     result.Labels,
		}
		c.samples[metricName] = append(c.samples[metricName], sample)
	}

	return nil
}

// GetSamples returns all collected samples for a metric
func (c *Collector) GetSamples(metricName string) []MetricSample {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	if samples, exists := c.samples[metricName]; exists {
		// Return a copy
		result := make([]MetricSample, len(samples))
		copy(result, samples)
		return result
	}

	return []MetricSample{}
}

// GetAllSamples returns all collected samples
func (c *Collector) GetAllSamples() map[string][]MetricSample {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	// Return a deep copy
	result := make(map[string][]MetricSample)
	for metricName, samples := range c.samples {
		samplesCopy := make([]MetricSample, len(samples))
		copy(samplesCopy, samples)
		result[metricName] = samplesCopy
	}

	return result
}

// GetLatestValue returns the latest value for a metric
func (c *Collector) GetLatestValue(metricName string) (float64, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	if samples, exists := c.samples[metricName]; exists && len(samples) > 0 {
		return samples[len(samples)-1].Value, true
	}

	return 0, false
}

// GetSampleCount returns the number of samples collected for a metric
func (c *Collector) GetSampleCount(metricName string) int {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	if samples, exists := c.samples[metricName]; exists {
		return len(samples)
	}

	return 0
}

// GetTotalSamples returns the total number of samples across all metrics
func (c *Collector) GetTotalSamples() int {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	total := 0
	for _, samples := range c.samples {
		total += len(samples)
	}

	return total
}

// Clear clears all collected samples
func (c *Collector) Clear() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.samples = make(map[string][]MetricSample)
}

// AddMetric adds a metric to the collection list
func (c *Collector) AddMetric(metricName string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Check if already exists
	for _, name := range c.metricNames {
		if name == metricName {
			return
		}
	}

	c.metricNames = append(c.metricNames, metricName)
}

// RemoveMetric removes a metric from the collection list
func (c *Collector) RemoveMetric(metricName string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	for i, name := range c.metricNames {
		if name == metricName {
			c.metricNames = append(c.metricNames[:i], c.metricNames[i+1:]...)
			break
		}
	}
}

// GetMetricNames returns the list of metrics being collected
func (c *Collector) GetMetricNames() []string {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	result := make([]string, len(c.metricNames))
	copy(result, c.metricNames)
	return result
}

// IsRunning returns true if the collector is running
func (c *Collector) IsRunning() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.running
}

// GetSummary returns a summary of collected data
func (c *Collector) GetSummary() string {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return fmt.Sprintf("Metrics Collector Summary:\n"+
		"  Metrics: %d\n"+
		"  Total Samples: %d\n"+
		"  Running: %v\n"+
		"  Interval: %v\n",
		len(c.metricNames),
		c.GetTotalSamples(),
		c.running,
		c.interval)
}

// ExportToTimeSeries exports samples to a time-series format
type TimeSeries struct {
	MetricName string
	Labels     map[string]string
	Datapoints []Datapoint
}

type Datapoint struct {
	Timestamp time.Time
	Value     float64
}

// ExportTimeSeries exports all samples as time series
func (c *Collector) ExportTimeSeries() []TimeSeries {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	// Group samples by metric and label set
	grouped := make(map[string]*TimeSeries)

	for metricName, samples := range c.samples {
		for _, sample := range samples {
			// Create a key from metric name and labels
			key := metricName
			for k, v := range sample.Labels {
				key += fmt.Sprintf("|%s=%s", k, v)
			}

			// Get or create time series
			ts, exists := grouped[key]
			if !exists {
				ts = &TimeSeries{
					MetricName: metricName,
					Labels:     sample.Labels,
					Datapoints: make([]Datapoint, 0),
				}
				grouped[key] = ts
			}

			// Add datapoint
			ts.Datapoints = append(ts.Datapoints, Datapoint{
				Timestamp: sample.Timestamp,
				Value:     sample.Value,
			})
		}
	}

	// Convert to slice
	result := make([]TimeSeries, 0, len(grouped))
	for _, ts := range grouped {
		result = append(result, *ts)
	}

	return result
}
