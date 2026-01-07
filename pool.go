package submux

import (
	"context"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"
)

// pubSubMetadata holds metadata for a PubSub connection.
type pubSubMetadata struct {
	// pubsub is the PubSub connection.
	pubsub *redis.PubSub

	// nodeAddr is the address of the Redis node.
	nodeAddr string

	// subscriptions maps channel/pattern name to list of subscriptions.
	subscriptions map[string][]*subscription

	// pendingSubscriptions tracks subscriptions waiting for confirmation.
	// Maps channel/pattern name to the subscription waiting for confirmation.
	pendingSubscriptions map[string]*subscription

	// state indicates if the connection is active, failed, or closed.
	state connectionState

	// cmdCh is the channel for sending commands to the event loop goroutine.
	cmdCh chan *command

	// done is closed when the connection is closed.
	done chan struct{}

	// wg tracks the event loop goroutine.
	wg sync.WaitGroup

	// mu protects subscriptions, pendingSubscriptions, and state.
	mu sync.RWMutex
}

// addSubscription adds a subscription to this PubSub.
func (m *pubSubMetadata) addSubscription(sub *subscription) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subscriptions[sub.channel] = append(m.subscriptions[sub.channel], sub)
}

// removeSubscription removes a subscription from this PubSub.
func (m *pubSubMetadata) removeSubscription(sub *subscription) {
	m.mu.Lock()
	defer m.mu.Unlock()
	subs := m.subscriptions[sub.channel]
	for i, s := range subs {
		if s == sub {
			m.subscriptions[sub.channel] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	if len(m.subscriptions[sub.channel]) == 0 {
		delete(m.subscriptions, sub.channel)
	}
	// Also remove from pending subscriptions if present
	delete(m.pendingSubscriptions, sub.channel)
}

// addPendingSubscription adds a subscription to the pending list.
func (m *pubSubMetadata) addPendingSubscription(sub *subscription) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingSubscriptions[sub.channel] = sub
}

// getPendingSubscription returns the pending subscription for a channel/pattern, if any.
func (m *pubSubMetadata) getPendingSubscription(channel string) *subscription {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pendingSubscriptions[channel]
}

// removePendingSubscription removes a subscription from the pending list.
func (m *pubSubMetadata) removePendingSubscription(channel string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pendingSubscriptions, channel)
}

// getSubscriptions returns all subscriptions for a channel.
func (m *pubSubMetadata) getSubscriptions(channel string) []*subscription {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.subscriptions[channel]
}

// getAllSubscriptions returns all subscriptions on this PubSub.
func (m *pubSubMetadata) getAllSubscriptions() []*subscription {
	m.mu.RLock()
	defer m.mu.RUnlock()
	subs := make([]*subscription, 0)
	for _, subList := range m.subscriptions {
		subs = append(subs, subList...)
	}
	return subs
}

// subscriptionCount returns the number of active subscriptions.
func (m *pubSubMetadata) subscriptionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	count := 0
	for _, subs := range m.subscriptions {
		count += len(subs)
	}
	return count
}

// setState sets the PubSub state.
func (m *pubSubMetadata) setState(newState connectionState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = newState
}

// getState returns the PubSub state.
func (m *pubSubMetadata) getState() connectionState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

// close closes the PubSub and stops all goroutines.
func (m *pubSubMetadata) close() error {
	m.mu.Lock()
	if m.state == connStateClosed {
		m.mu.Unlock()
		return nil
	}
	m.state = connStateClosed
	close(m.done)
	close(m.cmdCh)
	m.mu.Unlock()

	// Wait for goroutines to finish
	m.wg.Wait()

	return nil
}

