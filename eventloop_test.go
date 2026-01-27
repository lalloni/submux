package submux

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// mockRedisError implements the proto.RedisError interface for testing.
// go-redis uses the RedisError() marker method to identify Redis protocol errors.
type mockRedisError string

func (e mockRedisError) Error() string { return string(e) }
func (e mockRedisError) RedisError()   {} // Marker method required by go-redis

// Helper to create a test pubSubMetadata
func newTestMetadata() *pubSubMetadata {
	return &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
		logger:               slog.Default(),
		recorder:             &noopMetrics{},
		nodeAddr:             "test:7000",
	}
}

func TestCheckAndHandleRedirect_MovedError(t *testing.T) {
	meta := newTestMetadata()

	var capturedAddr string
	var capturedIsMoved bool
	callCount := 0

	meta.onRedirectDetected = func(addr string, isMoved bool) {
		capturedAddr = addr
		capturedIsMoved = isMoved
		callCount++
	}

	// Create a MOVED error using the redis error format
	// redis.IsMovedError checks for "MOVED" prefix in error message
	movedErr := mockRedisError("MOVED 3999 127.0.0.1:7001")

	checkAndHandleRedirect(meta, movedErr)

	if callCount != 1 {
		t.Errorf("callback called %d times, expected 1", callCount)
	}
	if capturedAddr != "127.0.0.1:7001" {
		t.Errorf("captured addr = %q, expected %q", capturedAddr, "127.0.0.1:7001")
	}
	if !capturedIsMoved {
		t.Error("expected isMoved=true for MOVED error")
	}
}

func TestCheckAndHandleRedirect_AskError(t *testing.T) {
	meta := newTestMetadata()

	var capturedAddr string
	var capturedIsMoved bool
	callCount := 0

	meta.onRedirectDetected = func(addr string, isMoved bool) {
		capturedAddr = addr
		capturedIsMoved = isMoved
		callCount++
	}

	// Create an ASK error
	askErr := mockRedisError("ASK 3999 127.0.0.1:7002")

	checkAndHandleRedirect(meta, askErr)

	if callCount != 1 {
		t.Errorf("callback called %d times, expected 1", callCount)
	}
	if capturedAddr != "127.0.0.1:7002" {
		t.Errorf("captured addr = %q, expected %q", capturedAddr, "127.0.0.1:7002")
	}
	if capturedIsMoved {
		t.Error("expected isMoved=false for ASK error")
	}
}

func TestCheckAndHandleRedirect_RegularError(t *testing.T) {
	meta := newTestMetadata()

	callCount := 0
	meta.onRedirectDetected = func(addr string, isMoved bool) {
		callCount++
	}

	// Regular error, not MOVED or ASK
	regularErr := errors.New("connection refused")

	checkAndHandleRedirect(meta, regularErr)

	if callCount != 0 {
		t.Errorf("callback should not be called for regular error, but called %d times", callCount)
	}
}

func TestCheckAndHandleRedirect_NilError(t *testing.T) {
	meta := newTestMetadata()

	callCount := 0
	meta.onRedirectDetected = func(addr string, isMoved bool) {
		callCount++
	}

	checkAndHandleRedirect(meta, nil)

	if callCount != 0 {
		t.Errorf("callback should not be called for nil error, but called %d times", callCount)
	}
}

func TestCheckAndHandleRedirect_NilCallback(t *testing.T) {
	meta := newTestMetadata()
	meta.onRedirectDetected = nil

	// Should not panic with nil callback
	movedErr := mockRedisError("MOVED 3999 127.0.0.1:7001")
	checkAndHandleRedirect(meta, movedErr) // Should not panic
}

