package anthropic_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
)

// systemField runs one completion with the given system messages and returns
// the raw `system` field of the request (nil when absent).
func systemField(t *testing.T, model string, system []string, opts ...anthropic.Option) json.RawMessage {
	t.Helper()
	var captured atomic.Value
	srv := sseServer(t, &captured, endTurn()...)
	defer srv.Close()

	var msgs []llm.Message
	if len(system) > 0 {
		msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Text: system[0]})
	}
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Text: "Hallo Flo"})
	for _, tail := range system[min(1, len(system)):] {
		msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Text: tail})
	}

	c := anthropic.New("k", append([]anthropic.Option{anthropic.WithBaseURL(srv.URL)}, opts...)...)
	ch, err := c.Complete(context.Background(), llm.Request{Model: model, Messages: msgs})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	collect(t, ch)

	var body map[string]json.RawMessage
	if err := json.Unmarshal(captured.Load().([]byte), &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return body["system"]
}

// TestComplete_Haiku55_CachesTheStableSystemPrompt pins prompt caching for the
// voice model (#702): the session-constant system prompt (ADR-0059's stable
// prefix) is its own block carrying the cache breakpoint, so tools + stable
// prompt are read from the cache at a tenth of the input rate on every later
// request — the next tool round, the next turn. The volatile tail (facts,
// memory, a directive) follows as a second block WITHOUT a breakpoint, so its
// per-turn changes never invalidate the cached prefix.
func TestComplete_Haiku55_CachesTheStableSystemPrompt(t *testing.T) {
	got := systemField(t, "claude-haiku-5-5", []string{"You are Flo.", "## Facts\nGellan was here.", "## Directive\nBe brief."})
	want := `[{"type":"text","text":"You are Flo.","cache_control":{"type":"ephemeral"}},` +
		`{"type":"text","text":"## Facts\nGellan was here.\n\n## Directive\nBe brief."}]`
	if string(got) != want {
		t.Errorf("system\n got %s\nwant %s", got, want)
	}

	got = systemField(t, "claude-haiku-5-5", []string{"You are Flo."})
	if want := `[{"type":"text","text":"You are Flo.","cache_control":{"type":"ephemeral"}}]`; string(got) != want {
		t.Errorf("system with no volatile tail\n got %s\nwant %s", got, want)
	}

	if got := systemField(t, "claude-haiku-5-5", nil); got != nil {
		t.Errorf("system = %s for a request without a system prompt, want the field absent", got)
	}
}

// TestComplete_OtherModels_SystemStaysAPlainString pins that caching is gated
// to the model it was measured on: every other model keeps the joined system
// string it has today. (claude-haiku-4-5 only caches a prefix of 4096 tokens or
// more, which the voice prompt does not reach.)
func TestComplete_OtherModels_SystemStaysAPlainString(t *testing.T) {
	for _, model := range []string{"claude-haiku-4-5", "claude-opus-4-8", ""} {
		got := systemField(t, model, []string{"You are Flo.", "## Facts\nGellan was here."})
		if want := `"You are Flo.\n\n## Facts\nGellan was here."`; string(got) != want {
			t.Errorf("model %q: system = %s, want %s", model, got, want)
		}
	}
}

// TestWithPromptCachingFor_OverridesTheGate pins the per-model switch.
func TestWithPromptCachingFor_OverridesTheGate(t *testing.T) {
	off := anthropic.WithPromptCachingFor(func(string) bool { return false })
	if got := systemField(t, "claude-haiku-5-5", []string{"You are Flo."}, off); string(got) != `"You are Flo."` {
		t.Errorf("system = %s with caching switched off, want the plain string", got)
	}
}
