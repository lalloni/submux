package submux

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
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
	recorder.recordWorkerPoolSubmission(false)
	recorder.recordWorkerPoolSubmission(true)
	recorder.recordWorkerPoolDropped()
	recorder.recordWorkerPoolQueueWait(5 * time.Millisecond)
	recorder.registerWorkerPoolGauges(nil)

	// Also test with a real pool
	pool := NewWorkerPool(2, 10)
	recorder.registerWorkerPoolGauges(pool)
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

// TestOtelMetrics_WorkerPoolGauges tests that worker pool gauges work correctly
func TestOtelMetrics_WorkerPoolGauges(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	recorder := newMetricsRecorder(provider, slog.Default())

	// Create and start a worker pool
	pool := NewWorkerPool(2, 100)
	pool.Start()
	defer pool.Stop()

	// Register the gauges
	recorder.registerWorkerPoolGauges(pool)

	// Add some tasks to the queue to have a non-zero depth
	done := make(chan struct{})
	for i := 0; i < 5; i++ {
		pool.Submit(func() {
			<-done // Block until we're done collecting
		})
	}

	// Collect metrics
	rm := &metricdata.ResourceMetrics{}
	err := reader.Collect(context.Background(), rm)
	if err != nil {
		close(done)
		t.Fatalf("failed to collect metrics: %v", err)
	}

	close(done) // Release blocked workers

	// Verify we have metrics
	if len(rm.ScopeMetrics) == 0 {
		t.Fatal("no scope metrics collected")
	}

	// Find the gauge metrics
	foundQueueDepth := false
	foundQueueCapacity := false
	for _, m := range rm.ScopeMetrics[0].Metrics {
		if m.Name == "submux.workerpool.queue_depth" {
			foundQueueDepth = true
			if gauge, ok := m.Data.(metricdata.Gauge[int64]); ok {
				if len(gauge.DataPoints) > 0 {
					// Queue depth should be >= 0 (some tasks may have been processed)
					t.Logf("queue_depth = %d", gauge.DataPoints[0].Value)
				}
			}
		}
		if m.Name == "submux.workerpool.queue_capacity" {
			foundQueueCapacity = true
			if gauge, ok := m.Data.(metricdata.Gauge[int64]); ok {
				if len(gauge.DataPoints) > 0 && gauge.DataPoints[0].Value != 100 {
					t.Errorf("queue_capacity = %d, want 100", gauge.DataPoints[0].Value)
				}
			}
		}
	}

	if !foundQueueDepth {
		t.Error("queue_depth gauge not found")
	}
	if !foundQueueCapacity {
		t.Error("queue_capacity gauge not found")
	}
}

