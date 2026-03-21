package discord

import (
	"testing"

	"github.com/MrWong99/glyphoxa/pkg/audio"
)

// ─── compile-time interface assertions ───────────────────────────────────────

var _ audio.Platform = (*VoiceOnlyPlatform)(nil)

// ─── VoiceOnlyPlatform tests ─────────────────────────────────────────────────

// TestNewVoiceOnlyPlatform_InvalidGuildID verifies that an unparseable guild ID
// returns an error without attempting a Discord connection.
func TestNewVoiceOnlyPlatform_InvalidGuildID(t *testing.T) {
	t.Parallel()

	_, err := NewVoiceOnlyPlatform(t.Context(), "fake-token", "not-a-snowflake")
	if err == nil {
		t.Fatal("expected error for invalid guild ID, got nil")
	}
}

// TestVoiceOnlyPlatform_CloseNil verifies that Close on a zero-value
// VoiceOnlyPlatform does not panic.
func TestVoiceOnlyPlatform_CloseNil(t *testing.T) {
	t.Parallel()

	p := &VoiceOnlyPlatform{}
	// Close should not panic even with nil client.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Close panicked: %v", r)
		}
	}()
	_ = p.Close()
}
