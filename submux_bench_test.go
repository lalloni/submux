package submux

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// BenchmarkSubscribe benchmarks subscription creation performance.
func BenchmarkSubscribe(b *testing.B) {
	// Skip if no Redis available
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{"localhost:7000"},
	})
	defer clusterClient.Close()

	// Check if cluster is available
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := clusterClient.Ping(ctx).Err(); err != nil {
		b.Skip("Redis cluster not available")
	}

	subMux, err := New(clusterClient)
	if err != nil {
		b.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channels := []string{"bench-channel"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sub, err := subMux.SubscribeSync(context.Background(), channels, func(msg *Message) {
			// Minimal callback
		})
		if err != nil {
			b.Fatalf("Subscribe failed: %v", err)
		}
		// Unsubscribe for next iteration
		_ = sub.Unsubscribe(context.Background())
	}
}

// BenchmarkSubscribe_MultipleChannels benchmarks subscribing to multiple channels.
func BenchmarkSubscribe_MultipleChannels(b *testing.B) {
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{"localhost:7000"},
	})
	defer clusterClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := clusterClient.Ping(ctx).Err(); err != nil {
		b.Skip("Redis cluster not available")
	}

	subMux, err := New(clusterClient)
	if err != nil {
		b.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channels := []string{"ch1", "ch2", "ch3", "ch4", "ch5"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sub, err := subMux.SubscribeSync(context.Background(), channels, func(msg *Message) {
			// Minimal callback
		})
		if err != nil {
			b.Fatalf("Subscribe failed: %v", err)
		}
		_ = sub.Unsubscribe(context.Background())
	}
}

// BenchmarkCallbackInvocation benchmarks callback invocation overhead.
// This test uses a mock to avoid requiring a real Redis cluster.
func BenchmarkCallbackInvocation(b *testing.B) {
	callback := func(msg *Message) {
		// Simulate minimal callback work
		_ = len(msg.Payload)
	}

	msg := &Message{
		Type:    MessageTypeMessage,
		Channel: "test",
		Payload: "test payload",
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		invokeCallback(logger, &noopMetrics{}, callback, msg)
	}
}

// BenchmarkCallbackInvocation_MultipleCallbacks benchmarks multiple callbacks per message.
func BenchmarkCallbackInvocation_MultipleCallbacks(b *testing.B) {
	callbacks := make([]MessageCallback, 10)
	for i := range callbacks {
		callbacks[i] = func(msg *Message) {
			_ = len(msg.Payload)
		}
	}

	msg := &Message{
		Type:    MessageTypeMessage,
		Channel: "test",
		Payload: "test payload",
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, cb := range callbacks {
			invokeCallback(logger, &noopMetrics{}, cb, msg)
		}
	}
}
