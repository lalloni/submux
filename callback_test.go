package submux

import (
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

	callback := func(msg *Message) {
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

	invokeCallback(logger, &noopMetrics{}, callback, testMsg)

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
	panicCallback := func(msg *Message) {
		defer close(done)
		panic("test panic")
	}

	testMsg := &Message{
		Type:    MessageTypeMessage,
		Channel: "test",
	}

	// This should not panic the test - panic should be recovered
	invokeCallback(logger, &noopMetrics{}, panicCallback, testMsg)

	// Wait for callback to complete
	<-done

	// If we reach here without panicking, the test passes
}

func TestInvokeCallback_PanicWithNil(t *testing.T) {
	logger := slog.Default()

	done := make(chan struct{})
	panicCallback := func(msg *Message) {
		defer close(done)
		panic(nil)
	}

	testMsg := &Message{Type: MessageTypeMessage}

	// Should handle panic(nil) gracefully
	invokeCallback(logger, &noopMetrics{}, panicCallback, testMsg)

	<-done
	// If we reach here, the test passes
}

func TestInvokeCallback_PanicWithError(t *testing.T) {
	logger := slog.Default()

	done := make(chan struct{})
	panicCallback := func(msg *Message) {
		defer close(done)
		panic("custom error message")
	}

	testMsg := &Message{Type: MessageTypeMessage}

	invokeCallback(logger, &noopMetrics{}, panicCallback, testMsg)

	<-done
	// If we reach here, the test passes
}

func TestInvokeCallback_NilMessage(t *testing.T) {
	logger := slog.Default()

	var receivedNil atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)

	callback := func(msg *Message) {
		if msg == nil {
			receivedNil.Store(true)
		}
		wg.Done()
	}

	invokeCallback(logger, &noopMetrics{}, callback, nil)

	wg.Wait()

	if !receivedNil.Load() {
		t.Error("callback should receive nil message")
	}
}

func TestInvokeCallback_Concurrency(t *testing.T) {
	logger := slog.Default()

	var callCount atomic.Int64
	var wg sync.WaitGroup

	callback := func(msg *Message) {
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
		invokeCallback(logger, &noopMetrics{}, callback, testMsg)
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
		panicCallback := func(msg *Message) {
			panic("intentional panic")
		}
		invokeCallback(logger, &noopMetrics{}, panicCallback, &Message{Type: MessageTypeMessage})
	}

	// Then, invoke successful callbacks
	for i := 0; i < numSuccess; i++ {
		successCallback := func(msg *Message) {
			successCount.Add(1)
			wg.Done()
		}
		invokeCallback(logger, &noopMetrics{}, successCallback, &Message{Type: MessageTypeMessage})
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

			callback := func(msg *Message) {
				receivedType.Store(int32(msg.Type))
				wg.Done()
			}

			testMsg := &Message{Type: tt.msgType}
			invokeCallback(logger, &noopMetrics{}, callback, testMsg)

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

	slowCallback := func(msg *Message) {
		time.Sleep(10 * time.Millisecond)
		completed.Store(true)
	}

	start := time.Now()
	invokeCallback(logger, &noopMetrics{}, slowCallback, &Message{Type: MessageTypeMessage})
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
