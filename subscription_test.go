package submux

import (
	"context"
	"testing"
	"time"
)

func TestSubscription_Creation(t *testing.T) {
	sub := &subscription{
		channel:   "testchannel",
		subType:   subTypeSubscribe,
		callback:  func(msg *Message) {},
		state:     subStatePending,
		confirmCh: make(chan error, 1),
		hashslot:  1234,
	}

	if sub.channel != "testchannel" {
		t.Errorf("subscription.channel = %q, want %q", sub.channel, "testchannel")
	}
	if sub.subType != subTypeSubscribe {
		t.Errorf("subscription.subType = %v, want %v", sub.subType, subTypeSubscribe)
	}
	if sub.getState() != subStatePending {
		t.Errorf("subscription.state = %v, want %v", sub.getState(), subStatePending)
	}
	if sub.hashslot != 1234 {
		t.Errorf("subscription.hashslot = %d, want %d", sub.hashslot, 1234)
	}
}

func TestSubscription_StateTransitions(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}

	// Pending -> Active
	sub.setState(subStateActive, nil)
	if sub.getState() != subStateActive {
		t.Errorf("Expected state Active, got %v", sub.getState())
	}

	// Active -> Failed
	err := context.DeadlineExceeded
	sub.setState(subStateFailed, err)
	if sub.getState() != subStateFailed {
		t.Errorf("Expected state Failed, got %v", sub.getState())
	}
	// Error is passed through confirmCh, not stored directly

	// Failed -> Closed
	sub.setState(subStateClosed, nil)
	if sub.getState() != subStateClosed {
		t.Errorf("Expected state Closed, got %v", sub.getState())
	}
}

func TestSubscription_WaitForConfirmation(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}

	// Test successful confirmation
	done := make(chan bool)
	go func() {
		err := sub.waitForConfirmation(context.Background())
		if err != nil {
			t.Errorf("waitForConfirmation returned error: %v", err)
		}
		done <- true
	}()

	// Send confirmation
	sub.confirmCh <- nil
	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("waitForConfirmation timed out")
	}

	// Test error confirmation
	sub2 := &subscription{
		channel:   "test2",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}

	done2 := make(chan bool)
	testErr := context.DeadlineExceeded
	go func() {
		err := sub2.waitForConfirmation(context.Background())
		if err != testErr {
			t.Errorf("waitForConfirmation returned error %v, want %v", err, testErr)
		}
		done2 <- true
	}()

	sub2.confirmCh <- testErr
	select {
	case <-done2:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("waitForConfirmation timed out")
	}
}

func TestSubscription_WaitForConfirmation_Timeout(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := sub.waitForConfirmation(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("waitForConfirmation returned %v, want %v", err, context.DeadlineExceeded)
	}
}

func TestSubscription_WaitForConfirmation_ContextCancellation(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := sub.waitForConfirmation(ctx)
	if err != context.Canceled {
		t.Errorf("waitForConfirmation returned %v, want %v", err, context.Canceled)
	}
}

func TestSubscription_StateGetterSetter(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}

	// Test getter
	if sub.getState() != subStatePending {
		t.Errorf("getState() = %v, want %v", sub.getState(), subStatePending)
	}

	// Test setter with nil error (Active state)
	sub.setState(subStateActive, nil)
	if sub.getState() != subStateActive {
		t.Errorf("After setState(Active), getState() = %v, want %v", sub.getState(), subStateActive)
	}
	// Consume the nil error from confirmCh
	select {
	case <-sub.confirmCh:
	default:
	}

	// Test error setting (create new subscription to avoid channel issues)
	sub2 := &subscription{
		channel:   "test2",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}
	testErr := context.DeadlineExceeded
	sub2.setState(subStateFailed, testErr)
	if sub2.getState() != subStateFailed {
		t.Errorf("After setState(Failed), getState() = %v, want %v", sub2.getState(), subStateFailed)
	}
	// Error is passed through confirmCh, verify it's there
	select {
	case err := <-sub2.confirmCh:
		if err != testErr {
			t.Errorf("confirmCh received %v, want %v", err, testErr)
		}
	default:
		t.Error("Expected error in confirmCh but channel is empty")
	}
}

func TestSubscription_ErrorHandling(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}

	// Test setting error through confirmCh
	err1 := context.DeadlineExceeded
	sub.setState(subStateFailed, err1)
	if sub.getState() != subStateFailed {
		t.Errorf("Expected state Failed, got %v", sub.getState())
	}

	// Verify error is sent through confirmCh
	select {
	case err := <-sub.confirmCh:
		if err != err1 {
			t.Errorf("confirmCh received %v, want %v", err, err1)
		}
	default:
		t.Error("Expected error in confirmCh but channel is empty")
	}

	// Test clearing error (setting Active with nil error)
	sub.setState(subStateActive, nil)
	if sub.getState() != subStateActive {
		t.Errorf("Expected state Active, got %v", sub.getState())
	}
}

// Edge case tests for subscription state transitions

