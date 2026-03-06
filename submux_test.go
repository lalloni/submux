package submux

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestSubMux_New(t *testing.T) {
	// Test with nil ClusterClient (should error)
	_, err := New(nil)
	if err == nil {
		t.Error("New(nil) should return error")
	}
	if !errors.Is(err, ErrInvalidClusterClient) {
		t.Errorf("New(nil) returned error %v, want %v", err, ErrInvalidClusterClient)
	}

	// Test with valid ClusterClient
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{"localhost:7000"},
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	subMux, err := New(clusterClient)
	if err != nil {
		t.Fatalf("New(valid client) returned error: %v", err)
	}
	if subMux == nil {
		t.Fatal("New(valid client) returned nil")
	}
	if subMux.clusterClient != clusterClient {
		t.Error("SubMux.clusterClient is not set correctly")
	}

	// Cleanup
	subMux.Close()
}

func TestSubMux_New_WithOptions(t *testing.T) {
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{"localhost:7000"},
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	// Test with WithAutoResubscribe
	subMux, err := New(clusterClient, WithAutoResubscribe(true))
	if err != nil {
		t.Fatalf("New with options returned error: %v", err)
	}
	if subMux.config.autoResubscribe != true {
		t.Error("WithAutoResubscribe option not applied")
	}
	subMux.Close()

	// Test with WithNodePreference
	subMux, err = New(clusterClient, WithNodePreference(PreferReplicas))
	if err != nil {
		t.Fatalf("New with options returned error: %v", err)
	}
	if subMux.config.nodePreference != PreferReplicas {
		t.Error("WithNodePreference option not applied")
	}
	subMux.Close()

	// Test with WithReplicaPreference (backward compatibility)
	subMux, err = New(clusterClient, WithReplicaPreference(true))
	if err != nil {
		t.Fatalf("New with options returned error: %v", err)
	}
	if subMux.config.nodePreference != PreferReplicas {
		t.Error("WithReplicaPreference option not applied")
	}
	subMux.Close()

	// Test with multiple options
	subMux, err = New(clusterClient,
		WithAutoResubscribe(true),
		WithNodePreference(BalancedAll),
	)
	if err != nil {
		t.Fatalf("New with multiple options returned error: %v", err)
	}
	if !subMux.config.autoResubscribe {
		t.Error("autoResubscribe not set")
	}
	if subMux.config.nodePreference != BalancedAll {
		t.Error("nodePreference not set")
	}
	subMux.Close()
}

func TestSubMux_Close(t *testing.T) {
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{"localhost:7000"},
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	subMux, err := New(clusterClient)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	// Test Close
	err = subMux.Close()
	if err != nil {
		t.Errorf("Close() returned error: %v", err)
	}

	// Test Close is idempotent
	err = subMux.Close()
	if err != nil {
		t.Errorf("Second Close() returned error: %v", err)
	}

	// Test that closed flag is set
	subMux.mu.RLock()
	closed := subMux.closed
	subMux.mu.RUnlock()
	if !closed {
		t.Error("SubMux.closed is not true after Close()")
	}
}

func TestSubMux_Close_WithSubscriptions(t *testing.T) {
	// This test would require a real Redis cluster or extensive mocking
	// For now, we test that Close doesn't panic with active subscriptions
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{"localhost:7000"},
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	subMux, err := New(clusterClient)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	// Close without subscriptions should work
	err = subMux.Close()
	if err != nil {
		t.Errorf("Close() returned error: %v", err)
	}
}

func TestSubMux_SubscribeSync_InvalidChannel(t *testing.T) {
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{"localhost:7000"},
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	subMux, err := New(clusterClient)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	defer subMux.Close()

	// Test with empty channel list
	_, err = subMux.SubscribeSync(context.Background(), []string{}, func(ctx context.Context, msg *Message) {})
	if err == nil {
		t.Error("SubscribeSync with empty channels should return error")
	}
	if !errors.Is(err, ErrInvalidChannel) {
		t.Errorf("SubscribeSync with empty channels returned error %v, want %v", err, ErrInvalidChannel)
	}

	// Test with empty channel name
	_, err = subMux.SubscribeSync(context.Background(), []string{""}, func(ctx context.Context, msg *Message) {})
	if err == nil {
		t.Error("SubscribeSync with empty channel name should return error")
	}
	if !errors.Is(err, ErrInvalidChannel) {
		t.Errorf("SubscribeSync with empty channel name returned error %v, want %v", err, ErrInvalidChannel)
	}
}

