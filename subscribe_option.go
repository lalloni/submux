package submux

// subscribeConfig holds per-subscription configuration applied via SubscribeOption.
type subscribeConfig struct {
	queueLimit    int  // effective limit (0 = unlimited)
	queueLimitSet bool // true if WithQueueLimit was called
}

// SubscribeOption configures per-subscription behavior.
// Options are passed to SubscribeSync, PSubscribeSync, or SSubscribeSync.
type SubscribeOption func(*subscribeConfig)

// WithQueueLimit overrides the default subscription queue limit for this subscription.
// When the queue reaches the limit, new messages are dropped (tail-drop) and an
// EventQueueOverflow signal is delivered to the callback.
// Set to 0 for unlimited (no dropping). If not called, the SubMux-level default
// from WithSubscriptionQueueLimit is used.
func WithQueueLimit(limit int) SubscribeOption {
	return func(c *subscribeConfig) {
		if limit < 0 {
			limit = 0
		}
		c.queueLimit = limit
		c.queueLimitSet = true
	}
}
