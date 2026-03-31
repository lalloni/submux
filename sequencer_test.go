package submux

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestCallbackSequencer_Ordering verifies that messages enqueued rapidly
// are delivered to the callback in FIFO order.
func TestCallbackSequencer_Ordering(t *testing.T) {
	pool := NewWorkerPool(4, 100)
	pool.Start()
	defer pool.Stop()

	const numMessages = 100
	var mu sync.Mutex
	var received []int

	callback := func(_ context.Context, msg *Message) {
		mu.Lock()
		defer mu.Unlock()
		// Use the payload to identify message order
		received = append(received, int(msg.SubscriptionType))
	}

	sub := &subscription{
		channel:  "test-channel",
		callback: callback,
	}

	// Enqueue messages rapidly
	for i := range numMessages {
		msg := &Message{
			Type:             MessageTypeMessage,
			Channel:          "test-channel",
			Payload:          "payload",
			Timestamp:        time.Now(),
			SubscriptionType: subscriptionType(i),
		}
		invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, nil, sub, msg)
	}

	// Wait for all messages to be processed
	// The drain loop processes all queued messages, so we poll until done
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n == numMessages {
			break
		}
		select {
		case <-deadline:
			mu.Lock()
			t.Fatalf("timeout: received %d/%d messages", len(received), numMessages)
			mu.Unlock()
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// Verify ordering
	mu.Lock()
	defer mu.Unlock()
	for i, v := range received {
		if v != i {
			t.Fatalf("message %d: got order %d, want %d", i, v, i)
		}
	}
}

// TestCallbackSequencer_SequentialExecution verifies that callbacks for the
// same subscription never overlap (no concurrent execution).
func TestCallbackSequencer_SequentialExecution(t *testing.T) {
	pool := NewWorkerPool(4, 100)
	pool.Start()
	defer pool.Stop()

	const numMessages = 20
	var overlapped atomic.Bool
	var running atomic.Int32
	var wg sync.WaitGroup
	wg.Add(numMessages)

	callback := func(_ context.Context, _ *Message) {
		defer wg.Done()
		if running.Add(1) > 1 {
			overlapped.Store(true)
		}
		// Simulate some work to increase overlap chance
		time.Sleep(100 * time.Microsecond)
		running.Add(-1)
	}

	sub := &subscription{
		channel:  "test-channel",
		callback: callback,
	}

	for range numMessages {
		msg := &Message{Type: MessageTypeMessage, Timestamp: time.Now()}
		invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, nil, sub, msg)
	}

	wg.Wait()
	if overlapped.Load() {
		t.Fatal("callbacks for the same subscription executed concurrently")
	}
}

// TestCallbackSequencer_ConcurrentSubscriptions verifies that different
// subscriptions execute concurrently while each preserves its own ordering.
func TestCallbackSequencer_ConcurrentSubscriptions(t *testing.T) {
	pool := NewWorkerPool(4, 100)
	pool.Start()
	defer pool.Stop()

	const numMessages = 50

	type result struct {
		mu       sync.Mutex
		received []int
	}
	results := [2]*result{{}, {}}

	subs := make([]*subscription, 2)
	for i := range subs {
		idx := i
		r := results[idx]
		subs[i] = &subscription{
			channel: "channel",
			callback: func(_ context.Context, msg *Message) {
				r.mu.Lock()
				r.received = append(r.received, int(msg.SubscriptionType))
				r.mu.Unlock()
				// Small delay to allow interleaving
				time.Sleep(10 * time.Microsecond)
			},
		}
	}

	// Send messages to both subscriptions
	for i := range numMessages {
		for _, sub := range subs {
			msg := &Message{
				Type:             MessageTypeMessage,
				Timestamp:        time.Now(),
				SubscriptionType: subscriptionType(i),
			}
			invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, nil, sub, msg)
		}
	}

	// Wait for completion
	deadline := time.After(5 * time.Second)
	for {
		allDone := true
		for _, r := range results {
			r.mu.Lock()
			if len(r.received) < numMessages {
				allDone = false
			}
			r.mu.Unlock()
		}
		if allDone {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for all messages")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// Verify per-subscription ordering
	for idx, r := range results {
		r.mu.Lock()
		for i, v := range r.received {
			if v != i {
				t.Fatalf("subscription %d: message %d got order %d, want %d", idx, i, v, i)
			}
		}
		r.mu.Unlock()
	}
}

// TestCallbackSequencer_NilPoolFallback verifies that ordering is preserved
// when the worker pool is nil (fallback to goroutine).
func TestCallbackSequencer_NilPoolFallback(t *testing.T) {
	const numMessages = 50
	var mu sync.Mutex
	var received []int

	callback := func(_ context.Context, msg *Message) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, int(msg.SubscriptionType))
	}

	sub := &subscription{
		channel:  "test-channel",
		callback: callback,
	}

	var wg sync.WaitGroup
	for i := range numMessages {
		msg := &Message{
			Type:             MessageTypeMessage,
			Timestamp:        time.Now(),
			SubscriptionType: subscriptionType(i),
		}
		invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, nil, &wg, sub, msg)
	}

	// Wait for fallback goroutine(s) to complete
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(received) != numMessages {
		t.Fatalf("received %d messages, want %d", len(received), numMessages)
	}
	for i, v := range received {
		if v != i {
			t.Fatalf("message %d: got order %d, want %d", i, v, i)
		}
	}
}

