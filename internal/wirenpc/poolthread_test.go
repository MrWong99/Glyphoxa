package wirenpc

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRunFromDB_DerivesSchemaCheckDSNFromPool is the Docker-free unit guard for
// the #77 pool-threading seam: RunFromDB no longer takes a dsn string and opens
// its own pool — it takes the caller's already-open *pgxpool.Pool and recovers
// the goose schema-check dsn from pool.Config().ConnString(). pgxpool.New is lazy
// (it parses the dsn without dialing), and ConnString() round-trips the dsn
// verbatim, so this test pins the contract the production callers rely on:
// whatever dsn the single shared pool was built from is exactly what backs the
// schema check, with no second connection string threaded through.
func TestRunFromDB_DerivesSchemaCheckDSNFromPool(t *testing.T) {
	const dsn = "postgres://u:p@127.0.0.1:5999/glyphoxa?sslmode=disable"

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	if got := pool.Config().ConnString(); got != dsn {
		t.Fatalf("pool.Config().ConnString() = %q, want %q — RunFromDB feeds this to "+
			"ensureSchemaCurrent, so a drift here would point the schema check at the wrong DB", got, dsn)
	}
}
