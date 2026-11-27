package rpc_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeAdminStore is a scripted AdminStore that records every call. calls is
// what the gate tests read: a refused caller must leave it empty.
type fakeAdminStore struct {
	calls []string

	people      []storage.AccessPerson
	invitations []storage.Invitation
	includeDev  bool

	granted struct {
		id, note string
		keys     bool
		by       uuid.UUID
	}
	minted       storage.NewInvitation
	suspendedArg struct {
		id string
		on bool
	}
	keysArg struct {
		id uuid.UUID
		on bool
	}
	revokedInvitation uuid.UUID
	revokedAccess     string

	err error
}

func (f *fakeAdminStore) ListAccessPeople(_ context.Context, includeDev bool) ([]storage.AccessPerson, error) {
	f.calls = append(f.calls, "ListAccessPeople")
	f.includeDev = includeDev
	return f.people, f.err
}

func (f *fakeAdminStore) ListAccessInvitations(context.Context) ([]storage.Invitation, error) {
	f.calls = append(f.calls, "ListAccessInvitations")
	return f.invitations, f.err
}

func (f *fakeAdminStore) GrantAccess(_ context.Context, id, note string, keys bool, by uuid.UUID) (storage.AccessGrant, error) {
	f.calls = append(f.calls, "GrantAccess")
	f.granted.id, f.granted.note, f.granted.keys, f.granted.by = id, note, keys, by
	return storage.AccessGrant{DiscordUserID: id}, f.err
}

func (f *fakeAdminStore) RevokeAccess(_ context.Context, id string) error {
	f.calls = append(f.calls, "RevokeAccess")
	f.revokedAccess = id
	return f.err
}

func (f *fakeAdminStore) CreateInvitation(_ context.Context, n storage.NewInvitation) (storage.Invitation, error) {
	f.calls = append(f.calls, "CreateInvitation")
	f.minted = n
	if f.err != nil {
		return storage.Invitation{}, f.err
	}
	return storage.Invitation{
		ID: uuid.New(), Kind: n.Kind, Label: n.Label, PlatformKeys: n.PlatformKeys,
		DiscordUserID: n.DiscordUserID, MaxUses: n.MaxUses, ExpiresAt: n.ExpiresAt,
		CreatedBy: &n.CreatedBy, CreatedAt: adminNow,
	}, nil
}

func (f *fakeAdminStore) RevokeAccessInvitation(_ context.Context, id uuid.UUID) error {
	f.calls = append(f.calls, "RevokeAccessInvitation")
	f.revokedInvitation = id
	return f.err
}

func (f *fakeAdminStore) SetUserSuspended(_ context.Context, id string, on bool) error {
	f.calls = append(f.calls, "SetUserSuspended")
	f.suspendedArg.id, f.suspendedArg.on = id, on
	return f.err
}

func (f *fakeAdminStore) SetTenantPlatformKeys(_ context.Context, id uuid.UUID, on bool) error {
	f.calls = append(f.calls, "SetTenantPlatformKeys")
	f.keysArg.id, f.keysArg.on = id, on
	return f.err
}

var adminNow = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

const (
	adminSnowflake      = "100000000000000001"
	otherAdminSnowflake = "100000000000000002"
	gmSnowflakeAdminT   = "300000000000000001"
)

var (
	adminUser = storage.User{ID: uuid.New(), DiscordUserID: adminSnowflake, Name: "Admin Ada", Role: "operator"}
	plainUser = storage.User{ID: uuid.New(), DiscordUserID: gmSnowflakeAdminT, Name: "Rin", Role: "operator"}
)

func testAdmins() auth.PlatformAdmins {
	return auth.PlatformAdmins{Allowlist: auth.ParseOperatorAllowlist(adminSnowflake + "," + otherAdminSnowflake)}
}

func newAdminServer(store *fakeAdminStore, mode auth.AdmissionMode) *rpc.AdminServer {
	srv := rpc.NewAdminServer(store, testAdmins(), mode, slog.New(slog.DiscardHandler))
	srv.SetClockForTest(func() time.Time { return adminNow })
	return srv
}

