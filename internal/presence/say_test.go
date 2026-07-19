package presence

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeSayer is a SayControl for the /say command tests: it records the SayAs calls
// and reports whether a session is active.
type fakeSayer struct {
	active     bool
	tenantID   uuid.UUID
	campaignID uuid.UUID
	sayErr     error
	calls      []sayCall
}

type sayCall struct {
	agentID string
	text    string
}

func (f *fakeSayer) Active(_ context.Context, tenantID uuid.UUID) (storage.VoiceSession, bool, error) {
	if !f.active || tenantID != f.tenantID {
		return storage.VoiceSession{}, false, nil
	}
	return storage.VoiceSession{CampaignID: f.campaignID}, true, nil
}

func (f *fakeSayer) SayAs(_ context.Context, _ uuid.UUID, agentID, text string) error {
	f.calls = append(f.calls, sayCall{agentID, text})
	return f.sayErr
}

func sayIC(resp *fakeResponder, text, as string) *Interaction {
	return &Interaction{
		guildID: testGuild,
		userID:  operatorID,
		opts:    fakeOpts{s: map[string]string{"text": text, "as": as}},
		resp:    resp,
	}
}

// TestSayCommand_ForeignTenantSessionRefused pins the cross-tenant guard (#490): a
// GM in Tenant A whose single active session belongs to Tenant B cannot puppet it —
// the handler refuses ephemerally and publishes no SpeakRequested.
func TestSayCommand_ForeignTenantSessionRefused(t *testing.T) {
	bart := storage.Agent{ID: uuid.New(), Name: "Bart"}
	// Live session belongs to tenantB; the interaction is tenantA. Active is
	// Tenant-keyed (#488), so the tenantA query sees no session.
	mgr := &fakeSayer{active: true, tenantID: tenantB, campaignID: uuid.New()}
	agents := &fakeLister{agents: []storage.Agent{bart}}
	cmd := SayCommand(mgr, agents)
	resp := &fakeResponder{}
	ic := &Interaction{guildID: testGuild, userID: operatorID, tenantID: tenantA, opts: fakeOpts{s: map[string]string{"text": "hi", "as": bart.ID.String()}}, resp: resp}

	if err := cmd.Handle(context.Background(), ic); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(mgr.calls) != 0 {
		t.Fatalf("puppeted a foreign Tenant's session: %+v", mgr.calls)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("reply = %+v, want one ephemeral refusal", resp.replies)
	}
}

func sayAC(as string) *Autocomplete {
	return &Autocomplete{
		guildID: testGuild,
		userID:  operatorID,
		data: discord.AutocompleteInteractionData{
			CommandName: "say",
			Options: map[string]discord.AutocompleteOption{
				"as": {
					Name:    "as",
					Type:    discord.ApplicationCommandOptionTypeString,
					Value:   json.RawMessage(`"` + as + `"`),
					Focused: true,
				},
			},
		},
	}
}

// TestSayCommand_IsFlatGMOnlyWithAutocomplete pins the command shape (ADR-0010):
// a FLAT /say (not grouped), GM-only, a required "text" and a required
// autocompleting "as" option.
func TestSayCommand_IsFlatGMOnlyWithAutocomplete(t *testing.T) {
	cmd := SayCommand(&fakeSayer{}, &fakeLister{})
	if cmd.Path != "say" || !cmd.GMOnly || cmd.Autocomplete == nil {
		t.Fatalf("SayCommand shape = {path %q GMOnly %v autocomplete %v}, want GM-only flat /say with autocomplete", cmd.Path, cmd.GMOnly, cmd.Autocomplete != nil)
	}
	if len(cmd.Options) != 2 {
		t.Fatalf("SayCommand has %d options, want 2 (text + as)", len(cmd.Options))
	}
}