func TestProcessResponse_SubscriptionConfirmation(t *testing.T) {
	meta := newTestMetadata()

	// Add a pending subscription
	sub := &subscription{
		channel:   "test-channel",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}
	meta.pendingSubscriptions["test-channel"] = sub

	// Process subscription confirmation
	subMsg := &redis.Subscription{
		Kind:    "subscribe",
		Channel: "test-channel",
		Count:   1,
	}

	err := processResponse(meta, subMsg)

	if err != nil {
		t.Errorf("processResponse returned error: %v", err)
	}

	// Verify subscription was marked as active
	if sub.state != subStateActive {
		t.Errorf("subscription state = %v, expected %v", sub.state, subStateActive)
	}

	// Verify removed from pending
	if _, ok := meta.pendingSubscriptions["test-channel"]; ok {
		t.Error("subscription should be removed from pending")
	}
}

func TestProcessResponse_UnsubscribeConfirmation(t *testing.T) {
	meta := newTestMetadata()

	// Process unsubscribe confirmation
	subMsg := &redis.Subscription{
		Kind:    "unsubscribe",
		Channel: "test-channel",
		Count:   0,
	}

	err := processResponse(meta, subMsg)

	if err != nil {
		t.Errorf("processResponse returned error: %v", err)
	}
}

func TestProcessResponse_RegularMessage(t *testing.T) {
	meta := newTestMetadata()

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

	// Add a subscription for the channel
	sub := &subscription{
		channel:  "test-channel",
		callback: callback,
		subType:  subTypeSubscribe,
	}
	meta.subscriptions["test-channel"] = []*subscription{sub}

	// Process a regular message
	redisMsg := &redis.Message{
		Channel: "test-channel",
		Payload: "hello world",
		Pattern: "", // Empty for regular SUBSCRIBE
	}

	err := processResponse(meta, redisMsg)

	if err != nil {
		t.Errorf("processResponse returned error: %v", err)
	}

	// Wait for async callback
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if receivedMsg == nil {
		t.Fatal("callback not invoked")
	}
	if receivedMsg.Type != MessageTypeMessage {
		t.Errorf("message type = %v, expected %v", receivedMsg.Type, MessageTypeMessage)
	}
	if receivedMsg.Channel != "test-channel" {
		t.Errorf("message channel = %q, expected %q", receivedMsg.Channel, "test-channel")
	}
	if receivedMsg.Payload != "hello world" {
		t.Errorf("message payload = %q, expected %q", receivedMsg.Payload, "hello world")
	}
}

func TestProcessResponse_PatternMessage(t *testing.T) {
	meta := newTestMetadata()

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

	// Add a pattern subscription
	sub := &subscription{
		channel:  "test-*",
		callback: callback,
		subType:  subTypePSubscribe,
	}
	meta.subscriptions["test-*"] = []*subscription{sub}

	// Process a pattern message
	redisMsg := &redis.Message{
		Pattern: "test-*",
		Channel: "test-channel",
		Payload: "pattern message",
	}

	err := processResponse(meta, redisMsg)

	if err != nil {
		t.Errorf("processResponse returned error: %v", err)
	}

	// Wait for async callback
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if receivedMsg == nil {
		t.Fatal("callback not invoked")
	}
	if receivedMsg.Type != MessageTypePMessage {
		t.Errorf("message type = %v, expected %v", receivedMsg.Type, MessageTypePMessage)
	}
	if receivedMsg.Pattern != "test-*" {
		t.Errorf("message pattern = %q, expected %q", receivedMsg.Pattern, "test-*")
	}
	if receivedMsg.Channel != "test-channel" {
		t.Errorf("message channel = %q, expected %q", receivedMsg.Channel, "test-channel")
	}
}

