# Logic Audit — 2026-03-27

Manual audit of Glyphoxa for logical correctness bugs that compile and pass tests
but produce wrong behavior. Automated tools (linters, race detector) cannot catch these.

**Scope:** Authorization, state machines, quota enforcement, tenant isolation, resource management.

**Method:** Five parallel agents audited off-by-one/boundary, state machines, semantic/business logic,
integration/gRPC, and web/auth/RBAC. All findings were manually verified against source code.

---

## Summary

| # | Severity | Bug | Location |
|---|----------|-----|----------|
| 1 | Critical | Cross-tenant session stop — no tenant check | `internal/web/handlers_sessions.go:121` |
| 2 | High | gRPC client connection leak on dispatch | `internal/gateway/sessionctrl.go:267` |
| 3 | High | Quota check TOCTOU race condition | `internal/gateway/usage/postgres.go:68` |
| 4 | Medium | Cross-tenant campaign blocking in memory store | `internal/gateway/sessionorch/memory.go:46` |
| 5 | Medium | X-Forwarded-For trusted for rate limit keying | `internal/web/ratelimit.go:150` |
| 6 | Medium | Inconsistent tenant ID validation regexes | `internal/config/tenant.go:49` vs `internal/web/store.go:22` |
| 7 | Low | Silent error swallowing in session scan | `internal/gateway/sessionorch/postgres.go:249` |

---

## Bug 1 — Cross-Tenant Session Stop (Critical)

**Location:** `internal/web/handlers_sessions.go:121-140`

**Description:** `handleStopSession` takes a session ID from the URL path and passes it
directly to `gwClient.StopWebSession` without verifying the session belongs to the
authenticated user's tenant. Any DM-role user from any tenant can stop any session
across all tenants by knowing (or guessing) the session UUID.

**What the code does:**
```go
func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
    claims := requireClaims(w, r)
    // ...
    sessionID := r.PathValue("id")
    // ❌ No check that sessionID belongs to claims.TenantID
    if _, err := s.gwClient.StopWebSession(r.Context(), &pb.StopWebSessionRequest{
        SessionId: sessionID,
    }); err != nil { ... }
}
```

**Proof:** Compare with `handleGetTranscript` (line 54) which correctly validates:
```go
exists, err := s.store.SessionExists(r.Context(), claims.TenantID, sessionID)
```

The gateway's `StopWebSession` (management.go:259) also doesn't validate
the caller's tenant — it looks up the session and routes to its controller directly.

**Suggested fix:** Add the same tenant ownership check used in `handleGetTranscript`:
```go
exists, err := s.store.SessionExists(r.Context(), claims.TenantID, sessionID)
if err != nil {
    writeError(w, http.StatusInternalServerError, "server_error", "failed to check session")
    return
}
if !exists {
    writeError(w, http.StatusNotFound, "not_found", "session not found")
    return
}
```

---

## Bug 2 — gRPC Client Connection Leak (High)

**Location:** `internal/gateway/sessionctrl.go:267-279`

**Description:** The `starter` callback in `GatewaySessionController.Start()` dials
a worker via `gc.dialer(addr)`, which creates a `grpctransport.Client` holding a
`grpc.ClientConn`. The client is used for a single `StartSession` RPC, then the
function returns — the connection is never closed.

**What the code does:**
```go
starter := func(callCtx context.Context, addr string) error {
    client, err := gc.dialer(addr)  // creates grpc.ClientConn
    if err != nil { return ... }
    if err := client.StartSession(callCtx, startReq); err != nil {
        return ...  // connection leaked
    }
    return nil      // connection leaked
}
```

**Proof:** `grpctransport.Client` has a `Close()` method (client.go:213) that closes the
underlying `grpc.ClientConn`. However, the `WorkerClient` interface (contract.go:95)
does not include `Close()`, so the caller has no way to close it through the interface.
Each session start leaks one TCP connection. Under sustained load this exhausts file
descriptors.

**Suggested fix:** Either:
- Add `Close() error` to the `WorkerClient` interface and defer close in the callback
- Or type-assert to `io.Closer` in the callback:
```go
starter := func(callCtx context.Context, addr string) error {
    client, err := gc.dialer(addr)
    if err != nil { return ... }
    if c, ok := client.(interface{ Close() error }); ok {
        defer c.Close()
    }
    return client.StartSession(callCtx, startReq)
}
```

---

## Bug 3 — Quota Check TOCTOU Race Condition (High)

**Location:** `internal/gateway/usage/postgres.go:68-84`

**Description:** `CheckQuota()` reads current usage with a plain SELECT, then returns.
`RecordUsage()` (line 30) writes usage with an UPSERT. These are separate operations
with no transactional or advisory lock coordination. Two concurrent `ValidateAndCreate`
calls can both pass the quota check before either records usage.

**What the code does:**
```go
func (s *PostgresStore) CheckQuota(ctx context.Context, tenantID string, quota QuotaConfig) error {
    rec, err := s.GetUsage(ctx, tenantID, period)  // plain SELECT
    if rec.SessionHours >= quota.MonthlySessionHours {
        return ErrQuotaExceeded
    }
    return nil  // ❌ no lock held — concurrent caller can also pass
}
```

**Attack scenario:**
1. Tenant at 9.5 of 10 allowed hours
2. Request A reads 9.5 hours → passes check
3. Request B reads 9.5 hours → passes check (before A records)
4. Both sessions start → tenant at 11.5 hours

