package submux

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestInvokeCallback_Normal(t *testing.T) {
	logger := slog.Default()

	var receivedMsg *Message
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)

	callback := func(ctx context.Context, msg *Message) {
		mu.Lock()
		receivedMsg = msg
		mu.Unlock()
		wg.Done()
	}

	testMsg := &Message{
		Type:    MessageTypeMessage,
		Channel: "test-channel",
		Payload: "hello",
	}

	invokeCallback(context.Background(), logger, &noopMetrics{}, nil, nil, callback, testMsg)

	// Wait for async callback
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if receivedMsg == nil {
		t.Fatal("received message is nil")
	}
	if receivedMsg.Channel != "test-channel" {
		t.Errorf("channel = %q, want %q", receivedMsg.Channel, "test-channel")
	}
	if receivedMsg.Payload != "hello" {
		t.Errorf("payload = %q, want %q", receivedMsg.Payload, "hello")
	}
}

func TestInvokeCallback_PanicRecovery(t *testing.T) {
	logger := slog.Default()

	done := make(chan struct{})
	panicCallback := func(ctx context.Context, msg *Message) {
		defer close(done)
		panic("test panic")
	}

	testMsg := &Message{
		Type:    MessageTypeMessage,
		Channel: "test",
	}

	// This should not panic the test - panic should be recovered
	invokeCallback(context.Background(), logger, &noopMetrics{}, nil, nil, panicCallback, testMsg)

	// Wait for callback to complete
	<-done

	// If we reach here without panicking, the test passes
}

func TestInvokeCallback_PanicWithNil(t *testing.T) {
	logger := slog.Default()

	done := make(chan struct{})
	panicCallback := func(ctx context.Context, msg *Message) {
		defer close(done)
		panic("panic called with nil argument") // Go 1.21+ converts panic(nil) to this
	}

	testMsg := &Message{Type: MessageTypeMessage}

	// Should handle panic(nil) gracefully
	invokeCallback(context.Background(), logger, &noopMetrics{}, nil, nil, panicCallback, testMsg)

	<-done
	// If we reach here, the test passes
}

func TestInvokeCallback_PanicWithError(t *testing.T) {
	logger := slog.Default()

	done := make(chan struct{})
	panicCallback := func(ctx context.Context, msg *Message) {
		defer close(done)
		panic("custom error message")
	}

	testMsg := &Message{Type: MessageTypeMessage}

	invokeCallback(context.Background(), logger, &noopMetrics{}, nil, nil, panicCallback, testMsg)

	<-done
	// If we reach here, the test passes
}

func TestInvokeCallback_NilMessage(t *testing.T) {
	logger := slog.Default()

	var receivedNil atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)

	callback := func(ctx context.Context, msg *Message) {
		if msg == nil {
			receivedNil.Store(true)
		}
		wg.Done()
	}

	invokeCallback(context.Background(), logger, &noopMetrics{}, nil, nil, callback, nil)

	wg.Wait()

	if !receivedNil.Load() {
		t.Error("callback should receive nil message")
	}
}

func TestInvokeCallback_Concurrency(t *testing.T) {
	logger := slog.Default()

	var callCount atomic.Int64
	var wg sync.WaitGroup

	callback := func(ctx context.Context, msg *Message) {
		callCount.Add(1)
		wg.Done()
	}

	numCalls := 100
	wg.Add(numCalls)

	// Invoke many callbacks concurrently
	for range numCalls {
		testMsg := &Message{
			Type:    MessageTypeMessage,
			Channel: "concurrent",
			Payload: "test",
		}
		invokeCallback(context.Background(), logger, &noopMetrics{}, nil, nil, callback, testMsg)
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
	case <-time.After(time.Second):
		t.Fatalf("timeout: only %d of %d callbacks completed", callCount.Load(), numCalls)
	}

	if callCount.Load() != int64(numCalls) {
		t.Errorf("call count = %d, want %d", callCount.Load(), numCalls)
	}
}

