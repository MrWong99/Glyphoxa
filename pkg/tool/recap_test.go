package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakeRecapper is a scripted Recapper: it records the session count it was handed
// and replays a fixed recap text/error, so the handler's clamping and error
// wrapping are pinned without a recap engine or DB.
type fakeRecapper struct {
	text        string
	err         error
	gotSessions int
	callCount   int
}

func (f *fakeRecapper) RecapLastSessions(_ context.Context, sessions int) (string, error) {
	f.callCount++
	f.gotSessions = sessions
	return f.text, f.err
}

// TestRecapToolContract pins the Tool metadata: name, read-only (inline, ADR-0030),
// no per-grant scope, and a schema that round-trips.
func TestRecapToolContract(t *testing.T) {
	tl := NewRecap(&fakeRecapper{})
	if tl.Name() != "recap" {
		t.Errorf("Name = %q, want recap", tl.Name())
	}
	if !tl.ReadOnly() {
		t.Error("recap must be read-only (ADR-0030 inline execution)")
	}
	if tl.SupportsScope() {
		t.Error("recap carries no per-grant scope (campaign implicit via active session)")
	}
	var schema map[string]any
	if err := json.Unmarshal(tl.InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema does not round-trip: %v", err)
	}
	if _, ok := schema["required"]; ok {
		t.Error("recap schema must declare no required fields")
	}
}

// TestRecapToolNilSource pins that with a nil source the Tool errors "unavailable"
// (fed back to the model) rather than panicking — the zero-Deps bench/RPC path.
func TestRecapToolNilSource(t *testing.T) {
	out, err := NewRecap(nil).Execute(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatalf("nil source should error, got %q", out)
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("nil-source error = %q, want it to mention unavailable", err)
	}
}

// TestRecapToolDefaultsAndClampsSessions pins the advisory-schema clamp (ADR-0029):
// an absent/zero count defaults to 1, an oversized count clamps to MaxRecapSessions.
func TestRecapToolDefaultsAndClampsSessions(t *testing.T) {
	cases := []struct {
		name string
		args string
		want int
	}{
		{"absent", `{}`, DefaultRecapSessions},
		{"zero", `{"sessions":0}`, DefaultRecapSessions},
		{"oversized", `{"sessions":99}`, MaxRecapSessions},
		{"in range", `{"sessions":2}`, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeRecapper{text: "ok"}
			if _, err := NewRecap(src).Execute(context.Background(), json.RawMessage(tc.args), nil); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if src.gotSessions != tc.want {
				t.Errorf("sessions → %d, want %d", src.gotSessions, tc.want)
			}
		})
	}
}

// TestRecapToolInvalidArgs pins that malformed JSON errors and never calls the source.
func TestRecapToolInvalidArgs(t *testing.T) {
	src := &fakeRecapper{}
	if _, err := NewRecap(src).Execute(context.Background(), json.RawMessage(`{"sessions":`), nil); err == nil {
		t.Fatal("invalid json should error")
	}
	if src.callCount != 0 {
		t.Errorf("source called %d times on bad args, want 0", src.callCount)
	}
}

// TestRecapToolCancelledCtx pins that a cancelled turn ctx short-circuits before
// the source is touched, returning ctx.Err.
func TestRecapToolCancelledCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := &fakeRecapper{}
	_, err := NewRecap(src).Execute(ctx, json.RawMessage(`{}`), nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if src.callCount != 0 {
		t.Errorf("source called on a cancelled ctx")
	}
}

// TestRecapToolWrapsSourceError pins the "recap: …" wrap so a failure is
// attributable in the tool-role message fed back to the model.
func TestRecapToolWrapsSourceError(t *testing.T) {
	src := &fakeRecapper{err: errors.New("boom")}
	_, err := NewRecap(src).Execute(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "recap: ") {
		t.Errorf("err = %v, want it wrapped with recap: prefix", err)
	}
}

// TestRecapToolTruncatesToBudget pins that an over-budget recap is rune-truncated
// with an ellipsis, multibyte-safe (never a split codepoint).
func TestRecapToolTruncatesToBudget(t *testing.T) {
	long := strings.Repeat("我", RecapResultBudgetRunes+500) // 3 bytes each, over budget
	out, err := NewRecap(&fakeRecapper{text: long}).Execute(
		context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(out) > RecapResultBudgetRunes+1 { // +1 for the ellipsis
		t.Errorf("recap %d runes exceeds budget %d", utf8.RuneCountInString(out), RecapResultBudgetRunes)
	}
	if !strings.HasSuffix(out, "…") {
		t.Error("a truncated recap should end in an ellipsis")
	}
	if !utf8.ValidString(out) {
		t.Error("truncation split a multibyte codepoint")
	}
}

// TestRecapToolReturnsTextVerbatim pins that an under-budget recap is relayed
// verbatim — the Description tells the model not to shorten it, so the Tool must not
// mangle it either.
func TestRecapToolReturnsTextVerbatim(t *testing.T) {
	const prose = "The party stormed the keep and freed the Duke."
	out, err := NewRecap(&fakeRecapper{text: prose}).Execute(
		context.Background(), json.RawMessage(`{"sessions":1}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != prose {
		t.Errorf("recap = %q, want verbatim %q", out, prose)
	}
}
