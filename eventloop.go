package submux

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// runEventLoop runs the single event loop goroutine for a PubSub metadata.
// It handles both sending commands to Redis and processing incoming messages.
func runEventLoop(meta *pubSubMetadata) {
	defer meta.wg.Done()

	// Get the message channel from PubSub
	// ChannelWithSubscriptions returns a channel that delivers both *redis.Subscription
	// (for subscription confirmations) and *redis.Message (for regular messages)
	msgCh := meta.pubsub.ChannelWithSubscriptions()

	for {
		select {
		case cmd, ok := <-meta.cmdCh:
			if !ok {
				return
			}

			// Send command to Redis
			err := sendRedisCommand(meta, cmd)
			if err != nil {
				// Remove pending subscription if this was a subscription command
				if cmd.sub != nil && (cmd.cmd == cmdSubscribe || cmd.cmd == cmdPSubscribe || cmd.cmd == cmdSSubscribe) {
					meta.removePendingSubscription(cmd.sub.channel)
					cmd.sub.setState(subStateFailed, err)
				}
				// Signal error to command sender
				if cmd.response != nil {
					select {
					case cmd.response <- err:
					default:
					}
				}
				// Mark PubSub as failed
				meta.setState(connStateFailed)
				log.Printf("submux: command send error on PubSub to %s: %v", meta.nodeAddr, err)
				return
			}

			// Signal success
			if cmd.response != nil {
				select {
				case cmd.response <- nil:
				default:
				}
			}

		case msg, ok := <-msgCh:
			if !ok {
				// Channel closed - mark as failed
				meta.setState(connStateFailed)
				log.Printf("submux: PubSub channel closed for %s", meta.nodeAddr)

				// Notify all subscriptions of failure
				notifySubscriptionsOfFailure(meta, fmt.Errorf("pubsub channel closed"))
				return
			}

			// Process the message - can be either *redis.Subscription or *redis.Message
			if err := processResponse(meta, msg); err != nil {
				log.Printf("submux: error processing response: %v", err)
			}

		case <-meta.done:
			return
		}
	}
}

// sendRedisCommand sends a command to Redis using the PubSub connection.
func sendRedisCommand(meta *pubSubMetadata, cmd *command) error {
	ctx := context.Background()

	// Convert []any to []string
	args := make([]string, len(cmd.args))
	for i, arg := range cmd.args {
		args[i] = arg.(string)
	}

	switch cmd.cmd {

	case cmdSubscribe:
		return meta.pubsub.Subscribe(ctx, args...)
	case cmdPSubscribe:
		return meta.pubsub.PSubscribe(ctx, args...)
	case cmdSSubscribe:
		return meta.pubsub.SSubscribe(ctx, args...)
	case cmdUnsubscribe:
		return meta.pubsub.Unsubscribe(ctx, args...)
	case cmdPUnsubscribe:
		return meta.pubsub.PUnsubscribe(ctx, args...)
	case cmdSUnsubscribe:
		return meta.pubsub.SUnsubscribe(ctx, args...)
	default:
		return fmt.Errorf("unknown command: %s", cmd.cmd)
	}
}

// processResponse processes a Redis PubSub message or subscription confirmation.
// ChannelWithSubscriptions() delivers either *redis.Subscription (confirmations) or *redis.Message (regular messages).
func processResponse(meta *pubSubMetadata, msg interface{}) error {
	// First, check if this is a subscription confirmation
	subMsg, ok := msg.(*redis.Subscription)
	if ok {
		// This is a subscription confirmation message
		return handleSubscriptionConfirmation(meta, subMsg)
	}

	// Otherwise, it's a regular message
	redisMsg, ok := msg.(*redis.Message)
	if !ok {
		return fmt.Errorf("unexpected message type: %T", msg)
	}

	// Route based on the subscription type
	// For regular SUBSCRIBE, Pattern is empty
	// For PSUBSCRIBE, Pattern contains the pattern that matched
	// For SSUBSCRIBE, we need to check the subscription type
	if redisMsg.Pattern != "" {
		// This is a pattern message (PSUBSCRIBE)
		return handlePMessageFromPubSub(meta, redisMsg.Pattern, redisMsg.Channel, redisMsg.Payload)
	}

	// Check if this is a sharded subscription by looking up the channel
	subs := meta.getSubscriptions(redisMsg.Channel)
	if len(subs) > 0 && subs[0].subType == subTypeSSubscribe {
		// This is a sharded subscription message
		return handleSMessageFromPubSub(meta, redisMsg.Channel, redisMsg.Payload)
	}

	// Regular message (SUBSCRIBE)
	return handleMessageFromPubSub(meta, redisMsg.Channel, redisMsg.Payload)
}

