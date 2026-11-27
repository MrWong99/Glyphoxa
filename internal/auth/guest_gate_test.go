package auth_test

// The Campaign Guest gate (ADR-0065). What matters here is default-deny: a
// guest reaches exactly the procedures and mounts someone decided they may
// reach, with exactly the right each needs, and nothing else — including an
// RPC nobody has written yet.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeWorkspaces is a scripted auth.WorkspaceResolver recording what it was
// asked.
type fakeWorkspaces struct {
	ws       storage.Workspace
	err      error
	gotToken string
	gotUser  storage.User
	calls    int
}

func (f *fakeWorkspaces) ResolveWorkspace(_ context.Context, u storage.User, token string) (storage.Workspace, error) {
	f.calls++
	f.gotUser, f.gotToken = u, token
	return f.ws, f.err
}

func guestOf(tenantID, campaignID uuid.UUID, rights ...auth.GuestRight) *fakeWorkspaces {
	return &fakeWorkspaces{ws: storage.Workspace{
		TenantID: tenantID, Guest: true, CampaignID: campaignID,
		Rights: auth.GuestRightStrings(auth.NormalizeGuestRights(rights)),
	}}
}

// managementProcedures enumerates EVERY procedure of every service in the
// management proto file, straight from the generated descriptor — the source
// of truth a new RPC cannot avoid appearing in.
func managementProcedures(t *testing.T) []string {
	t.Helper()
	var out []string
	services := managementv1.File_glyphoxa_management_v1_management_proto.Services()
	for i := 0; i < services.Len(); i++ {
		svc := services.Get(i)
		methods := svc.Methods()
		for j := 0; j < methods.Len(); j++ {
			out = append(out, "/"+string(svc.FullName())+"/"+string(methods.Get(j).Name()))
		}
	}
	if len(out) < 100 {
		t.Fatalf("descriptor walk found only %d procedures — the walk is broken", len(out))
	}
	sort.Strings(out)
	return out
}

// TestGuestGate_EveryProcedureIsClassified is the drift test: every procedure
// of every management service is either guest-reachable (with a declared
// posture) or explicitly GM-only. A new RPC fails here until someone decides —
// it can be neither silently guest-reachable nor silently unclassified.
func TestGuestGate_EveryProcedureIsClassified(t *testing.T) {
	t.Parallel()
	real := map[string]bool{}
	for _, proc := range managementProcedures(t) {
		real[proc] = true
		reachable := auth.GuestProcedurePosture(proc).Reachable()
		gmOnly := auth.GMOnlyProcedure(proc)
		switch {
		case reachable && gmOnly:
			t.Errorf("%s is in BOTH the guest table and the GM-only list — decide which", proc)
		case !reachable && !gmOnly:
			t.Errorf("%s is not classified. Add it to gmOnlyProcedures (the default), or — only after "+
				"hardening its handler for a guest principal — to guestProcedures (internal/auth/guest_gate.go).", proc)
		}
	}
	// A stale entry (a renamed or deleted RPC) would otherwise sit in a table
	// looking like a decision about something that no longer exists.
	for proc := range auth.GuestProcedures() {
		if !real[proc] {
			t.Errorf("guest table names %s, which is not a management procedure", proc)
		}
	}
}

