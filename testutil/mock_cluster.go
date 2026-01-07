package testutil

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// MockClusterClient provides a mock implementation of ClusterClient for testing.
type MockClusterClient struct {
	SubscribeFunc    func(ctx context.Context) *redis.PubSub
	PSubscribeFunc   func(ctx context.Context) *redis.PubSub
	SSubscribeFunc   func(ctx context.Context) *redis.PubSub
	ClusterSlotsFunc func(ctx context.Context) *redis.ClusterSlotsCmd
	PingFunc         func(ctx context.Context) *redis.StatusCmd
}

// Subscribe calls SubscribeFunc if set, otherwise returns nil.
func (m *MockClusterClient) Subscribe(ctx context.Context, channels ...string) *redis.PubSub {
	if m.SubscribeFunc != nil {
		return m.SubscribeFunc(ctx)
	}
	return nil
}

// PSubscribe calls PSubscribeFunc if set, otherwise returns nil.
func (m *MockClusterClient) PSubscribe(ctx context.Context, patterns ...string) *redis.PubSub {
	if m.PSubscribeFunc != nil {
		return m.PSubscribeFunc(ctx)
	}
	return nil
}

// SSubscribe calls SSubscribeFunc if set, otherwise returns nil.
func (m *MockClusterClient) SSubscribe(ctx context.Context, channels ...string) *redis.PubSub {
	if m.SSubscribeFunc != nil {
		return m.SSubscribeFunc(ctx)
	}
	return nil
}

// ClusterSlots calls ClusterSlotsFunc if set, otherwise returns a command that errors.
func (m *MockClusterClient) ClusterSlots(ctx context.Context) *redis.ClusterSlotsCmd {
	if m.ClusterSlotsFunc != nil {
		return m.ClusterSlotsFunc(ctx)
	}
	cmd := redis.NewClusterSlotsCmd(ctx)
	cmd.SetErr(redis.Nil)
	return cmd
}

// Ping calls PingFunc if set, otherwise returns a command that errors.
func (m *MockClusterClient) Ping(ctx context.Context) *redis.StatusCmd {
	if m.PingFunc != nil {
		return m.PingFunc(ctx)
	}
	cmd := redis.NewStatusCmd(ctx)
	cmd.SetErr(redis.Nil)
	return cmd
}

// MockPubSub provides a mock implementation of PubSub for testing.
type MockPubSub struct {
	ChannelFunc    func() <-chan *redis.Message
	SubscribeFunc  func(ctx context.Context, channels ...string) error
	PSubscribeFunc func(ctx context.Context, patterns ...string) error
	SSubscribeFunc func(ctx context.Context, channels ...string) error
	CloseFunc      func() error
}

// Channel returns the channel from ChannelFunc if set, otherwise returns nil.
func (m *MockPubSub) Channel() <-chan *redis.Message {
	if m.ChannelFunc != nil {
		return m.ChannelFunc()
	}
	return nil
}

// Subscribe calls SubscribeFunc if set, otherwise returns nil.
func (m *MockPubSub) Subscribe(ctx context.Context, channels ...string) error {
	if m.SubscribeFunc != nil {
		return m.SubscribeFunc(ctx, channels...)
	}
	return nil
}

// PSubscribe calls PSubscribeFunc if set, otherwise returns nil.
func (m *MockPubSub) PSubscribe(ctx context.Context, patterns ...string) error {
	if m.PSubscribeFunc != nil {
		return m.PSubscribeFunc(ctx, patterns...)
	}
	return nil
}

// SSubscribe calls SSubscribeFunc if set, otherwise returns nil.
func (m *MockPubSub) SSubscribe(ctx context.Context, channels ...string) error {
	if m.SSubscribeFunc != nil {
		return m.SSubscribeFunc(ctx, channels...)
	}
	return nil
}

// Close calls CloseFunc if set, otherwise returns nil.
func (m *MockPubSub) Close() error {
	if m.CloseFunc != nil {
		return m.CloseFunc()
	}
	return nil
}
