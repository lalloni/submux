package submux

import (
	"log/slog"
	"time"
)

// invokeCallback safely invokes a callback function with panic recovery.
// The callback is invoked asynchronously in a separate goroutine.
func invokeCallback(logger *slog.Logger, recorder metricsRecorder, callback MessageCallback, msg *Message) {
	go func() {
		// Get subscription type (handle nil message)
		var subType string
		if msg != nil {
			subType = subscriptionTypeToString(msg.SubscriptionType)
		} else {
			subType = "unknown"
		}

		// Record callback invocation
		recorder.recordCallbackInvocation(subType)

		// Measure callback latency
		start := time.Now()

		defer func() {
			// Record latency
			duration := time.Since(start)
			recorder.recordCallbackLatency(subType, duration)

			// Recover from panics
			if r := recover(); r != nil {
				logger.Error("submux: panic in callback", "error", r)
				recorder.recordCallbackPanic(subType)
			}
		}()

		// Calculate and record message latency (from receipt to callback invocation)
		if msg != nil && !msg.Timestamp.IsZero() {
			msgLatency := time.Since(msg.Timestamp)
			recorder.recordMessageLatency(subType, msgLatency)
		}

		callback(msg)
	}()
}

// subscriptionTypeToString converts subscriptionType to string for metrics.
func subscriptionTypeToString(st subscriptionType) string {
	switch st {
	case subTypeSubscribe:
		return "subscribe"
	case subTypePSubscribe:
		return "psubscribe"
	case subTypeSSubscribe:
		return "ssubscribe"
	default:
		return "unknown"
	}
}
