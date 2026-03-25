package sessionorch

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/gateway"
)

func TestMemoryOrchestrator_ValidateAndCreate(t *testing.T) {
	t.Parallel()

	t.Run("creates session successfully", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		id, err := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "tenant1",
			CampaignID:  "camp1",
			GuildID:     "guild1",
			ChannelID:   "chan1",
			LicenseTier: config.TierDedicated,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id == "" {
			t.Fatal("expected non-empty session ID")
		}

		s, err := m.GetSession(ctx, id)
		if err != nil {
			t.Fatalf("get session: %v", err)
		}
		if s.State != gateway.SessionPending {
			t.Errorf("got state %v, want %v", s.State, gateway.SessionPending)
		}
		if s.TenantID != "tenant1" {
			t.Errorf("got tenant %q, want %q", s.TenantID, "tenant1")
		}
	})

	t.Run("rejects duplicate campaign", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		req := SessionRequest{
			TenantID:    "tenant1",
			CampaignID:  "camp1",
			GuildID:     "guild1",
			LicenseTier: config.TierDedicated,
		}
		if _, err := m.ValidateAndCreate(ctx, req); err != nil {
			t.Fatalf("first create: %v", err)
		}

		_, err := m.ValidateAndCreate(ctx, req)
		if err == nil {
			t.Fatal("expected error for duplicate campaign")
		}
	})

	t.Run("allows same campaign after previous ends", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		req := SessionRequest{
			TenantID:    "tenant1",
			CampaignID:  "camp1",
			GuildID:     "guild1",
			LicenseTier: config.TierDedicated,
		}
		id, err := m.ValidateAndCreate(ctx, req)
		if err != nil {
			t.Fatalf("first create: %v", err)
		}

		if err := m.Transition(ctx, id, gateway.SessionEnded, "done"); err != nil {
			t.Fatalf("transition: %v", err)
		}

		if _, err := m.ValidateAndCreate(ctx, req); err != nil {
			t.Fatalf("second create after end: %v", err)
		}
	})

	t.Run("shared tier allows only one active session per tenant", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		req1 := SessionRequest{
			TenantID:    "tenant1",
			CampaignID:  "camp1",
			GuildID:     "guild1",
			LicenseTier: config.TierShared,
		}
		if _, err := m.ValidateAndCreate(ctx, req1); err != nil {
			t.Fatalf("first create: %v", err)
		}

		req2 := SessionRequest{
			TenantID:    "tenant1",
			CampaignID:  "camp2",
			GuildID:     "guild2",
			LicenseTier: config.TierShared,
		}
		_, err := m.ValidateAndCreate(ctx, req2)
		if err == nil {
			t.Fatal("expected error for shared tier second session")
		}
	})

	t.Run("dedicated tier allows one session per guild", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		req1 := SessionRequest{
			TenantID:    "tenant1",
			CampaignID:  "camp1",
			GuildID:     "guild1",
			LicenseTier: config.TierDedicated,
		}
		if _, err := m.ValidateAndCreate(ctx, req1); err != nil {
			t.Fatalf("first create: %v", err)
		}

		// Same guild, different campaign — should fail.
		req2 := SessionRequest{
			TenantID:    "tenant1",
			CampaignID:  "camp2",
			GuildID:     "guild1",
			LicenseTier: config.TierDedicated,
		}
		if _, err := m.ValidateAndCreate(ctx, req2); err == nil {
			t.Fatal("expected error for same guild")
		}

		// Different guild — should succeed.
		req3 := SessionRequest{
			TenantID:    "tenant1",
			CampaignID:  "camp3",
			GuildID:     "guild2",
			LicenseTier: config.TierDedicated,
		}
		if _, err := m.ValidateAndCreate(ctx, req3); err != nil {
			t.Fatalf("different guild: %v", err)
		}
	})

	t.Run("different tenants are independent", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		req1 := SessionRequest{
			TenantID:    "tenant1",
			CampaignID:  "camp1",
			GuildID:     "guild1",
			LicenseTier: config.TierShared,
		}
		if _, err := m.ValidateAndCreate(ctx, req1); err != nil {
			t.Fatalf("tenant1 create: %v", err)
		}

		req2 := SessionRequest{
			TenantID:    "tenant2",
			CampaignID:  "camp2",
			GuildID:     "guild1",
			LicenseTier: config.TierShared,
		}
		if _, err := m.ValidateAndCreate(ctx, req2); err != nil {
			t.Fatalf("tenant2 create: %v", err)
		}
	})
}

