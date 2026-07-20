package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// IntentControl is the web tier's split-mode SessionManager (#491, ADR-0057): in
// a -mode web + -mode voice deployment the web tier does NOT drive the voice loop
// in-process. Instead Start writes a voice_session_intents row and polls the
// claim plane until a -mode voice worker claims and drives it live; Stop flags
// the intent and polls until the worker winds the session down; Active reads the
// Tenant's live intent. It is a drop-in for *Manager on the web tier's RPC
// surface: the mgr-only live controls (mute/say/replay/spend) degrade with
// ErrSplitMode because the live session state lives in the worker, not here.

// IntentControlStore is the claim-plane + voice_sessions surface IntentControl
// needs. *storage.Store satisfies it; tests use a fake.
type IntentControlStore interface {
	CreateVoiceSessionIntent(ctx context.Context, tenantID, campaignID uuid.UUID) (storage.VoiceSessionIntent, error)
	RequestVoiceSessionStop(ctx context.Context, id uuid.UUID) (storage.VoiceSessionIntent, error)
	GetVoiceSessionIntent(ctx context.Context, id uuid.UUID) (storage.VoiceSessionIntent, error)
	GetLiveVoiceSessionIntentForTenant(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSessionIntent, error)
	GetVoiceSession(ctx context.Context, id uuid.UUID) (storage.VoiceSession, error)
	IsCampaignLiveIntent(ctx context.Context, campaignID uuid.UUID) (bool, error)
	AnyLiveVoiceSessionIntent(ctx context.Context) (bool, error)
	// ReapVoiceSessionIntentIfExpired marks the given claimed/live intent dead when
	// its heartbeat is stale — the zero-worker escape (#491 review item 4): Start
	// unblocks a Tenant whose prior worker died and left a claimed/live row no tick
	// will ever sweep. Returns whether it reaped.
	ReapVoiceSessionIntentIfExpired(ctx context.Context, id uuid.UUID, expiry time.Duration) (bool, error)
}

// IntentControlConfig carries IntentControl's poll cadence and budgets (#491). A
// non-positive value takes its default.
type IntentControlConfig struct {
	// Poll is the tick between claim-plane reads while Start/Stop wait. Default 1s.
	Poll time.Duration
	// StartBudget bounds how long Start waits for a worker to drive the intent live
	// before returning ErrIntentPending (the operator retries). Default 20s.
	StartBudget time.Duration
	// StopBudget bounds how long Stop waits for the worker to wind the session down
	// before returning ErrStopPending. Default 30s.
	StopBudget time.Duration
	// Expiry is the heartbeat-staleness horizon (matching the worker's
	// GLYPHOXA_VOICE_HEARTBEAT_EXPIRY): Start reaps a blocking claimed/live intent
	// whose heartbeat is older than this before failing ErrSessionActive — the
	// zero-worker escape (review item 4). Default 30s.
	Expiry time.Duration
}

// IntentControl drives voice sessions through the Postgres claim plane (#491).
type IntentControl struct {
	store IntentControlStore
	log   *slog.Logger
	cfg   IntentControlConfig
}

// NewIntentControl builds the web tier's split-mode session control over the
// claim-plane store. A non-positive config duration takes its default.
func NewIntentControl(store IntentControlStore, log *slog.Logger, cfg IntentControlConfig) *IntentControl {
	if log == nil {
		log = slog.Default()
	}
	if cfg.Poll <= 0 {
		cfg.Poll = time.Second
	}
	if cfg.StartBudget <= 0 {
		cfg.StartBudget = 20 * time.Second
	}
	if cfg.StopBudget <= 0 {
		cfg.StopBudget = 30 * time.Second
	}
	if cfg.Expiry <= 0 {
		cfg.Expiry = 30 * time.Second
	}
	return &IntentControl{store: store, log: log, cfg: cfg}
}