// wantGuestProcedures restates the guest-reachable table: procedure → the
// rights of which one is required (nil = every guest). It is a literal on
// purpose. Changing what a guest can reach must mean changing this test too,
// i.e. doing it deliberately — the plainMountPolicy discipline.
var wantGuestProcedures = map[string][]auth.GuestRight{
	"/glyphoxa.management.v1.AuthService/GetCurrentUser":   nil,
	"/glyphoxa.management.v1.AuthService/Logout":           nil,
	"/glyphoxa.management.v1.AuthService/GetAdmissionMode": nil,
	"/glyphoxa.management.v1.AuthService/GetInvitation":    nil,
	"/glyphoxa.management.v1.AuthService/SwitchWorkspace":  nil,

	"/glyphoxa.management.v1.CampaignService/GetActiveCampaign":      nil,
	"/glyphoxa.management.v1.CampaignService/ListSupportedLanguages": nil,

	"/glyphoxa.management.v1.AdminService/GetAccessOverview":      nil,
	"/glyphoxa.management.v1.AdminService/GrantAccess":            nil,
	"/glyphoxa.management.v1.AdminService/RevokeAccess":           nil,
	"/glyphoxa.management.v1.AdminService/CreateAccessInvitation": nil,
	"/glyphoxa.management.v1.AdminService/RevokeAccessInvitation": nil,
	"/glyphoxa.management.v1.AdminService/SetUserSuspended":       nil,
	"/glyphoxa.management.v1.AdminService/SetTenantPlatformKeys":  nil,

	"/glyphoxa.management.v1.CampaignService/ListNodes":           {auth.RightWikiRead},
	"/glyphoxa.management.v1.CampaignService/ListNodeEdges":       {auth.RightWikiRead},
	"/glyphoxa.management.v1.CampaignService/GetKnowledgeGraph":   {auth.RightWikiRead},
	"/glyphoxa.management.v1.CampaignService/GetCampaignTags":     {auth.RightWikiRead},
	"/glyphoxa.management.v1.CampaignService/SearchNodes":         {auth.RightWikiRead},
	"/glyphoxa.management.v1.CampaignService/ListNodeAppearances": {auth.RightWikiRead},

	// Prep boards are the GM's working surface: reading them needs wiki.edit.
	"/glyphoxa.management.v1.CampaignService/ListBoards":        {auth.RightWikiEdit},
	"/glyphoxa.management.v1.CampaignService/CreateNode":        {auth.RightWikiEdit},
	"/glyphoxa.management.v1.CampaignService/UpdateNode":        {auth.RightWikiEdit},
	"/glyphoxa.management.v1.CampaignService/DeleteNode":        {auth.RightWikiEdit},
	"/glyphoxa.management.v1.CampaignService/CreateEdge":        {auth.RightWikiEdit},
	"/glyphoxa.management.v1.CampaignService/DeleteEdge":        {auth.RightWikiEdit},
	"/glyphoxa.management.v1.CampaignService/UpdateEdgeDetails": {auth.RightWikiEdit},
	"/glyphoxa.management.v1.CampaignService/SetNodeTags":       {auth.RightWikiEdit},
	"/glyphoxa.management.v1.CampaignService/RenameTag":         {auth.RightWikiEdit},
	"/glyphoxa.management.v1.CampaignService/CreateBoard":       {auth.RightWikiEdit},
	"/glyphoxa.management.v1.CampaignService/UpdateBoard":       {auth.RightWikiEdit},
	"/glyphoxa.management.v1.CampaignService/DeleteBoard":       {auth.RightWikiEdit},

	"/glyphoxa.management.v1.CampaignService/ListMaps":   {auth.RightMapsRead},
	"/glyphoxa.management.v1.CampaignService/GetMapView": {auth.RightMapsRead},
	"/glyphoxa.management.v1.CampaignService/CreatePin":  {auth.RightMapsEdit},
	"/glyphoxa.management.v1.CampaignService/UpdatePin":  {auth.RightMapsEdit},
	"/glyphoxa.management.v1.CampaignService/DeletePin":  {auth.RightMapsEdit},

	"/glyphoxa.management.v1.SessionService/SearchTranscriptLines": {auth.RightTranscriptsRead},
	"/glyphoxa.management.v1.SessionService/SearchTranscripts":     {auth.RightTranscriptsRead},
	"/glyphoxa.management.v1.SessionService/ListSessions":          {auth.RightTranscriptsRead, auth.RightHighlightsRead},

	"/glyphoxa.management.v1.SessionService/ListHighlights":   {auth.RightHighlightsRead},
	"/glyphoxa.management.v1.SessionService/GetHighlight":     {auth.RightHighlightsRead},
	"/glyphoxa.management.v1.SessionService/SearchHighlights": {auth.RightHighlightsRead},

	"/glyphoxa.management.v1.ChatService/ListPlanningThreads":  {auth.RightPlanningUse},
	"/glyphoxa.management.v1.ChatService/GetPlanningThread":    {auth.RightPlanningUse},
	"/glyphoxa.management.v1.ChatService/CreatePlanningThread": {auth.RightPlanningUse},
	"/glyphoxa.management.v1.ChatService/RenamePlanningThread": {auth.RightPlanningUse},
	"/glyphoxa.management.v1.ChatService/DeletePlanningThread": {auth.RightPlanningUse},
	"/glyphoxa.management.v1.ChatService/PlanningExchange":     {auth.RightPlanningUse},
}

