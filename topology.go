package submux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
)

// topologyState represents the current cluster topology state.
type topologyState struct {
	// hashslotToNode maps hashslot to the node address that owns it.
	hashslotToNode map[int]string

	// nodeToHashslots maps node address to the list of hashslots it owns.
	nodeToHashslots map[string][]int

	// mu protects the topology state.
	mu sync.RWMutex
}

// newTopologyState creates a new topology state.
func newTopologyState() *topologyState {
	return &topologyState{
		hashslotToNode:  make(map[int]string),
		nodeToHashslots: make(map[string][]int),
	}
}

// update updates the topology state from cluster slots.
func (ts *topologyState) update(slots []redis.ClusterSlot) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// Clear existing mappings
	ts.hashslotToNode = make(map[int]string)
	ts.nodeToHashslots = make(map[string][]int)

	// Build new mappings
	for _, slot := range slots {
		if len(slot.Nodes) == 0 {
			continue
		}
		masterAddr := slot.Nodes[0].Addr

		// Map all hashslots in this range to the master node
		for hashslot := int(slot.Start); hashslot <= int(slot.End); hashslot++ {
			ts.hashslotToNode[hashslot] = masterAddr
			ts.nodeToHashslots[masterAddr] = append(ts.nodeToHashslots[masterAddr], hashslot)
		}
	}
}

// getNodeForHashslot returns the node address that owns the given hashslot.
func (ts *topologyState) getNodeForHashslot(hashslot int) (string, bool) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	node, ok := ts.hashslotToNode[hashslot]
	return node, ok
}

// getNodeForHashslot returns the node address that owns the given hashslot from the topology monitor.
func (tm *topologyMonitor) getNodeForHashslot(hashslot int) (string, bool) {
	tm.currentState.mu.RLock()
	defer tm.currentState.mu.RUnlock()
	return tm.currentState.getNodeForHashslot(hashslot)
}

// compareAndDetectChanges compares the current topology with a previous state and returns detected migrations.
func (ts *topologyState) compareAndDetectChanges(previous *topologyState) []hashslotMigration {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	previous.mu.RLock()
	defer previous.mu.RUnlock()

	var migrations []hashslotMigration

	// Check for hashslot migrations
	for hashslot, currentNode := range ts.hashslotToNode {
		previousNode, existed := previous.hashslotToNode[hashslot]
		if existed && previousNode != currentNode {
			// Hashslot has migrated
			migrations = append(migrations, hashslotMigration{
				hashslot: hashslot,
				oldNode:  previousNode,
				newNode:  currentNode,
			})
		}
	}

	return migrations
}

// hashslotMigration represents a detected hashslot migration.
type hashslotMigration struct {
	hashslot int
	oldNode  string
	newNode  string
}

// topologyMonitor monitors cluster topology changes and detects hashslot migrations.
type topologyMonitor struct {
	clusterClient *redis.ClusterClient
	config        *config
	subMux        *SubMux

	// currentState holds the current topology state.
	currentState *topologyState

	// done is closed when monitoring should stop.
	done chan struct{}

	// wg tracks the monitoring goroutine.
	wg sync.WaitGroup

	// pollInterval is how often to poll the topology.
	pollInterval time.Duration
}

// newTopologyMonitor creates a new topology monitor.
func newTopologyMonitor(clusterClient *redis.ClusterClient, config *config, subMux *SubMux) *topologyMonitor {
	return &topologyMonitor{
		clusterClient: clusterClient,
		config:        config,
		subMux:        subMux,
		currentState:  newTopologyState(),
		done:          make(chan struct{}),
		pollInterval:  config.topologyPollInterval,
	}
}

// start starts the topology monitoring goroutine.
func (tm *topologyMonitor) start() {
	tm.wg.Add(1)
	go tm.monitor()
}

// stop stops the topology monitoring.
func (tm *topologyMonitor) stop() {
	close(tm.done)
	tm.wg.Wait()
}