func TestMemoryOrchestrator_Transition(t *testing.T) {
	t.Parallel()

	t.Run("transitions to active", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		id, _ := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c1",
			GuildID:     "g1",
			LicenseTier: config.TierShared,
		})

		if err := m.Transition(ctx, id, gateway.SessionActive, ""); err != nil {
			t.Fatalf("transition: %v", err)
		}

		s, _ := m.GetSession(ctx, id)
		if s.State != gateway.SessionActive {
			t.Errorf("got state %v, want %v", s.State, gateway.SessionActive)
		}
	})

	t.Run("transitions to ended sets EndedAt and Error", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		id, _ := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c1",
			GuildID:     "g1",
			LicenseTier: config.TierShared,
		})

		if err := m.Transition(ctx, id, gateway.SessionEnded, "something broke"); err != nil {
			t.Fatalf("transition: %v", err)
		}

		s, _ := m.GetSession(ctx, id)
		if s.State != gateway.SessionEnded {
			t.Errorf("got state %v, want %v", s.State, gateway.SessionEnded)
		}
		if s.EndedAt == nil {
			t.Fatal("expected EndedAt to be set")
		}
		if s.Error != "something broke" {
			t.Errorf("got error %q, want %q", s.Error, "something broke")
		}
	})

	t.Run("returns error for unknown session", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		if err := m.Transition(ctx, "nonexistent", gateway.SessionActive, ""); err == nil {
			t.Fatal("expected error for unknown session")
		}
	})
}

func TestMemoryOrchestrator_RecordHeartbeat(t *testing.T) {
	t.Parallel()

	m := NewMemoryOrchestrator()
	ctx := context.Background()

	id, _ := m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})

	if err := m.RecordHeartbeat(ctx, id); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	s, _ := m.GetSession(ctx, id)
	if s.LastHeartbeat == nil {
		t.Fatal("expected LastHeartbeat to be set")
	}

	if err := m.RecordHeartbeat(ctx, "nonexistent"); err == nil {
		t.Fatal("expected error for unknown session")
	}
}

func TestMemoryOrchestrator_ActiveSessions(t *testing.T) {
	t.Parallel()

	m := NewMemoryOrchestrator()
	ctx := context.Background()

	id1, _ := m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierDedicated,
	})
	_, _ = m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c2",
		GuildID:     "g2",
		LicenseTier: config.TierDedicated,
	})
	// Different tenant.
	_, _ = m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t2",
		CampaignID:  "c3",
		GuildID:     "g3",
		LicenseTier: config.TierShared,
	})

	active, err := m.ActiveSessions(ctx, "t1")
	if err != nil {
		t.Fatalf("active sessions: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("got %d active sessions, want 2", len(active))
	}

	// End one session — should return 1.
	_ = m.Transition(ctx, id1, gateway.SessionEnded, "")
	active, _ = m.ActiveSessions(ctx, "t1")
	if len(active) != 1 {
		t.Fatalf("got %d active sessions after end, want 1", len(active))
	}
}