// TestGuestGate_TableIsPinned compares the production table against the
// literal above, both ways.
func TestGuestGate_TableIsPinned(t *testing.T) {
	t.Parallel()
	got := auth.GuestProcedures()
	for proc, wantRights := range wantGuestProcedures {
		posture, ok := got[proc]
		if !ok || !posture.Reachable() {
			t.Errorf("%s: want guest-reachable needing %v, but it is GM-only", proc, wantRights)
			continue
		}
		if !slices.Equal(posture.Rights(), wantRights) {
			t.Errorf("%s: requires %v, want %v", proc, posture.Rights(), wantRights)
		}
	}
	for proc := range got {
		if _, ok := wantGuestProcedures[proc]; !ok {
			t.Errorf("%s is guest-reachable in production but not in the pinned table — "+
				"opening a procedure to guests must be deliberate: update wantGuestProcedures too", proc)
		}
	}
	// Nothing on the six guest-management RPCs may ever be guest-reachable: a
	// guest who could manage guests could grant themselves everything.
	for proc := range got {
		if strings.Contains(proc, "CampaignGuest") || strings.Contains(proc, "CampaignInvitation") ||
			strings.HasSuffix(proc, "/ListCampaignAccess") {
			t.Errorf("guest-management procedure %s is guest-reachable", proc)
		}
	}
}

// TestGuestGate_AdminServiceIgnoresTheWorkspace: a Platform Admin who is
// acting as someone's guest must still be able to administer the instance, so
// the gate lets every AdminService procedure through for any guest. (Its
// handlers require a Platform Admin themselves — rpc.AdminServer.requireAdmin.)
func TestGuestGate_AdminServiceIgnoresTheWorkspace(t *testing.T) {
	t.Parallel()
	noRights := auth.NewRightSet()
	seen := 0
	for _, proc := range managementProcedures(t) {
		if !strings.HasPrefix(proc, "/"+managementv1connect.AdminServiceName+"/") {
			continue
		}
		seen++
		if !auth.GuestProcedurePosture(proc).Admits(noRights) {
			t.Errorf("%s is refused for a guest workspace; AdminService must not depend on the workspace", proc)
		}
	}
	if seen == 0 {
		t.Fatal("no AdminService procedure found")
	}
}

// testImplies is the ADR-0065 implication table restated, so the matrix below
// does not derive its expectations from the code under test.
var testImplies = map[auth.GuestRight][]auth.GuestRight{
	auth.RightWikiEdit:    {auth.RightWikiRead, auth.RightWikiSecrets},
	auth.RightMapsEdit:    {auth.RightMapsRead},
	auth.RightPlanningUse: {auth.RightWikiRead, auth.RightWikiSecrets},
}

var allGuestRights = []auth.GuestRight{
	auth.RightWikiRead, auth.RightWikiSecrets, auth.RightWikiEdit,
	auth.RightMapsRead, auth.RightMapsEdit,
	auth.RightTranscriptsRead, auth.RightHighlightsRead, auth.RightPlanningUse,
}