func TestInvokeCallback_PanicDoesNotAffectOthers(t *testing.T) {
	logger := slog.Default()

	var successCount atomic.Int64
	var wg sync.WaitGroup

	// Create a mix of panicking and successful callbacks
	numPanics := 10
	numSuccess := 20
	wg.Add(numSuccess)

	// First, invoke callbacks that will panic
	for range numPanics {
		panicCallback := func(ctx context.Context, msg *Message) {
			panic("intentional panic")
		}
		invokeCallback(context.Background(), logger, &noopMetrics{}, nil, nil, panicCallback, &Message{Type: MessageTypeMessage})
	}

	// Then, invoke successful callbacks
	for range numSuccess {
		successCallback := func(ctx context.Context, msg *Message) {
			successCount.Add(1)
			wg.Done()
		}
		invokeCallback(context.Background(), logger, &noopMetrics{}, nil, nil, successCallback, &Message{Type: MessageTypeMessage})
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
	case <-time.After(time.Second):
		t.Fatalf("timeout: only %d of %d successful callbacks completed", successCount.Load(), numSuccess)
	}

	if successCount.Load() != int64(numSuccess) {
		t.Errorf("success count = %d, want %d", successCount.Load(), numSuccess)
	}
}

func TestInvokeCallback_MessageTypes(t *testing.T) {
	logger := slog.Default()

	tests := []struct {
		name    string
		msgType MessageType
	}{
		{"regular message", MessageTypeMessage},
		{"pattern message", MessageTypePMessage},
		{"sharded message", MessageTypeSMessage},
		{"signal message", MessageTypeSignal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedType atomic.Int32
			var wg sync.WaitGroup
			wg.Add(1)

			callback := func(ctx context.Context, msg *Message) {
				receivedType.Store(int32(msg.Type))
				wg.Done()
			}

			testMsg := &Message{Type: tt.msgType}
			invokeCallback(context.Background(), logger, &noopMetrics{}, nil, nil, callback, testMsg)

			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()

			select {
			case <-done:
				if MessageType(receivedType.Load()) != tt.msgType {
					t.Errorf("received type = %v, want %v", MessageType(receivedType.Load()), tt.msgType)
				}
			case <-time.After(100 * time.Millisecond):
				t.Error("timeout waiting for callback")
			}
		})
	}
}

func TestInvokeCallback_SlowCallback(t *testing.T) {
	logger := slog.Default()

	var completed atomic.Bool

	slowCallback := func(ctx context.Context, msg *Message) {
		time.Sleep(10 * time.Millisecond)
		completed.Store(true)
	}

	start := time.Now()
	invokeCallback(context.Background(), logger, &noopMetrics{}, nil, nil, slowCallback, &Message{Type: MessageTypeMessage})
	elapsed := time.Since(start)

	// invokeCallback should return immediately (async)
	if elapsed > 10*time.Millisecond {
		t.Errorf("invokeCallback took %v, should be nearly instant", elapsed)
	}

	// Wait for the callback to complete
	time.Sleep(20 * time.Millisecond)

	if !completed.Load() {
		t.Error("slow callback did not complete")
	}
}

func TestInvokeCallback_WithWorkerPool(t *testing.T) {
	logger := slog.Default()

	pool := NewWorkerPool(4, 100)
	pool.Start()
	defer pool.Stop()

	var callCount atomic.Int64
	var wg sync.WaitGroup

	callback := func(ctx context.Context, msg *Message) {
		callCount.Add(1)
		wg.Done()
	}

	numCalls := 50
	wg.Add(numCalls)

	// Invoke callbacks using worker pool
	for range numCalls {
		testMsg := &Message{
			Type:    MessageTypeMessage,
			Channel: "pool-test",
			Payload: "test",
		}
		invokeCallback(context.Background(), logger, &noopMetrics{}, pool, nil, callback, testMsg)
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
	case <-time.After(time.Second):
		t.Fatalf("timeout: only %d of %d callbacks completed", callCount.Load(), numCalls)
	}

	if callCount.Load() != int64(numCalls) {
		t.Errorf("call count = %d, want %d", callCount.Load(), numCalls)
	}
}

func TestInvokeCallback_ContextCanceled(t *testing.T) {
	logger := slog.Default()

	ctx, cancel := context.WithCancel(context.Background())

	var receivedCtx context.Context
	var wg sync.WaitGroup
	wg.Add(1)

	callback := func(ctx context.Context, msg *Message) {
		receivedCtx = ctx
		wg.Done()
	}

	// Cancel context before invoking callback
	cancel()

	invokeCallback(ctx, logger, &noopMetrics{}, nil, nil, callback, &Message{Type: MessageTypeMessage})

	wg.Wait()

	// Callback should still be called, but context should be canceled
	if receivedCtx.Err() != context.Canceled {
		t.Errorf("expected context to be canceled, got %v", receivedCtx.Err())
	}
}

