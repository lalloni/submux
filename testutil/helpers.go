package testutil

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// WaitForClusterReady waits for a cluster client to be ready by pinging it.
func WaitForClusterReady(client *redis.ClusterClient, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		err := client.Ping(ctx).Err()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// PublishToChannel publishes a message to a channel using the cluster client.
func PublishToChannel(client *redis.ClusterClient, channel, message string) error {
	return client.Publish(context.Background(), channel, message).Err()
}
