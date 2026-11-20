package bundle_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// minimalBundle is a valid two-node bundle the bad-content cases mutate.
func minimalBundle() *bundle.Bundle {
	return &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign: bundle.Campaign{
			Name: "Bad Content", System: "dnd5e", Language: "en",
			Agents: []bundle.Agent{{ID: "a1", Role: "character", Name: "Bart"}},
			Nodes: []bundle.Node{
				{ID: "n1", Type: "npc", Name: "Bart"},
				{ID: "n2", Type: "location", Name: "Saltmarsh"},
			},
		},
	}
}

// TestImport_BadContentIsInvalidBundle: every failure the bundle's own content
// causes must carry ErrInvalidBundle, which the handler answers 400 — not fall
// through to the 500 "import failed" path with an Error log.
func TestImport_BadContentIsInvalidBundle(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*bundle.Bundle){
		"invalid planning role": func(b *bundle.Bundle) {
			b.Campaign.PlanningThreads = []bundle.PlanningThread{{
				Title: "prep", Messages: []bundle.PlanningMessage{{Role: "system", Content: "x"}},
			}}
		},
		"malformed character voice": func(b *bundle.Bundle) {
			b.Campaign.Agents[0].Voice = json.RawMessage(`"not an object"`)
		},
		"malformed butler voice": func(b *bundle.Bundle) {
			b.Campaign.Agents = append(b.Campaign.Agents,
				bundle.Agent{ID: "b", Role: "butler", Name: "Dom", Voice: json.RawMessage(`[1]`)})
		},
		"self edge": func(b *bundle.Bundle) {
			b.Campaign.Edges = []bundle.Edge{{From: "n1", To: "n1", Type: "knows"}}
		},
		"duplicate edge": func(b *bundle.Bundle) {
			e := bundle.Edge{From: "n1", To: "n2", Type: "resides_in"}
			b.Campaign.Edges = []bundle.Edge{e, e}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := minimalBundle()
			mutate(b)
			_, err := bundle.Import(context.Background(), newFakeStore(), uuid.New(), b)
			if !errors.Is(err, bundle.ErrInvalidBundle) {
				t.Fatalf("err = %v, want ErrInvalidBundle", err)
			}
		})
	}
}

// pgErrStore makes CreateNode fail the way Postgres refuses a value the bundle
// carried — here an unknown node type against the kg_node_type enum.
type pgErrStore struct {
	*fakeStore
	pgErr *pgconn.PgError
}

type pgErrTx struct {
	bundle.ImportStore
	pgErr *pgconn.PgError
}

func (s pgErrStore) InTx(ctx context.Context, fn func(tx bundle.ImportStore) error) error {
	return s.fakeStore.InTx(ctx, func(tx bundle.ImportStore) error {
		return fn(pgErrTx{ImportStore: tx, pgErr: s.pgErr})
	})
}

func (t pgErrTx) CreateNode(context.Context, storage.NewKGNode) (storage.KGNode, error) {
	return storage.KGNode{}, fmt.Errorf("storage: create kg node: %w", t.pgErr)
}

func TestImport_DatabaseRefusedValueIsInvalidBundle(t *testing.T) {
	t.Parallel()
	st := pgErrStore{fakeStore: newFakeStore(), pgErr: &pgconn.PgError{
		Code: "22P02", Message: `invalid input value for enum kg_node_type: "dragon"`,
	}}
	_, err := bundle.Import(context.Background(), st, uuid.New(), minimalBundle())
	if !errors.Is(err, bundle.ErrInvalidBundle) {
		t.Fatalf("err = %v, want ErrInvalidBundle", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, `"Bart"`) {
		t.Errorf("message %q should name the bundle entry that failed", msg)
	}
	// The message is surfaced verbatim to the client, so the database's own text
	// (schema and type names) must not ride along.
	if strings.Contains(msg, "kg_node_type") {
		t.Errorf("message %q leaks the database error text", msg)
	}
}

func TestImport_DatabaseOutageIsNotInvalidBundle(t *testing.T) {
	t.Parallel()
	// A connection failure is the server's problem, not the bundle's: it must
	// stay a 500, not be blamed on the upload.
	st := pgErrStore{fakeStore: newFakeStore(), pgErr: &pgconn.PgError{
		Code: "08006", Message: "connection failure",
	}}
	_, err := bundle.Import(context.Background(), st, uuid.New(), minimalBundle())
	if err == nil || errors.Is(err, bundle.ErrInvalidBundle) {
		t.Fatalf("err = %v, want a non-ErrInvalidBundle failure", err)
	}
}