func TestSubscription_SetStateWithNilConfirmCh(t *testing.T) {
	// Test that setState doesn't panic when confirmCh is nil
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: nil, // No confirmation channel
	}

	// Should not panic
	sub.setState(subStateActive, nil)
	if sub.getState() != subStateActive {
		t.Errorf("Expected state Active, got %v", sub.getState())
	}

	sub.setState(subStateFailed, context.DeadlineExceeded)
	if sub.getState() != subStateFailed {
		t.Errorf("Expected state Failed, got %v", sub.getState())
	}
}

func TestSubscription_SetStateDropsWhenConfirmChFull(t *testing.T) {
	// Test that setState doesn't block when confirmCh is full
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}

	// Fill the channel
	sub.confirmCh <- nil

	// This should not block (select with default case)
	sub.setState(subStateActive, nil)
	if sub.getState() != subStateActive {
		t.Errorf("Expected state Active, got %v", sub.getState())
	}
}

func TestSubscription_WaitForConfirmation_NilConfirmCh(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: nil,
	}

	// Should return immediately with nil error when confirmCh is nil
	err := sub.waitForConfirmation(context.Background())
	if err != nil {
		t.Errorf("waitForConfirmation with nil confirmCh should return nil, got %v", err)
	}
}

func TestSubscription_AllStateTransitionPaths(t *testing.T) {
	// Test all valid state transitions according to documentation:
	// Pending → Active, Pending → Failed
	// Active → Failed, Active → Closed
	// Failed → Closed

	tests := []struct {
		name      string
		fromState subscriptionState
		toState   subscriptionState
	}{
		{"Pending → Active", subStatePending, subStateActive},
		{"Pending → Failed", subStatePending, subStateFailed},
		{"Active → Failed", subStateActive, subStateFailed},
		{"Active → Closed", subStateActive, subStateClosed},
		{"Failed → Closed", subStateFailed, subStateClosed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := &subscription{
				channel:   "test",
				state:     tt.fromState,
				confirmCh: make(chan error, 1),
			}

			sub.setState(tt.toState, nil)
			if sub.getState() != tt.toState {
				t.Errorf("transition %s: got state %v, want %v", tt.name, sub.getState(), tt.toState)
			}
		})
	}
}

func TestSubscription_SameStateTransition(t *testing.T) {
	// Test that setting the same state is allowed
	states := []subscriptionState{subStatePending, subStateActive, subStateFailed, subStateClosed}

	for _, state := range states {
		t.Run("same state", func(t *testing.T) {
			sub := &subscription{
				channel:   "test",
				state:     state,
				confirmCh: make(chan error, 1),
			}

			sub.setState(state, nil)
			if sub.getState() != state {
				t.Errorf("same state transition failed: got %v, want %v", sub.getState(), state)
			}
		})
	}
}

func TestSubscription_ConcurrentStateAccess(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 100),
	}

	done := make(chan bool)
	iterations := 1000

	// Writer goroutine
	go func() {
		for i := 0; i < iterations; i++ {
			if i%2 == 0 {
				sub.setState(subStateActive, nil)
			} else {
				sub.setState(subStateFailed, nil)
			}
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < iterations; i++ {
			state := sub.getState()
			// Just verify no panic and state is valid
			if state != subStatePending && state != subStateActive && state != subStateFailed && state != subStateClosed {
				t.Errorf("Invalid state: %v", state)
			}
		}
		done <- true
	}()

	// Wait for both goroutines
	<-done
	<-done
}

func TestSubscription_SubscriptionTypes(t *testing.T) {
	types := []struct {
		subType subscriptionType
		name    string
	}{
		{subTypeSubscribe, "Subscribe"},
		{subTypePSubscribe, "PSubscribe"},
		{subTypeSSubscribe, "SSubscribe"},
	}

	for _, tt := range types {
		t.Run(tt.name, func(t *testing.T) {
			sub := &subscription{
				channel: "test",
				subType: tt.subType,
				state:   subStatePending,
			}

			if sub.subType != tt.subType {
				t.Errorf("subType = %v, want %v", sub.subType, tt.subType)
			}
		})
	}
}

func TestSubscription_HashslotValues(t *testing.T) {
	tests := []struct {
		name     string
		hashslot int
	}{
		{"minimum hashslot", 0},
		{"maximum hashslot", 16383},
		{"middle hashslot", 8192},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := &subscription{
				channel:  "test",
				hashslot: tt.hashslot,
				state:    subStatePending,
			}

			if sub.hashslot != tt.hashslot {
				t.Errorf("hashslot = %d, want %d", sub.hashslot, tt.hashslot)
			}
		})
	}
}

func TestSubscription_EmptyChannel(t *testing.T) {
	sub := &subscription{
		channel:   "",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}

	if sub.channel != "" {
		t.Errorf("channel = %q, want empty string", sub.channel)
	}

	// State operations should still work
	sub.setState(subStateActive, nil)
	if sub.getState() != subStateActive {
		t.Errorf("state = %v, want Active", sub.getState())
	}
}

func TestSubscription_NilCallback(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		callback:  nil,
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}

	// Nil callback should be allowed at struct level
	if sub.callback != nil {
		t.Error("callback should be nil")
	}

	// State operations should still work
	sub.setState(subStateActive, nil)
	if sub.getState() != subStateActive {
		t.Errorf("state = %v, want Active", sub.getState())
	}
}
