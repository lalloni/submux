package submux

import (
	"testing"
	"time"
)

func TestMessageType_Values(t *testing.T) {
	// Verify message type constants have expected values
	tests := []struct {
		name     string
		msgType  MessageType
		expected int
	}{
		{"MessageTypeMessage", MessageTypeMessage, 0},
		{"MessageTypePMessage", MessageTypePMessage, 1},
		{"MessageTypeSMessage", MessageTypeSMessage, 2},
		{"MessageTypeSignal", MessageTypeSignal, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if int(tt.msgType) != tt.expected {
				t.Errorf("%s = %d, want %d", tt.name, tt.msgType, tt.expected)
			}
		})
	}
}

func TestMessage_EmptyPayload(t *testing.T) {
	msg := &Message{
		Type:    MessageTypeMessage,
		Channel: "test-channel",
		Payload: "",
	}

	if msg.Payload != "" {
		t.Errorf("Payload = %q, want empty string", msg.Payload)
	}
}

func TestMessage_LargePayload(t *testing.T) {
	// Test with a large payload (1MB)
	largePayload := make([]byte, 1024*1024)
	for i := range largePayload {
		largePayload[i] = 'a'
	}

	msg := &Message{
		Type:    MessageTypeMessage,
		Channel: "test-channel",
		Payload: string(largePayload),
	}

	if len(msg.Payload) != 1024*1024 {
		t.Errorf("Payload length = %d, want %d", len(msg.Payload), 1024*1024)
	}
}

func TestMessage_BinaryPayload(t *testing.T) {
	// Test with binary data including null bytes
	binaryPayload := string([]byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0x00})

	msg := &Message{
		Type:    MessageTypeMessage,
		Channel: "test-channel",
		Payload: binaryPayload,
	}

	if len(msg.Payload) != 6 {
		t.Errorf("Binary payload length = %d, want 6", len(msg.Payload))
	}
}

func TestMessage_UnicodePayload(t *testing.T) {
	// Test with unicode characters including emojis
	unicodePayload := "Hello 世界 🎉 مرحبا"

	msg := &Message{
		Type:    MessageTypeMessage,
		Channel: "test-channel",
		Payload: unicodePayload,
	}

	if msg.Payload != unicodePayload {
		t.Errorf("Unicode payload = %q, want %q", msg.Payload, unicodePayload)
	}
}

func TestMessage_EmptyChannel(t *testing.T) {
	msg := &Message{
		Type:    MessageTypeMessage,
		Channel: "",
		Payload: "test",
	}

	if msg.Channel != "" {
		t.Errorf("Channel = %q, want empty string", msg.Channel)
	}
}

func TestMessage_SpecialCharacterChannel(t *testing.T) {
	channels := []string{
		"channel:with:colons",
		"channel.with.dots",
		"channel-with-dashes",
		"channel_with_underscores",
		"channel{with}braces",
		"channel[with]brackets",
		"channel/with/slashes",
		"channel with spaces",
		"",
	}

	for _, ch := range channels {
		t.Run("channel_"+ch, func(t *testing.T) {
			msg := &Message{
				Type:    MessageTypeMessage,
				Channel: ch,
				Payload: "test",
			}

			if msg.Channel != ch {
				t.Errorf("Channel = %q, want %q", msg.Channel, ch)
			}
		})
	}
}

func TestMessage_PatternMessage(t *testing.T) {
	msg := &Message{
		Type:    MessageTypePMessage,
		Channel: "news.sports.football",
		Pattern: "news.sports.*",
		Payload: "goal scored",
	}

	if msg.Type != MessageTypePMessage {
		t.Errorf("Type = %v, want MessageTypePMessage", msg.Type)
	}
	if msg.Pattern != "news.sports.*" {
		t.Errorf("Pattern = %q, want %q", msg.Pattern, "news.sports.*")
	}
	if msg.Channel != "news.sports.football" {
		t.Errorf("Channel = %q, want %q", msg.Channel, "news.sports.football")
	}
}

func TestMessage_ShardedMessage(t *testing.T) {
	msg := &Message{
		Type:    MessageTypeSMessage,
		Channel: "{user:123}:notifications",
		Payload: "new message",
	}

	if msg.Type != MessageTypeSMessage {
		t.Errorf("Type = %v, want MessageTypeSMessage", msg.Type)
	}
}

