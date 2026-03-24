//go:build integration

package sessionorch_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/gateway"
	"github.com/MrWong99/glyphoxa/internal/gateway/sessionorch"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testDSN reads the PostgreSQL DSN for integration tests.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GLYPHOXA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GLYPHOXA_TEST_POSTGRES_DSN not set; skipping integration test")
	}
	return dsn
}

// setupOrchestrator creates a PostgresOrchestrator connected to the test DB.
func setupOrchestrator(t *testing.T) *sessionorch.PostgresOrchestrator {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, testDSN(t))
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	orch, err := sessionorch.NewPostgresOrchestrator(ctx, pool)
	if err != nil {
		t.Fatalf("NewPostgresOrchestrator: %v", err)
	}
	return orch
}

// TestIntegration_SessionOrchestrator_Lifecycle tests the full session
// lifecycle through the PostgreSQL orchestrator: create → transition →
// heartbeat → query → cleanup.
func TestIntegration_SessionOrchestrator_Lifecycle(t *testing.T) {
	t.Parallel()

	orch := setupOrchestrator(t)
	ctx := context.Background()

	t.Run("create and get session", func(t *testing.T) {
		t.Parallel()
		orch := setupOrchestrator(t)

		sessionID, err := orch.ValidateAndCreate(ctx, sessionorch.SessionRequest{
			TenantID:    "test-lifecycle",
			CampaignID:  "campaign-lc-" + time.Now().Format("150405.000"),
			GuildID:     "guild-lc",
			ChannelID:   "channel-lc",
			LicenseTier: config.TierDedicated,
		})
		if err != nil {
			t.Fatalf("ValidateAndCreate: %v", err)
		}
		if sessionID == "" {
			t.Fatal("empty session ID")
		}

		session, err := orch.GetSession(ctx, sessionID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if session.State != gateway.SessionPending {
			t.Errorf("state = %v, want pending", session.State)
		}
		if session.TenantID != "test-lifecycle" {
			t.Errorf("tenant = %q, want test-lifecycle", session.TenantID)
		}
	})

	t.Run("transition pending to active to ended", func(t *testing.T) {
		t.Parallel()
		orch := setupOrchestrator(t)

		sessionID, err := orch.ValidateAndCreate(ctx, sessionorch.SessionRequest{
			TenantID:    "test-transition",
			CampaignID:  "campaign-tr-" + time.Now().Format("150405.000"),
			GuildID:     "guild-tr",
			ChannelID:   "channel-tr",
			LicenseTier: config.TierDedicated,
		})
		if err != nil {
			t.Fatalf("ValidateAndCreate: %v", err)
		}

		// Transition to active.
		if err := orch.Transition(ctx, sessionID, gateway.SessionActive, ""); err != nil {
			t.Fatalf("Transition(active): %v", err)
		}

		session, _ := orch.GetSession(ctx, sessionID)
		if session.State != gateway.SessionActive {
			t.Errorf("state = %v, want active", session.State)
		}

		// Transition to ended.
		if err := orch.Transition(ctx, sessionID, gateway.SessionEnded, "session complete"); err != nil {
			t.Fatalf("Transition(ended): %v", err)
		}

		session, _ = orch.GetSession(ctx, sessionID)
		if session.State != gateway.SessionEnded {
			t.Errorf("state = %v, want ended", session.State)
		}
		if session.Error != "session complete" {
			t.Errorf("error = %q, want 'session complete'", session.Error)
		}
		if session.EndedAt == nil {
			t.Error("ended_at should be set")
		}
	})

	t.Run("heartbeat updates timestamp", func(t *testing.T) {
		t.Parallel()
		orch := setupOrchestrator(t)

		sessionID, err := orch.ValidateAndCreate(ctx, sessionorch.SessionRequest{
			TenantID:    "test-heartbeat",
			CampaignID:  "campaign-hb-" + time.Now().Format("150405.000"),
			GuildID:     "guild-hb",
			ChannelID:   "channel-hb",
			LicenseTier: config.TierDedicated,
		})
		if err != nil {
			t.Fatalf("ValidateAndCreate: %v", err)
		}

		if err := orch.Transition(ctx, sessionID, gateway.SessionActive, ""); err != nil {
			t.Fatalf("Transition(active): %v", err)
		}

		if err := orch.RecordHeartbeat(ctx, sessionID); err != nil {
			t.Fatalf("RecordHeartbeat: %v", err)
		}

		session, _ := orch.GetSession(ctx, sessionID)
		if session.LastHeartbeat == nil {
			t.Error("last_heartbeat should be set after heartbeat")
		}
	})

	t.Run("active sessions query", func(t *testing.T) {
		t.Parallel()
		orch := setupOrchestrator(t)
		tenantID := "test-active-query"

		sessionID, _ := orch.ValidateAndCreate(ctx, sessionorch.SessionRequest{
			TenantID:    tenantID,
			CampaignID:  "campaign-aq-" + time.Now().Format("150405.000"),
			GuildID:     "guild-aq",
			ChannelID:   "channel-aq",
			LicenseTier: config.TierDedicated,
		})
		orch.Transition(ctx, sessionID, gateway.SessionActive, "")

		sessions, err := orch.ActiveSessions(ctx, tenantID)
		if err != nil {
			t.Fatalf("ActiveSessions: %v", err)
		}
		if len(sessions) == 0 {
			t.Error("expected at least 1 active session")
		}

		found := false
		for _, s := range sessions {
			if s.ID == sessionID {
				found = true
				if s.State != gateway.SessionActive {
					t.Errorf("state = %v, want active", s.State)
				}
			}
		}
		if !found {
			t.Error("created session not found in active list")
		}
	})

	t.Run("all non-ended sessions", func(t *testing.T) {
		t.Parallel()
		_ = orch

		sessions, err := setupOrchestrator(t).AllNonEndedSessions(ctx)
		if err != nil {
			t.Fatalf("AllNonEndedSessions: %v", err)
		}
		// Just verify it doesn't error — count depends on other test state.
		_ = sessions
	})

	t.Run("cleanup stale pending", func(t *testing.T) {
		t.Parallel()
		orch := setupOrchestrator(t)

		// Create a session and leave it pending.
		_, _ = orch.ValidateAndCreate(ctx, sessionorch.SessionRequest{
			TenantID:    "test-stale",
			CampaignID:  "campaign-stale-" + time.Now().Format("150405.000"),
			GuildID:     "guild-stale",
			ChannelID:   "channel-stale",
			LicenseTier: config.TierDedicated,
		})

		// Cleanup with a very long maxAge — should not clean anything.
		count, err := orch.CleanupStalePending(ctx, 24*time.Hour)
		if err != nil {
			t.Fatalf("CleanupStalePending: %v", err)
		}
		// We can't assert count == 0 because other tests might have stale sessions.
		_ = count
	})

	t.Run("cleanup zombies", func(t *testing.T) {
		t.Parallel()
		orch := setupOrchestrator(t)

		// Create a session with a heartbeat and make it active.
		sessionID, _ := orch.ValidateAndCreate(ctx, sessionorch.SessionRequest{
			TenantID:    "test-zombie",
			CampaignID:  "campaign-zombie-" + time.Now().Format("150405.000"),
			GuildID:     "guild-zombie",
			ChannelID:   "channel-zombie",
			LicenseTier: config.TierDedicated,
		})
		orch.Transition(ctx, sessionID, gateway.SessionActive, "")
		orch.RecordHeartbeat(ctx, sessionID)

		// Cleanup with a very long timeout — should not clean our session.
		count, err := orch.CleanupZombies(ctx, 24*time.Hour)
		if err != nil {
			t.Fatalf("CleanupZombies: %v", err)
		}
		_ = count

		// Verify our session is still active.
		session, _ := orch.GetSession(ctx, sessionID)
		if session.State != gateway.SessionActive {
			t.Errorf("session should still be active, got %v", session.State)
		}
	})
}
