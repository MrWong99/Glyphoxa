package voicecassette

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"gopkg.in/yaml.v3"
)

// LLMExchange is one (prompt fingerprint, recorded response) pair in an
// [LLMCassette] — the model's output for a single [llm.Provider.Complete] call.
//
// Unlike the STT/TTS cassettes (positional), LLM exchanges are matched by
// PromptHash: per ADR-0021 the cassette key is a sha256 of the rendered prompt,
// so a prompt change misses and fails the test rather than silently replaying a
// stale response. A turn that drives several Complete calls (the tool-use loop)
// records one exchange per call, each under its own hash; replay returns the
// matching one regardless of order.
type LLMExchange struct {
	// PromptHash is the hex sha256 of the rendered request (model + system +
	// messages + tools); see [HashLLMRequest].
	PromptHash string `yaml:"prompt_hash"`

	// Text is the assistant prose the model returned for this prompt.
	Text string `yaml:"text,omitempty"`

	// ToolCalls are the tool_calls the model emitted, replayed verbatim. The
	// IDs must round-trip exactly: the loop echoes them into the next call's
	// messages, which are hashed, so a changed ID breaks the following match.
	ToolCalls []LLMToolCall `yaml:"tool_calls,omitempty"`

	// StopReason is the provider's completion reason (e.g. "end_turn",
	// "tool_use") replayed on the terminating [llm.EventDone].
	StopReason string `yaml:"stop_reason,omitempty"`
}

// LLMToolCall is one recorded tool_call within an [LLMExchange].
type LLMToolCall struct {
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	Input string `yaml:"input"` // raw JSON, stored as a string for readable YAML diffs
}

// LLMCassette is the on-disk record of an LLM scenario: a set of exchanges
// keyed by prompt hash. Its identity is its filename — LoadLLM(t, "llm-foo")
// reads tests/voice-cassettes/llm-foo.yaml.
type LLMCassette struct {
	// Exchanges is the recorded (hash, response) set. Stored as a list (not a
	// map) so the YAML diff is stable and reviewable; replay indexes it by hash
	// on load.
	Exchanges []LLMExchange `yaml:"exchanges"`

	// Notes is free-form provenance (provider, model, recording date). Not
	// load-bearing; survives round-trip for human reviewers.
	Notes string `yaml:"notes,omitempty"`
}

// LLMProvider is an [llm.Provider] that replays a single [LLMCassette]. Each
// Complete call hashes its [llm.Request] and replays the matching exchange as a
// stream (text deltas, then tool_calls, then a terminating EventDone). A miss —
// no exchange for the computed hash — returns an error pointing at the
// re-record workflow, so a prompt change is caught, never silently passed.
//
// Safe for concurrent Complete calls (the exchange map is read-only after load;
// the mutex guards nothing mutable but is kept for forward-compat and to make
// the race detector's expectations explicit).
type LLMProvider struct {
	name   string
	mu     sync.Mutex
	byHash map[string]LLMExchange
}

// loadLLMCassetteFromDisk reads tests/voice-cassettes/<name>.yaml and returns
// the decoded cassette. When mustExist is true (replay mode) every failure path
// — missing file, malformed YAML, empty exchanges, empty prompt_hash — is
// fatal. When mustExist is false (record mode), a missing file yields
// (zero, false); a malformed existing file still fails so a corrupted fixture
// is never silently overwritten.
//
// One function instead of two so neither build configuration (default replay
// vs -tags=record) sees an unused helper — only one of [LoadLLM]'s build-tag
// variants is compiled at a time.
func loadLLMCassetteFromDisk(t *testing.T, name string, mustExist bool) (LLMCassette, bool) {
	t.Helper()
	path := filepath.Join(cassettesDir(), name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if !mustExist && os.IsNotExist(err) {
			return LLMCassette{}, false
		}
		t.Fatalf("voicecassette.LoadLLM(%q): %v", name, err)
	}
	var c LLMCassette
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatalf("voicecassette.LoadLLM(%q): unmarshal: %v", name, err)
	}
	if mustExist {
		if len(c.Exchanges) == 0 {
			t.Fatalf("voicecassette.LoadLLM(%q): cassette has no exchanges", name)
		}
		for i, ex := range c.Exchanges {
			if ex.PromptHash == "" {
				t.Fatalf("voicecassette.LoadLLM(%q): exchange %d has empty prompt_hash", name, i)
			}
		}
	}
	return c, true
}

// indexByHash builds the replay lookup from a cassette's exchanges.
func indexByHash(c LLMCassette) map[string]LLMExchange {
	m := make(map[string]LLMExchange, len(c.Exchanges))
	for _, ex := range c.Exchanges {
		m[ex.PromptHash] = ex
	}
	return m
}