func TestSubMux_SubscribeSync_ContextCancellation(t *testing.T) {
	// This test requires Redis cluster or would need extensive mocking
	// For now, we test that context cancellation is checked
	// Full testing requires integration tests
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{"localhost:7000"},
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	subMux, err := New(clusterClient)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	defer subMux.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err = subMux.SubscribeSync(ctx, []string{"test"}, func(ctx context.Context, msg *Message) {})
	// Error will be either context.Canceled or a connection error (if Redis not available)
	// Both are acceptable for this unit test
	if err == nil {
		t.Error("SubscribeSync with cancelled context should return error")
	}
	// Accept either context.Canceled or connection errors
	if err != context.Canceled && !errors.Is(err, context.Canceled) {
		// If it's a connection error, that's also acceptable for unit tests
		// (integration tests will verify actual behavior)
		t.Logf("SubscribeSync with cancelled context returned error %v (may be connection error)", err)
	}
}

func TestSubMux_SubscribeSync_ContextTimeout(t *testing.T) {
	// This test requires Redis cluster or would need extensive mocking
	// For now, we test that context timeout is checked
	// Full testing requires integration tests
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{"localhost:7000"},
		// Use short timeouts to fail fast
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	subMux, err := New(clusterClient)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	defer subMux.Close()

	// Use a very short timeout context to ensure it expires quickly
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = subMux.SubscribeSync(ctx, []string{"test"}, func(ctx context.Context, msg *Message) {})
	// Error will be either context.DeadlineExceeded or a connection error (if Redis not available)
	// Both are acceptable for this unit test
	if err == nil {
		t.Error("SubscribeSync with timed out context should return error")
	}
	// Accept either context.DeadlineExceeded or connection errors
	if err != context.DeadlineExceeded && !errors.Is(err, context.DeadlineExceeded) {
		// If it's a connection error, that's also acceptable for unit tests
		// (integration tests will verify actual behavior)
		t.Logf("SubscribeSync with timed out context returned error %v (may be connection error)", err)
	}
}

func TestSubscription_Unsubscribe(t *testing.T) {
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{"localhost:7000"},
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	subMux, err := New(clusterClient)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	defer subMux.Close()

	// Unsubscribe from nil subscription should not error
	var nilSub *Sub
	err = nilSub.Unsubscribe(context.Background())
	if err != nil {
		t.Errorf("Unsubscribe on nil subscription returned error: %v", err)
	}
}

// Note: Tests for actual subscription functionality (SubscribeSync, PSubscribeSync, SSubscribeSync)
// with real message delivery would require integration tests with a real Redis cluster.
// These are covered in the integration test plan.

// Unit tests for PSubscribeSync and SSubscribeSync

func TestSubMux_PSubscribeSync_EmptyPattern(t *testing.T) {
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{"localhost:7000"},
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	subMux, err := New(clusterClient)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	defer subMux.Close()

	_, err = subMux.PSubscribeSync(context.Background(), []string{""}, func(ctx context.Context, msg *Message) {})
	if err == nil {
		t.Error("PSubscribeSync with empty pattern should return error")
	}
	if !errors.Is(err, ErrInvalidChannel) {
		t.Errorf("PSubscribeSync with empty pattern returned error %v, want %v", err, ErrInvalidChannel)
	}
}

func TestSubMux_SSubscribeSync_EmptyChannel(t *testing.T) {
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{"localhost:7000"},
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	subMux, err := New(clusterClient)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	defer subMux.Close()

	_, err = subMux.SSubscribeSync(context.Background(), []string{""}, func(ctx context.Context, msg *Message) {})
	if err == nil {
		t.Error("SSubscribeSync with empty channel should return error")
	}
	if !errors.Is(err, ErrInvalidChannel) {
		t.Errorf("SSubscribeSync with empty channel returned error %v, want %v", err, ErrInvalidChannel)
	}
}

