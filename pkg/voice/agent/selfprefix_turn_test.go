package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
)

// namedReplier is [streamReplier] for an Agent that knows its own display name —
// the production shape since the Roster wires Persona.Name from the NPC spec.
func namedReplier(t *testing.T, eng agent.Engine) *agent.Replier {
	t.Helper()
	return agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Name: "Bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      eng,
		Synthesizer: stubSynth{},
	})
}

// TestReplyStream_StripsSelfNamePrefix is the live bug (2026-07-27): shown Hot
// Context's "Name: text" grammar, the model imitates it in its OWN reply, and
// the label is read aloud — the NPC says its own name before every line. The
// streamed reply must reach TTS without it, and — because history is what feeds
// the next prompt — must be COMMITTED without it too, or the model keeps copying
// itself for the rest of the session.
func TestReplyStream_StripsSelfNamePrefix(t *testing.T) {
	eng := &fakeStreamEngine{deltas: []string{"Bart: Aye. ", "Two rooms left."}}
	r := namedReplier(t, eng)

	var spoken []string
	if err := r.ReplyStream()(context.Background(), routed("bart", "rooms?"), func(rep orchestrator.Reply) error {
		spoken = append(spoken, rep.Sentence)
		return nil
	}); err != nil {
		t.Fatalf("ReplyStream: %v", err)
	}

	want := []string{"Aye.", "Two rooms left."}
	if len(spoken) != len(want) {
		t.Fatalf("spoken = %q, want %q", spoken, want)
	}
	for i := range want {
		if spoken[i] != want[i] {
			t.Errorf("sentence %d = %q, want %q", i, spoken[i], want[i])
		}
	}

	for _, m := range r.HistorySnapshot() {
		if strings.Contains(m.Text, "Bart:") {
			t.Errorf("history committed the self-label, which re-teaches the model the format: %q", m.Text)
		}
	}
}

// TestReplyStream_LabelOnlyFirstSentenceStillSpeaksTheRest pins the degenerate
// split: when the label lands in its own sentence ("Bart:. Aye.") stripping it
// leaves nothing speakable, which must skip that dispatch rather than send TTS
// an empty request — the rest of the reply still has to be spoken.
func TestReplyStream_LabelOnlyFirstSentenceStillSpeaksTheRest(t *testing.T) {
	eng := &fakeStreamEngine{deltas: []string{"Bart:. ", "Aye, two rooms."}}
	r := namedReplier(t, eng)

	var spoken []string
	if err := r.ReplyStream()(context.Background(), routed("bart", "rooms?"), func(rep orchestrator.Reply) error {
		spoken = append(spoken, rep.Sentence)
		return nil
	}); err != nil {
		t.Fatalf("ReplyStream: %v", err)
	}

	if len(spoken) != 1 || spoken[0] != "Aye, two rooms." {
		t.Fatalf("spoken = %q, want [\"Aye, two rooms.\"]", spoken)
	}
}

// TestReplyStream_StripsLabelOnEveryLine pins the multi-line case: a model that
// adopts the transcript grammar tends to label EVERY line, not just the first,
// and every one of them would be read aloud.
func TestReplyStream_StripsLabelOnEveryLine(t *testing.T) {
	eng := &fakeStreamEngine{deltas: []string{"Bart: Aye. ", "Bart: Two rooms left."}}
	r := namedReplier(t, eng)

	var spoken []string
	if err := r.ReplyStream()(context.Background(), routed("bart", "rooms?"), func(rep orchestrator.Reply) error {
		spoken = append(spoken, rep.Sentence)
		return nil
	}); err != nil {
		t.Fatalf("ReplyStream: %v", err)
	}

	want := []string{"Aye.", "Two rooms left."}
	for i := range want {
		if i >= len(spoken) || spoken[i] != want[i] {
			t.Fatalf("spoken = %q, want %q", spoken, want)
		}
	}
}

// TestReplyStream_KeepsAnotherSpeakersLine guards the blast radius: only the
// Agent's OWN label is removed. An NPC quoting somebody else — the bread and
// butter of a tavern rumour — must keep every word.
func TestReplyStream_KeepsAnotherSpeakersLine(t *testing.T) {
	eng := &fakeStreamEngine{deltas: []string{"Greta said: the bridge is out."}}
	r := namedReplier(t, eng)

	var spoken []string
	if err := r.ReplyStream()(context.Background(), routed("bart", "news?"), func(rep orchestrator.Reply) error {
		spoken = append(spoken, rep.Sentence)
		return nil
	}); err != nil {
		t.Fatalf("ReplyStream: %v", err)
	}

	if len(spoken) != 1 || spoken[0] != "Greta said: the bridge is out." {
		t.Fatalf("spoken = %q, want the quoted line intact", spoken)
	}
}

// TestReply_StripsSelfNamePrefix covers the batch path (a non-streaming Engine):
// the same label, removed before the single dispatch and before the commit.
func TestReply_StripsSelfNamePrefix(t *testing.T) {
	r := namedReplier(t, batchEngine{reply: "Bart: Aye."})

	replies := r.Reply()(context.Background(), routed("bart", "rooms?"))
	if len(replies) != 1 || replies[0].Sentence != "Aye." {
		t.Fatalf("batch reply = %+v, want one sentence %q", replies, "Aye.")
	}
}
