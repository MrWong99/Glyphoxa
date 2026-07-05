package tool

import (
	"fmt"
	"sort"
)

// Registry maps a Tool name to its [Tool]. It is deliberately dumb (ADR-0028):
// a map with registration and lookup, no lifecycle ceremony. The API makes no
// in-process assumption — a future MCP Server registers by enumerating its
// Tools into this same Registry via [Registry.Register], exactly as a built-in
// does.
//
// A Registry is not safe for concurrent registration; build it once at startup
// (sequential [Registry.Register] calls) and treat it as read-only thereafter.
// Lookup ([Registry.Get], [Registry.Declarations]) is then safe to share.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds t under its [Tool.Name]. It returns an error on a nil Tool, an
// empty name, or a duplicate name — registration failures are startup bugs the
// caller should surface, not silently overwrite.
func (r *Registry) Register(t Tool) error {
	if t == nil {
		return fmt.Errorf("tool: Register: nil Tool")
	}
	name := t.Name()
	if name == "" {
		return fmt.Errorf("tool: Register: empty Tool name")
	}
	if _, dup := r.tools[name]; dup {
		return fmt.Errorf("tool: Register: duplicate Tool name %q", name)
	}
	r.tools[name] = t
	return nil
}

// MustRegister is [Registry.Register] that panics on error. Convenient for
// wiring a known-good built-in set at startup.
func (r *Registry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

// Get returns the Tool registered under name and whether it was found.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Tools returns every registered Tool sorted by [Tool.Name]. This is the full
// available-Tools catalog — NOT grant-stripped, unlike [GrantSet.Declarations] —
// so the grant editor can list every Tool an Agent could be granted with its
// current grant state (#117). The stable sort keeps the rendered list order
// deterministic across process restarts.
func (r *Registry) Tools() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}
