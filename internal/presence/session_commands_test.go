package presence

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// setActiveCall records one SetActiveCampaign invocation for assertions.
type setActiveCall struct {
	discordUserID string
	campaignID    uuid.UUID
}

// fakeSessionStore is a configurable in-memory SessionStore for the /glyphoxa
// command unit tests (no DB). A nil *storage.Campaign pointer field models the
// ErrNotFound "absent" state so the resolution order can be driven precisely.
type fakeSessionStore struct {
	list       []storage.Campaign
	listErr    error
	byID       map[uuid.UUID]storage.Campaign
	byIDErr    error
	forUser    *storage.Campaign
	forUserErr error
	setErr     error
	setCalls   []setActiveCall
}

func (f *fakeSessionStore) ListCampaignsInTenant(_ context.Context, _ uuid.UUID) ([]storage.Campaign, error) {
	return f.list, f.listErr
}

func (f *fakeSessionStore) GetCampaign(_ context.Context, id uuid.UUID) (storage.Campaign, error) {
	if f.byIDErr != nil {
		return storage.Campaign{}, f.byIDErr
	}
	c, ok := f.byID[id]
	if !ok {
		return storage.Campaign{}, storage.ErrNotFound
	}
	return c, nil
}

func (f *fakeSessionStore) SetActiveCampaign(_ context.Context, discordUserID string, campaignID uuid.UUID) error {
	f.setCalls = append(f.setCalls, setActiveCall{discordUserID, campaignID})
	return f.setErr
}

func (f *fakeSessionStore) GetActiveCampaignForUserInTenant(_ context.Context, _ uuid.UUID, _ string) (storage.Campaign, error) {
	if f.forUserErr != nil {
		return storage.Campaign{}, f.forUserErr
	}
	if f.forUser == nil {
		return storage.Campaign{}, storage.ErrNotFound
	}
	return *f.forUser, nil
}

// tenantCampaignStore is a SessionStore whose campaign lists AND durable selections
// are keyed by Tenant — so a dispatch test can prove a command sees ONLY the
// invoking Tenant's campaigns (#490). GetCampaign resolves across all Tenants (the
// live-session step re-checks the Tenant itself).
type tenantCampaignStore struct {
	byTenant map[uuid.UUID][]storage.Campaign
	setCalls []setActiveCall
}

func (s *tenantCampaignStore) ListCampaignsInTenant(_ context.Context, tenantID uuid.UUID) ([]storage.Campaign, error) {
	return s.byTenant[tenantID], nil
}
func (s *tenantCampaignStore) GetCampaign(_ context.Context, id uuid.UUID) (storage.Campaign, error) {
	for _, list := range s.byTenant {
		for _, c := range list {
			if c.ID == id {
				return c, nil
			}
		}
	}
	return storage.Campaign{}, storage.ErrNotFound
}
func (s *tenantCampaignStore) SetActiveCampaign(_ context.Context, discordUserID string, campaignID uuid.UUID) error {
	s.setCalls = append(s.setCalls, setActiveCall{discordUserID, campaignID})
	return nil
}
func (s *tenantCampaignStore) GetActiveCampaignForUserInTenant(_ context.Context, _ uuid.UUID, _ string) (storage.Campaign, error) {
	return storage.Campaign{}, storage.ErrNotFound
}

// twoTenantRegistry builds a Registry whose Gate routes guildA→tenantA and
// guildB→tenantB, with operatorID a GM in BOTH.
func twoTenantRegistry(store SessionStore) *Registry {
	gate := NewGate(
		gmInTenants{tenantA: {operatorID: {}}, tenantB: {operatorID: {}}},
		fakeTenants{"gA": tenantA, "gB": tenantB},
	)
	reg := NewRegistry(gate, nil)
	reg.Register(UseCommand(store))
	return reg
}

