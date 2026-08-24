package anthropic

import (
	"encoding/json"
	"testing"
)

// sseFrame is one recorded Anthropic SSE "data:" payload plus the fields
// streamEvents reads out of it. Shapes follow the native Messages API stream
// that ADR-0037 deliberately keeps hand-rolled (Anthropic is out of scope for
// the OpenAI SDK core) and mirror the frames the ADR-0021 cassette tests pin,
// with realistic NPC reply text rather than microstrings.
type sseFrame struct {
	name string
	raw  []byte
	// check asserts the decoded event carries what streamEvents switches on.
	check func(t *testing.T, ev sseEvent)
}

// sseFrames is the benchmark corpus: one frame of every event type
// streamEvents acts on. Text deltas are sized like a real streamed NPC line
// (the cassette replies in tests/voice-cassettes are 40–70 characters, i.e. a
// handful of tokens per delta).
var sseFrames = []sseFrame{
	{
		name: "message_start",
		raw:  []byte(`{"type":"message_start","message":{"id":"msg_01XcQ7","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"usage":{"input_tokens":1842,"output_tokens":1}}}`),
		check: func(t *testing.T, ev sseEvent) {
			if ev.Message == nil || ev.Message.Usage == nil {
				t.Fatal("message_start: want message.usage present")
			}
			if got := ev.Message.Usage.InputTokens; got != 1842 {
				t.Errorf("input_tokens = %d, want 1842", got)
			}
		},
	},
	{
		name: "content_block_start_text",
		raw:  []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		check: func(t *testing.T, ev sseEvent) {
			if ev.ContentBlock.Type != "text" {
				t.Errorf("content_block.type = %q, want text", ev.ContentBlock.Type)
			}
		},
	},
	{
		name: "content_block_start_tool_use",
		raw:  []byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01A9Bd","name":"dice","input":{}}}`),
		check: func(t *testing.T, ev sseEvent) {
			if ev.Index != 1 || ev.ContentBlock.ID != "toolu_01A9Bd" || ev.ContentBlock.Name != "dice" {
				t.Errorf("tool block = %d/%q/%q, want 1/toolu_01A9Bd/dice", ev.Index, ev.ContentBlock.ID, ev.ContentBlock.Name)
			}
		},
	},
	{
		name: "content_block_delta_text",
		raw:  []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Welcome, traveler. A warm bed"}}`),
		check: func(t *testing.T, ev sseEvent) {
			if ev.Delta.Type != "text_delta" || ev.Delta.Text != "Welcome, traveler. A warm bed" {
				t.Errorf("text delta = %q/%q", ev.Delta.Type, ev.Delta.Text)
			}
		},
	},
	{
		name: "content_block_delta_input_json",
		raw:  []byte(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"notation\":\"1d20+3\""}}`),
		check: func(t *testing.T, ev sseEvent) {
			if ev.Delta.Type != "input_json_delta" || ev.Delta.PartialJSON != `{"notation":"1d20+3"` {
				t.Errorf("json delta = %q/%q", ev.Delta.Type, ev.Delta.PartialJSON)
			}
		},
	},
	{
		name: "content_block_stop",
		raw:  []byte(`{"type":"content_block_stop","index":0}`),
		check: func(t *testing.T, ev sseEvent) {
			if ev.Type != "content_block_stop" {
				t.Errorf("type = %q", ev.Type)
			}
		},
	},
	{
		name: "message_delta",
		raw:  []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":37}}`),
		check: func(t *testing.T, ev sseEvent) {
			if ev.Usage == nil || ev.Usage.OutputTokens != 37 {
				t.Fatalf("usage = %+v, want output_tokens 37", ev.Usage)
			}
			if ev.Delta.StopReason != "end_turn" {
				t.Errorf("stop_reason = %q, want end_turn", ev.Delta.StopReason)
			}
		},
	},
	{
		name: "error",
		raw:  []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`),
		check: func(t *testing.T, ev sseEvent) {
			if ev.Type != "error" {
				t.Errorf("type = %q, want error", ev.Type)
			}
		},
	},
}

// TestSSEFrameFixtures_DecodeToStreamEventsFields keeps the benchmark corpus
// honest: every fixture must decode into the [sseEvent] fields streamEvents
// switches on. A fixture that drifts from the wire shape fails here rather than
// silently benchmarking something production never sees.
func TestSSEFrameFixtures_DecodeToStreamEventsFields(t *testing.T) {
	for _, f := range sseFrames {
		t.Run(f.name, func(t *testing.T) {
			var ev sseEvent
			if err := json.Unmarshal(f.raw, &ev); err != nil {
				t.Fatalf("decode %s: %v", f.name, err)
			}
			f.check(t, ev)
		})
	}
}

// BenchmarkStreamEventsDecode measures the per-event json.Unmarshal into
// [sseEvent] that streamEvents runs on every SSE data: line of an LLM turn
// (#609). One iteration decodes one event, round-robin over the corpus, so the
// number reads as "per streamed SSE event".
//
// # Events/second → CPU budget
//
// No live Voice Session capture was available when this benchmark landed, so
// the rate below is a documented estimate, not a measurement (live verification
// is a follow-up):
//
//   - A streamed NPC reply is short: the ADR-0021 cassettes in
//     tests/voice-cassettes hold replies of 40–134 characters, i.e. ~10–35
//     output tokens; the message_delta usage fixture above (37 output tokens) is
//     the same order.
//   - Anthropic emits roughly one content_block_delta per token or small token
//     group, plus ~5 framing events (message_start, content_block_start/stop,
//     message_delta, message_stop) — call it ~40 events per turn, and ~60 for a
//     turn that also streams a tool_use input.
//   - Those events arrive over the ~1–2 s the model spends generating, so a
//     single in-flight turn is ~20–40 events/s. A voice pod under the ADR-0057
//     claim plane may hold many Voice Sessions, but only one NPC turn per
//     session streams at a time; take 20 concurrent turns as a busy-pod ceiling
//     → ~800 events/s.
//
// At that ceiling the decode's CPU share is 800 × ns/op: a 1 µs/op decode is
// 800 µs/s ≈ 0.08 % of one core, and one whole turn's ~40 events cost ~40 µs
// against a <400 ms time-to-first-token budget (ADR-0037). Adoption of
// encoding/json/v2 here is only worth filing if ns/op is orders of magnitude
// worse than that, or if allocs/op matter for GC pressure rather than CPU.
func BenchmarkStreamEventsDecode(b *testing.B) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		var ev sseEvent
		if err := json.Unmarshal(sseFrames[i%len(sseFrames)].raw, &ev); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

// BenchmarkStreamEventsDecodeTextDelta isolates the single hottest event shape:
// the content_block_delta text_delta that makes up the bulk of every turn.
func BenchmarkStreamEventsDecodeTextDelta(b *testing.B) {
	raw := sseFrames[3].raw // content_block_delta_text
	b.ReportAllocs()
	for b.Loop() {
		var ev sseEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			b.Fatal(err)
		}
	}
}
