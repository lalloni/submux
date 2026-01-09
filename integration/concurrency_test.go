package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lalloni/submux"
)

func TestConcurrentSubscriptions(t *testing.T) {
	t.Parallel()
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	// Create multiple goroutines subscribing to different channels concurrently
	numGoroutines := 10
	channelsPerGoroutine := 5
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			channels := make([]string, channelsPerGoroutine)
			for j := 0; j < channelsPerGoroutine; j++ {
				channels[j] = fmt.Sprintf("concurrent-%d-%d", id, j)
			}

			_, subErr := subMux.SubscribeSync(context.Background(), channels, func(msg *submux.Message) {
				// Message received
			})
			if subErr != nil {
				errors <- fmt.Errorf("goroutine %d failed to subscribe: %w", id, subErr)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Error(err)
	}
}

func TestConcurrentMessageHandling(t *testing.T) {
	t.Parallel()
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	// Subscribe to a channel
	messageCount := 0
	var mu sync.Mutex
	messages := make(chan *submux.Message, 1000)

	_, err = subMux.SubscribeSync(context.Background(), []string{"concurrent-msg"}, func(msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			mu.Lock()
			messageCount++
			mu.Unlock()
			messages <- msg
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Publish many messages concurrently
	numPublishers := 10
	messagesPerPublisher := 50
	var wg sync.WaitGroup
	pubClient := cluster.GetClusterClient()

	for i := 0; i < numPublishers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < messagesPerPublisher; j++ {
				err := pubClient.Publish(context.Background(), "concurrent-msg", fmt.Sprintf("msg-%d-%d", id, j)).Err()
				if err != nil {
					t.Errorf("Failed to publish message %d-%d: %v", id, j, err)
				}
			}
		}(i)
	}

	wg.Wait()

	// Wait for all messages to be received
	expectedCount := numPublishers * messagesPerPublisher
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		mu.Lock()
		received := messageCount
		mu.Unlock()

		if received >= expectedCount {
			break
		}

		select {
		case <-messages:
			// Drain messages
		case <-ctx.Done():
			mu.Lock()
			defer mu.Unlock()
			t.Errorf("Timeout waiting for messages: expected %d, received %d", expectedCount, messageCount)
			return
		case <-ticker.C:
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if messageCount != expectedCount {
		t.Errorf("Expected %d messages, received %d", expectedCount, messageCount)
	}
}

func TestConcurrentSubscribeUnsubscribe(t *testing.T) {
	t.Parallel()
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channels := []string{"concurrent-sub-1", "concurrent-sub-2", "concurrent-sub-3"}
	var wg sync.WaitGroup
	errors := make(chan error, 20)

	// Concurrent subscribe and unsubscribe operations
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Subscribe
			sub, err := subMux.SubscribeSync(context.Background(), channels, func(msg *submux.Message) {})
			if err != nil {
				errors <- fmt.Errorf("subscribe failed (goroutine %d): %w", id, err)
				return
			}

			// SubscribeSync already waits for confirmation, so no delay needed

			// Unsubscribe
			err = sub.Unsubscribe(context.Background())
			if err != nil {
				errors <- fmt.Errorf("unsubscribe failed (goroutine %d): %w", id, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Error(err)
	}
}

func TestConcurrentMultipleSubscriptionsSameChannel(t *testing.T) {
	// Note: not using t.Parallel() as this test is timing-sensitive
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	// Multiple goroutines subscribing to the same channel concurrently
	// Using 3 goroutines to test concurrency while keeping the test fast
	channel := uniqueChannel("shared")
	numGoroutines := 3
	callbackCount := 0
	var mu sync.Mutex
	callbackCh := make(chan struct{}, numGoroutines)

	var wg sync.WaitGroup
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			_, subErr := subMux.SubscribeSync(context.Background(), []string{channel}, func(msg *submux.Message) {
				if msg.Type == submux.MessageTypeMessage {
					mu.Lock()
					callbackCount++
					mu.Unlock()
					// Signal that a callback was invoked
					select {
					case callbackCh <- struct{}{}:
					default:
					}
				}
			})
			if subErr != nil {
				t.Errorf("Goroutine %d failed to subscribe: %v", id, subErr)
			}
		}(i)
	}

	wg.Wait()

	// SubscribeSync already waits for confirmation, so all subscriptions are ready
	// Publish a message - all callbacks should be invoked
	pubClient := cluster.GetClusterClient()
	err = pubClient.Publish(context.Background(), channel, "test").Err()
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	// Wait for all callbacks to be invoked using channel instead of polling
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	received := 0
	for received < numGoroutines {
		select {
		case <-callbackCh:
			received++
		case <-ctx.Done():
			mu.Lock()
			count := callbackCount
			mu.Unlock()
			if count < numGoroutines {
				t.Errorf("Too few callbacks: expected %d, got %d", numGoroutines, count)
			}
			return
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if callbackCount != numGoroutines {
		t.Errorf("Expected %d callbacks, got %d", numGoroutines, callbackCount)
	}
}

func TestConcurrentTopologyChanges(t *testing.T) {
	t.Parallel()
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	// Subscribe to multiple channels
	channels := []string{"topo-1", "topo-2", "topo-3"}
	messages := make(chan *submux.Message, 100)
	signalMessages := make(chan *submux.Message, 100)

	_, err = subMux.SubscribeSync(context.Background(), channels, func(msg *submux.Message) {
		if msg.Type == submux.MessageTypeSignal {
			signalMessages <- msg
		} else {
			messages <- msg
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Concurrently publish messages and trigger topology refreshes
	var wg sync.WaitGroup
	pubClient := cluster.GetClusterClient()

	// Publisher goroutines
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				for _, ch := range channels {
					err := pubClient.Publish(context.Background(), ch, fmt.Sprintf("msg-%d-%d", id, j)).Err()
					if err != nil {
						t.Logf("Publish error (may be expected during topology changes): %v", err)
					}
				}
			}
		}(i)
	}

	// Topology refresh goroutines
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				_ = client.ClusterSlots(context.Background()).Err()
			}
		}()
	}

	wg.Wait()

	// Verify we received some messages (exact count may vary due to concurrency)
	// Use shorter timeout and collect messages that are already buffered
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	receivedCount := 0
	// First, drain any buffered messages
	for {
		select {
		case <-messages:
			receivedCount++
		case <-signalMessages:
			// signal messages are acceptable
		default:
			goto checkTimeout
		}
	}
checkTimeout:
	// Then wait briefly for any additional messages
	for {
		select {
		case <-messages:
			receivedCount++
		case <-signalMessages:
			// signal messages are acceptable
		case <-ctx.Done():
			goto done
		}
	}
done:
	if receivedCount == 0 {
		t.Error("Did not receive any messages during concurrent operations")
	}
	t.Logf("Received %d messages during concurrent topology operations", receivedCount)
}