// TestUseSameCommandTwoTenantsActsOnOwnData pins #490 test (7): the SAME
// /glyphoxa use command invoked in two Tenants' Guilds acts on the correct Tenant's
// campaign — even when both Tenants have a campaign of the identical name.
func TestUseSameCommandTwoTenantsActsOnOwnData(t *testing.T) {
	aCamp := campaignIn(tenantA, "Shared Name")
	bCamp := campaignIn(tenantB, "Shared Name")
	store := &tenantCampaignStore{byTenant: map[uuid.UUID][]storage.Campaign{
		tenantA: {aCamp},
		tenantB: {bCamp},
	}}
	reg := twoTenantRegistry(store)
	ctx := context.Background()

	use := func(guild string) {
		reg.dispatch(ctx, "glyphoxa use", &Interaction{
			guildID: guild, userID: operatorID,
			opts: fakeOpts{s: map[string]string{"campaign": "Shared Name"}},
			resp: &fakeResponder{},
		})
	}
	use("gA")
	use("gB")

	if len(store.setCalls) != 2 {
		t.Fatalf("SetActiveCampaign calls = %d, want 2", len(store.setCalls))
	}
	if store.setCalls[0].campaignID != aCamp.ID {
		t.Errorf("guild A selected %s, want tenant A's campaign %s", store.setCalls[0].campaignID, aCamp.ID)
	}
	if store.setCalls[1].campaignID != bCamp.ID {
		t.Errorf("guild B selected %s, want tenant B's campaign %s", store.setCalls[1].campaignID, bCamp.ID)
	}
}

