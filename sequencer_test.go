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

// TestCallbackSequencer_BoundedQueue_DropNewest verifies that when maxQueueSize
// is set, messages are dropped (tail-drop) once the queue is full.
func TestCallbackSequencer_BoundedQueue_DropNewest(t *testing.T) {
	pool := NewWorkerPool(4, 100)
	pool.Start()
	defer pool.Stop()

	const maxQueue = 5
	const totalMessages = 10

	// Block the callback so messages accumulate in the queue
	blockCh := make(chan struct{})
	var mu sync.Mutex
	var received []int

	callback := func(_ context.Context, msg *Message) {
		if msg.Type == MessageTypeSignal {
			return // ignore signals in this test
		}
		<-blockCh // block until released
		mu.Lock()
		received = append(received, int(msg.SubscriptionType))
		mu.Unlock()
	}

	sub := &subscription{
		channel:  "test-channel",
		callback: callback,
	}
	sub.sequencer.maxQueueSize = maxQueue

	// Enqueue first message — it will be picked up by the drain task and block
	msg0 := &Message{
		Type:             MessageTypeMessage,
		Channel:          "test-channel",
		Timestamp:        time.Now(),
		SubscriptionType: subscriptionType(0),
	}
	invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, nil, sub, msg0)

	// Give drain task time to pick up msg0 and block on blockCh
	time.Sleep(20 * time.Millisecond)

	// Now enqueue remaining messages — first maxQueue-1 should queue, rest should drop
	// (msg0 is being processed, so the queue was empty before these)
	for i := 1; i < totalMessages; i++ {
		msg := &Message{
			Type:             MessageTypeMessage,
			Channel:          "test-channel",
			Timestamp:        time.Now(),
			SubscriptionType: subscriptionType(i),
		}
		invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, nil, sub, msg)
	}

	// Release all blocked callbacks
	close(blockCh)

	// Wait for processing
	deadline := time.After(5 * time.Second)
	expectedCount := 1 + maxQueue // msg0 (processing) + maxQueue queued
	for {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= expectedCount {
			break
		}
		select {
		case <-deadline:
			mu.Lock()
			t.Fatalf("timeout: received %d/%d messages, got %v", len(received), expectedCount, received)
			mu.Unlock()
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// Allow time for any extra (wrongly delivered) messages
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != expectedCount {
		t.Fatalf("expected exactly %d messages, got %d: %v", expectedCount, len(received), received)
	}
	// Verify ordering: should be 0..maxQueue (the first maxQueue+1 messages)
	for i, v := range received {
		if v != i {
			t.Fatalf("message %d: got %d, want %d (received: %v)", i, v, i, received)
		}
	}
}

// TestCallbackSequencer_BoundedQueue_SignalOnOverflow verifies that an
// EventQueueOverflow signal is delivered when the queue starts dropping.
func TestCallbackSequencer_BoundedQueue_SignalOnOverflow(t *testing.T) {
	pool := NewWorkerPool(4, 100)
	pool.Start()
	defer pool.Stop()

	const maxQueue = 3

	blockCh := make(chan struct{})
	var signalReceived atomic.Bool
	var signalDropCount atomic.Int64
	var wg sync.WaitGroup

	callback := func(_ context.Context, msg *Message) {
		if msg.Type == MessageTypeSignal && msg.Signal.EventType == EventQueueOverflow {
			signalReceived.Store(true)
			signalDropCount.Store(int64(msg.Signal.DroppedCount))
			return
		}
		<-blockCh
	}

	sub := &subscription{
		channel:  "test-channel",
		callback: callback,
	}
	sub.sequencer.maxQueueSize = maxQueue

	// First message: starts drain, blocks
	msg0 := &Message{Type: MessageTypeMessage, Channel: "test-channel", Timestamp: time.Now()}
	invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, &wg, sub, msg0)
	time.Sleep(20 * time.Millisecond)

	// Fill the queue
	for i := 1; i <= maxQueue; i++ {
		msg := &Message{Type: MessageTypeMessage, Channel: "test-channel", Timestamp: time.Now()}
		invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, &wg, sub, msg)
	}

	// This one should be dropped and trigger the signal
	overflowMsg := &Message{Type: MessageTypeMessage, Channel: "test-channel", Timestamp: time.Now()}
	invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, &wg, sub, overflowMsg)

	// Wait for signal (delivered via bypass goroutine)
	deadline := time.After(2 * time.Second)
	for !signalReceived.Load() {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for overflow signal")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	if signalDropCount.Load() != 1 {
		t.Fatalf("signal DroppedCount: got %d, want 1", signalDropCount.Load())
	}

	close(blockCh)
	wg.Wait()
}

