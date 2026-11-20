package storage

import "testing"

// TestSchemaAhead pins EnsureCurrent's "database newer than this binary" check
// (D10): only a database whose highest applied version exceeds every embedded
// migration counts, so a database at head, or one still catching up, never warns.
func TestSchemaAhead(t *testing.T) {
	t.Parallel()
	if schemaAhead(58, 58) {
		t.Error("db at head: ahead = true, want false")
	}
	if schemaAhead(55, 58) {
		t.Error("db behind the binary: ahead = true, want false (that is the unapplied-migration error, not this warning)")
	}
	if !schemaAhead(59, 58) {
		t.Error("db migrated by a later release: ahead = false, want true")
	}
}
