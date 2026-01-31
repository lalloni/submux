package submux

import (
	"context"
	"runtime"
	"sync"
)

// WorkerPool manages a bounded pool of worker goroutines for executing tasks.
// It provides backpressure when the queue is full, preventing goroutine explosion
// under high load.
type WorkerPool struct {
	// taskQueue is the channel used to submit tasks to workers.
	taskQueue chan func()

	// workers is the number of worker goroutines.
	workers int

	// ctx is the context for the worker pool lifecycle.
	ctx context.Context

	// cancel cancels the worker pool context.
	cancel context.CancelFunc

	// wg tracks running worker goroutines.
	wg sync.WaitGroup

	// started indicates whether the pool has been started.
	started bool

	// stopped indicates whether the pool has been stopped.
	stopped bool

	// mu protects started and stopped.
	mu sync.Mutex
}

// NewWorkerPool creates a new worker pool with the specified number of workers and queue size.
// If workers is 0, it defaults to runtime.NumCPU() * 2.
// If queueSize is 0, it defaults to 10000.
func NewWorkerPool(workers, queueSize int) *WorkerPool {
	if workers <= 0 {
		workers = runtime.NumCPU() * 2
	}
	if queueSize <= 0 {
		queueSize = 10000
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &WorkerPool{
		taskQueue: make(chan func(), queueSize),
		workers:   workers,
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Start starts the worker goroutines. It is safe to call multiple times.
func (wp *WorkerPool) Start() {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	if wp.started {
		return
	}
	wp.started = true

	for i := 0; i < wp.workers; i++ {
		wp.wg.Add(1)
		go wp.worker()
	}
}

// worker is the main loop for a worker goroutine.
func (wp *WorkerPool) worker() {
	defer wp.wg.Done()

	for {
		select {
		case task, ok := <-wp.taskQueue:
			if !ok {
				// Channel closed, exit
				return
			}
			// Execute the task
			task()
		case <-wp.ctx.Done():
			return
		}
	}
}

// Submit submits a task to the worker pool.
// It blocks if the queue is full (providing backpressure).
// Returns false if the pool is stopped or the context is canceled.
func (wp *WorkerPool) Submit(task func()) bool {
	// Check context first to avoid panic on closed channel
	select {
	case <-wp.ctx.Done():
		return false
	default:
	}

	select {
	case wp.taskQueue <- task:
		return true
	case <-wp.ctx.Done():
		return false
	}
}

// SubmitWithContext submits a task with a context for cancellation.
// Returns false if the pool is stopped, the context is canceled, or the provided context is done.
func (wp *WorkerPool) SubmitWithContext(ctx context.Context, task func()) bool {
	select {
	case wp.taskQueue <- task:
		return true
	case <-wp.ctx.Done():
		return false
	case <-ctx.Done():
		return false
	}
}

// TrySubmit attempts to submit a task without blocking.
// Returns true if the task was submitted, false if the queue is full or the pool is stopped.
func (wp *WorkerPool) TrySubmit(task func()) bool {
	select {
	case wp.taskQueue <- task:
		return true
	default:
		return false
	}
}

// Stop stops the worker pool gracefully, waiting for all workers to finish.
// Tasks still in the queue will be processed before workers exit.
// It is safe to call multiple times.
func (wp *WorkerPool) Stop() {
	wp.mu.Lock()
	if !wp.started || wp.stopped {
		wp.mu.Unlock()
		return
	}
	wp.stopped = true
	wp.mu.Unlock()

	// Cancel the context to signal workers to stop accepting new tasks
	wp.cancel()

	// Close the task queue to signal workers to drain and exit
	close(wp.taskQueue)

	// Wait for all workers to finish
	wp.wg.Wait()
}

// Context returns the worker pool's context.
// This context is canceled when Stop() is called.
func (wp *WorkerPool) Context() context.Context {
	return wp.ctx
}

// QueueLength returns the current number of tasks in the queue.
func (wp *WorkerPool) QueueLength() int {
	return len(wp.taskQueue)
}

// QueueCapacity returns the maximum queue capacity.
func (wp *WorkerPool) QueueCapacity() int {
	return cap(wp.taskQueue)
}

// Workers returns the number of worker goroutines.
func (wp *WorkerPool) Workers() int {
	return wp.workers
}
