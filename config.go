package submux

import (
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/metric"
)

// config holds the configuration for a SubMux instance.
type config struct {
	// autoResubscribe enables automatic resubscription when hashslot migrations occur.
	autoResubscribe bool

	// minConnectionsPerNode sets the minimum number of connections per shard node.
	minConnectionsPerNode int

	// nodePreference determines the strategy for distributing subscriptions across cluster nodes.
	nodePreference NodePreference

	// topologyPollInterval sets how often to poll the cluster topology for changes.
	topologyPollInterval time.Duration

	// migrationTimeout is the maximum duration to wait for migration resubscription to complete.
	migrationTimeout time.Duration

	// migrationStallCheck is how often to check for stalled migration resubscription progress.
	migrationStallCheck time.Duration

	// logger is the structured logger to use.
	logger *slog.Logger

	// meterProvider is the OpenTelemetry MeterProvider for metrics collection.
	// If nil, metrics are disabled (no-op).
	meterProvider metric.MeterProvider

	// recorder is the internal metrics recorder created from meterProvider.
	// This is set during SubMux initialization.
	recorder metricsRecorder
}

// defaultConfig returns the default configuration.
func defaultConfig() *config {
	return &config{
		autoResubscribe:       false,
		minConnectionsPerNode: 1,
		nodePreference:        BalancedAll,      // Default: distribute equally across all nodes
		topologyPollInterval:  1 * time.Second,  // Default: poll at least once per second
		migrationTimeout:      30 * time.Second, // Default: 30s max for migration resubscription
		migrationStallCheck:   2 * time.Second,  // Default: check for stalls every 2s
		logger:                slog.Default(),
		meterProvider:         nil,         // Default: metrics disabled
		recorder:              &noopMetrics{}, // Default: no-op recorder
	}
}

// Option is a function type for configuring a SubMux instance.
type Option func(*config)

// WithLogger sets the structured logger to use.
func WithLogger(logger *slog.Logger) Option {
	return func(c *config) {
		c.logger = logger
	}
}

// WithAutoResubscribe enables automatic resubscription when hashslot migrations occur.
func WithAutoResubscribe(enabled bool) Option {
	return func(c *config) {
		c.autoResubscribe = enabled
	}
}

// WithMinConnectionsPerNode sets the minimum number of connections per shard node (for load balancing).
func WithMinConnectionsPerNode(count int) Option {
	return func(c *config) {
		if count < 1 {
			count = 1
		}
		c.minConnectionsPerNode = count
	}
}

// WithNodePreference sets the strategy for distributing subscriptions across cluster nodes.
// Available options:
//   - PreferMasters: Route subscriptions to master nodes only (legacy behavior)
//   - BalancedAll: Distribute equally across all nodes - masters and replicas (recommended default)
//   - PreferReplicas: Prefer replicas to protect write-saturated masters
func WithNodePreference(preference NodePreference) Option {
	return func(c *config) {
		c.nodePreference = preference
	}
}

// WithReplicaPreference sets preference for using replica nodes over master nodes.
// Deprecated: Use WithNodePreference(PreferReplicas) instead for clearer intent,
// or WithNodePreference(BalancedAll) for better default behavior.
func WithReplicaPreference(preferReplicas bool) Option {
	return func(c *config) {
		if preferReplicas {
			c.nodePreference = PreferReplicas
		} else {
			c.nodePreference = PreferMasters
		}
	}
}

// WithTopologyPollInterval sets how often to poll the cluster topology for changes.
// The minimum recommended interval is 1 second. Shorter intervals may increase
// load on the Redis cluster.
func WithTopologyPollInterval(interval time.Duration) Option {
	return func(c *config) {
		if interval < 100*time.Millisecond {
			// Enforce minimum interval of 100ms to prevent excessive polling
			interval = 100 * time.Millisecond
		}
		c.topologyPollInterval = interval
	}
}

// WithMigrationTimeout sets the maximum duration to wait for migration resubscription
// to complete before timing out. Default is 30 seconds.
func WithMigrationTimeout(timeout time.Duration) Option {
	return func(c *config) {
		if timeout < 1*time.Second {
			// Enforce minimum timeout of 1 second
			timeout = 1 * time.Second
		}
		c.migrationTimeout = timeout
	}
}

// WithMigrationStallCheck sets how often to check for stalled migration resubscription
// progress. Default is 2 seconds.
func WithMigrationStallCheck(interval time.Duration) Option {
	return func(c *config) {
		if interval < 100*time.Millisecond {
			// Enforce minimum interval of 100ms
			interval = 100 * time.Millisecond
		}
		c.migrationStallCheck = interval
	}
}

// WithMeterProvider sets the OpenTelemetry MeterProvider for metrics collection.
// Metrics are opt-in - if not provided, all metrics operations become no-ops with zero overhead.
//
// Example usage with Prometheus:
//
//	import (
//	    sdkmetric "go.opentelemetry.io/otel/sdk/metric"
//	    "go.opentelemetry.io/otel/exporters/prometheus"
//	)
//
//	exporter, _ := prometheus.New()
//	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
//	subMux, _ := submux.New(clusterClient, submux.WithMeterProvider(provider))
//
func WithMeterProvider(provider metric.MeterProvider) Option {
	return func(c *config) {
		c.meterProvider = provider
	}
}
