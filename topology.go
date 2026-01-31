package submux

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// topologyState represents the current cluster topology state.
type topologyState struct {
	// hashslotToNode maps hashslot to the node address that owns it (master node).
	hashslotToNode map[int]string

	// hashslotToNodes maps hashslot to all nodes (master + replicas) that can serve it.
	// Index 0 is always the master, indices 1+ are replicas.
	hashslotToNodes map[int][]string

	// nodeToHashslots maps node address to the list of hashslots it owns.
	nodeToHashslots map[string][]int

	// mu protects the topology state.
	mu sync.RWMutex
}

// newTopologyState creates a new topology state.
func newTopologyState() *topologyState {
	return &topologyState{
		hashslotToNode:  make(map[int]string),
		hashslotToNodes: make(map[int][]string),
		nodeToHashslots: make(map[string][]int),
	}
}

// update updates the topology state from cluster slots.
func (ts *topologyState) update(slots []redis.ClusterSlot) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// Clear existing mappings
	ts.hashslotToNode = make(map[int]string)
	ts.hashslotToNodes = make(map[int][]string)
	ts.nodeToHashslots = make(map[string][]int)

	// Build new mappings
	for _, slot := range slots {
		if len(slot.Nodes) == 0 {
			continue
		}
		masterAddr := slot.Nodes[0].Addr

		// Collect all node addresses (master + replicas)
		allNodeAddrs := make([]string, len(slot.Nodes))
		for i, node := range slot.Nodes {
			allNodeAddrs[i] = node.Addr
		}

		// Map all hashslots in this range to both master and all nodes
		for hashslot := int(slot.Start); hashslot <= int(slot.End); hashslot++ {
			ts.hashslotToNode[hashslot] = masterAddr
			ts.hashslotToNodes[hashslot] = allNodeAddrs
			ts.nodeToHashslots[masterAddr] = append(ts.nodeToHashslots[masterAddr], hashslot)
		}
	}
}

// getNodeForHashslot returns the master node address that owns the given hashslot.
func (ts *topologyState) getNodeForHashslot(hashslot int) (string, bool) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	node, ok := ts.hashslotToNode[hashslot]
	return node, ok
}

// getNodesForHashslot returns all nodes (master + replicas) that can serve the given hashslot.
// Index 0 is always the master, indices 1+ are replicas.
func (ts *topologyState) getNodesForHashslot(hashslot int) ([]string, bool) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	nodes, ok := ts.hashslotToNodes[hashslot]
	if !ok {
		return nil, false
	}
	// Return a copy to avoid concurrent modification
	nodesCopy := make([]string, len(nodes))
	copy(nodesCopy, nodes)
	return nodesCopy, true
}

// selectNodeForHashslot selects a node for the given hashslot based on NodePreference.
func (ts *topologyState) selectNodeForHashslot(hashslot int, preference NodePreference) (string, bool) {
	nodes, ok := ts.getNodesForHashslot(hashslot)
	if !ok || len(nodes) == 0 {
		return "", false
	}

	switch preference {
	case PreferMasters:
		// Always return master (index 0)
		return nodes[0], true

	case PreferReplicas:
		// Prefer replicas, fall back to master if no replicas
		if len(nodes) > 1 {
			// Return first replica
			return nodes[1], true
		}
		// No replicas, use master
		return nodes[0], true

	case BalancedAll:
		// Return all nodes for external load balancing
		// Caller should implement least-loaded selection
		// For now, return master as default (caller will handle selection)
		return nodes[0], true

	default:
		// Default to master
		return nodes[0], true
	}
}

// getNodeForHashslot returns the node address for the given hashslot from the topology monitor,
// respecting the configured node preference strategy.
func (tm *topologyMonitor) getNodeForHashslot(hashslot int) (string, bool) {
	// Copy state reference under lock to avoid nested lock acquisition
	// (tm.mu -> topologyState.mu could deadlock if acquired in reverse elsewhere)
	tm.mu.Lock()
	state := tm.currentState
	preference := tm.config.nodePreference
	tm.mu.Unlock()

	if state == nil {
		return "", false
	}
	return state.selectNodeForHashslot(hashslot, preference)
}

