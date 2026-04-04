package submux

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// invokeCallbackOrdered enqueues a message for ordered delivery to the subscription's
// callback. Messages for the same subscription are guaranteed to execute sequentially
// in the order they were enqueued, while different subscriptions execute concurrently.
func invokeCallbackOrdered(ctx context.Context, logger *slog.Logger, recorder metricsRecorder, pool *WorkerPool, callbackWg *sync.WaitGroup, sub *subscription, msg *Message) {
	sub.sequencer.enqueue(ctx, logger, recorder, pool, callbackWg, sub.callback, msg)
}

// executeCallback executes a callback with panic recovery and metrics.
// This is the core callback execution logic extracted for reuse.
func executeCallback(ctx context.Context, logger *slog.Logger, recorder metricsRecorder, callback MessageCallback, msg *Message) {
	// Get subscription type (handle nil message)
	var subType string
	if msg != nil {
		subType = msg.SubscriptionType.String()
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
			if logger != nil {
				attrs := []any{
					"error", r,
					"subscription_type", subType,
				}
				if msg != nil {
					attrs = append(attrs, "channel", msg.Channel)
					if msg.Pattern != "" {
						attrs = append(attrs, "pattern", msg.Pattern)
					}
				}
				attrs = append(attrs, "stack", string(debug.Stack()))
				logger.Error("submux: panic in callback", attrs...)
			}
			recorder.recordCallbackPanic(subType)
		}
	}()

	// Calculate and record message latency (from receipt to callback invocation)
	if msg != nil && !msg.Timestamp.IsZero() {
		msgLatency := time.Since(msg.Timestamp)
		recorder.recordMessageLatency(subType, msgLatency)
	}

	callback(ctx, msg)
}

