package submux

import (
	"context"
	"sync"

	"github.com/redis/go-redis/v9"
)

// subscriptionType represents the type of subscription.
type subscriptionType int

const (
	subTypeSubscribe subscriptionType = iota
	subTypePSubscribe
	subTypeSSubscribe
)

// subscriptionState represents the state of a subscription.
//
// A subscription transitions through these states during its lifecycle:
//   - Pending: Initial state when subscription is created, waiting for Redis confirmation
//   - Active: Subscription confirmed and active, receiving messages
//   - Failed: Subscription failed due to error or connection loss
//   - Closed: Subscription explicitly closed by user (via Unsubscribe)
//
// State transitions:
//   - Pending → Active: When Redis confirms the subscription
//   - Pending → Failed: When subscription command fails or times out
//   - Active → Failed: When connection is lost or an error occurs
//   - Active → Closed: When user calls Unsubscribe
//   - Failed → Closed: When subscription is cleaned up
type subscriptionState int

const (
	// subStatePending indicates the subscription is waiting for Redis confirmation.
	// This is the initial state when a subscription is created. The subscription
	// command has been sent to Redis but confirmation has not yet been received.
	// Messages will not be delivered while in this state.
	subStatePending subscriptionState = iota

	// subStateActive indicates the subscription is confirmed and active.
	// Redis has confirmed the subscription and messages will be delivered to
	// the callback function. This is the normal operational state for a subscription.
	subStateActive

	// subStateFailed indicates the subscription has failed.
	// This state is set when:
	//   - The subscription command fails to send
	//   - The subscription confirmation times out
	//   - The PubSub connection is lost or closed
	//   - Any other error occurs during subscription
	// When a subscription fails, a signal message may be sent to notify
	// the callback about the failure. The subscription will not receive
	// messages while in this state.
	subStateFailed

	// subStateClosed indicates the subscription has been explicitly closed.
	// This state is set when:
	//   - The user calls Unsubscribe for this channel
	//   - The SubMux is closed
	//   - The subscription is cleaned up after a failure
	// Once closed, the subscription will not receive any more messages
	// and cannot be reactivated.
	subStateClosed
)

// subscription represents a single subscription to a channel or pattern.
type subscription struct {
	// channel is the channel or pattern name.
	channel string

	// subType is the subscription type.
	subType subscriptionType

	// pubsub is the PubSub connection handling this subscription.
	pubsub *redis.PubSub

	// callback is the user-provided callback function.
	callback MessageCallback

	// state is the current state of the subscription.
	state subscriptionState

	// mu protects state and confirmation channel.
	mu sync.RWMutex

	// confirmCh is used to signal subscription confirmation (for sync operations).
	confirmCh chan error

	// hashslot is the calculated hashslot for this channel.
	hashslot int
}

// setState sets the subscription state and signals confirmation if needed.
func (s *subscription) setState(newState subscriptionState, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = newState
	if s.confirmCh != nil {
		select {
		case s.confirmCh <- err:
		default:
		}
	}
}

// getState returns the current subscription state.
func (s *subscription) getState() subscriptionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// waitForConfirmation waits for subscription confirmation with context support.
func (s *subscription) waitForConfirmation(ctx context.Context) error {
	s.mu.RLock()
	confirmCh := s.confirmCh
	s.mu.RUnlock()

	if confirmCh == nil {
		return nil
	}

	select {
	case err := <-confirmCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
