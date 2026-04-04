//go:build nometrics

package submux

import (
	"log/slog"

	"go.opentelemetry.io/otel/metric"
)

// newOtelMetrics is a stub when building with the nometrics tag.
// It returns a no-op recorder regardless of the provider.
func newOtelMetrics(provider metric.MeterProvider, logger *slog.Logger) metricsRecorder {
	return &noopMetrics{}
}
