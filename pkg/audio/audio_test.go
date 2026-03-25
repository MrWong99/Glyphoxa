package audio

import (
	"testing"
)

func TestDrain_EmptyChannel(t *testing.T) {
	t.Parallel()

	ch := make(chan int)
	close(ch)

	// Should not block.
	Drain(ch)
}

func TestDrain_WithValues(t *testing.T) {
	t.Parallel()

	ch := make(chan string, 3)
	ch <- "a"
	ch <- "b"
	ch <- "c"
	close(ch)

	Drain(ch)
}

func TestDrain_ByteSlice(t *testing.T) {
	t.Parallel()

	ch := make(chan []byte, 2)
	ch <- []byte{1, 2, 3}
	ch <- []byte{4, 5, 6}
	close(ch)

	Drain(ch)
}

func TestEventType_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		et   EventType
		want string
	}{
		{"join", EventJoin, "JOIN"},
		{"leave", EventLeave, "LEAVE"},
		{"unknown", EventType(99), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.et.String(); got != tt.want {
				t.Errorf("EventType(%d).String() = %q, want %q", tt.et, got, tt.want)
			}
		})
	}
}
