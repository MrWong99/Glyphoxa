package mcphost

import (
	"fmt"

	"github.com/MrWong99/glyphoxa/internal/mcp/tools"
	"github.com/MrWong99/glyphoxa/internal/mcp/tools/diceroller"
	"github.com/MrWong99/glyphoxa/internal/mcp/tools/ruleslookup"
)

// RegisterStatelessTools registers all built-in stateless tools (dice roller,
// rules lookup) on the given Host. These tools have no external dependencies
// and are safe to share across tenants.
//
// Tenant-scoped tools (memory, fileio) are NOT included here; they require
// per-session state and must be registered by the worker.
func RegisterStatelessTools(h *Host) error {
	allTools := make([]tools.Tool, 0, 8)
	allTools = append(allTools, diceroller.Tools()...)
	allTools = append(allTools, ruleslookup.Tools()...)

	for _, t := range allTools {
		if err := h.RegisterBuiltin(BuiltinTool{
			Definition:  t.Definition,
			Handler:     t.Handler,
			DeclaredP50: t.DeclaredP50,
			DeclaredMax: t.DeclaredMax,
		}); err != nil {
			return fmt.Errorf("register stateless tool %q: %w", t.Definition.Name, err)
		}
	}
	return nil
}
