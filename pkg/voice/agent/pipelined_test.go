package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
)

// These are the producer half of the pre-synthesis pipeline (#626): under a
// pipelined turn ctx the dispatch callback returns as soon as the PREVIOUS
// sentence resolved, so its return value is control flow, not a commit signal.
// The Agent must commit strictly through Reply.OnDelivered and wait for every
// sentence's Reply.OnResolved before reading what it committed (ADR-0012).

// asyncDispatch models the orchestrator's pipelined dispatch: sentence k's outcome
// is decided on its own goroutine (gated by the test), dispatch(k+1) returns k's
// outcome, and the LAST sentence resolves after the producer has returned.
type asyncDispatch struct {
	mu       sync.Mutex
	outcomes map[string]orchestrator.SentenceOutcome
	gates    map[string]chan struct{}
	started  map[string]chan struct{}
	pending  chan orchestrator.SentenceOutcome
}

func newAsyncDispatch(sentences ...string) *asyncDispatch {
	d := &asyncDispatch{
		outcomes: map[string]orchestrator.SentenceOutcome{},
		gates:    map[string]chan struct{}{},
		started:  map[string]chan struct{}{},
	}
	for _, s := range sentences {
		d.gates[s] = make(chan struct{})
		d.started[s] = make(chan struct{})
	}
	return d
}

// script fixes one sentence's outcome (default: delivered).
func (d *asyncDispatch) script(sentence string, o orchestrator.SentenceOutcome) {
	d.outcomes[sentence] = o
}

func (d *asyncDispatch) chanFor(m map[string]chan struct{}, sentence string) chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	ch, ok := m[sentence]
	if !ok {
		ch = make(chan struct{})
		m[sentence] = ch
	}
	return ch
}

func (d *asyncDispatch) dispatch(rep orchestrator.Reply) error {
	prev := d.pending
	resolved := make(chan orchestrator.SentenceOutcome, 1)
	d.pending = resolved
	started, gate := d.chanFor(d.started, rep.Sentence), d.chanFor(d.gates, rep.Sentence)
	go func() {
		close(started)
		<-gate
		d.mu.Lock()
		outcome := d.outcomes[rep.Sentence]
		d.mu.Unlock()
		if outcome == orchestrator.SentenceDelivered && rep.OnDelivered != nil {
			rep.OnDelivered()
		}
		if rep.OnResolved != nil {
			rep.OnResolved(outcome)
		}
		resolved <- outcome
	}()
	if prev == nil {
		return nil // nothing has resolved yet: keep producing
	}
	switch <-prev {
	case orchestrator.SentenceNotDelivered:
		return orchestrator.ErrNotDelivered
	case orchestrator.SentenceCut:
		return context.Canceled
	}
	return nil
}

// resolve lets one dispatched sentence settle, after its dispatch was observed.
func (d *asyncDispatch) resolve(t *testing.T, sentence string) {
	t.Helper()
	select {
	case <-d.chanFor(d.started, sentence):
	case <-time.After(2 * time.Second):
		t.Fatalf("sentence %q was never dispatched", sentence)
	}
	close(d.chanFor(d.gates, sentence))
}

// pipelinedReplier is an Agent over a scripted completion, streaming (the
// sentence-by-sentence path the pipeline accelerates) or batch (the
// single-dispatch fallback path).
func pipelinedReplier(deltas []string, streaming bool) *agent.Replier {
	cfg := agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Synthesizer: stubSynth{},
	}
	if streaming {
		cfg.Engine = &fakeStreamEngine{deltas: deltas}
	} else {
		cfg.Engine = batchEngine{reply: strings.Join(deltas, "")}
	}
	return agent.NewReplier(cfg)
}