**Suggested fix:** Use `SELECT ... FOR UPDATE` inside a transaction, or use a PostgreSQL
advisory lock keyed on tenant ID:
```go
func (s *PostgresStore) CheckQuota(ctx context.Context, tenantID string, quota QuotaConfig) error {
    tx, _ := s.pool.Begin(ctx)
    defer tx.Rollback(ctx)

    var hours float64
    tx.QueryRow(ctx, `
        SELECT COALESCE(session_hours, 0)
        FROM usage_records WHERE tenant_id = $1 AND period = $2
        FOR UPDATE
    `, tenantID, CurrentPeriod()).Scan(&hours)

    if hours >= quota.MonthlySessionHours {
        return ErrQuotaExceeded
    }
    return tx.Commit(ctx) // hold the lock until commit
}
```

Or accept eventual consistency and document that quota is soft-enforced.

---

## Bug 4 — Cross-Tenant Campaign Blocking in Memory Store (Medium)

**Location:** `internal/gateway/sessionorch/memory.go:46-49`

**Description:** The campaign uniqueness check runs before the tenant filter.
If tenant A has an active session for campaign X, tenant B is blocked from
creating a session for any campaign with the same ID — violating tenant isolation.

**What the code does:**
```go
for _, s := range m.sessions {
    if s.State == gateway.SessionEnded { continue }
    if s.CampaignID == req.CampaignID {  // ❌ checked before tenant filter
        return "", fmt.Errorf("campaign %q already has an active session", ...)
    }
    if s.TenantID != req.TenantID { continue }  // tenant filter is too late
    // ...
}
```

**Mitigating factor:** Campaign IDs are UUIDs generated by `uuid.NewString()`, so
cross-tenant collisions are practically impossible. The PostgreSQL implementation
has no such constraint, creating an inconsistency between stores.

**Suggested fix:** Move the tenant filter before the campaign check:
```go
if s.TenantID != req.TenantID { continue }
if s.CampaignID == req.CampaignID { ... }
```

---

## Bug 5 — X-Forwarded-For Trusted for Rate Limit Keying (Medium)

**Location:** `internal/web/ratelimit.go:150-161`

**Description:** The `clientIP` function blindly trusts the `X-Forwarded-For` header
for IP-based rate limiting on unauthenticated requests. An attacker can rotate IPs
in the header to bypass rate limits entirely. The function also contains dead code:
`if i := 0; i < len(xff)` where `i` is never used (the condition is always true
since `xff != ""` was already checked).

**What the code does:**
```go
func clientIP(r *http.Request) string {
    if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
        if i := 0; i < len(xff) {  // dead variable, always true
            for j, c := range xff {
                if c == ',' { return xff[:j] }
                _ = j
            }
            return xff
        }
    }
    // ...
}
```

**Suggested fix:** Only trust forwarded headers when behind a known reverse proxy,
or fall back to `r.RemoteAddr` for rate limiting.

---

## Bug 6 — Inconsistent Tenant ID Validation Regexes (Medium)

**Location:** `internal/config/tenant.go:49` and `internal/web/store.go:22`

**Description:** Two different regexes validate tenant IDs:
- **config (gateway):** `^[a-z][a-z0-9_]{0,62}$` — lowercase start, lowercase + digits + underscore
- **web (onboarding):** `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$` — any alphanumeric start, mixed case, hyphens allowed

A tenant ID like `"MyTenant-1"` passes web validation during onboarding but fails
gateway validation, causing runtime errors when the gateway tries to process the tenant.

**Suggested fix:** Use a single canonical pattern. The gateway's stricter regex is
correct for PostgreSQL schema names. Update `web/store.go` to match:
```go
var validTenantID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
```

---

## Bug 7 — Silent Error Swallowing in Session Scan (Low)

**Location:** `internal/gateway/sessionorch/postgres.go:249-250`

**Description:** When scanning sessions from the database, `ParseLicenseTier` and
`ParseSessionState` errors are discarded:
```go
s.LicenseTier, _ = config.ParseLicenseTier(tierStr)
s.State, _ = gateway.ParseSessionState(stateStr)
```

If the database contains an invalid tier (due to schema drift, manual edit, or bug),
the session silently gets `TierShared` (zero value) and `SessionPending` (zero value).
A dedicated-tier session misclassified as shared could receive wrong resource allocation.

**Suggested fix:** Return the parse error:
```go
var err error
s.LicenseTier, err = config.ParseLicenseTier(tierStr)
if err != nil {
    return nil, fmt.Errorf("sessionorch: parse tier %q for session %s: %w", tierStr, s.ID, err)
}
```

---

## False Positives Rejected

| Claim | Why Rejected |
|-------|-------------|
| Missing RBAC in handleListUsers / handleDeleteUser / handleCreateInvite | `RequireRole("tenant_admin")` middleware wraps all three in `server.go:91-95` |
| Circuit breaker half-open close condition off-by-one | Logic is correct: `successes >= halfOpenMax` can only be true when `halfOpenCalls == halfOpenMax && halfOpenFails == 0`, meaning all probes succeeded |
| Audio PCM encoding endianness error | Agent self-corrected — Go operator precedence makes the expression correct |

---

## Areas Audited (No Bugs Found)

- **Pagination:** Cursor-based pagination in campaigns, offset-based in sessions — both correct
- **Audio buffer handling:** Bounds checks in `pkg/audio/convert.go`, ring buffer in consolidator correct
- **Mixer barge-in:** Interrupt logic properly synchronized under mutex
- **Provider failover:** FallbackGroup correctly wraps circuit breakers around each provider
- **Graceful shutdown:** Context propagation and goroutine cleanup patterns look sound
- **Discord event handling:** Command routing and voice state tracking appear complete
