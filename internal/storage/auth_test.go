//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// migrated stands up a freshly migrated DB and returns a Store over it.
func migrated(t *testing.T) *storage.Store {
	t.Helper()
	dsn := startPostgres(t)
	db := openSQL(t, dsn)
	if err := storage.MigrateUp(context.Background(), db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	return storage.New(openPool(t, dsn))
}

func TestUpsertUserInsertThenRefresh(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, storage.UpsertUserParams{
		DiscordUserID: "1234567890", Name: "Sora Vance", Avatar: "https://cdn/x.png",
	})
	if err != nil {
		t.Fatalf("UpsertUser insert: %v", err)
	}
	if u.ID == uuid.Nil || u.Name != "Sora Vance" || u.Role != "operator" {
		t.Fatalf("insert user = %+v, want named operator with id", u)
	}

	// A second upsert for the same Discord id refreshes name/avatar in place — no
	// duplicate row, same id, role preserved.
	u2, err := st.UpsertUser(ctx, storage.UpsertUserParams{
		DiscordUserID: "1234567890", Name: "Sora V.", Avatar: "https://cdn/y.png",
	})
	if err != nil {
		t.Fatalf("UpsertUser refresh: %v", err)
	}
	if u2.ID != u.ID {
		t.Errorf("refresh created a new row: id %s != %s", u2.ID, u.ID)
	}
	if u2.Name != "Sora V." || u2.Avatar != "https://cdn/y.png" {
		t.Errorf("refresh did not update display fields: %+v", u2)
	}

	got, err := st.GetUserByDiscordID(ctx, "1234567890")
	if err != nil || got.ID != u.ID {
		t.Fatalf("GetUserByDiscordID = %+v, %v", got, err)
	}
	if _, err := st.GetUserByDiscordID(ctx, "nope"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetUserByDiscordID(unknown) = %v, want ErrNotFound", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: "u1", Name: "Op"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	sess, err := st.CreateSession(ctx, storage.NewSession{
		UserID: u.ID, Token: "opaque-token-1", ExpiresAt: time.Now().Add(time.Hour),
		IP: "127.0.0.1", UA: "test-agent",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.ID == uuid.Nil || sess.UserID != u.ID {
		t.Fatalf("session = %+v", sess)
	}

	// Valid token resolves to the owning user.
	got, err := st.AuthenticateSession(ctx, "opaque-token-1")
	if err != nil {
		t.Fatalf("AuthenticateSession(valid): %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("authenticated user = %s, want %s", got.ID, u.ID)
	}

	// Unknown token → ErrNotFound (→ 401 at the RPC layer).
	if _, err := st.AuthenticateSession(ctx, "bogus"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("AuthenticateSession(unknown) = %v, want ErrNotFound", err)
	}

	// Expired token → ErrNotFound.
	if _, err := st.CreateSession(ctx, storage.NewSession{
		UserID: u.ID, Token: "expired-token", ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("CreateSession(expired): %v", err)
	}
	if _, err := st.AuthenticateSession(ctx, "expired-token"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("AuthenticateSession(expired) = %v, want ErrNotFound", err)
	}

	// Delete revokes immediately; a re-delete is a no-op (idempotent logout).
	if err := st.DeleteSession(ctx, "opaque-token-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := st.AuthenticateSession(ctx, "opaque-token-1"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("AuthenticateSession after delete = %v, want ErrNotFound", err)
	}
	if err := st.DeleteSession(ctx, "opaque-token-1"); err != nil {
		t.Errorf("DeleteSession(already gone) = %v, want nil", err)
	}
}

// TestResolveOperatorTenantClaimsSeeded asserts the first operator claims the
// pre-existing (seeded) unbound tenant, and the resolution is idempotent.
func TestResolveOperatorTenantClaimsSeeded(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()

	// A seeded, unbound tenant already exists (as `glyphoxa seed` writes).
	seededID, err := st.CreateTenant(ctx, "Seeded Tenant")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	u, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: "first-op"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// No tenant is bound yet.
	if _, err := st.TenantForUser(ctx, u.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("TenantForUser(unbound) = %v, want ErrNotFound", err)
	}

	bound, err := st.ResolveOperatorTenant(ctx, u.ID)
	if err != nil {
		t.Fatalf("ResolveOperatorTenant: %v", err)
	}
	if bound.ID != seededID {
		t.Errorf("claimed tenant = %s, want the seeded %s", bound.ID, seededID)
	}

	// Idempotent: a second resolve returns the same already-bound tenant, no new row.
	again, err := st.ResolveOperatorTenant(ctx, u.ID)
	if err != nil {
		t.Fatalf("ResolveOperatorTenant(again): %v", err)
	}
	if again.ID != seededID {
		t.Errorf("re-resolve = %s, want %s", again.ID, seededID)
	}

	tid, err := st.TenantForUser(ctx, u.ID)
	if err != nil || tid != seededID {
		t.Fatalf("TenantForUser = %s, %v; want %s", tid, err, seededID)
	}
}

// TestResolveOperatorTenantSeedsWhenEmpty asserts a fresh DB with no tenant at
// all gets one created bound to the operator.
func TestResolveOperatorTenantSeedsWhenEmpty(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: "lonely-op"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	bound, err := st.ResolveOperatorTenant(ctx, u.ID)
	if err != nil {
		t.Fatalf("ResolveOperatorTenant: %v", err)
	}
	if bound.ID == uuid.Nil || bound.Name != "Glyphoxa" {
		t.Errorf("seeded tenant = %+v, want a 'Glyphoxa' tenant", bound)
	}
}

// TestResolveOperatorTenantTakesOverDevTenant asserts the first REAL operator
// login claims a tenant previously bound to the synthetic GLYPHOXA_DEV_MODE
// operator (ADR-0041): everything configured in dev mode hands over instead of
// being stranded next to a fresh empty tenant. The dev operator does not steal
// a real operator's binding back.
func TestResolveOperatorTenantTakesOverDevTenant(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()

	seededID, err := st.CreateTenant(ctx, "Seeded Tenant")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	dev, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: storage.DevOperatorDiscordID})
	if err != nil {
		t.Fatalf("upsert dev operator: %v", err)
	}

	// A dev-mode boot claims the seeded tenant like a first login would.
	devTenant, err := st.ResolveOperatorTenant(ctx, dev.ID)
	if err != nil {
		t.Fatalf("ResolveOperatorTenant(dev): %v", err)
	}
	if devTenant.ID != seededID {
		t.Fatalf("dev operator claimed %s, want the seeded %s", devTenant.ID, seededID)
	}

	// The first real login takes the dev-held tenant over.
	real, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: "777000000000000000"})
	if err != nil {
		t.Fatalf("upsert real operator: %v", err)
	}
	taken, err := st.ResolveOperatorTenant(ctx, real.ID)
	if err != nil {
		t.Fatalf("ResolveOperatorTenant(real): %v", err)
	}
	if taken.ID != seededID {
		t.Errorf("real operator resolved %s, want the dev-held seeded tenant %s", taken.ID, seededID)
	}
	if tid, err := st.TenantForUser(ctx, real.ID); err != nil || tid != seededID {
		t.Errorf("TenantForUser(real) = %s, %v; want %s", tid, err, seededID)
	}

	// The dev operator lost the binding and does NOT steal it back — a later
	// dev-mode boot on the same DB gets a different (fresh) tenant instead,
	// and the real operator's binding survives.
	devAgain, err := st.ResolveOperatorTenant(ctx, dev.ID)
	if err != nil {
		t.Fatalf("ResolveOperatorTenant(dev, again): %v", err)
	}
	if devAgain.ID == seededID {
		t.Error("dev operator re-claimed the tenant a real operator took over")
	}
	if tid, err := st.TenantForUser(ctx, real.ID); err != nil || tid != seededID {
		t.Errorf("TenantForUser(real) after dev re-resolve = %s, %v; want %s", tid, err, seededID)
	}
}