// TestOtelMetrics_PoolGauges tests the observable gauges for connections and subscriptions
func TestOtelMetrics_PoolGauges(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	recorder := newMetricsRecorder(provider, slog.Default())

	// Create pool and SubMux in minimal state for testing
	pool := &pubSubPool{
		nodePubSubs:    make(map[string][]*redis.PubSub),
		pubSubMetadata: make(map[*redis.PubSub]*pubSubMetadata),
	}

	subMux := &SubMux{
		subscriptions: make(map[string][]*subscription),
	}

	// Add some test data to verify counts
	// Add 2 mock connections
	mockPubSub1 := &redis.PubSub{}
	mockPubSub2 := &redis.PubSub{}
	pool.nodePubSubs["node1"] = []*redis.PubSub{mockPubSub1}
	pool.nodePubSubs["node2"] = []*redis.PubSub{mockPubSub2}

	// Add metadata with subscriptions
	meta1 := &pubSubMetadata{
		subscriptions: map[string][]*subscription{
			"channel1": {&subscription{}},
		},
	}
	meta2 := &pubSubMetadata{
		subscriptions: map[string][]*subscription{
			"channel2": {&subscription{}, &subscription{}},
		},
	}
	pool.pubSubMetadata[mockPubSub1] = meta1
	pool.pubSubMetadata[mockPubSub2] = meta2

	// Add SubMux subscriptions
	subMux.subscriptions["channel1"] = []*subscription{&subscription{}, &subscription{}}
	subMux.subscriptions["channel2"] = []*subscription{&subscription{}}

	// Register the gauges
	recorder.registerPoolGauges(pool, subMux)

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

	// Find and verify the gauge metrics
	foundConnections := false
	foundRedisSubscriptions := false
	foundSubmuxSubscriptions := false

	for _, m := range rm.ScopeMetrics[0].Metrics {
		switch m.Name {
		case "submux.connections.active":
			foundConnections = true
			if gauge, ok := m.Data.(metricdata.Gauge[int64]); ok {
				if len(gauge.DataPoints) > 0 {
					value := gauge.DataPoints[0].Value
					t.Logf("connections.active = %d", value)
					if value != 2 {
						t.Errorf("expected 2 connections, got %d", value)
					}
				}
			}

		case "submux.subscriptions.redis":
			foundRedisSubscriptions = true
			if gauge, ok := m.Data.(metricdata.Gauge[int64]); ok {
				if len(gauge.DataPoints) > 0 {
					value := gauge.DataPoints[0].Value
					t.Logf("subscriptions.redis = %d", value)
					if value != 3 { // meta1 has 1, meta2 has 2
						t.Errorf("expected 3 redis subscriptions, got %d", value)
					}
				}
			}

		case "submux.subscriptions.active":
			foundSubmuxSubscriptions = true
			if gauge, ok := m.Data.(metricdata.Gauge[int64]); ok {
				if len(gauge.DataPoints) > 0 {
					value := gauge.DataPoints[0].Value
					t.Logf("subscriptions.active = %d", value)
					if value != 3 { // channel1 has 2, channel2 has 1
						t.Errorf("expected 3 submux subscriptions, got %d", value)
					}
				}
			}
		}
	}

	if !foundConnections {
		t.Error("submux.connections.active gauge not found")
	}
	if !foundRedisSubscriptions {
		t.Error("submux.subscriptions.redis gauge not found")
	}
	if !foundSubmuxSubscriptions {
		t.Error("submux.subscriptions.active gauge not found")
	}
}

// TestOtelMetrics_WorkerPoolDropped tests the dropped counter
func TestOtelMetrics_WorkerPoolDropped(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	recorder := newMetricsRecorder(provider, slog.Default())

	// Record some dropped callbacks
	recorder.recordWorkerPoolDropped()
	recorder.recordWorkerPoolDropped()
	recorder.recordWorkerPoolDropped()

	// Collect metrics
	rm := &metricdata.ResourceMetrics{}
	err := reader.Collect(context.Background(), rm)
	if err != nil {
		t.Fatalf("failed to collect metrics: %v", err)
	}

	// Find the dropped counter
	for _, m := range rm.ScopeMetrics[0].Metrics {
		if m.Name == "submux.workerpool.dropped" {
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
				total := int64(0)
				for _, dp := range sum.DataPoints {
					total += dp.Value
				}
				if total != 3 {
					t.Errorf("dropped = %d, want 3", total)
				}
				return
			}
		}
	}

	t.Error("dropped counter not found")
}

// TestOtelMetrics_WorkerPoolQueueWait tests queue wait histogram
func TestOtelMetrics_WorkerPoolQueueWait(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	recorder := newMetricsRecorder(provider, slog.Default())

	// Record some queue wait times
	recorder.recordWorkerPoolQueueWait(1 * time.Millisecond)
	recorder.recordWorkerPoolQueueWait(5 * time.Millisecond)
	recorder.recordWorkerPoolQueueWait(10 * time.Millisecond)
	recorder.recordWorkerPoolQueueWait(100 * time.Microsecond) // Sub-millisecond

	// Collect metrics
	rm := &metricdata.ResourceMetrics{}
	err := reader.Collect(context.Background(), rm)
	if err != nil {
		t.Fatalf("failed to collect metrics: %v", err)
	}

	// Find the queue wait histogram
	for _, m := range rm.ScopeMetrics[0].Metrics {
		if m.Name == "submux.workerpool.queue_wait" {
			if hist, ok := m.Data.(metricdata.Histogram[float64]); ok {
				if len(hist.DataPoints) > 0 && hist.DataPoints[0].Count != 4 {
					t.Errorf("queue_wait count = %d, want 4", hist.DataPoints[0].Count)
				}
				return
			}
		}
	}

	t.Error("queue_wait histogram not found")
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
