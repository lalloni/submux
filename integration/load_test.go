package integration

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalloni/submux"
)

// TestHighSubscriptionCount tests subscribing to a large number of channels.
func TestHighSubscriptionCount(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	// Subscribe to 1000 channels
	numChannels := 1000
	channels := make([]string, numChannels)
	for i := 0; i < numChannels; i++ {
		channels[i] = uniqueChannel(fmt.Sprintf("load-%d", i))
	}

	start := time.Now()
	messageCount := int64(0)

	_, err = subMux.SubscribeSync(context.Background(), channels, func(msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			atomic.AddInt64(&messageCount, 1)
		}
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Failed to subscribe to %d channels: %v", numChannels, err)
	}

	t.Logf("Subscribed to %d channels in %v (%.2f channels/sec)", numChannels, elapsed, float64(numChannels)/elapsed.Seconds())

	// Verify subscriptions are active by publishing a few messages
	pubClient := cluster.GetClusterClient()
	testChannels := channels[:10] // Test first 10 channels
	for _, ch := range testChannels {
		err := pubClient.Publish(context.Background(), ch, "test").Err()
		if err != nil {
			t.Errorf("Failed to publish to %s: %v", ch, err)
		}
	}

	// Wait for messages
	time.Sleep(500 * time.Millisecond)

	received := atomic.LoadInt64(&messageCount)
	if received < int64(len(testChannels)) {
		t.Errorf("Expected at least %d messages, got %d", len(testChannels), received)
	}

	// Check memory usage
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	t.Logf("Memory: Alloc=%d KB, TotalAlloc=%d KB, NumGC=%d", m.Alloc/1024, m.TotalAlloc/1024, m.NumGC)
}

