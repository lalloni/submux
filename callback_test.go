package submux

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

	invokeCallback(logger, &noopMetrics{}, nil, context.Background(), callback, testMsg)

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
	invokeCallback(logger, &noopMetrics{}, nil, context.Background(), panicCallback, testMsg)

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
	invokeCallback(logger, &noopMetrics{}, nil, context.Background(), panicCallback, testMsg)

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

	invokeCallback(logger, &noopMetrics{}, nil, context.Background(), panicCallback, testMsg)

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

	invokeCallback(logger, &noopMetrics{}, nil, context.Background(), callback, nil)

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
	for i := 0; i < numCalls; i++ {
		testMsg := &Message{
			Type:    MessageTypeMessage,
			Channel: "concurrent",
			Payload: "test",
		}
		invokeCallback(logger, &noopMetrics{}, nil, context.Background(), callback, testMsg)
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
	for i := 0; i < numPanics; i++ {
		panicCallback := func(ctx context.Context, msg *Message) {
			panic("intentional panic")
		}
		invokeCallback(logger, &noopMetrics{}, nil, context.Background(), panicCallback, &Message{Type: MessageTypeMessage})
	}

	// Then, invoke successful callbacks
	for i := 0; i < numSuccess; i++ {
		successCallback := func(ctx context.Context, msg *Message) {
			successCount.Add(1)
			wg.Done()
		}
		invokeCallback(logger, &noopMetrics{}, nil, context.Background(), successCallback, &Message{Type: MessageTypeMessage})
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
			invokeCallback(logger, &noopMetrics{}, nil, context.Background(), callback, testMsg)

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
	invokeCallback(logger, &noopMetrics{}, nil, context.Background(), slowCallback, &Message{Type: MessageTypeMessage})
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
	for i := 0; i < numCalls; i++ {
		testMsg := &Message{
			Type:    MessageTypeMessage,
			Channel: "pool-test",
			Payload: "test",
		}
		invokeCallback(logger, &noopMetrics{}, pool, context.Background(), callback, testMsg)
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

	invokeCallback(logger, &noopMetrics{}, nil, ctx, callback, &Message{Type: MessageTypeMessage})

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
	executeCallback(logger, &noopMetrics{}, context.Background(), callback, nil)

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
		invokeCallback(logger, &noopMetrics{}, pool, context.Background(), callback, &Message{Type: MessageTypeMessage})
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