// monitor is the main monitoring loop.
func (tm *topologyMonitor) monitor() {
	defer tm.wg.Done()

	// Initial topology fetch (non-blocking, errors are expected if cluster isn't ready)
	_ = tm.refreshTopology()

	ticker := time.NewTicker(tm.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := tm.refreshTopology(); err != nil {
				// Log error but continue monitoring (cluster might be temporarily unavailable)
				// Only log if it's not a connection error (to reduce noise)
				if !isConnectionError(err) {
					log.Printf("submux: topology refresh failed: %v", err)
				}
			}
		case <-tm.done:
			return
		}
	}
}

// isConnectionError checks if an error is a connection-related error.
// isConnectionError checks if an error is a connection-related error.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// Check for specific net errors
		if opErr.Op == "dial" {
			return true
		}
		if errors.Is(opErr, syscall.ECONNREFUSED) {
			return true
		}
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}

	return false
}

// refreshTopology fetches the current topology and detects changes.
// It first calls ReloadState() to update the ClusterClient's internal state,
// then fetches ClusterSlots() to get the slot information for comparison.
func (tm *topologyMonitor) refreshTopology() error {
	ctx := context.Background()

	// First, reload the ClusterClient's internal state to ensure it's up to date
	// This is important for proper command routing. ReloadState() doesn't return
	// an error, but it may fail internally - we continue anyway and rely on
	// ClusterSlots() to provide the topology information.
	tm.clusterClient.ReloadState(ctx)

	// Get cluster slots information for topology comparison
	slots, err := tm.clusterClient.ClusterSlots(ctx).Result()
	if err != nil {
		return fmt.Errorf("failed to get cluster slots: %w", err)
	}

	// Create new state from current topology
	newState := newTopologyState()
	newState.update(slots)

	// Compare with previous state
	previousState := tm.currentState
	migrations := newState.compareAndDetectChanges(previousState)

	// Update current state
	tm.currentState = newState

	// Handle detected migrations
	if len(migrations) > 0 {
		tm.handleMigrations(migrations)
	}

	return nil
}

// handleMigrations handles detected hashslot migrations.
func (tm *topologyMonitor) handleMigrations(migrations []hashslotMigration) {
	for _, migration := range migrations {
		tm.handleMigration(migration)
	}
}

// handleMigration handles a single hashslot migration.
func (tm *topologyMonitor) handleMigration(migration hashslotMigration) {
	// Invalidate pool cache for this hashslot so future subscriptions use the new node
	tm.subMux.pool.mu.Lock()
	delete(tm.subMux.pool.hashslotPubSubs, migration.hashslot)
	tm.subMux.pool.mu.Unlock()

	// Find all subscriptions affected by this migration
	affectedSubs := tm.findAffectedSubscriptions(migration.hashslot)

	if len(affectedSubs) == 0 {
		// No subscriptions affected, nothing to do
		return
	}

	// Send signal message to all affected subscriptions
	tm.sendSignalMessages(affectedSubs, migration)

	// If auto-resubscribe is enabled, recreate subscriptions on new node
	// Run in goroutine with progress monitoring to detect timeouts/stalls
	if tm.config.autoResubscribe {
		go tm.resubscribeOnNewNodeWithMonitoring(affectedSubs, migration)
	}
}

// findAffectedSubscriptions finds all subscriptions affected by a hashslot migration.
func (tm *topologyMonitor) findAffectedSubscriptions(hashslot int) []*subscription {
	tm.subMux.mu.RLock()
	defer tm.subMux.mu.RUnlock()

	var affected []*subscription

	// Iterate through all subscriptions and find those matching this hashslot
	for _, subs := range tm.subMux.subscriptions {
		for _, sub := range subs {
			if sub.hashslot == hashslot {
				affected = append(affected, sub)
			}
		}
	}

	return affected
}

// sendSignalMessages sends signal messages to affected subscriptions.
func (tm *topologyMonitor) sendSignalMessages(subs []*subscription, migration hashslotMigration) {
	signal := &SignalInfo{
		EventType: EventMigration,
		Hashslot:  migration.hashslot,
		OldNode:   migration.oldNode,
		NewNode:   migration.newNode,
		Details:   fmt.Sprintf("Hashslot %d migrated from %s to %s", migration.hashslot, migration.oldNode, migration.newNode),
	}

	msg := &Message{
		Type:      MessageTypeSignal,
		Signal:    signal,
		Timestamp: time.Now(),
	}

	// Send to all affected subscriptions
	for _, sub := range subs {
		invokeCallback(sub.callback, msg)
	}
}