func TestProcessResponse_ShardedMessage(t *testing.T) {
	meta := newTestMetadata()

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

	// Add a sharded subscription
	sub := &subscription{
		channel:  "sharded-channel",
		callback: callback,
		subType:  subTypeSSubscribe,
	}
	meta.subscriptions["sharded-channel"] = []*subscription{sub}

	// Process a sharded message (no pattern)
	redisMsg := &redis.Message{
		Channel: "sharded-channel",
		Payload: "sharded message",
		Pattern: "",
	}

	err := processResponse(meta, redisMsg)

	if err != nil {
		t.Errorf("processResponse returned error: %v", err)
	}

	// Wait for async callback
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if receivedMsg == nil {
		t.Fatal("callback not invoked")
	}
	if receivedMsg.Type != MessageTypeSMessage {
		t.Errorf("message type = %v, expected %v", receivedMsg.Type, MessageTypeSMessage)
	}
	if receivedMsg.Channel != "sharded-channel" {
		t.Errorf("message channel = %q, expected %q", receivedMsg.Channel, "sharded-channel")
	}
}

func TestProcessResponse_UnknownType(t *testing.T) {
	meta := newTestMetadata()

	// Process an unknown type
	err := processResponse(meta, "unexpected string type")

	if err == nil {
		t.Error("expected error for unknown message type")
	}
}

func TestProcessResponse_NoSubscriptions(t *testing.T) {
	meta := newTestMetadata()

	// Process a message with no matching subscriptions
	redisMsg := &redis.Message{
		Channel: "nonexistent-channel",
		Payload: "hello",
	}

	err := processResponse(meta, redisMsg)

	// Should not return an error, just silently ignore
	if err != nil {
		t.Errorf("processResponse returned error: %v", err)
	}
}

func TestProcessResponse_MultipleSubscriptions(t *testing.T) {
	meta := newTestMetadata()

	var wg sync.WaitGroup
	callCount := 0
	var mu sync.Mutex

	callback := func(msg *Message) {
		mu.Lock()
		callCount++
		mu.Unlock()
		wg.Done()
	}

	// Add multiple subscriptions for the same channel
	for i := 0; i < 3; i++ {
		sub := &subscription{
			channel:  "shared-channel",
			callback: callback,
			subType:  subTypeSubscribe,
		}
		meta.subscriptions["shared-channel"] = append(meta.subscriptions["shared-channel"], sub)
	}

	wg.Add(3)

	redisMsg := &redis.Message{
		Channel: "shared-channel",
		Payload: "broadcast",
	}

	err := processResponse(meta, redisMsg)

	if err != nil {
		t.Errorf("processResponse returned error: %v", err)
	}

	// Wait for all callbacks with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for callbacks")
	}

	mu.Lock()
	if callCount != 3 {
		t.Errorf("callback called %d times, expected 3", callCount)
	}
	mu.Unlock()
}

func TestHandleSubscriptionConfirmation_Subscribe(t *testing.T) {
	meta := newTestMetadata()

	// Add pending subscription
	sub := &subscription{
		channel:   "ch1",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}
	meta.pendingSubscriptions["ch1"] = sub

	subMsg := &redis.Subscription{Kind: "subscribe", Channel: "ch1", Count: 1}
	err := handleSubscriptionConfirmation(meta, subMsg)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if sub.state != subStateActive {
		t.Errorf("state = %v, want %v", sub.state, subStateActive)
	}
	if _, ok := meta.pendingSubscriptions["ch1"]; ok {
		t.Error("should be removed from pending")
	}
}

func TestHandleSubscriptionConfirmation_PSubscribe(t *testing.T) {
	meta := newTestMetadata()

	sub := &subscription{
		channel:   "pattern-*",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}
	meta.pendingSubscriptions["pattern-*"] = sub

	subMsg := &redis.Subscription{Kind: "psubscribe", Channel: "pattern-*", Count: 1}
	err := handleSubscriptionConfirmation(meta, subMsg)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if sub.state != subStateActive {
		t.Errorf("state = %v, want %v", sub.state, subStateActive)
	}
}

func TestHandleSubscriptionConfirmation_SSubscribe(t *testing.T) {
	meta := newTestMetadata()

	sub := &subscription{
		channel:   "sharded",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}
	meta.pendingSubscriptions["sharded"] = sub

	subMsg := &redis.Subscription{Kind: "ssubscribe", Channel: "sharded", Count: 1}
	err := handleSubscriptionConfirmation(meta, subMsg)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if sub.state != subStateActive {
		t.Errorf("state = %v, want %v", sub.state, subStateActive)
	}
}

