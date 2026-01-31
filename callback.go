package submux

import (
	"context"
	"log/slog"
	"time"
)

// invokeCallback safely invokes a callback function with panic recovery.
// The callback is invoked asynchronously via the worker pool.
// If pool is nil, falls back to spawning a new goroutine (for backwards compatibility).
func invokeCallback(logger *slog.Logger, recorder metricsRecorder, pool *WorkerPool, ctx context.Context, callback MessageCallback, msg *Message) {
	// Use worker pool if available, otherwise fall back to goroutine
	if pool != nil {
		submitTime := time.Now()

		task := func() {
			// Record queue wait time (latency added by waiting in queue)
			waitDuration := time.Since(submitTime)
			recorder.recordWorkerPoolQueueWait(waitDuration)

			executeCallback(logger, recorder, ctx, callback, msg)
		}

		// Try non-blocking submit first to detect if we would block
		if pool.TrySubmit(task) {
			// Queue had space - submission did not block
			recorder.recordWorkerPoolSubmission(false)
		} else {
			// Queue was full - try blocking submit
			if pool.Submit(task) {
				// Blocked but eventually succeeded
				recorder.recordWorkerPoolSubmission(true)
			} else {
				// Pool is stopped or context canceled - record drop and run directly
				recorder.recordWorkerPoolDropped()
				go func() {
					executeCallback(logger, recorder, ctx, callback, msg)
				}()
			}
		}
	} else {
		go func() {
			executeCallback(logger, recorder, ctx, callback, msg)
		}()
	}
}

// executeCallback executes a callback with panic recovery and metrics.
// This is the core callback execution logic extracted for reuse.
func executeCallback(logger *slog.Logger, recorder metricsRecorder, ctx context.Context, callback MessageCallback, msg *Message) {
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

	callback(ctx, msg)
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
