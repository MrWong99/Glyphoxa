package presence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeMuter is a SessionMuter for the mute-command tests. tenantID is the Tenant
// its live session belongs to (#488): Active is Tenant-keyed, so a query for a
// different Tenant reports no session — the cross-tenant guard, now on the read
// itself. The zero value (uuid.Nil) matches an Interaction built without a resolved
// Tenant, so the pre-#490 success tests (muteIC has no tenant) keep passing.
type fakeMuter struct {
	active     bool
	tenantID   uuid.UUID
	campaignID uuid.UUID
	agentCalls []muteCall
	allCalls   []bool
	mutedIDs   []string
}

type muteCall struct {
	id    string
	muted bool
}

func (f *fakeMuter) Active(_ context.Context, tenantID uuid.UUID) (storage.VoiceSession, bool, error) {
	if !f.active || tenantID != f.tenantID {
		return storage.VoiceSession{}, false, nil
	}
	return storage.VoiceSession{CampaignID: f.campaignID}, true, nil
}

func (f *fakeMuter) SetAgentMute(_ context.Context, _ uuid.UUID, id string, muted bool) ([]string, error) {
	f.agentCalls = append(f.agentCalls, muteCall{id, muted})
	return f.mutedIDs, nil
}

func (f *fakeMuter) SetAllMute(_ context.Context, _ uuid.UUID, muted bool) ([]string, error) {
	f.allCalls = append(f.allCalls, muted)
	return f.mutedIDs, nil
}

// fakeLister is an AgentLister for the mute/say-command tests.
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
	cmd := MuteCommand(&fakeMuter{}, &fakeLister{}, nil)
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
	cmd := MuteCommand(mgr, &fakeLister{}, nil)
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

// fakePool is a PoolControl for the cross-pod control tests (#503): it scripts
// the claim plane's pool-wide Active answer and records the relayed controls.
type fakePool struct {
	live       bool
	campaignID uuid.UUID
	mutedIDs   []string
	muteErr    error
	sayErr     error
	agentCalls []muteCall
	allCalls   []bool
	sayCalls   []poolSayCall
}

type poolSayCall struct {
	id   string
	text string
}

func (f *fakePool) Active(context.Context, uuid.UUID) (storage.VoiceSession, bool, error) {
	if !f.live {
		return storage.VoiceSession{}, false, nil
	}
	return storage.VoiceSession{CampaignID: f.campaignID}, true, nil
}

func (f *fakePool) SetAgentMute(_ context.Context, _ uuid.UUID, id string, muted bool) ([]string, error) {
	f.agentCalls = append(f.agentCalls, muteCall{id, muted})
	return f.mutedIDs, f.muteErr
}

func (f *fakePool) SetAllMute(_ context.Context, _ uuid.UUID, muted bool) ([]string, error) {
	f.allCalls = append(f.allCalls, muted)
	return f.mutedIDs, f.muteErr
}

func (f *fakePool) SayAs(_ context.Context, _ uuid.UUID, id, text string) error {
	f.sayCalls = append(f.sayCalls, poolSayCall{id, text})
	return f.sayErr
}

// TestMuteCommand_LocalPathNoDefer covers sequence (12)/AC3: with the session
// live on THIS worker the old path runs byte-identically — an immediate
// ephemeral reply, NO Defer, the pool never consulted.
func TestMuteCommand_LocalPathNoDefer(t *testing.T) {
	bart := storage.Agent{ID: uuid.New(), Name: "Bart"}
	mgr := &fakeMuter{active: true, campaignID: uuid.New()}
	pool := &fakePool{live: true}
	cmd := MuteCommand(mgr, &fakeLister{agents: []storage.Agent{bart}}, pool)
	resp := &fakeResponder{}
	if err := cmd.Handle(context.Background(), muteIC(resp, bart.ID.String())); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.deferred != nil {
		t.Fatal("local path Deferred, want the pre-#503 immediate reply (AC3)")
	}
	if len(resp.replies) != 1 || resp.replies[0].content != "Bart is muted." || !resp.replies[0].ephemeral {
		t.Fatalf("local reply = %+v, want the ephemeral 'Bart is muted.'", resp.replies)
	}
	if len(pool.agentCalls) != 0 {
		t.Fatalf("local path relayed %v, want the local Manager only", pool.agentCalls)
	}
}