func TestHandleSubscriptionConfirmation_NoPending(t *testing.T) {
	meta := newTestMetadata()

	// No pending subscription
	subMsg := &redis.Subscription{Kind: "subscribe", Channel: "nonexistent", Count: 1}
	err := handleSubscriptionConfirmation(meta, subMsg)

	// Should not error, just ignore
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHandleSubscriptionConfirmation_UnknownKind(t *testing.T) {
	meta := newTestMetadata()

	// Unknown kind should be logged but not error
	subMsg := &redis.Subscription{Kind: "unknown", Channel: "ch", Count: 1}
	err := handleSubscriptionConfirmation(meta, subMsg)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHandleMessageFromPubSub(t *testing.T) {
	meta := newTestMetadata()

	var received *Message
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)

	sub := &subscription{
		channel: "ch1",
		callback: func(msg *Message) {
			mu.Lock()
			received = msg
			mu.Unlock()
			wg.Done()
		},
		subType: subTypeSubscribe,
	}
	meta.subscriptions["ch1"] = []*subscription{sub}

	err := handleMessageFromPubSub(meta, "ch1", "payload")

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("callback not called")
	}
	if received.Type != MessageTypeMessage {
		t.Errorf("Type = %v, want %v", received.Type, MessageTypeMessage)
	}
	if received.Channel != "ch1" {
		t.Errorf("Channel = %q, want %q", received.Channel, "ch1")
	}
	if received.Payload != "payload" {
		t.Errorf("Payload = %q, want %q", received.Payload, "payload")
	}
}

func TestHandlePMessageFromPubSub(t *testing.T) {
	meta := newTestMetadata()

	var received *Message
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)

	sub := &subscription{
		channel: "pat*",
		callback: func(msg *Message) {
			mu.Lock()
			received = msg
			mu.Unlock()
			wg.Done()
		},
		subType: subTypePSubscribe,
	}
	meta.subscriptions["pat*"] = []*subscription{sub}

	err := handlePMessageFromPubSub(meta, "pat*", "pattern-match", "payload")

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("callback not called")
	}
	if received.Type != MessageTypePMessage {
		t.Errorf("Type = %v, want %v", received.Type, MessageTypePMessage)
	}
	if received.Pattern != "pat*" {
		t.Errorf("Pattern = %q, want %q", received.Pattern, "pat*")
	}
	if received.Channel != "pattern-match" {
		t.Errorf("Channel = %q, want %q", received.Channel, "pattern-match")
	}
}

func TestHandleSMessageFromPubSub(t *testing.T) {
	meta := newTestMetadata()

	var received *Message
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)

	sub := &subscription{
		channel: "sharded",
		callback: func(msg *Message) {
			mu.Lock()
			received = msg
			mu.Unlock()
			wg.Done()
		},
		subType: subTypeSSubscribe,
	}
	meta.subscriptions["sharded"] = []*subscription{sub}

	err := handleSMessageFromPubSub(meta, "sharded", "payload")

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("callback not called")
	}
	if received.Type != MessageTypeSMessage {
		t.Errorf("Type = %v, want %v", received.Type, MessageTypeSMessage)
	}
	if received.Channel != "sharded" {
		t.Errorf("Channel = %q, want %q", received.Channel, "sharded")
	}
}