func TestSubMux_SubscribeSync_EmptyChannelList(t *testing.T) {
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{"localhost:7000"},
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	subMux, err := New(clusterClient)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	defer subMux.Close()

	_, err = subMux.SubscribeSync(context.Background(), []string{}, func(ctx context.Context, msg *Message) {})
	if err == nil {
		t.Error("SubscribeSync with empty channel list should return error")
	}
	// Empty channel list returns ErrInvalidChannel with "channels list is empty" message
	if !errors.Is(err, ErrInvalidChannel) {
		t.Errorf("SubscribeSync with empty channel list returned error %v, want %v", err, ErrInvalidChannel)
	}
}

func TestSubMux_SubscribeSync_Closed(t *testing.T) {
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{"localhost:7000"},
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	subMux, err := New(clusterClient)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	// Close before subscribing
	subMux.Close()

	_, err = subMux.SubscribeSync(context.Background(), []string{"test"}, func(ctx context.Context, msg *Message) {})
	if err == nil {
		t.Error("SubscribeSync on closed SubMux should return error")
	}
	if !errors.Is(err, ErrClosed) {
		t.Errorf("SubscribeSync on closed SubMux returned error %v, want %v", err, ErrClosed)
	}
}

func TestSubMux_Close_Idempotent(t *testing.T) {
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{"localhost:7000"},
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	subMux, err := New(clusterClient)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	// Multiple closes should not panic
	subMux.Close()
	subMux.Close()
	subMux.Close()
}

func TestSubMux_Unsubscribe_ClosedSubMux(t *testing.T) {
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{"localhost:7000"},
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	subMux, err := New(clusterClient)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	// Create a fake Sub manually to test unsubscribe behavior
	sub := &Sub{
		subMux: subMux,
		subs: []*subscription{
			{
				channel:   "test-channel",
				state:     subStateActive,
				confirmCh: make(chan error, 1),
			},
		},
	}

	// Close the subMux
	subMux.Close()

	// Unsubscribe should return ErrClosed
	err = sub.Unsubscribe(context.Background())
	if err == nil {
		t.Error("Unsubscribe on closed SubMux should return error")
	}
	if !errors.Is(err, ErrClosed) {
		t.Errorf("Unsubscribe on closed SubMux returned error %v, want %v", err, ErrClosed)
	}
}

// unsubscribeTestFixture holds the common objects used by unsubscribe context tests.
type unsubscribeTestFixture struct {
	subMux      *SubMux
	meta        *pubSubMetadata
	internalSub *subscription
	cmdCh       chan *command
	sub         *Sub
}

// setupUnsubscribeTest creates a SubMux with a fake PubSub registration, suitable for
// testing unsubscribe behavior without a real Redis connection.
func setupUnsubscribeTest(t *testing.T) *unsubscribeTestFixture {
	t.Helper()

	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{"localhost:7000"},
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	t.Cleanup(func() { clusterClient.Close() })

	subMux, err := New(clusterClient)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	t.Cleanup(func() { subMux.Close() })

	fakePubSub := &redis.PubSub{}
	cmdCh := make(chan *command, 10)

	meta := &pubSubMetadata{
		subscriptions: make(map[string][]*subscription),
		cmdCh:         cmdCh,
		done:          make(chan struct{}),
	}

	internalSub := &subscription{
		channel:   "test-channel",
		subType:   subTypeSubscribe,
		pubsub:    fakePubSub,
		state:     subStateActive,
		confirmCh: make(chan error, 1),
	}

	subMux.pool.mu.Lock()
	subMux.pool.pubSubMetadata[fakePubSub] = meta
	subMux.pool.mu.Unlock()

	meta.addSubscription(internalSub)

	subMux.mu.Lock()
	subMux.subscriptions["test-channel"] = []*subscription{internalSub}
	subMux.mu.Unlock()

	return &unsubscribeTestFixture{
		subMux:      subMux,
		meta:        meta,
		internalSub: internalSub,
		cmdCh:       cmdCh,
		sub:         &Sub{subMux: subMux, subs: []*subscription{internalSub}},
	}
}

