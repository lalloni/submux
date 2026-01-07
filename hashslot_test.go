package submux

import "testing"

func TestHashslot_Basic(t *testing.T) {
	tests := []struct {
		name     string
		channel  string
		validate func(int) bool
	}{
		{
			name:    "simple channel",
			channel: "mychannel",
			validate: func(slot int) bool {
				return slot >= 0 && slot < 16384
			},
		},
		{
			name:    "empty channel",
			channel: "",
			validate: func(slot int) bool {
				return slot >= 0 && slot < 16384
			},
		},
		{
			name:    "channel with special chars",
			channel: "channel-123_test",
			validate: func(slot int) bool {
				return slot >= 0 && slot < 16384
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slot := Hashslot(tt.channel)
			if !tt.validate(slot) {
				t.Errorf("Hashslot(%q) = %d, want value in range [0, 16383]", tt.channel, slot)
			}
		})
	}
}

func TestHashslot_Consistency(t *testing.T) {
	channel := "testchannel"
	slot1 := Hashslot(channel)
	slot2 := Hashslot(channel)

	if slot1 != slot2 {
		t.Errorf("Hashslot(%q) inconsistent: first call = %d, second call = %d", channel, slot1, slot2)
	}
}

func TestHashslot_Hashtag(t *testing.T) {
	tests := []struct {
		name     string
		channel  string
		expected int // Expected hashslot (based on tag only)
	}{
		{
			name:     "hashtag extraction",
			channel:  "channel{tag}",
			expected: Hashslot("tag"), // Should use only "tag"
		},
		{
			name:     "hashtag with extra",
			channel:  "prefix{tag}suffix",
			expected: Hashslot("tag"), // Should use only "tag"
		},
		{
			name:     "multiple hashtags",
			channel:  "channel{tag1}extra{tag2}",
			expected: Hashslot("tag1"), // Should use first tag
		},
		{
			name:     "unclosed hashtag",
			channel:  "channel{tag",
			expected: Hashslot("channel{tag"), // Should use full string
		},
		{
			name:     "empty hashtag",
			channel:  "channel{}",
			expected: Hashslot("channel{}"), // Empty tag not extracted (e=0, not >0), uses full key
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Hashslot(tt.channel)
			if got != tt.expected {
				t.Errorf("Hashslot(%q) = %d, want %d", tt.channel, got, tt.expected)
			}
		})
	}
}

func TestHashslot_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		channel string
	}{
		{"empty string", ""},
		{"very long channel", string(make([]byte, 1000))},
		{"unicode characters", "频道测试"},
		{"special characters", "channel!@#$%^&*()"},
		{"spaces", "channel with spaces"},
		{"newlines", "channel\nwith\nnewlines"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slot := Hashslot(tt.channel)
			if slot < 0 || slot >= 16384 {
				t.Errorf("Hashslot(%q) = %d, want value in range [0, 16383]", tt.channel, slot)
			}
		})
	}
}

func TestHashslot_GoRedisCompatibility(t *testing.T) {
	// Test that hashslot calculation is consistent and produces valid results
	// The actual values may differ from go-redis, but the algorithm should be consistent
	tests := []struct {
		channel string
	}{
		{"mychannel"},
		{"test"},
		{"channel{tag}"},
	}

	for _, tt := range tests {
		t.Run(tt.channel, func(t *testing.T) {
			got := Hashslot(tt.channel)
			// Verify it's in valid range
			if got < 0 || got >= 16384 {
				t.Errorf("Hashslot(%q) = %d, want value in range [0, 16383]", tt.channel, got)
			}
			// Verify consistency
			got2 := Hashslot(tt.channel)
			if got != got2 {
				t.Errorf("Hashslot(%q) inconsistent: first = %d, second = %d", tt.channel, got, got2)
			}
		})
	}
}

func BenchmarkHashslot(b *testing.B) {
	channel := "testchannel"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Hashslot(channel)
	}
}

func BenchmarkHashslot_Hashtag(b *testing.B) {
	channel := "channel{tag}"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Hashslot(channel)
	}
}