// TestMuteCommand_SessionOnAnotherWorker covers sequence (13)/#503: the local
// Manager holds no session but the pool does — the handler Defers ephemerally,
// relays the mute through the claim plane, and confirms with "X is muted."
func TestMuteCommand_SessionOnAnotherWorker(t *testing.T) {
	bart := storage.Agent{ID: uuid.New(), Name: "Bart"}
	mgr := &fakeMuter{active: false}
	pool := &fakePool{live: true, campaignID: uuid.New()}
	cmd := MuteCommand(mgr, &fakeLister{agents: []storage.Agent{bart}}, pool)
	resp := &fakeResponder{}
	if err := cmd.Handle(context.Background(), muteIC(resp, bart.ID.String())); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.deferred == nil || !*resp.deferred {
		t.Fatal("cross-pod mute did not Defer(true) — the relay can outlive Discord's 3s deadline")
	}
	if len(pool.agentCalls) != 1 || pool.agentCalls[0].id != bart.ID.String() || !pool.agentCalls[0].muted {
		t.Fatalf("pool relay calls = %+v, want one {%s true}", pool.agentCalls, bart.ID)
	}
	if len(mgr.agentCalls) != 0 {
		t.Fatalf("cross-pod path drove the LOCAL manager: %+v", mgr.agentCalls)
	}
	if len(resp.followups) != 1 || !strings.Contains(resp.followups[0].content, "Bart is muted.") || !resp.followups[0].ephemeral {
		t.Fatalf("confirming followup = %+v, want ephemeral 'Bart is muted.'", resp.followups)
	}
}

// TestMuteCommand_RelayFailureSurfaces covers sequence (14): a relay failure
// posts the REAL error text — never a silent no-op — and an unconfirmed relay
// (ErrControlPending) posts the honest not-confirmed wording, no phantom success.
func TestMuteCommand_RelayFailureSurfaces(t *testing.T) {
	bart := storage.Agent{ID: uuid.New(), Name: "Bart"}
	lister := &fakeLister{agents: []storage.Agent{bart}}

	pool := &fakePool{live: true, muteErr: errors.New("worker could not apply control: tts exploded")}
	resp := &fakeResponder{}
	if err := MuteCommand(&fakeMuter{}, lister, pool).Handle(context.Background(), muteIC(resp, bart.ID.String())); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(resp.followups) != 1 || !strings.Contains(resp.followups[0].content, "tts exploded") {
		t.Fatalf("failure followup = %+v, want the real error text", resp.followups)
	}

	pool = &fakePool{live: true, muteErr: session.ErrControlPending}
	resp = &fakeResponder{}
	if err := MuteCommand(&fakeMuter{}, lister, pool).Handle(context.Background(), muteIC(resp, bart.ID.String())); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(resp.followups) != 1 || !strings.Contains(resp.followups[0].content, "not confirmed") {
		t.Fatalf("pending followup = %+v, want the honest not-confirmed wording", resp.followups)
	}
	if strings.Contains(resp.followups[0].content, "is muted.") {
		t.Fatalf("pending followup %q claims success", resp.followups[0].content)
	}
}