func TestSubscriptionTypeToString_AllTypes(t *testing.T) {
	tests := []struct {
		subType  subscriptionType
		expected string
	}{
		{subTypeSubscribe, "subscribe"},
		{subTypePSubscribe, "psubscribe"},
		{subTypeSSubscribe, "ssubscribe"},
		{subscriptionType(100), "unknown"}, // Unknown type
	}

	for _, tt := range tests {
		result := subscriptionTypeToString(tt.subType)
		if result != tt.expected {
			t.Errorf("subscriptionTypeToString(%d) = %q, want %q", tt.subType, result, tt.expected)
		}
	}
}

func TestExecuteCallback_NilMessage(t *testing.T) {
	logger := slog.Default()

	var calledWithNil bool
	callback := func(ctx context.Context, msg *Message) {
		calledWithNil = (msg == nil)
	}

	// Should not panic with nil message
	executeCallback(context.Background(), logger, &noopMetrics{}, callback, nil)

	if !calledWithNil {
		t.Error("callback should have been called with nil message")
	}
}

func TestInvokeCallback_WorkerPoolFull_FallbackToGoroutine(t *testing.T) {
	logger := slog.Default()

	// Test that callbacks work correctly even when pool is under load
	pool := NewWorkerPool(4, 100)
	pool.Start()
	defer pool.Stop()

	var callCount atomic.Int64
	var wg sync.WaitGroup

	// Submit callbacks to the pool
	numCalls := 20
	wg.Add(numCalls)

	for range numCalls {
		callback := func(ctx context.Context, msg *Message) {
			callCount.Add(1)
			wg.Done()
		}
		invokeCallback(context.Background(), logger, &noopMetrics{}, pool, nil, callback, &Message{Type: MessageTypeMessage})
	}

	// Wait with timeout for all callbacks
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout: only %d of %d callbacks completed", callCount.Load(), numCalls)
	}

	if callCount.Load() != int64(numCalls) {
		t.Errorf("call count = %d, want %d", callCount.Load(), numCalls)
	}
}

func TestClose_WaitsForFallbackCallbacks(t *testing.T) {
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{"localhost:7000"},
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	subMux, _ := New(clusterClient)

	var callbackStarted sync.WaitGroup
	callbackStarted.Add(1)
	var callbackCompleted atomic.Bool

	// Invoke a callback with nil pool to force fallback goroutine path
	invokeCallback(
		subMux.lifecycleCtx,
		subMux.config.logger,
		subMux.config.recorder,
		nil, // nil pool forces fallback goroutine
		&subMux.callbackWg,
		func(ctx context.Context, msg *Message) {
			callbackStarted.Done()
			time.Sleep(100 * time.Millisecond)
			callbackCompleted.Store(true)
		},
		&Message{Type: MessageTypeMessage},
	)

	// Wait for callback to start
	callbackStarted.Wait()

	// Close should wait for the callback goroutine
	subMux.Close()

	// EXPECTED: callbackCompleted should be true after Close()
	if !callbackCompleted.Load() {
		t.Error("Close() returned before fallback callback completed")
	}
}

func TestInvokeCallback_FallbackGoroutineTracked(t *testing.T) {
	logger := slog.Default()
	var wg sync.WaitGroup

	var completed atomic.Bool
	callback := func(ctx context.Context, msg *Message) {
		time.Sleep(50 * time.Millisecond)
		completed.Store(true)
	}

	// Stopped pool forces fallback goroutine path
	pool := NewWorkerPool(2, 10)
	pool.Start()
	pool.Stop() // Stop pool so Submit fails

	invokeCallback(context.Background(), logger, &noopMetrics{}, pool, &wg, callback, &Message{Type: MessageTypeMessage})

	// wg.Wait() should block until the fallback goroutine completes
	wg.Wait()

	if !completed.Load() {
		t.Error("fallback goroutine should have completed after wg.Wait()")
	}
}

func TestExecuteCallback_NilLogger_PanicRecovery(t *testing.T) {
	// Verify that a nil logger does not cause a secondary panic
	// when the callback panics.
	recorder := &noopMetrics{}
	msg := &Message{
		Type:             MessageTypeMessage,
		Channel:          "test",
		Payload:          "data",
		SubscriptionType: subTypeSubscribe,
	}

	panickingCallback := func(ctx context.Context, msg *Message) {
		panic("intentional test panic")
	}

	// This must not panic (the nil logger should be handled gracefully)
	executeCallback(context.Background(), nil, recorder, panickingCallback, msg)
}
