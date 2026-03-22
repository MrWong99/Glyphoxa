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