func TestMessage_SignalMessage(t *testing.T) {
	msg := &Message{
		Type: MessageTypeSignal,
		Signal: &SignalInfo{
			EventType: EventMigration,
			Hashslot:  1234,
			OldNode:   "127.0.0.1:7000",
			NewNode:   "127.0.0.1:7001",
			Details:   "hashslot migration",
		},
	}

	if msg.Type != MessageTypeSignal {
		t.Errorf("Type = %v, want MessageTypeSignal", msg.Type)
	}
	if msg.Signal == nil {
		t.Fatal("Signal should not be nil")
	}
	if msg.Signal.EventType != EventMigration {
		t.Errorf("EventType = %q, want %q", msg.Signal.EventType, EventMigration)
	}
	if msg.Signal.Hashslot != 1234 {
		t.Errorf("Hashslot = %d, want 1234", msg.Signal.Hashslot)
	}
}

func TestMessage_SignalMessageNilSignalInfo(t *testing.T) {
	// Signal message with nil SignalInfo is technically valid (edge case)
	msg := &Message{
		Type:   MessageTypeSignal,
		Signal: nil,
	}

	if msg.Type != MessageTypeSignal {
		t.Errorf("Type = %v, want MessageTypeSignal", msg.Type)
	}
	if msg.Signal != nil {
		t.Error("Signal should be nil")
	}
}

func TestMessage_Timestamp(t *testing.T) {
	now := time.Now()
	msg := &Message{
		Type:      MessageTypeMessage,
		Channel:   "test",
		Payload:   "test",
		Timestamp: now,
	}

	if msg.Timestamp != now {
		t.Errorf("Timestamp = %v, want %v", msg.Timestamp, now)
	}
}

func TestMessage_ZeroTimestamp(t *testing.T) {
	msg := &Message{
		Type:    MessageTypeMessage,
		Channel: "test",
		Payload: "test",
	}

	if !msg.Timestamp.IsZero() {
		t.Errorf("Timestamp should be zero, got %v", msg.Timestamp)
	}
}

func TestSignalInfo_AllEventTypes(t *testing.T) {
	eventTypes := []EventType{
		EventNodeFailure,
		EventMigration,
		EventMigrationTimeout,
		EventMigrationStalled,
	}

	for _, et := range eventTypes {
		t.Run(string(et), func(t *testing.T) {
			sig := &SignalInfo{
				EventType: et,
			}

			if sig.EventType != et {
				t.Errorf("EventType = %q, want %q", sig.EventType, et)
			}
		})
	}
}

func TestSignalInfo_HashslotBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		hashslot int
	}{
		{"minimum", 0},
		{"maximum", 16383},
		{"middle", 8192},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sig := &SignalInfo{
				EventType: EventMigration,
				Hashslot:  tt.hashslot,
			}

			if sig.Hashslot != tt.hashslot {
				t.Errorf("Hashslot = %d, want %d", sig.Hashslot, tt.hashslot)
			}
		})
	}
}

func TestSignalInfo_EmptyNodes(t *testing.T) {
	// Test signal with empty node addresses (e.g., new node appears)
	sig := &SignalInfo{
		EventType: EventMigration,
		Hashslot:  100,
		OldNode:   "",
		NewNode:   "127.0.0.1:7000",
	}

	if sig.OldNode != "" {
		t.Errorf("OldNode = %q, want empty", sig.OldNode)
	}

	// Test signal with empty new node (node disappears)
	sig2 := &SignalInfo{
		EventType: EventNodeFailure,
		Hashslot:  100,
		OldNode:   "127.0.0.1:7000",
		NewNode:   "",
	}

	if sig2.NewNode != "" {
		t.Errorf("NewNode = %q, want empty", sig2.NewNode)
	}
}

func TestSub_NilFields(t *testing.T) {
	// Sub with nil fields (edge case)
	sub := &Sub{
		subMux: nil,
		subs:   nil,
	}

	if sub.subMux != nil {
		t.Error("subMux should be nil")
	}
	if sub.subs != nil {
		t.Error("subs should be nil")
	}
}

func TestSub_EmptySubs(t *testing.T) {
	// Sub with empty subs slice
	sub := &Sub{
		subMux: nil,
		subs:   []*subscription{},
	}

	if len(sub.subs) != 0 {
		t.Errorf("subs length = %d, want 0", len(sub.subs))
	}
}
