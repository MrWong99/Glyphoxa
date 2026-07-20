package session_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// newControlIntentControl builds an IntentControl over a fakeControlStore with a
// LIVE intent for tenantID, millisecond poll and the given control budget.
func newControlIntentControl(t *testing.T, budget time.Duration) (*session.IntentControl, *fakeControlStore, uuid.UUID) {
	t.Helper()
	store := newFakeControlStore()
	tenantID := uuid.New()
	intent := store.add(tenantID, uuid.New())
	store.fakeIntentStore.mu.Lock()
	store.fakeIntentStore.intents[intent.ID].Status = storage.VoiceIntentLive
	store.fakeIntentStore.mu.Unlock()
	ic := session.NewIntentControl(store, slog.New(slog.DiscardHandler),
		session.IntentControlConfig{Poll: time.Millisecond, ControlBudget: budget})
	return ic, store, tenantID
}

// TestIntentControlSetAgentMute_RelaysToWorker covers sequence (9): SetAgentMute
// writes a mute_agent control row and returns the result ids once the (scripted)
// hosting worker flips the row done.
func TestIntentControlSetAgentMute_RelaysToWorker(t *testing.T) {
	ic, store, tenantID := newControlIntentControl(t, time.Second)
	var written storage.VoiceSessionControl
	store.onControlCreate = func(c *storage.VoiceSessionControl) {
		written = *c
		now := time.Now()
		c.Status = storage.VoiceControlDone
		c.ResultIDs = []string{"agent-1"}
		c.EndedAt = &now
	}

	ids, err := ic.SetAgentMute(context.Background(), tenantID, "agent-1", true)
	if err != nil {
		t.Fatalf("SetAgentMute: %v", err)
	}
	if len(ids) != 1 || ids[0] != "agent-1" {
		t.Fatalf("result ids = %v, want [agent-1]", ids)
	}
	if written.Kind != storage.VoiceControlMuteAgent || written.AgentID != "agent-1" || !written.Muted || written.TenantID != tenantID {
		t.Fatalf("written control = %+v, want a mute_agent row for agent-1", written)
	}
}

// TestIntentControlControl_WorkerFailureDecodes covers sequence (10): the worker
// writing failed with an ENCODED sentinel surfaces as the same typed error the
// -mode all path returns; an uncoded failure surfaces verbatim.
func TestIntentControlControl_WorkerFailureDecodes(t *testing.T) {
	ic, store, tenantID := newControlIntentControl(t, time.Second)
	store.onControlCreate = func(c *storage.VoiceSessionControl) {
		now := time.Now()
		c.Status = storage.VoiceControlFailed
		c.LastError = session.EncodeControlFailure(session.ErrButlerVoiceless)
		c.EndedAt = &now
	}
	if err := ic.SpeakAsButler(context.Background(), tenantID, "recap text"); !errors.Is(err, session.ErrButlerVoiceless) {
		t.Fatalf("SpeakAsButler err = %v, want ErrButlerVoiceless decoded", err)
	}

	store.onControlCreate = func(c *storage.VoiceSessionControl) {
		now := time.Now()
		c.Status = storage.VoiceControlFailed
		c.LastError = "tts provider exploded"
		c.EndedAt = &now
	}
	err := ic.SayAs(context.Background(), tenantID, "agent-1", "hi")
	if err == nil || !strings.Contains(err.Error(), "tts provider exploded") {
		t.Fatalf("SayAs err = %v, want the raw worker error surfaced verbatim", err)
	}
}

// TestIntentControlControl_BudgetAndNoSession covers sequence (11): a worker
// that never confirms yields ErrControlPending with the pending row cancelled;
// no live intent yields ErrNoActiveSession without writing any row.
func TestIntentControlControl_BudgetAndNoSession(t *testing.T) {
	ic, store, tenantID := newControlIntentControl(t, 20*time.Millisecond)

	var rowID uuid.UUID
	store.onControlCreate = func(c *storage.VoiceSessionControl) { rowID = c.ID }
	_, err := ic.SetAllMute(context.Background(), tenantID, true)
	if !errors.Is(err, session.ErrControlPending) {
		t.Fatalf("SetAllMute with a silent worker = %v, want ErrControlPending", err)
	}
	got := store.getControl(rowID)
	if got.Status != storage.VoiceControlFailed || got.LastError != "requester timed out" {
		t.Fatalf("row after budget expiry = %+v, want cancelled 'requester timed out'", got)
	}

	// No live intent for an unknown tenant → ErrNoActiveSession, no row written.
	before := len(store.controls)
	if _, err := ic.SetAgentMute(context.Background(), uuid.New(), "agent-1", true); !errors.Is(err, session.ErrNoActiveSession) {
		t.Fatalf("SetAgentMute with no live intent = %v, want ErrNoActiveSession", err)
	}
	if len(store.controls) != before {
		t.Fatal("a control row was written despite no live intent")
	}
}

// TestButlerControl_LocalFirstRemoteFallback covers sequence (17): with no local
// session the ButlerControl falls through to the remote relay; a local refusal
// OTHER than ErrNoActiveSession (here a voiceless Butler) does NOT — the local
// answer stands and the remote is untouched.
func TestButlerControl_LocalFirstRemoteFallback(t *testing.T) {
	// Local Manager idle → ErrNoActiveSession → remote relay handles it.
	idleMgr, _ := muteManager(t, newFakeStore())
	remote, store, tenantID := newControlIntentControl(t, time.Second)
	var relayed *storage.VoiceSessionControl
	store.onControlCreate = func(c *storage.VoiceSessionControl) {
		relayed = c
		now := time.Now()
		c.Status = storage.VoiceControlDone
		c.EndedAt = &now
	}
	bc := session.NewButlerControl(idleMgr, remote)
	if err := bc.SpeakAsButler(context.Background(), tenantID, "recap text"); err != nil {
		t.Fatalf("SpeakAsButler via remote: %v", err)
	}
	if relayed == nil || relayed.Kind != storage.VoiceControlButlerSay || relayed.SayText != "recap text" {
		t.Fatalf("relayed control = %+v, want a butler_say row", relayed)
	}

	// Local session live but the Butler is voiceless: the LOCAL refusal stands,
	// the remote is never consulted.
	mstore := newFakeStore()
	mgr, _ := muteManager(t, mstore)
	localTenant, _ := startMuteSession(t, mgr)
	mstore.mu.Lock()
	mstore.agents = []storage.Agent{{ID: uuid.New(), Role: storage.AgentRoleButler, Name: "Butler"}}
	mstore.mu.Unlock()
	relayed = nil
	bc = session.NewButlerControl(mgr, remote)
	if err := bc.SpeakAsButler(context.Background(), localTenant, "recap"); !errors.Is(err, session.ErrButlerVoiceless) {
		t.Fatalf("local voiceless butler = %v, want ErrButlerVoiceless (no remote fallback)", err)
	}
	if relayed != nil {
		t.Fatal("remote relay consulted despite a local non-ErrNoActiveSession answer")
	}
}