// TestMuteAllCommand_SessionOnAnotherWorker covers the muteall arm of sequence
// (15): Defer + relay + confirming count followup.
func TestMuteAllCommand_SessionOnAnotherWorker(t *testing.T) {
	mgr := &fakeMuter{active: false}
	pool := &fakePool{live: true, mutedIDs: []string{"a", "b"}}
	resp := &fakeResponder{}
	if err := MuteAllCommand(mgr, &fakeLister{}, pool).Handle(context.Background(),
		&Interaction{guildID: testGuild, userID: operatorID, resp: resp}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.deferred == nil || !*resp.deferred {
		t.Fatal("cross-pod muteall did not Defer(true)")
	}
	if len(pool.allCalls) != 1 || !pool.allCalls[0] {
		t.Fatalf("pool muteall calls = %v, want one true", pool.allCalls)
	}
	if len(mgr.allCalls) != 0 {
		t.Fatalf("cross-pod muteall drove the LOCAL manager: %v", mgr.allCalls)
	}
	if len(resp.followups) != 1 || !strings.Contains(resp.followups[0].content, "Muted all 2 Agent voices.") {
		t.Fatalf("confirming followup = %+v, want 'Muted all 2 Agent voices.'", resp.followups)
	}
}

// TestMuteAutocomplete_PoolFallback: with no LOCAL session the mute autocomplete
// falls back to the pool's Active for the roster campaign (#503), so a GM on the
// presence owner still gets choices for a session hosted elsewhere.
func TestMuteAutocomplete_PoolFallback(t *testing.T) {
	bart := storage.Agent{ID: uuid.New(), Name: "Bart"}
	cmd := MuteCommand(&fakeMuter{active: false}, &fakeLister{agents: []storage.Agent{bart}},
		&fakePool{live: true, campaignID: uuid.New()})
	choices, err := cmd.Autocomplete(context.Background(), muteAC(""))
	if err != nil {
		t.Fatalf("Autocomplete: %v", err)
	}
	if len(choices) != 1 {
		t.Fatalf("choices = %+v, want Bart via the pool fallback", choices)
	}
}

// TestMuteCommand_PoolIdleKeepsPlainGuard pins the degrade: pool wired but the
// Tenant has no live intent anywhere → the plain no-session guard, unchanged.
func TestMuteCommand_PoolIdleKeepsPlainGuard(t *testing.T) {
	cmd := MuteCommand(&fakeMuter{active: false}, &fakeLister{}, &fakePool{live: false})
	resp := &fakeResponder{}
	if err := cmd.Handle(context.Background(), muteIC(resp, "Bart")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(resp.replies) != 1 || resp.replies[0].content != "No Voice Session is active." {
		t.Fatalf("idle-pool reply = %+v, want the plain no-session guard", resp.replies)
	}
}

// TestMuteCommand_ForeignTenantSessionRefused pins the cross-tenant guard (#490):
// the Manager is single-active, so a GM in Tenant A whose live session actually
// belongs to Tenant B must NOT mute it — the handler refuses ephemerally and mutes
// nothing. (If sessionInTenant wrongly returned true, the mute would proceed.)
func TestMuteCommand_ForeignTenantSessionRefused(t *testing.T) {
	bart := storage.Agent{ID: uuid.New(), Name: "Bart"}
	// The live session belongs to tenantB; the interaction is tenantA. Active is
	// Tenant-keyed, so the tenantA query sees no session (#488 subsumes the #490 guard).
	mgr := &fakeMuter{active: true, tenantID: tenantB, campaignID: uuid.New()}
	agents := &fakeLister{agents: []storage.Agent{bart}}
	cmd := MuteCommand(mgr, agents, nil)
	resp := &fakeResponder{}
	ic := &Interaction{guildID: testGuild, userID: operatorID, tenantID: tenantA, opts: fakeOpts{s: map[string]string{"npc": bart.ID.String()}}, resp: resp}

	if err := cmd.Handle(context.Background(), ic); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(mgr.agentCalls) != 0 {
		t.Fatalf("muted a foreign Tenant's session: %+v", mgr.agentCalls)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("reply = %+v, want one ephemeral refusal", resp.replies)
	}
}

// TestMuteAllCommand_ForeignTenantSessionRefused is the muteall sibling of the guard.
func TestMuteAllCommand_ForeignTenantSessionRefused(t *testing.T) {
	mgr := &fakeMuter{active: true, tenantID: tenantB, campaignID: uuid.New()}
	agents := &fakeLister{}
	cmd := MuteAllCommand(mgr, agents, nil)
	resp := &fakeResponder{}
	ic := &Interaction{guildID: testGuild, userID: operatorID, tenantID: tenantA, resp: resp}

	if err := cmd.Handle(context.Background(), ic); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(mgr.allCalls) != 0 {
		t.Fatalf("muteall drove a foreign Tenant's session: %v", mgr.allCalls)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("reply = %+v, want one ephemeral refusal", resp.replies)
	}
}

// TestMuteCommand_ResolvesUUIDValue pins the autocomplete-picked path: the npc
// value is an Agent UUID, resolved against the roster, and muted.
func TestMuteCommand_ResolvesUUIDValue(t *testing.T) {
	bart := storage.Agent{ID: uuid.New(), Name: "Bart"}
	mgr := &fakeMuter{active: true, campaignID: uuid.New()}
	cmd := MuteCommand(mgr, &fakeLister{agents: []storage.Agent{bart}}, nil)
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
	cmd := MuteCommand(mgr, &fakeLister{agents: []storage.Agent{bart}}, nil)

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
	cmd := MuteCommand(mgr, &fakeLister{agents: []storage.Agent{{ID: uuid.New(), Name: "Bart"}}}, nil)
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
	cmd := MuteCommand(mgr, &fakeLister{agents: []storage.Agent{bart, greta}}, nil)

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

// TestMuteCommand_ExcludesButler pins the Address-Only Butler exclusion
// (ADR-0009/ADR-0024): the Butler is returned by ListAgents but is never offered
// by the autocomplete, nor resolvable by name or UUID, so a GM cannot pick an
// inert mute target. Only the voiced Character NPC is offered and mutable.
func TestMuteCommand_ExcludesButler(t *testing.T) {
	butler := storage.Agent{ID: uuid.New(), Name: "Alfred", Role: storage.AgentRoleButler}
	bart := storage.Agent{ID: uuid.New(), Name: "Bart", Role: storage.AgentRoleCharacter}
	mgr := &fakeMuter{active: true, campaignID: uuid.New()}
	cmd := MuteCommand(mgr, &fakeLister{agents: []storage.Agent{butler, bart}}, nil)

	// Autocomplete offers only the voiced Character, not the Butler.
	choices, err := cmd.Autocomplete(context.Background(), muteAC(""))
	if err != nil {
		t.Fatalf("Autocomplete: %v", err)
	}
	if len(choices) != 1 || choices[0].ChoiceName() != "Bart" {
		t.Fatalf("autocomplete choices = %v, want only [Bart] (Butler excluded)", choices)
	}

	// Resolving the Butler by name is refused and mutes nothing.
	resp := &fakeResponder{}
	if err := cmd.Handle(context.Background(), muteIC(resp, "Alfred")); err != nil {
		t.Fatalf("Handle by name: %v", err)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral || !strings.Contains(resp.replies[0].content, "Alfred") {
		t.Fatalf("Butler-by-name reply = %+v, want an ephemeral naming the input", resp.replies)
	}

	// Even the Butler's UUID (an autocomplete-picked value) resolves to nothing.
	resp2 := &fakeResponder{}
	if err := cmd.Handle(context.Background(), muteIC(resp2, butler.ID.String())); err != nil {
		t.Fatalf("Handle by UUID: %v", err)
	}
	if len(mgr.agentCalls) != 0 {
		t.Fatalf("resolving the Butler muted %v, want nothing", mgr.agentCalls)
	}

	// The voiced Character NPC is still muteable.
	resp3 := &fakeResponder{}
	if err := cmd.Handle(context.Background(), muteIC(resp3, "Bart")); err != nil {
		t.Fatalf("Handle Bart: %v", err)
	}
	if len(mgr.agentCalls) != 1 || mgr.agentCalls[0].id != bart.ID.String() {
		t.Fatalf("mute calls = %+v, want one for Bart", mgr.agentCalls)
	}
}

// TestMuteCommand_AutocompleteCapsAt25 pins the Discord 25-choice limit.
func TestMuteCommand_AutocompleteCapsAt25(t *testing.T) {
	agents := make([]storage.Agent, 30)
	for i := range agents {
		agents[i] = storage.Agent{ID: uuid.New(), Name: "Agent"}
	}
	cmd := MuteCommand(&fakeMuter{active: true, campaignID: uuid.New()}, &fakeLister{agents: agents}, nil)
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
	cmd := MuteAllCommand(&fakeMuter{}, &fakeLister{}, nil)
	if cmd.Path != "glyphoxa muteall" || !cmd.GMOnly {
		t.Fatalf("MuteAllCommand shape = {path %q GMOnly %v}, want GM-only /glyphoxa muteall", cmd.Path, cmd.GMOnly)
	}

	// Idle: ephemeral, mutes nothing.
	idleMgr := &fakeMuter{active: false}
	idleResp := &fakeResponder{}
	if err := MuteAllCommand(idleMgr, &fakeLister{}, nil).Handle(context.Background(), &Interaction{guildID: testGuild, userID: operatorID, resp: idleResp}); err != nil {
		t.Fatalf("Handle idle: %v", err)
	}
	if len(idleResp.replies) != 1 || !idleResp.replies[0].ephemeral || len(idleMgr.allCalls) != 0 {
		t.Fatalf("idle muteall reply = %+v, allCalls = %v; want one ephemeral, no mute", idleResp.replies, idleMgr.allCalls)
	}

	// Active: mutes all.
	mgr := &fakeMuter{active: true, campaignID: uuid.New(), mutedIDs: []string{"a", "b"}}
	resp := &fakeResponder{}
	if err := MuteAllCommand(mgr, &fakeLister{}, nil).Handle(context.Background(), &Interaction{guildID: testGuild, userID: operatorID, resp: resp}); err != nil {
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
	reg.Register(MuteCommand(mgr, &fakeLister{agents: []storage.Agent{{ID: uuid.New(), Name: "Bart"}}}, nil))
	reg.Register(MuteAllCommand(mgr, &fakeLister{}, nil))

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