func TestMemoryOrchestrator_CleanupZombies(t *testing.T) {
	t.Parallel()

	m := NewMemoryOrchestrator()
	ctx := context.Background()

	id, _ := m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})

	// No heartbeat yet — should not be cleaned up (heartbeat is nil).
	count, err := m.CleanupZombies(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if count != 0 {
		t.Fatalf("got %d cleaned up, want 0 (no heartbeat set)", count)
	}

	// Set heartbeat in the past.
	m.mu.Lock()
	past := time.Now().UTC().Add(-10 * time.Second)
	m.sessions[id].LastHeartbeat = &past
	m.mu.Unlock()

	count, err = m.CleanupZombies(ctx, 5*time.Second)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if count != 1 {
		t.Fatalf("got %d cleaned up, want 1", count)
	}

	s, _ := m.GetSession(ctx, id)
	if s.State != gateway.SessionEnded {
		t.Errorf("got state %v, want %v", s.State, gateway.SessionEnded)
	}
	if s.Error != "heartbeat timeout" {
		t.Errorf("got error %q, want %q", s.Error, "heartbeat timeout")
	}
}

func TestMemoryOrchestrator_CleanupStalePending(t *testing.T) {
	t.Parallel()

	t.Run("cleans up old pending sessions", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		id, _ := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c1",
			GuildID:     "g1",
			LicenseTier: config.TierShared,
		})

		// Backdate the session's StartedAt to simulate an old pending session.
		m.mu.Lock()
		m.sessions[id].StartedAt = time.Now().UTC().Add(-5 * time.Minute)
		m.mu.Unlock()

		count, err := m.CleanupStalePending(ctx, 2*time.Minute)
		if err != nil {
			t.Fatalf("cleanup: %v", err)
		}
		if count != 1 {
			t.Fatalf("got %d cleaned up, want 1", count)
		}

		s, _ := m.GetSession(ctx, id)
		if s.State != gateway.SessionEnded {
			t.Errorf("got state %v, want %v", s.State, gateway.SessionEnded)
		}
		if s.Error != "stale pending: dispatch timeout" {
			t.Errorf("got error %q, want %q", s.Error, "stale pending: dispatch timeout")
		}
	})

	t.Run("ignores recent pending sessions", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		_, _ = m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c1",
			GuildID:     "g1",
			LicenseTier: config.TierShared,
		})

		// Session was just created — should not be cleaned up.
		count, err := m.CleanupStalePending(ctx, 2*time.Minute)
		if err != nil {
			t.Fatalf("cleanup: %v", err)
		}
		if count != 0 {
			t.Fatalf("got %d cleaned up, want 0", count)
		}
	})

	t.Run("ignores active and ended sessions", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		id1, _ := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c1",
			GuildID:     "g1",
			LicenseTier: config.TierDedicated,
		})
		id2, _ := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c2",
			GuildID:     "g2",
			LicenseTier: config.TierDedicated,
		})

		// Transition one to active, one to ended.
		_ = m.Transition(ctx, id1, gateway.SessionActive, "")
		_ = m.Transition(ctx, id2, gateway.SessionEnded, "done")

		// Backdate both to be old.
		m.mu.Lock()
		old := time.Now().UTC().Add(-10 * time.Minute)
		m.sessions[id1].StartedAt = old
		m.sessions[id2].StartedAt = old
		m.mu.Unlock()

		count, err := m.CleanupStalePending(ctx, 2*time.Minute)
		if err != nil {
			t.Fatalf("cleanup: %v", err)
		}
		if count != 0 {
			t.Fatalf("got %d cleaned up, want 0 (neither is pending)", count)
		}
	})

	t.Run("frees tenant slot after cleanup", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		_, _ = m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c1",
			GuildID:     "g1",
			LicenseTier: config.TierShared,
		})

		// Second session for same shared tenant should fail.
		_, err := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c2",
			GuildID:     "g2",
			LicenseTier: config.TierShared,
		})
		if err == nil {
			t.Fatal("expected constraint error")
		}

		// Backdate and clean up the stale pending session.
		m.mu.Lock()
		for _, s := range m.sessions {
			s.StartedAt = time.Now().UTC().Add(-5 * time.Minute)
		}
		m.mu.Unlock()

		_, _ = m.CleanupStalePending(ctx, 2*time.Minute)

		// Now the tenant slot should be free.
		_, err = m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c2",
			GuildID:     "g2",
			LicenseTier: config.TierShared,
		})
		if err != nil {
			t.Fatalf("expected slot freed after cleanup, got: %v", err)
		}
	})
}