// getNodesForHashslot returns all nodes (master + replicas) for the given hashslot from the topology monitor.
func (tm *topologyMonitor) getNodesForHashslot(hashslot int) ([]string, bool) {
	tm.mu.Lock()
	state := tm.currentState
	tm.mu.Unlock()

	if state == nil {
		return nil, false
	}
	return state.getNodesForHashslot(hashslot)
}

// getAnySlotForNode returns any hashslot owned by the given node.
func (ts *topologyState) getAnySlotForNode(nodeAddr string) (int, bool) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	slots, ok := ts.nodeToHashslots[nodeAddr]
	if !ok || len(slots) == 0 {
		return 0, false
	}
	return slots[0], true
}

// getAnySlotForNode returns any hashslot owned by the given node from the topology monitor.
func (tm *topologyMonitor) getAnySlotForNode(nodeAddr string) (int, bool) {
	// Copy state reference under lock to avoid nested lock acquisition
	tm.mu.Lock()
	state := tm.currentState
	tm.mu.Unlock()

	if state == nil {
		return 0, false
	}
	return state.getAnySlotForNode(nodeAddr)
}

// compareAndDetectChanges compares the current topology with a previous state and returns detected migrations.
func (ts *topologyState) compareAndDetectChanges(previous *topologyState) []hashslotMigration {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	previous.mu.RLock()
	defer previous.mu.RUnlock()

	var migrations []hashslotMigration

	// Check for modified or new slots
	for hashslot, currentNode := range ts.hashslotToNode {
		previousNode, existed := previous.hashslotToNode[hashslot]
		if !existed || previousNode != currentNode {
			// Hashslot has migrated (or appeared)
			migrations = append(migrations, hashslotMigration{
				hashslot: hashslot,
				oldNode:  previousNode, // will be empty string if !existed
				newNode:  currentNode,
			})
		}
	}

	// Check for removed slots
	for hashslot, previousNode := range previous.hashslotToNode {
		_, existed := ts.hashslotToNode[hashslot]
		if !existed {
			// Hashslot has disappeared
			migrations = append(migrations, hashslotMigration{
				hashslot: hashslot,
				oldNode:  previousNode,
				newNode:  "", // empty string indicates missing/unassigned
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

	// mu protects currentState and concurrent access to refreshTopology
	mu sync.Mutex
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

// triggerRefresh triggers an immediate topology refresh in response to a MOVED/ASK error.
// This is called asynchronously to avoid blocking the event loop.
// The redirectAddr parameter is the address Redis told us to redirect to.
func (tm *topologyMonitor) triggerRefresh(redirectAddr string, isMoved bool) {
	// Log the redirect detection
	if isMoved {
		tm.config.logger.Info("submux: MOVED redirect detected, triggering topology refresh", "redirect_addr", redirectAddr)
	} else {
		tm.config.logger.Info("submux: ASK redirect detected, triggering topology refresh", "redirect_addr", redirectAddr)
	}

	// Perform refresh asynchronously to avoid blocking the caller
	go func() {
		if err := tm.refreshTopology(); err != nil {
			tm.config.logger.Error("submux: topology refresh after redirect failed", "error", err)
		}
	}()
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
				// Log all errors for debugging
				tm.config.logger.Error("submux: topology refresh failed", "error", err)
			}
		case <-tm.done:
			return
		}
	}
}

// refreshTopology fetches the current topology and detects changes.
// It first calls ReloadState() to update the ClusterClient's internal state,
// then fetches ClusterSlots() to get the slot information for comparison.
func (tm *topologyMonitor) refreshTopology() error {
	startTime := time.Now()

	tm.mu.Lock()
	defer tm.mu.Unlock()

	ctx := context.Background()

	// First, reload the ClusterClient's internal state to ensure it's up to date
	// This is important for proper command routing. ReloadState() doesn't return
	// an error, but it may fail internally - we continue anyway and rely on
	// ClusterSlots() to provide the topology information.
	tm.clusterClient.ReloadState(ctx)

	// Get cluster slots information for topology comparison
	var slots []redis.ClusterSlot
	var err error

	// Try main client first
	slots, err = tm.clusterClient.ClusterSlots(ctx).Result()
	if err != nil {
		// Log the error
		tm.config.logger.Warn("submux: main client ClusterSlots failed, trying fallback", "error", err)

		// Fallback: try to contact seed nodes directly
		// This handles cases where the main client's view of the cluster is stale or stuck on a dead node
		opts := tm.clusterClient.Options()

		// Shuffle addresses to avoid getting stuck on a bad seed node
		addrs := make([]string, len(opts.Addrs))
		copy(addrs, opts.Addrs)
		rand.Shuffle(len(addrs), func(i, j int) {
			addrs[i], addrs[j] = addrs[j], addrs[i]
		})

		for _, addr := range addrs {
			// Create a temporary client for this node
			// Copy relevant options
			nodeOpts := &redis.Options{
				Addr:                  addr,
				ClientName:            opts.ClientName,
				Dialer:                opts.Dialer,
				OnConnect:             opts.OnConnect,
				Username:              opts.Username,
				Password:              opts.Password,
				ContextTimeoutEnabled: opts.ContextTimeoutEnabled,
				PoolSize:              1, // We only need one connection
				MinIdleConns:          0,
				MaxRetries:            1,
				DialTimeout:           1 * time.Second,
				ReadTimeout:           1 * time.Second,
				WriteTimeout:          1 * time.Second,
				TLSConfig:             opts.TLSConfig,
			}

			nodeClient := redis.NewClient(nodeOpts)

			// Try to get slots from this node
			// We use a short timeout context
			nodeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)

			// Check cluster state first
			info, infoErr := nodeClient.ClusterInfo(nodeCtx).Result()
			if infoErr != nil || !strings.Contains(info, "cluster_state:ok") {
				cancel()
				nodeClient.Close()
				continue
			}

			nodeSlots, nodeErr := nodeClient.ClusterSlots(nodeCtx).Result()
			cancel()
			nodeClient.Close()

			if nodeErr == nil {
				tm.config.logger.Info("submux: successfully recovered topology from seed node", "seed_node", addr)
				slots = nodeSlots
				err = nil
				break
			}
		}
	}

	if err != nil {
		// Record failed topology refresh
		tm.config.recorder.recordTopologyRefresh(false)
		tm.config.recorder.recordTopologyRefreshLatency(time.Since(startTime))
		return fmt.Errorf("failed to get cluster slots (tried main client and seed nodes): %w", err)
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
		tm.config.logger.Debug("submux: detected migrations", "count", len(migrations))
		for _, m := range migrations {
			tm.config.logger.Debug("submux: migration", "slot", m.hashslot, "old_node", m.oldNode, "new_node", m.newNode)
		}
		tm.handleMigrations(migrations)
	}

	// Record successful topology refresh
	tm.config.recorder.recordTopologyRefresh(true)
	tm.config.recorder.recordTopologyRefreshLatency(time.Since(startTime))

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
	tm.subMux.pool.invalidateHashslot(migration.hashslot)

	// Find all subscriptions affected by this migration
	affectedSubs := tm.findAffectedSubscriptions(migration.hashslot)

	if len(affectedSubs) == 0 {
		// No subscriptions affected, nothing to do
		return
	}

	// Record migration started metric
	tm.config.recorder.recordMigrationStarted()

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

	timestamp := time.Now()

	// Get worker pool and lifecycle context from subMux (may be nil in tests)
	var workerPool *WorkerPool
	var lifecycleCtx context.Context
	if tm.subMux != nil {
		workerPool = tm.subMux.workerPool
		lifecycleCtx = tm.subMux.lifecycleCtx
	}
	if lifecycleCtx == nil {
		lifecycleCtx = context.Background()
	}

	// Send to all affected subscriptions
	// Create a separate message copy for each subscription to avoid race conditions
	for _, sub := range subs {
		msg := &Message{
			Type:             MessageTypeSignal,
			Signal:           signal,
			Timestamp:        timestamp,
			SubscriptionType: sub.subType,
		}
		invokeCallback(tm.config.logger, tm.config.recorder, workerPool, lifecycleCtx, sub.callback, msg)
	}
}

// sendMigrationTimeoutSignal sends a signal message when migration resubscription exceeds the maximum duration.
func (tm *topologyMonitor) sendMigrationTimeoutSignal(subs []*subscription, migration hashslotMigration, duration time.Duration) {
	// Record migration timeout metric
	tm.config.recorder.recordMigrationTimeout()

	signal := &SignalInfo{
		EventType: EventMigrationTimeout,
		Hashslot:  migration.hashslot,
		OldNode:   migration.oldNode,
		NewNode:   migration.newNode,
		Details:   fmt.Sprintf("Hashslot %d migration resubscription exceeded maximum duration of %v. Subscribers may need to manually resubscribe.", migration.hashslot, duration),
	}

	timestamp := time.Now()

	// Get worker pool and lifecycle context from subMux (may be nil in tests)
	var workerPool *WorkerPool
	var lifecycleCtx context.Context
	if tm.subMux != nil {
		workerPool = tm.subMux.workerPool
		lifecycleCtx = tm.subMux.lifecycleCtx
	}
	if lifecycleCtx == nil {
		lifecycleCtx = context.Background()
	}

	// Send to all affected subscriptions
	// Create a separate message copy for each subscription to avoid race conditions
	for _, sub := range subs {
		msg := &Message{
			Type:             MessageTypeSignal,
			Signal:           signal,
			Timestamp:        timestamp,
			SubscriptionType: sub.subType,
		}
		invokeCallback(tm.config.logger, tm.config.recorder, workerPool, lifecycleCtx, sub.callback, msg)
	}

	tm.config.logger.Warn("submux: migration timeout", "hashslot", migration.hashslot, "duration", duration)
}

// sendMigrationStalledSignal sends a signal message when migration resubscription appears to have stalled.
func (tm *topologyMonitor) sendMigrationStalledSignal(subs []*subscription, migration hashslotMigration, stallDuration time.Duration) {
	// Record migration stalled metric
	tm.config.recorder.recordMigrationStalled()

	signal := &SignalInfo{
		EventType: EventMigrationStalled,
		Hashslot:  migration.hashslot,
		OldNode:   migration.oldNode,
		NewNode:   migration.newNode,
		Details:   fmt.Sprintf("Hashslot %d migration resubscription appears stalled (no progress for %v). Subscribers may need to manually resubscribe.", migration.hashslot, stallDuration),
	}

	timestamp := time.Now()

	// Get worker pool and lifecycle context from subMux (may be nil in tests)
	var workerPool *WorkerPool
	var lifecycleCtx context.Context
	if tm.subMux != nil {
		workerPool = tm.subMux.workerPool
		lifecycleCtx = tm.subMux.lifecycleCtx
	}
	if lifecycleCtx == nil {
		lifecycleCtx = context.Background()
	}

	// Send to all affected subscriptions
	// Create a separate message copy for each subscription to avoid race conditions
	for _, sub := range subs {
		msg := &Message{
			Type:             MessageTypeSignal,
			Signal:           signal,
			Timestamp:        timestamp,
			SubscriptionType: sub.subType,
		}
		invokeCallback(tm.config.logger, tm.config.recorder, workerPool, lifecycleCtx, sub.callback, msg)
	}

	tm.config.logger.Warn("submux: migration stalled", "hashslot", migration.hashslot, "duration", stallDuration)
}

// resubscribeOnNewNodeWithMonitoring recreates subscriptions on the new node after migration
// with progress monitoring to detect timeouts and stalls.
func (tm *topologyMonitor) resubscribeOnNewNodeWithMonitoring(subs []*subscription, migration hashslotMigration) {
	migrationTimeout := tm.config.migrationTimeout
	stallCheckInterval := tm.config.migrationStallCheck

	startTime := time.Now()
	lastProgressTime := startTime
	var lastProcessedCount int64

	// Atomic counter for tracking progress from multiple goroutines
	var processedCount atomic.Int64

	// WaitGroup to track spawned goroutines
	var wg sync.WaitGroup

	// Channel to signal completion to monitoring goroutine
	doneCh := make(chan struct{})

	totalSubs := int64(len(subs))

	// Progress monitoring goroutine
	go func() {
		ticker := time.NewTicker(stallCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-doneCh:
				return
			case <-ticker.C:
				currentCount := processedCount.Load()

				// Check if completed
				if currentCount >= totalSubs {
					return
				}

				// Check for timeout
				if time.Since(startTime) > migrationTimeout {
					tm.sendMigrationTimeoutSignal(subs, migration, migrationTimeout)
					return
				}

				// Check for progress since last tick
				if currentCount > lastProcessedCount {
					lastProcessedCount = currentCount
					lastProgressTime = time.Now()
				} else if time.Since(lastProgressTime) > stallCheckInterval {
					// No progress in last interval - it's a stall
					tm.sendMigrationStalledSignal(subs, migration, time.Since(lastProgressTime))
					return
				}
			}
		}
	}()

	// Perform resubscription
	tm.resubscribeOnNewNode(subs, migration, &processedCount, &wg)

	// Wait for all spawned goroutines to complete before signaling done
	wg.Wait()

	// Record migration metrics
	duration := time.Since(startTime)
	tm.config.recorder.recordMigrationDuration(duration)
	tm.config.recorder.recordMigrationCompleted()

	// Signal monitoring goroutine to exit
	close(doneCh)
}