// TestSayCommand_NoSessionRefused pins the active-session requirement (ADR-0010):
// with no live Voice Session /say is refused ephemerally and SayAs is never called.
func TestSayCommand_NoSessionRefused(t *testing.T) {
	sayer := &fakeSayer{active: false}
	resp := &fakeResponder{}
	if err := SayCommand(sayer, &fakeLister{}).Handle(context.Background(), sayIC(resp, "hi", "Bart")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(sayer.calls) != 0 {
		t.Fatalf("SayAs called %d times with no session, want 0", len(sayer.calls))
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("no-session reply = %+v, want one ephemeral refusal", resp.replies)
	}
}

// TestSayCommand_UnknownAgentRefused pins the resolver error: a name matching no
// roster Agent is an ephemeral error and SayAs is never called.
func TestSayCommand_UnknownAgentRefused(t *testing.T) {
	campaign := uuid.New()
	sayer := &fakeSayer{active: true, campaignID: campaign}
	lister := &fakeLister{agents: []storage.Agent{{ID: uuid.New(), Role: storage.AgentRoleCharacter, Name: "Bart"}}}
	resp := &fakeResponder{}
	if err := SayCommand(sayer, lister).Handle(context.Background(), sayIC(resp, "hi", "Nobody")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(sayer.calls) != 0 {
		t.Fatalf("SayAs called for an unknown agent, want 0 calls")
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("unknown-agent reply = %+v, want one ephemeral error", resp.replies)
	}
}

// TestSayCommand_ButlerRefusedOnHandle pins #365 review finding 4: the manager
// SayAs now ACCEPTS the Butler (SpeakAsButler needs it), so the Discord /say
// puppet-exclusion must hold at the presence Handle path — not only in Autocomplete.
// The Address-Only Butler is filtered out of the resolvable roster (voiced()), so a
// GM naming the Butler by name OR by UUID gets an ephemeral "no such Agent" refusal
// and SayAs is NEVER called (no accidental Butler puppeting, ADR-0009/0024).
func TestSayCommand_ButlerRefusedOnHandle(t *testing.T) {
	campaign := uuid.New()
	butler := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleButler, Name: "Glyphoxa"}
	bart := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleCharacter, Name: "Bart"}

	for _, as := range []string{"Glyphoxa", butler.ID.String()} {
		sayer := &fakeSayer{active: true, campaignID: campaign}
		lister := &fakeLister{agents: []storage.Agent{butler, bart}}
		resp := &fakeResponder{}
		if err := SayCommand(sayer, lister).Handle(context.Background(), sayIC(resp, "hi", as)); err != nil {
			t.Fatalf("Handle(as=%q): %v", as, err)
		}
		if len(sayer.calls) != 0 {
			t.Fatalf("SayAs called for the Butler (as=%q), want 0 calls (puppet-excluded)", as)
		}
		if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
			t.Fatalf("butler refusal (as=%q) = %+v, want one ephemeral error", as, resp.replies)
		}
	}
}

// TestSayCommand_HappyCallsSayAs pins the success path: a resolved voiced NPC is
// spoken via SayAs(id, text) and the GM gets an ephemeral ack naming the Agent.
func TestSayCommand_HappyCallsSayAs(t *testing.T) {
	campaign := uuid.New()
	bart := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleCharacter, Name: "Bart"}
	sayer := &fakeSayer{active: true, campaignID: campaign}
	lister := &fakeLister{agents: []storage.Agent{bart}}
	resp := &fakeResponder{}
	if err := SayCommand(sayer, lister).Handle(context.Background(), sayIC(resp, "Welcome!", bart.ID.String())); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(sayer.calls) != 1 || sayer.calls[0].agentID != bart.ID.String() || sayer.calls[0].text != "Welcome!" {
		t.Fatalf("SayAs calls = %+v, want one {%s, Welcome!}", sayer.calls, bart.ID)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("ack = %+v, want one ephemeral reply", resp.replies)
	}
}

// TestSayCommand_SayAsRaceRefused pins the guard on a session ending between the
// resolve and the publish: SayAs returning an error surfaces the same ephemeral
// no-session refusal (mirrors the mute command), not a generic failure.
func TestSayCommand_SayAsRaceRefused(t *testing.T) {
	bart := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleCharacter, Name: "Bart"}
	sayer := &fakeSayer{active: true, sayErr: context.Canceled}
	lister := &fakeLister{agents: []storage.Agent{bart}}
	resp := &fakeResponder{}
	if err := SayCommand(sayer, lister).Handle(context.Background(), sayIC(resp, "hi", bart.ID.String())); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("race reply = %+v, want one ephemeral refusal", resp.replies)
	}
}

// TestSayCommand_AutocompleteOffersVoicedNPCs pins the autocomplete (mute pattern):
// it offers the voiced Character NPCs (Name shown, Agent UUID value) and excludes
// the Address-Only Butler, which /say cannot puppet yet (#299).
func TestSayCommand_AutocompleteOffersVoicedNPCs(t *testing.T) {
	campaign := uuid.New()
	butler := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleButler, Name: "Butler"}
	bart := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleCharacter, Name: "Bart"}
	sayer := &fakeSayer{active: true, campaignID: campaign}
	lister := &fakeLister{agents: []storage.Agent{butler, bart}}

	choices, err := SayCommand(sayer, lister).Autocomplete(context.Background(), sayAC(""))
	if err != nil {
		t.Fatalf("Autocomplete: %v", err)
	}
	if len(choices) != 1 {
		t.Fatalf("choices = %+v, want only the voiced NPC (Butler excluded)", choices)
	}
	c, ok := choices[0].(discord.AutocompleteChoiceString)
	if !ok || c.Name != "Bart" || c.Value != bart.ID.String() {
		t.Fatalf("choice = %+v, want {Bart, %s}", choices[0], bart.ID)
	}
}

// TestSayCommand_AutocompleteIdleOffersNothing pins that with no live session the
// autocomplete offers nothing (nothing to puppet).
func TestSayCommand_AutocompleteIdleOffersNothing(t *testing.T) {
	choices, err := SayCommand(&fakeSayer{active: false}, &fakeLister{}).Autocomplete(context.Background(), sayAC(""))
	if err != nil {
		t.Fatalf("Autocomplete: %v", err)
	}
	if len(choices) != 0 {
		t.Fatalf("idle autocomplete = %+v, want none", choices)
	}
}