// sendMigrationTimeoutSignal sends a signal message when migration resubscription exceeds the maximum duration.
func (tm *topologyMonitor) sendMigrationTimeoutSignal(subs []*subscription, migration hashslotMigration, duration time.Duration) {
	signal := &SignalInfo{
		EventType: EventMigrationTimeout,
		Hashslot:  migration.hashslot,
		OldNode:   migration.oldNode,
		NewNode:   migration.newNode,
		Details:   fmt.Sprintf("Hashslot %d migration resubscription exceeded maximum duration of %v. Subscribers may need to manually resubscribe.", migration.hashslot, duration),
	}

	msg := &Message{
		Type:      MessageTypeSignal,
		Signal:    signal,
		Timestamp: time.Now(),
	}

	// Send to all affected subscriptions
	for _, sub := range subs {
		invokeCallback(sub.callback, msg)
	}

	log.Printf("submux: migration timeout for hashslot %d after %v", migration.hashslot, duration)
}

// sendMigrationStalledSignal sends a signal message when migration resubscription appears to have stalled.
func (tm *topologyMonitor) sendMigrationStalledSignal(subs []*subscription, migration hashslotMigration, stallDuration time.Duration) {
	signal := &SignalInfo{
		EventType: EventMigrationStalled,
		Hashslot:  migration.hashslot,
		OldNode:   migration.oldNode,
		NewNode:   migration.newNode,
		Details:   fmt.Sprintf("Hashslot %d migration resubscription appears stalled (no progress for %v). Subscribers may need to manually resubscribe.", migration.hashslot, stallDuration),
	}

	msg := &Message{
		Type:      MessageTypeSignal,
		Signal:    signal,
		Timestamp: time.Now(),
	}

	// Send to all affected subscriptions
	for _, sub := range subs {
		invokeCallback(sub.callback, msg)
	}

	log.Printf("submux: migration stalled for hashslot %d (no progress for %v)", migration.hashslot, stallDuration)
}

// resubscribeOnNewNodeWithMonitoring recreates subscriptions on the new node after migration
// with progress monitoring to detect timeouts and stalls.
func (tm *topologyMonitor) resubscribeOnNewNodeWithMonitoring(subs []*subscription, migration hashslotMigration) {
	const (
		maxMigrationDuration = 30 * time.Second
		stallCheckInterval   = 2 * time.Second
	)

	startTime := time.Now()
	lastProgressTime := startTime
	lastProcessedCount := 0

	// Channel to track progress updates
	progressCh := make(chan int, 1)
	doneCh := make(chan struct{})

	// Progress monitoring goroutine
	go func() {
		ticker := time.NewTicker(stallCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-doneCh:
				return
			case count := <-progressCh:
				lastProcessedCount = count
				lastProgressTime = time.Now()
				// Check if we've completed all subscriptions
				if count >= len(subs) {
					return
				}
			case <-ticker.C:
				// Check for timeout
				if time.Since(startTime) > maxMigrationDuration {
					tm.sendMigrationTimeoutSignal(subs, migration, maxMigrationDuration)
					return
				}

				// Check for stall (no progress in last interval)
				// Only report stall if we haven't completed all subscriptions
				if lastProcessedCount < len(subs) && time.Since(lastProgressTime) > stallCheckInterval {
					// Check if there's new progress we haven't seen yet
					select {
					case count := <-progressCh:
						lastProcessedCount = count
						lastProgressTime = time.Now()
						if count >= len(subs) {
							return
						}
					default:
						// No new progress and we haven't completed - it's a stall
						tm.sendMigrationStalledSignal(subs, migration, time.Since(lastProgressTime))
						return
					}
				}
			}
		}
	}()

	// Perform resubscription with proper cleanup
	func() {
		defer func() {
			// Always send final progress update and signal completion, even on panic
			if progressCh != nil {
				select {
				case progressCh <- len(subs):
				default:
				}
			}
			close(doneCh)
		}()
		tm.resubscribeOnNewNode(subs, migration, progressCh)
	}()
}

