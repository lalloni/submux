package testutil

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// PublishToChannel publishes a message to a channel using the cluster client.
func PublishToChannel(client *redis.ClusterClient, channel, message string) error {
	return client.Publish(context.Background(), channel, message).Err()
}