// TestPolicy_GuestGate_RightsMatrix runs the real policy for every right ×
// every management procedure (plus a guest with no rights at all, and one
// with every right): a guest holding exactly one granted right passes a
// procedure iff the pinned table says that right — or one it implies — opens
// it. For every procedure NOT in the pinned table that means: never.
func TestPolicy_GuestGate_RightsMatrix(t *testing.T) {
	t.Parallel()
	op := operator()
	authn := fakeAuthN{users: map[string]storage.User{validToken: op}}
	tenantID, campaignID := uuid.New(), uuid.New()

	type holder struct {
		name    string
		granted []auth.GuestRight
	}
	holders := []holder{{name: "no rights"}, {name: "every right", granted: allGuestRights}}
	for _, r := range allGuestRights {
		holders = append(holders, holder{name: string(r), granted: []auth.GuestRight{r}})
	}

	for _, h := range holders {
		effective := map[auth.GuestRight]bool{}
		for _, r := range h.granted {
			effective[r] = true
			for _, implied := range testImplies[r] {
				effective[implied] = true
			}
		}
		policy := auth.NewPolicy(authn, nil).WithWorkspaces(guestOf(tenantID, campaignID, h.granted...))
		for _, proc := range managementProcedures(t) {
			needs, reachable := wantGuestProcedures[proc]
			want := reachable && len(needs) == 0
			for _, r := range needs {
				want = want || effective[r]
			}
			v := policy.Evaluate(context.Background(),
				auth.Input{SessionToken: validToken},
				auth.Check{Tenant: auth.TenantOptional, Guest: auth.GuestProcedurePosture(proc)})
			switch {
			case want && v.Deny != auth.DenyNone:
				t.Errorf("guest with %s: %s denied (%v), want allowed", h.name, proc, v.Deny)
			case !want && v.Deny != auth.DenyGuest:
				t.Errorf("guest with %s: %s verdict = %v, want DenyGuest", h.name, proc, v.Deny)
			case want && (!v.IsGuest || v.Guest.CampaignID != campaignID || v.Tenant != tenantID):
				t.Errorf("guest with %s: %s allowed but the verdict lost the guest scope: %+v", h.name, proc, v)
			}
		}
	}
}