// TestCallbackSequencer_BoundedQueue_SignalCoalescing verifies that multiple
// consecutive drops produce only one signal per overflow episode.
func TestCallbackSequencer_BoundedQueue_SignalCoalescing(t *testing.T) {
	pool := NewWorkerPool(4, 100)
	pool.Start()
	defer pool.Stop()

	const maxQueue = 2

	blockCh := make(chan struct{})
	var signalCount atomic.Int32
	var wg sync.WaitGroup

	callback := func(_ context.Context, msg *Message) {
		if msg.Type == MessageTypeSignal {
			signalCount.Add(1)
			return
		}
		<-blockCh
	}

	sub := &subscription{
		channel:  "test-channel",
		callback: callback,
	}
	sub.sequencer.maxQueueSize = maxQueue

	// First message: blocks in callback
	msg0 := &Message{Type: MessageTypeMessage, Channel: "test-channel", Timestamp: time.Now()}
	invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, &wg, sub, msg0)
	time.Sleep(20 * time.Millisecond)

	// Fill queue
	for range maxQueue {
		msg := &Message{Type: MessageTypeMessage, Channel: "test-channel", Timestamp: time.Now()}
		invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, &wg, sub, msg)
	}

	// Drop 5 more messages — should only produce 1 signal total
	for range 5 {
		msg := &Message{Type: MessageTypeMessage, Channel: "test-channel", Timestamp: time.Now()}
		invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, &wg, sub, msg)
	}

	// Wait for signal goroutine to execute
	time.Sleep(50 * time.Millisecond)

	if count := signalCount.Load(); count != 1 {
		t.Fatalf("expected 1 coalesced signal, got %d", count)
	}

	close(blockCh)
	wg.Wait()
}

// TestCallbackSequencer_BoundedQueue_SignalResetAfterDrain verifies that after
// the queue drains and refills, a new overflow signal is sent.
func TestCallbackSequencer_BoundedQueue_SignalResetAfterDrain(t *testing.T) {
	pool := NewWorkerPool(4, 100)
	pool.Start()
	defer pool.Stop()

	const maxQueue = 2

	var signalCount atomic.Int32
	// gate controls whether callbacks block: send to gate to release one callback
	gate := make(chan struct{}, 100)
	var phase atomic.Int32 // 0 = first phase (block on gate), 1 = pass-through, 2 = block on gate
	var wg sync.WaitGroup

	callback := func(_ context.Context, msg *Message) {
		if msg.Type == MessageTypeSignal {
			signalCount.Add(1)
			return
		}
		p := phase.Load()
		if p == 0 || p == 2 {
			<-gate // block until a token is sent
		}
	}

	sub := &subscription{
		channel:  "test-channel",
		callback: callback,
	}
	sub.sequencer.maxQueueSize = maxQueue

	// Phase 1: fill and overflow
	msg := &Message{Type: MessageTypeMessage, Channel: "test-channel", Timestamp: time.Now()}
	invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, &wg, sub, msg)
	time.Sleep(20 * time.Millisecond)

	for range maxQueue {
		msg := &Message{Type: MessageTypeMessage, Channel: "test-channel", Timestamp: time.Now()}
		invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, &wg, sub, msg)
	}
	// Trigger overflow
	overflowMsg := &Message{Type: MessageTypeMessage, Channel: "test-channel", Timestamp: time.Now()}
	invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, &wg, sub, overflowMsg)
	time.Sleep(50 * time.Millisecond)

	if signalCount.Load() != 1 {
		t.Fatalf("phase 1: expected 1 signal, got %d", signalCount.Load())
	}

	// Drain the queue: switch to pass-through and release all blocked callbacks
	phase.Store(1)
	for range 1 + maxQueue { // 1 blocked in callback + maxQueue queued
		gate <- struct{}{}
	}
	time.Sleep(100 * time.Millisecond) // let drain complete

	// Phase 2: block on gate again, fill and overflow
	phase.Store(2)

	msg2 := &Message{Type: MessageTypeMessage, Channel: "test-channel", Timestamp: time.Now()}
	invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, &wg, sub, msg2)
	time.Sleep(20 * time.Millisecond)

	for range maxQueue {
		msg := &Message{Type: MessageTypeMessage, Channel: "test-channel", Timestamp: time.Now()}
		invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, &wg, sub, msg)
	}
	overflowMsg2 := &Message{Type: MessageTypeMessage, Channel: "test-channel", Timestamp: time.Now()}
	invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, &wg, sub, overflowMsg2)
	time.Sleep(50 * time.Millisecond)

	if signalCount.Load() != 2 {
		t.Fatalf("phase 2: expected 2 signals total, got %d", signalCount.Load())
	}

	// Release phase 2 callbacks
	for range 1 + maxQueue {
		gate <- struct{}{}
	}
	wg.Wait()
}