// Complete implements [llm.Provider]. It hashes req, looks up the recorded
// exchange, and replays it as a stream. A missing hash returns an error naming
// the computed hash so the diff against the cassette is obvious — the LLM
// counterpart of the STT hash-mismatch / TTS sentence-mismatch errors.
func (p *LLMProvider) Complete(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	hash := HashLLMRequest(req)

	p.mu.Lock()
	ex, ok := p.byHash[hash]
	p.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf(
			"voicecassette: no LLM exchange for prompt hash %s in cassette %q (%d recorded); re-record with -tags=record",
			hash, p.name, len(p.byHash),
		)
	}

	ch := make(chan llm.StreamEvent)
	go func() {
		defer close(ch)
		if ex.Text != "" {
			select {
			case <-ctx.Done():
				return
			case ch <- llm.StreamEvent{Type: llm.EventText, Text: ex.Text}:
			}
		}
		for _, tc := range ex.ToolCalls {
			select {
			case <-ctx.Done():
				return
			case ch <- llm.StreamEvent{Type: llm.EventToolCall, ToolCall: llm.ToolCall{
				ID:    tc.ID,
				Name:  tc.Name,
				Input: json.RawMessage(tc.Input),
			}}:
			}
		}
		stop := ex.StopReason
		if stop == "" {
			stop = "end_turn"
		}
		select {
		case <-ctx.Done():
		case ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: stop}:
		}
	}()
	return ch, nil
}

// encodeToolChoice renders a [llm.ToolChoice] as the cassette-hash token (#398):
// the zero value (Auto) encodes to "" so it drops out under omitempty and old
// cassettes never drift; None/Required encode to their bare mode; the pinned-tool
// choice encodes as "tool:<name>" so a different pinned tool hashes distinctly.
func encodeToolChoice(tc llm.ToolChoice) string {
	switch tc.Mode {
	case llm.ToolChoiceNone:
		return "none"
	case llm.ToolChoiceRequired:
		return "required"
	case llm.ToolChoiceTool:
		return "tool:" + tc.Tool
	default: // ToolChoiceAuto / zero
		return ""
	}
}

// HashLLMRequest returns the hex sha256 cassette key for req: the rendered
// prompt the model would see. It marshals model + system + messages + tools to
// canonical JSON and hashes the bytes. Exported so the record path and test
// helpers compute the same fingerprint.
//
// Determinism notes (ADR-0021): tool argument and schema JSON are treated as
// opaque bytes (never unmarshalled and re-marshalled, which could reorder map
// keys); the tools array order is the caller's responsibility (tool-framework
// sorts declarations by name). The hash deliberately includes the tools array
// and the model — a granted-tools or model change is a genuine prompt change
// and must miss.
func HashLLMRequest(req llm.Request) string {
	// A hashing-only mirror of llm.Request using json.RawMessage for opaque
	// fields, so encoding/json renders struct fields in declaration order and
	// passes tool inputs/schemas through byte-for-byte.
	type hMsg struct {
		Role        string           `json:"role"`
		Text        string           `json:"text,omitempty"`
		ToolCalls   []llm.ToolCall   `json:"tool_calls,omitempty"`
		ToolResults []llm.ToolResult `json:"tool_results,omitempty"`
	}
	type hReq struct {
		Model     string        `json:"model"`
		MaxTokens int           `json:"max_tokens"`
		Messages  []hMsg        `json:"messages"`
		Tools     []llm.ToolDef `json:"tools,omitempty"`
		// ToolChoice is the encoded per-round tool-choice knob (#398). It is
		// omitempty and the zero-value [llm.ToolChoice] encodes to "" (see
		// [encodeToolChoice]), so a request that never sets a choice hashes
		// byte-identical to a pre-field cassette (ADR-0021). A non-zero choice — a
		// tool-less fallback or a forced round — is a genuine prompt change and hashes
		// distinctly.
		ToolChoice string `json:"tool_choice,omitempty"`
	}
	h := hReq{Model: req.Model, MaxTokens: req.MaxTokens, Tools: req.Tools, ToolChoice: encodeToolChoice(req.ToolChoice)}
	for _, m := range req.Messages {
		h.Messages = append(h.Messages, hMsg{
			Role:        string(m.Role),
			Text:        m.Text,
			ToolCalls:   m.ToolCalls,
			ToolResults: m.ToolResults,
		})
	}
	// json.Marshal renders struct fields in declaration order and emits
	// json.RawMessage verbatim, so the bytes are stable across runs.
	b, err := json.Marshal(h)
	if err != nil {
		// Marshal fails only on invalid RawMessage JSON in tool inputs/schemas.
		// Hashing the empty bytes instead would collapse every such request to
		// ONE hash — silently replaying the wrong recorded exchange, the exact
		// failure the hash-keyed cassette exists to prevent. This is test
		// infrastructure fed by fixed fixtures: fail loudly at the source.
		panic(fmt.Sprintf("voicecassette: hash llm request: %v", err))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