// TestCallbackSequencer_StoppedPoolFallback verifies ordering when the pool
// is stopped and we fall back to goroutines.
func TestCallbackSequencer_StoppedPoolFallback(t *testing.T) {
	pool := NewWorkerPool(2, 10)
	pool.Start()
	pool.Stop() // Stop immediately

	const numMessages = 20
	var mu sync.Mutex
	var received []int
	var wg sync.WaitGroup

	callback := func(_ context.Context, msg *Message) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, int(msg.SubscriptionType))
	}

	sub := &subscription{
		channel:  "test-channel",
		callback: callback,
	}

	for i := range numMessages {
		msg := &Message{
			Type:             MessageTypeMessage,
			Timestamp:        time.Now(),
			SubscriptionType: subscriptionType(i),
		}
		invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, &wg, sub, msg)
	}

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(received) != numMessages {
		t.Fatalf("received %d messages, want %d", len(received), numMessages)
	}
	for i, v := range received {
		if v != i {
			t.Fatalf("message %d: got order %d, want %d", i, v, i)
		}
	}
}

// TestCallbackSequencer_PanicRecovery verifies that a panic in one callback
// does not prevent subsequent messages from being delivered in order.
func TestCallbackSequencer_PanicRecovery(t *testing.T) {
	pool := NewWorkerPool(2, 100)
	pool.Start()
	defer pool.Stop()

	const numMessages = 10
	var mu sync.Mutex
	var received []int

	callback := func(_ context.Context, msg *Message) {
		idx := int(msg.SubscriptionType)
		if idx == 3 {
			panic("intentional test panic")
		}
		mu.Lock()
		received = append(received, idx)
		mu.Unlock()
	}

	sub := &subscription{
		channel:  "test-channel",
		callback: callback,
	}

	for i := range numMessages {
		msg := &Message{
			Type:             MessageTypeMessage,
			Timestamp:        time.Now(),
			SubscriptionType: subscriptionType(i),
		}
		invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, nil, sub, msg)
	}

	// Wait for all messages (minus the panic one) to be processed
	deadline := time.After(5 * time.Second)
	expected := numMessages - 1 // one panics
	for {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n == expected {
			break
		}
		select {
		case <-deadline:
			mu.Lock()
			t.Fatalf("timeout: received %d/%d messages", len(received), expected)
			mu.Unlock()
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// Verify ordering (should have 0,1,2 then 4,5,6,7,8,9 — skipping 3)
	mu.Lock()
	defer mu.Unlock()
	expectedOrder := []int{0, 1, 2, 4, 5, 6, 7, 8, 9}
	if len(received) != len(expectedOrder) {
		t.Fatalf("got %d messages, want %d", len(received), len(expectedOrder))
	}
	for i, v := range received {
		if v != expectedOrder[i] {
			t.Fatalf("message %d: got %d, want %d", i, v, expectedOrder[i])
		}
	}
}

// TestCallbackSequencer_ActiveFlagReset verifies that after draining, the
// sequencer can accept and process new messages (active flag resets properly).
func TestCallbackSequencer_ActiveFlagReset(t *testing.T) {
	pool := NewWorkerPool(2, 100)
	pool.Start()
	defer pool.Stop()

	var mu sync.Mutex
	var received []string

	callback := func(_ context.Context, msg *Message) {
		mu.Lock()
		received = append(received, msg.Payload)
		mu.Unlock()
	}

	sub := &subscription{
		channel:  "test-channel",
		callback: callback,
	}

	// First batch
	msg1 := &Message{Type: MessageTypeMessage, Payload: "first", Timestamp: time.Now()}
	invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, nil, sub, msg1)

	// Wait for first batch to drain
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for first message")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// Small delay to ensure drain loop exits and sets active=false
	time.Sleep(10 * time.Millisecond)

	// Second batch
	msg2 := &Message{Type: MessageTypeMessage, Payload: "second", Timestamp: time.Now()}
	invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, nil, sub, msg2)

	// Wait for second message
	for {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for second message")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if received[0] != "first" || received[1] != "second" {
		t.Fatalf("got %v, want [first, second]", received)
	}
}
