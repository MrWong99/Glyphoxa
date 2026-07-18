package agent_test

import (
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
)

// artusasNamer resolves the test speaker "111" to "Artusas" — the bound player
// character — and everything else to "" (the generic label).
func artusasNamer(speakerID string) string {
	if speakerID == "111" {
		return "Artusas"
	}
	return ""
}

// TestSystemPrompt_SpeakerRoster_Section pins the speaker-attribution section:
// with a SpeakerName resolver wired AND a table roster configured, the system
// prompt carries a "## Who is speaking" section that (a) explains the "Name: text"
// user-line prefix as THE speaker identity, (b) lists the player characters as
// humans at the table, (c) lists the fellow NPCs by name, and (d) explains the
// generic "Player / DM" label — placed after the memory slot, before the markup.
func TestSystemPrompt_SpeakerRoster_Section(t *testing.T) {
	prov := &fakeProvider{reply: "Aye."}
	mem := &fakeRecaller{mem: agent.Memory{Personal: []string{"I served him ale."}}}
	r := agent.NewReplier(agent.Config{
		Persona:          agent.Persona{AgentID: "npc-lukas", Markdown: "You are Lukas.", Voice: testVoice()},
		Provider:         prov,
		Synthesizer:      stubSynth{},
		Memory:           mem,
		SpeakerName:      artusasNamer,
		PlayerCharacters: []string{"Artusas"},
		FellowNPCs:       []string{"Mehra", "Gebroner"},
	})

	r.Reply()(t.Context(), routedFrom("npc-lukas", "111", "wie geht es dir?"))

	sys := prov.lastRequest(t).Messages[0].Text
	for _, want := range []string{
		"## Who is speaking",
		`Each user line begins with the name of the HUMAN speaking it, as "Name: text". This prefix — not your persona, your memories, or past conversation — tells you who is addressing you.`,
		"Player characters at the table (each played by a human): Artusas.",
		"Fellow NPCs (AI-played like you; a user-line prefix never refers to them): Mehra, Gebroner.",
		`Lines prefixed "Player / DM" come from an unidentified human.`,
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing %q:\n%q", want, sys)
		}
	}
	// Slot order: persona < memory < speaker section < markup.
	iPersona := strings.Index(sys, "You are Lukas.")
	iMemory := strings.Index(sys, "I served him ale.")
	iSpeaker := strings.Index(sys, "## Who is speaking")
	iMarkup := strings.Index(sys, sentinelMarkup)
	if iPersona >= iMemory || iMemory >= iSpeaker || iSpeaker >= iMarkup {
		t.Errorf("slot order wrong (want persona<memory<speaker<markup): persona=%d memory=%d speaker=%d markup=%d\n%q",
			iPersona, iMemory, iSpeaker, iMarkup, sys)
	}
}

// TestSystemPrompt_NoRoster_ByteIdentical locks backward compat: with SpeakerName
// wired but NO roster configured (the pre-feature live config), the prompt is
// byte-identical to the pre-roster path — the section emits zero bytes.
func TestSystemPrompt_NoRoster_ByteIdentical(t *testing.T) {
	prov := &fakeProvider{reply: "Aye."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart, the innkeeper.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		SpeakerName: artusasNamer,
		// PlayerCharacters / FellowNPCs deliberately unset.
	})

	r.Reply()(t.Context(), routedFrom("bart", "111", "Hello, innkeeper."))

	sys := prov.lastRequest(t).Messages[0].Text
	want := "You are Bart, the innkeeper.\n\n" + sentinelMarkup
	if sys != want {
		t.Errorf("no-roster prompt not byte-identical:\n got %q\nwant %q", sys, want)
	}
}

// TestSystemPrompt_RosterWithoutSpeakerName_Absent pins the coherence guard: a
// roster configured WITHOUT a SpeakerName resolver renders no section — the
// section describes the "Name: text" prefix, and with a nil resolver user lines
// carry no prefix, so the section would lie. The prompt stays byte-identical.
func TestSystemPrompt_RosterWithoutSpeakerName_Absent(t *testing.T) {
	prov := &fakeProvider{reply: "Aye."}
	r := agent.NewReplier(agent.Config{
		Persona:          agent.Persona{AgentID: "bart", Markdown: "You are Bart, the innkeeper.", Voice: testVoice()},
		Provider:         prov,
		Synthesizer:      stubSynth{},
		PlayerCharacters: []string{"Artusas"},
		FellowNPCs:       []string{"Mehra"},
		// SpeakerName deliberately nil: no prefixes, so no section.
	})

	r.Reply()(t.Context(), routed("bart", "Hello, innkeeper."))

	sys := prov.lastRequest(t).Messages[0].Text
	want := "You are Bart, the innkeeper.\n\n" + sentinelMarkup
	if sys != want {
		t.Errorf("roster-without-resolver prompt not byte-identical:\n got %q\nwant %q", sys, want)
	}
}

// TestSystemPrompt_SpeakerRoster_NPCNameCollision is the live-campaign worst case
// (the Gebronies bug): an NPC named "Lukas" shares its name with a human's guild
// display name. The prompt must list "Lukas" under fellow NPCs while the user
// line carries the resolved PLAYER name — so the model can tell "Artusas: ..."
// is the human addressing it, never the NPC Lukas speaking.
func TestSystemPrompt_SpeakerRoster_NPCNameCollision(t *testing.T) {
	prov := &fakeProvider{reply: "Hallo."}
	r := agent.NewReplier(agent.Config{
		Persona:          agent.Persona{AgentID: "npc-mehra", Markdown: "You are Mehra.", Voice: testVoice()},
		Provider:         prov,
		Synthesizer:      stubSynth{},
		SpeakerName:      artusasNamer,
		PlayerCharacters: []string{"Artusas"},
		FellowNPCs:       []string{"Lukas", "Gebroner"},
	})

	r.Reply()(t.Context(), routedFrom("npc-mehra", "111", "Lukas hat dich gegrüßt."))

	req := prov.lastRequest(t)
	sys := req.Messages[0].Text
	if !strings.Contains(sys, "Fellow NPCs (AI-played like you; a user-line prefix never refers to them): Lukas, Gebroner.") {
		t.Errorf("colliding NPC name not listed under fellow NPCs:\n%q", sys)
	}
	if !strings.Contains(sys, "Player characters at the table (each played by a human): Artusas.") {
		t.Errorf("player character missing from the humans list:\n%q", sys)
	}
	userLine := req.Messages[len(req.Messages)-1].Text
	if userLine != "Artusas: Lukas hat dich gegrüßt." {
		t.Errorf("user line = %q, want the PLAYER-name prefix \"Artusas: Lukas hat dich gegrüßt.\"", userLine)
	}
}
