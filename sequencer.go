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
	mu     sync.Mutex
	queue  []pendingCallback
	active bool // true while a drain task is submitted/running
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
	s.queue = append(s.queue, pendingCallback{msg: msg, enqueuedAt: time.Now()})
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
			if len(s.queue) == 0 {
				s.active = false
				s.mu.Unlock()
				return
			}
			item := s.queue[0]
			s.queue[0] = pendingCallback{} // zero for GC
			s.queue = s.queue[1:]
			s.mu.Unlock()

			// Record queue wait time
			waitDuration := time.Since(item.enqueuedAt)
			recorder.recordWorkerPoolQueueWait(waitDuration)

			executeCallback(ctx, logger, recorder, callback, item.msg)
		}
	}
}