func TestMemoryOrchestrator_GetSession_NotFound(t *testing.T) {
	t.Parallel()

	m := NewMemoryOrchestrator()
	ctx := context.Background()

	_, err := m.GetSession(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
}

func TestMemoryOrchestrator_AllNonEndedSessions(t *testing.T) {
	t.Parallel()

	t.Run("returns empty for no sessions", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		sessions, err := m.AllNonEndedSessions(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sessions) != 0 {
			t.Fatalf("got %d sessions, want 0", len(sessions))
		}
	})

	t.Run("returns sessions across multiple tenants", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		_, _ = m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c1",
			GuildID:     "g1",
			LicenseTier: config.TierDedicated,
		})
		_, _ = m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t2",
			CampaignID:  "c2",
			GuildID:     "g2",
			LicenseTier: config.TierShared,
		})
		_, _ = m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t3",
			CampaignID:  "c3",
			GuildID:     "g3",
			LicenseTier: config.TierDedicated,
		})

		sessions, err := m.AllNonEndedSessions(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sessions) != 3 {
			t.Fatalf("got %d sessions, want 3", len(sessions))
		}
	})

	t.Run("excludes ended sessions", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		id1, _ := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c1",
			GuildID:     "g1",
			LicenseTier: config.TierDedicated,
		})
		_, _ = m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t2",
			CampaignID:  "c2",
			GuildID:     "g2",
			LicenseTier: config.TierShared,
		})

		// End the first session.
		_ = m.Transition(ctx, id1, gateway.SessionEnded, "done")

		sessions, err := m.AllNonEndedSessions(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sessions) != 1 {
			t.Fatalf("got %d sessions, want 1", len(sessions))
		}
		if sessions[0].TenantID != "t2" {
			t.Errorf("got tenant %q, want %q", sessions[0].TenantID, "t2")
		}
	})

	t.Run("includes both pending and active sessions", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		id1, _ := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c1",
			GuildID:     "g1",
			LicenseTier: config.TierDedicated,
		})
		_, _ = m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c2",
			GuildID:     "g2",
			LicenseTier: config.TierDedicated,
		})

		// Transition one to active, leave the other pending.
		_ = m.Transition(ctx, id1, gateway.SessionActive, "")

		sessions, err := m.AllNonEndedSessions(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sessions) != 2 {
			t.Fatalf("got %d sessions, want 2", len(sessions))
		}
	})

	t.Run("returns empty when all sessions ended", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		id1, _ := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c1",
			GuildID:     "g1",
			LicenseTier: config.TierDedicated,
		})
		id2, _ := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t2",
			CampaignID:  "c2",
			GuildID:     "g2",
			LicenseTier: config.TierShared,
		})

		_ = m.Transition(ctx, id1, gateway.SessionEnded, "")
		_ = m.Transition(ctx, id2, gateway.SessionEnded, "")

		sessions, err := m.AllNonEndedSessions(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sessions) != 0 {
			t.Fatalf("got %d sessions, want 0", len(sessions))
		}
	})
}

func TestMemoryOrchestrator_ValidateAndCreate_CrossTenantCampaignConflict(t *testing.T) {
	t.Parallel()

	// Campaign uniqueness is global, not per-tenant.
	m := NewMemoryOrchestrator()
	ctx := context.Background()

	_, err := m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "tenant1",
		CampaignID:  "shared-campaign",
		GuildID:     "g1",
		LicenseTier: config.TierDedicated,
	})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Different tenant, same campaign ID — should fail.
	_, err = m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "tenant2",
		CampaignID:  "shared-campaign",
		GuildID:     "g2",
		LicenseTier: config.TierDedicated,
	})
	if err == nil {
		t.Fatal("expected error: same campaign across different tenants should conflict")
	}
}