func TestNotifySubscriptionsOfFailure(t *testing.T) {
	meta := newTestMetadata()

	var wg sync.WaitGroup
	var receivedSignals []*Message
	var mu sync.Mutex

	callback := func(msg *Message) {
		mu.Lock()
		receivedSignals = append(receivedSignals, msg)
		mu.Unlock()
		wg.Done()
	}

	// Add multiple subscriptions
	for i := 0; i < 3; i++ {
		ch := "ch" + string(rune('0'+i))
		sub := &subscription{
			channel:  ch,
			callback: callback,
			subType:  subTypeSubscribe,
			state:    subStateActive,
		}
		meta.subscriptions[ch] = []*subscription{sub}
	}

	wg.Add(3)

	testErr := errors.New("connection failed")
	notifySubscriptionsOfFailure(meta, testErr)

	// Wait with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for failure notifications")
	}

	mu.Lock()
	defer mu.Unlock()

	if len(receivedSignals) != 3 {
		t.Errorf("received %d signals, expected 3", len(receivedSignals))
	}

	for _, msg := range receivedSignals {
		if msg.Type != MessageTypeSignal {
			t.Errorf("signal type = %v, want %v", msg.Type, MessageTypeSignal)
		}
		if msg.Signal == nil {
			t.Error("signal info should not be nil")
			continue
		}
		if msg.Signal.EventType != EventNodeFailure {
			t.Errorf("event type = %v, want %v", msg.Signal.EventType, EventNodeFailure)
		}
	}
}

// Tests for runEventLoop

func TestRunEventLoop_DoneChannelClosed(t *testing.T) {
	// Create a redis client with invalid address (won't actually connect)
	client := redis.NewClient(&redis.Options{
		Addr: "invalid:9999",
	})
	defer client.Close()

	pubsub := client.Subscribe(context.Background())
	defer pubsub.Close()

	meta := &pubSubMetadata{
		pubsub:               pubsub,
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
		logger:               slog.Default(),
		nodeAddr:             "test:7000",
	}

	meta.wg.Add(1)

	// Start event loop in goroutine
	go runEventLoop(meta)

	// Close done channel to signal shutdown
	close(meta.done)

	// Wait for goroutine to exit
	done := make(chan struct{})
	go func() {
		meta.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success - event loop exited
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event loop to exit after done channel closed")
	}
}

func TestRunEventLoop_CmdChannelClosed(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "invalid:9999",
	})
	defer client.Close()

	pubsub := client.Subscribe(context.Background())
	defer pubsub.Close()

	meta := &pubSubMetadata{
		pubsub:               pubsub,
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
		logger:               slog.Default(),
		nodeAddr:             "test:7000",
	}

	meta.wg.Add(1)

	go runEventLoop(meta)

	// Close command channel
	close(meta.cmdCh)

	done := make(chan struct{})
	go func() {
		meta.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event loop to exit after cmdCh closed")
	}
}

func TestRunEventLoop_CommandError_SetsFailedState(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "invalid:9999",
	})
	defer client.Close()

	pubsub := client.Subscribe(context.Background())
	defer pubsub.Close()

	meta := &pubSubMetadata{
		pubsub:               pubsub,
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
		logger:               slog.Default(),
		nodeAddr:             "test:7000",
	}

	sub := &subscription{
		channel:   "test-channel",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}
	meta.pendingSubscriptions["test-channel"] = sub

	meta.wg.Add(1)

	go runEventLoop(meta)

	// Send a command that will fail (invalid connection)
	responseCh := make(chan error, 1)
	cmd := &command{
		cmd:      cmdSubscribe,
		args:     []any{"test-channel"},
		sub:      sub,
		response: responseCh,
	}

	meta.cmdCh <- cmd

	// Wait for response or timeout
	select {
	case err := <-responseCh:
		if err == nil {
			t.Error("expected error from failed command")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for command response")
	}

	// Wait for event loop to exit
	done := make(chan struct{})
	go func() {
		meta.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event loop to exit")
	}

	// Verify state is failed
	if meta.getState() != connStateFailed {
		t.Errorf("state = %v, want %v", meta.getState(), connStateFailed)
	}

	// Verify subscription state is failed
	if sub.getState() != subStateFailed {
		t.Errorf("subscription state = %v, want %v", sub.getState(), subStateFailed)
	}

	// Verify removed from pending
	if _, ok := meta.pendingSubscriptions["test-channel"]; ok {
		t.Error("subscription should be removed from pending")
	}
}

