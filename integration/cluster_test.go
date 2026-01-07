package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lalloni/submux"
)

func TestSubscribeBasic(t *testing.T) {
	t.Parallel()
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	if client == nil {
		t.Fatal("cluster client is nil")
	}

	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channel := uniqueChannel("test")
	messages := make(chan *submux.Message, 10)
	_, err = subMux.SubscribeSync(context.Background(), []string{channel}, func(msg *submux.Message) {
		messages <- msg
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Publish message
	err = client.Publish(context.Background(), channel, "hello").Err()
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	// Verify message received
	select {
	case msg := <-messages:
		if msg.Type != submux.MessageTypeMessage {
			t.Errorf("Expected MessageTypeMessage, got %v", msg.Type)
		}
		if msg.Channel != channel {
			t.Errorf("Expected channel %q, got %q", channel, msg.Channel)
		}
		if msg.Payload != "hello" {
			t.Errorf("Expected payload 'hello', got %q", msg.Payload)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for message")
	}
}

func TestSubscribeMultipleChannels(t *testing.T) {
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

	_, err = subMux.SubscribeSync(context.Background(), []string{"channel1", "channel2", "channel3"}, func(msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			mu.Lock()
			received[msg.Channel] = true
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Publish to all channels
	channels := []string{"channel1", "channel2", "channel3"}
	for _, ch := range channels {
		err = client.Publish(context.Background(), ch, "test").Err()
		if err != nil {
			t.Fatalf("Failed to publish to %s: %v", ch, err)
		}
	}

	// Wait for all messages to be received
	// Messages arrive asynchronously, so we need to wait for all channels
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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
			mu.Lock()
			defer mu.Unlock()
			for _, ch := range channels {
				if !received[ch] {
					t.Errorf("Did not receive message on channel %s", ch)
				}
			}
			return
		case <-ticker.C:
		}
	}
}

func TestSubscribeMessageDelivery(t *testing.T) {
	t.Parallel()
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	messages := make(chan *submux.Message, 100)
	_, err = subMux.SubscribeSync(context.Background(), []string{"test"}, func(msg *submux.Message) {
		messages <- msg
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Publish multiple messages
	pubClient := cluster.GetClusterClient()
	for i := 0; i < 10; i++ {
		err = pubClient.Publish(context.Background(), "test", fmt.Sprintf("message-%d", i)).Err()
		if err != nil {
			t.Fatalf("Failed to publish message %d: %v", i, err)
		}
	}

	// Verify all messages received - wait for all 10 messages
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	received := 0
	for received < 10 {
		select {
		case <-messages:
			received++
		case <-ctx.Done():
			t.Errorf("Timeout waiting for messages: expected 10, received %d", received)
			return
		}
	}

	if received != 10 {
		t.Errorf("Expected 10 messages, received %d", received)
	}
}

func TestSubscribeErrorHandling(t *testing.T) {
	t.Parallel()
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	// Test with empty channel list
	_, err = subMux.SubscribeSync(context.Background(), []string{}, func(msg *submux.Message) {})
	if err == nil {
		t.Error("Expected error for empty channel list")
	}

	// Test with empty channel name
	_, err = subMux.SubscribeSync(context.Background(), []string{""}, func(msg *submux.Message) {})
	if err == nil {
		t.Error("Expected error for empty channel name")
	}
}
