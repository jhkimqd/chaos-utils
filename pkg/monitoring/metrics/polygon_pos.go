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

	// CometBFT consensus safety metrics
	ByzantineValidators      string
	ConsensusRounds          string
	MissingValidatorsPower   string
	ConsensusMissedBlocks    string
	SideTxConsensusFailures  string

	// Bor chain safety metrics
	ReorgExecutes string
	ReorgDropped  string
	FinalityGap   string

	// Block timing & span rotation metrics
	BlockIntervalAvg       string
	SpanAPICallSuccessRate string

	// Bor-side span request metrics
	BorSpanRequestSuccessRate string

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

		// CometBFT consensus safety metrics
		ByzantineValidators:     `max(cometbft_consensus_byzantine_validators{job=~"l2-cl-.*-heimdall-v2-bor-validator"}) or vector(0)`,
		ConsensusRounds:         `max(cometbft_consensus_rounds{job=~"l2-cl-.*-heimdall-v2-bor-validator"})`,
		MissingValidatorsPower:  `max(cometbft_consensus_missing_validators_power{job=~"l2-cl-.*-heimdall-v2-bor-validator"})`,
		ConsensusMissedBlocks:   `sum(rate(cometbft_consensus_missing_validators{job=~"l2-cl-.*-heimdall-v2-bor-validator"}[2m]))`,
		SideTxConsensusFailures: `sum(rate(heimdallv2_sidetx_consensus_failures_total[3m])) or vector(0)`,

		// Bor chain safety metrics
		ReorgExecutes: `sum(increase(chain_reorg_executes{job=~"l2-el-.*-bor-heimdall-v2-validator"}[5m])) or vector(0)`,
		ReorgDropped:  `sum(increase(chain_reorg_drop{job=~"l2-el-.*-bor-heimdall-v2-validator"}[5m])) or vector(0)`,
		FinalityGap:   `max(chain_head_block{job=~"l2-el-.*-bor-heimdall-v2-validator"}) - max(chain_head_finalized{job=~"l2-el-.*-bor-heimdall-v2-validator"})`,

		// Block timing & span rotation
		BlockIntervalAvg:       "rate(cometbft_consensus_block_interval_seconds_sum[2m]) / clamp_min(rate(cometbft_consensus_block_interval_seconds_count[2m]), 0.001)",
		SpanAPICallSuccessRate: "(sum(rate(heimdallv2_bor_api_calls_success_total[5m])) / clamp_min(sum(rate(heimdallv2_bor_api_calls_total[5m])), 0.001)) or vector(1)",

		// Bor-side span requests
		BorSpanRequestSuccessRate: "(sum(rate(client_requests_span_valid[5m])) / clamp_min(sum(rate(client_requests_span_valid[5m])) + sum(rate(client_requests_span_invalid[5m])), 0.001)) or vector(1)",

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

		// CometBFT consensus safety metrics
		{
			Name:        "cometbft_consensus_byzantine_validators",
			Query:       "cometbft_consensus_byzantine_validators",
			Description: "Number of validators detected as byzantine (double-signing) — must always be 0",
			Type:        "gauge",
			Labels:      []string{"service"},
		},
		{
			Name:        "cometbft_consensus_rounds",
			Query:       "cometbft_consensus_rounds",
			Description: "Current consensus round — values > 0 indicate proposal disagreements",
			Type:        "gauge",
			Labels:      []string{"service"},
		},
		{
			Name:        "cometbft_consensus_missing_validators_power",
			Query:       "cometbft_consensus_missing_validators_power",
			Description: "Voting power of validators missing from consensus — if > 1/3 total, no blocks finalize",
			Type:        "gauge",
			Labels:      []string{"service"},
		},
		{
			Name:        "cometbft_consensus_late_votes",
			Query:       "cometbft_consensus_late_votes",
			Description: "Votes from earlier rounds arriving late — spikes during partition healing",
			Type:        "counter",
			Labels:      []string{"service", "vote_type"},
		},
		{
			Name:        "heimdallv2_sidetx_consensus_failures_total",
			Query:       "heimdallv2_sidetx_consensus_failures_total",
			Description: "Side-tx consensus failures — non-zero indicates Heimdall consensus degradation",
			Type:        "counter",
			Labels:      []string{"service"},
		},
		{
			Name:        "heimdallv2_sidetx_consensus_approved_total",
			Query:       "heimdallv2_sidetx_consensus_approved_total",
			Description: "Side-tx consensus approvals — healthy rate indicates normal Heimdall operation",
			Type:        "counter",
			Labels:      []string{"service"},
		},

		// Bor chain safety metrics
		{
			Name:        "chain_reorg_executes",
			Query:       "chain_reorg_executes",
			Description: "Chain reorganization events — non-zero indicates fork resolution occurred",
			Type:        "counter",
			Labels:      []string{"service"},
		},
		{
			Name:        "chain_reorg_drop",
			Query:       "chain_reorg_drop",
			Description: "Blocks dropped during chain reorganization — quantifies reorg depth",
			Type:        "counter",
			Labels:      []string{"service"},
		},
		{
			Name:        "chain_head_finalized",
			Query:       "chain_head_finalized",
			Description: "Latest finalized block number — gap from chain_head_block indicates finality lag",
			Type:        "gauge",
			Labels:      []string{"service"},
		},

		// Block timing metrics (slow block detection)
		{
			Name:        "cometbft_consensus_block_interval_seconds_sum",
			Query:       "cometbft_consensus_block_interval_seconds_sum",
			Description: "Sum of block interval durations — used with _count to compute average block time",
			Type:        "counter",
			Labels:      []string{"service"},
		},
		{
			Name:        "cometbft_consensus_block_interval_seconds_count",
			Query:       "cometbft_consensus_block_interval_seconds_count",
			Description: "Count of block intervals — used with _sum to compute average block time",
			Type:        "counter",
			Labels:      []string{"service"},
		},

		// Span rotation metrics
		{
			Name:        "heimdallv2_bor_api_calls_total",
			Query:       "heimdallv2_bor_api_calls_total",
			Description: "Total Bor API calls to Heimdall (span/milestone queries)",
			Type:        "counter",
			Labels:      []string{"service", "endpoint"},
		},
		{
			Name:        "heimdallv2_bor_api_calls_success_total",
			Query:       "heimdallv2_bor_api_calls_success_total",
			Description: "Successful Bor API calls to Heimdall — low success rate indicates span rotation failures",
			Type:        "counter",
			Labels:      []string{"service", "endpoint"},
		},

		// Bor-side span request metrics
		{
			Name:        "client_requests_span_valid",
			Query:       "client_requests_span_valid",
			Description: "Successful Bor span fetch requests to Heimdall",
			Type:        "counter",
			Labels:      []string{"service"},
		},
		{
			Name:        "client_requests_span_invalid",
			Query:       "client_requests_span_invalid",
			Description: "Failed Bor span fetch requests to Heimdall — indicates span rotation failures",
			Type:        "counter",
			Labels:      []string{"service"},
		},
		{
			Name:        "client_requests_span_duration",
			Query:       "client_requests_span_duration",
			Description: "Duration of Bor span fetch requests to Heimdall",
			Type:        "histogram",
			Labels:      []string{"service"},
		},
		{
			Name:        "client_requests_latestspan_valid",
			Query:       "client_requests_latestspan_valid",
			Description: "Successful Bor latest-span fetch requests to Heimdall",
			Type:        "counter",
			Labels:      []string{"service"},
		},
		{
			Name:        "client_requests_latestspan_invalid",
			Query:       "client_requests_latestspan_invalid",
			Description: "Failed Bor latest-span fetch requests to Heimdall — indicates span rotation failures",
			Type:        "counter",
			Labels:      []string{"service"},
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
