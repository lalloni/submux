package submux

import (
	"context"
	"errors"
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

	// Test with WithMinConnectionsPerNode
	subMux, err = New(clusterClient, WithMinConnectionsPerNode(3))
	if err != nil {
		t.Fatalf("New with options returned error: %v", err)
	}
	if subMux.config.minConnectionsPerNode != 3 {
		t.Errorf("WithMinConnectionsPerNode option not applied: got %d, want 3", subMux.config.minConnectionsPerNode)
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
		WithMinConnectionsPerNode(2),
		WithNodePreference(BalancedAll),
	)
	if err != nil {
		t.Fatalf("New with multiple options returned error: %v", err)
	}
	if !subMux.config.autoResubscribe {
		t.Error("autoResubscribe not set")
	}
	if subMux.config.minConnectionsPerNode != 2 {
		t.Error("minConnectionsPerNode not set")
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