// TestReplyStream_Pipelined_CommitsTailSentence pins the #626 wait barrier: the
// last sentence resolves AFTER the producer has finished streaming, so a turn that
// committed on its dispatch returns would drop it from history. The Agent waits
// for every sentence's resolution before committing what was delivered.
func TestReplyStream_Pipelined_CommitsTailSentence(t *testing.T) {
	r := pipelinedReplier([]string{"Aye. ", "Two rooms left."}, true)
	d := newAsyncDispatch("Aye.", "Two rooms left.")

	done := make(chan error, 1)
	go func() {
		done <- r.ReplyStream()(orchestrator.WithPipelinedDispatch(t.Context()), routed("bart", "rooms?"), d.dispatch)
	}()

	d.resolve(t, "Aye.")
	// The producer must still be waiting on the tail: it cannot commit history
	// before knowing whether the room heard the last sentence.
	select {
	case err := <-done:
		t.Fatalf("the turn returned (%v) before its tail sentence resolved", err)
	case <-time.After(50 * time.Millisecond):
	}
	d.resolve(t, "Two rooms left.")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReplyStream = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the turn never returned after its tail resolved")
	}

	got := assistantText(t, r)
	for _, want := range []string{"Aye.", "Two rooms left."} {
		if !strings.Contains(got, want) {
			t.Fatalf("committed reply = %q, want it to contain %q", got, want)
		}
	}
}

// TestReplyStream_Pipelined_CutSentenceNeverCommits pins ADR-0012 under the
// pipeline: a sentence the room never heard (cut mid-delivery by a barge) must not
// enter history, even though the dispatch that carried it returned "keep going".
func TestReplyStream_Pipelined_CutSentenceNeverCommits(t *testing.T) {
	r := pipelinedReplier([]string{"Aye. ", "Two rooms left."}, true)
	d := newAsyncDispatch("Aye.", "Two rooms left.")
	d.script("Two rooms left.", orchestrator.SentenceCut)

	done := make(chan error, 1)
	go func() {
		done <- r.ReplyStream()(orchestrator.WithPipelinedDispatch(t.Context()), routed("bart", "rooms?"), d.dispatch)
	}()
	d.resolve(t, "Aye.")
	d.resolve(t, "Two rooms left.")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the turn never returned after the cut")
	}

	got := assistantText(t, r)
	if strings.Contains(got, "Two rooms left.") {
		t.Fatalf("committed reply = %q, want the cut sentence absent (ADR-0012)", got)
	}
	if !strings.Contains(got, "Aye.") {
		t.Fatalf("committed reply = %q, want the delivered sentence kept", got)
	}
}

// TestFallbackTurn_Pipelined_CommitsOnlyOnDelivery pins the non-streaming engine
// path under the pipeline: its single dispatch returns before the reply resolved,
// so the commit must hang off the delivery hook — and a barge must leave history
// empty.
func TestFallbackTurn_Pipelined_CommitsOnlyOnDelivery(t *testing.T) {
	t.Run("delivered commits", func(t *testing.T) {
		r := pipelinedReplier([]string{"I am Bart."}, false)
		d := newAsyncDispatch("I am Bart.")
		done := make(chan error, 1)
		go func() {
			done <- r.ReplyStream()(orchestrator.WithPipelinedDispatch(t.Context()), routed("bart", "who?"), d.dispatch)
		}()
		d.resolve(t, "I am Bart.")
		if err := <-done; err != nil {
			t.Fatalf("ReplyStream = %v, want nil", err)
		}
		if got := assistantText(t, r); !strings.Contains(got, "I am Bart.") {
			t.Fatalf("committed reply = %q, want the delivered reply", got)
		}
	})

	t.Run("cut commits nothing", func(t *testing.T) {
		r := pipelinedReplier([]string{"I am Bart."}, false)
		d := newAsyncDispatch("I am Bart.")
		d.script("I am Bart.", orchestrator.SentenceCut)
		done := make(chan error, 1)
		go func() {
			done <- r.ReplyStream()(orchestrator.WithPipelinedDispatch(t.Context()), routed("bart", "who?"), d.dispatch)
		}()
		d.resolve(t, "I am Bart.")
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("ReplyStream = %v, want nil or the cut", err)
		}
		if got := assistantText(t, r); got != "" {
			t.Fatalf("committed reply = %q, want nothing (the room heard nothing)", got)
		}
	})
}

// assistantText returns the concatenated assistant history the turn committed.
func assistantText(t *testing.T, r *agent.Replier) string {
	t.Helper()
	var b strings.Builder
	for _, m := range r.HistorySnapshot() {
		if m.Role == llm.RoleAssistant {
			b.WriteString(m.Text)
		}
	}
	return b.String()
}
