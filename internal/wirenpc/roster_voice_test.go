package wirenpc

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
)

// silentRoster builds a Roster with a silent replier factory (no engine needed) —
// enough to exercise the spec bookkeeping Roster.Voice reads.
func silentRoster() *Roster {
	return newRoster(rosterDeps{replierFor: func(s npcSpec) *agent.Replier {
		return agent.NewReplier(agent.Config{
			Persona:     agent.Persona{AgentID: s.agentID, Voice: s.voice},
			Engine:      scriptEngine{},
			Synthesizer: &recordingSynth{},
		})
	}})
}

// TestRosterVoice_HeldNPC pins the /say voice lookup (#295): a held NPC's Voice is
// returned by agentID, so the DirectSpeech reactor renders /say in the right voice.
func TestRosterVoice_HeldNPC(t *testing.T) {
	r := silentRoster()
	r.AddNPC(specFor("bart", "Bart", ""))

	v, ok := r.Voice("bart")
	if !ok {
		t.Fatal("Voice(bart) not found, want the held NPC's voice")
	}
	if v.VoiceID != "bart" || v.Name != "Bart" {
		t.Errorf("Voice(bart) = %+v, want the specFor voice", v)
	}
}

// TestRosterVoice_UnknownNPC pins the miss: an id the Roster never held reports
// ok=false (the DirectSpeech reactor then ends the turn rather than panicking).
func TestRosterVoice_UnknownNPC(t *testing.T) {
	r := silentRoster()
	r.AddNPC(specFor("bart", "Bart", ""))

	if _, ok := r.Voice("ghost"); ok {
		t.Fatal("Voice(ghost) found, want ok=false for an unheld id")
	}
}

// TestRosterVoice_RemovedNPC pins that a removed NPC's Voice is gone, so a /say for
// a just-removed NPC ends cleanly rather than voicing a stale spec.
func TestRosterVoice_RemovedNPC(t *testing.T) {
	r := silentRoster()
	r.AddNPC(specFor("bart", "Bart", ""))
	r.RemoveNPC("bart")

	if _, ok := r.Voice("bart"); ok {
		t.Fatal("Voice(bart) found after RemoveNPC, want ok=false")
	}
}
