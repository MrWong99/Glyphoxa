package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestPublicProcedures pins the PRODUCTION public set: the Connect procedures
// reachable with no session at all. Every entry is a hole in the "whole API is
// gated" rule, so adding one must mean touching this literal too — i.e. doing
// it on purpose. GetInvitation joined it with ADR-0065: the join page previews
// an invitation link before its visitor has signed in.
func TestPublicProcedures(t *testing.T) {
	t.Parallel()
	want := []string{
		"/glyphoxa.management.v1.AuthService/GetCurrentUser",
		"/glyphoxa.management.v1.AuthService/GetAdmissionMode",
		"/glyphoxa.management.v1.AuthService/GetInvitation",
	}
	if !slices.Equal(publicProcedures, want) {
		t.Fatalf("publicProcedures drifted from the pinned set:\n got  %v\n want %v\n"+
			"A public procedure is reachable by anyone on the internet — update both lists only deliberately.",
			publicProcedures, want)
	}
	// Nothing on the Platform Admin console may ever be public.
	for _, p := range publicProcedures {
		if strings.Contains(p, managementv1connect.AdminServiceName) {
			t.Errorf("AdminService procedure %q is public", p)
		}
	}
}

// TestManagementMounts_AccessSurface checks the ADR-0065 wiring on the REAL
// mount table, sessionless: the join preview answers (and answers "not valid"
// rather than an error for a token that cannot exist), while the AdminService
// is mounted and refuses a caller with no session before any handler runs.
func TestManagementMounts_AccessSurface(t *testing.T) {
	t.Parallel()
	// No request here carries a session and the one public call never reaches
	// the (nil-pool) store: the table is built exactly as runWeb builds it.
	store, log := storage.New(nil), slog.New(slog.DiscardHandler)
	sessions := session.NewIntentControl(store, log, session.IntentControlConfig{})
	mounts := managementMounts(store, nil, nil, nil, log,
		sessions, nil, nil, nil, nil, nil, nil, nil, nil, "", auth.AdmissionAllowlist,
		auth.PlatformAdmins{Allowlist: auth.ParseOperatorAllowlist("42")}, "", nil,
		newOffSessionMetering(auth.AdmissionAllowlist, store, log), llmbuild.DeploymentDefault{})
	mux := http.NewServeMux()
	for _, m := range mounts {
		mux.Handle(m.Path, m.Handler)
	}
	post := func(procedure, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api"+procedure, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// "!" is not token-shaped, so the preview answers without a store read.
	rec := post(managementv1connect.AuthServiceGetInvitationProcedure, `{"token":"!"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetInvitation without a session: status = %d, body = %s; want 200 (it is public)", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "valid") {
		t.Errorf("GetInvitation for an impossible token = %s, want the empty valid=false response", rec.Body)
	}

	for _, procedure := range []string{
		managementv1connect.AdminServiceGetAccessOverviewProcedure,
		managementv1connect.AdminServiceGrantAccessProcedure,
	} {
		if rec := post(procedure, `{}`); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a session: status = %d, want 401", procedure, rec.Code)
		}
	}
}
