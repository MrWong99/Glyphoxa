package storage

import "testing"

// TestSchemaVersionVerdict pins EnsureCurrent's three outcomes: behind is a
// startup error, equal is clean, and a DB AHEAD of the binary is served but
// flagged so the caller warns (D10: it used to be indistinguishable from equal).
func TestSchemaVersionVerdict(t *testing.T) {
	t.Parallel()
	if _, err := schemaVersionVerdict(54, 57); err == nil {
		t.Error("db behind binary: err = nil, want the run-migrate-up error")
	}
	if ahead, err := schemaVersionVerdict(57, 57); err != nil || ahead {
		t.Errorf("db at head: ahead=%v err=%v, want false/nil", ahead, err)
	}
	if ahead, err := schemaVersionVerdict(58, 57); err != nil || !ahead {
		t.Errorf("db ahead of binary: ahead=%v err=%v, want true/nil (serve, but warn)", ahead, err)
	}
}