// TestUseForeignCampaignUUIDNotMatched pins #490 test (6): a pasted campaign UUID
// belonging to ANOTHER Tenant is not matchable — /glyphoxa use in Tenant A cannot
// select Tenant B's campaign by id (it never appears in the tenant-scoped list), so
// nothing is set and the GM gets the graceful "no match" reply.
func TestUseForeignCampaignUUIDNotMatched(t *testing.T) {
	aCamp := campaignIn(tenantA, "Alpha")
	bCamp := campaignIn(tenantB, "Bravo")
	store := &tenantCampaignStore{byTenant: map[uuid.UUID][]storage.Campaign{
		tenantA: {aCamp},
		tenantB: {bCamp},
	}}
	reg := twoTenantRegistry(store)

	resp := &fakeResponder{}
	// Invoked in Guild A, but naming Tenant B's campaign UUID.
	reg.dispatch(context.Background(), "glyphoxa use", &Interaction{
		guildID: "gA", userID: operatorID,
		opts: fakeOpts{s: map[string]string{"campaign": bCamp.ID.String()}},
		resp: resp,
	})

	if len(store.setCalls) != 0 {
		t.Fatalf("a foreign campaign UUID was selected: %+v, want none", store.setCalls)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral || !strings.Contains(resp.replies[0].content, "No campaign matches") {
		t.Errorf("reply = %+v, want an ephemeral no-match", resp.replies)
	}
}

// startCall records the (tenant, campaign) a Start was driven with.
type startCall struct {
	tenantID   uuid.UUID
	campaignID uuid.UUID
}

// fakeVoice is a configurable VoiceControl for the start/end command tests.
type fakeVoice struct {
	snap       storage.VoiceSession
	active     bool
	startVS    storage.VoiceSession
	startErr   error
	started    *startCall
	stopVS     storage.VoiceSession
	stopErr    error
	stopCalled bool
}

func (f *fakeVoice) Start(_ context.Context, tenantID, campaignID uuid.UUID) (storage.VoiceSession, error) {
	f.started = &startCall{tenantID, campaignID}
	if f.startErr != nil {
		return storage.VoiceSession{}, f.startErr
	}
	return f.startVS, nil
}

func (f *fakeVoice) Stop(context.Context) (storage.VoiceSession, error) {
	f.stopCalled = true
	return f.stopVS, f.stopErr
}

func (f *fakeVoice) Snapshot() (storage.VoiceSession, bool) { return f.snap, f.active }

// campaign builds a Campaign in tenantA — the Tenant that testGuild resolves to in
// the dispatch tests (see testRegistry), so a dispatched command's ic.TenantID()
// matches the campaign's Tenant. Cross-tenant tests build campaigns in tenantB
// explicitly.
func campaign(name string) storage.Campaign {
	return campaignIn(tenantA, name)
}

func campaignIn(tenantID uuid.UUID, name string) storage.Campaign {
	return storage.Campaign{ID: uuid.New(), TenantID: tenantID, Name: name}
}

// dispatchAs runs one interaction through the registry as the given user with
// the given string options, returning the recorded responder.
func dispatchAs(reg *Registry, key, userID string, opts map[string]string) *fakeResponder {
	resp := &fakeResponder{}
	ic := &Interaction{guildID: testGuild, userID: userID, opts: fakeOpts{s: opts}, resp: resp}
	reg.dispatch(context.Background(), key, ic)
	return resp
}

func TestUseSetsActiveCampaignByName(t *testing.T) {
	lost := campaign("Lost Mine")
	store := &fakeSessionStore{list: []storage.Campaign{campaign("Curse of Strahd"), lost}}
	reg := testRegistry(testGuild, operatorID)
	reg.Register(UseCommand(store))

	// Case-insensitive free-text name resolves.
	resp := dispatchAs(reg, "glyphoxa use", operatorID, map[string]string{"campaign": "lost mine"})

	if len(store.setCalls) != 1 {
		t.Fatalf("SetActiveCampaign calls = %d, want 1", len(store.setCalls))
	}
	if store.setCalls[0].discordUserID != operatorID || store.setCalls[0].campaignID != lost.ID {
		t.Errorf("SetActiveCampaign = %+v, want operator %s + campaign %s", store.setCalls[0], operatorID, lost.ID)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral || !strings.Contains(resp.replies[0].content, "Lost Mine") {
		t.Errorf("reply = %+v, want one ephemeral naming the campaign", resp.replies)
	}
}

func TestUseResolvesByUUIDValue(t *testing.T) {
	lost := campaign("Lost Mine")
	store := &fakeSessionStore{list: []storage.Campaign{lost}}
	reg := testRegistry(testGuild, operatorID)
	reg.Register(UseCommand(store))

	// The autocomplete choice value is the campaign UUID.
	dispatchAs(reg, "glyphoxa use", operatorID, map[string]string{"campaign": lost.ID.String()})

	if len(store.setCalls) != 1 || store.setCalls[0].campaignID != lost.ID {
		t.Fatalf("SetActiveCampaign = %+v, want campaign %s", store.setCalls, lost.ID)
	}
}

func TestUseUnknownCampaign(t *testing.T) {
	store := &fakeSessionStore{list: []storage.Campaign{campaign("Lost Mine")}}
	reg := testRegistry(testGuild, operatorID)
	reg.Register(UseCommand(store))

	resp := dispatchAs(reg, "glyphoxa use", operatorID, map[string]string{"campaign": "Nonexistent"})

	if len(store.setCalls) != 0 {
		t.Errorf("SetActiveCampaign called %d times for an unknown campaign, want 0", len(store.setCalls))
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral || !strings.Contains(resp.replies[0].content, "Nonexistent") {
		t.Errorf("reply = %+v, want one ephemeral error naming the input", resp.replies)
	}
}

func TestUseAutocompleteListsAndFilters(t *testing.T) {
	strahd := campaign("Curse of Strahd")
	lost := campaign("Lost Mine")
	store := &fakeSessionStore{list: []storage.Campaign{strahd, lost}}
	cmd := UseCommand(store)

	// Empty focus lists all; each choice DISPLAYS the name, CARRIES the UUID value.
	all, err := cmd.Autocomplete(context.Background(), &Autocomplete{})
	if err != nil {
		t.Fatalf("autocomplete all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("empty-focus choices = %d, want 2", len(all))
	}
	got := all[0].(discord.AutocompleteChoiceString)
	if got.Name != "Curse of Strahd" || got.Value != strahd.ID.String() {
		t.Errorf("choice 0 = %+v, want name 'Curse of Strahd' value %s", got, strahd.ID)
	}

	// A typed substring filters case-insensitively.
	filtered := campaignChoices(store.list, "lost")
	if len(filtered) != 1 || filtered[0].(discord.AutocompleteChoiceString).Value != lost.ID.String() {
		t.Errorf("filtered choices = %+v, want only Lost Mine", filtered)
	}
}

func TestStartSuccessUsesResolvedCampaign(t *testing.T) {
	lost := campaign("Lost Mine")
	store := &fakeSessionStore{forUser: &lost}
	voice := &fakeVoice{startVS: storage.VoiceSession{ID: uuid.New(), CampaignID: lost.ID}}
	reg := testRegistry(testGuild, operatorID)
	reg.Register(StartCommand(store, voice))

	resp := dispatchAs(reg, "glyphoxa start", operatorID, nil)

	if voice.started == nil || voice.started.tenantID != lost.TenantID || voice.started.campaignID != lost.ID {
		t.Fatalf("Start called with %+v, want tenant %s campaign %s", voice.started, lost.TenantID, lost.ID)
	}
	if resp.deferred == nil {
		t.Error("start did not Defer before the slow work")
	}
	// The confirmation is ephemeral: the first followup after Defer(true) inherits
	// ephemerality in the real client, and no AC requires a public reply.
	if len(resp.followups) != 1 || !resp.followups[0].ephemeral || !strings.Contains(resp.followups[0].content, "Lost Mine") {
		t.Errorf("followup = %+v, want one ephemeral confirmation naming the campaign", resp.followups)
	}
}

func TestStartAlreadyActive(t *testing.T) {
	lost := campaign("Lost Mine")
	store := &fakeSessionStore{forUser: &lost}
	voice := &fakeVoice{startErr: session.ErrSessionActive}
	reg := testRegistry(testGuild, operatorID)
	reg.Register(StartCommand(store, voice))

	resp := dispatchAs(reg, "glyphoxa start", operatorID, nil)

	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("already-active reply = %+v, want one ephemeral error", resp.followups)
	}
	if !strings.Contains(strings.ToLower(resp.followups[0].content), "already active") {
		t.Errorf("already-active reply = %q, want a clear 'already active' message", resp.followups[0].content)
	}
}

func TestStartNoActiveCampaign(t *testing.T) {
	// No live session and no durable selection → fail with the run-/use hint (the
	// slash surface has no most-recently-created fallback, ADR-0009).
	store := &fakeSessionStore{}
	voice := &fakeVoice{}
	reg := testRegistry(testGuild, operatorID)
	reg.Register(StartCommand(store, voice))

	resp := dispatchAs(reg, "glyphoxa start", operatorID, nil)

	if voice.started != nil {
		t.Error("Start was driven despite no resolvable campaign")
	}
	if len(resp.followups) != 1 || !resp.followups[0].ephemeral || !strings.Contains(resp.followups[0].content, "use") {
		t.Errorf("reply = %+v, want one ephemeral hint to run /glyphoxa use", resp.followups)
	}
}

func TestStartDiscordNotConfigured(t *testing.T) {
	lost := campaign("Lost Mine")
	store := &fakeSessionStore{forUser: &lost}
	voice := &fakeVoice{startErr: session.ErrDiscordNotConfigured}
	reg := testRegistry(testGuild, operatorID)
	reg.Register(StartCommand(store, voice))

	resp := dispatchAs(reg, "glyphoxa start", operatorID, nil)

	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("not-configured reply = %+v, want one ephemeral error", resp.followups)
	}
	if !strings.Contains(strings.ToLower(resp.followups[0].content), "configur") {
		t.Errorf("not-configured reply = %q, want a clear configuration hint", resp.followups[0].content)
	}
}

func TestEndSuccess(t *testing.T) {
	voice := &fakeVoice{stopVS: storage.VoiceSession{ID: uuid.New()}}
	reg := testRegistry(testGuild, operatorID)
	reg.Register(EndCommand(voice, &fakeSessionStore{}))

	resp := dispatchAs(reg, "glyphoxa end", operatorID, nil)

	if !voice.stopCalled {
		t.Fatal("Stop was not called")
	}
	if resp.deferred == nil {
		t.Error("end did not Defer before the slow work")
	}
	// Ephemeral confirmation (see TestStartSuccessUsesResolvedCampaign).
	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Errorf("followup = %+v, want one ephemeral confirmation", resp.followups)
	}
}

func TestEndNoneRunning(t *testing.T) {
	voice := &fakeVoice{stopErr: session.ErrNoActiveSession}
	reg := testRegistry(testGuild, operatorID)
	reg.Register(EndCommand(voice, &fakeSessionStore{}))

	resp := dispatchAs(reg, "glyphoxa end", operatorID, nil)

	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("none-running reply = %+v, want one ephemeral error", resp.followups)
	}
	if !strings.Contains(strings.ToLower(resp.followups[0].content), "no voice session") {
		t.Errorf("none-running reply = %q, want a clear 'none running' message", resp.followups[0].content)
	}
}

