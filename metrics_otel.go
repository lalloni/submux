//go:build !nometrics

package submux

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// otelMetrics is the OpenTelemetry implementation of metricsRecorder.
type otelMetrics struct {
	logger *slog.Logger

	// Counters
	messagesReceived     metric.Int64Counter
	callbacksInvoked     metric.Int64Counter
	callbacksPanics      metric.Int64Counter
	subscriptionAttempts metric.Int64Counter
	connectionsCreated   metric.Int64Counter
	connectionsFailed    metric.Int64Counter
	migrationsStarted    metric.Int64Counter
	migrationsCompleted  metric.Int64Counter
	migrationsStalled    metric.Int64Counter
	migrationsTimeout    metric.Int64Counter
	topologyRefreshes    metric.Int64Counter

	// Histograms
	callbackLatency        metric.Float64Histogram
	messageLatency         metric.Float64Histogram
	migrationDuration      metric.Float64Histogram
	topologyRefreshLatency metric.Float64Histogram
}

// newOtelMetrics creates a new OpenTelemetry metrics recorder.
func newOtelMetrics(provider metric.MeterProvider, logger *slog.Logger) *otelMetrics {
	meter := provider.Meter(
		"github.com/lalloni/submux",
		metric.WithInstrumentationVersion("1.0.0"),
	)

	m := &otelMetrics{logger: logger}

	var err error

	// Create counter instruments
	m.messagesReceived, err = meter.Int64Counter(
		"submux.messages.received",
		metric.WithDescription("Total messages received from Redis"),
		metric.WithUnit("{message}"),
	)
	if err != nil {
		logger.Warn("submux: failed to create messagesReceived counter", "error", err)
	}

	m.callbacksInvoked, err = meter.Int64Counter(
		"submux.callbacks.invoked",
		metric.WithDescription("Total callback invocations"),
		metric.WithUnit("{invocation}"),
	)
	if err != nil {
		logger.Warn("submux: failed to create callbacksInvoked counter", "error", err)
	}

	m.callbacksPanics, err = meter.Int64Counter(
		"submux.callbacks.panics",
		metric.WithDescription("Total panics recovered in callbacks"),
		metric.WithUnit("{panic}"),
	)
	if err != nil {
		logger.Warn("submux: failed to create callbacksPanics counter", "error", err)
	}

	m.subscriptionAttempts, err = meter.Int64Counter(
		"submux.subscriptions.attempts",
		metric.WithDescription("Total subscription attempts"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		logger.Warn("submux: failed to create subscriptionAttempts counter", "error", err)
	}

	m.connectionsCreated, err = meter.Int64Counter(
		"submux.connections.created",
		metric.WithDescription("Total connections created"),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		logger.Warn("submux: failed to create connectionsCreated counter", "error", err)
	}

	m.connectionsFailed, err = meter.Int64Counter(
		"submux.connections.failed",
		metric.WithDescription("Total connection failures"),
		metric.WithUnit("{failure}"),
	)
	if err != nil {
		logger.Warn("submux: failed to create connectionsFailed counter", "error", err)
	}

	m.migrationsStarted, err = meter.Int64Counter(
		"submux.migrations.started",
		metric.WithDescription("Total migrations detected"),
		metric.WithUnit("{migration}"),
	)
	if err != nil {
		logger.Warn("submux: failed to create migrationsStarted counter", "error", err)
	}

	m.migrationsCompleted, err = meter.Int64Counter(
		"submux.migrations.completed",
		metric.WithDescription("Total migrations successfully completed"),
		metric.WithUnit("{migration}"),
	)
	if err != nil {
		logger.Warn("submux: failed to create migrationsCompleted counter", "error", err)
	}

	m.migrationsStalled, err = meter.Int64Counter(
		"submux.migrations.stalled",
		metric.WithDescription("Total migrations that stalled"),
		metric.WithUnit("{migration}"),
	)
	if err != nil {
		logger.Warn("submux: failed to create migrationsStalled counter", "error", err)
	}

	m.migrationsTimeout, err = meter.Int64Counter(
		"submux.migrations.timeout",
		metric.WithDescription("Total migrations that timed out"),
		metric.WithUnit("{migration}"),
	)
	if err != nil {
		logger.Warn("submux: failed to create migrationsTimeout counter", "error", err)
	}

	m.topologyRefreshes, err = meter.Int64Counter(
		"submux.topology.refreshes",
		metric.WithDescription("Total topology refresh attempts"),
		metric.WithUnit("{refresh}"),
	)
	if err != nil {
		logger.Warn("submux: failed to create topologyRefreshes counter", "error", err)
	}

	// Create histogram instruments
	m.callbackLatency, err = meter.Float64Histogram(
		"submux.callbacks.latency",
		metric.WithDescription("Callback execution time"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(1, 5, 10, 25, 50, 100, 250, 500, 1000, 5000, 10000),
	)
	if err != nil {
		logger.Warn("submux: failed to create callbackLatency histogram", "error", err)
	}

	m.messageLatency, err = meter.Float64Histogram(
		"submux.messages.latency",
		metric.WithDescription("Message receive to callback invocation latency"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(1, 5, 10, 25, 50, 100, 250, 500, 1000, 5000),
	)
	if err != nil {
		logger.Warn("submux: failed to create messageLatency histogram", "error", err)
	}

	m.migrationDuration, err = meter.Float64Histogram(
		"submux.migrations.duration",
		metric.WithDescription("Time to complete migration resubscription"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(100, 500, 1000, 2000, 5000, 10000, 30000),
	)
	if err != nil {
		logger.Warn("submux: failed to create migrationDuration histogram", "error", err)
	}

	m.topologyRefreshLatency, err = meter.Float64Histogram(
		"submux.topology.refresh_latency",
		metric.WithDescription("Time to refresh cluster topology"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(10, 50, 100, 250, 500, 1000, 2000),
	)
	if err != nil {
		logger.Warn("submux: failed to create topologyRefreshLatency histogram", "error", err)
	}

	return m
}

// Counter recording methods

func (m *otelMetrics) recordMessageReceived(subType string, nodeAddr string) {
	if m.messagesReceived != nil {
		m.messagesReceived.Add(context.Background(), 1,
			metric.WithAttributes(
				attribute.String("subscription_type", subType),
				attribute.String("node_address", nodeAddr),
			))
	}
}

func (m *otelMetrics) recordCallbackInvocation(subType string) {
	if m.callbacksInvoked != nil {
		m.callbacksInvoked.Add(context.Background(), 1,
			metric.WithAttributes(
				attribute.String("subscription_type", subType),
			))
	}
}

func (m *otelMetrics) recordCallbackPanic(subType string) {
	if m.callbacksPanics != nil {
		m.callbacksPanics.Add(context.Background(), 1,
			metric.WithAttributes(
				attribute.String("subscription_type", subType),
			))
	}
}

func (m *otelMetrics) recordSubscriptionAttempt(subType string, success bool) {
	if m.subscriptionAttempts != nil {
		m.subscriptionAttempts.Add(context.Background(), 1,
			metric.WithAttributes(
				attribute.String("subscription_type", subType),
				attribute.Bool("success", success),
			))
	}
}

func (m *otelMetrics) recordConnectionCreated(nodeAddr string) {
	if m.connectionsCreated != nil {
		m.connectionsCreated.Add(context.Background(), 1,
			metric.WithAttributes(
				attribute.String("node_address", nodeAddr),
			))
	}
}

func (m *otelMetrics) recordConnectionFailed(nodeAddr string, errorType string) {
	if m.connectionsFailed != nil {
		m.connectionsFailed.Add(context.Background(), 1,
			metric.WithAttributes(
				attribute.String("node_address", nodeAddr),
				attribute.String("error_type", errorType),
			))
	}
}

func (m *otelMetrics) recordMigrationStarted() {
	if m.migrationsStarted != nil {
		m.migrationsStarted.Add(context.Background(), 1)
	}
}

func (m *otelMetrics) recordMigrationCompleted() {
	if m.migrationsCompleted != nil {
		m.migrationsCompleted.Add(context.Background(), 1)
	}
}

func (m *otelMetrics) recordMigrationStalled() {
	if m.migrationsStalled != nil {
		m.migrationsStalled.Add(context.Background(), 1)
	}
}

func (m *otelMetrics) recordMigrationTimeout() {
	if m.migrationsTimeout != nil {
		m.migrationsTimeout.Add(context.Background(), 1)
	}
}

func (m *otelMetrics) recordTopologyRefresh(success bool) {
	if m.topologyRefreshes != nil {
		m.topologyRefreshes.Add(context.Background(), 1,
			metric.WithAttributes(
				attribute.Bool("success", success),
			))
	}
}

// Histogram recording methods

func (m *otelMetrics) recordCallbackLatency(subType string, duration time.Duration) {
	if m.callbackLatency != nil {
		ms := float64(duration.Milliseconds())
		m.callbackLatency.Record(context.Background(), ms,
			metric.WithAttributes(
				attribute.String("subscription_type", subType),
			))
	}
}

func (m *otelMetrics) recordMessageLatency(subType string, duration time.Duration) {
	if m.messageLatency != nil {
		ms := float64(duration.Milliseconds())
		m.messageLatency.Record(context.Background(), ms,
			metric.WithAttributes(
				attribute.String("subscription_type", subType),
			))
	}
}

func (m *otelMetrics) recordMigrationDuration(duration time.Duration) {
	if m.migrationDuration != nil {
		ms := float64(duration.Milliseconds())
		m.migrationDuration.Record(context.Background(), ms)
	}
}

func (m *otelMetrics) recordTopologyRefreshLatency(duration time.Duration) {
	if m.topologyRefreshLatency != nil {
		ms := float64(duration.Milliseconds())
		m.topologyRefreshLatency.Record(context.Background(), ms)
	}
}