func TestMemoryOrchestrator_ValidateAndCreate_SharedTierAfterEnd(t *testing.T) {
	t.Parallel()

	m := NewMemoryOrchestrator()
	ctx := context.Background()

	id, err := m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	// End the session.
	if err := m.Transition(ctx, id, gateway.SessionEnded, ""); err != nil {
		t.Fatalf("transition: %v", err)
	}

	// Should now be able to create a new session for the same shared tenant.
	_, err = m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c2",
		GuildID:     "g2",
		LicenseTier: config.TierShared,
	})
	if err != nil {
		t.Fatalf("expected success after ending previous shared session, got: %v", err)
	}
}

func TestMemoryOrchestrator_ValidateAndCreate_DedicatedTierSameGuildAfterEnd(t *testing.T) {
	t.Parallel()

	m := NewMemoryOrchestrator()
	ctx := context.Background()

	id, err := m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierDedicated,
	})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	// End the session.
	if err := m.Transition(ctx, id, gateway.SessionEnded, ""); err != nil {
		t.Fatalf("transition: %v", err)
	}

	// Same guild, different campaign — should now succeed.
	_, err = m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c2",
		GuildID:     "g1",
		LicenseTier: config.TierDedicated,
	})
	if err != nil {
		t.Fatalf("expected success after ending previous guild session, got: %v", err)
	}
}

func TestMemoryOrchestrator_Transition_NonEndedDoesNotSetEndedAtOrError(t *testing.T) {
	t.Parallel()

	m := NewMemoryOrchestrator()
	ctx := context.Background()

	id, _ := m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})

	if err := m.Transition(ctx, id, gateway.SessionActive, "this should be ignored"); err != nil {
		t.Fatalf("transition: %v", err)
	}

	s, _ := m.GetSession(ctx, id)
	if s.EndedAt != nil {
		t.Error("expected EndedAt to remain nil for non-ended transition")
	}
	if s.Error != "" {
		t.Errorf("expected Error to remain empty for non-ended transition, got %q", s.Error)
	}
}

func TestMemoryOrchestrator_RecordHeartbeat_UpdatesTimestamp(t *testing.T) {
	t.Parallel()

	m := NewMemoryOrchestrator()
	ctx := context.Background()

	id, _ := m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})

	// First heartbeat.
	if err := m.RecordHeartbeat(ctx, id); err != nil {
		t.Fatalf("first heartbeat: %v", err)
	}
	s1, _ := m.GetSession(ctx, id)
	first := *s1.LastHeartbeat

	// Ensure at least a tick of time passes.
	time.Sleep(1 * time.Millisecond)

	// Second heartbeat — timestamp should advance.
	if err := m.RecordHeartbeat(ctx, id); err != nil {
		t.Fatalf("second heartbeat: %v", err)
	}
	s2, _ := m.GetSession(ctx, id)
	second := *s2.LastHeartbeat

	if !second.After(first) {
		t.Errorf("expected second heartbeat (%v) to be after first (%v)", second, first)
	}
}

func TestMemoryOrchestrator_ActiveSessions_EmptyTenant(t *testing.T) {
	t.Parallel()

	m := NewMemoryOrchestrator()
	ctx := context.Background()

	sessions, err := m.ActiveSessions(ctx, "nonexistent-tenant")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("got %d sessions, want 0", len(sessions))
	}
}

