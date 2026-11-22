package anthropic_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
)

// complete runs one completion against a server that streams events and
// returns the decoded stream.
func complete(t *testing.T, events ...string) []llm.StreamEvent {
	t.Helper()
	srv := sseServer(t, nil, events...)
	defer srv.Close()
	ch, err := anthropic.New("k", anthropic.WithBaseURL(srv.URL)).Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return collect(t, ch)
}

// TestComplete_Usage_CountsCachedInputTokens pins the metering of a cached
// prompt (#702): the API moves cached prompt tokens out of input_tokens into
// cache_creation_input_tokens / cache_read_input_tokens, so input_tokens alone
// under-counted a cached request about 35-fold. All three are prompt tokens the
// request was billed for.
//
// The reported input usage is priced at ONE rate, the model's input rate, and
// it has to be an upper bound of the bill on every single request, because a
// spend cap reads it. A cache read costs a tenth of that rate, so counting it
// in full over-estimates. A cache WRITE costs 1.25x, so counting it once would
// under-meter a request that writes and is never read back (an NPC addressed
// once in five minutes) by up to a quarter: written tokens count 1.25x,
// rounded up.
func TestComplete_Usage_CountsCachedInputTokens(t *testing.T) {
	for _, tc := range []struct {
		name               string
		input, write, read int
		want               int
	}{
		{"write and read", 123, 70, 4193, 123 + 88 + 4193}, // 70 x 1.25 = 87.5, rounded up
		{"lone cache write", 20, 4000, 0, 20 + 5000},
		{"read only", 20, 0, 4000, 4020},
		{"no caching", 4020, 0, 0, 4020},
		{"one written token", 0, 1, 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := complete(t,
				sse(fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}}`, tc.input, tc.write, tc.read)),
				sse(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
				sse(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hallo."}}`),
				sse(`{"type":"content_block_stop","index":0}`),
				sse(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`),
				sse(`{"type":"message_stop"}`),
			)
			for _, ev := range events {
				if ev.Type == llm.EventUsage {
					if ev.Usage.InputTokens != tc.want {
						t.Errorf("InputTokens = %d, want %d (input + 1.25 x cache write + cache read)", ev.Usage.InputTokens, tc.want)
					}
					if ev.Usage.OutputTokens != 9 {
						t.Errorf("OutputTokens = %d, want 9", ev.Usage.OutputTokens)
					}
					return
				}
			}
			t.Fatal("no EventUsage emitted")
		})
	}
}

// TestComplete_Refusal_IsAClassifiedError pins the handling of a safety
// classifier decline (#702): the API answers HTTP 200 with stop_reason
// "refusal" and no text. Passed on as an ordinary EventDone that is an empty
// reply the voice loop would treat as "the NPC said nothing"; it is a terminal
// EventError of class refusal instead, so the turn fails visibly and speaks
// its fallback line.
func TestComplete_Refusal_IsAClassifiedError(t *testing.T) {
	events := complete(t,
		sse(`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":50}}}`),
		sse(`{"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber","explanation":"declined"}},"usage":{"output_tokens":0}}`),
		sse(`{"type":"message_stop"}`),
	)

	var sawUsage bool
	for _, ev := range events {
		switch ev.Type {
		case llm.EventDone:
			t.Errorf("refusal emitted EventDone (stop %q): a decline must not read as a complete reply", ev.StopReason)
		case llm.EventUsage:
			sawUsage = true
		case llm.EventError:
			if ev.ErrClass != llm.ErrClassRefusal {
				t.Errorf("ErrClass = %q, want %q", ev.ErrClass, llm.ErrClassRefusal)
			}
			if !strings.Contains(ev.Err, "refus") || !strings.Contains(ev.Err, "cyber") {
				t.Errorf("Err = %q, want it to name the refusal and its category", ev.Err)
			}
		}
	}
	if !sawUsage {
		t.Error("the refused request's usage was not reported (it is still billed input)")
	}
	if last := events[len(events)-1]; last.Type != llm.EventError {
		t.Errorf("last event type = %v, want the terminal EventError", last.Type)
	}
}
