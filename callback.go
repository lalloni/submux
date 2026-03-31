package submux

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// invokeCallback safely invokes a callback function with panic recovery.
// The callback is invoked asynchronously via the worker pool.
// If pool is nil, falls back to spawning a new goroutine (for backwards compatibility).
// If callbackWg is non-nil, fallback goroutines are tracked so Close() can wait for them.
func invokeCallback(ctx context.Context, logger *slog.Logger, recorder metricsRecorder, pool *WorkerPool, callbackWg *sync.WaitGroup, callback MessageCallback, msg *Message) {
	// Use worker pool if available, otherwise fall back to goroutine
	if pool != nil {
		submitTime := time.Now()

		task := func() {
			// Record queue wait time (latency added by waiting in queue)
			waitDuration := time.Since(submitTime)
			recorder.recordWorkerPoolQueueWait(waitDuration)

			executeCallback(ctx, logger, recorder, callback, msg)
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
				// Pool is stopped or context canceled - record drop and run in fallback goroutine
				recorder.recordWorkerPoolDropped()
				runFallbackCallback(ctx, callbackWg, logger, recorder, callback, msg)
			}
		}
	} else {
		runFallbackCallback(ctx, callbackWg, logger, recorder, callback, msg)
	}
}

// runFallbackCallback spawns a goroutine to execute a callback outside the worker pool.
// If callbackWg is non-nil, the goroutine is tracked so Close() can wait for it.
func runFallbackCallback(ctx context.Context, callbackWg *sync.WaitGroup, logger *slog.Logger, recorder metricsRecorder, callback MessageCallback, msg *Message) {
	if callbackWg != nil {
		callbackWg.Add(1)
	}
	go func() {
		if callbackWg != nil {
			defer callbackWg.Done()
		}
		executeCallback(ctx, logger, recorder, callback, msg)
	}()
}

// executeCallback executes a callback with panic recovery and metrics.
// This is the core callback execution logic extracted for reuse.
func executeCallback(ctx context.Context, logger *slog.Logger, recorder metricsRecorder, callback MessageCallback, msg *Message) {
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
