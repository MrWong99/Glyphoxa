package tool

import "sort"

// Grant is an Agent's explicit permission to invoke one named Tool, with
// optional per-grant Config that may narrow the Tool's authority for that Agent
// (ADR-0029). It is modeled as a struct, not a bare name string, so the
// per-grant config door is open from day one — dice's Config is always nil, but
// a future remember_knowledge granted "only about yourself" to an NPC vs
// campaign-wide to the Butler carries different Config behind the same Tool.
//
// Grants are an in-memory value in v1.0 (no agents table yet); when persistence
// lands they hydrate from the DB into this identical shape and the loop never
// knows the difference.
type Grant struct {
	// ToolName is the [Tool.Name] this grant permits.
	ToolName string

	// Config narrows the Tool's authority for this Agent. It is passed to
	// [Tool.Execute] as grantConfig and enforced in the handler, never by the
	// LLM. nil means "no narrowing" (dice).
	Config any
}

// GrantSet is an Agent's full set of Tool Grants. It enforces least-privilege
// (ADR-0029): the LLM is only ever shown — and the loop only ever executes —
// Tools the Agent is granted. Ungranted Tools are filtered out before the
// prompt is built and are never declared to the model.
//
// A GrantSet resolves grants against a [Registry]: the grant says "may call
// dice with this config", the Registry holds the dice [Tool]. A grant naming an
// unregistered Tool is silently skipped — it grants access to nothing.
type GrantSet struct {
	registry *Registry
	grants   map[string]Grant
}

// NewGrantSet builds a GrantSet over registry from grants. A duplicate
// ToolName keeps the last grant for that name. registry must be non-nil.
func NewGrantSet(registry *Registry, grants ...Grant) *GrantSet {
	if registry == nil {
		panic("tool: NewGrantSet: nil registry")
	}
	m := make(map[string]Grant, len(grants))
	for _, g := range grants {
		m[g.ToolName] = g
	}
	return &GrantSet{registry: registry, grants: m}
}

// Without returns a derived GrantSet identical to this one but with the named
// Tool's grant removed, sharing the same [Registry]. The receiver is not
// mutated — it returns a copy — so a caller can narrow grants for one turn (e.g.
// drop an unneeded Tool so it is never declared to the model, saving a wasted
// tool-call round) without affecting any other turn. Removing a name that is not
// granted is a no-op copy.
func (gs *GrantSet) Without(toolName string) *GrantSet {
	m := make(map[string]Grant, len(gs.grants))
	for name, g := range gs.grants {
		if name == toolName {
			continue
		}
		m[name] = g
	}
	return &GrantSet{registry: gs.registry, grants: m}
}

// resolve returns the [Tool] and per-grant config for name, and whether the
// Agent is both granted name and the Tool is registered. A grant for an
// unregistered Tool, or no grant at all, returns ok=false — the loop treats
// both as "not callable" and never widens access.
func (gs *GrantSet) resolve(name string) (t Tool, config any, ok bool) {
	g, granted := gs.grants[name]
	if !granted {
		return nil, nil, false
	}
	t, found := gs.registry.Get(name)
	if !found {
		return nil, nil, false
	}
	return t, g.Config, true
}

// Declarations returns the [Decl] for every granted Tool that is registered,
// sorted by Name. This is the grant-stripped tool list handed to the LLM —
// ungranted Tools never appear, so the model cannot call what it cannot see
// (ADR-0029). The order is stable (sorted, not map-iteration order) so the
// rendered prompt — and thus the ADR-0021 cassette prompt_hash — does not
// thrash between runs when more than one Tool is granted.
func (gs *GrantSet) Declarations() []Decl {
	decls := make([]Decl, 0, len(gs.grants))
	for name := range gs.grants {
		t, found := gs.registry.Get(name)
		if !found {
			continue
		}
		decls = append(decls, Decl{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	sort.Slice(decls, func(i, j int) bool { return decls[i].Name < decls[j].Name })
	return decls
}
