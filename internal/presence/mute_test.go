package presence

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeMuter is a SessionMuter for the mute-command tests.
type fakeMuter struct {
	active     bool
	campaignID uuid.UUID
	agentCalls []muteCall
	allCalls   []bool
	mutedIDs   []string
}

type muteCall struct {
	id    string
	muted bool
}

func (f *fakeMuter) Snapshot() (storage.VoiceSession, bool) {
	return storage.VoiceSession{CampaignID: f.campaignID}, f.active
}

func (f *fakeMuter) SetAgentMute(_ context.Context, id string, muted bool) ([]string, error) {
	f.agentCalls = append(f.agentCalls, muteCall{id, muted})
	return f.mutedIDs, nil
}

func (f *fakeMuter) SetAllMute(_ context.Context, muted bool) ([]string, error) {
	f.allCalls = append(f.allCalls, muted)
	return f.mutedIDs, nil
}

// fakeLister is an AgentLister for the mute-command tests.
type fakeLister struct {
	agents []storage.Agent
	err    error
}

func (f *fakeLister) ListAgents(context.Context, uuid.UUID) ([]storage.Agent, error) {
	return f.agents, f.err
}

func muteIC(resp *fakeResponder, npc string) *Interaction {
	return &Interaction{
		guildID: testGuild,
		userID:  operatorID,
		opts:    fakeOpts{s: map[string]string{"npc": npc}},
		resp:    resp,
	}
}

func muteAC(npc string) *Autocomplete {
	return &Autocomplete{
		guildID: testGuild,
		userID:  operatorID,
		data: discord.AutocompleteInteractionData{
			CommandName:    commandGroup,
			SubCommandName: strPtr("mute"),
			Options: map[string]discord.AutocompleteOption{
				"npc": {
					Name:    "npc",
					Type:    discord.ApplicationCommandOptionTypeString,
					Value:   json.RawMessage(`"` + npc + `"`),
					Focused: true,
				},
			},
		},
	}
}

func strPtr(s string) *string { return &s }

// TestMuteCommand_IsGMOnlyWithAutocomplete pins the command shape (AC4): GM-only,
// a required "npc" string option, and an autocomplete handler.
func TestMuteCommand_IsGMOnlyWithAutocomplete(t *testing.T) {
	cmd := MuteCommand(&fakeMuter{}, &fakeLister{})
	if cmd.Path != "glyphoxa mute" || !cmd.GMOnly || cmd.Autocomplete == nil {
		t.Fatalf("MuteCommand shape = {path %q GMOnly %v autocomplete %v}, want GM-only /glyphoxa mute with autocomplete", cmd.Path, cmd.GMOnly, cmd.Autocomplete != nil)
	}
	if len(cmd.Options) != 1 {
		t.Fatalf("MuteCommand options = %d, want 1 (npc)", len(cmd.Options))
	}
}

// TestMuteCommand_IdleEphemeral pins the active-session requirement (AC4): with no
// Voice Session, the handler replies ephemerally and mutes nothing.
func TestMuteCommand_IdleEphemeral(t *testing.T) {
	mgr := &fakeMuter{active: false}
	cmd := MuteCommand(mgr, &fakeLister{})
	resp := &fakeResponder{}
	if err := cmd.Handle(context.Background(), muteIC(resp, "Bart")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("idle reply = %+v, want one ephemeral", resp.replies)
	}
	if len(mgr.agentCalls) != 0 {
		t.Fatalf("idle handler muted %v, want nothing", mgr.agentCalls)
	}
}

