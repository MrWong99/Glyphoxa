package auth

import (
	"strings"
	"unicode"
)

// OperatorAllowlist is the parsed GLYPHOXA_OPERATOR_IDS gate (ADR-0041): the set
// of Discord User snowflakes a self-host deployment grants web-tier access to.
// It is the single authorization gate at the OAuth callback — a Discord User
// whose snowflake is absent is rejected before any session or Tenant write.
// The zero value denies every snowflake.
type OperatorAllowlist struct {
	ids map[string]struct{}
}

// ParseOperatorAllowlist parses a GLYPHOXA_OPERATOR_IDS value into an allowlist.
// Snowflakes are separated by commas and/or whitespace; surrounding whitespace
// is trimmed and empty entries are dropped. Exported and dependency-free so the
// boot-refusal check (#112) can reuse it.
func ParseOperatorAllowlist(s string) OperatorAllowlist {
	sep := func(r rune) bool { return r == ',' || unicode.IsSpace(r) }
	ids := make(map[string]struct{})
	for _, f := range strings.FieldsFunc(s, sep) {
		ids[f] = struct{}{}
	}
	return OperatorAllowlist{ids: ids}
}

// Contains reports whether the Discord User snowflake is on the allowlist.
func (a OperatorAllowlist) Contains(id string) bool {
	_, ok := a.ids[id]
	return ok
}

// Len is the number of distinct snowflakes on the allowlist; zero means the gate
// is unconfigured and every login is rejected.
func (a OperatorAllowlist) Len() int { return len(a.ids) }
