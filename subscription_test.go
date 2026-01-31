package submux

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestSubscription_Creation(t *testing.T) {
	sub := &subscription{
		channel:   "testchannel",
		subType:   subTypeSubscribe,
		callback:  func(ctx context.Context, msg *Message) {},
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

// Tests for waitForActive method

func TestSubscription_WaitForActive_Success(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
	}

	// Start goroutine to wait
	result := make(chan error, 1)
	go func() {
		result <- sub.waitForActive(context.Background())
	}()

	// Give goroutine time to start waiting
	time.Sleep(10 * time.Millisecond)

	// Transition to active - this should broadcast to waiters
	sub.setState(subStateActive, nil)

	// Should return without error
	select {
	case err := <-result:
		if err != nil {
			t.Errorf("waitForActive returned error: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for waitForActive to return")
	}
}

func TestSubscription_WaitForActive_Error(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
	}

	// Start goroutine to wait
	result := make(chan error, 1)
	go func() {
		result <- sub.waitForActive(context.Background())
	}()

	// Give goroutine time to start waiting
	time.Sleep(10 * time.Millisecond)

	// Transition to failed with error - this should broadcast the error to waiters
	testErr := context.DeadlineExceeded
	sub.setState(subStateFailed, testErr)

	// Should return the error
	select {
	case err := <-result:
		if err != testErr {
			t.Errorf("waitForActive returned %v, want %v", err, testErr)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for waitForActive to return")
	}
}

func TestSubscription_WaitForActive_AlreadyActive(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStateActive, // Already active
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
	}

	// Should return immediately when already not pending
	err := sub.waitForActive(context.Background())
	if err != nil {
		t.Errorf("waitForActive should return nil for already active subscription, got %v", err)
	}
}

func TestSubscription_WaitForActive_AlreadyFailed(t *testing.T) {
	sub := &subscription{
		channel:      "test",
		state:        subStateFailed, // Already failed
		confirmCh:    make(chan error, 1),
		doneCh:       make(chan struct{}),
		confirmErr:   context.DeadlineExceeded,
		doneChClosed: true,
	}
	close(sub.doneCh) // Simulate previously closed

	// Should return the stored error immediately
	err := sub.waitForActive(context.Background())
	if err != context.DeadlineExceeded {
		t.Errorf("waitForActive should return stored error for failed subscription, got %v", err)
	}
}

func TestSubscription_WaitForActive_ContextCancellation(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// waitForActive should return context error when context is canceled
	err := sub.waitForActive(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("waitForActive returned %v, want %v", err, context.DeadlineExceeded)
	}
}

func TestSubscription_WaitForActive_MultipleWaiters(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
	}

	numWaiters := 10
	results := make(chan error, numWaiters)

	// Start multiple waiters
	for range numWaiters {
		go func() {
			results <- sub.waitForActive(context.Background())
		}()
	}

	// Give all goroutines time to start waiting
	time.Sleep(20 * time.Millisecond)

	// Transition to active - should broadcast to ALL waiters
	sub.setState(subStateActive, nil)

	// All waiters should receive nil (success)
	successCount := 0
	for i := range numWaiters {
		select {
		case err := <-results:
			if err == nil {
				successCount++
			} else {
				t.Errorf("waiter %d received error: %v", i, err)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("timeout waiting for waiter %d", i)
		}
	}

	if successCount != numWaiters {
		t.Errorf("expected %d successful waiters, got %d", numWaiters, successCount)
	}
}

func TestSubscription_WaitForActive_NilDoneCh(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
		doneCh:    nil, // No doneCh
	}

	// Should return nil immediately when doneCh is nil
	err := sub.waitForActive(context.Background())
	if err != nil {
		t.Errorf("waitForActive with nil doneCh should return nil, got %v", err)
	}
}

func TestSubscription_WaitForActive_MultipleWaiters_WithError(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
	}

	numWaiters := 5
	results := make(chan error, numWaiters)

	// Start multiple waiters
	for range numWaiters {
		go func() {
			results <- sub.waitForActive(context.Background())
		}()
	}

	// Give all goroutines time to start waiting
	time.Sleep(20 * time.Millisecond)

	// Transition to failed with error - should broadcast to ALL waiters
	testErr := context.DeadlineExceeded
	sub.setState(subStateFailed, testErr)

	// All waiters should receive the same error
	errorCount := 0
	for i := range numWaiters {
		select {
		case err := <-results:
			if err == testErr {
				errorCount++
			} else {
				t.Errorf("waiter %d received wrong error: %v", i, err)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("timeout waiting for waiter %d", i)
		}
	}

	if errorCount != numWaiters {
		t.Errorf("expected %d waiters with error, got %d", numWaiters, errorCount)
	}
}

