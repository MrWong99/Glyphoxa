package storage

import "github.com/MrWong99/Glyphoxa/pkg/tool"

// GrantsFromRows maps an Agent's persisted Tool Grant rows to the in-memory
// [tool.Grant]s the live loop hydrates into a [tool.GrantSet] (#113, ADR-0029) —
// the identical shape the orchestrator consumes, so the loop never knows the
// grants came from the DB. A row's jsonb config becomes Grant.Config as a
// [json.RawMessage] (nil when the column is NULL — dice's shape); the Tool
// handler receives it as grantConfig at execution time and enforces scope there,
// never the LLM. No rows yields no grants: the Agent is shown no Tool at all.
//
// This is the SINGLE canonical row→Grant mapping. wirenpc's live loop and the
// grant RPC's AC4 hydration test both call it, so neither can drift from the
// other (issue #215). It is the one place in this package that depends on
// pkg/tool — the vendor-neutral CRUD in tool_grant.go keeps the storage rows a
// raw blob; only this shared mapper crosses into the Tool domain, and the
// dependency is one-way (pkg/tool never imports internal/*).
func GrantsFromRows(rows []ToolGrant) []tool.Grant {
	if len(rows) == 0 {
		return nil
	}
	grants := make([]tool.Grant, 0, len(rows))
	for _, r := range rows {
		g := tool.Grant{ToolName: r.ToolName}
		if len(r.Config) > 0 {
			g.Config = r.Config // json.RawMessage; the handler decodes its scope
		}
		grants = append(grants, g)
	}
	return grants
}
