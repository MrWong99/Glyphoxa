package wirenpc

import (
	"encoding/json"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// These pin the DB-rows → in-memory GrantSet hydration (#113) without a database:
// grantsFromRows is the pure mapping the live loop uses, so its output can be fed
// straight into a real GrantSet and its Declarations() (the grant-stripped list
// handed to the LLM) asserted.

// TestGrantsFromRows_DiceOnly: a single dice grant row hydrates into a GrantSet
// that declares exactly the dice Tool to the LLM.
func TestGrantsFromRows_DiceOnly(t *testing.T) {
	grants := grantsFromRows([]storage.ToolGrant{{ToolName: "dice"}})

	reg := tool.NewRegistry()
	reg.MustRegister(tool.NewDice())
	decls := tool.NewGrantSet(reg, grants...).Declarations()

	if len(decls) != 1 || decls[0].Name != "dice" {
		t.Fatalf("declared %+v, want exactly [dice]", decls)
	}
}

// TestGrantsFromRows_NoRowsDeclaresNothing is the AC2/AC3 core: an Agent with no
// grant rows is granted nothing, so the LLM is never shown a Tool. Removing the
// last grant row IS this case — the Tool is never declared to the model.
func TestGrantsFromRows_NoRowsDeclaresNothing(t *testing.T) {
	grants := grantsFromRows(nil)
	if len(grants) != 0 {
		t.Fatalf("no rows produced %d grants, want 0", len(grants))
	}

	reg := tool.NewRegistry()
	reg.MustRegister(tool.NewDice())
	if decls := tool.NewGrantSet(reg, grants...).Declarations(); len(decls) != 0 {
		t.Fatalf("agent with no grants declared %+v, want none (LLM shown no Tool)", decls)
	}
}

// TestGrantsFromRows_PreservesConfig: a grant's jsonb config becomes the
// in-memory Grant.Config (so scope can reach the Tool handler, ADR-0029), and a
// nil-config row maps to a nil Config (dice's shape).
func TestGrantsFromRows_PreservesConfig(t *testing.T) {
	cfg := json.RawMessage(`{"scope":"self"}`)
	grants := grantsFromRows([]storage.ToolGrant{
		{ToolName: "dice"},
		{ToolName: "remember_knowledge", Config: cfg},
	})
	if len(grants) != 2 {
		t.Fatalf("got %d grants, want 2", len(grants))
	}
	if grants[0].ToolName != "dice" || grants[0].Config != nil {
		t.Errorf("dice grant = %+v, want {dice <nil>}", grants[0])
	}
	got, ok := grants[1].Config.(json.RawMessage)
	if !ok {
		t.Fatalf("scoped Config type = %T, want json.RawMessage", grants[1].Config)
	}
	if string(got) != string(cfg) {
		t.Errorf("scoped Config = %q, want %q", got, cfg)
	}
}
