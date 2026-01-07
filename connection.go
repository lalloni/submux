package submux

import (
	"context"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"
)

// connection represents a Pub/Sub connection to a Redis node.
type connection struct {
	// nodeAddr is the address of the Redis node.
	nodeAddr string

	// clusterClient is the underlying Redis cluster client.
	clusterClient *redis.ClusterClient

	// pubsub is the PubSub connection for this node/hashslot.
	pubsub *redis.PubSub

	// msgCh is the channel for receiving messages from PubSub.
	msgCh <-chan *redis.Message

	// subscriptions maps channel/pattern name to list of subscriptions (allowing multiple subscriptions per channel).
	subscriptions map[string][]*subscription

	// mu protects subscriptions and state.
	mu sync.RWMutex

	// state indicates if the connection is active, failed, or closed.
	state connectionState

	// cmdCh is the channel for sending commands to the command sender goroutine.
	cmdCh chan *command

	// done is closed when the connection is closed.
	done chan struct{}

	// wg tracks the command sender and response handler goroutines.
	wg sync.WaitGroup
}

// connectionState represents the state of a connection.
type connectionState int

const (
	connStateActive connectionState = iota
	connStateFailed
	connStateClosed
)

// command represents a command to be sent to Redis.
type command struct {
	cmd      string
	args     []any
	sub      *subscription
	response chan error
}

// addSubscription adds a subscription to this connection.
func (c *connection) addSubscription(sub *subscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subscriptions[sub.channel] = append(c.subscriptions[sub.channel], sub)
}

// removeSubscription removes a subscription from this connection.
func (c *connection) removeSubscription(sub *subscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	subs := c.subscriptions[sub.channel]
	for i, s := range subs {
		if s == sub {
			c.subscriptions[sub.channel] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	if len(c.subscriptions[sub.channel]) == 0 {
		delete(c.subscriptions, sub.channel)
	}
}

// getSubscriptions returns all subscriptions for a channel.
func (c *connection) getSubscriptions(channel string) []*subscription {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.subscriptions[channel]
}

// isSubscribed returns whether the connection is subscribed to a channel.
func (c *connection) isSubscribed(channel string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	subs, ok := c.subscriptions[channel]
	return ok && len(subs) > 0
}

// getAllSubscriptions returns all subscriptions on this connection.
func (c *connection) getAllSubscriptions() []*subscription {
	c.mu.RLock()
	defer c.mu.RUnlock()
	subs := make([]*subscription, 0)
	for _, subList := range c.subscriptions {
		subs = append(subs, subList...)
	}
	return subs
}

// subscriptionCount returns the number of active subscriptions.
func (c *connection) subscriptionCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	count := 0
	for _, subs := range c.subscriptions {
		count += len(subs)
	}
	return count
}

// setState sets the connection state.
func (c *connection) setState(newState connectionState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = newState
}

// getState returns the connection state.
func (c *connection) getState() connectionState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// close closes the connection and stops all goroutines.
func (c *connection) close() error {
	c.mu.Lock()
	if c.state == connStateClosed {
		c.mu.Unlock()
		return nil
	}
	c.state = connStateClosed
	close(c.done)
	close(c.cmdCh)
	c.mu.Unlock()

	// Wait for goroutines to finish
	c.wg.Wait()

	// Close the underlying PubSub connection
	if c.pubsub != nil {
		return c.pubsub.Close()
	}
	return nil
}

// sendCommand sends a command to the command sender goroutine.
func (c *connection) sendCommand(ctx context.Context, cmd *command) error {
	select {
	case c.cmdCh <- cmd:
		return nil
	case <-c.done:
		return fmt.Errorf("connection closed")
	case <-ctx.Done():
		return ctx.Err()
	}
}