// Start writes a pending intent for the Tenant's Campaign and polls the claim
// plane until a worker drives it live (returning the loaded voice_sessions row),
// it fails/dies (an error carrying the recorded last_error), or the Start budget
// elapses (ErrIntentPending → the RPC's CodeUnavailable, the operator retries).
// A duplicate live-per-tenant intent (storage.ErrIntentActive) surfaces as
// ErrSessionActive so the RPC maps it to CodeAlreadyExists exactly like the
// in-process Manager's per-Tenant guard.
func (c *IntentControl) Start(ctx context.Context, tenantID, campaignID uuid.UUID) (storage.VoiceSession, error) {
	intent, err := c.store.CreateVoiceSessionIntent(ctx, tenantID, campaignID)
	if errors.Is(err, storage.ErrIntentActive) {
		// Zero-worker escape (review item 4): the blocking intent may be a dead
		// worker's claimed/live row that no tick will ever sweep. Reap it if its
		// heartbeat is stale, then retry the create ONCE. A still-live row (fresh
		// beat) or an in-flight pending Start is left alone → ErrSessionActive.
		if reaped, rerr := c.reapBlockingIfExpired(ctx, tenantID); rerr == nil && reaped {
			intent, err = c.store.CreateVoiceSessionIntent(ctx, tenantID, campaignID)
		}
		if errors.Is(err, storage.ErrIntentActive) {
			return storage.VoiceSession{}, ErrSessionActive
		}
	}
	if err != nil {
		return storage.VoiceSession{}, fmt.Errorf("session: create voice session intent: %w", err)
	}

	deadline := time.Now().Add(c.cfg.StartBudget)
	ticker := time.NewTicker(c.cfg.Poll)
	defer ticker.Stop()
	for {
		cur, err := c.store.GetVoiceSessionIntent(ctx, intent.ID)
		if err != nil {
			// Abandoning the poll: best-effort cancel the intent (mirrors the deadline
			// path) so a worker booting later never claims a row nobody is watching.
			c.cancelAbandonedIntent(intent.ID)
			return storage.VoiceSession{}, fmt.Errorf("session: poll voice session intent: %w", err)
		}
		switch cur.Status {
		case storage.VoiceIntentLive:
			if cur.VoiceSessionID.Valid {
				vs, err := c.store.GetVoiceSession(ctx, cur.VoiceSessionID.UUID)
				if err != nil {
					return storage.VoiceSession{}, fmt.Errorf("session: load live voice session: %w", err)
				}
				return vs, nil
			}
			// live but the id has not landed yet — keep polling.
		case storage.VoiceIntentFailed, storage.VoiceIntentDead:
			return storage.VoiceSession{}, fmt.Errorf("session: voice worker could not start the session: %s", intentReason(cur))
		case storage.VoiceIntentDone:
			// Stopped before it ever went live (an external stop hit the pending row):
			// a distinct cancelled outcome, NOT the still-queued ErrIntentPending
			// (review item 7).
			return storage.VoiceSession{}, ErrIntentCancelled
		}
		if time.Now().After(deadline) {
			// Budget spent and still not live: CANCEL the pending intent so a retry
			// does not 23505 into a dead-end AlreadyExists and a worker booting later
			// does not claim a stale row nobody is watching (review item 3). Then
			// "try again shortly" is honest. A concurrent claim turns the cancel into
			// a stop_requested the claiming worker honors — also correct.
			if _, cerr := c.store.RequestVoiceSessionStop(ctx, intent.ID); cerr != nil && !errors.Is(cerr, storage.ErrNotFound) {
				c.log.Warn("intent control: cancel pending intent after start timeout", "intent", intent.ID, "err", cerr)
			}
			return storage.VoiceSession{}, ErrIntentPending
		}
		select {
		case <-ctx.Done():
			// The caller abandoned the Start (RPC ctx cancelled): best-effort cancel
			// the intent on a detached ctx — the pending row would otherwise sit for a
			// worker to claim with nobody watching (or linger until the Start budget of
			// a retry sweeps it). A concurrent claim turns this into a stop_requested
			// the claiming worker honors — also correct (same as the deadline path).
			c.cancelAbandonedIntent(intent.ID)
			return storage.VoiceSession{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// cancelAbandonedIntentTimeout bounds the detached best-effort cancel write when
// Start abandons its poll (ctx cancelled, or a poll error).
const cancelAbandonedIntentTimeout = 3 * time.Second

// cancelAbandonedIntent best-effort cancels an intent Start stopped watching, on
// a detached short ctx (the caller's ctx may already be cancelled). A pending row
// resolves straight to 'done'; a claimed/live one becomes a stop_requested the
// owning worker honors. ErrNotFound (already terminal) is expected and silent.
func (c *IntentControl) cancelAbandonedIntent(intentID uuid.UUID) {
	dctx, cancel := context.WithTimeout(context.Background(), cancelAbandonedIntentTimeout)
	defer cancel()
	if _, err := c.store.RequestVoiceSessionStop(dctx, intentID); err != nil && !errors.Is(err, storage.ErrNotFound) {
		c.log.Warn("intent control: cancel abandoned intent", "intent", intentID, "err", err)
	}
}

// reapBlockingIfExpired reaps the Tenant's current blocking intent when it is a
// claimed/live row whose heartbeat is stale (its worker died) — the single-row
// zero-worker escape (review item 4). A pending or fresh row is left untouched
// (false). It is best-effort: any error is returned so Start falls back to the
// plain ErrSessionActive.
func (c *IntentControl) reapBlockingIfExpired(ctx context.Context, tenantID uuid.UUID) (bool, error) {
	blocking, err := c.store.GetLiveVoiceSessionIntentForTenant(ctx, tenantID)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil // it already cleared — the retry will succeed
	}
	if err != nil {
		return false, err
	}
	if blocking.Status != storage.VoiceIntentClaimed && blocking.Status != storage.VoiceIntentLive {
		return false, nil // pending: an in-flight Start owns it, not a dead worker
	}
	return c.store.ReapVoiceSessionIntentIfExpired(ctx, blocking.ID, c.cfg.Expiry)
}

// Stop flags the Tenant's live intent for the owning worker to wind down and
// polls until the intent goes terminal (done/dead/failed) or the Stop budget
// elapses, returning the closed voice_sessions row when one is bound. No live
// intent for the Tenant is ErrNoActiveSession.
func (c *IntentControl) Stop(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSession, error) {
	live, err := c.store.GetLiveVoiceSessionIntentForTenant(ctx, tenantID)
	if errors.Is(err, storage.ErrNotFound) {
		return storage.VoiceSession{}, ErrNoActiveSession
	}
	if err != nil {
		return storage.VoiceSession{}, fmt.Errorf("session: load live intent: %w", err)
	}

	if _, err := c.store.RequestVoiceSessionStop(ctx, live.ID); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return storage.VoiceSession{}, fmt.Errorf("session: request voice session stop: %w", err)
	}

	deadline := time.Now().Add(c.cfg.StopBudget)
	ticker := time.NewTicker(c.cfg.Poll)
	defer ticker.Stop()
	for {
		cur, err := c.store.GetVoiceSessionIntent(ctx, live.ID)
		if err != nil {
			return storage.VoiceSession{}, fmt.Errorf("session: poll intent on stop: %w", err)
		}
		if isIntentTerminal(cur.Status) {
			return c.loadRow(ctx, cur)
		}
		if time.Now().After(deadline) {
			// The worker has not confirmed within the budget: the session may still be
			// running, so surface an error (→ CodeUnavailable, retry) rather than a
			// false success carrying a still-'running' row (review item 7).
			return storage.VoiceSession{}, ErrStopPending
		}
		select {
		case <-ctx.Done():
			return storage.VoiceSession{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Active reports the Tenant's LIVE Voice Session (the split-mode read backing
// GetSession / searchCampaign): a live intent with its voice_sessions row loaded.
// A pending/claimed intent (not yet live) reports no active session — the screen
// shows idle until the worker joins. No intent at all is likewise not-active.
func (c *IntentControl) Active(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSession, bool, error) {
	live, err := c.store.GetLiveVoiceSessionIntentForTenant(ctx, tenantID)
	if errors.Is(err, storage.ErrNotFound) {
		return storage.VoiceSession{}, false, nil
	}
	if err != nil {
		return storage.VoiceSession{}, false, err
	}
	if live.Status != storage.VoiceIntentLive || !live.VoiceSessionID.Valid {
		return storage.VoiceSession{}, false, nil
	}
	vs, err := c.store.GetVoiceSession(ctx, live.VoiceSessionID.UUID)
	if err != nil {
		return storage.VoiceSession{}, false, err
	}
	return vs, true, nil
}

// IsCampaignLive reports whether campaignID has a live intent anywhere in the
// pool — the split-mode archive/delete guard (#491). Errors degrade to false
// (the guard is a safety net; a DB blip should not falsely block, and the RPC
// re-reads). ctx-free to match the *Manager seam the web servers wire.
func (c *IntentControl) IsCampaignLive(campaignID uuid.UUID) bool {
	live, err := c.store.IsCampaignLiveIntent(context.Background(), campaignID)
	if err != nil {
		c.log.Warn("intent control: campaign live-guard read", "campaign", campaignID, "err", err)
		return false
	}
	return live
}

// AnyLive reports whether any intent is live in the pool — the split-mode Discord
// health short-circuit (#491, the claim-plane sibling of Manager.AnyLive).
func (c *IntentControl) AnyLive() bool {
	live, err := c.store.AnyLiveVoiceSessionIntent(context.Background())
	if err != nil {
		c.log.Warn("intent control: any-live health read", "err", err)
		return false
	}
	return live
}

// The live-session controls below are Manager-only: their state (the mute set,
// the spend meter, the outbound pump for say/replay) lives in the -mode voice
// worker, unreachable from the web tier. In a split deployment they degrade with
// ErrSplitMode (→ CodeFailedPrecondition) rather than lie (ADR-0057 consequence:
// no mute/say RPCs on the web tier of a split deployment).

// SetAgentMute is Manager-only (#491): ErrSplitMode in a split deployment.
func (c *IntentControl) SetAgentMute(context.Context, uuid.UUID, string, bool) ([]string, error) {
	return nil, ErrSplitMode
}

// SetAllMute is Manager-only (#491): ErrSplitMode in a split deployment.
func (c *IntentControl) SetAllMute(context.Context, uuid.UUID, bool) ([]string, error) {
	return nil, ErrSplitMode
}

// MutedAgentIDs is Manager-only (#491): nil on the web tier of a split deployment.
func (c *IntentControl) MutedAgentIDs(uuid.UUID) []string { return nil }

// Spend is Manager-only (#491): the zero Status on the web tier of a split
// deployment (the live meter rides the worker; the durable ledger is the truth).
func (c *IntentControl) Spend(uuid.UUID) spend.Status { return spend.Status{} }

// ReplayHighlight is Manager-only (#491): a live-channel replay needs the worker's
// outbound pump, so it is ErrSplitMode on the web tier of a split deployment.
func (c *IntentControl) ReplayHighlight(context.Context, uuid.UUID, string) error {
	return ErrSplitMode
}

// loadRow loads the voice_sessions row an intent bound, or the zero VoiceSession
// (nil id) when the intent never went live (no worker claimed it).
func (c *IntentControl) loadRow(ctx context.Context, intent storage.VoiceSessionIntent) (storage.VoiceSession, error) {
	if !intent.VoiceSessionID.Valid {
		return storage.VoiceSession{}, nil
	}
	vs, err := c.store.GetVoiceSession(ctx, intent.VoiceSessionID.UUID)
	if errors.Is(err, storage.ErrNotFound) {
		return storage.VoiceSession{}, nil
	}
	if err != nil {
		return storage.VoiceSession{}, fmt.Errorf("session: load voice session row: %w", err)
	}
	return vs, nil
}

// isIntentTerminal reports whether a status is a settled end state.
func isIntentTerminal(s storage.VoiceSessionIntentStatus) bool {
	switch s {
	case storage.VoiceIntentDone, storage.VoiceIntentDead, storage.VoiceIntentFailed:
		return true
	default:
		return false
	}
}

// intentReason renders an intent's failure cause for the Start error: its
// recorded last_error, or a generic phrase naming the terminal status.
func intentReason(intent storage.VoiceSessionIntent) string {
	if intent.LastError != "" {
		return intent.LastError
	}
	return string(intent.Status)
}
