package presence

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeDirector is a DirectControl for the /direct command tests: it records the
// DirectAs calls and reports whether a session is active.
type fakeDirector struct {
	active     bool
	tenantID   uuid.UUID
	campaignID uuid.UUID
	directErr  error
	calls      []directCall
}

type directCall struct {
	agentID string
	text    string
	turns   int
}

func (f *fakeDirector) Active(_ context.Context, tenantID uuid.UUID) (storage.VoiceSession, bool, error) {
	if !f.active || tenantID != f.tenantID {
		return storage.VoiceSession{}, false, nil
	}
	return storage.VoiceSession{CampaignID: f.campaignID}, true, nil
}

func (f *fakeDirector) DirectAs(_ context.Context, _ uuid.UUID, agentID, text string, turns int) error {
	f.calls = append(f.calls, directCall{agentID, text, turns})
	return f.directErr
}

func directIC(resp *fakeResponder, tenantID uuid.UUID, as, note string, turns int) *Interaction {
	opts := fakeOpts{s: map[string]string{"as": as}, i: map[string]int{}}
	if note != "" {
		opts.s["note"] = note
	}
	if turns > 0 {
		opts.i["turns"] = turns
	}
	return &Interaction{guildID: testGuild, userID: operatorID, tenantID: tenantID, opts: opts, resp: resp}
}

// TestDirectCommand_IsFlatGMOnlyWithAutocomplete pins the command shape
// (ADR-0059 on the ADR-0010 surface): a FLAT GM-only /direct with a required
// autocompleting "as" plus optional "note" and "turns".
func TestDirectCommand_IsFlatGMOnlyWithAutocomplete(t *testing.T) {
	cmd := DirectCommand(&fakeDirector{}, &fakeLister{}, nil)
	if cmd.Path != "direct" || !cmd.GMOnly || cmd.Autocomplete == nil {
		t.Fatalf("DirectCommand shape = {path %q GMOnly %v autocomplete %v}, want GM-only flat /direct with autocomplete", cmd.Path, cmd.GMOnly, cmd.Autocomplete != nil)
	}
	if len(cmd.Options) != 3 {
		t.Fatalf("DirectCommand has %d options, want 3 (as + note + turns)", len(cmd.Options))
	}
}

// TestDirectCommand_SetsBoundedDirective pins the local set path: a note with a
// turns bound reaches DirectAs with both, and the GM gets an ephemeral
// confirmation (the directive must never post publicly).
func TestDirectCommand_SetsBoundedDirective(t *testing.T) {
	bart := storage.Agent{ID: uuid.New(), Name: "Bart"}
	mgr := &fakeDirector{active: true, tenantID: tenantA, campaignID: uuid.New()}
	resp := &fakeResponder{}
	cmd := DirectCommand(mgr, &fakeLister{agents: []storage.Agent{bart}}, nil)

	if err := cmd.Handle(context.Background(), directIC(resp, tenantA, bart.ID.String(), "Bart lies about the key.", 3)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(mgr.calls) != 1 || mgr.calls[0] != (directCall{bart.ID.String(), "Bart lies about the key.", 3}) {
		t.Fatalf("DirectAs calls = %+v, want the bounded directive", mgr.calls)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("reply = %+v, want one ephemeral confirmation", resp.replies)
	}
}

// TestDirectCommand_OmittedNoteClears pins the clear path: /direct with no note
// relays an empty directive text — the session-side clear.
func TestDirectCommand_OmittedNoteClears(t *testing.T) {
	bart := storage.Agent{ID: uuid.New(), Name: "Bart"}
	mgr := &fakeDirector{active: true, tenantID: tenantA, campaignID: uuid.New()}
	resp := &fakeResponder{}
	cmd := DirectCommand(mgr, &fakeLister{agents: []storage.Agent{bart}}, nil)

	if err := cmd.Handle(context.Background(), directIC(resp, tenantA, bart.ID.String(), "", 0)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(mgr.calls) != 1 || mgr.calls[0] != (directCall{bart.ID.String(), "", 0}) {
		t.Fatalf("DirectAs calls = %+v, want one clearing call", mgr.calls)
	}
}

// TestDirectCommand_ButlerNotATarget pins the Butler exclusion on the command
// surface: the voiced filter drops the Butler from resolution, so directing it
// by UUID is a clean "no such Agent" refusal, never a DirectAs call.
func TestDirectCommand_ButlerNotATarget(t *testing.T) {
	butler := storage.Agent{ID: uuid.New(), Name: "Glyphoxa", Role: storage.AgentRoleButler}
	mgr := &fakeDirector{active: true, tenantID: tenantA, campaignID: uuid.New()}
	resp := &fakeResponder{}
	cmd := DirectCommand(mgr, &fakeLister{agents: []storage.Agent{butler}}, nil)

	if err := cmd.Handle(context.Background(), directIC(resp, tenantA, butler.ID.String(), "note", 0)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(mgr.calls) != 0 {
		t.Fatalf("directed the Butler: %+v", mgr.calls)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("reply = %+v, want one ephemeral refusal", resp.replies)
	}
}

// TestDirectCommand_SessionOnAnotherWorker covers the direct arm of the #503
// cross-pod relay: the local Manager holds no session but the pool does —
// /direct Defers, relays through the claim plane (the LOCAL DirectAs is never
// called), and confirms.
func TestDirectCommand_SessionOnAnotherWorker(t *testing.T) {
	bart := storage.Agent{ID: uuid.New(), Name: "Bart"}
	mgr := &fakeDirector{active: false}
	pool := &fakePool{live: true, campaignID: uuid.New()}
	resp := &fakeResponder{}

	if err := DirectCommand(mgr, &fakeLister{agents: []storage.Agent{bart}}, pool).Handle(context.Background(),
		directIC(resp, tenantA, bart.ID.String(), "whisper", 2)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.deferred == nil || !*resp.deferred {
		t.Fatal("cross-pod direct did not Defer(true)")
	}
	if len(mgr.calls) != 0 {
		t.Fatalf("local DirectAs called on the cross-pod path: %+v", mgr.calls)
	}
	if len(pool.directCalls) != 1 || pool.directCalls[0] != (poolDirectCall{bart.ID.String(), "whisper", 2}) {
		t.Fatalf("pool DirectAs calls = %+v, want the relayed directive", pool.directCalls)
	}
}

// TestDirectCommand_NoSessionAnywhere pins the plain guard: neither the local
// Manager nor the pool has a session — ephemeral refusal, nothing relayed.
func TestDirectCommand_NoSessionAnywhere(t *testing.T) {
	mgr := &fakeDirector{active: false}
	resp := &fakeResponder{}
	if err := DirectCommand(mgr, &fakeLister{}, nil).Handle(context.Background(),
		directIC(resp, tenantA, uuid.NewString(), "note", 0)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("reply = %+v, want one ephemeral no-session guard", resp.replies)
	}
	if len(mgr.calls) != 0 {
		t.Fatalf("DirectAs called with no session: %+v", mgr.calls)
	}
}