func TestMemoryOrchestrator_CleanupZombies_MixedSessions(t *testing.T) {
	t.Parallel()

	t.Run("only cleans stale heartbeats not fresh or nil", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		// Session 1: stale heartbeat — should be cleaned.
		id1, _ := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c1",
			GuildID:     "g1",
			LicenseTier: config.TierDedicated,
		})
		// Session 2: fresh heartbeat — should survive.
		id2, _ := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c2",
			GuildID:     "g2",
			LicenseTier: config.TierDedicated,
		})
		// Session 3: nil heartbeat — should survive.
		_, _ = m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c3",
			GuildID:     "g3",
			LicenseTier: config.TierDedicated,
		})
		// Session 4: already ended with stale heartbeat — should not be double-ended.
		id4, _ := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t2",
			CampaignID:  "c4",
			GuildID:     "g4",
			LicenseTier: config.TierShared,
		})

		m.mu.Lock()
		stale := time.Now().UTC().Add(-30 * time.Second)
		fresh := time.Now().UTC()
		m.sessions[id1].LastHeartbeat = &stale
		m.sessions[id2].LastHeartbeat = &fresh
		m.sessions[id4].LastHeartbeat = &stale
		m.sessions[id4].State = gateway.SessionEnded
		m.mu.Unlock()

		count, err := m.CleanupZombies(ctx, 10*time.Second)
		if err != nil {
			t.Fatalf("cleanup: %v", err)
		}
		if count != 1 {
			t.Fatalf("got %d cleaned up, want 1", count)
		}

		// Verify id1 was ended.
		s1, _ := m.GetSession(ctx, id1)
		if s1.State != gateway.SessionEnded {
			t.Errorf("session 1: got state %v, want %v", s1.State, gateway.SessionEnded)
		}
		if s1.EndedAt == nil {
			t.Error("session 1: expected EndedAt to be set")
		}

		// Verify id2 still active.
		s2, _ := m.GetSession(ctx, id2)
		if s2.State == gateway.SessionEnded {
			t.Error("session 2: should not have been cleaned up (fresh heartbeat)")
		}
	})

	t.Run("frees guild slot after zombie cleanup", func(t *testing.T) {
		t.Parallel()
		m := NewMemoryOrchestrator()
		ctx := context.Background()

		id, _ := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c1",
			GuildID:     "g1",
			LicenseTier: config.TierDedicated,
		})

		// Set stale heartbeat.
		m.mu.Lock()
		stale := time.Now().UTC().Add(-20 * time.Second)
		m.sessions[id].LastHeartbeat = &stale
		m.mu.Unlock()

		_, _ = m.CleanupZombies(ctx, 5*time.Second)

		// Should be able to create a new session for the same guild.
		_, err := m.ValidateAndCreate(ctx, SessionRequest{
			TenantID:    "t1",
			CampaignID:  "c2",
			GuildID:     "g1",
			LicenseTier: config.TierDedicated,
		})
		if err != nil {
			t.Fatalf("expected guild slot freed after zombie cleanup, got: %v", err)
		}
	})
}

func TestMemoryOrchestrator_CleanupStalePending_MultipleSessions(t *testing.T) {
	t.Parallel()

	m := NewMemoryOrchestrator()
	ctx := context.Background()

	id1, _ := m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierDedicated,
	})
	id2, _ := m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c2",
		GuildID:     "g2",
		LicenseTier: config.TierDedicated,
	})
	id3, _ := m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t2",
		CampaignID:  "c3",
		GuildID:     "g3",
		LicenseTier: config.TierShared,
	})

	// Backdate only two of three.
	m.mu.Lock()
	old := time.Now().UTC().Add(-10 * time.Minute)
	m.sessions[id1].StartedAt = old
	m.sessions[id3].StartedAt = old
	m.mu.Unlock()

	count, err := m.CleanupStalePending(ctx, 2*time.Minute)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if count != 2 {
		t.Fatalf("got %d cleaned up, want 2", count)
	}

	// id2 should still be pending.
	s2, _ := m.GetSession(ctx, id2)
	if s2.State != gateway.SessionPending {
		t.Errorf("session 2: got state %v, want %v", s2.State, gateway.SessionPending)
	}
}

