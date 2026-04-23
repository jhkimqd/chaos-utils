package metrics

// PolygonPoSSLI defines Service Level Indicators for Polygon Chain networks
type PolygonPoSSLI struct {
	// Bor (L2 Execution Layer) metrics
	BorBlockProductionRate string
	BorRPCLatencyP95       string
	BorRPCLatencyP99       string
	BorPeerCount           string
	BorBlockGasUsed        string

	// Heimdall (L2 Consensus Layer) metrics
	HeimdallConsensusHeight     string
	HeimdallCheckpointSubmitted string
	HeimdallCheckpointLatency   string
	HeimdallPeerCount           string
	HeimdallValidatorPower      string

	// L1 Integration metrics
	L1RPCLatency        string
	L1CheckpointSuccess string
	L1StateSync         string

	// RabbitMQ metrics
	RabbitMQQueueDepth      string
	RabbitMQConnectionCount string
	RabbitMQMessageRate     string
}

// DefaultPolygonPoSSLI returns the default SLI queries for Polygon Chain
func DefaultPolygonPosSLI() PolygonPoSSLI {
	return PolygonPoSSLI{
		// Bor metrics
		BorBlockProductionRate: "rate(bor_blockchain_head_block[1m])",
		BorRPCLatencyP95:       "histogram_quantile(0.95, rate(bor_rpc_duration_seconds_bucket[5m]))",
		BorRPCLatencyP99:       "histogram_quantile(0.99, rate(bor_rpc_duration_seconds_bucket[5m]))",
		BorPeerCount:           "bor_p2p_peers",
		BorBlockGasUsed:        "rate(bor_block_gas_used[1m])",

		// Heimdall metrics
		HeimdallConsensusHeight:     "rate(heimdall_consensus_height[1m])",
		HeimdallCheckpointSubmitted: "rate(heimdall_checkpoint_submitted_total[1h])",
		HeimdallCheckpointLatency:   "histogram_quantile(0.95, rate(heimdall_checkpoint_duration_seconds_bucket[5m]))",
		HeimdallPeerCount:           "heimdall_p2p_peers",
		HeimdallValidatorPower:      "heimdall_validator_voting_power",

		// L1 Integration
		L1RPCLatency:        "histogram_quantile(0.95, rate(l1_rpc_duration_seconds_bucket[5m]))",
		L1CheckpointSuccess: "rate(l1_checkpoint_success_total[1h])",
		L1StateSync:         "rate(l1_state_sync_events[5m])",

		// RabbitMQ
		RabbitMQQueueDepth:      "rabbitmq_queue_messages_ready",
		RabbitMQConnectionCount: "rabbitmq_connections",
		RabbitMQMessageRate:     "rate(rabbitmq_queue_messages_published_total[1m])",
	}
}

// MetricDefinition describes a Prometheus metric
type MetricDefinition struct {
	Name        string
	Query       string
	Description string
	Type        string // counter, gauge, histogram
	Labels      []string
}

// GetAllMetrics returns all defined Polygon Chain metrics
func GetAllMetrics() []MetricDefinition {
	return []MetricDefinition{
		// Bor metrics
		{
			Name:        "bor_blockchain_head_block",
			Query:       "bor_blockchain_head_block",
			Description: "Current head block number in Bor blockchain",
			Type:        "gauge",
			Labels:      []string{"service"},
		},
		{
			Name:        "bor_block_production_rate",
			Query:       "rate(bor_blockchain_head_block[1m])",
			Description: "Rate of block production per second",
			Type:        "gauge",
			Labels:      []string{"service"},
		},
		{
			Name:        "bor_rpc_duration_seconds",
			Query:       "bor_rpc_duration_seconds",
			Description: "RPC call duration histogram",
			Type:        "histogram",
			Labels:      []string{"service", "method"},
		},
		{
			Name:        "bor_p2p_peers",
			Query:       "bor_p2p_peers",
			Description: "Number of connected P2P peers",
			Type:        "gauge",
			Labels:      []string{"service"},
		},

		// Heimdall metrics
		{
			Name:        "heimdall_consensus_height",
			Query:       "heimdall_consensus_height",
			Description: "Current consensus height in Heimdall",
			Type:        "gauge",
			Labels:      []string{"service"},
		},
		{
			Name:        "heimdall_checkpoint_submitted_total",
			Query:       "heimdall_checkpoint_submitted_total",
			Description: "Total number of checkpoints submitted",
			Type:        "counter",
			Labels:      []string{"service"},
		},
		{
			Name:        "heimdall_checkpoint_submitted",
			Query:       "rate(heimdall_checkpoint_submitted_total[5m])",
			Description: "Rate of checkpoint submissions",
			Type:        "gauge",
			Labels:      []string{"service"},
		},
		{
			Name:        "heimdall_p2p_peers",
			Query:       "heimdall_p2p_peers",
			Description: "Number of connected Heimdall peers",
			Type:        "gauge",
			Labels:      []string{"service"},
		},
		{
			Name:        "heimdall_validator_voting_power",
			Query:       "heimdall_validator_voting_power",
			Description: "Validator voting power",
			Type:        "gauge",
			Labels:      []string{"service", "validator"},
		},

		// RabbitMQ metrics
		{
			Name:        "rabbitmq_queue_messages_ready",
			Query:       "rabbitmq_queue_messages_ready",
			Description: "Number of messages ready in queue",
			Type:        "gauge",
			Labels:      []string{"queue", "vhost"},
		},
		{
			Name:        "rabbitmq_connections",
			Query:       "rabbitmq_connections",
			Description: "Number of active RabbitMQ connections",
			Type:        "gauge",
			Labels:      []string{},
		},
		{
			Name:        "rabbitmq_queue_messages_published_total",
			Query:       "rabbitmq_queue_messages_published_total",
			Description: "Total messages published to queue",
			Type:        "counter",
			Labels:      []string{"queue", "vhost"},
		},
	}
}

// GetMetricByName returns a metric definition by name
func GetMetricByName(name string) *MetricDefinition {
	for _, metric := range GetAllMetrics() {
		if metric.Name == name {
			return &metric
		}
	}
	return nil
}

// ValidateQuery checks if a Prometheus query is syntactically valid (basic check)
func ValidateQuery(query string) bool {
	// Basic validation: non-empty and doesn't contain obvious syntax errors
	if query == "" {
		return false
	}

	// Check for balanced parentheses
	parenCount := 0
	for _, ch := range query {
		if ch == '(' {
			parenCount++
		} else if ch == ')' {
			parenCount--
		}
		if parenCount < 0 {
			return false
		}
	}

	return parenCount == 0
}
