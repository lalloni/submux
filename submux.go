// Package submux provides connection multiplexing for Redis Cluster Pub/Sub operations.
//
// submux minimizes the number of required Pub/Sub connections by intelligently
// multiplexing multiple subscriptions over a small number of dedicated connections.
// It supports regular subscriptions (SUBSCRIBE), pattern subscriptions (PSUBSCRIBE),
// and sharded subscriptions (SSUBSCRIBE) in Redis Cluster environments.
//
// Key features:
//   - Hashslot-based connection reuse across subscriptions
//   - Load balancing across master and replica nodes
//   - Multiple subscriptions to the same channel with independent callbacks
//   - Automatic topology change detection and signal message delivery
//   - Thread-safe operations
//
// # Basic Usage
//
// The simplest way to use submux is to create a SubMux instance and subscribe to channels:
//
//	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
//	    Addrs: []string{"localhost:7000", "localhost:7001"},
//	})
//
//	subMux, err := submux.New(clusterClient)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer subMux.Close()
//
//	sub, err := subMux.SubscribeSync(context.Background(), []string{"mychannel"}, func(msg *submux.Message) {
//	    switch msg.Type {
//	    case submux.MessageTypeMessage:
//	        fmt.Printf("Received message on %s: %s\n", msg.Channel, msg.Payload)
//	    case submux.MessageTypeSignal:
//	        fmt.Printf("Topology change: %s\n", msg.Signal.Details)
//	    }
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer sub.Unsubscribe(context.Background())
//
// # Pattern Subscriptions
//
// Subscribe to multiple channels matching a pattern:
//
//	sub, err := subMux.PSubscribeSync(context.Background(), []string{"news:*"}, func(msg *submux.Message) {
//	    fmt.Printf("Pattern match: channel=%s, payload=%s\n", msg.Channel, msg.Payload)
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer sub.Unsubscribe(context.Background())
//
// # Sharded Subscriptions
//
// For Redis 7.0+ sharded pub/sub:
//
//	sub, err := subMux.SSubscribeSync(context.Background(), []string{"shardchannel"}, func(msg *submux.Message) {
//	    fmt.Printf("Sharded message: %s\n", msg.Payload)
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer sub.Unsubscribe(context.Background())
//
// # Multiple Subscriptions to Same Channel
//
// You can subscribe to the same channel multiple times with different callbacks:
//
//	// First subscription
//	sub1, err := subMux.SubscribeSync(ctx, []string{"events"}, func(msg *submux.Message) {
//	    log.Printf("Logger: %s", msg.Payload)
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Second subscription to the same channel
//	sub2, err := subMux.SubscribeSync(ctx, []string{"events"}, func(msg *submux.Message) {
//	    metrics.Increment("events.received")
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Both callbacks will be invoked when messages arrive on "events".
//	// To unsubscribe just the first callback:
//	// sub1.Unsubscribe(ctx)
//
// # Configuration Options
//
// Configure SubMux behavior using options:
//
//	subMux, err := submux.New(clusterClient,
//	    submux.WithAutoResubscribe(true),              // Auto-resubscribe on topology changes
//	    submux.WithReplicaPreference(true),            // Prefer replica nodes
//	    submux.WithTopologyPollInterval(2*time.Second), // Poll topology every 2 seconds
//	)
//
// # Handling Topology Changes
//
// SubMux automatically detects hashslot migrations and sends signal messages:
//
//	sub, err := subMux.SubscribeSync(ctx, []string{"mychannel"}, func(msg *submux.Message) {
//	    if msg.Type == submux.MessageTypeSignal {
//	        switch msg.Signal.EventType {
//	        case "migration":
//	            fmt.Printf("Hashslot %d migrated from %s to %s\n",
//	                msg.Signal.Hashslot, msg.Signal.OldNode, msg.Signal.NewNode)
//	        case "node_failure":
//	            fmt.Printf("Node failure: %s\n", msg.Signal.Details)
//	        }
//	    } else {
//	        // Handle regular message
//	        processMessage(msg)
//	    }
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer sub.Unsubscribe(ctx)
//
// # Unsubscribing
//
// Unsubscribe from channels when no longer needed using the Sub returned
// from SubscribeSync, PSubscribeSync, or SSubscribeSync:
//
//	sub, err := subMux.SubscribeSync(ctx, []string{"mychannel"}, callback)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	// Later, when done:
//	sub.Unsubscribe(ctx)
//
// Each Sub represents a specific callback. If you subscribe to the same
// channel multiple times with different callbacks, each returns its own Sub
// that can be unsubscribed independently.
//
// # Best Practices
//
//  1. Always call Close() when done to clean up resources
//  2. Handle signal messages to be aware of topology changes
//  3. Use context cancellation for subscription operations
//  4. Enable auto-resubscribe if you want automatic recovery from migrations
//  5. Prefer replica nodes to reduce load on master nodes
//
// See DESIGN.md for more detailed guidance.
package submux