// TestPolicy_GuestGate_Postures covers the gate's edges on the bare policy.
func TestPolicy_GuestGate_Postures(t *testing.T) {
	t.Parallel()
	op := operator()
	authn := fakeAuthN{users: map[string]storage.User{validToken: op}}
	tenantID, campaignID := uuid.New(), uuid.New()
	in := auth.Input{SessionToken: validToken}
	everything := guestOf(tenantID, campaignID, allGuestRights...)

	t.Run("the zero posture denies a guest holding every right", func(t *testing.T) {
		t.Parallel()
		for _, mode := range []auth.TenantMode{auth.TenantNone, auth.TenantOptional, auth.TenantRequired} {
			v := auth.NewPolicy(authn, nil).WithWorkspaces(everything).
				Evaluate(context.Background(), in, auth.Check{Tenant: mode})
			if v.Deny != auth.DenyGuest {
				t.Errorf("tenant mode %d: verdict = %v, want DenyGuest", mode, v.Deny)
			}
		}
	})

	t.Run("GuestWith with no rights is GM-only, not every guest", func(t *testing.T) {
		t.Parallel()
		if auth.GuestWith().Reachable() || auth.GuestWith().Admits(auth.NewRightSet(allGuestRights...)) {
			t.Fatal("GuestWith() admitted a guest; an empty requirement must not read as no requirement")
		}
	})

	t.Run("a TenantNone mount still learns the caller is a guest", func(t *testing.T) {
		t.Parallel()
		ws := guestOf(tenantID, campaignID, allGuestRights...)
		v := auth.NewPolicy(authn, nil).WithWorkspaces(ws).
			Evaluate(context.Background(), in, auth.Check{Tenant: auth.TenantNone, Guest: auth.GuestAny()})
		if v.Deny != auth.DenyNone || !v.IsGuest || v.HasTenant {
			t.Fatalf("verdict = %+v, want an admitted guest with NO tenant injected", v)
		}
		if ws.calls != 1 {
			t.Fatalf("workspace resolved %d times, want 1", ws.calls)
		}
	})

	t.Run("the workspace is resolved with the request's session token and user", func(t *testing.T) {
		t.Parallel()
		ws := guestOf(tenantID, campaignID)
		auth.NewPolicy(authn, nil).WithWorkspaces(ws).
			Evaluate(context.Background(), in, auth.Check{Tenant: auth.TenantOptional, Guest: auth.GuestAny()})
		if ws.gotToken != validToken || ws.gotUser.ID != op.ID {
			t.Fatalf("resolver got token %q user %s, want the session's", ws.gotToken, ws.gotUser.ID)
		}
	})

	t.Run("an own-tenant workspace is not a guest and passes a GM-only check", func(t *testing.T) {
		t.Parallel()
		own := &fakeWorkspaces{ws: storage.Workspace{TenantID: tenantID}}
		v := auth.NewPolicy(authn, nil).WithWorkspaces(own).
			Evaluate(context.Background(), in, auth.Check{Tenant: auth.TenantRequired})
		if v.Deny != auth.DenyNone || v.IsGuest || v.Tenant != tenantID {
			t.Fatalf("verdict = %+v, want the operator admitted in their own tenant", v)
		}
	})

	t.Run("no workspace at all is no tenant", func(t *testing.T) {
		t.Parallel()
		none := &fakeWorkspaces{err: storage.ErrNotFound}
		p := auth.NewPolicy(authn, nil).WithWorkspaces(none)
		if v := p.Evaluate(context.Background(), in, auth.Check{Tenant: auth.TenantRequired, Guest: auth.GuestAny()}); v.Deny != auth.DenyUnauthenticated {
			t.Errorf("TenantRequired: verdict = %v, want DenyUnauthenticated", v.Deny)
		}
		if v := p.Evaluate(context.Background(), in, auth.Check{Tenant: auth.TenantOptional}); v.Deny != auth.DenyNone || v.HasTenant || v.IsGuest {
			t.Errorf("TenantOptional: verdict = %+v, want tenantless and not a guest", v)
		}
	})

	t.Run("a resolver failure is unavailable, never an admission", func(t *testing.T) {
		t.Parallel()
		down := &fakeWorkspaces{err: errors.New("pool timeout")}
		for _, mode := range []auth.TenantMode{auth.TenantNone, auth.TenantOptional, auth.TenantRequired} {
			v := auth.NewPolicy(authn, nil).WithWorkspaces(down).
				Evaluate(context.Background(), in, auth.Check{Tenant: mode, Guest: auth.GuestAny()})
			if v.Deny != auth.DenyUnavailable {
				t.Errorf("tenant mode %d: verdict = %v, want DenyUnavailable", mode, v.Deny)
			}
		}
	})

	t.Run("an undeclared tenant mode fails closed", func(t *testing.T) {
		t.Parallel()
		v := auth.NewPolicy(authn, nil).WithWorkspaces(everything).
			Evaluate(context.Background(), in, auth.Check{Guest: auth.GuestAny()})
		if v.Deny == auth.DenyNone {
			t.Fatal("a Check with no tenant mode was admitted")
		}
	})

	t.Run("without WithWorkspaces nobody is ever a guest", func(t *testing.T) {
		t.Parallel()
		v := auth.NewPolicy(authn, fakeTenant{id: tenantID}).
			Evaluate(context.Background(), in, auth.Check{Tenant: auth.TenantRequired})
		if v.Deny != auth.DenyNone || v.IsGuest {
			t.Fatalf("verdict = %+v, want the plain tenant pass-through", v)
		}
	})

	t.Run("unknown stored rights grant nothing", func(t *testing.T) {
		t.Parallel()
		odd := &fakeWorkspaces{ws: storage.Workspace{
			TenantID: tenantID, Guest: true, CampaignID: campaignID,
			Rights: []string{"wiki.admin", "*", "WIKI.READ", ""},
		}}
		v := auth.NewPolicy(authn, nil).WithWorkspaces(odd).
			Evaluate(context.Background(), in, auth.Check{Tenant: auth.TenantOptional, Guest: auth.GuestWith(auth.RightWikiRead)})
		if v.Deny != auth.DenyGuest {
			t.Fatalf("verdict = %v, want DenyGuest: an unrecognised right must not open anything", v.Deny)
		}
	})
}