// handleSubscriptionConfirmation handles subscription confirmation messages from Redis.
func handleSubscriptionConfirmation(meta *pubSubMetadata, sub *redis.Subscription) error {
	// sub.Kind can be "subscribe", "psubscribe", "ssubscribe", "unsubscribe", "punsubscribe", "sunsubscribe"
	// sub.Channel is the channel/pattern name
	// sub.Count is the total number of subscriptions

	switch sub.Kind {
	case "subscribe", "psubscribe", "ssubscribe":
		// Mark the pending subscription as active
		pendingSub := meta.getPendingSubscription(sub.Channel)
		if pendingSub != nil {
			pendingSub.setState(subStateActive, nil)
			meta.removePendingSubscription(sub.Channel)
		}

	case "unsubscribe", "punsubscribe", "sunsubscribe":
		// Unsubscribe confirmation - nothing to do

	default:
		log.Printf("submux: unknown subscription kind: %s", sub.Kind)
	}

	return nil
}

// handleMessageFromPubSub handles regular SUBSCRIBE messages.
func handleMessageFromPubSub(meta *pubSubMetadata, channel, payload string) error {
	subs := meta.getSubscriptions(channel)
	if len(subs) == 0 {
		// Subscription not found
		return nil
	}

	// Create message and invoke all callbacks
	msg := &Message{
		Type:      MessageTypeMessage,
		Channel:   channel,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	for _, sub := range subs {
		invokeCallback(sub.callback, msg)
	}
	return nil
}

// handlePMessageFromPubSub handles PSUBSCRIBE pattern messages.
func handlePMessageFromPubSub(meta *pubSubMetadata, pattern, channel, payload string) error {
	subs := meta.getSubscriptions(pattern)
	if len(subs) == 0 {
		// Subscription not found
		return nil
	}

	// Create message and invoke all callbacks
	msg := &Message{
		Type:      MessageTypePMessage,
		Pattern:   pattern,
		Channel:   channel,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	for _, sub := range subs {
		invokeCallback(sub.callback, msg)
	}
	return nil
}

// handleSMessageFromPubSub handles SSUBSCRIBE sharded messages.
func handleSMessageFromPubSub(meta *pubSubMetadata, channel, payload string) error {
	// For SSUBSCRIBE, the pattern field in go-redis is used as the channel
	// We need to look up subscriptions by channel name
	subs := meta.getSubscriptions(channel)
	if len(subs) == 0 {
		// Try pattern lookup as fallback
		// In some cases, go-redis might use pattern field
		return nil
	}

	// Create message and invoke all callbacks
	msg := &Message{
		Type:      MessageTypeSMessage,
		Channel:   channel,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	for _, sub := range subs {
		invokeCallback(sub.callback, msg)
	}
	return nil
}

// notifySubscriptionsOfFailure notifies all subscriptions on this PubSub of a failure.
func notifySubscriptionsOfFailure(meta *pubSubMetadata, err error) {
	subs := meta.getAllSubscriptions()
	for _, sub := range subs {
		sub.setState(subStateFailed, err)

		// Send signal message about connection failure
		signal := &SignalInfo{
			EventType: EventNodeFailure,
			Details:   err.Error(),
		}
		msg := &Message{
			Type:      MessageTypeSignal,
			Signal:    signal,
			Timestamp: time.Now(),
		}
		invokeCallback(sub.callback, msg)
	}
}