// TestCallbackSequencer_UnlimitedByDefault verifies that maxQueueSize=0
// means no messages are ever dropped (backward compatibility).
func TestCallbackSequencer_UnlimitedByDefault(t *testing.T) {
	pool := NewWorkerPool(4, 100)
	pool.Start()
	defer pool.Stop()

	const numMessages = 200
	var mu sync.Mutex
	var received []int

	callback := func(_ context.Context, msg *Message) {
		if msg.Type == MessageTypeSignal {
			t.Error("unexpected signal received")
			return
		}
		mu.Lock()
		received = append(received, int(msg.SubscriptionType))
		mu.Unlock()
	}

	sub := &subscription{
		channel:  "test-channel",
		callback: callback,
	}
	// maxQueueSize is zero (default) — no limit

	for i := range numMessages {
		msg := &Message{
			Type:             MessageTypeMessage,
			Channel:          "test-channel",
			Timestamp:        time.Now(),
			SubscriptionType: subscriptionType(i),
		}
		invokeCallbackOrdered(context.Background(), slog.Default(), &noopMetrics{}, pool, nil, sub, msg)
	}

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

	mu.Lock()
	defer mu.Unlock()
	for i, v := range received {
		if v != i {
			t.Fatalf("message %d: got %d, want %d", i, v, i)
		}
	}
}

// TestCallbackSequencer_BoundedQueue_Metrics verifies that queue depth and
// drop metrics are recorded correctly.
func TestCallbackSequencer_BoundedQueue_Metrics(t *testing.T) {
	pool := NewWorkerPool(4, 100)
	pool.Start()
	defer pool.Stop()

	const maxQueue = 3

	blockCh := make(chan struct{})
	var wg sync.WaitGroup

	recorder := &testMetricsRecorder{}

	callback := func(_ context.Context, msg *Message) {
		if msg.Type == MessageTypeSignal {
			return
		}
		<-blockCh
	}

	sub := &subscription{
		channel:  "test-channel",
		callback: callback,
	}
	sub.sequencer.maxQueueSize = maxQueue

	// First message: blocks
	msg0 := &Message{Type: MessageTypeMessage, Channel: "test-channel", Timestamp: time.Now()}
	invokeCallbackOrdered(context.Background(), slog.Default(), recorder, pool, &wg, sub, msg0)
	time.Sleep(20 * time.Millisecond)

	// Fill queue (3 more)
	for range maxQueue {
		msg := &Message{Type: MessageTypeMessage, Channel: "test-channel", Timestamp: time.Now()}
		invokeCallbackOrdered(context.Background(), slog.Default(), recorder, pool, &wg, sub, msg)
	}

	// Drop 2 more
	for range 2 {
		msg := &Message{Type: MessageTypeMessage, Channel: "test-channel", Timestamp: time.Now()}
		invokeCallbackOrdered(context.Background(), slog.Default(), recorder, pool, &wg, sub, msg)
	}

	// Verify metrics
	if recorder.queueDropped.Load() != 2 {
		t.Fatalf("queueDropped: got %d, want 2", recorder.queueDropped.Load())
	}
	// Queue depth was recorded for every enqueue call (1 + maxQueue + 2 = 6)
	if recorder.queueDepthCalls.Load() != 6 {
		t.Fatalf("queueDepthCalls: got %d, want 6", recorder.queueDepthCalls.Load())
	}

	close(blockCh)
	wg.Wait()
}

// testMetricsRecorder is a minimal metricsRecorder that tracks queue metrics for testing.
type testMetricsRecorder struct {
	noopMetrics
	queueDropped    atomic.Int64
	queueDepthCalls atomic.Int64
}

func (r *testMetricsRecorder) recordSubscriptionQueueDropped() {
	r.queueDropped.Add(1)
}

func (r *testMetricsRecorder) recordSubscriptionQueueDepth(_ int) {
	r.queueDepthCalls.Add(1)
}