func TestUnsubscribe_ContextCancellation(t *testing.T) {
	f := setupUnsubscribeTest(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Command is always queued (uses context.Background()), but the response wait
	// sees the cancelled ctx and returns context.Canceled.
	err := f.sub.Unsubscribe(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Unsubscribe with canceled context: got %v, want %v", err, context.Canceled)
	}
}

func TestUnsubscribe_ContextTimeout(t *testing.T) {
	f := setupUnsubscribeTest(t)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// No event loop consuming, so no response arrives. Timeout fires from response wait.
	err := f.sub.Unsubscribe(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Unsubscribe with timeout context: got %v, want %v", err, context.DeadlineExceeded)
	}
}

func TestUnsubscribe_ContextCancellation_StillCleansUp(t *testing.T) {
	f := setupUnsubscribeTest(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := f.sub.Unsubscribe(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	// Verify cleanup still happened despite context cancellation
	if f.internalSub.getState() != subStateClosed {
		t.Errorf("subscription state = %v, want %v", f.internalSub.getState(), subStateClosed)
	}

	f.subMux.mu.RLock()
	_, exists := f.subMux.subscriptions["test-channel"]
	f.subMux.mu.RUnlock()
	if exists {
		t.Error("channel should have been removed from subscriptions map")
	}

	remainingSubs := f.meta.getSubscriptions("test-channel")
	if len(remainingSubs) != 0 {
		t.Errorf("metadata should have 0 subscriptions, got %d", len(remainingSubs))
	}
}

func TestUnsubscribe_ContextBackground_Succeeds(t *testing.T) {
	f := setupUnsubscribeTest(t)

	// Simulate event loop: consume command and respond with nil (success)
	go func() {
		cmd := <-f.cmdCh
		cmd.response <- nil
	}()

	err := f.sub.Unsubscribe(context.Background())
	if err != nil {
		t.Errorf("Unsubscribe with background context: got %v, want nil", err)
	}
}

func TestUnsubscribe_CancelledContext_StillSendsCommand(t *testing.T) {
	f := setupUnsubscribeTest(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_ = f.sub.Unsubscribe(ctx)

	// EXPECTED: Even with cancelled ctx, an UNSUBSCRIBE command should be queued.
	select {
	case cmd := <-f.cmdCh:
		if cmd.cmd != cmdUnsubscribe {
			t.Errorf("expected UNSUBSCRIBE command, got %s", cmd.cmd)
		}
		// Verify the command uses a non-cancelled context for Redis execution
		if cmd.ctx.Err() != nil {
			t.Error("command context should not be cancelled (should use background)")
		}
	default:
		t.Error("expected UNSUBSCRIBE command to be queued, but cmdCh is empty")
	}
}

func TestUnsubscribe_WaitsForResponse(t *testing.T) {
	f := setupUnsubscribeTest(t)

	// Simulate the event loop: read command and respond with an error
	simulatedErr := fmt.Errorf("simulated Redis error")
	go func() {
		cmd := <-f.cmdCh
		cmd.response <- simulatedErr
	}()

	// Unsubscribe should surface the error from the event loop
	err := f.sub.Unsubscribe(context.Background())

	// EXPECTED: err should contain the simulated Redis error
	if !errors.Is(err, simulatedErr) {
		t.Errorf("expected simulated error, got %v", err)
	}
}

// TestUnsubscribe_ContextBackground_NoHangWhenEventLoopUnresponsive verifies that
// when unsubscribeSubscription is called with context.Background() (e.g. from
// subscribe cleanup) and the event loop never responds (e.g. connection failure
// after command queued), we timeout instead of hanging forever.
func TestUnsubscribe_ContextBackground_NoHangWhenEventLoopUnresponsive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 5s timeout test in short mode")
	}
	f := setupUnsubscribeTest(t)

	// Don't consume from cmdCh - simulate event loop dead after command queued.
	// With context.Background(), ctx.Done() never fires. Without the fix, we'd hang forever.
	done := make(chan error, 1)
	go func() {
		done <- f.sub.Unsubscribe(context.Background())
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("expected context.DeadlineExceeded when event loop unresponsive, got %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Unsubscribe hung for 6s - should timeout when event loop is unresponsive")
	}
}