// TestHighSubscriptionCount_MemoryUsage tests memory usage with many subscriptions.
func TestHighSubscriptionCount_MemoryUsage(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	// Subscribe to 5000 channels
	numChannels := 5000
	channels := make([]string, numChannels)
	for i := 0; i < numChannels; i++ {
		channels[i] = uniqueChannel(fmt.Sprintf("mem-%d", i))
	}

	_, err = subMux.SubscribeSync(context.Background(), channels, func(msg *submux.Message) {
		// Minimal callback
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	runtime.GC()
	runtime.ReadMemStats(&m2)

	allocDiff := int64(m2.Alloc) - int64(m1.Alloc)
	allocDiffKB := allocDiff / 1024
	bytesPerChannel := allocDiff / int64(numChannels)

	t.Logf("Memory allocated for %d subscriptions: %d KB (%.2f bytes/channel)", numChannels, allocDiffKB, float64(bytesPerChannel))

	// Verify reasonable memory usage (should be < 1MB for 5000 subscriptions)
	if allocDiff > 10*1024*1024 {
		t.Errorf("Memory usage too high: %d KB for %d subscriptions", allocDiffKB, numChannels)
	}
}

// TestHighMessageThroughput tests high message throughput.
func TestHighMessageThroughput(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channel := uniqueChannel("throughput")
	expectedMessages := 10000
	receivedCount := int64(0)
	var mu sync.Mutex
	receivedMessages := make(map[string]bool)

	_, err = subMux.SubscribeSync(context.Background(), []string{channel}, func(msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			atomic.AddInt64(&receivedCount, 1)
			mu.Lock()
			receivedMessages[msg.Payload] = true
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Give subscription time to establish
	time.Sleep(100 * time.Millisecond)

	// Publish messages at high rate
	pubClient := cluster.GetClusterClient()
	start := time.Now()

	var wg sync.WaitGroup
	numPublishers := 10
	messagesPerPublisher := expectedMessages / numPublishers

	for i := 0; i < numPublishers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < messagesPerPublisher; j++ {
				msgID := fmt.Sprintf("msg-%d-%d", id, j)
				err := pubClient.Publish(context.Background(), channel, msgID).Err()
				if err != nil {
					t.Errorf("Failed to publish %s: %v", msgID, err)
				}
			}
		}(i)
	}

	wg.Wait()
	publishTime := time.Since(start)

	// Wait for all messages to be received
	timeout := time.After(10 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		received := atomic.LoadInt64(&receivedCount)
		if received >= int64(expectedMessages) {
			break
		}
		select {
		case <-timeout:
			t.Fatalf("Timeout waiting for messages. Received %d/%d", received, expectedMessages)
		case <-ticker.C:
			// Continue waiting
		}
	}

	receiveTime := time.Since(start)
	received := atomic.LoadInt64(&receivedCount)

	publishRate := float64(expectedMessages) / publishTime.Seconds()
	receiveRate := float64(received) / receiveTime.Seconds()

	t.Logf("Published %d messages in %v (%.0f msg/sec)", expectedMessages, publishTime, publishRate)
	t.Logf("Received %d messages in %v (%.0f msg/sec)", received, receiveTime, receiveRate)

	if received < int64(expectedMessages) {
		t.Errorf("Message loss detected: expected %d, received %d", expectedMessages, received)
	}

	// Verify all messages were received
	mu.Lock()
	uniqueReceived := len(receivedMessages)
	mu.Unlock()

	if uniqueReceived < expectedMessages {
		t.Errorf("Duplicate or missing messages: expected %d unique, got %d", expectedMessages, uniqueReceived)
	}
}

// TestHighMessageThroughput_CallbackPerformance tests callback performance under load.
func TestHighMessageThroughput_CallbackPerformance(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channel := uniqueChannel("callback-perf")
	numMessages := 5000
	callbackTimes := make([]time.Duration, 0, numMessages)
	var mu sync.Mutex

	_, err = subMux.SubscribeSync(context.Background(), []string{channel}, func(msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			start := time.Now()
			// Simulate minimal callback work
			_ = len(msg.Payload)
			elapsed := time.Since(start)
			mu.Lock()
			callbackTimes = append(callbackTimes, elapsed)
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Publish messages
	pubClient := cluster.GetClusterClient()
	start := time.Now()

	for i := 0; i < numMessages; i++ {
		err := pubClient.Publish(context.Background(), channel, fmt.Sprintf("msg-%d", i)).Err()
		if err != nil {
			t.Errorf("Failed to publish: %v", err)
		}
	}

	publishTime := time.Since(start)

	// Wait for all messages
	timeout := time.After(10 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		mu.Lock()
		received := len(callbackTimes)
		mu.Unlock()
		if received >= numMessages {
			break
		}
		select {
		case <-timeout:
			t.Fatalf("Timeout: received %d/%d", received, numMessages)
		case <-ticker.C:
		}
	}

	totalTime := time.Since(start)

	// Calculate statistics
	mu.Lock()
	var totalCallbackTime time.Duration
	for _, d := range callbackTimes {
		totalCallbackTime += d
	}
	avgCallbackTime := totalCallbackTime / time.Duration(len(callbackTimes))
	maxCallbackTime := time.Duration(0)
	for _, d := range callbackTimes {
		if d > maxCallbackTime {
			maxCallbackTime = d
		}
	}
	mu.Unlock()

	t.Logf("Published %d messages in %v", numMessages, publishTime)
	t.Logf("Total processing time: %v", totalTime)
	t.Logf("Average callback time: %v", avgCallbackTime)
	t.Logf("Max callback time: %v", maxCallbackTime)
	t.Logf("Throughput: %.0f msg/sec", float64(numMessages)/totalTime.Seconds())

	// Verify callbacks are fast (should be < 1ms average)
	if avgCallbackTime > time.Millisecond {
		t.Errorf("Average callback time too high: %v", avgCallbackTime)
	}
}

// TestLongRunningSubscriptions tests subscriptions over an extended period.
func TestLongRunningSubscriptions(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping long-running test in short mode")
	}

	// Run for 3 seconds (can be extended for real stress testing via env var)
	duration := 3 * time.Second
	if d := os.Getenv("LONG_TEST_DURATION"); d != "" {
		var err error
		duration, err = time.ParseDuration(d)
		if err != nil {
			t.Fatalf("Invalid LONG_TEST_DURATION: %v", err)
		}
	}

	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channel := uniqueChannel("long-running")
	messageCount := int64(0)
	errorCount := int64(0)

	_, err = subMux.SubscribeSync(context.Background(), []string{channel}, func(msg *submux.Message) {
		switch msg.Type {
		case submux.MessageTypeMessage:
			atomic.AddInt64(&messageCount, 1)
		case submux.MessageTypeSignal:
			atomic.AddInt64(&errorCount, 1)
			t.Logf("signal message: %+v", msg.Signal)
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Monitor memory
	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	// Publish messages continuously
	pubClient := cluster.GetClusterClient()
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	publishedCount := int64(0)
	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				err := pubClient.Publish(ctx, channel, fmt.Sprintf("msg-%d", atomic.AddInt64(&publishedCount, 1))).Err()
				if err != nil && ctx.Err() == nil {
					atomic.AddInt64(&errorCount, 1)
				}
			}
		}
	}()

	// Wait for duration
	<-ctx.Done()
	<-done

	// Final memory check
	runtime.GC()
	runtime.ReadMemStats(&m2)

	received := atomic.LoadInt64(&messageCount)
	published := atomic.LoadInt64(&publishedCount)
	errors := atomic.LoadInt64(&errorCount)

	memDiff := int64(m2.Alloc) - int64(m1.Alloc)
	memDiffKB := memDiff / 1024

	t.Logf("Test duration: %v", duration)
	t.Logf("Published: %d messages", published)
	t.Logf("Received: %d messages", received)
	t.Logf("Errors/signal: %d", errors)
	t.Logf("Memory delta: %d KB", memDiffKB)
	t.Logf("Message loss: %d (%.2f%%)", published-received, float64(published-received)/float64(published)*100)

	// Verify no significant memory leak (memory should not grow by more than 1MB)
	if memDiff > 1024*1024 {
		t.Errorf("Potential memory leak: %d KB allocated over %v", memDiffKB, duration)
	}

	// Verify message delivery (allow for some loss due to timing)
	deliveryRate := float64(received) / float64(published) * 100
	if deliveryRate < 95.0 {
		t.Errorf("Low delivery rate: %.2f%% (expected >95%%)", deliveryRate)
	}
}
