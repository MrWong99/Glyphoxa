package mcphost

import (
	"testing"

	"github.com/MrWong99/glyphoxa/internal/mcp"
)

func TestRegisterStatelessTools(t *testing.T) {
	t.Parallel()

	h := New()
	defer h.Close()

	if err := RegisterStatelessTools(h); err != nil {
		t.Fatalf("RegisterStatelessTools: %v", err)
	}

	// Should have dice (roll, roll_table) and rules (search_rules, get_rule) tools.
	tools := h.AvailableTools(mcp.BudgetDeep)
	if len(tools) < 4 {
		t.Errorf("expected at least 4 tools, got %d", len(tools))
	}

	// Verify specific tools exist.
	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"roll", "roll_table", "search_rules", "get_rule"} {
		if !names[want] {
			t.Errorf("missing expected tool %q", want)
		}
	}
}
