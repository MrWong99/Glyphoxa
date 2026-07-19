package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Presence-owner election persistence (#492, ADR-0057 (c)): the singleton
// presence_owner row elects exactly one Voice Instance to register command
// listeners and dispatch interactions for a shared central token; non-owners drop
// the duplicate interaction events they still receive (every gateway session on
// one token gets the FULL stream, P5). Election is ONE upsert that wins when the
// caller already owns the row OR the incumbent's heartbeat has expired — the same
// expiry-then-claim idiom the job runner (ADR-0049) and the voice claim plane
// (#491) prove. Poll only (ADR-0057 (b)); no LISTEN/NOTIFY.

// AcquireOrRenewPresenceOwner atomically claims or renews the singleton
// presence-owner row for instanceID and reports whether instanceID now owns it.
// The single upsert wins — inserts on an empty table, or updates the existing
// row — only when the row is already instanceID's (a renew, advancing the
// heartbeat) OR the incumbent's heartbeat is older than expiry (a dead owner's
// row, so a challenger takes over). When another live instance holds it the
// ON CONFLICT WHERE predicate is false, no row is written, and this returns
// false — the caller is a non-owner and must stay inactive.
func (s *Store) AcquireOrRenewPresenceOwner(ctx context.Context, instanceID string, expiry time.Duration) (bool, error) {
	var owner string
	err := s.db.QueryRow(ctx,
		`INSERT INTO presence_owner AS po (singleton, instance_id, heartbeat_at)
		 VALUES (true, $1, now())
		 ON CONFLICT (singleton) DO UPDATE
		    SET instance_id = excluded.instance_id,
		        heartbeat_at = now()
		  WHERE po.instance_id = $1
		     OR po.heartbeat_at < now() - make_interval(secs => $2)
		 RETURNING po.instance_id`,
		instanceID, expiry.Seconds()).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		// The conflict predicate was false: a live incumbent still owns the row, no
		// write happened, and this caller is a non-owner.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("storage: acquire or renew presence owner for %s: %w", instanceID, err)
	}
	return true, nil
}

// ReleasePresenceOwner drops instanceID's presence-owner claim so a challenger's
// very next AcquireOrRenewPresenceOwner wins immediately (a clean drain handover,
// not an expiry wait). Fenced by instance_id: a superseded former owner that
// already lost the row deletes nothing, never the new owner's claim. Deleting no
// row is not an error.
func (s *Store) ReleasePresenceOwner(ctx context.Context, instanceID string) error {
	if _, err := s.db.Exec(ctx,
		`DELETE FROM presence_owner WHERE instance_id = $1`, instanceID); err != nil {
		return fmt.Errorf("storage: release presence owner for %s: %w", instanceID, err)
	}
	return nil
}