// resubscribeOnNewNode recreates subscriptions on the new node after migration.
func (tm *topologyMonitor) resubscribeOnNewNode(subs []*subscription, migration hashslotMigration, progressCh chan<- int) {
	// Group subscriptions by channel (to avoid duplicate SUBSCRIBE commands)
	channelsByType := make(map[subscriptionType]map[string][]*subscription)
	for _, sub := range subs {
		if channelsByType[sub.subType] == nil {
			channelsByType[sub.subType] = make(map[string][]*subscription)
		}
		channelsByType[sub.subType][sub.channel] = append(channelsByType[sub.subType][sub.channel], sub)
	}

	processedCount := 0

	// For each subscription type and channel, recreate subscriptions
	for subType, channels := range channelsByType {
		for channel, channelSubs := range channels {
			// Unsubscribe from old connection (if still active)
			for _, sub := range channelSubs {
				meta := tm.subMux.pool.getMetadata(sub.pubsub)
				if meta != nil && meta.getState() == connStateActive {
					// Mark subscription as closed (will be recreated)
					sub.setState(subStateClosed, nil)
				}
			}

			// Recreate subscriptions on new node
			// Get new PubSub for the hashslot (will use new node)
			ctx := context.Background()
			newPubsub, err := tm.subMux.pool.getPubSubForHashslot(ctx, migration.hashslot)
			if err != nil {
				// Log error and continue with other subscriptions
				log.Printf("submux: failed to get PubSub for migrated hashslot %d: %v", migration.hashslot, err)
				// Report progress even on error
				processedCount += len(channelSubs)
				if progressCh != nil {
					select {
					case progressCh <- processedCount:
					default:
					}
				}
				continue
			}
			for _, sub := range channelSubs {
				// Update subscription's PubSub reference
				sub.pubsub = newPubsub

				// Get metadata for new PubSub
				meta := tm.subMux.pool.getMetadata(newPubsub)
				if meta == nil {
					processedCount++
					if progressCh != nil {
						select {
						case progressCh <- processedCount:
						default:
						}
					}
					continue
				}

				// Re-register subscription in metadata
				meta.addSubscription(sub)

				// Check if channel is already subscribed on new PubSub
				existingSubs := meta.getSubscriptions(channel)
				isFirstOnNewPubSub := len(existingSubs) == 1 // Only our subscription

				if isFirstOnNewPubSub {
					// Need to send SUBSCRIBE command
					var cmdName string
					switch subType {
					case subTypeSubscribe:
						cmdName = cmdSubscribe
					case subTypePSubscribe:
						cmdName = cmdPSubscribe
					case subTypeSSubscribe:
						cmdName = cmdSSubscribe
					}

					// Mark as pending
					sub.setState(subStatePending, nil)
					meta.addPendingSubscription(sub)

					// Send subscription command
					cmd := &command{
						cmd:      cmdName,
						args:     []any{channel},
						sub:      sub,
						response: make(chan error, 1),
					}

					if err := meta.sendCommand(ctx, cmd); err != nil {
						meta.removePendingSubscription(channel)
						sub.setState(subStateFailed, err)
						processedCount++
						if progressCh != nil {
							select {
							case progressCh <- processedCount:
							default:
							}
						}
						continue
					}

					// Wait for confirmation (with timeout)
					confirmCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
					if err := sub.waitForConfirmation(confirmCtx); err != nil {
						cancel()
						meta.removePendingSubscription(channel)
						sub.setState(subStateFailed, err)
						processedCount++
						if progressCh != nil {
							select {
							case progressCh <- processedCount:
							default:
							}
						}
						continue
					}
					cancel()

					// Check if subscription is in failed state
					if sub.getState() == subStateFailed {
						processedCount++
						if progressCh != nil {
							select {
							case progressCh <- processedCount:
							default:
							}
						}
						continue
					}
				} else {
					// Already subscribed on new PubSub, mark as active
					sub.setState(subStateActive, nil)
				}

				// Report progress after processing each subscription
				processedCount++
				if progressCh != nil {
					select {
					case progressCh <- processedCount:
					default:
					}
				}
			}
		}
	}
}