import (
	"context"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"
)

const (
	cmdSubscribe    = "SUBSCRIBE"
	cmdPSubscribe   = "PSUBSCRIBE"
	cmdSSubscribe   = "SSUBSCRIBE"
	cmdUnsubscribe  = "UNSUBSCRIBE"
	cmdPUnsubscribe = "PUNSUBSCRIBE"
	cmdSUnsubscribe = "SUNSUBSCRIBE"
)

// SubMux provides connection multiplexing for Redis Cluster Pub/Sub operations.
type SubMux struct {
	// clusterClient is the underlying Redis cluster client.
	clusterClient *redis.ClusterClient

	// config holds the configuration.
	config *config

	// pool manages PubSub connections to cluster nodes.
	pool *pubSubPool

	// topologyMonitor monitors cluster topology changes.
	topologyMonitor *topologyMonitor

	// subscriptions maps channel/pattern name to list of subscriptions (allowing multiple subscriptions per channel).
	subscriptions map[string][]*subscription

	// mu protects subscriptions and closed flag.
	mu sync.RWMutex

	// closed indicates if SubMux is closed.
	closed bool

	// closeOnce ensures Close is only called once.
	closeOnce sync.Once
}

// New creates a new SubMux instance wrapping a ClusterClient.
func New(clusterClient *redis.ClusterClient, opts ...Option) (*SubMux, error) {
	if clusterClient == nil {
		return nil, fmt.Errorf("%w", ErrInvalidClusterClient)
	}

	// Apply configuration options
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	// Create metrics recorder from configured MeterProvider
	cfg.recorder = newMetricsRecorder(cfg.meterProvider, cfg.logger)

	// Create PubSub pool
	pool := newPubSubPool(clusterClient, cfg)

	// Create SubMux
	subMux := &SubMux{
		clusterClient: clusterClient,
		config:        cfg,
		pool:          pool,
		subscriptions: make(map[string][]*subscription),
	}

	// Create and start topology monitor
	subMux.topologyMonitor = newTopologyMonitor(clusterClient, cfg, subMux)
	subMux.pool.setTopologyMonitor(subMux.topologyMonitor)
	subMux.topologyMonitor.start()

	return subMux, nil
}

// SubscribeSync subscribes to one or more channels using regular channel subscription.
// It waits for the subscription to be confirmed before returning.
// Returns a Subscription that can be used to unsubscribe the provided callback.
func (sm *SubMux) SubscribeSync(ctx context.Context, channels []string, callback MessageCallback) (*Sub, error) {
	return sm.subscribe(ctx, channels, subTypeSubscribe, callback)
}

// PSubscribeSync subscribes to one or more channel patterns using pattern subscription.
// It waits for the subscription to be confirmed before returning.
// Returns a Sub that can be used to unsubscribe the provided callback.
func (sm *SubMux) PSubscribeSync(ctx context.Context, patterns []string, callback MessageCallback) (*Sub, error) {
	return sm.subscribe(ctx, patterns, subTypePSubscribe, callback)
}