// TestEndForeignTenantSessionRefused pins the cross-tenant guard (#490): the
// Manager is single-active, so a GM in Tenant A must NOT stop a live session that
// belongs to Tenant B — end refuses and never calls Stop.
func TestEndForeignTenantSessionRefused(t *testing.T) {
	foreign := campaignIn(tenantB, "Tenant B session")
	store := &fakeSessionStore{byID: map[uuid.UUID]storage.Campaign{foreign.ID: foreign}}
	voice := &fakeVoice{active: true, snap: storage.VoiceSession{CampaignID: foreign.ID}}
	reg := testRegistry(testGuild, operatorID) // testGuild → tenantA
	reg.Register(EndCommand(voice, store))

	resp := dispatchAs(reg, "glyphoxa end", operatorID, nil)

	if voice.stopCalled {
		t.Fatal("end stopped a foreign Tenant's live session")
	}
	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("reply = %+v, want one ephemeral refusal", resp.followups)
	}
}

func TestSessionCommandsRefusedForNonGM(t *testing.T) {
	lost := campaign("Lost Mine")
	store := &fakeSessionStore{list: []storage.Campaign{lost}, forUser: &lost}
	voice := &fakeVoice{}
	reg := testRegistry(testGuild, operatorID) // only operatorID is allowlisted
	reg.Register(UseCommand(store), StartCommand(store, voice), EndCommand(voice, store))

	for _, key := range []string{"glyphoxa use", "glyphoxa start", "glyphoxa end"} {
		resp := dispatchAs(reg, key, strangerID, map[string]string{"campaign": "Lost Mine"})
		if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
			t.Errorf("%s by non-GM: reply = %+v, want one ephemeral denial", key, resp.replies)
		}
	}
	if len(store.setCalls) != 0 || voice.started != nil || voice.stopCalled {
		t.Errorf("a non-GM reached a handler: set=%d start=%v stop=%v", len(store.setCalls), voice.started, voice.stopCalled)
	}
}

