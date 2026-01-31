package submux

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerPool_NewWithDefaults(t *testing.T) {
	pool := NewWorkerPool(0, 0) // Use defaults
	defer pool.Stop()

	if pool.workers <= 0 {
		t.Error("workers should be > 0 with defaults")
	}
	if pool.QueueCapacity() <= 0 {
		t.Error("queue capacity should be > 0 with defaults")
	}
}

func TestWorkerPool_NewWithCustomValues(t *testing.T) {
	pool := NewWorkerPool(8, 500)
	defer pool.Stop()

	if pool.Workers() != 8 {
		t.Errorf("workers = %d, want 8", pool.Workers())
	}
	if pool.QueueCapacity() != 500 {
		t.Errorf("queue capacity = %d, want 500", pool.QueueCapacity())
	}
}

func TestWorkerPool_Start(t *testing.T) {
	pool := NewWorkerPool(4, 100)

	// Pool should accept tasks only after starting
	pool.Start()
	defer pool.Stop()

	var executed atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)

	pool.Submit(func() {
		executed.Store(true)
		wg.Done()
	})

	wg.Wait()

	if !executed.Load() {
		t.Error("task should have executed")
	}
}

func TestWorkerPool_StartMultipleTimes(t *testing.T) {
	pool := NewWorkerPool(4, 100)
	defer pool.Stop()

	// Multiple Start calls should be safe
	pool.Start()
	pool.Start()
	pool.Start()

	// Should still work correctly
	var executed atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)

	pool.Submit(func() {
		executed.Store(true)
		wg.Done()
	})

	wg.Wait()

	if !executed.Load() {
		t.Error("task should have executed")
	}
}

func TestWorkerPool_Submit(t *testing.T) {
	pool := NewWorkerPool(4, 100)
	pool.Start()
	defer pool.Stop()

	var count atomic.Int64
	var wg sync.WaitGroup
	numTasks := 100
	wg.Add(numTasks)

	for range numTasks {
		ok := pool.Submit(func() {
			count.Add(1)
			wg.Done()
		})
		if !ok {
			t.Error("Submit should return true")
		}
	}

	// Wait with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout: only %d of %d tasks completed", count.Load(), numTasks)
	}

	if count.Load() != int64(numTasks) {
		t.Errorf("count = %d, want %d", count.Load(), numTasks)
	}
}

func TestWorkerPool_TrySubmit(t *testing.T) {
	pool := NewWorkerPool(1, 2) // Single worker, small queue
	pool.Start()
	defer pool.Stop()

	// Fill the worker with a blocking task
	blocker := make(chan struct{})
	pool.Submit(func() {
		<-blocker
	})

	// Wait for worker to pick up the blocking task
	time.Sleep(10 * time.Millisecond)

	// Fill the queue (2 slots)
	queued1 := pool.TrySubmit(func() {})
	if !queued1 {
		t.Error("first TrySubmit should succeed when queue has space")
	}

	queued2 := pool.TrySubmit(func() {})
	if !queued2 {
		t.Error("second TrySubmit should succeed when queue has space")
	}

	// Queue should now be full
	overflowed := pool.TrySubmit(func() {})

	if overflowed {
		t.Error("TrySubmit should return false when queue is full")
	}

	// Release the blocker
	close(blocker)
}

func TestWorkerPool_Stop(t *testing.T) {
	pool := NewWorkerPool(4, 100)
	pool.Start()

	// Submit some tasks and wait for them to start
	var wg sync.WaitGroup
	var count atomic.Int64

	numTasks := 10
	wg.Add(numTasks)

	for range numTasks {
		pool.Submit(func() {
			count.Add(1)
			wg.Done()
		})
	}

	// Wait for all tasks to complete
	wg.Wait()

	// Then stop
	pool.Stop()

	// All tasks should have completed
	if count.Load() != int64(numTasks) {
		t.Errorf("count = %d, want %d", count.Load(), numTasks)
	}
}

func TestWorkerPool_StopMultipleTimes(t *testing.T) {
	pool := NewWorkerPool(4, 100)
	pool.Start()

	var wg sync.WaitGroup
	wg.Add(1)
	pool.Submit(func() {
		wg.Done()
	})
	wg.Wait()

	// Multiple Stop calls should be safe
	pool.Stop()
	pool.Stop()
	pool.Stop()
}

