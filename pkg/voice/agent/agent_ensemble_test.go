package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
)

// TestReplier_Draft_IsPure pins the Ensemble Turn's speculative-fan-out contract
// (ADR-0025/0012, #301): Draft produces the Agent's would-be reply text WITHOUT
// mutating anything — no user message appended, no assistant message committed —
// so a LOSING candidate in the Lead race commits nothing. It reads the same Hot
// Context the LLM turn would, but on a history SNAPSHOT.
func TestReplier_Draft_IsPure(t *testing.T) {
	prov := &fakeProvider{reply: "Aye, traveler."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
	})

	before := r.HistorySnapshot()

	draft, err := r.Draft(t.Context(), "", "Hail, Bart.")
	if err != nil {
		t.Fatalf("Draft errored: %v", err)
	}
	if !strings.Contains(draft, "traveler") {
		t.Fatalf("draft = %q, want the scripted reply", draft)
	}
	// The user message the LLM saw must be in the request, on a COPY of history.
	last := prov.lastRequest(t)
	if last.Messages[len(last.Messages)-1].Text != "Hail, Bart." {
		t.Fatalf("last message = %q, want the drafted user utterance", last.Messages[len(last.Messages)-1].Text)
	}
	// Purity: history is byte-identical after Draft (no user msg, no assistant msg).
	after := r.HistorySnapshot()
	if len(after) != len(before) {
		t.Fatalf("Draft mutated history: len before=%d after=%d (a losing draft must commit NOTHING)", len(before), len(after))
	}
}

// TestReplier_Draft_CancelledCtxErrorsAndCommitsNothing pins that a Draft whose
// ctx is already cancelled (the loser's shared draftCtx after the winner is
// elected) returns an error, produces no text, and still mutates no history.
func TestReplier_Draft_CancelledCtxErrorsAndCommitsNothing(t *testing.T) {
	prov := &fakeProvider{reply: "Aye, traveler."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	draft, err := r.Draft(ctx, "", "Hail, Bart.")
	if err == nil {
		t.Fatal("Draft on a cancelled ctx must return an error")
	}
	if draft != "" {
		t.Fatalf("draft = %q, want empty on a cancelled ctx", draft)
	}
	if len(r.HistorySnapshot()) != 0 {
		t.Fatal("a cancelled Draft must commit nothing to history")
	}
}

// draftReplier builds a Replier with no live LLM (SpeakDraft never generates — it
// speaks a supplied draft), so a nil-ish provider is fine via the scripted fake.
func draftReplier() *agent.Replier {
	return agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    &fakeProvider{reply: "unused"},
		Synthesizer: stubSynth{},
	})
}

// TestReplier_SpeakDraft_CommitsUserMsgAndDelivered pins the winning Lead's commit
// (ADR-0012, #301): SpeakDraft appends the user message (parity with the streaming
// turn), sentence-splits the supplied draft, dispatches each in order, and commits
// the DELIVERED text as the assistant turn — so the Lead's turn lands in history
// exactly as an LLM turn would.
func TestReplier_SpeakDraft_CommitsUserMsgAndDelivered(t *testing.T) {
	r := draftReplier()

	var got []string
	dispatch := func(rep orchestrator.Reply) error {
		got = append(got, rep.Sentence)
		return nil
	}

	delivered, err := r.SpeakDraft(t.Context(), "", "Hail, Bart.", "First sentence. Second sentence.", dispatch)
	if err != nil {
		t.Fatalf("SpeakDraft errored: %v", err)
	}
	if want := "First sentence. Second sentence."; delivered != want {
		t.Fatalf("delivered = %q, want %q", delivered, want)
	}
	if len(got) != 2 || got[0] != "First sentence." || got[1] != "Second sentence." {
		t.Fatalf("dispatched sentences = %v, want the two split sentences in order", got)
	}
	hist := r.HistorySnapshot()
	if len(hist) != 2 {
		t.Fatalf("history len = %d, want 2 (user + assistant)", len(hist))
	}
	if hist[0].Text != "Hail, Bart." {
		t.Fatalf("history[0] = %q, want the user utterance", hist[0].Text)
	}
	if hist[1].Text != "First sentence. Second sentence." {
		t.Fatalf("history[1] = %q, want the delivered assistant text", hist[1].Text)
	}
}

// TestReplier_SpeakDraft_ZeroDeliveredCommitsNothing pins the #375 F3 lazy-append
// delta (ADR-0012: "If zero sentences were delivered the utterance is not logged at
// all"): a SpeakDraft where NO sentence is delivered — every dispatch start-errors
// (ErrNotDelivered) or the turn is cut before the first — commits NOTHING, not even
// the user message. This is a DELIBERATE change from the old eager prologue append,
// which left an orphan user message on a zero-delivered turn.
func TestReplier_SpeakDraft_ZeroDeliveredCommitsNothing(t *testing.T) {
	t.Run("all sentences start-error", func(t *testing.T) {
		r := draftReplier()
		dispatch := func(orchestrator.Reply) error { return orchestrator.ErrNotDelivered }
		delivered, err := r.SpeakDraft(t.Context(), "", "Hail, Bart.", "First. Second.", dispatch)
		if err != nil {
			t.Fatalf("SpeakDraft errored: %v", err)
		}
		if delivered != "" {
			t.Fatalf("delivered = %q, want empty (nothing was delivered)", delivered)
		}
		if hist := r.HistorySnapshot(); len(hist) != 0 {
			t.Fatalf("history = %+v, want EMPTY — a zero-delivered turn logs nothing, incl. the user msg (ADR-0012)", hist)
		}
	})

	t.Run("cut before the first sentence", func(t *testing.T) {
		r := draftReplier()
		ctx, cancel := context.WithCancel(t.Context())
		cancel() // barge before anything dispatched
		dispatch := func(orchestrator.Reply) error { return ctx.Err() }
		delivered, err := r.SpeakDraft(ctx, "", "Hail, Bart.", "First. Second.", dispatch)
		if err != nil {
			t.Fatalf("SpeakDraft on a barge must not surface an error: %v", err)
		}
		if delivered != "" {
			t.Fatalf("delivered = %q, want empty", delivered)
		}
		if hist := r.HistorySnapshot(); len(hist) != 0 {
			t.Fatalf("history = %+v, want EMPTY — a cut-before-delivery turn logs nothing (ADR-0012)", hist)
		}
	})
}

