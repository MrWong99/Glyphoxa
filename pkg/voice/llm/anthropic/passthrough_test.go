package anthropic_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
)

// thinkingThenDice is round 1 of an adaptive-thinking tool turn as the API
// streams it: a thinking block whose text is omitted (signature only), then the
// tool_use block.
func thinkingThenDice() []string {
	return []string{
		sse(`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":10}}}`),
		sse(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`),
		sse(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqQBsig"}}`),
		sse(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"+/tail=="}}`),
		sse(`{"type":"content_block_stop","index":0}`),
		sse(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"dice","input":{}}}`),
		sse(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"sides\":20}"}}`),
		sse(`{"type":"content_block_stop","index":1}`),
		sse(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":40}}`),
		sse(`{"type":"message_stop"}`),
	}
}

// scriptedServer answers the n-th request with the n-th reply and records every
// request body. A reply is either SSE event lines (200) or, when status is set,
// a plain error body.
type scriptedReply struct {
	status int
	body   string
	events []string
}

type scriptedServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
}

func newScriptedServer(t *testing.T, replies ...scriptedReply) *scriptedServer {
	t.Helper()
	s := &scriptedServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readBody(r)
		s.mu.Lock()
		n := len(s.bodies)
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()
		if n >= len(replies) {
			t.Errorf("unexpected request #%d", n+1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		rep := replies[n]
		if rep.status != 0 {
			w.WriteHeader(rep.status)
			_, _ = w.Write([]byte(rep.body))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range rep.events {
			_, _ = w.Write([]byte(ev))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// request returns the n-th (0-based) recorded request body.
func (s *scriptedServer) request(t *testing.T, n int) []byte {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if n >= len(s.bodies) {
		t.Fatalf("only %d requests were made, want #%d", len(s.bodies), n+1)
	}
	return s.bodies[n]
}

// assistantContent returns the raw content blocks of the first assistant
// message in a recorded request body.
func assistantContent(t *testing.T, raw []byte) []json.RawMessage {
	t.Helper()
	var body struct {
		Messages []struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode request: %v\n%s", err, raw)
	}
	for _, m := range body.Messages {
		if m.Role == "assistant" {
			return m.Content
		}
	}
	t.Fatalf("request has no assistant message:\n%s", raw)
	return nil
}

// toolRound runs round 1 (the model asks for the dice Tool) and then round 2
// (the tool result fed back) the way the tool loop does: the assistant turn is
// rebuilt from the decoded [llm.ToolCall]s, which carry no thinking block.
func toolRound(t *testing.T, c *anthropic.Client) {
	t.Helper()
	user := llm.Message{Role: llm.RoleUser, Text: "Würfel einen W20."}
	tools := []llm.ToolDef{{Name: "dice", InputSchema: json.RawMessage(`{"type":"object"}`)}}

	ch, err := c.Complete(context.Background(), llm.Request{Model: "claude-haiku-5-5", Messages: []llm.Message{user}, Tools: tools})
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	var calls []llm.ToolCall
	for _, ev := range collect(t, ch) {
		if ev.Type == llm.EventToolCall {
			calls = append(calls, ev.ToolCall)
		}
	}
	if len(calls) != 1 || calls[0].ID != "toolu_1" {
		t.Fatalf("round 1 tool calls = %+v, want the one dice call", calls)
	}

	ch, err = c.Complete(context.Background(), llm.Request{
		Model: "claude-haiku-5-5",
		Messages: []llm.Message{
			user,
			{Role: llm.RoleAssistant, ToolCalls: calls},
			{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{CallID: "toolu_1", Content: "17"}}},
		},
		Tools: tools,
	})
	if err != nil {
		t.Fatalf("round 2: %v", err)
	}
	collect(t, ch)
}

// TestComplete_ThinkingBlock_ReplayedVerbatimOnTheNextToolRound pins the
// thinking-block continuity of an adaptive-thinking tool turn (#702): the block
// the model emitted before its tool_use — empty text, signature only — rides
// the assistant turn of the next request unchanged, in front of the tool_use
// block. Dropping it is accepted by the API but was measured to make the model
// think again after the tool result (seconds, hundreds of output tokens).
func TestComplete_ThinkingBlock_ReplayedVerbatimOnTheNextToolRound(t *testing.T) {
	srv := newScriptedServer(t,
		scriptedReply{events: thinkingThenDice()},
		scriptedReply{events: endTurn()},
	)
	toolRound(t, anthropic.New("k", anthropic.WithBaseURL(srv.URL)))

	content := assistantContent(t, srv.request(t, 1))
	if len(content) != 2 {
		t.Fatalf("assistant content = %s, want [thinking, tool_use]", content)
	}
	if got, want := string(content[0]), `{"type":"thinking","thinking":"","signature":"EqQBsig+/tail=="}`; got != want {
		t.Errorf("replayed thinking block\n got %s\nwant %s", got, want)
	}
	if !strings.Contains(string(content[1]), `"type":"tool_use"`) || !strings.Contains(string(content[1]), `"id":"toolu_1"`) {
		t.Errorf("second block = %s, want the tool_use block", content[1])
	}
}

// TestComplete_RedactedThinkingBlock_ReplayedVerbatim pins the other block kind
// the API may put in front of a tool call: redacted_thinking arrives whole in
// content_block_start and must go back as it came.
func TestComplete_RedactedThinkingBlock_ReplayedVerbatim(t *testing.T) {
	round1 := thinkingThenDice()
	round1[1] = sse(`{"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"b3BhcXVl"}}`)
	round1 = append(round1[:2], round1[4:]...) // no deltas for a redacted block
	srv := newScriptedServer(t, scriptedReply{events: round1}, scriptedReply{events: endTurn()})
	toolRound(t, anthropic.New("k", anthropic.WithBaseURL(srv.URL)))

	content := assistantContent(t, srv.request(t, 1))
	if len(content) != 2 {
		t.Fatalf("assistant content = %s, want [redacted_thinking, tool_use]", content)
	}
	if got, want := string(content[0]), `{"type":"redacted_thinking","data":"b3BhcXVl"}`; got != want {
		t.Errorf("replayed block\n got %s\nwant %s", got, want)
	}
}

// TestComplete_NoThinkingBlock_AssistantTurnUnchanged pins that a tool turn
// without a thinking block (claude-haiku-4-5, a forced tool choice, every other
// provider's history) replays exactly the blocks it does today.
func TestComplete_NoThinkingBlock_AssistantTurnUnchanged(t *testing.T) {
	round1 := thinkingThenDice()
	round1 = append(round1[:1], round1[5:]...) // drop the thinking block entirely
	for i, ev := range round1 {
		round1[i] = strings.ReplaceAll(ev, `"index":1`, `"index":0`)
	}
	srv := newScriptedServer(t, scriptedReply{events: round1}, scriptedReply{events: endTurn()})
	toolRound(t, anthropic.New("k", anthropic.WithBaseURL(srv.URL)))

	content := assistantContent(t, srv.request(t, 1))
	if len(content) != 1 || !strings.Contains(string(content[0]), `"type":"tool_use"`) {
		t.Errorf("assistant content = %s, want only the tool_use block", content)
	}
}

// TestComplete_ThinkingReplayRejected_RetriesOnceWithoutIt pins the recovery
// for the API's history check: a thinking block is bound to the conversation
// that produced it, and replaying it after anything in front of it changed is a
// 400. The adapter cannot see every edit its callers make (a regenerated turn
// reuses tool-call ids), so it strips the blocks and retries once rather than
// failing the turn.
func TestComplete_ThinkingReplayRejected_RetriesOnceWithoutIt(t *testing.T) {
	srv := newScriptedServer(t,
		scriptedReply{events: thinkingThenDice()},
		scriptedReply{status: http.StatusBadRequest, body: `{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block. The block is bound to a different conversation."}}`},
		scriptedReply{events: endTurn()},
	)
	toolRound(t, anthropic.New("k", anthropic.WithBaseURL(srv.URL)))

	if content := assistantContent(t, srv.request(t, 1)); len(content) != 2 {
		t.Fatalf("first attempt content = %s, want the thinking block replayed", content)
	}
	content := assistantContent(t, srv.request(t, 2))
	if len(content) != 1 || !strings.Contains(string(content[0]), `"type":"tool_use"`) {
		t.Errorf("retry content = %s, want only the tool_use block", content)
	}
}

// TestComplete_Unrelated400_NotRetried pins that the strip-and-retry is not a
// general 400 retry: a rejection that is not about a thinking block fails the
// call at once, even when the request replayed one.
func TestComplete_Unrelated400_NotRetried(t *testing.T) {
	srv := newScriptedServer(t,
		scriptedReply{events: thinkingThenDice()},
		scriptedReply{status: http.StatusBadRequest, body: `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: must be positive"}}`},
	)
	c := anthropic.New("k", anthropic.WithBaseURL(srv.URL))

	user := llm.Message{Role: llm.RoleUser, Text: "Würfel einen W20."}
	ch, err := c.Complete(context.Background(), llm.Request{Model: "claude-haiku-5-5", Messages: []llm.Message{user}})
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	collect(t, ch)

	_, err = c.Complete(context.Background(), llm.Request{
		Model: "claude-haiku-5-5",
		Messages: []llm.Message{
			user,
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"sides":20}`)}}},
			{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{CallID: "toolu_1", Content: "17"}}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "max_tokens") {
		t.Fatalf("err = %v, want the 400 surfaced", err)
	}
}

// thinkingThenTwoCalls is round 1 of a turn with parallel tool calls: one
// thinking block, then two tool_use blocks.
func thinkingThenTwoCalls() []string {
	return []string{
		sse(`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":10}}}`),
		sse(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":"sig"}}`),
		sse(`{"type":"content_block_stop","index":0}`),
		sse(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_a","name":"dice","input":{}}}`),
		sse(`{"type":"content_block_stop","index":1}`),
		sse(`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_b","name":"dice","input":{}}}`),
		sse(`{"type":"content_block_stop","index":2}`),
		sse(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":40}}`),
		sse(`{"type":"message_stop"}`),
	}
}

const thinking400 = `{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: Invalid signature in thinking block."}}`

// TestComplete_ThinkingReplayRejected_IsNotReplayedAgain pins what the adapter
// remembers after the API refused a replayed thinking block: nothing of that
// assistant turn. The blocks are stored under every tool_use id of the turn, so
// forgetting only the id they were found under left a parallel-call turn to be
// replayed through its sibling id on the next round — one more refused request
// and retry per round, on the turn a player is waiting on (#702 review).
func TestComplete_ThinkingReplayRejected_IsNotReplayedAgain(t *testing.T) {
	for _, tc := range []struct {
		name   string
		round1 []string
		ids    []string
	}{
		{"one tool call", thinkingThenDice(), []string{"toolu_1"}},
		{"parallel tool calls", thinkingThenTwoCalls(), []string{"toolu_a", "toolu_b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newScriptedServer(t,
				scriptedReply{events: tc.round1},
				scriptedReply{status: http.StatusBadRequest, body: thinking400},
				scriptedReply{events: endTurn()}, // the retry of round 2
				scriptedReply{events: endTurn()}, // round 3: must go through first time
			)
			c := anthropic.New("k", anthropic.WithBaseURL(srv.URL))
			user := llm.Message{Role: llm.RoleUser, Text: "Würfel."}

			ch, err := c.Complete(context.Background(), llm.Request{Model: "claude-haiku-5-5", Messages: []llm.Message{user}})
			if err != nil {
				t.Fatalf("round 1: %v", err)
			}
			collect(t, ch)

			var calls []llm.ToolCall
			var results []llm.ToolResult
			for _, id := range tc.ids {
				calls = append(calls, llm.ToolCall{ID: id, Name: "dice", Input: json.RawMessage(`{}`)})
				results = append(results, llm.ToolResult{CallID: id, Content: "17"})
			}
			history := []llm.Message{user, {Role: llm.RoleAssistant, ToolCalls: calls}, {Role: llm.RoleTool, ToolResults: results}}
			for round := 2; round <= 3; round++ {
				ch, err := c.Complete(context.Background(), llm.Request{Model: "claude-haiku-5-5", Messages: history})
				if err != nil {
					t.Fatalf("round %d: %v", round, err)
				}
				collect(t, ch)
			}

			if content := assistantContent(t, srv.request(t, 1)); !strings.Contains(string(content[0]), `"type":"thinking"`) {
				t.Fatalf("round 2 did not replay the thinking block: %s", content)
			}
			for _, block := range assistantContent(t, srv.request(t, 3)) {
				if strings.Contains(string(block), `"type":"thinking"`) {
					t.Errorf("round 3 replayed a thinking block the API had refused: %s", block)
				}
			}
		})
	}
}

// TestComplete_ThinkingReplayRejected_IsLogged pins that the strip-and-retry is
// visible: it costs a refused request and makes the model think again, and
// nothing above the adapter can see it happen (#702 review).
func TestComplete_ThinkingReplayRejected_IsLogged(t *testing.T) {
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn}))
	srv := newScriptedServer(t,
		scriptedReply{events: thinkingThenDice()},
		scriptedReply{status: http.StatusBadRequest, body: thinking400},
		scriptedReply{events: endTurn()},
	)
	toolRound(t, anthropic.New("k", anthropic.WithBaseURL(srv.URL), anthropic.WithLogger(log)))

	out := logged.String()
	if strings.Count(out, "level=WARN") != 1 || !strings.Contains(out, "thinking") || !strings.Contains(out, "claude-haiku-5-5") {
		t.Errorf("log = %q, want one warning naming the refused thinking replay and the model", out)
	}

	// A tool round the API accepts logs nothing.
	logged.Reset()
	srv = newScriptedServer(t, scriptedReply{events: thinkingThenDice()}, scriptedReply{events: endTurn()})
	toolRound(t, anthropic.New("k", anthropic.WithBaseURL(srv.URL), anthropic.WithLogger(log)))
	if logged.Len() != 0 {
		t.Errorf("an accepted replay logged %q", logged.String())
	}
}

// TestComplete_ThinkingStore_IsBounded pins the bound on what the adapter
// remembers: the thinking of the most recent 256 tool calls. One client serves
// a whole Voice Session, so without the bound it would grow with every tool
// call of the session.
func TestComplete_ThinkingStore_IsBounded(t *testing.T) {
	const turns = 257
	var mu sync.Mutex
	n := 0
	var last []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readBody(r)
		mu.Lock()
		n++
		id, replay := n, n > turns
		last = body
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		events := endTurn()
		if !replay {
			events = thinkingThenDice()
		}
		for _, ev := range events {
			_, _ = w.Write([]byte(strings.ReplaceAll(ev, "toolu_1", fmt.Sprintf("toolu_%d", id))))
		}
	}))
	defer srv.Close()
	c := anthropic.New("k", anthropic.WithBaseURL(srv.URL))
	user := llm.Message{Role: llm.RoleUser, Text: "Würfel."}

	for i := 0; i < turns; i++ {
		ch, err := c.Complete(context.Background(), llm.Request{Model: "claude-haiku-5-5", Messages: []llm.Message{user}})
		if err != nil {
			t.Fatalf("turn %d: %v", i+1, err)
		}
		collect(t, ch)
	}

	replayed := func(id string) bool {
		ch, err := c.Complete(context.Background(), llm.Request{Model: "claude-haiku-5-5", Messages: []llm.Message{
			user,
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "dice", Input: json.RawMessage(`{}`)}}},
			{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{CallID: id, Content: "17"}}},
		}})
		if err != nil {
			t.Fatalf("replay %s: %v", id, err)
		}
		collect(t, ch)
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(string(assistantContent(t, last)[0]), `"type":"thinking"`)
	}
	if replayed("toolu_1") {
		t.Error("the oldest of 257 tool calls still had its thinking stored; the store is not bounded at 256")
	}
	if !replayed("toolu_2") || !replayed("toolu_257") {
		t.Error("a tool call within the most recent 256 lost its thinking")
	}
}