// SSubscribeSync subscribes to one or more channel patterns using sharded subscription (Redis 7.0+).
// It waits for the subscription to be confirmed before returning.
// Returns a Sub that can be used to unsubscribe the provided callback.
func (sm *SubMux) SSubscribeSync(ctx context.Context, patterns []string, callback MessageCallback) (*Sub, error) {
	return sm.subscribe(ctx, patterns, subTypeSSubscribe, callback)
}

// subscribe is the internal implementation for all subscription types.
// It returns a Sub that contains all internal subscriptions created for the provided callback.
func (sm *SubMux) subscribe(ctx context.Context, channels []string, subType subscriptionType, callback MessageCallback) (*Sub, error) {
	sm.mu.Lock()
	if sm.closed {
		sm.mu.Unlock()
		return nil, fmt.Errorf("%w", ErrClosed)
	}
	sm.mu.Unlock()

	if len(channels) == 0 {
		return nil, fmt.Errorf("%w: channels list is empty", ErrInvalidChannel)
	}

	// Determine command name based on subscription type
	var cmdName string
	switch subType {
	case subTypeSubscribe:
		cmdName = cmdSubscribe
	case subTypePSubscribe:
		cmdName = cmdPSubscribe
	case subTypeSSubscribe:
		cmdName = cmdSSubscribe
	default:
		return nil, fmt.Errorf("invalid subscription type: %d", subType)
	}

	// Create Sub to hold all internal subscriptions
	sub := &Sub{
		subMux: sm,
		subs:   make([]*subscription, 0, len(channels)),
	}

	// Subscribe to each channel
	for _, channel := range channels {
		internalSub, err := sm.subscribeToChannel(ctx, channel, subType, cmdName, callback)
		if err != nil {
			// If we fail partway through, unsubscribe what we've already subscribed to
			sm.unsubscribeSubscription(sub)
			return nil, fmt.Errorf("failed to subscribe to channel %s: %w", channel, err)
		}
		sub.subs = append(sub.subs, internalSub)
	}

	return sub, nil
}

