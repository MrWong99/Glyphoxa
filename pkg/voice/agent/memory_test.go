package agent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// fakeRecaller is a scripted [agent.MemoryRecaller]: it records the args it was
// called with and returns a fixed Memory (or runs a probe hook). nil probe/mem
// yields zero Memory.
type fakeRecaller struct {
	mem     agent.Memory
	calls   int
	agentID string
	text    string
	probe   func()
}

func (f *fakeRecaller) Recall(_ context.Context, agentID, utterance string) agent.Memory {
	f.calls++
	f.agentID = agentID
	f.text = utterance
	if f.probe != nil {
		f.probe()
	}
	return f.mem
}

// TestSystemPrompt_NilMemory_ByteIdenticalToToday locks AC6: with Config.Memory
// nil the assembled system prompt is exactly Persona + markup, byte-for-byte the
// pre-memory behavior — no memory block, no stray whitespace.
func TestSystemPrompt_NilMemory_ByteIdenticalToToday(t *testing.T) {
	prov := &fakeProvider{reply: "Aye."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart, the innkeeper.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		// Memory deliberately nil: today's behavior exactly.
	})

	r.Reply()(t.Context(), routed("bart", "Hello, innkeeper."))

	sys := prov.lastRequest(t).Messages[0]
	if sys.Role != llm.RoleSystem {
		t.Fatalf("first message role = %q, want system", sys.Role)
	}
	want := "You are Bart, the innkeeper.\n\n" + sentinelMarkup
	if sys.Text != want {
		t.Errorf("nil-memory system prompt not byte-identical to today:\n got %q\nwant %q", sys.Text, want)
	}
}

// TestVolatileTail_Memory_LabeledSections pins AC2 + AC5 under the ADR-0059
// volatile-tail layout: a non-zero Memory injects a memory block into the
// TRAILING system message (never the stable system prompt), with the Personal
// chunks under a witnessed heading and the World chunks framed as possibly
// second-hand (rumor), Personal before World.
func TestVolatileTail_Memory_LabeledSections(t *testing.T) {
	prov := &fakeProvider{reply: "Aye."}
	rec := &fakeRecaller{mem: agent.Memory{
		Personal: []string{"The ruby dagger was stolen from the vault."},
		World:    []string{"A dragon was seen near the northern pass."},
	}}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		Memory:      rec,
	})

	r.Reply()(t.Context(), routed("bart", "Do you remember the ruby dagger?"))

	if rec.calls != 1 {
		t.Fatalf("recaller called %d times, want 1", rec.calls)
	}
	if rec.agentID != "bart" || rec.text != "Do you remember the ruby dagger?" {
		t.Errorf("recaller args = (%q, %q), want (bart, the utterance)", rec.agentID, rec.text)
	}

	msgs := prov.lastRequest(t).Messages
	// The STABLE system prompt stays free of per-turn memory (ADR-0059).
	if sys := msgs[0].Text; strings.Contains(sys, "The ruby dagger was stolen from the vault.") {
		t.Errorf("recalled memory leaked into the stable system prompt: %q", sys)
	}
	tail := volatileTail(t, msgs)

	// Both chunk contents present, each under the right framing.
	if !strings.Contains(tail, "The ruby dagger was stolen from the vault.") {
		t.Errorf("volatile tail missing personal chunk: %q", tail)
	}
	if !strings.Contains(tail, "A dragon was seen near the northern pass.") {
		t.Errorf("volatile tail missing world chunk: %q", tail)
	}
	// World context framed as "may not personally know" (ADR-0011), NOT an
	// assertion the NPC was absent.
	if !strings.Contains(strings.ToLower(tail), "may not have witnessed") {
		t.Errorf("world context not framed as possibly-not-witnessed (ADR-0011): %q", tail)
	}

	// Personal (witnessed) precedes World (second-hand) within the block.
	iPersonal := strings.Index(tail, "The ruby dagger was stolen from the vault.")
	iWorld := strings.Index(tail, "A dragon was seen near the northern pass.")
	if !(iPersonal >= 0 && iWorld >= 0 && iPersonal < iWorld) {
		t.Errorf("memory section order wrong (want personal<world): personal=%d world=%d\n%q",
			iPersonal, iWorld, tail)
	}
}

// TestSystemPrompt_Memory_OmitsEmptyHalves pins that an empty Personal or World
// half drops its whole subsection (heading + bullets), never emits an empty
// labeled list.
func TestSystemPrompt_Memory_OmitsEmptyHalves(t *testing.T) {
	prov := &fakeProvider{reply: "Aye."}
	rec := &fakeRecaller{mem: agent.Memory{
		Personal: []string{"I served him ale last night."},
		// World empty.
	}}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		Memory:      rec,
	})

	r.Reply()(t.Context(), routed("bart", "Who came in last night?"))

	tail := volatileTail(t, prov.lastRequest(t).Messages)
	if !strings.Contains(tail, "I served him ale last night.") {
		t.Errorf("volatile tail missing personal chunk: %q", tail)
	}
	// No world subsection when World is empty.
	if strings.Contains(strings.ToLower(tail), "may not have witnessed") {
		t.Errorf("empty World half must omit its world-context subsection: %q", tail)
	}
}

// TestRecall_RunsOutsideHistoryLock proves recall runs OUTSIDE r.mu (ADR-0042):
// a MemoryRecaller that probes the loop's own lock (HistorySnapshot) must not
// deadlock. If recall ran while the loop held r.mu this test would hang.
func TestRecall_RunsOutsideHistoryLock(t *testing.T) {
	prov := &fakeProvider{reply: "Aye."}
	rec := &fakeRecaller{}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		Memory:      rec,
	})
	// The probe reaches back into the loop's mutex-guarded history — a deadlock if
	// recall were called under r.mu.
	rec.probe = func() { _ = r.HistorySnapshot() }

	done := make(chan struct{})
	go func() {
		r.Reply()(t.Context(), routed("bart", "Hello."))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("turn deadlocked: recall must run outside the history lock")
	}
	if rec.calls != 1 {
		t.Errorf("recaller called %d times, want 1", rec.calls)
	}
}
