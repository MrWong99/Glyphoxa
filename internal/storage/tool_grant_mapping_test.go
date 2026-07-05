package storage_test

import (
	"encoding/json"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// GrantsFromRows is the single canonical row→Grant mapper the live loop (wirenpc)
// and the grant RPC's hydration test both consume (issue #215): keeping it here
// kills the drift a second reimplementation invited. These pin its behaviour
// directly, no database.

// TestGrantsFromRows_NoRows: an Agent with no grant rows maps to no grants
// (least-privilege — the LLM is shown no Tool).
func TestGrantsFromRows_NoRows(t *testing.T) {
	if got := storage.GrantsFromRows(nil); len(got) != 0 {
		t.Fatalf("no rows produced %d grants, want 0", len(got))
	}
}

// TestGrantsFromRows_DiceNilConfig: a config-less row (dice's shape) maps to a
// Grant with a nil Config.
func TestGrantsFromRows_DiceNilConfig(t *testing.T) {
	got := storage.GrantsFromRows([]storage.ToolGrant{{ToolName: "dice"}})
	if len(got) != 1 {
		t.Fatalf("got %d grants, want 1", len(got))
	}
	if got[0].ToolName != "dice" || got[0].Config != nil {
		t.Errorf("dice grant = %+v, want {dice <nil>}", got[0])
	}
}

// TestGrantsFromRows_PreservesConfig: a row's jsonb config becomes Grant.Config
// as a json.RawMessage with the bytes preserved (so scope reaches the Tool
// handler, ADR-0029).
func TestGrantsFromRows_PreservesConfig(t *testing.T) {
	cfg := json.RawMessage(`{"scope":"self"}`)
	got := storage.GrantsFromRows([]storage.ToolGrant{
		{ToolName: "dice"},
		{ToolName: "remember_knowledge", Config: cfg},
	})
	if len(got) != 2 {
		t.Fatalf("got %d grants, want 2", len(got))
	}
	raw, ok := got[1].Config.(json.RawMessage)
	if !ok {
		t.Fatalf("scoped Config type = %T, want json.RawMessage", got[1].Config)
	}
	if string(raw) != string(cfg) {
		t.Errorf("scoped Config = %q, want %q", raw, cfg)
	}
}

// TestGrantsFromRows_HydratesDeclarations: the mapped grants feed a real GrantSet
// whose Declarations() is exactly the granted Tools — the live-loop hydration the
// grant RPC's AC4 test asserts through this same function.
func TestGrantsFromRows_HydratesDeclarations(t *testing.T) {
	grants := storage.GrantsFromRows([]storage.ToolGrant{{ToolName: "dice"}})
	decls := tool.NewGrantSet(tool.BuiltinRegistry(), grants...).Declarations()
	if len(decls) != 1 || decls[0].Name != "dice" {
		t.Fatalf("declared %+v, want exactly [dice]", decls)
	}
}
