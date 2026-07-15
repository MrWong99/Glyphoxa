package auth

import (
	"fmt"
	"net/http"
	"strings"
)

// This file is the declarative policy table for the plain (non-Connect)
// net/http mounts (issue #446) — the SSE relay + snapshot, the highlight
// clip/image byte streams, and the campaign bundle export/import. Which
// checks each mount requires is declared as data in ONE place (the
// composition root's []GuardedMount) and enforced by [MustGuardMounts], so a
// mount cannot silently compose a wrapper subset that differs from the
// Connect gate — the drift that shipped #408.

// GuardedMount is one row of the plain-mount policy table: a Go 1.22
// method+path ServeMux pattern, the tenant posture the handler needs, and the
// handler itself. The session check is unconditional — every guarded mount is
// operator-only (ADR-0041) — and the CSRF check derives from the request
// method (see [MustGuardMounts]), so the tenant mode is the only per-mount
// decision left to declare. Its zero value is invalid on purpose: a row that
// forgets to declare it fails loudly at startup instead of shipping the #408
// subset.
type GuardedMount struct {
	Pattern string
	Tenant  TenantMode
	Handler http.Handler
}

// guardedMethods are the method verbs a guarded pattern must lead with. The
// method requirement keeps the table honest: every row states what it serves,
// and the ServeMux registration then enforces it.
var guardedMethods = map[string]struct{}{
	http.MethodGet:    {},
	http.MethodHead:   {},
	http.MethodPost:   {},
	http.MethodPut:    {},
	http.MethodPatch:  {},
	http.MethodDelete: {},
}

// validate rejects a row that under-declares its policy. Returned errors are
// programmer errors — MustGuardMounts turns them into a boot panic.
func (g GuardedMount) validate() error {
	if g.Pattern == "" {
		return fmt.Errorf("auth: guarded mount with empty pattern")
	}
	method, _, ok := strings.Cut(g.Pattern, " ")
	if _, known := guardedMethods[method]; !ok || !known {
		return fmt.Errorf("auth: guarded mount %q must lead with an explicit method (e.g. %q)", g.Pattern, "GET "+g.Pattern)
	}
	if g.Handler == nil {
		return fmt.Errorf("auth: guarded mount %q has a nil handler", g.Pattern)
	}
	switch g.Tenant {
	case TenantNone, TenantOptional, TenantRequired:
		return nil
	case tenantUnspecified:
		return fmt.Errorf("auth: guarded mount %q does not declare a tenant mode — set Tenant to TenantNone, TenantOptional or TenantRequired (the undeclared-subset drift is what shipped #408)", g.Pattern)
	default:
		return fmt.Errorf("auth: guarded mount %q has an unknown tenant mode %d", g.Pattern, g.Tenant)
	}
}

// MustGuardMounts validates the plain-mount policy table and returns a copy
// with every handler wrapped in the shared policy gate ([Policy.Evaluate] —
// the same evaluation the Connect interceptor runs). It panics on an invalid
// table (undeclared tenant mode, missing method, nil handler, duplicate
// pattern): the table is static boot configuration, and a mount without a
// declared policy must fail the boot, not serve.
//
// Per request the guard requires a valid session, requires the CSRF
// double-submit pair for state-changing methods (everything but GET/HEAD/
// OPTIONS — the plain-HTTP spelling of the Connect NO_SIDE_EFFECTS
// exemption, ADR-0016), and applies the row's declared [TenantMode]. Denials
// map to 401/403 via [WriteDenial]; on success the resolved operator and
// tenant ride the request context ([CurrentUser] / [TenantID]).
func MustGuardMounts(p *Policy, rows []GuardedMount) []GuardedMount {
	if p == nil {
		panic("auth: MustGuardMounts requires a non-nil Policy")
	}
	seen := make(map[string]struct{}, len(rows))
	guarded := make([]GuardedMount, 0, len(rows))
	for _, row := range rows {
		if err := row.validate(); err != nil {
			panic(err.Error())
		}
		if _, dup := seen[row.Pattern]; dup {
			panic(fmt.Sprintf("auth: guarded mount %q declared twice", row.Pattern))
		}
		seen[row.Pattern] = struct{}{}
		guarded = append(guarded, GuardedMount{
			Pattern: row.Pattern,
			Tenant:  row.Tenant,
			Handler: p.guard(row),
		})
	}
	return guarded
}

// guard wraps one row's handler in the policy gate.
func (p *Policy) guard(row GuardedMount) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := p.Evaluate(r.Context(), InputFromHeader(r.Header), Check{
			CSRF:   !safeMethod(r.Method),
			Tenant: row.Tenant,
		})
		if v.Deny != DenyNone {
			WriteDenial(w, v.Deny)
			return
		}
		row.Handler.ServeHTTP(w, r.WithContext(v.Context(r.Context())))
	})
}

// safeMethod reports whether the request method is read-only in the ADR-0016
// sense — exempt from the CSRF double-submit exactly like a NO_SIDE_EFFECTS
// Connect procedure. Derived at request time from the live method (not the
// pattern), so a future state-changing mount cannot forget its CSRF gate.
func safeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}