// asAdmin is a request context carrying the signed-in Platform Admin, as the
// policy interceptor would inject it.
func asAdmin() context.Context { return auth.WithUser(context.Background(), adminUser) }

// adminAuthN resolves two session tokens: a Platform Admin's and an ordinary
// GM's.
type adminAuthN struct{}

func (adminAuthN) AuthenticateSession(_ context.Context, token string) (storage.User, error) {
	switch token {
	case "tok-admin":
		return adminUser, nil
	case "tok-gm":
		return plainUser, nil
	}
	return storage.User{}, storage.ErrNotFound
}

type adminTenants struct{}

func (adminTenants) TenantForUser(context.Context, uuid.UUID) (uuid.UUID, error) {
	return uuid.New(), nil
}

// TestAdminService_EveryProcedureRequiresPlatformAdmin walks the AdminService
// DESCRIPTOR — not a hand-kept list — and calls every procedure three ways
// through the production interceptor stack: with no session, as a signed-in
// GM who is not a Platform Admin, and as a Platform Admin. The first two must
// be refused before the store is touched; the third must get past the gate
// (the control that proves the refusals are about WHO is calling). A procedure
// added to the service later is picked up automatically, so it cannot ship
// without the requireAdmin call.
func TestAdminService_EveryProcedureRequiresPlatformAdmin(t *testing.T) {
	t.Parallel()
	methods := managementv1.File_glyphoxa_management_v1_management_proto.Services().ByName("AdminService").Methods()
	if methods.Len() < 7 {
		t.Fatalf("AdminService descriptor has %d methods, want at least the 7 of ADR-0065 — is the walk broken?", methods.Len())
	}

	store := &fakeAdminStore{}
	srv := newAdminServer(store, auth.AdmissionAllowlist)
	stack := auth.NewPolicy(adminAuthN{}, adminTenants{}).Stack()
	guarded := http.NewServeMux()
	guarded.Handle(srv.Handler(stack.HandlerOptions()...))
	// The same handlers with NO interceptor in front: the gate must hold on
	// its own, not only because the stack happens to reject first.
	bare := http.NewServeMux()
	bare.Handle(srv.Handler())

	call := func(mux *http.ServeMux, method, token string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/glyphoxa.management.v1.AdminService/"+method, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
			// The CSRF double-submit, so a state-changing call reaches the handler.
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "csrf"})
			req.Header.Set("X-CSRF-Token", "csrf")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	for i := range methods.Len() {
		name := string(methods.Get(i).Name())

		store.calls = nil
		if code, body := call(guarded, name, ""); code != http.StatusUnauthorized || !strings.Contains(body, "unauthenticated") {
			t.Errorf("%s with no session: %d %s, want 401 unauthenticated", name, code, body)
		}
		if code, body := call(bare, name, ""); code != http.StatusUnauthorized || !strings.Contains(body, "unauthenticated") {
			t.Errorf("%s with no session and no interceptor: %d %s, want 401 unauthenticated from the handler itself", name, code, body)
		}
		if code, body := call(guarded, name, "tok-gm"); code != http.StatusForbidden || !strings.Contains(body, "permission_denied") {
			t.Errorf("%s as a GM who is not a Platform Admin: %d %s, want 403 permission_denied", name, code, body)
		}
		if len(store.calls) != 0 {
			t.Errorf("%s: a refused caller reached the store: %v", name, store.calls)
		}

		if code, body := call(guarded, name, "tok-admin"); code == http.StatusUnauthorized || code == http.StatusForbidden {
			t.Errorf("%s as a Platform Admin: %d %s, want it past the gate", name, code, body)
		}
	}
}

