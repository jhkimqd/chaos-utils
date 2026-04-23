package prometheus

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// Client wraps the Prometheus API client
type Client struct {
	api    v1.API
	config Config
}

// Config contains Prometheus client configuration
type Config struct {
	URL             string
	Timeout         time.Duration
	RefreshInterval time.Duration
}

// QueryResult represents a Prometheus query result
type QueryResult struct {
	Timestamp time.Time
	Value     float64
	Labels    map[string]string
	Raw       model.Value
}

// New creates a new Prometheus client
func New(config Config) (*Client, error) {
	// Create API client
	apiClient, err := api.NewClient(api.Config{
		Address: config.URL,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Prometheus client: %w", err)
	}

	// Create v1 API
	v1api := v1.NewAPI(apiClient)

	return &Client{
		api:    v1api,
		config: config,
	}, nil
}

// QueryInstant executes an instant query at a specific time
func (c *Client) QueryInstant(ctx context.Context, query string, ts time.Time) ([]QueryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	result, warnings, err := c.api.Query(ctx, query, ts)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	if len(warnings) > 0 {
		fmt.Printf("Prometheus warnings: %v\n", warnings)
	}

	return c.parseResult(result)
}

// QueryRange executes a range query over a time window
func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]QueryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	r := v1.Range{
		Start: start,
		End:   end,
		Step:  step,
	}

	result, warnings, err := c.api.QueryRange(ctx, query, r)
	if err != nil {
		return nil, fmt.Errorf("range query failed: %w", err)
	}

	if len(warnings) > 0 {
		fmt.Printf("Prometheus warnings: %v\n", warnings)
	}

	return c.parseResult(result)
}

// QueryLatest executes an instant query at the current time
func (c *Client) QueryLatest(ctx context.Context, query string) ([]QueryResult, error) {
	return c.QueryInstant(ctx, query, time.Now())
}

// TestConnection tests the connection to Prometheus
func (c *Client) TestConnection(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	// Try a simple query
	_, _, err := c.api.Query(ctx, "up", time.Now())
	if err != nil {
		return fmt.Errorf("connection test failed: %w", err)
	}

	return nil
}

// GetMetricNames retrieves all available metric names
func (c *Client) GetMetricNames(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	labelValues, warnings, err := c.api.LabelValues(ctx, "__name__", nil, time.Now().Add(-1*time.Hour), time.Now())
	if err != nil {
		return nil, fmt.Errorf("failed to get metric names: %w", err)
	}

	if len(warnings) > 0 {
		fmt.Printf("Prometheus warnings: %v\n", warnings)
	}

	names := make([]string, len(labelValues))
	for i, v := range labelValues {
		names[i] = string(v)
	}

	return names, nil
}

// parseResult converts Prometheus model.Value to QueryResult
func (c *Client) parseResult(value model.Value) ([]QueryResult, error) {
	results := make([]QueryResult, 0)

	switch v := value.(type) {
	case model.Vector:
		// Instant vector
		for _, sample := range v {
			results = append(results, QueryResult{
				Timestamp: sample.Timestamp.Time(),
				Value:     float64(sample.Value),
				Labels:    metricToMap(sample.Metric),
				Raw:       value,
			})
		}

	case model.Matrix:
		// Range vector
		for _, stream := range v {
			for _, sample := range stream.Values {
				results = append(results, QueryResult{
					Timestamp: sample.Timestamp.Time(),
					Value:     float64(sample.Value),
					Labels:    metricToMap(stream.Metric),
					Raw:       value,
				})
			}
		}

	case *model.Scalar:
		// Scalar value
		results = append(results, QueryResult{
			Timestamp: v.Timestamp.Time(),
			Value:     float64(v.Value),
			Labels:    make(map[string]string),
			Raw:       value,
		})

	case *model.String:
		// String value (rare)
		return nil, fmt.Errorf("string result type not supported")

	default:
		return nil, fmt.Errorf("unknown result type: %T", value)
	}

	return results, nil
}

// metricToMap converts model.Metric to map[string]string
func metricToMap(metric model.Metric) map[string]string {
	labels := make(map[string]string)
	for k, v := range metric {
		labels[string(k)] = string(v)
	}
	return labels
}

// GetLatestValue is a convenience method to get the latest single value from a query
func (c *Client) GetLatestValue(ctx context.Context, query string) (float64, error) {
	results, err := c.QueryLatest(ctx, query)
	if err != nil {
		return 0, err
	}

	if len(results) == 0 {
		return 0, fmt.Errorf("query returned no results")
	}

	// Return the first result's value
	return results[0].Value, nil
}

// CheckMetricExists checks if a metric exists in Prometheus
func (c *Client) CheckMetricExists(ctx context.Context, metricName string) (bool, error) {
	query := fmt.Sprintf("%s[1m]", metricName)
	results, err := c.QueryLatest(ctx, query)
	if err != nil {
		// If query fails, metric might not exist
		return false, nil
	}

	return len(results) > 0, nil
}