// Test setState broadcasts to doneCh only once (Pending -> non-Pending)

func TestSubscription_SetState_DoneChBroadcastOnce(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
	}

	// First transition: Pending -> Active (should close doneCh)
	sub.setState(subStateActive, nil)

	// Verify doneCh is closed
	select {
	case <-sub.doneCh:
		// Good - channel is closed
	default:
		t.Error("doneCh should be closed after Pending -> Active transition")
	}

	// Second transition: Active -> Failed (should NOT panic from double close)
	sub.setState(subStateFailed, context.DeadlineExceeded)

	// Verify state is updated
	if sub.getState() != subStateFailed {
		t.Errorf("state = %v, want Failed", sub.getState())
	}
}

// Invalid state transition test

func TestSubscription_InvalidTransition_ClosedToActive(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStateClosed,
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
	}

	// This is technically an invalid transition but should not panic
	// (the code allows all transitions for simplicity)
	sub.setState(subStateActive, nil)
	if sub.getState() != subStateActive {
		t.Errorf("state = %v, want Active", sub.getState())
	}
}

// Rapid state transitions test

func TestSubscription_RapidStateTransitions(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 100), // Large buffer for rapid transitions
		doneCh:    make(chan struct{}),
	}

	// Perform rapid state transitions
	for i := range 100 {
		switch i % 3 {
		case 0:
			sub.setState(subStateActive, nil)
		case 1:
			sub.setState(subStateFailed, nil)
		case 2:
			sub.setState(subStateClosed, nil)
		}
	}

	// Should not panic and state should be deterministic
	// 99 % 3 == 0, so last setState was to Active
	finalState := sub.getState()
	if finalState != subStateActive {
		t.Errorf("final state = %v, want Active", finalState)
	}
}

// Tests for concurrent state changes

func TestSubscription_ConcurrentSetState(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 100),
		doneCh:    make(chan struct{}),
	}

	numGoroutines := 100
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Many goroutines setting state simultaneously
	for range numGoroutines {
		go func() {
			defer wg.Done()
			// Random state changes
			sub.setState(subStateActive, nil)
			sub.setState(subStateFailed, context.DeadlineExceeded)
		}()
	}

	wg.Wait()

	// Should not panic or race - final state will be non-deterministic but valid
	state := sub.getState()
	if state != subStateActive && state != subStateFailed {
		t.Errorf("final state should be Active or Failed, got %v", state)
	}
}

// Tests for waitForConfirmation

func TestSubscription_WaitForConfirmation_Success(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
	}

	// Send success confirmation
	go func() {
		time.Sleep(10 * time.Millisecond)
		sub.confirmCh <- nil
	}()

	err := sub.waitForConfirmation(context.Background())
	if err != nil {
		t.Errorf("waitForConfirmation should return nil on success, got %v", err)
	}
}

func TestSubscription_WaitForConfirmation_Error(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
	}

	// Send error confirmation
	testErr := context.DeadlineExceeded
	go func() {
		time.Sleep(10 * time.Millisecond)
		sub.confirmCh <- testErr
	}()

	err := sub.waitForConfirmation(context.Background())
	if err != testErr {
		t.Errorf("waitForConfirmation should return error, got %v", err)
	}
}

func TestSubscription_WaitForConfirmation_ContextCanceled(t *testing.T) {
	sub := &subscription{
		channel:   "test",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := sub.waitForConfirmation(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("waitForConfirmation should return context error, got %v", err)
	}
}

// Edge case: Subscription types

func TestSubscriptionType_String(t *testing.T) {
	tests := []struct {
		subType  subscriptionType
		expected string
	}{
		{subTypeSubscribe, "subscribe"},
		{subTypePSubscribe, "psubscribe"},
		{subTypeSSubscribe, "ssubscribe"},
		{subscriptionType(99), "unknown"},
	}

	for _, tt := range tests {
		result := subscriptionTypeToString(tt.subType)
		if result != tt.expected {
			t.Errorf("subscriptionTypeToString(%d) = %q, want %q", tt.subType, result, tt.expected)
		}
	}
}

// Edge case: Closed subscription behavior

func TestSubscription_ClosedState_NoCallback(t *testing.T) {
	var callbackInvoked bool
	sub := &subscription{
		channel: "test",
		state:   subStateClosed,
		callback: func(ctx context.Context, msg *Message) {
			callbackInvoked = true
		},
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
	}

	// Set state to closed - callback should not be invoked by this
	sub.setState(subStateClosed, nil)

	// Callback invocation is separate from state - this tests state management
	if sub.getState() != subStateClosed {
		t.Error("state should be Closed")
	}
	if callbackInvoked {
		t.Error("callback should not be invoked by setState")
	}
}
