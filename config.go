package submux

import "time"

// config holds the configuration for a SubMux instance.
type config struct {
	// autoResubscribe enables automatic resubscription when hashslot migrations occur.
	autoResubscribe bool

	// minConnectionsPerNode sets the minimum number of connections per shard node.
	minConnectionsPerNode int

	// replicaPreference sets preference for using replica nodes over master nodes.
	replicaPreference bool

	// topologyPollInterval sets how often to poll the cluster topology for changes.
	topologyPollInterval time.Duration
}

// defaultConfig returns the default configuration.
func defaultConfig() *config {
	return &config{
		autoResubscribe:       false,
		minConnectionsPerNode: 1,
		replicaPreference:     false,
		topologyPollInterval:  1 * time.Second, // Default: poll at least once per second
	}
}

// Option is a function type for configuring a SubMux instance.
type Option func(*config)

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

// WithReplicaPreference sets preference for using replica nodes over master nodes.
func WithReplicaPreference(preferReplicas bool) Option {
	return func(c *config) {
		c.replicaPreference = preferReplicas
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
