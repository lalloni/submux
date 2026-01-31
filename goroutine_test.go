package submux

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWorkerPool_GoroutineLeaks verifies that the worker pool doesn't leak goroutines.
func TestWorkerPool_GoroutineLeaks(t *testing.T) {
	// Get baseline goroutine count
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	// Create and use several worker pools
	for range 5 {
		pool := NewWorkerPool(10, 100)
		pool.Start()

		// Submit tasks
		var wg sync.WaitGroup
		wg.Add(50)
		for range 50 {
			pool.Submit(func() {
				time.Sleep(time.Millisecond)
				wg.Done()
			})
		}
		wg.Wait()

		pool.Stop()
	}

	// Wait for goroutines to clean up
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	// Check goroutine count
	final := runtime.NumGoroutine()

	// Allow some margin for test framework goroutines
	margin := 5
	if final > baseline+margin {
		t.Errorf("goroutine leak detected: baseline=%d, final=%d (increase of %d)", baseline, final, final-baseline)
	}
}

// TestWorkerPool_BoundedGoroutines verifies that the worker pool limits goroutine count.
func TestWorkerPool_BoundedGoroutines(t *testing.T) {
	// Get baseline goroutine count
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	workers := 8
	pool := NewWorkerPool(workers, 1000)
	pool.Start()
	defer pool.Stop()

	// Submit many tasks that block
	var running atomic.Int64
	blocker := make(chan struct{})
	numTasks := 100

	var wg sync.WaitGroup
	wg.Add(numTasks)

	for range numTasks {
		pool.Submit(func() {
			running.Add(1)
			<-blocker
			wg.Done()
		})
	}

	// Wait for workers to pick up tasks
	time.Sleep(50 * time.Millisecond)

	// Check that only 'workers' goroutines are running tasks
	if running.Load() > int64(workers) {
		t.Errorf("running = %d, should be <= %d (bounded by worker count)", running.Load(), workers)
	}

	// Goroutine count should be bounded
	current := runtime.NumGoroutine()
	// Expected: baseline + workers (worker goroutines) + 1 (test goroutine overhead)
	maxExpected := baseline + workers + 10 // margin for other goroutines
	if current > maxExpected {
		t.Errorf("goroutine count = %d, expected <= %d", current, maxExpected)
	}

	// Release tasks
	close(blocker)
	wg.Wait()
}

// TestInvokeCallback_GoroutineLeaks verifies that invokeCallback doesn't leak goroutines
// when using the worker pool.
func TestInvokeCallback_GoroutineLeaks(t *testing.T) {
	// Get baseline goroutine count
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	pool := NewWorkerPool(4, 100)
	pool.Start()

	var wg sync.WaitGroup
	numCalls := 100
	wg.Add(numCalls)

	for range numCalls {
		invokeCallback(
			nil, // logger
			&noopMetrics{},
			pool,
			pool.Context(),
			func(ctx context.Context, msg *Message) {
				wg.Done()
			},
			&Message{Type: MessageTypeMessage},
		)
	}

	wg.Wait()
	pool.Stop()

	// Wait for cleanup
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	// Check goroutine count
	final := runtime.NumGoroutine()

	// Allow some margin
	margin := 5
	if final > baseline+margin {
		t.Errorf("goroutine leak detected: baseline=%d, final=%d", baseline, final)
	}
}

// TestInvokeCallback_UnboundedWithoutPool verifies fallback behavior when pool is nil.
func TestInvokeCallback_UnboundedWithoutPool(t *testing.T) {
	// When pool is nil, invokeCallback falls back to creating goroutines
	// This test verifies they still complete and don't leak

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	var wg sync.WaitGroup
	numCalls := 50
	wg.Add(numCalls)

	for range numCalls {
		invokeCallback(
			nil, // logger
			&noopMetrics{},
			nil, // no pool - falls back to goroutines
			nil, // no context
			func(ctx context.Context, msg *Message) {
				wg.Done()
			},
			&Message{Type: MessageTypeMessage},
		)
	}

	wg.Wait()

	// Wait for cleanup
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	final := runtime.NumGoroutine()

	margin := 5
	if final > baseline+margin {
		t.Errorf("goroutine leak detected: baseline=%d, final=%d", baseline, final)
	}
}

// TestHighThroughput_NoGoroutineExplosion verifies that high message throughput
// doesn't cause goroutine count to explode.
func TestHighThroughput_NoGoroutineExplosion(t *testing.T) {
	workers := 4
	pool := NewWorkerPool(workers, 1000)
	pool.Start()
	defer pool.Stop()

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	// Submit many fast tasks rapidly
	var count atomic.Int64
	numTasks := 10000

	var wg sync.WaitGroup
	wg.Add(numTasks)

	for range numTasks {
		pool.Submit(func() {
			count.Add(1)
			wg.Done()
		})
	}

	// Check goroutine count during execution
	midExec := runtime.NumGoroutine()

	// Should be bounded by workers, not by number of tasks
	maxExpected := baseline + workers + 10 // margin
	if midExec > maxExpected {
		t.Errorf("goroutine explosion during execution: count=%d, expected<=%d", midExec, maxExpected)
	}

	wg.Wait()

	if count.Load() != int64(numTasks) {
		t.Errorf("not all tasks completed: %d/%d", count.Load(), numTasks)
	}
}
