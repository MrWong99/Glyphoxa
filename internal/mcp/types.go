package mcp

// Transport selects the connection mechanism for an MCP server.
type Transport string

const (
	// TransportStdio spawns a subprocess and communicates over stdin/stdout.
	TransportStdio Transport = "stdio"

	// TransportStreamableHTTP communicates via the MCP Streamable HTTP protocol.
	TransportStreamableHTTP Transport = "streamable-http"
)

// IsValid reports whether t is a recognised transport.
func (t Transport) IsValid() bool {
	return t == TransportStdio || t == TransportStreamableHTTP
}

// BudgetTier controls which MCP tools are visible to the LLM based on latency constraints.
//
// The zero value is [BudgetUnset], meaning "no tier specified". Callers should
// treat BudgetUnset as equivalent to [BudgetFast] unless they need to
// distinguish "not set" from "explicitly fast" (e.g., for defaulting logic).
type BudgetTier int

const (
	// BudgetUnset is the zero value, meaning no tier has been specified.
	BudgetUnset BudgetTier = iota

	// BudgetFast allows only tools with ≤ 500ms estimated latency.
	BudgetFast

	// BudgetStandard allows tools with ≤ 1500ms estimated latency.
	BudgetStandard

	// BudgetDeep allows all tools regardless of latency.
	BudgetDeep
)

// Resolve returns t if it is an explicit tier, or [BudgetFast] if t is [BudgetUnset].
func (t BudgetTier) Resolve() BudgetTier {
	if t == BudgetUnset {
		return BudgetFast
	}
	return t
}

// String returns the human-readable name of the budget tier.
func (t BudgetTier) String() string {
	switch t {
	case BudgetUnset:
		return "UNSET"
	case BudgetFast:
		return "FAST"
	case BudgetStandard:
		return "STANDARD"
	case BudgetDeep:
		return "DEEP"
	default:
		return "UNKNOWN"
	}
}

// MaxLatencyMs returns the maximum parallel tool latency for this tier.
// [BudgetUnset] is treated as [BudgetFast].
func (t BudgetTier) MaxLatencyMs() int {
	switch t {
	case BudgetUnset, BudgetFast:
		return 500
	case BudgetStandard:
		return 1500
	case BudgetDeep:
		return 4000
	default:
		return 500
	}
}
