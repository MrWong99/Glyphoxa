package storage

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// neverSent is an error of the kind pgconn reports when a request provably
// never reached the server.
type neverSent struct{}

func (neverSent) Error() string     { return "write failed before any byte was sent" }
func (neverSent) SafeToRetry() bool { return true }

// TestCommitErrorSeparatesProvableFailureFromUnknownOutcome pins which failed
// COMMITs a non-idempotent caller (the additive Usage Ledger upsert) may retry.
// The server answering, or the request never leaving, proves nothing was
// committed. A timeout or a dropped connection with the COMMIT on the wire does
// not: the outcome is unknown and a retry could double-write.
func TestCommitErrorSeparatesProvableFailureFromUnknownOutcome(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		err     error
		unknown bool
	}{
		"server rolled back an aborted tx": {pgx.ErrTxCommitRollback, false},
		"server rejected the commit":       {&pgconn.PgError{Code: "40001"}, false},
		"request never sent":               {neverSent{}, false},
		"deadline with commit in flight":   {context.DeadlineExceeded, true},
		"connection dropped mid-commit":    {io.ErrUnexpectedEOF, true},
	} {
		got := commitError(tc.err)
		if !errors.Is(got, tc.err) {
			t.Errorf("%s: cause lost from %v", name, got)
		}
		if unknown := errors.Is(got, ErrCommitOutcomeUnknown); unknown != tc.unknown {
			t.Errorf("%s: outcome-unknown = %v, want %v", name, unknown, tc.unknown)
		}
	}
}
