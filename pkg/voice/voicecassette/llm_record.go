//go:build record

package voicecassette

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
	"gopkg.in/yaml.v3"
)

// LoadLLM in -tags=record builds returns an [LLMRecorder] that proxies every
// Complete call to a live Anthropic client, captures the request hash and the
// streamed response (text, tool_calls, stop reason), and rewrites
// tests/voice-cassettes/<name>.yaml at test cleanup. The ANTHROPIC_API_KEY
// environment variable supplies credentials.
//
// Because the cassette is hashed per prompt, the tool-use loop records one
// exchange per Complete call (the request, the result fed back, the next
// request, …), each under its own hash — replay then drives the same loop
// keylessly. Any existing cassette's Notes and leading header comments are
// preserved with an idempotent dated provenance line.
func LoadLLM(t *testing.T, name string) llm.Provider {
	t.Helper()
	existing, _ := loadLLMCassetteFromDisk(t, name, false)
	r := &LLMRecorder{
		name:     name,
		client:   anthropic.New(""),
		existing: existing,
	}
	t.Cleanup(func() {
		if err := r.write(); err != nil {
			t.Fatalf("voicecassette.LoadLLM(%q): record write: %v", name, err)
		}
	})
	return r
}

// LLMRecorder is the -tags=record counterpart to [LLMProvider]: it forwards
// every Complete call to a live [anthropic.Client], drains the stream while
// capturing the response, and appends a keyed [LLMExchange] so the cassette can
// be rewritten at cleanup.
type LLMRecorder struct {
	name     string
	client   *anthropic.Client
	existing LLMCassette

	mu        sync.Mutex
	exchanges []LLMExchange
}

// Complete implements [llm.Provider]. It hashes req, forwards to the live
// client, and re-streams events to the caller while accumulating the recorded
// exchange — so the test under record mode exercises the real loop against real
// provider output.
func (r *LLMRecorder) Complete(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	hash := HashLLMRequest(req)
	in, err := r.client.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("voicecassette: LLMRecorder live Complete for cassette %q: %w", r.name, err)
	}

	out := make(chan llm.StreamEvent)
	go func() {
		defer close(out)
		var ex LLMExchange
		ex.PromptHash = hash
		for ev := range in {
			switch ev.Type {
			case llm.EventText:
				ex.Text += ev.Text
			case llm.EventToolCall:
				ex.ToolCalls = append(ex.ToolCalls, LLMToolCall{
					ID:    ev.ToolCall.ID,
					Name:  ev.ToolCall.Name,
					Input: string(ev.ToolCall.Input),
				})
			case llm.EventDone:
				ex.StopReason = ev.StopReason
			}
			out <- ev
		}
		r.mu.Lock()
		r.exchanges = append(r.exchanges, ex)
		r.mu.Unlock()
	}()
	return out, nil
}

// write serialises the captured exchanges to tests/voice-cassettes/<name>.yaml,
// preserving the existing Notes (idempotent dated provenance) and re-prepending
// the hand-authored header block yaml.Marshal drops. A no-op if Complete was
// never called.
func (r *LLMRecorder) write() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.exchanges) == 0 {
		return nil
	}
	out := LLMCassette{
		Exchanges: r.exchanges,
		Notes:     appendProvenance(r.existing.Notes, "claude"),
	}
	body, err := yaml.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal cassette: %w", err)
	}
	data := append([]byte(leadingComment(r.name)), body...)
	path := filepath.Join(cassettesDir(), r.name+".yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