// resubscribeOnNewNode recreates subscriptions on the new node after migration.
func (tm *topologyMonitor) resubscribeOnNewNode(subs []*subscription, migration hashslotMigration, processedCount *atomic.Int64, wg *sync.WaitGroup) {
	// Group subscriptions by channel (to avoid duplicate SUBSCRIBE commands)
	channelsByType := make(map[subscriptionType]map[string][]*subscription)
	for _, sub := range subs {
		if channelsByType[sub.subType] == nil {
			channelsByType[sub.subType] = make(map[string][]*subscription)
		}
		channelsByType[sub.subType][sub.channel] = append(channelsByType[sub.subType][sub.channel], sub)
	}

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
				tm.config.logger.Error("submux: failed to get PubSub for migrated hashslot", "hashslot", migration.hashslot, "error", err)
				// Report progress even on error
				processedCount.Add(int64(len(channelSubs)))
				continue
			}
			for _, sub := range channelSubs {
				// Update subscription's PubSub reference
				sub.pubsub = newPubsub

				// Get metadata for new PubSub
				meta := tm.subMux.pool.getMetadata(newPubsub)
				if meta == nil {
					processedCount.Add(1)
					continue
				}

				// Re-register subscription in metadata
				meta.addSubscription(sub)

				// Determine command name based on subscription type
				var cmdName string
				switch subType {
				case subTypeSubscribe:
					cmdName = cmdSubscribe
				case subTypePSubscribe:
					cmdName = cmdPSubscribe
				case subTypeSSubscribe:
					cmdName = cmdSSubscribe
				}

				// Check if channel is already subscribed on new PubSub
				// existingSubs now includes the one we just added, so count should be >= 1
				existingSubs := meta.getSubscriptions(channel)
				isFirstOnNewPubSub := len(existingSubs) == 1

				if isFirstOnNewPubSub {
					// Mark as pending
					sub.setState(subStatePending, nil)
					meta.addPendingSubscription(sub)

					// Track this goroutine in the WaitGroup
					wg.Add(1)

					// Send subscription command asynchronously to avoid blocking topology monitor
					go func(s *subscription, cName string, chName string, m *pubSubMetadata, counter *atomic.Int64) {
						defer wg.Done()
						defer counter.Add(1)

						cmd := &command{
							cmd:      cName,
							args:     []any{chName},
							sub:      s,
							response: make(chan error, 1),
						}

						// Use a separate context for the command
						cmdCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()

						if err := m.sendCommand(cmdCtx, cmd); err != nil {
							m.logger.Error("submux: failed to send resubscribe command", "channel", chName, "error", err)
							m.removePendingSubscription(chName)
							s.setState(subStateFailed, err)
							return
						}

						if err := s.waitForConfirmation(cmdCtx); err != nil {
							m.logger.Error("submux: resubscribe confirmation failed", "channel", chName, "error", err)
							// Cleanup handled by waitForConfirmation/eventLoop usually, but ensure consistency
							s.setState(subStateFailed, err)
						}
					}(sub, cmdName, channel, meta, processedCount)
				} else {
					// Channel is already active on this connection
					sub.setState(subStateActive, nil)
					processedCount.Add(1)
				}
			}
		}
	}
}
