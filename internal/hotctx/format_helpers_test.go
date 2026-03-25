package hotctx

import (
	"testing"
	"time"
)

func TestFormatRelativeTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"negative becomes just now", -5 * time.Second, "just now"},
		{"zero is just now", 0, "just now"},
		{"1 second is just now", 1 * time.Second, "just now"},
		{"4 seconds is just now", 4 * time.Second, "just now"},
		{"5 seconds shows seconds", 5 * time.Second, "5s ago"},
		{"30 seconds", 30 * time.Second, "30s ago"},
		{"59 seconds", 59 * time.Second, "59s ago"},
		{"60 seconds shows minutes", 60 * time.Second, "1m ago"},
		{"90 seconds shows minutes", 90 * time.Second, "1m ago"},
		{"5 minutes", 5 * time.Minute, "5m ago"},
		{"59 minutes", 59 * time.Minute, "59m ago"},
		{"1 hour", 1 * time.Hour, "1h ago"},
		{"2 hours", 2 * time.Hour, "2h ago"},
		{"24 hours", 24 * time.Hour, "24h ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatRelativeTime(tt.d)
			if got != tt.want {
				t.Errorf("formatRelativeTime(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}
