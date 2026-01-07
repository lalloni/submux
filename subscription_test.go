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
