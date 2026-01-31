package submux

import (
	"context"
	"log/slog"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestNoopMetrics_NoOp verifies that no-op metrics don't panic
func TestNoopMetrics_NoOp(t *testing.T) {
	recorder := &noopMetrics{}

	// Call all methods - should not panic
	recorder.recordMessageReceived("subscribe", "node1:6379")
	recorder.recordCallbackInvocation("subscribe")
	recorder.recordCallbackPanic("subscribe")
	recorder.recordSubscriptionAttempt("subscribe", true)
	recorder.recordConnectionCreated("node1:6379")
	recorder.recordConnectionFailed("node1:6379", "timeout")
	recorder.recordMigrationStarted()
	recorder.recordMigrationCompleted()
	recorder.recordMigrationStalled()
	recorder.recordMigrationTimeout()
	recorder.recordTopologyRefresh(true)
	recorder.recordCallbackLatency("subscribe", 10*time.Millisecond)
	recorder.recordMessageLatency("subscribe", 5*time.Millisecond)
	recorder.recordMigrationDuration(100 * time.Millisecond)
	recorder.recordTopologyRefreshLatency(50 * time.Millisecond)
}

// TestNewMetricsRecorder_NilProvider verifies nil provider creates no-op recorder
func TestNewMetricsRecorder_NilProvider(t *testing.T) {
	recorder := newMetricsRecorder(nil, slog.Default())

	// Should be a no-op recorder
	_, ok := recorder.(*noopMetrics)
	if !ok {
		t.Errorf("expected noopMetrics, got %T", recorder)
	}
}

// TestNewMetricsRecorder_WithProvider verifies provider creates OTEL recorder
func TestNewMetricsRecorder_WithProvider(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	recorder := newMetricsRecorder(provider, slog.Default())

	// Should be an OTEL recorder
	_, ok := recorder.(*otelMetrics)
	if !ok {
		t.Errorf("expected otelMetrics, got %T", recorder)
	}
}

// TestOtelMetrics_CounterRecording tests that counters are recorded correctly
func TestOtelMetrics_CounterRecording(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	recorder := newMetricsRecorder(provider, slog.Default())

	// Record some metrics
	recorder.recordMessageReceived("subscribe", "node1:6379")
	recorder.recordMessageReceived("subscribe", "node1:6379")
	recorder.recordCallbackInvocation("psubscribe")
	recorder.recordSubscriptionAttempt("ssubscribe", true)
	recorder.recordSubscriptionAttempt("subscribe", false)
	recorder.recordConnectionCreated("node2:6379")
	recorder.recordMigrationStarted()

	// Collect metrics
	rm := &metricdata.ResourceMetrics{}
	err := reader.Collect(context.Background(), rm)
	if err != nil {
		t.Fatalf("failed to collect metrics: %v", err)
	}

	// Verify we have metrics
	if len(rm.ScopeMetrics) == 0 {
		t.Fatal("no scope metrics collected")
	}

	metrics := rm.ScopeMetrics[0].Metrics
	if len(metrics) == 0 {
		t.Fatal("no metrics collected")
	}

	// Build a map of metric names to values for easy verification
	metricValues := make(map[string]int64)
	for _, m := range metrics {
		if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
			for _, dp := range sum.DataPoints {
				metricValues[m.Name] += dp.Value
			}
		}
	}

	// Verify expected metrics
	tests := []struct {
		name     string
		expected int64
	}{
		{"submux.messages.received", 2},
		{"submux.callbacks.invoked", 1},
		{"submux.subscriptions.attempts", 2},
		{"submux.connections.created", 1},
		{"submux.migrations.started", 1},
	}

	for _, tt := range tests {
		if got := metricValues[tt.name]; got != tt.expected {
			t.Errorf("%s = %d, want %d", tt.name, got, tt.expected)
		}
	}
}

