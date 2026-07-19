//go:build integration

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestPresenceOwnerElection covers sequence (1): the first instance to acquire the
// empty singleton row wins; a second, distinct instance loses while the first's
// heartbeat is live; and the first renewing advances its own heartbeat.
func TestPresenceOwnerElection(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	const expiry = 15 * time.Second

	won, err := st.AcquireOrRenewPresenceOwner(ctx, "instance-a", expiry)
	if err != nil {
		t.Fatalf("acquire (empty): %v", err)
	}
	if !won {
		t.Fatal("first instance on an empty row should win")
	}

	// A distinct instance while A's heartbeat is fresh must lose.
	won, err = st.AcquireOrRenewPresenceOwner(ctx, "instance-b", expiry)
	if err != nil {
		t.Fatalf("acquire (contended): %v", err)
	}
	if won {
		t.Fatal("second instance should lose while the incumbent's heartbeat is live")
	}

	// A renews: still the owner, heartbeat advances.
	var before time.Time
	if err := pool.QueryRow(ctx, `SELECT heartbeat_at FROM presence_owner`).Scan(&before); err != nil {
		t.Fatalf("read heartbeat: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	won, err = st.AcquireOrRenewPresenceOwner(ctx, "instance-a", expiry)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !won {
		t.Fatal("incumbent renewing should still own the row")
	}
	var after time.Time
	if err := pool.QueryRow(ctx, `SELECT heartbeat_at FROM presence_owner`).Scan(&after); err != nil {
		t.Fatalf("read heartbeat after renew: %v", err)
	}
	if !after.After(before) {
		t.Fatalf("renew should advance heartbeat: before=%s after=%s", before, after)
	}
}

// TestPresenceOwnerFailover covers sequence (2): once the incumbent's heartbeat
// expires a challenger wins (failover on owner death), and an explicit Release
// hands over immediately without waiting for expiry.
func TestPresenceOwnerFailover(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// A owns with a tiny expiry, then goes silent past it: the row is now stale.
	if won, err := st.AcquireOrRenewPresenceOwner(ctx, "instance-a", 20*time.Millisecond); err != nil || !won {
		t.Fatalf("A acquire: won=%v err=%v", won, err)
	}
	time.Sleep(40 * time.Millisecond)

	won, err := st.AcquireOrRenewPresenceOwner(ctx, "instance-b", 20*time.Millisecond)
	if err != nil {
		t.Fatalf("B acquire after expiry: %v", err)
	}
	if !won {
		t.Fatal("challenger should win once the incumbent heartbeat has expired")
	}

	// B releases: A's next acquire wins immediately, no expiry wait.
	if err := st.ReleasePresenceOwner(ctx, "instance-b"); err != nil {
		t.Fatalf("release: %v", err)
	}
	won, err = st.AcquireOrRenewPresenceOwner(ctx, "instance-a", 15*time.Second)
	if err != nil {
		t.Fatalf("A re-acquire after release: %v", err)
	}
	if !won {
		t.Fatal("after a clean Release the next acquirer should win immediately")
	}

	// A superseded former owner's Release must NOT delete the current owner's row.
	if err := st.ReleasePresenceOwner(ctx, "instance-b"); err != nil {
		t.Fatalf("stale release: %v", err)
	}
	var owner string
	if err := pool.QueryRow(ctx, `SELECT instance_id FROM presence_owner`).Scan(&owner); err != nil {
		t.Fatalf("owner still present: %v", err)
	}
	if owner != "instance-a" {
		t.Fatalf("owner = %q, want instance-a (stale release must not evict the live owner)", owner)
	}
}
