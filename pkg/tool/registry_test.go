package tool

import (
	"context"
	"encoding/json"
	"testing"
)

// stubTool is a minimal [Tool] for registry/grant tests, parameterized on the
// fields those tests vary (name, read-only bit) without needing a real handler.
type stubTool struct {
	name          string
	readOnly      bool
	supportsScope bool
	exec          func(ctx context.Context, args json.RawMessage, grant any) (string, error)
}

func (s stubTool) Name() string                 { return s.name }
func (s stubTool) Description() string          { return s.name + " stub" }
func (s stubTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s stubTool) ReadOnly() bool               { return s.readOnly }
func (s stubTool) SupportsScope() bool          { return s.supportsScope }
func (s stubTool) Execute(ctx context.Context, args json.RawMessage, grant any) (string, error) {
	if s.exec != nil {
		return s.exec(ctx, args, grant)
	}
	return "ok", nil
}

func TestRegistryRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	d := NewDice()
	if err := r.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Get("dice")
	if !ok || got != Tool(d) {
		t.Fatalf("Get(dice) = %v, %v; want the registered dice", got, ok)
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("Get(nope) found a tool that was never registered")
	}
}

// TestRegistryToolsSorted: Tools() returns the full catalog (not grant-stripped)
// in stable Name order, so the #117 grant editor lists every registerable Tool
// deterministically.
func TestRegistryToolsSorted(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"charlie", "alpha", "bravo"} {
		r.MustRegister(stubTool{name: n, readOnly: true})
	}
	got := r.Tools()
	if len(got) != 3 {
		t.Fatalf("Tools() len = %d, want 3", len(got))
	}
	want := []string{"alpha", "bravo", "charlie"}
	for i, w := range want {
		if got[i].Name() != w {
			t.Fatalf("Tools() order = %v, want %v", []string{got[0].Name(), got[1].Name(), got[2].Name()}, want)
		}
	}
}

// TestBuiltinRegistryIsDiceOnly: the shared built-in set is exactly [dice] and it
// is a plain on/off grant (no scope editor). This is the source the grant editor
// and the live loop both build from — they must agree on the available Tools.
func TestBuiltinRegistryIsDiceOnly(t *testing.T) {
	tools := BuiltinRegistry().Tools()
	if len(tools) != 1 || tools[0].Name() != "dice" {
		t.Fatalf("BuiltinRegistry Tools = %+v, want exactly [dice]", tools)
	}
	if tools[0].SupportsScope() {
		t.Error("dice must not advertise scope support (it carries no per-grant config)")
	}
	if _, ok := BuiltinRegistry().Get("dice"); !ok {
		t.Error("BuiltinRegistry must have dice registered")
	}
}

func TestRegistryRejectsBadRegistration(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Error("Register(nil) should error")
	}
	if err := r.Register(stubTool{name: ""}); err == nil {
		t.Error("Register(empty name) should error")
	}
	if err := r.Register(stubTool{name: "x"}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(stubTool{name: "x"}); err == nil {
		t.Error("duplicate Register should error")
	}
}

func TestGrantSetGrantStripping(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(stubTool{name: "dice", readOnly: true})
	r.MustRegister(stubTool{name: "secret", readOnly: true})

	// Agent is granted only dice; secret must not be declared (ADR-0029).
	gs := NewGrantSet(r, Grant{ToolName: "dice"})
	decls := gs.Declarations()
	if len(decls) != 1 || decls[0].Name != "dice" {
		t.Fatalf("Declarations = %+v; want only dice", decls)
	}

	// Resolving an ungranted tool must fail closed.
	if _, _, ok := gs.resolve("secret"); ok {
		t.Error("resolve(secret) succeeded for an ungranted tool")
	}
	if _, _, ok := gs.resolve("dice"); !ok {
		t.Error("resolve(dice) failed for a granted, registered tool")
	}
}

func TestGrantSetWithoutIsPureAndDrops(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(stubTool{name: "dice", readOnly: true})
	r.MustRegister(stubTool{name: "writer", readOnly: true})

	full := NewGrantSet(r, Grant{ToolName: "dice"}, Grant{ToolName: "writer"})
	gated := full.Without("dice")

	// The derived set drops only the named grant.
	if _, _, ok := gated.resolve("dice"); ok {
		t.Error("Without(dice) must drop the dice grant")
	}
	if _, _, ok := gated.resolve("writer"); !ok {
		t.Error("Without(dice) must keep other grants")
	}
	if d := gated.Declarations(); len(d) != 1 || d[0].Name != "writer" {
		t.Errorf("gated Declarations = %+v, want only writer", d)
	}

	// The original is untouched (purity): Without returns a copy.
	if _, _, ok := full.resolve("dice"); !ok {
		t.Error("Without must not mutate the receiver; dice must still resolve on the original")
	}
	if d := full.Declarations(); len(d) != 2 {
		t.Errorf("original Declarations = %+v, want both grants intact", d)
	}

	// Removing a name that is not granted is a no-op copy.
	same := full.Without("ghost")
	if d := same.Declarations(); len(d) != 2 {
		t.Errorf("Without(ungranted) = %+v, want an unchanged copy of both grants", d)
	}
}

func TestGrantSetSkipsUnregisteredGrant(t *testing.T) {
	r := NewRegistry()
	// Grant names a tool that was never registered: grants access to nothing.
	gs := NewGrantSet(r, Grant{ToolName: "ghost"})
	if d := gs.Declarations(); len(d) != 0 {
		t.Errorf("Declarations = %+v; want none for an unregistered grant", d)
	}
	if _, _, ok := gs.resolve("ghost"); ok {
		t.Error("resolve(ghost) succeeded for an unregistered tool")
	}
}

func TestGrantSetDeclarationsSorted(t *testing.T) {
	// Declarations must come out sorted by Name so the rendered prompt — and
	// the ADR-0021 cassette prompt_hash — is stable across runs regardless of
	// grant/map order.
	r := NewRegistry()
	for _, n := range []string{"charlie", "alpha", "bravo"} {
		r.MustRegister(stubTool{name: n, readOnly: true})
	}
	gs := NewGrantSet(r,
		Grant{ToolName: "charlie"}, Grant{ToolName: "alpha"}, Grant{ToolName: "bravo"})
	decls := gs.Declarations()
	got := []string{decls[0].Name, decls[1].Name, decls[2].Name}
	want := []string{"alpha", "bravo", "charlie"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Declarations order = %v, want %v", got, want)
		}
	}
}

func TestGrantSetPassesConfig(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(stubTool{name: "scoped", readOnly: true})
	cfg := map[string]string{"scope": "self"}
	gs := NewGrantSet(r, Grant{ToolName: "scoped", Config: cfg})
	_, got, ok := gs.resolve("scoped")
	if !ok {
		t.Fatal("resolve(scoped) failed")
	}
	if m, _ := got.(map[string]string); m["scope"] != "self" {
		t.Errorf("grant config not threaded through: %v", got)
	}
}