// A user who merely claims the operator role, or whose Discord ID is a prefix
// of an admin's, is not a Platform Admin; the zero PlatformAdmins refuses
// everyone, including people the database thinks highly of.
func TestAdminService_GateIsTheEnvListOnly(t *testing.T) {
	t.Parallel()
	for name, u := range map[string]storage.User{
		"operator role":      {ID: uuid.New(), DiscordUserID: "999", Role: "operator"},
		"admin role string":  {ID: uuid.New(), DiscordUserID: "999", Role: "platform_admin"},
		"prefix of an admin": {ID: uuid.New(), DiscordUserID: adminSnowflake[:10]},
		"dev operator":       {ID: uuid.New(), DiscordUserID: storage.DevOperatorDiscordID},
		"empty discord id":   {ID: uuid.New()},
	} {
		store := &fakeAdminStore{}
		_, err := newAdminServer(store, auth.AdmissionAllowlist).GetAccessOverview(
			auth.WithUser(context.Background(), u), connect.NewRequest(&managementv1.GetAccessOverviewRequest{}))
		if connect.CodeOf(err) != connect.CodePermissionDenied || len(store.calls) != 0 {
			t.Errorf("%s: err = %v, store calls = %v; want PermissionDenied and an untouched store", name, err, store.calls)
		}
	}

	store := &fakeAdminStore{}
	unwired := rpc.NewAdminServer(store, auth.PlatformAdmins{}, auth.AdmissionAllowlist, nil)
	if _, err := unwired.GetAccessOverview(asAdmin(), connect.NewRequest(&managementv1.GetAccessOverviewRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("zero PlatformAdmins admitted a caller: %v", err)
	}

	// Dev mode: the synthetic dev operator IS the instance's Platform Admin.
	dev := rpc.NewAdminServer(store, auth.PlatformAdmins{Dev: true}, auth.AdmissionAllowlist, nil)
	devCtx := auth.WithUser(context.Background(), storage.User{ID: uuid.New(), DiscordUserID: storage.DevOperatorDiscordID})
	if _, err := dev.GetAccessOverview(devCtx, connect.NewRequest(&managementv1.GetAccessOverviewRequest{})); err != nil {
		t.Errorf("dev operator refused on a dev instance: %v", err)
	}
	if !store.includeDev {
		t.Error("a dev instance must list the dev operator (includeDev)")
	}
}

func wantCode(t *testing.T, what string, err error, want connect.Code) {
	t.Helper()
	if err == nil || connect.CodeOf(err) != want {
		t.Errorf("%s: err = %v, want %v", what, err, want)
	}
}

func TestAdminGrantAccess(t *testing.T) {
	t.Parallel()
	grant := func(store *fakeAdminStore, id, note string, keys bool) error {
		_, err := newAdminServer(store, auth.AdmissionAllowlist).GrantAccess(asAdmin(),
			connect.NewRequest(&managementv1.GrantAccessRequest{DiscordUserId: id, Note: note, PlatformKeys: keys}))
		return err
	}

	for name, id := range map[string]string{
		"empty":      "",
		"blank":      "   ",
		"a username": "rin_okabe",
		"quoted":     `"300000000000000001"`,
		"signed":     "-300000000000000001",
		"spaced":     "3000 0000",
		"too long":   strings.Repeat("9", 21),
	} {
		store := &fakeAdminStore{}
		wantCode(t, name, grant(store, id, "", false), connect.CodeInvalidArgument)
		if len(store.calls) != 0 {
			t.Errorf("%s: an invalid snowflake reached the store", name)
		}
	}

	store := &fakeAdminStore{}
	wantCode(t, "note too long", grant(store, gmSnowflakeAdminT, strings.Repeat("ü", 201), false), connect.CodeInvalidArgument)
	if err := grant(store, gmSnowflakeAdminT, strings.Repeat("ü", 200), false); err != nil {
		t.Errorf("a 200-character note was refused: %v", err)
	}

	// A Platform Admin is configured in the environment; granting them access
	// here would be a row that means nothing.
	for _, id := range []string{adminSnowflake, otherAdminSnowflake} {
		store := &fakeAdminStore{}
		wantCode(t, "grant to a Platform Admin", grant(store, id, "", true), connect.CodeFailedPrecondition)
		if len(store.calls) != 0 {
			t.Error("a grant for a Platform Admin reached the store")
		}
	}

	store = &fakeAdminStore{}
	if err := grant(store, "  "+gmSnowflakeAdminT+" ", "  plays Fridays ", true); err != nil {
		t.Fatalf("GrantAccess: %v", err)
	}
	if store.granted.id != gmSnowflakeAdminT || store.granted.note != "plays Fridays" || !store.granted.keys || store.granted.by != adminUser.ID {
		t.Errorf("store got %+v, want the trimmed id and note, the keys choice and the caller as grantor", store.granted)
	}

	wantCode(t, "store failure", grant(&fakeAdminStore{err: errAny}, gmSnowflakeAdminT, "", false), connect.CodeInternal)
}

func TestAdminRevokeAccess(t *testing.T) {
	t.Parallel()
	revoke := func(store *fakeAdminStore, id string) error {
		_, err := newAdminServer(store, auth.AdmissionAllowlist).RevokeAccess(asAdmin(),
			connect.NewRequest(&managementv1.RevokeAccessRequest{DiscordUserId: id}))
		return err
	}
	store := &fakeAdminStore{}
	wantCode(t, "not a snowflake", revoke(store, "rin"), connect.CodeInvalidArgument)
	wantCode(t, "a Platform Admin", revoke(store, otherAdminSnowflake), connect.CodeFailedPrecondition)
	wantCode(t, "the caller", revoke(store, adminSnowflake), connect.CodeFailedPrecondition)
	if len(store.calls) != 0 {
		t.Errorf("refused revokes reached the store: %v", store.calls)
	}
	wantCode(t, "no grant", revoke(&fakeAdminStore{err: storage.ErrNotFound}, gmSnowflakeAdminT), connect.CodeNotFound)
	wantCode(t, "store failure", revoke(&fakeAdminStore{err: errAny}, gmSnowflakeAdminT), connect.CodeInternal)

	if err := revoke(store, gmSnowflakeAdminT); err != nil || store.revokedAccess != gmSnowflakeAdminT {
		t.Errorf("RevokeAccess = %v, store revoked %q", err, store.revokedAccess)
	}
}

// Suspension works in every Admission Mode, never on a Platform Admin, and
// never on the caller's own row.
func TestAdminSetUserSuspended(t *testing.T) {
	t.Parallel()
	for _, mode := range []auth.AdmissionMode{auth.AdmissionAllowlist, auth.AdmissionOpen} {
		suspend := func(store *fakeAdminStore, id string, on bool) error {
			_, err := newAdminServer(store, mode).SetUserSuspended(asAdmin(),
				connect.NewRequest(&managementv1.SetUserSuspendedRequest{DiscordUserId: id, Suspended: on}))
			return err
		}
		store := &fakeAdminStore{}
		wantCode(t, "not a snowflake", suspend(store, storage.DevOperatorDiscordID, true), connect.CodeInvalidArgument)
		wantCode(t, "the caller's own row", suspend(store, adminSnowflake, true), connect.CodeFailedPrecondition)
		wantCode(t, "another Platform Admin", suspend(store, otherAdminSnowflake, true), connect.CodeFailedPrecondition)
		// Not even "restore": the env list is the only thing that speaks for an admin.
		wantCode(t, "restoring a Platform Admin", suspend(store, otherAdminSnowflake, false), connect.CodeFailedPrecondition)
		if len(store.calls) != 0 {
			t.Errorf("%s: refused suspensions reached the store: %v", mode, store.calls)
		}
		wantCode(t, "never signed in", suspend(&fakeAdminStore{err: storage.ErrNotFound}, gmSnowflakeAdminT, true), connect.CodeNotFound)

		for _, on := range []bool{true, false} {
			if err := suspend(store, gmSnowflakeAdminT, on); err != nil {
				t.Fatalf("%s: SetUserSuspended(%v): %v", mode, on, err)
			}
			if store.suspendedArg.id != gmSnowflakeAdminT || store.suspendedArg.on != on {
				t.Errorf("%s: store got %+v, want (%s, %v)", mode, store.suspendedArg, gmSnowflakeAdminT, on)
			}
		}
	}
}

func TestAdminCreateAccessInvitation(t *testing.T) {
	t.Parallel()
	create := func(store *fakeAdminStore, req *managementv1.CreateAccessInvitationRequest) (*managementv1.CreateAccessInvitationResponse, error) {
		resp, err := newAdminServer(store, auth.AdmissionAllowlist).CreateAccessInvitation(asAdmin(), connect.NewRequest(req))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}

	for name, req := range map[string]*managementv1.CreateAccessInvitationRequest{
		"label too long":    {Label: strings.Repeat("ü", 201)},
		"negative max uses": {MaxUses: -1},
		"negative expiry":   {ExpiresInDays: -1},
		"expiry past a year": {
			ExpiresInDays: 366,
		},
		"pinned to a username": {DiscordUserId: "rin_okabe"},
	} {
		store := &fakeAdminStore{}
		_, err := create(store, req)
		wantCode(t, name, err, connect.CodeInvalidArgument)
		if len(store.calls) != 0 {
			t.Errorf("%s: an invalid request reached the store", name)
		}
	}

	// The open-ended link: no cap, no expiry, no pin.
	store := &fakeAdminStore{}
	resp, err := create(store, &managementv1.CreateAccessInvitationRequest{})
	if err != nil {
		t.Fatalf("CreateAccessInvitation(defaults): %v", err)
	}
	if store.minted.Kind != storage.InvitationAccess || store.minted.MaxUses != nil || store.minted.ExpiresAt != nil ||
		store.minted.DiscordUserID != "" || store.minted.PlatformKeys || store.minted.CreatedBy != adminUser.ID ||
		store.minted.CampaignID != nil || len(store.minted.Rights) != 0 {
		t.Errorf("default invitation = %+v, want an unlimited, non-expiring ACCESS link minted by the caller", store.minted)
	}
	// The token: 256 bits from auth.NewToken, returned once, and the same
	// value the store was handed to digest.
	if len(resp.GetToken()) != 43 || resp.GetToken() != store.minted.Token {
		t.Errorf("token = %q (len %d), store got %q; want one 43-character token", resp.GetToken(), len(resp.GetToken()), store.minted.Token)
	}
	again, err := create(&fakeAdminStore{}, &managementv1.CreateAccessInvitationRequest{})
	if err != nil || again.GetToken() == resp.GetToken() {
		t.Errorf("two invitations share a token (%v)", err)
	}
	inv := resp.GetInvitation()
	if inv.GetKind() != managementv1.InvitationKind_INVITATION_KIND_ACCESS || !inv.GetLive() || inv.GetMaxUses() != 0 ||
		inv.GetExpiresAt() != nil || inv.GetRevokedAt() != nil || inv.GetId() == "" {
		t.Errorf("returned invitation = %+v", inv)
	}

	// Every option set, at the boundaries.
	store = &fakeAdminStore{}
	resp, err = create(store, &managementv1.CreateAccessInvitationRequest{
		Label: "  " + strings.Repeat("ü", 200) + " ", MaxUses: 5, ExpiresInDays: 365,
		PlatformKeys: true, DiscordUserId: " " + gmSnowflakeAdminT + " ",
	})
	if err != nil {
		t.Fatalf("CreateAccessInvitation(full): %v", err)
	}
	wantExp := adminNow.Add(365 * 24 * time.Hour)
	if store.minted.Label != strings.Repeat("ü", 200) || store.minted.MaxUses == nil || *store.minted.MaxUses != 5 ||
		store.minted.ExpiresAt == nil || !store.minted.ExpiresAt.Equal(wantExp) ||
		!store.minted.PlatformKeys || store.minted.DiscordUserID != gmSnowflakeAdminT {
		t.Errorf("full invitation = %+v", store.minted)
	}
	inv = resp.GetInvitation()
	if inv.GetMaxUses() != 5 || !inv.GetExpiresAt().AsTime().Equal(wantExp) || !inv.GetPlatformKeys() ||
		inv.GetDiscordUserId() != gmSnowflakeAdminT || !inv.GetLive() {
		t.Errorf("returned full invitation = %+v", inv)
	}

	_, err = create(&fakeAdminStore{err: errAny}, &managementv1.CreateAccessInvitationRequest{})
	wantCode(t, "store failure", err, connect.CodeInternal)
}

func TestAdminRevokeAccessInvitation(t *testing.T) {
	t.Parallel()
	revoke := func(store *fakeAdminStore, id string) error {
		_, err := newAdminServer(store, auth.AdmissionAllowlist).RevokeAccessInvitation(asAdmin(),
			connect.NewRequest(&managementv1.RevokeAccessInvitationRequest{Id: id}))
		return err
	}
	store := &fakeAdminStore{}
	wantCode(t, "not a uuid", revoke(store, "nope"), connect.CodeInvalidArgument)
	if len(store.calls) != 0 {
		t.Error("an invalid id reached the store")
	}
	id := uuid.New()
	wantCode(t, "unknown", revoke(&fakeAdminStore{err: storage.ErrNotFound}, id.String()), connect.CodeNotFound)
	if err := revoke(store, id.String()); err != nil || store.revokedInvitation != id {
		t.Errorf("RevokeAccessInvitation = %v, store revoked %s", err, store.revokedInvitation)
	}
}

// The Platform Keys switch is an allowlist-mode control; in open mode the Plan
// decides and the switch must refuse rather than pretend.
func TestAdminSetTenantPlatformKeys(t *testing.T) {
	t.Parallel()
	set := func(store *fakeAdminStore, mode auth.AdmissionMode, id string, on bool) error {
		_, err := newAdminServer(store, mode).SetTenantPlatformKeys(asAdmin(),
			connect.NewRequest(&managementv1.SetTenantPlatformKeysRequest{TenantId: id, Enabled: on}))
		return err
	}
	tenant := uuid.New()

	store := &fakeAdminStore{}
	wantCode(t, "open mode", set(store, auth.AdmissionOpen, tenant.String(), true), connect.CodeFailedPrecondition)
	wantCode(t, "not a uuid", set(store, auth.AdmissionAllowlist, "nope", true), connect.CodeInvalidArgument)
	if len(store.calls) != 0 {
		t.Errorf("refused switches reached the store: %v", store.calls)
	}
	wantCode(t, "unknown tenant", set(&fakeAdminStore{err: storage.ErrNotFound}, auth.AdmissionAllowlist, tenant.String(), true), connect.CodeNotFound)

	for _, on := range []bool{true, false} {
		if err := set(store, auth.AdmissionAllowlist, tenant.String(), on); err != nil {
			t.Fatalf("SetTenantPlatformKeys(%v): %v", on, err)
		}
		if store.keysArg.id != tenant || store.keysArg.on != on {
			t.Errorf("store got %+v, want (%s, %v)", store.keysArg, tenant, on)
		}
	}
}

// overview runs GetAccessOverview and indexes the people by Discord User ID.
func overview(t *testing.T, store *fakeAdminStore, mode auth.AdmissionMode) (*managementv1.GetAccessOverviewResponse, map[string]*managementv1.AccessPerson) {
	t.Helper()
	resp, err := newAdminServer(store, mode).GetAccessOverview(asAdmin(), connect.NewRequest(&managementv1.GetAccessOverviewRequest{}))
	if err != nil {
		t.Fatalf("GetAccessOverview: %v", err)
	}
	byID := map[string]*managementv1.AccessPerson{}
	for _, p := range resp.Msg.GetPeople() {
		if _, dup := byID[p.GetDiscordUserId()+"/"+p.GetTenantId()]; dup {
			t.Errorf("%s listed twice for one tenant", p.GetDiscordUserId())
		}
		byID[p.GetDiscordUserId()+"/"+p.GetTenantId()] = p
		byID[p.GetDiscordUserId()] = p
	}
	return resp.Msg, byID
}

func TestAdminGetAccessOverview(t *testing.T) {
	t.Parallel()
	joined := adminNow.Add(-48 * time.Hour)
	seen := adminNow.Add(-time.Hour)
	suspendedAt := adminNow.Add(-time.Minute)
	adminTenant, gmTenant, signupTenant := uuid.New(), uuid.New(), uuid.New()
	one := 1
	past := adminNow.Add(-time.Hour)

	people := []storage.AccessPerson{
		// The caller: on the env list, signed in, operates a Tenant. A grant
		// row (which the API refuses to create) must not demote the status.
		{DiscordUserID: adminSnowflake, Known: true, Name: "Admin Ada", Avatar: "https://cdn/a.png", JoinedAt: &joined, LastSeenAt: &seen,
			HasGrant: true, TenantID: &adminTenant, TenantName: "Ada's Table", TenantPlatformKeys: true, CampaignCount: 3},
		// Granted and signed in: the TENANT's switch is what counts, not the grant's.
		{DiscordUserID: "300000000000000001", Known: true, Name: "Rin", JoinedAt: &joined,
			HasGrant: true, GrantNote: "plays Fridays", GrantPlatformKeys: true, InvitationLabel: "Con crew",
			TenantID: &gmTenant, TenantName: "Rin's Table", TenantPlatformKeys: false, CampaignCount: 1, GuestCampaignCount: 2},
		// Granted through a link, never signed in: the grant's choice and the
		// link's label stand in.
		{DiscordUserID: "300000000000000002", HasGrant: true, GrantPlatformKeys: true, InvitationLabel: "Con crew"},
		// Operates a Tenant with no grant: a signup in open mode, nobody on allowlist.
		{DiscordUserID: "300000000000000003", Known: true, Name: "Mo", TenantID: &signupTenant, TenantName: "Mo's Table",
			TenantPlatformKeys: true, SuspendedAt: &suspendedAt},
		// No Tenant, guest of two Campaigns.
		{DiscordUserID: "300000000000000004", Known: true, Name: "Gil", GuestCampaignCount: 2},
		// Signed in once, nothing else (e.g. access revoked before founding).
		{DiscordUserID: "300000000000000005", Known: true, Name: "Nobody"},
	}
	invitations := []storage.Invitation{
		{ID: uuid.New(), Kind: storage.InvitationAccess, Label: "Con crew", PlatformKeys: true, MaxUses: &one, Uses: 1, CreatedAt: joined},
		{ID: uuid.New(), Kind: storage.InvitationAccess, DiscordUserID: "300000000000000009", ExpiresAt: &past, CreatedAt: joined},
		{ID: uuid.New(), Kind: storage.InvitationAccess, RevokedAt: &past, CreatedAt: joined},
		{ID: uuid.New(), Kind: storage.InvitationAccess, CreatedAt: joined},
	}

	store := &fakeAdminStore{people: people, invitations: invitations}
	resp, by := overview(t, store, auth.AdmissionAllowlist)
	if store.includeDev {
		t.Error("a production instance listed the dev operator")
	}
	if resp.GetAdmissionMode() != managementv1.AdmissionMode_ADMISSION_MODE_ALLOWLIST {
		t.Errorf("admission_mode = %v, want ALLOWLIST", resp.GetAdmissionMode())
	}
	// Six stored rows plus the env-list admin who has never signed in.
	if len(resp.GetPeople()) != 7 {
		t.Fatalf("people = %d rows, want 7", len(resp.GetPeople()))
	}

	wantStatus := map[string]managementv1.AccessStatus{
		adminSnowflake:       managementv1.AccessStatus_ACCESS_STATUS_PLATFORM_ADMIN,
		otherAdminSnowflake:  managementv1.AccessStatus_ACCESS_STATUS_PLATFORM_ADMIN,
		"300000000000000001": managementv1.AccessStatus_ACCESS_STATUS_GRANTED,
		"300000000000000002": managementv1.AccessStatus_ACCESS_STATUS_GRANTED,
		"300000000000000003": managementv1.AccessStatus_ACCESS_STATUS_NONE,
		"300000000000000004": managementv1.AccessStatus_ACCESS_STATUS_GUEST,
		"300000000000000005": managementv1.AccessStatus_ACCESS_STATUS_NONE,
	}
	for id, want := range wantStatus {
		p := by[id]
		if p == nil {
			t.Fatalf("%s missing from the overview", id)
		}
		if p.GetStatus() != want {
			t.Errorf("%s: status = %v, want %v", id, p.GetStatus(), want)
		}
		if p.GetIsSelf() != (id == adminSnowflake) {
			t.Errorf("%s: is_self = %v", id, p.GetIsSelf())
		}
	}

	self := by[adminSnowflake]
	if self.GetName() != "Admin Ada" || self.GetAvatar() != "https://cdn/a.png" || !self.GetHasSignedIn() ||
		self.GetTenantId() != adminTenant.String() || self.GetTenantName() != "Ada's Table" || self.GetCampaignCount() != 3 ||
		!self.GetPlatformKeys() || !self.GetJoinedAt().AsTime().Equal(joined) || !self.GetLastSeenAt().AsTime().Equal(seen) {
		t.Errorf("self row = %+v", self)
	}

	// The admin who has never signed in exists only in the environment.
	ghost := by[otherAdminSnowflake]
	if ghost.GetHasSignedIn() || ghost.GetName() != "" || ghost.GetTenantId() != "" || ghost.GetSuspended() ||
		ghost.GetJoinedAt() != nil || ghost.GetLastSeenAt() != nil {
		t.Errorf("never-signed-in admin row = %+v", ghost)
	}

	rin := by["300000000000000001"]
	if rin.GetNote() != "plays Fridays" || rin.GetPlatformKeys() || rin.GetGuestCampaignCount() != 2 || rin.GetLastSeenAt() != nil {
		t.Errorf("granted+signed-in row = %+v, want the grant's note and the TENANT's (off) switch", rin)
	}
	unused := by["300000000000000002"]
	if unused.GetHasSignedIn() || unused.GetNote() != "Con crew" || !unused.GetPlatformKeys() || unused.GetTenantId() != "" {
		t.Errorf("unused grant row = %+v, want the link's label as the note and the grant's (on) switch", unused)
	}
	if mo := by["300000000000000003"]; !mo.GetSuspended() || !mo.GetPlatformKeys() {
		t.Errorf("suspended operator row = %+v", mo)
	}
	if nobody := by["300000000000000005"]; nobody.GetPlatformKeys() || nobody.GetSuspended() {
		t.Errorf("bare row = %+v, want no platform keys without a Tenant or a grant", nobody)
	}

	gotInv := resp.GetInvitations()
	if len(gotInv) != 4 {
		t.Fatalf("invitations = %d, want 4", len(gotInv))
	}
	for i, wantLive := range []bool{false, false, false, true} {
		if gotInv[i].GetId() != invitations[i].ID.String() || gotInv[i].GetLive() != wantLive ||
			gotInv[i].GetKind() != managementv1.InvitationKind_INVITATION_KIND_ACCESS {
			t.Errorf("invitation %d = %+v, want id %s live=%v (store order kept)", i, gotInv[i], invitations[i].ID, wantLive)
		}
	}
	if gotInv[0].GetMaxUses() != 1 || gotInv[0].GetUses() != 1 || gotInv[0].GetLabel() != "Con crew" || !gotInv[0].GetPlatformKeys() {
		t.Errorf("spent invitation = %+v", gotInv[0])
	}
	if gotInv[1].GetDiscordUserId() != "300000000000000009" || !gotInv[1].GetExpiresAt().AsTime().Equal(past) {
		t.Errorf("expired pinned invitation = %+v", gotInv[1])
	}
	if gotInv[2].GetRevokedAt() == nil || gotInv[3].GetMaxUses() != 0 || gotInv[3].GetExpiresAt() != nil {
		t.Errorf("revoked / open invitations = %+v / %+v", gotInv[2], gotInv[3])
	}

	// In open mode an operator without a grant is a self-signup; everything
	// else derives the same.
	respOpen, byOpen := overview(t, &fakeAdminStore{people: people}, auth.AdmissionOpen)
	if respOpen.GetAdmissionMode() != managementv1.AdmissionMode_ADMISSION_MODE_OPEN {
		t.Errorf("admission_mode = %v, want OPEN", respOpen.GetAdmissionMode())
	}
	wantStatus["300000000000000003"] = managementv1.AccessStatus_ACCESS_STATUS_SIGNUP
	for id, want := range wantStatus {
		if got := byOpen[id].GetStatus(); got != want {
			t.Errorf("open mode, %s: status = %v, want %v", id, got, want)
		}
	}

	_, err := newAdminServer(&fakeAdminStore{err: errors.New("db down")}, auth.AdmissionAllowlist).
		GetAccessOverview(asAdmin(), connect.NewRequest(&managementv1.GetAccessOverviewRequest{}))
	wantCode(t, "store failure", err, connect.CodeInternal)
	if err != nil && strings.Contains(err.Error(), "db down") {
		t.Errorf("the store error leaked to the client: %v", err)
	}
}