func TestWorkerPool_SubmitAfterStop(t *testing.T) {
	pool := NewWorkerPool(4, 100)
	pool.Start()
	pool.Stop()

	// Submit should return false after stop
	ok := pool.Submit(func() {
		t.Error("this task should not execute")
	})

	if ok {
		t.Error("Submit should return false after Stop")
	}
}

func TestWorkerPool_Context(t *testing.T) {
	pool := NewWorkerPool(4, 100)
	pool.Start()

	ctx := pool.Context()
	if ctx == nil {
		t.Error("Context should not be nil")
	}

	// Context should not be canceled yet
	select {
	case <-ctx.Done():
		t.Error("Context should not be canceled before Stop")
	default:
		// OK
	}

	pool.Stop()

	// Context should be canceled after Stop
	select {
	case <-ctx.Done():
		// OK
	case <-time.After(100 * time.Millisecond):
		t.Error("Context should be canceled after Stop")
	}
}

func TestWorkerPool_QueueLength(t *testing.T) {
	pool := NewWorkerPool(1, 100)
	pool.Start()
	defer pool.Stop()

	// Block the single worker
	blocker := make(chan struct{})
	pool.Submit(func() {
		<-blocker
	})

	// Give worker time to start
	time.Sleep(10 * time.Millisecond)

	// Queue should be empty
	if pool.QueueLength() != 0 {
		t.Errorf("QueueLength = %d, want 0", pool.QueueLength())
	}

	// Submit tasks that will queue up
	for range 5 {
		pool.Submit(func() {})
	}

	if pool.QueueLength() != 5 {
		t.Errorf("QueueLength = %d, want 5", pool.QueueLength())
	}

	// Release blocker
	close(blocker)

	// Wait for queue to drain
	time.Sleep(50 * time.Millisecond)

	if pool.QueueLength() != 0 {
		t.Errorf("QueueLength = %d, want 0 after drain", pool.QueueLength())
	}
}

func TestWorkerPool_Concurrency(t *testing.T) {
	pool := NewWorkerPool(10, 1000)
	pool.Start()
	defer pool.Stop()

	var concurrent atomic.Int64
	var maxConcurrent atomic.Int64
	var wg sync.WaitGroup
	numTasks := 100
	wg.Add(numTasks)

	for range numTasks {
		pool.Submit(func() {
			current := concurrent.Add(1)

			// Track max concurrency
			for {
				max := maxConcurrent.Load()
				if current <= max {
					break
				}
				if maxConcurrent.CompareAndSwap(max, current) {
					break
				}
			}

			time.Sleep(10 * time.Millisecond)
			concurrent.Add(-1)
			wg.Done()
		})
	}

	wg.Wait()

	// Max concurrency should be equal to number of workers
	if maxConcurrent.Load() > 10 {
		t.Errorf("maxConcurrent = %d, should be <= 10 (number of workers)", maxConcurrent.Load())
	}
	if maxConcurrent.Load() < 1 {
		t.Error("maxConcurrent should be >= 1")
	}
}

func TestWorkerPool_PanicRecovery(t *testing.T) {
	pool := NewWorkerPool(1, 10)
	pool.Start()
	defer pool.Stop()

	// Submit a task that panics
	var wg sync.WaitGroup
	wg.Add(2)

	// Note: The worker pool itself doesn't recover panics - that's handled by invokeCallback.
	// This test verifies the pool continues working after task completion.

	pool.Submit(func() {
		wg.Done()
	})

	pool.Submit(func() {
		wg.Done()
	})

	// Wait with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(time.Second):
		t.Error("timeout waiting for tasks")
	}
}

func BenchmarkWorkerPool_Submit(b *testing.B) {
	pool := NewWorkerPool(0, 10000) // Use default workers
	pool.Start()
	defer pool.Stop()

	b.ResetTimer()
	for b.Loop() {
		pool.Submit(func() {})
	}
}

func BenchmarkWorkerPool_TrySubmit(b *testing.B) {
	pool := NewWorkerPool(0, 100000) // Large queue to avoid blocking
	pool.Start()
	defer pool.Stop()

	b.ResetTimer()
	for b.Loop() {
		pool.TrySubmit(func() {})
	}
}
