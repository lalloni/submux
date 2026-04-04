package submux

import (
	"log/slog"
	"testing"
	"time"
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

// BenchmarkNoopMetrics_Overhead benchmarks no-op recorder overhead
func BenchmarkNoopMetrics_Overhead(b *testing.B) {
	recorder := &noopMetrics{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recorder.recordMessageReceived("subscribe", "node1:6379")
	}
}
