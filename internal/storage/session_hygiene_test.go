//go:build integration

package storage_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// migratedWithPool is migrated plus the raw pool, for tests that must reach
// behind the store (backdating a timestamp, reading a system column).
func migratedWithPool(t *testing.T) (*storage.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := startPostgres(t)
	if err := storage.MigrateUp(context.Background(), openSQL(t, dsn)); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool := openPool(t, dsn)
	return storage.New(pool), pool
}

func tokenDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// TestAuthenticateSession_DoesNotWriteOnEveryRequest is the R7 regression.
// AuthenticateSession used to UPDATE last_seen_at on every call, so every
// authenticated request — each read RPC, SSE reconnect and image fetch — wrote
// a new version of the session row. xmin (the id of the transaction that wrote
// the current row version) is the direct observable: it must NOT move across
// back-to-back authentications of a freshly seen session, and MUST move once
// last_seen_at has gone stale.
func TestAuthenticateSession_DoesNotWriteOnEveryRequest(t *testing.T) {
	st, pool := migratedWithPool(t)
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: "u-seen", Name: "Op"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := st.CreateSession(ctx, storage.NewSession{
		UserID: u.ID, Token: "seen-token", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	rowVersion := func() (xmin string, lastSeen time.Time) {
		t.Helper()
		if err := pool.QueryRow(ctx,
			`SELECT xmin::text, last_seen_at FROM sessions WHERE token_hash = $1`, tokenDigest("seen-token")).
			Scan(&xmin, &lastSeen); err != nil {
			t.Fatalf("read session row version: %v", err)
		}
		return xmin, lastSeen
	}

	created, _ := rowVersion()
	for i := 0; i < 3; i++ {
		got, err := st.AuthenticateSession(ctx, "seen-token")
		if err != nil || got.ID != u.ID {
			t.Fatalf("AuthenticateSession #%d = (%v, %v), want the owner", i, got.ID, err)
		}
	}
	if after, _ := rowVersion(); after != created {
		t.Errorf("session row was rewritten (xmin %s -> %s) by authenticating a freshly seen session — "+
			"last_seen_at must only be bumped once it is older than SessionSeenGranularity", created, after)
	}

	// Once last_seen_at is stale, the next authentication refreshes it — once.
	stale := storage.SessionSeenGranularity + time.Minute
	if _, err := pool.Exec(ctx,
		`UPDATE sessions SET last_seen_at = now() - make_interval(secs => $2) WHERE token_hash = $1`,
		tokenDigest("seen-token"), stale.Seconds()); err != nil {
		t.Fatalf("backdate last_seen_at: %v", err)
	}
	backdated, _ := rowVersion()
	if _, err := st.AuthenticateSession(ctx, "seen-token"); err != nil {
		t.Fatalf("AuthenticateSession(stale last_seen_at): %v", err)
	}
	bumped, lastSeen := rowVersion()
	if bumped == backdated {
		t.Error("a stale last_seen_at was not refreshed")
	}
	if age := time.Since(lastSeen); age > time.Minute || age < -time.Minute {
		t.Errorf("last_seen_at = %v (%v ago), want ~now", lastSeen, age)
	}
	if _, err := st.AuthenticateSession(ctx, "seen-token"); err != nil {
		t.Fatalf("AuthenticateSession(after bump): %v", err)
	}
	if again, _ := rowVersion(); again != bumped {
		t.Error("the row was rewritten again right after its last_seen_at was refreshed")
	}
}

// TestAuthenticateSession_SuspendedAndExpiredStillRefused guards the rewrite of
// the lookup query: the read-mostly form must keep every refusal the old
// UPDATE … RETURNING form had.
func TestAuthenticateSession_SuspendedAndExpiredStillRefused(t *testing.T) {
	st, pool := migratedWithPool(t)
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: "u-susp", Name: "Op"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for token, ttl := range map[string]time.Duration{"live": time.Hour, "expired": -time.Minute} {
		if _, err := st.CreateSession(ctx, storage.NewSession{UserID: u.ID, Token: token, ExpiresAt: time.Now().Add(ttl)}); err != nil {
			t.Fatalf("CreateSession(%s): %v", token, err)
		}
	}
	if _, err := st.AuthenticateSession(ctx, "expired"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expired session: err = %v, want ErrNotFound", err)
	}
	if _, err := st.AuthenticateSession(ctx, "live"); err != nil {
		t.Fatalf("live session: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET suspended_at = now() WHERE id = $1`, u.ID); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if _, err := st.AuthenticateSession(ctx, "live"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("suspended owner: err = %v, want ErrNotFound", err)
	}
}

// TestPurgeExpiredSessions is the #643 purge: a row expired past the grace
// period is deleted, while a live session and one that expired only moments
// ago (still inside the grace) survive. Before it, nothing ever removed an
// expired session and the table kept every login's IP and user agent forever.
func TestPurgeExpiredSessions(t *testing.T) {
	st, pool := migratedWithPool(t)
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: "u-purge", Name: "Op"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for token, expiresIn := range map[string]time.Duration{
		"live":              time.Hour,
		"just-expired":      -time.Minute,
		"long-expired":      -(storage.ExpiredSessionGrace + time.Hour),
		"very-long-expired": -40 * 24 * time.Hour,
	} {
		if _, err := st.CreateSession(ctx, storage.NewSession{
			UserID: u.ID, Token: token, ExpiresAt: time.Now().Add(expiresIn), IP: "203.0.113.7", UA: "agent",
		}); err != nil {
			t.Fatalf("CreateSession(%s): %v", token, err)
		}
	}

	purged, err := st.PurgeExpiredSessions(ctx)
	if err != nil {
		t.Fatalf("PurgeExpiredSessions: %v", err)
	}
	if purged != 2 {
		t.Errorf("purged = %d, want 2 (the two rows expired past the grace)", purged)
	}
	remaining := map[string]bool{}
	rows, err := pool.Query(ctx, `SELECT token_hash FROM sessions`)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			t.Fatalf("scan: %v", err)
		}
		remaining[h] = true
	}
	for token, want := range map[string]bool{"live": true, "just-expired": true, "long-expired": false, "very-long-expired": false} {
		if remaining[tokenDigest(token)] != want {
			t.Errorf("session %q present = %v, want %v", token, remaining[tokenDigest(token)], want)
		}
	}
	if _, err := st.AuthenticateSession(ctx, "live"); err != nil {
		t.Errorf("the live session no longer authenticates after the purge: %v", err)
	}

	// Idempotent: nothing left to purge.
	if again, err := st.PurgeExpiredSessions(ctx); err != nil || again != 0 {
		t.Errorf("second purge = (%d, %v), want (0, nil)", again, err)
	}
}

// TestMigration00056_SessionsTokenHash covers the #643 schema step against rows
// written by the previous schema: expired rows are deleted, the column is
// renamed with its UNIQUE constraint intact, and a session that was live
// before the migration still authenticates after it — the rename must not log
// anyone out. Down restores the old column name.
func TestMigration00056_SessionsTokenHash(t *testing.T) {
	dsn := startPostgres(t)
	db := openSQL(t, dsn)
	pool := openPool(t, dsn)
	ctx := context.Background()

	provider, err := storage.NewMigrationProvider(db)
	if err != nil {
		t.Fatalf("NewMigrationProvider: %v", err)
	}
	if _, err := provider.UpTo(ctx, 55); err != nil {
		t.Fatalf("migrate up to 00055: %v", err)
	}

	st := storage.New(pool)
	u, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: "u-mig", Name: "Op"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Rows as the pre-00056 schema held them: the digest in a column named
	// `token`. One live, one expired.
	insert := func(token string, expires time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`INSERT INTO sessions (user_id, token, expires_at) VALUES ($1, $2, $3)`,
			u.ID, tokenDigest(token), expires); err != nil {
			t.Fatalf("seed pre-migration session %q: %v", token, err)
		}
	}
	insert("live-before", time.Now().Add(time.Hour))
	insert("expired-before", time.Now().Add(-time.Hour))

	if _, err := provider.UpTo(ctx, 56); err != nil {
		t.Fatalf("migrate up to 00056: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if n != 1 {
		t.Errorf("sessions after 00056 = %d, want 1 (the expired row deleted, the live one kept)", n)
	}
	if got, err := st.AuthenticateSession(ctx, "live-before"); err != nil || got.ID != u.ID {
		t.Errorf("session live before the migration: AuthenticateSession = (%v, %v), want its owner — the rename must not sign anyone out", got.ID, err)
	}
	if _, err := st.AuthenticateSession(ctx, "expired-before"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expired session after the migration: err = %v, want ErrNotFound", err)
	}
	var oldColumn int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns WHERE table_name = 'sessions' AND column_name = 'token'`).
		Scan(&oldColumn); err != nil {
		t.Fatalf("inspect columns: %v", err)
	}
	if oldColumn != 0 {
		t.Error("sessions.token still exists after 00056")
	}
	// The UNIQUE constraint survived the rename: a duplicate digest is refused.
	if _, err := pool.Exec(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 hour')`,
		u.ID, tokenDigest("live-before")); err == nil {
		t.Error("a duplicate token_hash was accepted — the UNIQUE constraint was lost in the rename")
	}

	// Down restores the old name and keeps the surviving row.
	if _, err := provider.DownTo(ctx, 55); err != nil {
		t.Fatalf("migrate down to 00055: %v", err)
	}
	var kept uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT user_id FROM sessions WHERE token = $1`, tokenDigest("live-before")).Scan(&kept); err != nil || kept != u.ID {
		t.Errorf("after Down: lookup by the old column = (%v, %v), want the surviving session", kept, err)
	}
}