func TestResolveActiveCampaignOrder(t *testing.T) {
	live := campaign("Live Session Campaign")
	selected := campaign("Selected Campaign")
	ctx := context.Background()

	// (1) A live Voice Session wins even over a durable selection.
	s1 := &fakeSessionStore{
		byID:    map[uuid.UUID]storage.Campaign{live.ID: live},
		forUser: &selected,
	}
	v1 := &fakeVoice{active: true, snap: storage.VoiceSession{CampaignID: live.ID}}
	if c, err := resolveActiveCampaign(ctx, s1, v1, tenantA, operatorID); err != nil || c.ID != live.ID {
		t.Errorf("step 1: got %s, %v; want live session campaign %s", c.ID, err, live.ID)
	}

	// (2) No live session → the operator's durable selection.
	s2 := &fakeSessionStore{forUser: &selected}
	if c, err := resolveActiveCampaign(ctx, s2, &fakeVoice{}, tenantA, operatorID); err != nil || c.ID != selected.ID {
		t.Errorf("step 2: got %s, %v; want selection %s", c.ID, err, selected.ID)
	}

	// (3) No session and no selection → FAIL (no most-recently-created fallback on
	// the slash surface, ADR-0009).
	if _, err := resolveActiveCampaign(ctx, &fakeSessionStore{}, &fakeVoice{}, tenantA, operatorID); err != ErrNoActiveCampaign {
		t.Errorf("no session + no selection: err = %v, want ErrNoActiveCampaign", err)
	}
}

// TestResolveActiveCampaignLiveSessionForeignTenant pins #490 test (8): with the
// manager still single-active (#488 unmerged), a live Voice Session belonging to
// ANOTHER Tenant must NOT pin this Tenant's Active Campaign onto the foreign
// campaign. Step 1 counts the live session only when its campaign is in THIS Tenant;
// otherwise it falls through to the durable selection.
func TestResolveActiveCampaignLiveSessionForeignTenant(t *testing.T) {
	ctx := context.Background()
	foreignLive := campaignIn(tenantB, "Tenant B live") // running session's campaign, other Tenant
	mine := campaign("My durable pick")                 // tenantA durable selection

	store := &fakeSessionStore{
		byID:    map[uuid.UUID]storage.Campaign{foreignLive.ID: foreignLive},
		forUser: &mine,
	}
	voice := &fakeVoice{active: true, snap: storage.VoiceSession{CampaignID: foreignLive.ID}}

	c, err := resolveActiveCampaign(ctx, store, voice, tenantA, operatorID)
	if err != nil || c.ID != mine.ID {
		t.Errorf("got %s, %v; want the tenant-A durable selection %s (foreign live session ignored)", c.ID, err, mine.ID)
	}
}