// subscribeToChannel subscribes to a single channel.
// Multiple subscriptions to the same channel are allowed, each with its own callback.
// Returns the internal subscription created.
func (sm *SubMux) subscribeToChannel(ctx context.Context, channel string, subType subscriptionType, cmdName string, callback MessageCallback) (*subscription, error) {
	// Validate channel name
	if channel == "" {
		return nil, fmt.Errorf("%w: channel name is empty", ErrInvalidChannel)
	}

	// Calculate hashslot
	hashslot := Hashslot(channel)

	// Get or create PubSub for this hashslot
	pubsub, err := sm.pool.getPubSubForHashslot(ctx, hashslot)
	if err != nil {
		return nil, fmt.Errorf("failed to get PubSub for hashslot %d: %w", hashslot, err)
	}

	// Get metadata for this PubSub
	meta := sm.pool.getMetadata(pubsub)
	if meta == nil {
		return nil, fmt.Errorf("no metadata found for PubSub")
	}

	// Check if channel is already subscribed on this PubSub (per-PubSub check)
	existingSubs := meta.getSubscriptions(channel)
	isFirstSubscriptionOnPubSub := len(existingSubs) == 0

	// Create subscription
	sub := &subscription{
		channel:   channel,
		subType:   subType,
		pubsub:    pubsub,
		callback:  callback,
		state:     subStatePending,
		confirmCh: make(chan error, 1),
		hashslot:  hashslot,
	}

	// Register subscription in global map
	sm.mu.Lock()
	sm.subscriptions[channel] = append(sm.subscriptions[channel], sub)
	sm.mu.Unlock()

	// Register subscription in PubSub metadata
	meta.addSubscription(sub)

	// Only send SUBSCRIBE command if this is the first subscription to this channel on this PubSub
	// (Redis only needs one SUBSCRIBE per channel per connection)
	if isFirstSubscriptionOnPubSub {
		// Add subscription to pending list BEFORE sending command to prevent race condition
		// where confirmation arrives before we add it to pending
		meta.addPendingSubscription(sub)

		// Create and send subscription command
		cmd := &command{
			cmd:      cmdName,
			args:     []any{channel},
			sub:      sub,
			response: make(chan error, 1),
		}

		// Send command
		if err := meta.sendCommand(ctx, cmd); err != nil {
			// Record failed subscription attempt
			sm.config.recorder.recordSubscriptionAttempt(subscriptionTypeToString(subType), false)

			// Remove from pending list on error
			meta.removePendingSubscription(channel)
			// Cleanup on error
			sm.mu.Lock()
			// Remove the subscription we just added
			subs := sm.subscriptions[channel]
			for i, s := range subs {
				if s == sub {
					sm.subscriptions[channel] = append(subs[:i], subs[i+1:]...)
					break
				}
			}
			if len(sm.subscriptions[channel]) == 0 {
				delete(sm.subscriptions, channel)
			}
			sm.mu.Unlock()
			meta.removeSubscription(sub)
			return nil, fmt.Errorf("failed to send subscription command: %w", err)
		}

		// Wait for confirmation (subscription is already in pending list)
		if err := sub.waitForConfirmation(ctx); err != nil {
			// Record failed subscription attempt
			sm.config.recorder.recordSubscriptionAttempt(subscriptionTypeToString(subType), false)

			// Cleanup on error
			sm.mu.Lock()
			subs := sm.subscriptions[channel]
			for i, s := range subs {
				if s == sub {
					sm.subscriptions[channel] = append(subs[:i], subs[i+1:]...)
					break
				}
			}
			if len(sm.subscriptions[channel]) == 0 {
				delete(sm.subscriptions, channel)
			}
			sm.mu.Unlock()
			meta.removeSubscription(sub)
			return nil, fmt.Errorf("subscription confirmation failed: %w", err)
		}

		// Check if subscription is in failed state
		if sub.getState() == subStateFailed {
			// Record failed subscription attempt
			sm.config.recorder.recordSubscriptionAttempt(subscriptionTypeToString(subType), false)

			sm.mu.Lock()
			subs := sm.subscriptions[channel]
			for i, s := range subs {
				if s == sub {
					sm.subscriptions[channel] = append(subs[:i], subs[i+1:]...)
					break
				}
			}
			if len(sm.subscriptions[channel]) == 0 {
				delete(sm.subscriptions, channel)
			}
			sm.mu.Unlock()
			meta.removeSubscription(sub)
			return nil, fmt.Errorf("%w", ErrSubscriptionFailed)
		}
	} else {
		// Not the first subscription - mark as active immediately
		// (the connection is already subscribed to this channel)
		sub.setState(subStateActive, nil)
	}

	// Record successful subscription attempt
	sm.config.recorder.recordSubscriptionAttempt(subscriptionTypeToString(subType), true)

	return sub, nil
}

// Unsubscribe unsubscribes the callback associated with this Subscription from all channels/patterns
// it was subscribed to. It is safe to call multiple times.
func (s *Sub) Unsubscribe(ctx context.Context) error {
	if s == nil || s.subMux == nil {
		return nil
	}
	return s.subMux.unsubscribeSubscription(s)
}