// guestStack mounts a recording AuthService behind the REAL interceptor stack
// over a policy that resolves the session to the given workspace.
func guestStack(t *testing.T, ws auth.WorkspaceResolver) (managementv1connect.AuthServiceClient, *guestSeen) {
	t.Helper()
	seen := &guestSeen{}
	authn := fakeAuthN{users: map[string]storage.User{validToken: operator()}}
	stack := auth.NewPolicy(authn, nil).WithWorkspaces(ws).Stack(
		managementv1connect.AuthServiceGetCurrentUserProcedure)
	mux := http.NewServeMux()
	mux.Handle(managementv1connect.NewAuthServiceHandler(&guestRecordingAuthService{seen: seen}, stack.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return managementv1connect.NewAuthServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON()), seen
}

type guestSeen struct {
	guest      auth.GuestScope
	isGuest    bool
	tenant     uuid.UUID
	renameHits int
}

// guestRecordingAuthService records the guest scope the stack injected and
// counts handler entries for the GM-only RenameTenant.
type guestRecordingAuthService struct {
	managementv1connect.UnimplementedAuthServiceHandler
	seen *guestSeen
}

func (s *guestRecordingAuthService) GetCurrentUser(ctx context.Context, _ *connect.Request[managementv1.GetCurrentUserRequest]) (*connect.Response[managementv1.GetCurrentUserResponse], error) {
	s.seen.guest, s.seen.isGuest = auth.Guest(ctx)
	s.seen.tenant, _ = auth.TenantID(ctx)
	return connect.NewResponse(&managementv1.GetCurrentUserResponse{}), nil
}

func (s *guestRecordingAuthService) RenameTenant(context.Context, *connect.Request[managementv1.RenameTenantRequest]) (*connect.Response[managementv1.RenameTenantResponse], error) {
	s.seen.renameHits++
	return connect.NewResponse(&managementv1.RenameTenantResponse{}), nil
}

func signedIn[T any](msg *T) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Cookie", auth.SessionCookieName+"="+validToken+"; "+auth.CSRFCookieName+"="+csrfValue)
	req.Header().Set("X-CSRF-Token", csrfValue)
	return req
}

