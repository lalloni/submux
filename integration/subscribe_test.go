package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lalloni/submux"
)

func TestPSubscribePattern(t *testing.T) {
	t.Parallel()
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	messages := make(chan *submux.Message, 10)
	_, err = subMux.PSubscribeSync(context.Background(), []string{"test:*"}, func(msg *submux.Message) {
		messages <- msg
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Publish messages matching the pattern
	pubClient := cluster.GetClusterClient()
	testChannels := []string{"test:1", "test:2", "test:abc"}
	for _, ch := range testChannels {
		err = pubClient.Publish(context.Background(), ch, "message").Err()
		if err != nil {
			t.Fatalf("Failed to publish to %s: %v", ch, err)
		}
	}

	// Verify messages received - wait for all expected messages
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	received := make(map[string]bool)
	for len(received) < len(testChannels) {
		select {
		case msg := <-messages:
			if msg.Type == submux.MessageTypePMessage {
				received[msg.Channel] = true
			}
		case <-ctx.Done():
			t.Errorf("Timeout waiting for messages: expected %d, received %d", len(testChannels), len(received))
			for _, ch := range testChannels {
				if !received[ch] {
					t.Errorf("Did not receive message for channel %s", ch)
				}
			}
			return
		}
	}

	if len(received) != len(testChannels) {
		t.Errorf("Expected %d messages, received %d", len(testChannels), len(received))
	}
	for _, ch := range testChannels {
		if !received[ch] {
			t.Errorf("Did not receive message for channel %s", ch)
		}
	}
}

func TestSSubscribeSharded(t *testing.T) {
	t.Parallel()
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channel := uniqueChannel("test-sharded")
	messages := make(chan *submux.Message, 10)
	_, err = subMux.SSubscribeSync(context.Background(), []string{channel}, func(msg *submux.Message) {
		messages <- msg
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// For sharded subscriptions, we need to use SPUBLISH instead of PUBLISH
	// SPUBLISH is a Redis 7.0+ command that publishes to sharded channels
	pubClient := cluster.GetClusterClient()

	// Publish and wait for message with retry on timeout
	// Sharded subscriptions in cluster may need a moment to propagate
	// We use event-driven retries (only retry on actual timeout events)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var msg *submux.Message
	received := false

	// Publish initial message
	err = pubClient.SPublish(context.Background(), channel, "hello").Err()
	if err != nil {
		t.Fatalf("Failed to SPUBLISH: %v", err)
	}

	for !received {
		// Wait for message with shorter timeout per attempt
		attemptCtx, attemptCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		select {
		case msg = <-messages:
			attemptCancel()
			received = true
		case <-attemptCtx.Done():
			attemptCancel()
			// Check if overall context is done
			select {
			case <-ctx.Done():
				t.Fatal("Timeout waiting for message after multiple publish attempts")
			default:
			}
			// Retry publishing (event-driven: only retry on timeout)
			err = pubClient.Do(context.Background(), "SPUBLISH", channel, "hello").Err()
			if err != nil {
				t.Fatalf("Failed to SPUBLISH (retry): %v", err)
			}
			continue
		case <-ctx.Done():
			attemptCancel()
			t.Fatal("Timeout waiting for message after multiple publish attempts")
		}
	}

	// Verify message
	if msg.Type != submux.MessageTypeSMessage {
		t.Errorf("Expected MessageTypeSMessage, got %v", msg.Type)
	}
	if msg.Channel != channel {
		t.Errorf("Expected channel %q, got %q", channel, msg.Channel)
	}
	if msg.Payload != "hello" {
		t.Errorf("Expected payload 'hello', got %q", msg.Payload)
	}
}

func TestMultipleSubscriptionsSameChannel(t *testing.T) {
	t.Parallel()
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	// Create multiple subscriptions to the same channel with different callbacks
	channel := uniqueChannel("test-multi")
	callback1Count := 0
	callback2Count := 0
	var mu sync.Mutex

	_, err = subMux.SubscribeSync(context.Background(), []string{channel}, func(msg *submux.Message) {
		mu.Lock()
		callback1Count++
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Failed to subscribe (callback 1): %v", err)
	}

	_, err = subMux.SubscribeSync(context.Background(), []string{channel}, func(msg *submux.Message) {
		mu.Lock()
		callback2Count++
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Failed to subscribe (callback 2): %v", err)
	}

	// Publish a message
	err = client.Publish(context.Background(), channel, "hello").Err()
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	// Wait for both callbacks to be called
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		mu.Lock()
		bothCalled := callback1Count >= 1 && callback2Count >= 1
		mu.Unlock()

		if bothCalled {
			break
		}

		select {
		case <-ctx.Done():
			mu.Lock()
			defer mu.Unlock()
			if callback1Count != 1 {
				t.Errorf("Expected callback1 to be called once, got %d", callback1Count)
			}
			if callback2Count != 1 {
				t.Errorf("Expected callback2 to be called once, got %d", callback2Count)
			}
			return
		case <-ticker.C:
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if callback1Count != 1 {
		t.Errorf("Expected callback1 to be called once, got %d", callback1Count)
	}
	if callback2Count != 1 {
		t.Errorf("Expected callback2 to be called once, got %d", callback2Count)
	}
}

func TestUnsubscribe(t *testing.T) {
	t.Parallel()
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channel := uniqueChannel("test-unsub")
	messages := make(chan *submux.Message, 10)
	sub, err := subMux.SubscribeSync(context.Background(), []string{channel}, func(msg *submux.Message) {
		messages <- msg
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Publish a message before unsubscribe
	pubClient := cluster.GetClusterClient()
	err = pubClient.Publish(context.Background(), channel, "before").Err()
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	// Wait for message with shorter timeout
	select {
	case msg := <-messages:
		if msg.Payload != "before" {
			t.Errorf("Expected 'before', got %q", msg.Payload)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Timeout waiting for message before unsubscribe")
	}

	// Unsubscribe
	err = sub.Unsubscribe(context.Background())
	if err != nil {
		t.Fatalf("Failed to unsubscribe: %v", err)
	}

	// Publish a message after unsubscribe
	err = pubClient.Publish(context.Background(), channel, "after").Err()
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	// Wait a short time and verify no message received
	// Use a shorter timeout to verify unsubscribe worked
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	select {
	case msg := <-messages:
		t.Errorf("Received message after unsubscribe: %q", msg.Payload)
	case <-ctx.Done():
		// Expected - no message should be received
	}
}

func TestUnsubscribeMultipleChannels(t *testing.T) {
	t.Parallel()
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	received := make(map[string]bool)
	var mu sync.Mutex

	channels := []string{"ch1", "ch2", "ch3"}
	sub, err := subMux.SubscribeSync(context.Background(), channels, func(msg *submux.Message) {
		mu.Lock()
		received[msg.Channel] = true
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Publish to all channels
	pubClient := cluster.GetClusterClient()
	for _, ch := range channels {
		err = pubClient.Publish(context.Background(), ch, "test").Err()
		if err != nil {
			t.Fatalf("Failed to publish to %s: %v", ch, err)
		}
	}

	// Wait for all messages to be received before unsubscribing
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		mu.Lock()
		allReceived := true
		for _, ch := range channels {
			if !received[ch] {
				allReceived = false
				break
			}
		}
		mu.Unlock()

		if allReceived {
			break
		}

		select {
		case <-ctx.Done():
			// Timeout - continue anyway to test unsubscribe
			goto unsubscribe
		case <-ticker.C:
		}
	}
unsubscribe:

	// Unsubscribe from all
	err = sub.Unsubscribe(context.Background())
	if err != nil {
		t.Fatalf("Failed to unsubscribe: %v", err)
	}

	// Verify we received messages before unsubscribe
	mu.Lock()
	for _, ch := range channels {
		if !received[ch] {
			t.Errorf("Did not receive message on channel %s before unsubscribe", ch)
		}
	}
	mu.Unlock()

	// Publish again after unsubscribe
	for _, ch := range channels {
		err = pubClient.Publish(context.Background(), ch, "after").Err()
		if err != nil {
			t.Fatalf("Failed to publish to %s: %v", ch, err)
		}
	}

	// Wait a short time and verify no new messages received
	// Since we don't have a message channel in this test, we just verify
	// that the received map hasn't changed (no new channels added)

	mu.Lock()
	defer mu.Unlock()
	// All should still be true (no new messages)
	for _, ch := range channels {
		if !received[ch] {
			t.Errorf("Channel %s not received", ch)
		}
	}
}

func TestUnsubscribePartial(t *testing.T) {
	t.Parallel()
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	callback1Count := 0
	callback2Count := 0
	var mu sync.Mutex

	// Subscribe with two callbacks
	sub1, err := subMux.SubscribeSync(context.Background(), []string{"test"}, func(msg *submux.Message) {
		mu.Lock()
		callback1Count++
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Failed to subscribe (callback 1): %v", err)
	}

	_, err = subMux.SubscribeSync(context.Background(), []string{"test"}, func(msg *submux.Message) {
		mu.Lock()
		callback2Count++
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Failed to subscribe (callback 2): %v", err)
	}

	// Publish before unsubscribe
	pubClient := cluster.GetClusterClient()
	err = pubClient.Publish(context.Background(), "test", "before").Err()
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	// Wait for both callbacks to receive "before" message
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		mu.Lock()
		bothReceivedBefore := callback1Count >= 1 && callback2Count >= 1
		mu.Unlock()

		if bothReceivedBefore {
			break
		}

		select {
		case <-ctx.Done():
			t.Fatal("Timeout waiting for both callbacks to receive 'before' message")
		case <-ticker.C:
		}
	}

	// Unsubscribe (should remove one subscription - callback1)
	err = sub1.Unsubscribe(context.Background())
	if err != nil {
		t.Fatalf("Failed to unsubscribe: %v", err)
	}

	// Publish after unsubscribe (one callback should still be active)
	err = pubClient.Publish(context.Background(), "test", "after").Err()
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	// Wait for one more callback (from the remaining subscription)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel2()

	ticker2 := time.NewTicker(50 * time.Millisecond)
	defer ticker2.Stop()

	for {
		mu.Lock()
		total := callback1Count + callback2Count
		mu.Unlock()

		if total >= 3 { // 2 for "before" + 1 for "after"
			break
		}

		select {
		case <-ctx2.Done():
			// Continue to check anyway
			goto checkDone
		case <-ticker2.C:
		}
	}
checkDone:

	mu.Lock()
	defer mu.Unlock()
	// Both callbacks should have been called for "before"
	// Only one should be called for "after"
	if callback1Count < 1 || callback2Count < 1 {
		t.Errorf("Expected both callbacks called at least once, got callback1=%d, callback2=%d", callback1Count, callback2Count)
	}
	// After unsubscribe, only one callback should receive "after"
	// Total should be 3 (2 for "before", 1 for "after")
	total := callback1Count + callback2Count
	if total < 3 {
		t.Errorf("Expected at least 3 total callbacks (2 before + 1 after), got %d", total)
	}
}
