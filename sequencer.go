package submux

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// callbackSequencer ensures ordered, sequential callback execution for a
// single subscription while reusing the shared WorkerPool for bounded
// concurrency. At most one drain task runs per sequencer at any time.
//
// The zero value is ready to use.
type callbackSequencer struct {
	mu           sync.Mutex
	queue        pendingCallbackRing
	active       bool // true while a drain task is submitted/running
	maxQueueSize int  // 0 means unlimited
	overflowing  bool // true when queue is at capacity (for signal coalescing)
	dropCount    int  // cumulative drops since overflow started
}

// pendingCallback holds a message waiting to be delivered to the callback.
type pendingCallback struct {
	msg        *Message
	enqueuedAt time.Time
}

// enqueue appends a message to the sequencer's queue and, if no drain task
// is currently active, submits one to the worker pool. If a drain task is
// already running, it will pick up the newly enqueued message.
func (s *callbackSequencer) enqueue(ctx context.Context, logger *slog.Logger, recorder metricsRecorder, pool *WorkerPool, callbackWg *sync.WaitGroup, callback MessageCallback, msg *Message) {
	s.mu.Lock()

	// Lazy-initialize the ring buffer on first use
	if s.queue.buf == nil {
		s.queue.initRing(s.maxQueueSize)
	}

	// Record queue depth histogram at enqueue time
	recorder.recordSubscriptionQueueDepth(s.queue.len())

	// Check queue capacity (0 means unlimited)
	if s.maxQueueSize > 0 && s.queue.len() >= s.maxQueueSize {
		s.dropCount++
		recorder.recordSubscriptionQueueDropped()

		sendSignal := false
		if !s.overflowing {
			s.overflowing = true
			sendSignal = true
		}
		droppedSoFar := s.dropCount
		s.mu.Unlock()

		logger.Warn("submux: subscription queue full, dropping message",
			"channel", msg.Channel,
			"queue_limit", s.maxQueueSize,
			"dropped_count", droppedSoFar,
		)

		// Send overflow signal bypassing the queue (direct goroutine)
		if sendSignal {
			signalMsg := &Message{
				Type: MessageTypeSignal,
				Signal: &SignalInfo{
					EventType:    EventQueueOverflow,
					Details:      "subscription queue full, messages being dropped",
					DroppedCount: droppedSoFar,
				},
				Channel:          msg.Channel,
				Timestamp:        time.Now(),
				SubscriptionType: msg.SubscriptionType,
			}
			if callbackWg != nil {
				callbackWg.Add(1)
			}
			go func() {
				if callbackWg != nil {
					defer callbackWg.Done()
				}
				executeCallback(ctx, logger, recorder, callback, signalMsg)
			}()
		}
		return
	}

	// Reset overflow state when queue has space again
	if s.overflowing {
		s.overflowing = false
		s.dropCount = 0
	}

	s.queue.push(pendingCallback{msg: msg, enqueuedAt: time.Now()})
	if s.active {
		s.mu.Unlock()
		return
	}
	s.active = true
	s.mu.Unlock()

	drainFn := s.makeDrainFunc(ctx, logger, recorder, callback)

	if pool != nil {
		if pool.TrySubmit(drainFn) {
			recorder.recordWorkerPoolSubmission(false)
			return
		}
		if pool.Submit(drainFn) {
			recorder.recordWorkerPoolSubmission(true)
			return
		}
		recorder.recordWorkerPoolDropped()
	}

	// Fallback: run drain in a goroutine
	if callbackWg != nil {
		callbackWg.Add(1)
	}
	go func() {
		if callbackWg != nil {
			defer callbackWg.Done()
		}
		drainFn()
	}()
}

// makeDrainFunc returns a closure that processes all queued messages
// sequentially. It loops until the queue is empty, then resets the active
// flag so a future enqueue will start a new drain task.
func (s *callbackSequencer) makeDrainFunc(ctx context.Context, logger *slog.Logger, recorder metricsRecorder, callback MessageCallback) func() {
	return func() {
		for {
			s.mu.Lock()
			if s.queue.len() == 0 {
				s.active = false
				s.mu.Unlock()
				return
			}
			item, _ := s.queue.pop()
			s.mu.Unlock()

			// Record queue wait time
			waitDuration := time.Since(item.enqueuedAt)
			recorder.recordWorkerPoolQueueWait(waitDuration)

			executeCallback(ctx, logger, recorder, callback, item.msg)
		}
	}
}
