package anthropic_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
)

// TestChatThinking pins the Planning chat's thinking policy (ADR-0062 amendment
// 2026-11-22): adaptive at effort medium for every family that accepts both
// fields, nothing for claude-haiku-4-5 (400 on both) and for ids the adapter
// does not know.
func TestChatThinking(t *testing.T) {
	t.Parallel()
	medium := anthropic.Thinking{Mode: anthropic.ThinkingAdaptive, Effort: "medium"}
	for model, want := range map[string]anthropic.Thinking{
		"claude-haiku-5-5":          medium,
		"claude-sonnet-5-5":         medium,
		"claude-opus-5-5":           medium,
		"claude-sonnet-5":           medium,
		"claude-opus-5":             medium,
		"claude-opus-4-8":           medium,
		"claude-opus-4-7":           medium,
		"claude-opus-4-6":           medium,
		"claude-sonnet-4-6":         medium,
		"claude-fable-5-2":          medium,
		"claude-mythos-5-2":         medium,
		"claude-haiku-4-5":          {},
		"claude-haiku-4-5-20251001": {},
		"claude-opus-4-5":           {},
		"some-future-id":            {},
		"":                          {},
	} {
		if got := anthropic.ChatThinking(model); got != want {
			t.Errorf("ChatThinking(%q) = %+v, want %+v", model, got, want)
		}
	}
}

// The chat default is one of the current models and thinks under the chat
// policy; a default the policy does not name would run at the API's own depth.
func TestChatModel_IsCoveredByChatThinking(t *testing.T) {
	t.Parallel()
	if anthropic.ChatModel != "claude-sonnet-5-5" {
		t.Errorf("ChatModel = %q, want claude-sonnet-5-5", anthropic.ChatModel)
	}
	if anthropic.ChatThinking(anthropic.ChatModel).Mode != anthropic.ThinkingAdaptive {
		t.Errorf("ChatThinking(%q) is not adaptive", anthropic.ChatModel)
	}
}

// TestComplete_ChatThinkingOnTheWire: a client built with the chat policy asks
// claude-sonnet-5-5 for adaptive thinking at effort medium, and sends neither
// field to claude-haiku-4-5.
func TestComplete_ChatThinkingOnTheWire(t *testing.T) {
	chat := anthropic.WithThinkingFor(anthropic.ChatThinking)

	body := requestBody(t, "claude-sonnet-5-5", chat)
	if got := string(body["thinking"]); got != `{"type":"adaptive"}` {
		t.Errorf("thinking = %s, want {\"type\":\"adaptive\"}", got)
	}
	if got := string(body["output_config"]); got != `{"effort":"medium"}` {
		t.Errorf("output_config = %s, want {\"effort\":\"medium\"}", got)
	}

	body = requestBody(t, "claude-haiku-4-5", chat)
	for _, field := range []string{"thinking", "output_config"} {
		if raw, ok := body[field]; ok {
			t.Errorf("claude-haiku-4-5 carries %s = %s, want the field absent", field, raw)
		}
	}
}

// The chat policy is opt-in: a client built without it keeps the voice policy,
// so a Voice Session on claude-sonnet-5-5 sends no thinking field at all and a
// Haiku 5.5 one keeps effort low.
func TestComplete_VoicePolicyUntouchedByChatThinking(t *testing.T) {
	body := requestBody(t, "claude-sonnet-5-5")
	for _, field := range []string{"thinking", "output_config"} {
		if raw, ok := body[field]; ok {
			t.Errorf("voice request on claude-sonnet-5-5 carries %s = %s", field, raw)
		}
	}
	body = requestBody(t, "claude-haiku-5-5")
	if got := string(body["output_config"]); got != `{"effort":"low"}` {
		t.Errorf("voice output_config on claude-haiku-5-5 = %s, want {\"effort\":\"low\"}", got)
	}
}

