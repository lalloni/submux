package submux

import (
	"log/slog"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := defaultConfig()

	if cfg.autoResubscribe != false {
		t.Errorf("autoResubscribe = %v, want false", cfg.autoResubscribe)
	}
	if cfg.minConnectionsPerNode != 1 {
		t.Errorf("minConnectionsPerNode = %d, want 1", cfg.minConnectionsPerNode)
	}
	if cfg.nodePreference != BalancedAll {
		t.Errorf("nodePreference = %v, want BalancedAll", cfg.nodePreference)
	}
	if cfg.topologyPollInterval != 1*time.Second {
		t.Errorf("topologyPollInterval = %v, want 1s", cfg.topologyPollInterval)
	}
	if cfg.migrationTimeout != 30*time.Second {
		t.Errorf("migrationTimeout = %v, want 30s", cfg.migrationTimeout)
	}
	if cfg.migrationStallCheck != 2*time.Second {
		t.Errorf("migrationStallCheck = %v, want 2s", cfg.migrationStallCheck)
	}
	if cfg.logger == nil {
		t.Error("logger should not be nil")
	}
}

func TestWithMinConnectionsPerNode(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		expected int
	}{
		{"positive value", 5, 5},
		{"value of 1", 1, 1},
		{"zero clamped to 1", 0, 1},
		{"negative clamped to 1", -5, 1},
		{"large negative clamped to 1", -1000, 1},
		{"large positive value", 100, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			WithMinConnectionsPerNode(tt.input)(cfg)

			if cfg.minConnectionsPerNode != tt.expected {
				t.Errorf("minConnectionsPerNode = %d, want %d", cfg.minConnectionsPerNode, tt.expected)
			}
		})
	}
}

func TestWithTopologyPollInterval(t *testing.T) {
	tests := []struct {
		name     string
		input    time.Duration
		expected time.Duration
	}{
		{"normal interval", 5 * time.Second, 5 * time.Second},
		{"minimum allowed", 100 * time.Millisecond, 100 * time.Millisecond},
		{"below minimum clamped", 50 * time.Millisecond, 100 * time.Millisecond},
		{"zero clamped", 0, 100 * time.Millisecond},
		{"negative clamped", -1 * time.Second, 100 * time.Millisecond},
		{"1 millisecond clamped", 1 * time.Millisecond, 100 * time.Millisecond},
		{"99 milliseconds clamped", 99 * time.Millisecond, 100 * time.Millisecond},
		{"large interval", 1 * time.Hour, 1 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			WithTopologyPollInterval(tt.input)(cfg)

			if cfg.topologyPollInterval != tt.expected {
				t.Errorf("topologyPollInterval = %v, want %v", cfg.topologyPollInterval, tt.expected)
			}
		})
	}
}

func TestWithMigrationTimeout(t *testing.T) {
	tests := []struct {
		name     string
		input    time.Duration
		expected time.Duration
	}{
		{"normal timeout", 30 * time.Second, 30 * time.Second},
		{"minimum allowed", 1 * time.Second, 1 * time.Second},
		{"below minimum clamped", 500 * time.Millisecond, 1 * time.Second},
		{"999 milliseconds clamped", 999 * time.Millisecond, 1 * time.Second},
		{"zero clamped", 0, 1 * time.Second},
		{"negative clamped", -5 * time.Second, 1 * time.Second},
		{"large timeout", 5 * time.Minute, 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			WithMigrationTimeout(tt.input)(cfg)

			if cfg.migrationTimeout != tt.expected {
				t.Errorf("migrationTimeout = %v, want %v", cfg.migrationTimeout, tt.expected)
			}
		})
	}
}

func TestWithMigrationStallCheck(t *testing.T) {
	tests := []struct {
		name     string
		input    time.Duration
		expected time.Duration
	}{
		{"normal interval", 2 * time.Second, 2 * time.Second},
		{"minimum allowed", 100 * time.Millisecond, 100 * time.Millisecond},
		{"below minimum clamped", 50 * time.Millisecond, 100 * time.Millisecond},
		{"99 milliseconds clamped", 99 * time.Millisecond, 100 * time.Millisecond},
		{"zero clamped", 0, 100 * time.Millisecond},
		{"negative clamped", -1 * time.Second, 100 * time.Millisecond},
		{"large interval", 1 * time.Minute, 1 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			WithMigrationStallCheck(tt.input)(cfg)

			if cfg.migrationStallCheck != tt.expected {
				t.Errorf("migrationStallCheck = %v, want %v", cfg.migrationStallCheck, tt.expected)
			}
		})
	}
}

