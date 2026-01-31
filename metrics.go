package submux

import (
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/metric"
)

// metricsRecorder is the internal interface for recording metrics.
// Implementations must be thread-safe as they will be called from multiple goroutines.
type metricsRecorder interface {
	// Counter metrics
	recordMessageReceived(subType string, nodeAddr string)
	recordCallbackInvocation(subType string)
	recordCallbackPanic(subType string)
	recordSubscriptionAttempt(subType string, success bool)
	recordConnectionCreated(nodeAddr string)
	recordConnectionFailed(nodeAddr string, errorType string)
	recordMigrationStarted()
	recordMigrationCompleted()
	recordMigrationStalled()
	recordMigrationTimeout()
	recordTopologyRefresh(success bool)
	recordWorkerPoolSubmission(blocked bool)
	recordWorkerPoolDropped()

	// Histogram metrics
	recordCallbackLatency(subType string, duration time.Duration)
	recordMessageLatency(subType string, duration time.Duration)
	recordMigrationDuration(duration time.Duration)
	recordTopologyRefreshLatency(duration time.Duration)
	recordWorkerPoolQueueWait(duration time.Duration)

	// Gauge registration (for observable metrics)
	registerWorkerPoolGauges(pool *WorkerPool)
}

// newMetricsRecorder creates a metrics recorder based on the provided MeterProvider.
// If provider is nil, returns a no-op recorder with zero overhead.
// If provider is non-nil, returns an OTEL-based recorder (when built without nometrics tag).
func newMetricsRecorder(provider metric.MeterProvider, logger *slog.Logger) metricsRecorder {
	if provider == nil {
		return &noopMetrics{}
	}
	// This will be implemented in metrics_otel.go
	return newOtelMetrics(provider, logger)
}

// noopMetrics is a zero-overhead no-op implementation of metricsRecorder.
// All methods are empty and should be inlined by the compiler.
type noopMetrics struct{}

func (n *noopMetrics) recordMessageReceived(string, string)        {}
func (n *noopMetrics) recordCallbackInvocation(string)             {}
func (n *noopMetrics) recordCallbackPanic(string)                  {}
func (n *noopMetrics) recordSubscriptionAttempt(string, bool)      {}
func (n *noopMetrics) recordConnectionCreated(string)              {}
func (n *noopMetrics) recordConnectionFailed(string, string)       {}
func (n *noopMetrics) recordMigrationStarted()                     {}
func (n *noopMetrics) recordMigrationCompleted()                   {}
func (n *noopMetrics) recordMigrationStalled()                     {}
func (n *noopMetrics) recordMigrationTimeout()                     {}
func (n *noopMetrics) recordTopologyRefresh(bool)                  {}
func (n *noopMetrics) recordWorkerPoolSubmission(bool)             {}
func (n *noopMetrics) recordWorkerPoolDropped()                    {}
func (n *noopMetrics) recordCallbackLatency(string, time.Duration) {}
func (n *noopMetrics) recordMessageLatency(string, time.Duration)  {}
func (n *noopMetrics) recordMigrationDuration(time.Duration)       {}
func (n *noopMetrics) recordTopologyRefreshLatency(time.Duration)  {}
func (n *noopMetrics) recordWorkerPoolQueueWait(time.Duration)     {}
func (n *noopMetrics) registerWorkerPoolGauges(*WorkerPool)        {}