// toolChoiceOnWire runs one tool-declaring completion for model and returns the
// tool_choice the request carried.
func toolChoiceOnWire(t *testing.T, c func(baseURL string) *anthropic.Client, model string, tc llm.ToolChoice) (typ, name string) {
	t.Helper()
	var capture atomic.Value
	srv := sseServer(t, &capture, endTurn()...)
	defer srv.Close()
	ch, err := c(srv.URL).Complete(context.Background(), llm.Request{
		Model:      model,
		MaxTokens:  128,
		Messages:   []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20."}},
		Tools:      []llm.ToolDef{{Name: "dice", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: tc,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	collect(t, ch)
	var body struct {
		ToolChoice struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tool_choice"`
	}
	if err := json.Unmarshal(capture.Load().([]byte), &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return body.ToolChoice.Type, body.ToolChoice.Name
}

// TestComplete_ForcedToolChoiceDowngradedWhereRejected: Sonnet 5.5, Opus 5.5 and
// Fable 5.2 answer 400 to a forced tool_choice, so the adapter sends auto; a
// model that accepts it keeps the forced choice, and none/auto pass untouched.
func TestComplete_ForcedToolChoiceDowngradedWhereRejected(t *testing.T) {
	plain := func(baseURL string) *anthropic.Client {
		return anthropic.New("k", anthropic.WithBaseURL(baseURL), anthropic.WithLogger(slog.New(slog.DiscardHandler)))
	}
	pinned := llm.ToolChoice{Mode: llm.ToolChoiceTool, Tool: "dice"}
	required := llm.ToolChoice{Mode: llm.ToolChoiceRequired}

	for _, model := range []string{"claude-sonnet-5-5", "claude-opus-5-5", "claude-fable-5-2", "claude-fable-5-1", "claude-mythos-5-2"} {
		for _, tc := range []llm.ToolChoice{pinned, required} {
			if typ, name := toolChoiceOnWire(t, plain, model, tc); typ != "auto" || name != "" {
				t.Errorf("%s with %s: tool_choice = {%q,%q}, want {auto}", model, tc.Mode, typ, name)
			}
		}
		if typ, _ := toolChoiceOnWire(t, plain, model, llm.ToolChoice{Mode: llm.ToolChoiceNone}); typ != "none" {
			t.Errorf("%s with none: tool_choice type = %q, want none", model, typ)
		}
	}

	for _, model := range []string{"claude-haiku-5-5", "claude-haiku-4-5", "claude-opus-4-8", "claude-sonnet-5", "claude-opus-5", "claude-fable-5"} {
		if typ, name := toolChoiceOnWire(t, plain, model, pinned); typ != "tool" || name != "dice" {
			t.Errorf("%s: tool_choice = {%q,%q}, want {tool,dice}", model, typ, name)
		}
		if typ, _ := toolChoiceOnWire(t, plain, model, required); typ != "any" {
			t.Errorf("%s: tool_choice type = %q, want any", model, typ)
		}
	}
}

// The downgrade is logged, once per client and model: visible in the pod log,
// not a line per forced re-roll.
func TestComplete_ForcedToolChoiceDowngradeWarnsOncePerModel(t *testing.T) {
	var logs bytes.Buffer
	var client *anthropic.Client
	same := func(baseURL string) *anthropic.Client {
		if client == nil {
			client = anthropic.New("k", anthropic.WithBaseURL(baseURL),
				anthropic.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
		}
		return client
	}
	// One client against one server, so the warn-once state is shared.
	var capture atomic.Value
	srv := sseServer(t, &capture, endTurn()...)
	defer srv.Close()
	fixed := func(string) *anthropic.Client { return same(srv.URL) }

	pinned := llm.ToolChoice{Mode: llm.ToolChoiceTool, Tool: "dice"}
	for range 3 {
		toolChoiceOnWireVia(t, fixed, "claude-sonnet-5-5", pinned)
	}
	toolChoiceOnWireVia(t, fixed, "claude-opus-5-5", pinned)
	toolChoiceOnWireVia(t, fixed, "claude-haiku-5-5", pinned)

	if got := strings.Count(logs.String(), "rejects a forced tool_choice"); got != 2 {
		t.Errorf("downgrade warnings = %d, want 2 (one per rejecting model)\n%s", got, logs.String())
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("the downgrade is not logged at WARN:\n%s", logs.String())
	}
}

// toolChoiceOnWireVia sends one forced-tool completion through the client c
// hands out, ignoring the wire result (the caller asserts on the log).
func toolChoiceOnWireVia(t *testing.T, c func(string) *anthropic.Client, model string, tc llm.ToolChoice) {
	t.Helper()
	ch, err := c("").Complete(context.Background(), llm.Request{
		Model:      model,
		MaxTokens:  128,
		Messages:   []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20."}},
		Tools:      []llm.ToolDef{{Name: "dice", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: tc,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	collect(t, ch)
}