func TestWithAutoResubscribe(t *testing.T) {
	tests := []struct {
		name     string
		input    bool
		expected bool
	}{
		{"enable", true, true},
		{"disable", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			WithAutoResubscribe(tt.input)(cfg)

			if cfg.autoResubscribe != tt.expected {
				t.Errorf("autoResubscribe = %v, want %v", cfg.autoResubscribe, tt.expected)
			}
		})
	}
}

func TestWithNodePreference(t *testing.T) {
	tests := []struct {
		name     string
		input    NodePreference
		expected NodePreference
	}{
		{"prefer masters", PreferMasters, PreferMasters},
		{"balanced all", BalancedAll, BalancedAll},
		{"prefer replicas", PreferReplicas, PreferReplicas},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			WithNodePreference(tt.input)(cfg)

			if cfg.nodePreference != tt.expected {
				t.Errorf("nodePreference = %v, want %v", cfg.nodePreference, tt.expected)
			}
		})
	}
}

func TestWithReplicaPreference(t *testing.T) {
	tests := []struct {
		name     string
		input    bool
		expected NodePreference
	}{
		{"prefer replicas true", true, PreferReplicas},
		{"prefer replicas false", false, PreferMasters},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			WithReplicaPreference(tt.input)(cfg)

			if cfg.nodePreference != tt.expected {
				t.Errorf("nodePreference = %v, want %v", cfg.nodePreference, tt.expected)
			}
		})
	}
}

func TestWithLogger(t *testing.T) {
	// Test with custom logger
	customLogger := slog.Default().With("test", "value")

	cfg := defaultConfig()
	WithLogger(customLogger)(cfg)

	if cfg.logger != customLogger {
		t.Error("logger was not set correctly")
	}

	// Test with nil logger (should set to nil)
	cfg2 := defaultConfig()
	WithLogger(nil)(cfg2)

	if cfg2.logger != nil {
		t.Error("nil logger should be allowed")
	}
}

func TestMultipleOptions(t *testing.T) {
	cfg := defaultConfig()

	// Apply multiple options
	options := []Option{
		WithMinConnectionsPerNode(10),
		WithTopologyPollInterval(5 * time.Second),
		WithMigrationTimeout(60 * time.Second),
		WithMigrationStallCheck(5 * time.Second),
		WithAutoResubscribe(true),
		WithReplicaPreference(true),
	}

	for _, opt := range options {
		opt(cfg)
	}

	if cfg.minConnectionsPerNode != 10 {
		t.Errorf("minConnectionsPerNode = %d, want 10", cfg.minConnectionsPerNode)
	}
	if cfg.topologyPollInterval != 5*time.Second {
		t.Errorf("topologyPollInterval = %v, want 5s", cfg.topologyPollInterval)
	}
	if cfg.migrationTimeout != 60*time.Second {
		t.Errorf("migrationTimeout = %v, want 60s", cfg.migrationTimeout)
	}
	if cfg.migrationStallCheck != 5*time.Second {
		t.Errorf("migrationStallCheck = %v, want 5s", cfg.migrationStallCheck)
	}
	if cfg.autoResubscribe != true {
		t.Errorf("autoResubscribe = %v, want true", cfg.autoResubscribe)
	}
	if cfg.nodePreference != PreferReplicas {
		t.Errorf("nodePreference = %v, want PreferReplicas", cfg.nodePreference)
	}
}

func TestOptionOverwrite(t *testing.T) {
	cfg := defaultConfig()

	// Apply options that overwrite each other
	WithMinConnectionsPerNode(5)(cfg)
	WithMinConnectionsPerNode(10)(cfg)

	if cfg.minConnectionsPerNode != 10 {
		t.Errorf("Last option should win: minConnectionsPerNode = %d, want 10", cfg.minConnectionsPerNode)
	}
}