// TestGlyphoxaUseEndToEnd routes a real grouped `/glyphoxa use` JSON interaction
// through HandleCommand (the disgo listener), proving the wiring from a live
// event to SetActiveCampaign + reply — the same harness the #102 grouped test
// pins.
func TestGlyphoxaUseEndToEnd(t *testing.T) {
	lost := campaign("Lost Mine")
	store := &fakeSessionStore{list: []storage.Campaign{lost}}
	reg := testRegistry(testGuild, operatorID)
	reg.Register(UseCommand(store))

	payload := `{
		"id": "1", "application_id": "2", "type": 2, "token": "t", "version": 1,
		"guild_id": "` + testGuild + `",
		"member": {"user": {"id": "` + operatorID + `", "username": "gm"}},
		"data": {"id": "3", "name": "glyphoxa", "type": 1,
			"options": [{"type": 1, "name": "use", "options": [{"type": 3, "name": "campaign", "value": "Lost Mine"}]}]}
	}`
	var aci discord.ApplicationCommandInteraction
	if err := json.Unmarshal([]byte(payload), &aci); err != nil {
		t.Fatalf("unmarshal grouped interaction: %v", err)
	}

	var reply discord.MessageCreate
	e := &events.ApplicationCommandInteractionCreate{
		ApplicationCommandInteraction: aci,
		Respond: func(_ discord.InteractionResponseType, data discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			reply = data.(discord.MessageCreate)
			return nil
		},
	}
	reg.HandleCommand(e)

	if len(store.setCalls) != 1 || store.setCalls[0].campaignID != lost.ID {
		t.Fatalf("SetActiveCampaign = %+v, want campaign %s", store.setCalls, lost.ID)
	}
	if !strings.Contains(reply.Content, "Lost Mine") {
		t.Errorf("reply = %q, want a confirmation naming the campaign", reply.Content)
	}
}

// TestGlyphoxaUseAutocompleteEndToEnd routes a real grouped `/glyphoxa use`
// AUTOCOMPLETE JSON interaction through HandleAutocomplete: disgo flattens the
// subcommand into AutocompleteInteractionData.SubCommandName + Focused option, so
// this pins that flattening (against a disgo upgrade) for a grouped autocomplete
// — #213's e2e only covers a FLAT autocomplete. It asserts the focused substring
// filters the campaigns and each choice carries the campaign UUID as its value.
func TestGlyphoxaUseAutocompleteEndToEnd(t *testing.T) {
	lost := campaign("Lost Mine")
	store := &fakeSessionStore{list: []storage.Campaign{campaign("Curse of Strahd"), lost}}
	reg := testRegistry(testGuild, operatorID)
	reg.Register(UseCommand(store))

	payload := `{
		"id": "1", "application_id": "2", "type": 4, "token": "t", "version": 1,
		"guild_id": "` + testGuild + `",
		"member": {"user": {"id": "` + operatorID + `", "username": "gm"}},
		"data": {"id": "3", "name": "glyphoxa",
			"options": [{"type": 1, "name": "use", "options": [{"type": 3, "name": "campaign", "value": "lost", "focused": true}]}]}
	}`
	var ai discord.AutocompleteInteraction
	if err := json.Unmarshal([]byte(payload), &ai); err != nil {
		t.Fatalf("unmarshal grouped autocomplete: %v", err)
	}

	var got discord.AutocompleteResult
	e := &events.AutocompleteInteractionCreate{
		AutocompleteInteraction: ai,
		Respond: func(_ discord.InteractionResponseType, data discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			got = data.(discord.AutocompleteResult)
			return nil
		},
	}
	reg.HandleAutocomplete(e)

	if len(got.Choices) != 1 {
		t.Fatalf("grouped autocomplete choices = %+v, want only the 'lost' match", got.Choices)
	}
	c := got.Choices[0].(discord.AutocompleteChoiceString)
	if c.Name != "Lost Mine" || c.Value != lost.ID.String() {
		t.Errorf("choice = %+v, want name 'Lost Mine' value %s", c, lost.ID)
	}
}