// TestMuteCommand_ResolvesUUIDValue pins the autocomplete-picked path: the npc
// value is an Agent UUID, resolved against the roster, and muted.
func TestMuteCommand_ResolvesUUIDValue(t *testing.T) {
	bart := storage.Agent{ID: uuid.New(), Name: "Bart"}
	mgr := &fakeMuter{active: true, campaignID: uuid.New()}
	cmd := MuteCommand(mgr, &fakeLister{agents: []storage.Agent{bart}})
	resp := &fakeResponder{}
	if err := cmd.Handle(context.Background(), muteIC(resp, bart.ID.String())); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(mgr.agentCalls) != 1 || mgr.agentCalls[0].id != bart.ID.String() || !mgr.agentCalls[0].muted {
		t.Fatalf("mute calls = %+v, want one {%s true}", mgr.agentCalls, bart.ID)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral || !strings.Contains(resp.replies[0].content, "Bart") {
		t.Fatalf("success reply = %+v, want an ephemeral naming Bart", resp.replies)
	}
}

// TestMuteCommand_ResolvesFreeTextName pins the typed-name path: a case-insensitive
// name (then alias) match resolves to the Agent and mutes it.
func TestMuteCommand_ResolvesFreeTextName(t *testing.T) {
	bart := storage.Agent{ID: uuid.New(), Name: "Bart", Aliases: []string{"Innkeeper"}}
	mgr := &fakeMuter{active: true, campaignID: uuid.New()}
	cmd := MuteCommand(mgr, &fakeLister{agents: []storage.Agent{bart}})

	// By name (different case).
	resp := &fakeResponder{}
	if err := cmd.Handle(context.Background(), muteIC(resp, "bart")); err != nil {
		t.Fatalf("Handle name: %v", err)
	}
	// By alias.
	resp2 := &fakeResponder{}
	if err := cmd.Handle(context.Background(), muteIC(resp2, "innkeeper")); err != nil {
		t.Fatalf("Handle alias: %v", err)
	}
	if len(mgr.agentCalls) != 2 || mgr.agentCalls[0].id != bart.ID.String() || mgr.agentCalls[1].id != bart.ID.String() {
		t.Fatalf("mute calls = %+v, want two for Bart (name + alias)", mgr.agentCalls)
	}
}

// TestMuteCommand_UnknownNameEphemeral pins AC4's clear error: an unknown name
// replies ephemerally naming the input and mutes nothing.
func TestMuteCommand_UnknownNameEphemeral(t *testing.T) {
	mgr := &fakeMuter{active: true, campaignID: uuid.New()}
	cmd := MuteCommand(mgr, &fakeLister{agents: []storage.Agent{{ID: uuid.New(), Name: "Bart"}}})
	resp := &fakeResponder{}
	if err := cmd.Handle(context.Background(), muteIC(resp, "Zaphod")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral || !strings.Contains(resp.replies[0].content, "Zaphod") {
		t.Fatalf("unknown-name reply = %+v, want an ephemeral naming the input", resp.replies)
	}
	if len(mgr.agentCalls) != 0 {
		t.Fatalf("unknown name muted %v, want nothing", mgr.agentCalls)
	}
}

// TestMuteCommand_Autocomplete pins the autocomplete: prefix-filtered choices whose
// Value is the Agent UUID and Name the display name, capped at 25; empty when idle.
func TestMuteCommand_Autocomplete(t *testing.T) {
	bart := storage.Agent{ID: uuid.New(), Name: "Bart"}
	greta := storage.Agent{ID: uuid.New(), Name: "Greta"}
	mgr := &fakeMuter{active: true, campaignID: uuid.New()}
	cmd := MuteCommand(mgr, &fakeLister{agents: []storage.Agent{bart, greta}})

	choices, err := cmd.Autocomplete(context.Background(), muteAC("bar"))
	if err != nil {
		t.Fatalf("Autocomplete: %v", err)
	}
	if len(choices) != 1 {
		t.Fatalf("prefix 'bar' choices = %d, want 1 (Bart)", len(choices))
	}
	if choices[0].ChoiceName() != "Bart" {
		t.Fatalf("choice name = %q, want Bart", choices[0].ChoiceName())
	}
	sc, ok := choices[0].(discord.AutocompleteChoiceString)
	if !ok || sc.Value != bart.ID.String() {
		t.Fatalf("choice value = %v, want the Agent UUID %s", choices[0], bart.ID)
	}

	// Idle: no choices (no campaign to list against).
	mgr.active = false
	idle, err := cmd.Autocomplete(context.Background(), muteAC(""))
	if err != nil {
		t.Fatalf("Autocomplete idle: %v", err)
	}
	if len(idle) != 0 {
		t.Fatalf("idle autocomplete = %d choices, want 0", len(idle))
	}
}

// TestMuteCommand_AutocompleteCapsAt25 pins the Discord 25-choice limit.
func TestMuteCommand_AutocompleteCapsAt25(t *testing.T) {
	agents := make([]storage.Agent, 30)
	for i := range agents {
		agents[i] = storage.Agent{ID: uuid.New(), Name: "Agent"}
	}
	cmd := MuteCommand(&fakeMuter{active: true, campaignID: uuid.New()}, &fakeLister{agents: agents})
	choices, err := cmd.Autocomplete(context.Background(), muteAC(""))
	if err != nil {
		t.Fatalf("Autocomplete: %v", err)
	}
	if len(choices) > 25 {
		t.Fatalf("autocomplete returned %d choices, want <= 25", len(choices))
	}
}

// TestMuteAllCommand pins /glyphoxa muteall (AC4): GM-only, idle-ephemeral, and an
// active session mutes every Agent (SetAllMute(true)).
func TestMuteAllCommand(t *testing.T) {
	cmd := MuteAllCommand(&fakeMuter{})
	if cmd.Path != "glyphoxa muteall" || !cmd.GMOnly {
		t.Fatalf("MuteAllCommand shape = {path %q GMOnly %v}, want GM-only /glyphoxa muteall", cmd.Path, cmd.GMOnly)
	}

	// Idle: ephemeral, mutes nothing.
	idleMgr := &fakeMuter{active: false}
	idleResp := &fakeResponder{}
	if err := MuteAllCommand(idleMgr).Handle(context.Background(), &Interaction{guildID: testGuild, userID: operatorID, resp: idleResp}); err != nil {
		t.Fatalf("Handle idle: %v", err)
	}
	if len(idleResp.replies) != 1 || !idleResp.replies[0].ephemeral || len(idleMgr.allCalls) != 0 {
		t.Fatalf("idle muteall reply = %+v, allCalls = %v; want one ephemeral, no mute", idleResp.replies, idleMgr.allCalls)
	}

	// Active: mutes all.
	mgr := &fakeMuter{active: true, campaignID: uuid.New(), mutedIDs: []string{"a", "b"}}
	resp := &fakeResponder{}
	if err := MuteAllCommand(mgr).Handle(context.Background(), &Interaction{guildID: testGuild, userID: operatorID, resp: resp}); err != nil {
		t.Fatalf("Handle active: %v", err)
	}
	if len(mgr.allCalls) != 1 || !mgr.allCalls[0] {
		t.Fatalf("muteall SetAllMute calls = %v, want one true", mgr.allCalls)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("muteall reply = %+v, want one ephemeral", resp.replies)
	}
}

// TestMuteCommands_RefusedForNonOperator pins AC4's GM-only gate end-to-end: a
// non-operator invoking either command is denied and nothing is muted.
func TestMuteCommands_RefusedForNonOperator(t *testing.T) {
	mgr := &fakeMuter{active: true, campaignID: uuid.New()}
	reg := testRegistry(testGuild, operatorID)
	reg.Register(MuteCommand(mgr, &fakeLister{agents: []storage.Agent{{ID: uuid.New(), Name: "Bart"}}}))
	reg.Register(MuteAllCommand(mgr))

	resp := &fakeResponder{}
	reg.dispatch(context.Background(), "glyphoxa mute", &Interaction{guildID: testGuild, userID: strangerID, opts: fakeOpts{s: map[string]string{"npc": "Bart"}}, resp: resp})
	resp2 := &fakeResponder{}
	reg.dispatch(context.Background(), "glyphoxa muteall", &Interaction{guildID: testGuild, userID: strangerID, resp: resp2})

	if len(resp.replies) != 1 || !resp.replies[0].ephemeral || len(resp2.replies) != 1 || !resp2.replies[0].ephemeral {
		t.Fatalf("non-operator replies = %+v / %+v, want one ephemeral denial each", resp.replies, resp2.replies)
	}
	if len(mgr.agentCalls) != 0 || len(mgr.allCalls) != 0 {
		t.Fatalf("a non-operator muted something: agent=%v all=%v", mgr.agentCalls, mgr.allCalls)
	}
}