// TestOtelMetrics_HistogramRecording tests that histograms are recorded correctly
func TestOtelMetrics_HistogramRecording(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	recorder := newMetricsRecorder(provider, slog.Default())

	// Record histogram metrics
	recorder.recordCallbackLatency("subscribe", 10*time.Millisecond)
	recorder.recordCallbackLatency("subscribe", 25*time.Millisecond)
	recorder.recordMessageLatency("psubscribe", 5*time.Millisecond)
	recorder.recordMigrationDuration(500 * time.Millisecond)
	recorder.recordTopologyRefreshLatency(100 * time.Millisecond)

	// Collect metrics
	rm := &metricdata.ResourceMetrics{}
	err := reader.Collect(context.Background(), rm)
	if err != nil {
		t.Fatalf("failed to collect metrics: %v", err)
	}

	// Verify we have metrics
	if len(rm.ScopeMetrics) == 0 {
		t.Fatal("no scope metrics collected")
	}

	metrics := rm.ScopeMetrics[0].Metrics
	if len(metrics) == 0 {
		t.Fatal("no metrics collected")
	}

	// Count histograms
	histogramCount := 0
	for _, m := range metrics {
		if _, ok := m.Data.(metricdata.Histogram[float64]); ok {
			histogramCount++
		}
	}

	// We should have at least the histograms we recorded
	if histogramCount < 4 {
		t.Errorf("expected at least 4 histograms, got %d", histogramCount)
	}
}

// TestOtelMetrics_Attributes verifies attributes are set correctly
func TestOtelMetrics_Attributes(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	recorder := newMetricsRecorder(provider, slog.Default())

	// Record metrics with different attributes
	recorder.recordMessageReceived("subscribe", "node1:6379")
	recorder.recordMessageReceived("psubscribe", "node2:6379")
	recorder.recordSubscriptionAttempt("ssubscribe", true)
	recorder.recordSubscriptionAttempt("subscribe", false)

	// Collect metrics
	rm := &metricdata.ResourceMetrics{}
	err := reader.Collect(context.Background(), rm)
	if err != nil {
		t.Fatalf("failed to collect metrics: %v", err)
	}

	// Verify attributes exist
	metrics := rm.ScopeMetrics[0].Metrics
	foundAttributes := false
	for _, m := range metrics {
		if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
			for _, dp := range sum.DataPoints {
				if len(dp.Attributes.ToSlice()) > 0 {
					foundAttributes = true
					break
				}
			}
		}
	}

	if !foundAttributes {
		t.Error("no attributes found in metrics")
	}
}

// TestSubscriptionTypeToString verifies subscription type conversion
func TestSubscriptionTypeToString(t *testing.T) {
	tests := []struct {
		subType  subscriptionType
		expected string
	}{
		{subTypeSubscribe, "subscribe"},
		{subTypePSubscribe, "psubscribe"},
		{subTypeSSubscribe, "ssubscribe"},
		{subscriptionType(99), "unknown"},
	}

	for _, tt := range tests {
		got := subscriptionTypeToString(tt.subType)
		if got != tt.expected {
			t.Errorf("subscriptionTypeToString(%v) = %q, want %q", tt.subType, got, tt.expected)
		}
	}
}

// BenchmarkNoopMetrics_Overhead benchmarks no-op recorder overhead
func BenchmarkNoopMetrics_Overhead(b *testing.B) {
	recorder := &noopMetrics{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recorder.recordMessageReceived("subscribe", "node1:6379")
	}
}

// BenchmarkOtelMetrics_CounterOverhead benchmarks OTEL counter overhead
func BenchmarkOtelMetrics_CounterOverhead(b *testing.B) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	recorder := newMetricsRecorder(provider, slog.Default())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recorder.recordMessageReceived("subscribe", "node1:6379")
	}
}

// BenchmarkOtelMetrics_HistogramOverhead benchmarks OTEL histogram overhead
func BenchmarkOtelMetrics_HistogramOverhead(b *testing.B) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	recorder := newMetricsRecorder(provider, slog.Default())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recorder.recordCallbackLatency("subscribe", 10*time.Millisecond)
	}
}

// BenchmarkOtelMetrics_MultipleMetrics benchmarks recording multiple metrics
func BenchmarkOtelMetrics_MultipleMetrics(b *testing.B) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	recorder := newMetricsRecorder(provider, slog.Default())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recorder.recordMessageReceived("subscribe", "node1:6379")
		recorder.recordCallbackInvocation("subscribe")
		recorder.recordCallbackLatency("subscribe", 10*time.Millisecond)
	}
}
