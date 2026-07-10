package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestBuiltinRegistryRegistersKnowledgeTools pins that the built-in catalog is
// dice + the two read-only knowledge Tools (#296), regardless of whether their
// sources are wired — the grant editor's Tool list must be identical in every
// mode, so a GM toggling grants on the Campaign screen sees the same catalog the
// live session runs.
func TestBuiltinRegistryRegistersKnowledgeTools(t *testing.T) {
	reg := BuiltinRegistry(Deps{})
	want := []string{"dice", "kg_query", "transcript_search"} // Tools() is Name-sorted
	got := reg.Tools()
	if len(got) != len(want) {
		t.Fatalf("registered %d tools, want %d: %+v", len(got), len(want), got)
	}
	for i, name := range want {
		if got[i].Name() != name {
			t.Errorf("tool[%d] = %q, want %q", i, got[i].Name(), name)
		}
	}
}

// TestKnowledgeToolsAreReadOnly pins ADR-0030: both knowledge Tools are read-only
// so the loop runs them inline (never the refused side-effecting path).
func TestKnowledgeToolsAreReadOnly(t *testing.T) {
	reg := BuiltinRegistry(Deps{})
	for _, name := range []string{"transcript_search", "kg_query"} {
		tl, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if !tl.ReadOnly() {
			t.Errorf("%s must be read-only (ADR-0030 inline execution)", name)
		}
	}
}

// TestKQQuerySupportsScope pins that kg_query exposes a scope editor (its NPC
// grant narrows to own_node vs the Butler's campaign) while transcript_search
// does not (campaign-scoped for everyone, no per-grant narrowing).
func TestKnowledgeToolsScopeDeclarations(t *testing.T) {
	reg := BuiltinRegistry(Deps{})
	kg, _ := reg.Get("kg_query")
	if !kg.SupportsScope() {
		t.Error("kg_query must support a scope (ADR-0029 own_node vs campaign)")
	}
	ts, _ := reg.Get("transcript_search")
	if ts.SupportsScope() {
		t.Error("transcript_search carries no per-grant scope")
	}
}

// TestNilSourceToolsReportUnavailable pins that with a zero Deps the Tools are
// still registered but their Execute returns an error result (fed back to the
// model, not a panic) — the standalone bench and grant-editor RPC path.
func TestNilSourceToolsReportUnavailable(t *testing.T) {
	reg := BuiltinRegistry(Deps{})
	args := json.RawMessage(`{"query":"anything"}`)
	for _, name := range []string{"transcript_search", "kg_query"} {
		tl, _ := reg.Get(name)
		out, err := tl.Execute(context.Background(), args, nil)
		if err == nil {
			t.Errorf("%s with nil source should error, got %q", name, out)
			continue
		}
		if !strings.Contains(err.Error(), "unavailable") {
			t.Errorf("%s nil-source error = %q, want it to mention unavailable", name, err)
		}
	}
}