// TestInterceptor_GuestGate drives the Connect adapter: a guest's scope
// reaches a guest-reachable handler, and a GM-only procedure is refused with
// CodePermissionDenied BEFORE its handler runs.
func TestInterceptor_GuestGate(t *testing.T) {
	t.Parallel()
	tenantID, campaignID := uuid.New(), uuid.New()
	client, seen := guestStack(t, guestOf(tenantID, campaignID, auth.RightMapsEdit))

	if _, err := client.GetCurrentUser(context.Background(), signedIn(&managementv1.GetCurrentUserRequest{})); err != nil {
		t.Fatalf("GetCurrentUser as a guest: %v", err)
	}
	if !seen.isGuest || seen.guest.CampaignID != campaignID || seen.tenant != tenantID {
		t.Fatalf("handler saw guest=%v scope=%+v tenant=%s; want the guest of %s in host tenant %s",
			seen.isGuest, seen.guest, seen.tenant, campaignID, tenantID)
	}
	// maps.edit implies maps.read and nothing else.
	if !seen.guest.Rights.Has(auth.RightMapsEdit) || !seen.guest.Rights.Has(auth.RightMapsRead) || seen.guest.Rights.Has(auth.RightWikiRead) {
		t.Fatalf("injected rights = %v, want maps.read + maps.edit", seen.guest.Rights.List())
	}

	_, err := client.RenameTenant(context.Background(), signedIn(&managementv1.RenameTenantRequest{Name: "Mine now"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("RenameTenant as a guest: err = %v, want CodePermissionDenied", err)
	}
	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Message() != auth.DenyGuest.Message() {
		t.Errorf("denial text = %v, want %q", err, auth.DenyGuest.Message())
	}
	if seen.renameHits != 0 {
		t.Fatalf("the GM-only handler ran %d times for a guest; the gate must refuse before it", seen.renameHits)
	}

	// The same call from the Tenant's own operator goes through.
	own, ownSeen := guestStack(t, &fakeWorkspaces{ws: storage.Workspace{TenantID: tenantID}})
	if _, err := own.RenameTenant(context.Background(), signedIn(&managementv1.RenameTenantRequest{Name: "Mine"})); err != nil {
		t.Fatalf("RenameTenant as the operator: %v", err)
	}
	if ownSeen.renameHits != 1 {
		t.Fatalf("operator's RenameTenant reached the handler %d times, want 1", ownSeen.renameHits)
	}
}

// TestGuardedMount_GuestGate drives the plain-HTTP adapter with the same
// decisions: a row without a guest posture is 403 for a guest whatever they
// hold, a row with one admits the right holder and injects the scope.
func TestGuardedMount_GuestGate(t *testing.T) {
	t.Parallel()
	tenantID, campaignID := uuid.New(), uuid.New()
	authn := fakeAuthN{users: map[string]storage.User{validToken: operator()}}

	var sawGuest bool
	var sawCampaign uuid.UUID
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g, ok := auth.Guest(r.Context())
		sawGuest, sawCampaign = ok, g.CampaignID
		w.WriteHeader(http.StatusOK)
	})
	rows := []auth.GuardedMount{
		{Pattern: "GET /gm-only", Tenant: auth.TenantRequired, Handler: inner},
		{Pattern: "GET /transcript", Tenant: auth.TenantRequired, Handler: inner, Guest: auth.GuestWith(auth.RightTranscriptsRead)},
		{Pattern: "POST /import", Tenant: auth.TenantNone, Handler: inner},
	}
	serve := func(ws auth.WorkspaceResolver, method, path string) int {
		t.Helper()
		guarded := auth.MustGuardMounts(auth.NewPolicy(authn, nil).WithWorkspaces(ws), rows)
		mux := http.NewServeMux()
		for _, g := range guarded {
			mux.Handle(g.Pattern, g.Handler)
		}
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Cookie", auth.SessionCookieName+"="+validToken+"; "+auth.CSRFCookieName+"="+csrfValue)
		req.Header.Set("X-CSRF-Token", csrfValue)
		rec := httptest.NewRecorder()
		sawGuest, sawCampaign = false, uuid.Nil
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusForbidden {
			if got := strings.TrimSpace(rec.Body.String()); got != auth.DenyGuest.Message() {
				t.Errorf("%s %s: 403 body = %q, want the guest denial text", method, path, got)
			}
		}
		return rec.Code
	}

	everything := guestOf(tenantID, campaignID, allGuestRights...)
	if code := serve(everything, http.MethodGet, "/gm-only"); code != http.StatusForbidden {
		t.Errorf("guest with every right on a mount without a posture: %d, want 403", code)
	}
	if code := serve(everything, http.MethodPost, "/import"); code != http.StatusForbidden {
		t.Errorf("guest on the TenantNone mount without a posture: %d, want 403", code)
	}
	if code := serve(everything, http.MethodGet, "/transcript"); code != http.StatusOK {
		t.Errorf("guest with transcripts.read on its mount: %d, want 200", code)
	} else if !sawGuest || sawCampaign != campaignID {
		t.Errorf("handler saw guest=%v campaign=%s, want the scope injected", sawGuest, sawCampaign)
	}
	wikiOnly := guestOf(tenantID, campaignID, auth.RightWikiEdit, auth.RightPlanningUse, auth.RightHighlightsRead, auth.RightMapsEdit)
	if code := serve(wikiOnly, http.MethodGet, "/transcript"); code != http.StatusForbidden {
		t.Errorf("guest WITHOUT transcripts.read on the transcript mount: %d, want 403", code)
	}

	operatorWS := &fakeWorkspaces{ws: storage.Workspace{TenantID: tenantID}}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/gm-only"}, {http.MethodGet, "/transcript"}, {http.MethodPost, "/import"},
	} {
		if code := serve(operatorWS, c.method, c.path); code != http.StatusOK {
			t.Errorf("operator on %s %s: %d, want 200", c.method, c.path, code)
		}
	}
}

