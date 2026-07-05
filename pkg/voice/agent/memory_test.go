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

// TestSystemPrompt_Memory_LabeledSectionsInSlotOrder pins AC2 + AC5: a non-zero
// Memory injects a memory block between the Persona and the audio markup, with
// the Personal chunks under a witnessed heading and the World chunks framed as
// possibly second-hand (rumor). Slot order is Persona → memory → markup.
func TestSystemPrompt_Memory_LabeledSectionsInSlotOrder(t *testing.T) {
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

	sys := prov.lastRequest(t).Messages[0].Text

	// Both chunk contents present, each under the right framing.
	if !strings.Contains(sys, "The ruby dagger was stolen from the vault.") {
		t.Errorf("system prompt missing personal chunk: %q", sys)
	}
	if !strings.Contains(sys, "A dragon was seen near the northern pass.") {
		t.Errorf("system prompt missing world chunk: %q", sys)
	}
	// World context framed as second-hand / rumor (ADR-0011).
	if !strings.Contains(strings.ToLower(sys), "second-hand") && !strings.Contains(strings.ToLower(sys), "rumor") {
		t.Errorf("world context not framed as possibly second-hand: %q", sys)
	}

	// Slot order: Persona precedes the memory block precedes the markup.
	iPersona := strings.Index(sys, "You are Bart.")
	iPersonal := strings.Index(sys, "The ruby dagger was stolen from the vault.")
	iWorld := strings.Index(sys, "A dragon was seen near the northern pass.")
	iMarkup := strings.Index(sys, sentinelMarkup)
	ordered := iPersona < iPersonal && iPersonal < iWorld && iWorld < iMarkup
	if !ordered {
		t.Errorf("slot order wrong (want persona<personal<world<markup): "+
			"persona=%d personal=%d world=%d markup=%d\n%q",
			iPersona, iPersonal, iWorld, iMarkup, sys)
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

	sys := prov.lastRequest(t).Messages[0].Text
	if !strings.Contains(sys, "I served him ale last night.") {
		t.Errorf("system prompt missing personal chunk: %q", sys)
	}
	// No world subsection when World is empty.
	if strings.Contains(strings.ToLower(sys), "second-hand") || strings.Contains(strings.ToLower(sys), "rumor") {
		t.Errorf("empty World half must omit its second-hand subsection: %q", sys)
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