// sendCommand sends a command to the command sender goroutine.
func (m *pubSubMetadata) sendCommand(ctx context.Context, cmd *command) error {
	select {
	case m.cmdCh <- cmd:
		return nil
	case <-m.done:
		return fmt.Errorf("pubsub closed")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pubSubPool manages PubSub connections to Redis cluster nodes.
type pubSubPool struct {
	// clusterClient is the underlying Redis cluster client.
	clusterClient *redis.ClusterClient

	// config holds the configuration.
	config *config

	// nodePubSubs maps node address to list of PubSub connections.
	nodePubSubs map[string][]*redis.PubSub

	// hashslotPubSubs maps hashslot to list of PubSub connections that can handle it.
	hashslotPubSubs map[int][]*redis.PubSub

	// pubSubMetadata maps PubSub to its metadata.
	pubSubMetadata map[*redis.PubSub]*pubSubMetadata

	// mu protects all maps.
	mu sync.RWMutex
}

// newPubSubPool creates a new PubSub pool.
func newPubSubPool(clusterClient *redis.ClusterClient, config *config) *pubSubPool {
	return &pubSubPool{
		clusterClient:   clusterClient,
		config:          config,
		nodePubSubs:     make(map[string][]*redis.PubSub),
		hashslotPubSubs: make(map[int][]*redis.PubSub),
		pubSubMetadata:  make(map[*redis.PubSub]*pubSubMetadata),
	}
}

// getPubSubForHashslot returns a PubSub connection for the given hashslot.
// It selects the least-loaded PubSub from available connections.
func (p *pubSubPool) getPubSubForHashslot(ctx context.Context, hashslot int) (*redis.PubSub, error) {
	p.mu.RLock()
	pubsubs := p.hashslotPubSubs[hashslot]
	p.mu.RUnlock()

	if len(pubsubs) > 0 {
		// Find least-loaded PubSub
		var bestPubSub *redis.PubSub
		minSubs := int(^uint(0) >> 1) // max int

		for _, pubsub := range pubsubs {
			meta := p.pubSubMetadata[pubsub]
			if meta == nil || meta.getState() != connStateActive {
				continue
			}
			count := meta.subscriptionCount()
			if count < minSubs {
				minSubs = count
				bestPubSub = pubsub
			}
		}

		if bestPubSub != nil {
			return bestPubSub, nil
		}
	}

	// No PubSub available, need to create one
	return p.createPubSubForHashslot(ctx, hashslot)
}

// createPubSubForHashslot creates a new PubSub connection for the given hashslot.
func (p *pubSubPool) createPubSubForHashslot(ctx context.Context, hashslot int) (*redis.PubSub, error) {
	// Get node information for this hashslot from cluster topology
	nodeAddr, err := p.getNodeForHashslot(ctx, hashslot)
	if err != nil {
		return nil, fmt.Errorf("failed to get node for hashslot %d: %w", hashslot, err)
	}

	// Check if we already have a PubSub connection to this node
	p.mu.Lock()
	if pubsubs, ok := p.nodePubSubs[nodeAddr]; ok && len(pubsubs) > 0 {
		// Find least-loaded PubSub
		var bestPubSub *redis.PubSub
		minSubs := int(^uint(0) >> 1) // max int

		for _, pubsub := range pubsubs {
			meta := p.pubSubMetadata[pubsub]
			if meta == nil || meta.getState() != connStateActive {
				continue
			}
			count := meta.subscriptionCount()
			if count < minSubs {
				minSubs = count
				bestPubSub = pubsub
			}
		}

		if bestPubSub != nil {
			// Reuse existing connection
			// Add to hashslotPubSubs if not already present
			// (Use a separate check or just append? hashslotPubSubs[hashslot] is likely empty since we are here)
			p.hashslotPubSubs[hashslot] = append(p.hashslotPubSubs[hashslot], bestPubSub)
			p.mu.Unlock()
			return bestPubSub, nil
		}
	}
	p.mu.Unlock()

	// Create PubSub connection to the node
	pubsub, err := p.createPubSubToNode(ctx, nodeAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to create PubSub to node %s: %w", nodeAddr, err)
	}

	// Register PubSub
	p.mu.Lock()
	p.nodePubSubs[nodeAddr] = append(p.nodePubSubs[nodeAddr], pubsub)
	p.hashslotPubSubs[hashslot] = append(p.hashslotPubSubs[hashslot], pubsub)
	p.mu.Unlock()

	return pubsub, nil
}

// createPubSubToNode creates a new PubSub connection to a specific node.
// It uses the ClusterClient's Subscribe method to create a PubSub connection.
func (p *pubSubPool) createPubSubToNode(ctx context.Context, nodeAddr string) (*redis.PubSub, error) {
	// Create a PubSub connection using ClusterClient's Subscribe method
	// We start with an empty subscription - channels will be added via command sender
	pubsub := p.clusterClient.Subscribe(ctx)

	// Create metadata for this PubSub
	meta := &pubSubMetadata{
		pubsub:               pubsub,
		nodeAddr:             nodeAddr,
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 100),
		done:                 make(chan struct{}),
	}

	// Register metadata
	p.mu.Lock()
	p.pubSubMetadata[pubsub] = meta
	p.mu.Unlock()

	// Start single event loop goroutine
	meta.wg.Add(1)
	go runEventLoop(meta)

	return pubsub, nil
}

// getMetadata returns the metadata for a PubSub.
func (p *pubSubPool) getMetadata(pubsub *redis.PubSub) *pubSubMetadata {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pubSubMetadata[pubsub]
}

// getNodeForHashslot returns the node address that owns the given hashslot.
// It first tries to get it from the topology monitor if available, otherwise falls back to querying ClusterClient.
func (p *pubSubPool) getNodeForHashslot(ctx context.Context, hashslot int) (string, error) {
	// Try to get from topology monitor first (if available)
	// Note: This requires access to SubMux, which we don't have here.
	// For now, fall back to querying ClusterClient directly.
	// In the future, we could pass topologyMonitor reference to pool.

	// Use ClusterClient to get cluster slots
	slots, err := p.clusterClient.ClusterSlots(ctx).Result()
	if err != nil {
		return "", fmt.Errorf("failed to get cluster slots: %w", err)
	}

	// Find the slot range that contains our hashslot
	for _, slot := range slots {
		if hashslot >= int(slot.Start) && hashslot <= int(slot.End) {
			// Return the master node address
			if len(slot.Nodes) > 0 {
				node := slot.Nodes[0]
				// ClusterNode has Addr field, need to check the actual structure
				return node.Addr, nil
			}
		}
	}

	return "", fmt.Errorf("hashslot %d not found in cluster", hashslot)
}

// removePubSub removes a PubSub from the pool.
func (p *pubSubPool) removePubSub(pubsub *redis.PubSub) {
	p.mu.Lock()
	defer p.mu.Unlock()

	meta := p.pubSubMetadata[pubsub]
	if meta == nil {
		return
	}

	// Remove from node PubSubs
	if pubsubs, ok := p.nodePubSubs[meta.nodeAddr]; ok {
		for i, ps := range pubsubs {
			if ps == pubsub {
				p.nodePubSubs[meta.nodeAddr] = append(pubsubs[:i], pubsubs[i+1:]...)
				break
			}
		}
		if len(p.nodePubSubs[meta.nodeAddr]) == 0 {
			delete(p.nodePubSubs, meta.nodeAddr)
		}
	}

	// Remove from hashslot PubSubs
	for hashslot, pubsubs := range p.hashslotPubSubs {
		for i, ps := range pubsubs {
			if ps == pubsub {
				p.hashslotPubSubs[hashslot] = append(pubsubs[:i], pubsubs[i+1:]...)
				break
			}
		}
		if len(p.hashslotPubSubs[hashslot]) == 0 {
			delete(p.hashslotPubSubs, hashslot)
		}
	}

	// Remove metadata
	delete(p.pubSubMetadata, pubsub)
}

// closeAll closes all PubSub connections in the pool.
func (p *pubSubPool) closeAll() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var firstErr error
	for _, pubsubs := range p.nodePubSubs {
		for _, pubsub := range pubsubs {
			meta := p.pubSubMetadata[pubsub]
			if meta != nil {
				// Close metadata (stops goroutines)
				if err := meta.close(); err != nil && firstErr == nil {
					firstErr = err
				}
			}
			// Close PubSub
			if err := pubsub.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}

	p.nodePubSubs = make(map[string][]*redis.PubSub)
	p.hashslotPubSubs = make(map[int][]*redis.PubSub)
	p.pubSubMetadata = make(map[*redis.PubSub]*pubSubMetadata)

	return firstErr
}