// fakeWorkspaceStore records the grant requirement [auth.Workspaces] passes
// down.
type fakeWorkspaceStore struct {
	resolveNeedsGrant, ownNeedsGrant bool
}

func (f *fakeWorkspaceStore) ResolveWorkspace(_ context.Context, _ uuid.UUID, _ string, needsGrant bool) (storage.Workspace, error) {
	f.resolveNeedsGrant = needsGrant
	return storage.Workspace{}, nil
}

func (f *fakeWorkspaceStore) OwnTenantForUser(_ context.Context, _ uuid.UUID, needsGrant bool) (uuid.UUID, error) {
	f.ownNeedsGrant = needsGrant
	return uuid.Nil, nil
}

// TestWorkspaces_OwnTenantNeedsGrant pins who must hold an Access Grant to act
// in a Tenant they operate. Getting this wrong in the permissive direction
// lets a GM whose access was revoked — but who is someone's Campaign Guest —
// sign in as a guest and land back in their own Tenant.
func TestWorkspaces_OwnTenantNeedsGrant(t *testing.T) {
	t.Parallel()
	admins := auth.PlatformAdmins{Allowlist: auth.ParseOperatorAllowlist("42")}
	admin := storage.User{ID: uuid.New(), DiscordUserID: "42"}
	other := storage.User{ID: uuid.New(), DiscordUserID: "77"}
	dev := storage.User{ID: uuid.New(), DiscordUserID: storage.DevOperatorDiscordID}

	cases := []struct {
		name   string
		mode   auth.AdmissionMode
		admins auth.PlatformAdmins
		user   storage.User
		want   bool
	}{
		{"allowlist, Platform Admin", auth.AdmissionAllowlist, admins, admin, false},
		{"allowlist, anyone else", auth.AdmissionAllowlist, admins, other, true},
		{"open, anyone", auth.AdmissionOpen, admins, other, false},
		{"unset mode fails closed", "", admins, other, true},
		{"unset mode, Platform Admin", "", admins, admin, false},
		{"dev operator on a dev instance", auth.AdmissionAllowlist, auth.PlatformAdmins{Dev: true}, dev, false},
		{"dev operator row outside dev mode", auth.AdmissionAllowlist, admins, dev, true},
	}
	for _, tc := range cases {
		store := &fakeWorkspaceStore{}
		ws := auth.Workspaces{Store: store, Admins: tc.admins, Mode: tc.mode}
		if _, err := ws.ResolveWorkspace(context.Background(), tc.user, "tok"); err != nil {
			t.Fatalf("%s: ResolveWorkspace: %v", tc.name, err)
		}
		if _, err := ws.OwnTenant(context.Background(), tc.user); err != nil {
			t.Fatalf("%s: OwnTenant: %v", tc.name, err)
		}
		if store.resolveNeedsGrant != tc.want || store.ownNeedsGrant != tc.want {
			t.Errorf("%s: needsGrant resolve=%v own=%v, want %v",
				tc.name, store.resolveNeedsGrant, store.ownNeedsGrant, tc.want)
		}
	}
}
