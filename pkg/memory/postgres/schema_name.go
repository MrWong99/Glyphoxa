package postgres

import (
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
)

var validSchema = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// SchemaName is a validated PostgreSQL schema identifier.
// Construction via NewSchemaName is the only way to obtain one,
// ensuring that unvalidated strings never reach SQL.
type SchemaName struct{ name string }

// NewSchemaName validates raw against PostgreSQL identifier rules and returns
// a SchemaName. Returns an error if raw does not match ^[a-z][a-z0-9_]{0,62}$.
func NewSchemaName(raw string) (SchemaName, error) {
	if !validSchema.MatchString(raw) {
		return SchemaName{}, fmt.Errorf("postgres: invalid schema name: %q", raw)
	}
	return SchemaName{name: raw}, nil
}

// TableRef returns a fully-qualified, properly quoted table reference
// (e.g., "public"."session_entries").
func (s SchemaName) TableRef(table string) string {
	return pgx.Identifier{s.name, table}.Sanitize()
}

// String returns the raw schema name.
func (s SchemaName) String() string { return s.name }
