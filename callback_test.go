package submux

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

func TestSubscriptionType_String_AllTypes(t *testing.T) {
	tests := []struct {
		subType  subscriptionType
		expected string
	}{
		{subTypeSubscribe, "subscribe"},
		{subTypePSubscribe, "psubscribe"},
		{subTypeSSubscribe, "ssubscribe"},
		{subscriptionType(100), "unknown"}, // Unknown type
	}

	for _, tt := range tests {
		result := tt.subType.String()
		if result != tt.expected {
			t.Errorf("subscriptionType(%d).String() = %q, want %q", tt.subType, result, tt.expected)
		}
	}
}

func TestExecuteCallback_NilMessage(t *testing.T) {
	logger := slog.Default()

	var calledWithNil bool
	callback := func(ctx context.Context, msg *Message) {
		calledWithNil = (msg == nil)
	}

	// Should not panic with nil message
	executeCallback(context.Background(), logger, &noopMetrics{}, callback, nil)

	if !calledWithNil {
		t.Error("callback should have been called with nil message")
	}
}

func TestExecuteCallback_PanicLogFields(t *testing.T) {
	// Verify that panic recovery logs enriched context fields.
	var records []slog.Record
	var mu sync.Mutex
	handler := &captureHandler{
		fn: func(r slog.Record) {
			mu.Lock()
			records = append(records, r)
			mu.Unlock()
		},
	}
	logger := slog.New(handler)

	tests := []struct {
		name           string
		msg            *Message
		expectChannel  string
		expectPattern  string
		expectSubType  string
		patternPresent bool
	}{
		{
			name: "subscribe with channel",
			msg: &Message{
				Type:             MessageTypeMessage,
				Channel:          "news:sports",
				SubscriptionType: subTypeSubscribe,
			},
			expectChannel:  "news:sports",
			expectSubType:  "subscribe",
			patternPresent: false,
		},
		{
			name: "psubscribe with pattern",
			msg: &Message{
				Type:             MessageTypePMessage,
				Channel:          "news:sports",
				Pattern:          "news:*",
				SubscriptionType: subTypePSubscribe,
			},
			expectChannel:  "news:sports",
			expectPattern:  "news:*",
			expectSubType:  "psubscribe",
			patternPresent: true,
		},
		{
			name:           "nil message",
			msg:            nil,
			expectSubType:  "unknown",
			patternPresent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mu.Lock()
			records = records[:0]
			mu.Unlock()

			panicCallback := func(ctx context.Context, msg *Message) {
				panic("test panic")
			}

			executeCallback(context.Background(), logger, &noopMetrics{}, panicCallback, tt.msg)

			mu.Lock()
			defer mu.Unlock()

			if len(records) != 1 {
				t.Fatalf("expected 1 log record, got %d", len(records))
			}

			r := records[0]
			attrs := make(map[string]any)
			r.Attrs(func(a slog.Attr) bool {
				attrs[a.Key] = a.Value.Any()
				return true
			})

			// Check error field
			if _, ok := attrs["error"]; !ok {
				t.Error("missing 'error' attribute")
			}

			// Check subscription_type
			if v, ok := attrs["subscription_type"]; !ok {
				t.Error("missing 'subscription_type' attribute")
			} else if v != tt.expectSubType {
				t.Errorf("subscription_type = %q, want %q", v, tt.expectSubType)
			}

			// Check channel
			if tt.msg != nil {
				if v, ok := attrs["channel"]; !ok {
					t.Error("missing 'channel' attribute")
				} else if v != tt.expectChannel {
					t.Errorf("channel = %q, want %q", v, tt.expectChannel)
				}
			} else {
				if _, ok := attrs["channel"]; ok {
					t.Error("'channel' attribute should not be present for nil message")
				}
			}

			// Check pattern
			if tt.patternPresent {
				if v, ok := attrs["pattern"]; !ok {
					t.Error("missing 'pattern' attribute")
				} else if v != tt.expectPattern {
					t.Errorf("pattern = %q, want %q", v, tt.expectPattern)
				}
			} else if tt.msg != nil {
				if _, ok := attrs["pattern"]; ok {
					t.Error("'pattern' attribute should not be present when pattern is empty")
				}
			}

			// Check stack
			if _, ok := attrs["stack"]; !ok {
				t.Error("missing 'stack' attribute")
			}
		})
	}
}

// captureHandler is a slog.Handler that captures log records for testing.
type captureHandler struct {
	fn func(slog.Record)
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.fn(r)
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func TestExecuteCallback_NilLogger_PanicRecovery(t *testing.T) {
	// Verify that a nil logger does not cause a secondary panic
	// when the callback panics.
	recorder := &noopMetrics{}
	msg := &Message{
		Type:             MessageTypeMessage,
		Channel:          "test",
		Payload:          "data",
		SubscriptionType: subTypeSubscribe,
	}

	panickingCallback := func(ctx context.Context, msg *Message) {
		panic("intentional test panic")
	}

	// This must not panic (the nil logger should be handled gracefully)
	executeCallback(context.Background(), nil, recorder, panickingCallback, msg)
}