// TestReplier_SpeakDraft_CutMidDraftCommitsDeliveredOnly pins the barge path: a
// dispatch that reports the turn cancelled mid-draft stops the drain, and only the
// sentences forwarded BEFORE the cut are committed (ADR-0012 delivered-only).
func TestReplier_SpeakDraft_CutMidDraftCommitsDeliveredOnly(t *testing.T) {
	r := draftReplier()

	ctx, cancel := context.WithCancel(t.Context())
	var got []string
	dispatch := func(rep orchestrator.Reply) error {
		if ctx.Err() != nil {
			return ctx.Err() // the turn was already cut — this sentence is not delivered
		}
		got = append(got, rep.Sentence)
		cancel() // a barge cuts the turn right after the first sentence is forwarded
		return nil
	}

	delivered, err := r.SpeakDraft(ctx, "", "Hail, Bart.", "First. Second. Third.", dispatch)
	if err != nil {
		t.Fatalf("SpeakDraft on a barge must not surface an error: %v", err)
	}
	if delivered != "First." {
		t.Fatalf("delivered = %q, want only the first (forwarded) sentence", delivered)
	}
	if len(got) != 1 {
		t.Fatalf("dispatched %d sentences, want 1 before the cut", len(got))
	}
	hist := r.HistorySnapshot()
	if len(hist) != 2 || hist[1].Text != "First." {
		t.Fatalf("history = %+v, want user + assistant committing only the delivered 'First.'", hist)
	}
}

// TestReplier_SpeakDraft_StartErrorSkipsSentenceAndContinues pins the real
// SpeakDraft against the #362 three-class contract: a dispatch returning
// [orchestrator.ErrNotDelivered] for sentence 1 (a TTS start-error under a live
// turn) SKIPS that sentence's commit but keeps draining — sentence 2 is
// delivered and committed. This drives the REAL agent.go:408 continue-on-sentinel
// (not a fake that reimplements the skip), so flipping it to `return e` goes RED.
func TestReplier_SpeakDraft_StartErrorSkipsSentenceAndContinues(t *testing.T) {
	r := draftReplier()

	var got []string
	dispatch := func(rep orchestrator.Reply) error {
		got = append(got, rep.Sentence)
		if rep.Sentence == "First." {
			return orchestrator.ErrNotDelivered // start-error: NOT delivered, turn alive
		}
		return nil // delivered
	}

	delivered, err := r.SpeakDraft(t.Context(), "", "Hail, Bart.", "First. Second.", dispatch)
	if err != nil {
		t.Fatalf("SpeakDraft errored on a start-error sentinel: %v", err)
	}
	// The drain CONTINUED past the start-errored sentence: both were attempted.
	if len(got) != 2 || got[0] != "First." || got[1] != "Second." {
		t.Fatalf("dispatched = %v, want both sentences attempted in order (drain continues past start-error)", got)
	}
	// Only the delivered sentence is committed — the start-errored one is skipped.
	if delivered != "Second." {
		t.Fatalf("delivered = %q, want only the delivered 'Second.' (start-errored 'First.' skipped)", delivered)
	}
	hist := r.HistorySnapshot()
	if len(hist) != 2 || hist[1].Text != "Second." {
		t.Fatalf("history = %+v, want user + assistant committing only the delivered 'Second.'", hist)
	}
}

// TestReplier_SpeakReaction_StartErrorSkipsSentenceAndContinues pins that
// SpeakReaction inherits the same continue-on-sentinel behaviour (it delegates to
// SpeakDraft with the composite Cross-talk user text, #302/#362): a start-errored
// reaction sentence is skipped, the drain continues, and only the delivered
// sentence commits.
func TestReplier_SpeakReaction_StartErrorSkipsSentenceAndContinues(t *testing.T) {
	r := draftReplier()

	var got []string
	dispatch := func(rep orchestrator.Reply) error {
		got = append(got, rep.Sentence)
		if rep.Sentence == "Aye." {
			return orchestrator.ErrNotDelivered
		}
		return nil
	}

	delivered, err := r.SpeakReaction(t.Context(), "", "Bart, thoughts?", "Greta", "We ride.", "Aye. Indeed.", dispatch)
	if err != nil {
		t.Fatalf("SpeakReaction errored on a start-error sentinel: %v", err)
	}
	if len(got) != 2 || got[0] != "Aye." || got[1] != "Indeed." {
		t.Fatalf("dispatched = %v, want both reaction sentences attempted (drain continues)", got)
	}
	if delivered != "Indeed." {
		t.Fatalf("delivered = %q, want only the delivered 'Indeed.'", delivered)
	}
	// The reaction commits the delivered text on the SAME composite user message
	// React reasoned over.
	hist := r.HistorySnapshot()
	if hist[len(hist)-1].Text != "Indeed." {
		t.Fatalf("committed reaction = %q, want only the delivered 'Indeed.'", hist[len(hist)-1].Text)
	}
}