func TestRunEventLoop_CommandError_NilResponse(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "invalid:9999",
	})
	defer client.Close()

	pubsub := client.Subscribe(context.Background())
	defer pubsub.Close()

	meta := &pubSubMetadata{
		pubsub:               pubsub,
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
		logger:               slog.Default(),
		nodeAddr:             "test:7000",
	}

	meta.wg.Add(1)

	go runEventLoop(meta)

	// Send a command with nil response channel
	cmd := &command{
		cmd:      cmdSubscribe,
		args:     []any{"test-channel"},
		sub:      nil,
		response: nil, // nil response channel
	}

	meta.cmdCh <- cmd

	// Wait for event loop to exit (will fail and exit)
	done := make(chan struct{})
	go func() {
		meta.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success - should not panic with nil response
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for event loop to exit")
	}

	if meta.getState() != connStateFailed {
		t.Errorf("state = %v, want %v", meta.getState(), connStateFailed)
	}
}

// Tests for sendRedisCommand

func TestSendRedisCommand_AllCommandTypes(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "invalid:9999",
	})
	defer client.Close()

	pubsub := client.Subscribe(context.Background())
	defer pubsub.Close()

	meta := &pubSubMetadata{
		pubsub:   pubsub,
		logger:   slog.Default(),
		nodeAddr: "test:7000",
	}

	tests := []struct {
		name    string
		cmdType string
	}{
		{"Subscribe", cmdSubscribe},
		{"PSubscribe", cmdPSubscribe},
		{"SSubscribe", cmdSSubscribe},
		{"Unsubscribe", cmdUnsubscribe},
		{"PUnsubscribe", cmdPUnsubscribe},
		{"SUnsubscribe", cmdSUnsubscribe},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &command{
				cmd:  tt.cmdType,
				args: []any{"test-channel"},
			}

			// All commands will fail due to invalid connection, but
			// we're testing that the command type is recognized
			err := sendRedisCommand(meta, cmd)

			// Error is expected (invalid connection), but not "unknown command"
			if err != nil && err.Error() == "unknown command: "+tt.cmdType {
				t.Errorf("command type %s was not recognized", tt.cmdType)
			}
		})
	}
}

func TestSendRedisCommand_UnknownCommand(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "invalid:9999",
	})
	defer client.Close()

	pubsub := client.Subscribe(context.Background())
	defer pubsub.Close()

	meta := &pubSubMetadata{
		pubsub:   pubsub,
		logger:   slog.Default(),
		nodeAddr: "test:7000",
	}

	cmd := &command{
		cmd:  "INVALID_COMMAND",
		args: []any{"test-channel"},
	}

	err := sendRedisCommand(meta, cmd)

	if err == nil {
		t.Error("expected error for unknown command")
	}
	if err.Error() != "unknown command: INVALID_COMMAND" {
		t.Errorf("error = %q, want %q", err.Error(), "unknown command: INVALID_COMMAND")
	}
}

func TestSendRedisCommand_RedirectDetection(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "invalid:9999",
	})
	defer client.Close()

	pubsub := client.Subscribe(context.Background())
	defer pubsub.Close()

	redirectCalled := false
	meta := &pubSubMetadata{
		pubsub:   pubsub,
		logger:   slog.Default(),
		nodeAddr: "test:7000",
		onRedirectDetected: func(addr string, isMoved bool) {
			redirectCalled = true
		},
	}

	cmd := &command{
		cmd:  cmdSubscribe,
		args: []any{"test-channel"},
	}

	// This will error (connection failure, not redirect)
	_ = sendRedisCommand(meta, cmd)

	// Connection errors are not redirect errors
	// so redirect callback should not be called
	if redirectCalled {
		t.Error("redirect callback should not be called for non-redirect errors")
	}
}
