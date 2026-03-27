package gateway

import (
	"testing"
)

func TestSessionState_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state SessionState
		want  string
	}{
		{"pending", SessionPending, "pending"},
		{"active", SessionActive, "active"},
		{"ended", SessionEnded, "ended"},
		{"unknown value", SessionState(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.state.String(); got != tt.want {
				t.Errorf("SessionState(%d).String() = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestParseSessionState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		want   SessionState
		wantOK bool
	}{
		{"pending", "pending", SessionPending, true},
		{"active", "active", SessionActive, true},
		{"ended", "ended", SessionEnded, true},
		{"empty string", "", 0, false},
		{"unknown", "running", 0, false},
		{"uppercase", "PENDING", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseSessionState(tt.input)
			if ok != tt.wantOK {
				t.Errorf("ParseSessionState(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("ParseSessionState(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidTransition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		from SessionState
		to   SessionState
		want bool
	}{
		{"pending→active", SessionPending, SessionActive, true},
		{"pending→ended", SessionPending, SessionEnded, true},
		{"active→ended", SessionActive, SessionEnded, true},
		{"ended→active", SessionEnded, SessionActive, false},
		{"ended→pending", SessionEnded, SessionPending, false},
		{"active→pending", SessionActive, SessionPending, false},
		{"pending→pending", SessionPending, SessionPending, false},
		{"ended→ended", SessionEnded, SessionEnded, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidTransition(tt.from, tt.to); got != tt.want {
				t.Errorf("ValidTransition(%v, %v) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}
