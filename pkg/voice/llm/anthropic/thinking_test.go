package anthropic_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
)

// endTurn is the smallest complete SSE reply: one text block and a clean stop.
func endTurn() []string {
	return []string{
		sse(`{"type":"message_start","message":{"id":"msg_1"}}`),
		sse(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		sse(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hallo."}}`),
		sse(`{"type":"content_block_stop","index":0}`),
		sse(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`),
		sse(`{"type":"message_stop"}`),
	}
}

// requestBody runs one completion for model against a capturing server and
// returns the decoded JSON request body.
func requestBody(t *testing.T, model string, opts ...anthropic.Option) map[string]json.RawMessage {
	t.Helper()
	var captured atomic.Value
	srv := sseServer(t, &captured, endTurn()...)
	defer srv.Close()

	c := anthropic.New("k", append([]anthropic.Option{anthropic.WithBaseURL(srv.URL)}, opts...)...)
	ch, err := c.Complete(context.Background(), llm.Request{
		Model:    model,
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Hallo Flo"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	collect(t, ch)

	var body map[string]json.RawMessage
	if err := json.Unmarshal(captured.Load().([]byte), &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return body
}

// TestComplete_Haiku55_DefaultsToAdaptiveThinkingAtLowEffort pins the voice
// policy measured for #702: claude-haiku-5-5 runs adaptive thinking at effort
// low. Thinking disabled made the model write narration before every tool call
// (it would be spoken), and the API's own default (effort medium) costs about
// a second of first-token latency on plain chat.
func TestComplete_Haiku55_DefaultsToAdaptiveThinkingAtLowEffort(t *testing.T) {
	body := requestBody(t, "claude-haiku-5-5")

	if got := string(body["thinking"]); got != `{"type":"adaptive"}` {
		t.Errorf("thinking = %s, want {\"type\":\"adaptive\"}", got)
	}
	if got := string(body["output_config"]); got != `{"effort":"low"}` {
		t.Errorf("output_config = %s, want {\"effort\":\"low\"}", got)
	}
}

// TestComplete_OtherModels_SendNoThinkingFields pins the model gate:
// claude-haiku-4-5 answers 400 to both output_config.effort and adaptive
// thinking, and every other model must keep the request it has today (the
// recorded cassettes hash the body).
func TestComplete_OtherModels_SendNoThinkingFields(t *testing.T) {
	for _, model := range []string{"claude-haiku-4-5", "claude-haiku-4-5-20251001", "claude-opus-4-8", "", "some-gateway-model"} {
		body := requestBody(t, model)
		for _, field := range []string{"thinking", "output_config"} {
			if raw, ok := body[field]; ok {
				t.Errorf("model %q: request carries %s = %s, want the field absent", model, field, raw)
			}
		}
	}
}

// TestComplete_NeverSendsSamplingParams pins that no sampling parameter is ever
// on the wire: claude-haiku-5-5 answers 400 to a non-default temperature, top_p
// or top_k.
func TestComplete_NeverSendsSamplingParams(t *testing.T) {
	for _, model := range []string{"claude-haiku-5-5", "claude-haiku-4-5", ""} {
		body := requestBody(t, model)
		for _, field := range []string{"temperature", "top_p", "top_k"} {
			if _, ok := body[field]; ok {
				t.Errorf("model %q: request carries %s", model, field)
			}
		}
	}
}

// TestWithThinkingFor_OverridesThePolicyPerModel pins the per-model knob (the
// counterpart of openaicompat.WithReasoningEffortFor): a caller can select the
// thinking-disabled arm for one model without touching any other.
func TestWithThinkingFor_OverridesThePolicyPerModel(t *testing.T) {
	disabled := anthropic.WithThinkingFor(func(model string) anthropic.Thinking {
		if model == "claude-haiku-5-5" {
			return anthropic.Thinking{Mode: anthropic.ThinkingDisabled}
		}
		return anthropic.Thinking{}
	})

	body := requestBody(t, "claude-haiku-5-5", disabled)
	if got := string(body["thinking"]); got != `{"type":"disabled"}` {
		t.Errorf("thinking = %s, want {\"type\":\"disabled\"}", got)
	}
	if raw, ok := body["output_config"]; ok {
		t.Errorf("output_config = %s, want absent (no effort was asked for)", raw)
	}

	body = requestBody(t, "claude-haiku-4-5", disabled)
	if _, ok := body["thinking"]; ok {
		t.Error("claude-haiku-4-5 got a thinking field from a policy that returned none")
	}
}
