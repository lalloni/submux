package submux

import (
	"context"
	"time"
)

// Redis subscription message kinds.
// These are the values returned by redis.Subscription.Kind.
const (
	redisKindSubscribe    = "subscribe"
	redisKindPSubscribe   = "psubscribe"
	redisKindSSubscribe   = "ssubscribe"
	redisKindUnsubscribe  = "unsubscribe"
	redisKindPUnsubscribe = "punsubscribe"
	redisKindSUnsubscribe = "sunsubscribe"
)

// MessageType represents the type of a message received from a subscription.
type MessageType int

const (
	// MessageTypeMessage is a regular SUBSCRIBE message.
	MessageTypeMessage MessageType = iota
	// MessageTypePMessage is a pattern PSUBSCRIBE message.
	MessageTypePMessage
	// MessageTypeSMessage is a sharded SSUBSCRIBE message.
	MessageTypeSMessage
	// MessageTypeSignal is a signal notification (topology change).
	MessageTypeSignal
)

// Message represents a message received from a subscription or a signal notification.
type Message struct {
	// Type indicates the message type.
	Type MessageType

	// Channel is the channel name (for regular messages).
	Channel string

	// Payload is the message payload.
	Payload string

	// Pattern is the pattern that matched (for PSUBSCRIBE/SSUBSCRIBE messages).
	Pattern string

	// Signal contains signal information (if Type is MessageTypeSignal).
	Signal *SignalInfo

	// Timestamp is when the message was received.
	Timestamp time.Time

	// SubscriptionType indicates the subscription method (subscribe, psubscribe, ssubscribe).
	// Used internally for metrics attribution.
	SubscriptionType subscriptionType
}

// SignalInfo contains information about cluster topology changes or hashslot migrations.
type SignalInfo struct {
	// EventType is the type of event: "migration", "topology_change", "node_failure".
	EventType EventType

	// Hashslot is the affected hashslot (0-16383).
	Hashslot int

	// OldNode is the previous node address (if applicable).
	OldNode string

	// NewNode is the new node address (if applicable).
	NewNode string

	// Details contains additional details about the event.
	Details string
}

// EventType represents the type of a signal event.
type EventType string

// Event types for SignalInfo.
const (
	EventNodeFailure      EventType = "node_failure"
	EventMigration        EventType = "migration"
	EventMigrationTimeout EventType = "migration_timeout"
	EventMigrationStalled EventType = "migration_stalled"
)

// NodePreference determines the strategy for distributing subscriptions across cluster nodes.
type NodePreference int

const (
	// PreferMasters routes all subscriptions to master nodes only.
	// Use this when: You want to minimize the number of nodes involved in pub/sub,
	// or when masters have spare capacity and you want centralized routing.
	PreferMasters NodePreference = iota

	// BalancedAll distributes subscriptions equally across all nodes (masters and replicas) within each shard.
	// This is the recommended default for most workloads as it:
	// - Maximizes resource utilization across all infrastructure
	// - Provides better failure characteristics (smaller blast radius)
	// - Works well for both light and heavy pub/sub loads
	BalancedAll

	// PreferReplicas routes subscriptions to replica nodes, avoiding masters when possible.
	// Use this when: Masters are write-saturated and you need to protect them from additional pub/sub load.
	// Falls back to masters if no replicas are available.
	PreferReplicas
)

// MessageCallback is a function type for handling subscription events (messages and signal notifications).
// Callbacks are invoked asynchronously via a bounded worker pool and must be thread-safe.
// The context is derived from the SubMux lifecycle and is canceled when Close() is called.
type MessageCallback func(ctx context.Context, msg *Message)

// Sub represents a subscription to one or more channels/patterns with a specific callback.
// Sub is returned from SubscribeSync, PSubscribeSync, or SSubscribeSync and can be used
// to unsubscribe the specific callback that was provided during subscription.
type Sub struct {
	subMux *SubMux
	subs   []*subscription // Internal subscriptions created for this Subscription
}