// unsubscribeSubscription unsubscribes all internal subscriptions in a Subscription.
func (sm *SubMux) unsubscribeSubscription(sub *Sub) error {
	if sub == nil || len(sub.subs) == 0 {
		return nil
	}

	sm.mu.Lock()
	if sm.closed {
		sm.mu.Unlock()
		return fmt.Errorf("%w", ErrClosed)
	}
	sm.mu.Unlock()

	// Group subscriptions by channel to determine which ones need UNSUBSCRIBE commands
	channelsToUnsub := make(map[string]bool)
	subsByChannel := make(map[string][]*subscription)

	for _, internalSub := range sub.subs {
		channel := internalSub.channel
		subsByChannel[channel] = append(subsByChannel[channel], internalSub)

		// Check if this is the last subscription for this channel
		sm.mu.RLock()
		allSubsRef := sm.subscriptions[channel]
		// Make a copy to avoid races when other goroutines modify the slice
		allSubs := make([]*subscription, len(allSubsRef))
		copy(allSubs, allSubsRef)
		sm.mu.RUnlock()

		// Count how many subscriptions remain after removing ours
		remainingCount := 0
		for _, s := range allSubs {
			isOurs := false
			for _, ourSub := range sub.subs {
				if s == ourSub {
					isOurs = true
					break
				}
			}
			if !isOurs {
				remainingCount++
			}
		}

		// If this is the last subscription for this channel, we need to send UNSUBSCRIBE
		if remainingCount == 0 {
			channelsToUnsub[channel] = true
		}
	}

	// Remove all subscriptions from global map
	sm.mu.Lock()
	for channel, internalSubs := range subsByChannel {
		allSubs := sm.subscriptions[channel]
		for _, internalSub := range internalSubs {
			for i, s := range allSubs {
				if s == internalSub {
					sm.subscriptions[channel] = append(allSubs[:i], allSubs[i+1:]...)
					allSubs = sm.subscriptions[channel]
					break
				}
			}
		}
		if len(sm.subscriptions[channel]) == 0 {
			delete(sm.subscriptions, channel)
		}
	}
	sm.mu.Unlock()

	// Send UNSUBSCRIBE commands and remove from PubSub metadata
	for channel, internalSubs := range subsByChannel {
		if len(internalSubs) == 0 {
			continue
		}

		// Use the first subscription to determine command type and PubSub
		firstSub := internalSubs[0]
		meta := sm.pool.getMetadata(firstSub.pubsub)
		if meta == nil {
			continue
		}

		// Determine unsubscribe command based on subscription type
		var cmdName string
		switch firstSub.subType {
		case subTypeSubscribe:
			cmdName = cmdUnsubscribe
		case subTypePSubscribe:
			cmdName = cmdPUnsubscribe
		case subTypeSSubscribe:
			cmdName = cmdSUnsubscribe
		}

		// Mark all subscriptions as closed
		for _, internalSub := range internalSubs {
			internalSub.setState(subStateClosed, nil)
		}

		// Only send UNSUBSCRIBE command if this is the last subscription for this channel
		if channelsToUnsub[channel] {
			cmd := &command{
				cmd:      cmdName,
				args:     []any{channel},
				sub:      firstSub,
				response: make(chan error, 1),
			}

			// Send command (ignore errors - we'll still remove from metadata)
			_ = meta.sendCommand(context.Background(), cmd)
		}

		// Remove all subscriptions from PubSub metadata
		for _, internalSub := range internalSubs {
			meta.removeSubscription(internalSub)
		}
	}

	return nil
}

// Close closes all subscriptions and connections, stops all goroutines, and releases resources.
// It is safe to call multiple times.
func (sm *SubMux) Close() error {
	var firstErr error

	sm.closeOnce.Do(func() {
		sm.mu.Lock()
		sm.closed = true
		subs := make([]*subscription, 0)
		for _, subList := range sm.subscriptions {
			subs = append(subs, subList...)
		}
		sm.mu.Unlock()

		// Stop topology monitor
		if sm.topologyMonitor != nil {
			sm.topologyMonitor.stop()
		}

		// Close all subscriptions
		for _, sub := range subs {
			sub.setState(subStateClosed, nil)
		}

		// Close all connections
		if err := sm.pool.closeAll(); err != nil && firstErr == nil {
			firstErr = err
		}

		// Clear subscriptions
		sm.mu.Lock()
		sm.subscriptions = make(map[string][]*subscription)
		sm.mu.Unlock()
	})

	return firstErr
}