func TestMemoryOrchestrator_ValidateAndCreate_SessionFieldsPersisted(t *testing.T) {
	t.Parallel()

	m := NewMemoryOrchestrator()
	ctx := context.Background()

	id, err := m.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "tenant-abc",
		CampaignID:  "camp-xyz",
		GuildID:     "guild-123",
		ChannelID:   "chan-456",
		LicenseTier: config.TierDedicated,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	s, err := m.GetSession(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if s.ID != id {
		t.Errorf("ID: got %q, want %q", s.ID, id)
	}
	if s.TenantID != "tenant-abc" {
		t.Errorf("TenantID: got %q, want %q", s.TenantID, "tenant-abc")
	}
	if s.CampaignID != "camp-xyz" {
		t.Errorf("CampaignID: got %q, want %q", s.CampaignID, "camp-xyz")
	}
	if s.GuildID != "guild-123" {
		t.Errorf("GuildID: got %q, want %q", s.GuildID, "guild-123")
	}
	if s.ChannelID != "chan-456" {
		t.Errorf("ChannelID: got %q, want %q", s.ChannelID, "chan-456")
	}
	if s.LicenseTier != config.TierDedicated {
		t.Errorf("LicenseTier: got %v, want %v", s.LicenseTier, config.TierDedicated)
	}
	if s.State != gateway.SessionPending {
		t.Errorf("State: got %v, want %v", s.State, gateway.SessionPending)
	}
	if s.StartedAt.IsZero() {
		t.Error("StartedAt should not be zero")
	}
	if s.EndedAt != nil {
		t.Error("EndedAt should be nil for new session")
	}
	if s.LastHeartbeat != nil {
		t.Error("LastHeartbeat should be nil for new session")
	}
	if s.Error != "" {
		t.Errorf("Error should be empty, got %q", s.Error)
	}
}

func TestOrchestratorAdapter(t *testing.T) {
	t.Parallel()

	t.Run("ValidateAndCreate delegates correctly", func(t *testing.T) {
		t.Parallel()
		orch := NewMemoryOrchestrator()
		adapter := NewOrchestratorAdapter(orch)
		ctx := context.Background()

		id, err := adapter.ValidateAndCreate(ctx, "t1", "c1", "g1", "ch1", config.TierDedicated)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if id == "" {
			t.Fatal("expected non-empty session ID")
		}

		// Verify session was created in the underlying orchestrator.
		s, err := orch.GetSession(ctx, id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if s.TenantID != "t1" || s.CampaignID != "c1" || s.GuildID != "g1" || s.ChannelID != "ch1" {
			t.Errorf("session fields not propagated correctly: %+v", s)
		}
	})

	t.Run("Transition delegates correctly", func(t *testing.T) {
		t.Parallel()
		orch := NewMemoryOrchestrator()
		adapter := NewOrchestratorAdapter(orch)
		ctx := context.Background()

		id, _ := adapter.ValidateAndCreate(ctx, "t1", "c1", "g1", "ch1", config.TierShared)

		if err := adapter.Transition(ctx, id, gateway.SessionActive, ""); err != nil {
			t.Fatalf("transition: %v", err)
		}

		s, _ := orch.GetSession(ctx, id)
		if s.State != gateway.SessionActive {
			t.Errorf("got state %v, want %v", s.State, gateway.SessionActive)
		}
	})

	t.Run("GetSessionInfo maps fields", func(t *testing.T) {
		t.Parallel()
		orch := NewMemoryOrchestrator()
		adapter := NewOrchestratorAdapter(orch)
		ctx := context.Background()

		id, _ := adapter.ValidateAndCreate(ctx, "t1", "camp-name", "g1", "ch1", config.TierDedicated)

		info, err := adapter.GetSessionInfo(ctx, id)
		if err != nil {
			t.Fatalf("get session info: %v", err)
		}
		if info.SessionID != id {
			t.Errorf("SessionID: got %q, want %q", info.SessionID, id)
		}
		if info.GuildID != "g1" {
			t.Errorf("GuildID: got %q, want %q", info.GuildID, "g1")
		}
		if info.ChannelID != "ch1" {
			t.Errorf("ChannelID: got %q, want %q", info.ChannelID, "ch1")
		}
		if info.CampaignName != "camp-name" {
			t.Errorf("CampaignName: got %q, want %q", info.CampaignName, "camp-name")
		}
		if info.State != gateway.SessionPending {
			t.Errorf("State: got %v, want %v", info.State, gateway.SessionPending)
		}
		if info.StartedAt.IsZero() {
			t.Error("StartedAt should not be zero")
		}
	})

	t.Run("GetSessionInfo returns error for unknown session", func(t *testing.T) {
		t.Parallel()
		orch := NewMemoryOrchestrator()
		adapter := NewOrchestratorAdapter(orch)
		ctx := context.Background()

		_, err := adapter.GetSessionInfo(ctx, "nonexistent")
		if err == nil {
			t.Fatal("expected error for unknown session")
		}
	})

	t.Run("ListActiveSessionIDs returns correct IDs", func(t *testing.T) {
		t.Parallel()
		orch := NewMemoryOrchestrator()
		adapter := NewOrchestratorAdapter(orch)
		ctx := context.Background()

		id1, _ := adapter.ValidateAndCreate(ctx, "t1", "c1", "g1", "ch1", config.TierDedicated)
		id2, _ := adapter.ValidateAndCreate(ctx, "t1", "c2", "g2", "ch2", config.TierDedicated)
		// Different tenant — should not appear.
		_, _ = adapter.ValidateAndCreate(ctx, "t2", "c3", "g3", "ch3", config.TierShared)

		ids, err := adapter.ListActiveSessionIDs(ctx, "t1")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(ids) != 2 {
			t.Fatalf("got %d IDs, want 2", len(ids))
		}

		// Check both IDs are present (order not guaranteed from map iteration).
		found := map[string]bool{}
		for _, id := range ids {
			found[id] = true
		}
		if !found[id1] || !found[id2] {
			t.Errorf("expected IDs %q and %q, got %v", id1, id2, ids)
		}
	})

	t.Run("ListActiveSessionIDs excludes ended", func(t *testing.T) {
		t.Parallel()
		orch := NewMemoryOrchestrator()
		adapter := NewOrchestratorAdapter(orch)
		ctx := context.Background()

		id1, _ := adapter.ValidateAndCreate(ctx, "t1", "c1", "g1", "ch1", config.TierDedicated)
		_, _ = adapter.ValidateAndCreate(ctx, "t1", "c2", "g2", "ch2", config.TierDedicated)

		_ = adapter.Transition(ctx, id1, gateway.SessionEnded, "")

		ids, err := adapter.ListActiveSessionIDs(ctx, "t1")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(ids) != 1 {
			t.Fatalf("got %d IDs, want 1", len(ids))
		}
	})

	t.Run("ListActiveSessionIDs returns empty for unknown tenant", func(t *testing.T) {
		t.Parallel()
		orch := NewMemoryOrchestrator()
		adapter := NewOrchestratorAdapter(orch)
		ctx := context.Background()

		ids, err := adapter.ListActiveSessionIDs(ctx, "nonexistent")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(ids) != 0 {
			t.Fatalf("got %d IDs, want 0", len(ids))
		}
	})
}

func TestCallbackBridge_ReportState_Error(t *testing.T) {
	t.Parallel()

	orch := NewMemoryOrchestrator()
	cb := NewCallbackBridge(orch)
	ctx := context.Background()

	// ReportState for nonexistent session should propagate the error.
	if err := cb.ReportState(ctx, "nonexistent", gateway.SessionActive, ""); err == nil {
		t.Fatal("expected error for unknown session")
	}
}

func TestCallbackBridge_Heartbeat_Error(t *testing.T) {
	t.Parallel()

	orch := NewMemoryOrchestrator()
	cb := NewCallbackBridge(orch)
	ctx := context.Background()

	// Heartbeat for nonexistent session should propagate the error.
	if err := cb.Heartbeat(ctx, "nonexistent"); err == nil {
		t.Fatal("expected error for unknown session")
	}
}
