//go:build integration

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/transcript"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// The Campaign Guest gate end to end (ADR-0065): the REAL mount table —
// managementMounts, exactly as runWeb builds it — over a real Postgres, driven
// with real session cookies. Everything here is asserted on what a browser
// would receive, so it covers the wiring the unit tests cannot: that the
// composition root actually installs the workspace resolver, that every
// service and every plain mount sits behind the gate, and that a guest of one
// Campaign cannot reach another Campaign of the same host Tenant through any
// id they might guess.

const (
	guestGateHost  = "500000000000000001"
	guestGateGuest = "500000000000000002"
	guestGateCSRF  = "guest-gate-csrf"
)

// noLiveSessions is the relay's session registry with nothing live.
type noLiveSessions struct{}

func (noLiveSessions) Resolve(uuid.UUID) (storage.VoiceSession, bool) {
	return storage.VoiceSession{}, false
}

// guestGateWorld is the fixture: a host GM (a Platform Admin, so their own
// Tenant is a workspace on this allowlist deployment) with campaigns alpha —
// the one shared with the guest — and beta, created later so every "most
// recent" resolution points at it.
type guestGateWorld struct {
	t      *testing.T
	store  *storage.Store
	srv    *httptest.Server
	tenant uuid.UUID
	alpha  uuid.UUID
	beta   uuid.UUID
	guest  storage.User
	// Ids of things in BETA — the campaign the guest was never given.
	betaNode, betaMap, betaSession, betaHighlight, betaThread uuid.UUID
	// Ids of things in ALPHA.
	alphaPublic, alphaSecret, alphaSession, alphaPromoted, alphaCandidate uuid.UUID
}

func newGuestGateWorld(t *testing.T) *guestGateWorld {
	t.Helper()
	ctx := context.Background()
	store := bootMigratedStore(t)
	log := slog.New(slog.DiscardHandler)
	w := &guestGateWorld{t: t, store: store}

	host, err := store.ProvisionSignup(ctx, storage.SignupParams{
		User:       storage.UpsertUserParams{DiscordUserID: guestGateHost, Name: "GM Gil"},
		TenantName: "Gil's Table",
		Session:    storage.NewSession{Token: "tok-host", ExpiresAt: time.Now().Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("provision host: %v", err)
	}
	w.tenant = host.Tenant.ID
	mkCampaign := func(name string) uuid.UUID {
		id, err := store.CreateCampaign(ctx, storage.NewCampaign{TenantID: w.tenant, Name: name, System: "dnd5e", Language: "en"})
		if err != nil {
			t.Fatalf("create campaign %s: %v", name, err)
		}
		return id
	}
	w.alpha = mkCampaign("Alpha")
	time.Sleep(5 * time.Millisecond)
	w.beta = mkCampaign("Beta")

	node := func(campaign uuid.UUID, name string, private bool) uuid.UUID {
		n, err := store.CreateNode(ctx, storage.NewKGNode{CampaignID: campaign, Type: storage.KGNodeNPC, Name: name, GMPrivate: private})
		if err != nil {
			t.Fatalf("create node %s: %v", name, err)
		}
		return n.ID
	}
	w.alphaPublic = node(w.alpha, "Bart the innkeeper", false)
	w.alphaSecret = node(w.alpha, "The cult leader", true)
	w.betaNode = node(w.beta, "Beta's innkeeper", false)

	m, err := store.CreateMap(ctx, storage.NewCampaignMap{CampaignID: w.beta, Name: "Beta's map", BlobKey: "k", WidthPx: 10, HeightPx: 10})
	if err != nil {
		t.Fatalf("create map: %v", err)
	}
	w.betaMap = m.ID

	voice := func(campaign uuid.UUID) uuid.UUID {
		vs, err := store.CreateVoiceSession(ctx, campaign)
		if err != nil {
			t.Fatalf("create voice session: %v", err)
		}
		return vs.ID
	}
	w.alphaSession, w.betaSession = voice(w.alpha), voice(w.beta)
	highlight := func(campaign, sess uuid.UUID, status string) uuid.UUID {
		id := uuid.New()
		if err := store.CreateHighlight(ctx, storage.Highlight{
			ID: id, TenantID: w.tenant, VoiceSessionID: sess, CampaignID: campaign, Status: status,
			StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now(), Excerpt: "nat 20", ClipKey: "clip-" + id.String(),
		}); err != nil {
			t.Fatalf("create highlight: %v", err)
		}
		return id
	}
	w.alphaPromoted = highlight(w.alpha, w.alphaSession, storage.HighlightPromoted)
	w.alphaCandidate = highlight(w.alpha, w.alphaSession, storage.HighlightCandidate)
	w.betaHighlight = highlight(w.beta, w.betaSession, storage.HighlightPromoted)
	th, err := store.CreatePlanningThreadBy(ctx, w.beta, "the GM's beta prep", host.User.ID)
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	w.betaThread = th.ID

	// The guest: added by snowflake, then signed in, acting in alpha.
	if _, err := store.AddCampaignGuest(ctx, w.alpha, guestGateGuest, nil, host.User.ID); err != nil {
		t.Fatalf("add guest: %v", err)
	}
	if w.guest, err = store.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: guestGateGuest, Name: "Mika"}); err != nil {
		t.Fatalf("upsert guest: %v", err)
	}
	if _, err := store.CreateSession(ctx, storage.NewSession{
		UserID: w.guest.ID, Token: "tok-guest", ExpiresAt: time.Now().Add(time.Hour), GuestCampaignID: &w.alpha,
	}); err != nil {
		t.Fatalf("create guest session: %v", err)
	}

	// The relay as runWeb wires it: both ownership scopes installed.
	relay := transcript.NewRelay(voiceevent.NewBus(), noLiveSessions{}, store, log)
	relay.SetTenantScope(store.VoiceSessionInTenant)
	relay.SetCampaignScope(store.VoiceSessionInCampaign)

	sessions := session.NewIntentControl(store, log, session.IntentControlConfig{})
	mounts := managementMounts(store, nil, nil, nil, log,
		sessions, relay, nil, nil, nil, nil, nil, nil, nil, "", auth.AdmissionAllowlist,
		auth.PlatformAdmins{Allowlist: auth.ParseOperatorAllowlist(guestGateHost)}, "", nil,
		newOffSessionMetering(auth.AdmissionAllowlist, store, log), llmbuild.DeploymentDefault{})
	mux := http.NewServeMux()
	for _, m := range mounts {
		mux.Handle(m.Path, m.Handler)
	}
	w.srv = httptest.NewServer(mux)
	t.Cleanup(w.srv.Close)
	return w
}

// grant replaces the guest's rights on alpha.
func (w *guestGateWorld) grant(rights ...auth.GuestRight) {
	w.t.Helper()
	stored := auth.GuestRightStrings(auth.NormalizeGuestRights(rights))
	if _, err := w.store.UpdateCampaignGuest(context.Background(), w.alpha, w.guest.ID, stored); err != nil {
		w.t.Fatalf("update guest rights: %v", err)
	}
}

func (w *guestGateWorld) do(token, method, path, body string) (int, string) {
	w.t.Helper()
	req, err := http.NewRequest(method, w.srv.URL+path, strings.NewReader(body))
	if err != nil {
		w.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", auth.SessionCookieName+"="+token+"; "+auth.CSRFCookieName+"="+guestGateCSRF)
	req.Header.Set("X-CSRF-Token", guestGateCSRF)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// rpc posts a unary Connect-JSON call.
func (w *guestGateWorld) rpc(token, procedure, body string) (int, string) {
	return w.do(token, http.MethodPost, "/api"+procedure, body)
}

// refusedByGate reports whether a unary response is the guest gate's denial.
func refusedByGate(status int, body string) bool {
	return status == http.StatusForbidden && strings.Contains(body, `"permission_denied"`) &&
		strings.Contains(body, auth.DenyGuest.Message())
}

// unaryProcedures walks the generated descriptor for every management
// procedure, split into unary and streaming.
func unaryProcedures(t *testing.T) (unary, streaming []string) {
	t.Helper()
	services := managementv1.File_glyphoxa_management_v1_management_proto.Services()
	for i := 0; i < services.Len(); i++ {
		svc := services.Get(i)
		for j := 0; j < svc.Methods().Len(); j++ {
			m := svc.Methods().Get(j)
			proc := "/" + string(svc.FullName()) + "/" + string(m.Name())
			if m.IsStreamingServer() || m.IsStreamingClient() {
				streaming = append(streaming, proc)
			} else {
				unary = append(unary, proc)
			}
		}
	}
	if len(unary) < 100 {
		t.Fatalf("descriptor walk found only %d unary procedures", len(unary))
	}
	return unary, streaming
}

// mountURL turns a plain-mount pattern into a concrete method + path.
func mountURL(pattern string, id uuid.UUID) (method, path string) {
	method, path, _ = strings.Cut(pattern, " ")
	return method, strings.ReplaceAll(path, "{id}", id.String())
}

// TestGuestGate_EndToEnd_DefaultDeny: on the real mount table a guest is
// refused on EVERY GM-only procedure and on EVERY plain mount without a guest
// posture, even holding every Guest Right — and with no rights at all, on
// everything that needs one.
func TestGuestGate_EndToEnd_DefaultDeny(t *testing.T) {
	w := newGuestGateWorld(t)
	unary, streaming := unaryProcedures(t)
	everyRight := auth.GuestRights()

	t.Run("holding every right: every GM-only procedure is refused", func(t *testing.T) {
		w.grant(everyRight...)
		refused := 0
		for _, proc := range unary {
			if auth.GuestProcedurePosture(proc).Reachable() {
				continue
			}
			status, body := w.rpc("tok-guest", proc, `{}`)
			if !refusedByGate(status, body) {
				t.Errorf("%s as a guest holding every right: %d %s; want 403 permission_denied from the guest gate", proc, status, body)
			}
			refused++
		}
		if refused < 60 {
			t.Fatalf("only %d GM-only procedures were exercised — the walk or the table is wrong", refused)
		}
		// The streaming procedures are all guest-reachable today; if one ever
		// is not, this test must learn to drive it.
		for _, proc := range streaming {
			if !auth.GuestProcedurePosture(proc).Reachable() {
				t.Errorf("streaming procedure %s is GM-only and not exercised here", proc)
			}
		}
	})

	t.Run("holding every right: every mount without a guest posture is refused", func(t *testing.T) {
		w.grant(everyRight...)
		closed := 0
		for pattern := range plainMountPolicy {
			if plainMountGuestPolicy[pattern].Reachable() {
				continue
			}
			method, path := mountURL(pattern, w.alpha)
			status, body := w.do("tok-guest", method, path, "")
			if status != http.StatusForbidden || !strings.Contains(body, auth.DenyGuest.Message()) {
				t.Errorf("%s as a guest holding every right: %d %q; want 403 from the guest gate", pattern, status, body)
			}
			closed++
		}
		if closed != 3 {
			t.Errorf("%d mounts are closed to guests, want 3 (the SSE relay, the bundle export and import)", closed)
		}
	})

	t.Run("holding no rights: everything that needs a right is refused", func(t *testing.T) {
		w.grant()
		for _, proc := range unary {
			posture := auth.GuestProcedurePosture(proc)
			if posture.Reachable() && len(posture.Rights()) == 0 {
				continue // open to every guest
			}
			status, body := w.rpc("tok-guest", proc, `{}`)
			if !refusedByGate(status, body) {
				t.Errorf("%s as a guest with no rights: %d %s; want the guest gate's 403", proc, status, body)
			}
		}
		for pattern := range plainMountPolicy {
			method, path := mountURL(pattern, w.alpha)
			if status, _ := w.do("tok-guest", method, path, ""); status != http.StatusForbidden {
				t.Errorf("%s as a guest with no rights: %d, want 403", pattern, status)
			}
		}
		// The one streaming procedure, through a real client.
		chat := managementv1connect.NewChatServiceClient(http.DefaultClient, w.srv.URL+"/api", connect.WithProtoJSON())
		req := connect.NewRequest(&managementv1.PlanningExchangeRequest{CampaignId: w.alpha.String(), ThreadId: uuid.NewString(), Message: "hi"})
		req.Header().Set("Cookie", auth.SessionCookieName+"=tok-guest; "+auth.CSRFCookieName+"="+guestGateCSRF)
		req.Header().Set("X-CSRF-Token", guestGateCSRF)
		stream, err := chat.PlanningExchange(context.Background(), req)
		if err == nil {
			for stream.Receive() {
			}
			err = stream.Err()
		}
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("PlanningExchange as a guest with no rights: err = %v, want CodePermissionDenied", err)
		}
	})

	t.Run("the host GM is refused nothing by the guest gate", func(t *testing.T) {
		if status, _ := w.rpc("tok-host", managementv1connect.CampaignServiceListCampaignsProcedure, `{}`); status != http.StatusOK {
			t.Errorf("ListCampaigns as the host GM: %d, want 200", status)
		}
		for _, proc := range unary {
			if proc == managementv1connect.AuthServiceLogoutProcedure {
				continue // it would end the session the rest of the walk uses
			}
			if status, body := w.rpc("tok-host", proc, `{}`); refusedByGate(status, body) {
				t.Errorf("%s as the host GM was refused by the guest gate: %s", proc, body)
			}
		}
	})
}

// TestGuestGate_EndToEnd_OneCampaignOnly: what a guest DOES reach is their one
// Campaign. Every by-id path is tried with an id from the host Tenant's other
// Campaign, and every one answers as if that thing did not exist.
func TestGuestGate_EndToEnd_OneCampaignOnly(t *testing.T) {
	w := newGuestGateWorld(t)
	w.grant(auth.GuestRights()...)
	ok := func(procedure, body string) map[string]any {
		t.Helper()
		status, raw := w.rpc("tok-guest", procedure, body)
		if status != http.StatusOK {
			t.Fatalf("%s: %d %s; want 200", procedure, status, raw)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("%s: decode %q: %v", procedure, raw, err)
		}
		return out
	}

	// The shell: a guest of alpha, in the host tenant, with no tenant of their own.
	me := ok(managementv1connect.AuthServiceGetCurrentUserProcedure, `{}`)
	guest, _ := me["guest"].(map[string]any)
	if guest == nil || guest["campaignId"] != w.alpha.String() || guest["campaignName"] != "Alpha" || guest["tenantName"] != "Gil's Table" {
		t.Fatalf("GetCurrentUser.guest = %v, want alpha at Gil's Table", me["guest"])
	}
	if me["tenantId"] != w.tenant.String() || me["hasOwnTenant"] == true || me["isPlatformAdmin"] == true {
		t.Errorf("GetCurrentUser = %v; want the HOST tenant, no own tenant, not an admin", me)
	}
	if shared, _ := me["guestWorkspaces"].([]any); len(shared) != 1 {
		t.Errorf("guest_workspaces = %v, want the one shared campaign", me["guestWorkspaces"])
	}

	// Pinned: beta is the tenant's most recent campaign, and it is never resolved.
	active := ok(managementv1connect.CampaignServiceGetActiveCampaignProcedure, `{}`)
	if c, _ := active["campaign"].(map[string]any); c == nil || c["id"] != w.alpha.String() {
		t.Fatalf("the guest's active campaign = %v, want alpha", active["campaign"])
	}
	names := func(resp map[string]any) []string {
		var out []string
		list, _ := resp["nodes"].([]any)
		for _, n := range list {
			out = append(out, n.(map[string]any)["name"].(string))
		}
		slices.Sort(out)
		return out
	}
	if got := names(ok(managementv1connect.CampaignServiceListNodesProcedure, `{}`)); !slices.Equal(got, []string{"Bart the innkeeper", "The cult leader"}) {
		t.Errorf("ListNodes with every right = %v, want alpha's two entries and nothing of beta", got)
	}

	// Without wiki.secrets the private entry is gone, on the real store.
	w.grant(auth.RightWikiRead, auth.RightMapsRead, auth.RightTranscriptsRead, auth.RightHighlightsRead)
	if got := names(ok(managementv1connect.CampaignServiceListNodesProcedure, `{}`)); !slices.Equal(got, []string{"Bart the innkeeper"}) {
		t.Errorf("ListNodes without wiki.secrets = %v, want only the public entry", got)
	}
	if got := names(ok(managementv1connect.CampaignServiceSearchNodesProcedure, `{"query":"cult"}`)); len(got) != 0 {
		t.Errorf("SearchNodes for a private entry's name without wiki.secrets = %v, want nothing", got)
	}
	if status, _ := w.do("tok-guest", http.MethodGet, "/api/v1/knowledge/nodes/"+w.alphaSecret.String()+"/portrait", ""); status != http.StatusNotFound {
		t.Errorf("a private entry's portrait without wiki.secrets: %d, want 404", status)
	}

	w.grant(auth.GuestRights()...)
	// Highlights: the candidate is not listed and not readable.
	hl := ok(managementv1connect.SessionServiceListHighlightsProcedure, `{"voiceSessionId":"`+w.alphaSession.String()+`"}`)
	if list, _ := hl["highlights"].([]any); len(list) != 1 || list[0].(map[string]any)["id"] != w.alphaPromoted.String() {
		t.Errorf("ListHighlights = %v, want only the promoted highlight", hl["highlights"])
	}

	notFound := func(name string, status int, body string) {
		t.Helper()
		if status != http.StatusNotFound {
			t.Errorf("%s: %d %s; want 404", name, status, body)
		}
	}
	rpcNotFound := func(name, procedure, body string) {
		t.Helper()
		status, raw := w.rpc("tok-guest", procedure, body)
		notFound(name, status, raw)
		if !strings.Contains(raw, `"not_found"`) {
			t.Errorf("%s: body %s, want code not_found (never permission_denied — that would confirm the id exists)", name, raw)
		}
	}
	id := func(u uuid.UUID) string { return `"` + u.String() + `"` }

	// Every by-id path, with an id from BETA.
	rpcNotFound("edges of beta's entry", managementv1connect.CampaignServiceListNodeEdgesProcedure, `{"nodeId":`+id(w.betaNode)+`}`)
	rpcNotFound("updating beta's entry", managementv1connect.CampaignServiceUpdateNodeProcedure, `{"id":`+id(w.betaNode)+`,"name":"mine now"}`)
	rpcNotFound("deleting beta's entry", managementv1connect.CampaignServiceDeleteNodeProcedure, `{"id":`+id(w.betaNode)+`}`)
	rpcNotFound("tagging beta's entry", managementv1connect.CampaignServiceSetNodeTagsProcedure, `{"nodeId":`+id(w.betaNode)+`,"tags":["x"]}`)
	rpcNotFound("an edge onto beta's entry", managementv1connect.CampaignServiceCreateEdgeProcedure,
		`{"fromNodeId":`+id(w.alphaPublic)+`,"toNodeId":`+id(w.betaNode)+`,"edgeType":"EDGE_TYPE_KNOWS"}`)
	rpcNotFound("viewing beta's map", managementv1connect.CampaignServiceGetMapViewProcedure, `{"id":`+id(w.betaMap)+`}`)
	rpcNotFound("pinning onto beta's map", managementv1connect.CampaignServiceCreatePinProcedure,
		`{"mapId":`+id(w.betaMap)+`,"nodeId":`+id(w.alphaPublic)+`,"x":0.5,"y":0.5}`)
	rpcNotFound("highlights of beta's session", managementv1connect.SessionServiceListHighlightsProcedure, `{"voiceSessionId":`+id(w.betaSession)+`}`)
	rpcNotFound("beta's highlight", managementv1connect.SessionServiceGetHighlightProcedure, `{"id":`+id(w.betaHighlight)+`}`)
	rpcNotFound("alpha's candidate", managementv1connect.SessionServiceGetHighlightProcedure, `{"id":`+id(w.alphaCandidate)+`}`)
	rpcNotFound("beta's planning threads", managementv1connect.ChatServiceListPlanningThreadsProcedure, `{"campaignId":`+id(w.beta)+`}`)
	rpcNotFound("a thread of beta", managementv1connect.ChatServiceGetPlanningThreadProcedure,
		`{"campaignId":`+id(w.beta)+`,"threadId":`+id(w.betaThread)+`}`)
	rpcNotFound("a thread of beta addressed through alpha", managementv1connect.ChatServiceGetPlanningThreadProcedure,
		`{"campaignId":`+id(w.alpha)+`,"threadId":`+id(w.betaThread)+`}`)
	rpcNotFound("starting a thread in beta", managementv1connect.ChatServiceCreatePlanningThreadProcedure, `{"campaignId":`+id(w.beta)+`}`)
	rpcNotFound("switching to beta", managementv1connect.AuthServiceSwitchWorkspaceProcedure, `{"campaignId":`+id(w.beta)+`}`)

	for name, path := range map[string]string{
		"beta's session snapshot":  "/api/v1/sessions/" + w.betaSession.String(),
		"beta's highlight clip":    "/api/v1/highlights/" + w.betaHighlight.String() + "/clip",
		"beta's highlight image":   "/api/v1/highlights/" + w.betaHighlight.String() + "/image",
		"beta's highlight sound":   "/api/v1/highlights/" + w.betaHighlight.String() + "/sound",
		"alpha's candidate's clip": "/api/v1/highlights/" + w.alphaCandidate.String() + "/clip",
		"beta's map image":         "/api/v1/maps/" + w.betaMap.String() + "/image",
		"beta's entry's portrait":  "/api/v1/knowledge/nodes/" + w.betaNode.String() + "/portrait",
	} {
		status, body := w.do("tok-guest", http.MethodGet, path, "")
		notFound(name, status, body)
	}
	// The anti-vacuity half: the same snapshot route serves alpha's session.
	if status, body := w.do("tok-guest", http.MethodGet, "/api/v1/sessions/"+w.alphaSession.String(), ""); status != http.StatusOK {
		t.Errorf("alpha's session snapshot: %d %s; want 200", status, body)
	}
	// Nothing in beta was changed by any of it.
	if n, err := w.store.ListNodes(context.Background(), w.beta); err != nil || len(n) != 1 || n[0].Name != "Beta's innkeeper" {
		t.Errorf("beta's entries after the guest's attempts = %+v, %v", n, err)
	}

	// Removal takes effect on the very next request.
	if err := w.store.RemoveCampaignGuest(context.Background(), w.alpha, w.guest.ID); err != nil {
		t.Fatalf("RemoveCampaignGuest: %v", err)
	}
	for _, proc := range []string{
		managementv1connect.CampaignServiceGetActiveCampaignProcedure,
		managementv1connect.CampaignServiceListNodesProcedure,
	} {
		if status, body := w.rpc("tok-guest", proc, `{}`); status == http.StatusOK {
			t.Errorf("%s after the guest was removed: 200 %s; want a refusal", proc, body)
		}
	}
	after := ok(managementv1connect.AuthServiceGetCurrentUserProcedure, `{}`)
	if after["guest"] != nil || after["tenantId"] != nil {
		t.Errorf("GetCurrentUser after removal = %v; want no guest scope and no tenant", after)
	}
	// A live session with no workspace left reaches no byte mount at all —
	// the bundle import included, which used to resolve a Tenant by itself.
	for pattern := range plainMountPolicy {
		method, path := mountURL(pattern, w.alpha)
		if status, body := w.do("tok-guest", method, path, ""); status != http.StatusUnauthorized {
			t.Errorf("%s with a session that has no workspace: %d %q; want 401", pattern, status, body)
		}
	}
}

// TestGuestGate_EndToEnd_RevokedGMIsOnlyAGuest: on an allowlist deployment, a
// GM who operates a Tenant but holds no Access Grant — their access was
// revoked — and who is someone's Campaign Guest acts as that guest. Their own
// Tenant is not a workspace: revocation is not undone by being invited.
func TestGuestGate_EndToEnd_RevokedGMIsOnlyAGuest(t *testing.T) {
	w := newGuestGateWorld(t)
	ctx := context.Background()
	const revoked = "500000000000000003"
	ex, err := w.store.ProvisionSignup(ctx, storage.SignupParams{
		User:       storage.UpsertUserParams{DiscordUserID: revoked, Name: "Ex GM"},
		TenantName: "Ex's Table",
		// No guest selection on the session: it must not fall back to the tenant.
		Session: storage.NewSession{Token: "tok-ex", ExpiresAt: time.Now().Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if _, err := w.store.AddCampaignGuest(ctx, w.alpha, revoked, []string{"wiki.read"}, ex.User.ID); err != nil {
		t.Fatalf("add guest: %v", err)
	}

	status, raw := w.rpc("tok-ex", managementv1connect.AuthServiceGetCurrentUserProcedure, `{}`)
	var me map[string]any
	if status != http.StatusOK || json.Unmarshal([]byte(raw), &me) != nil {
		t.Fatalf("GetCurrentUser: %d %s", status, raw)
	}
	if g, _ := me["guest"].(map[string]any); g == nil || g["campaignId"] != w.alpha.String() {
		t.Fatalf("a GM without a grant resolved to %v; want the guest workspace", me)
	}
	if me["tenantId"] != w.tenant.String() || me["hasOwnTenant"] == true {
		t.Errorf("tenant = %v has_own_tenant = %v; want the HOST tenant and no usable own tenant", me["tenantId"], me["hasOwnTenant"])
	}
	// GM-only work — which would act on the HOST tenant — is refused…
	if status, body := w.rpc("tok-ex", managementv1connect.CampaignServiceListCampaignsProcedure, `{}`); !refusedByGate(status, body) {
		t.Errorf("ListCampaigns: %d %s; want the guest gate's 403", status, body)
	}
	// …a bundle import — which would land in a Tenant — is refused too…
	if status, body := w.do("tok-ex", http.MethodPost, "/api/v1/campaigns/import", ""); status != http.StatusForbidden {
		t.Errorf("bundle import as a GM without a grant: %d %q; want 403", status, body)
	}
	// …and so it stays once the guest row is gone and nothing resolves at
	// all: their own Tenant is still not a workspace.
	if err := w.store.RemoveCampaignGuest(ctx, w.alpha, ex.User.ID); err != nil {
		t.Fatalf("RemoveCampaignGuest: %v", err)
	}
	if status, body := w.do("tok-ex", http.MethodPost, "/api/v1/campaigns/import", ""); status != http.StatusUnauthorized {
		t.Errorf("bundle import with no workspace at all: %d %q; want 401 (never an import into the revoked tenant)", status, body)
	}
	if _, err := w.store.AddCampaignGuest(ctx, w.alpha, revoked, []string{"wiki.read"}, ex.User.ID); err != nil {
		t.Fatalf("re-add guest: %v", err)
	}
	// …and they cannot switch into the tenant they no longer may use.
	if status, body := w.rpc("tok-ex", managementv1connect.AuthServiceSwitchWorkspaceProcedure, `{}`); status == http.StatusOK {
		t.Errorf("SwitchWorkspace to their own tenant succeeded: %s", body)
	}

	// Granting access again brings their own Tenant back on the next request.
	if _, err := w.store.GrantAccess(ctx, revoked, "", false, ex.User.ID); err != nil {
		t.Fatalf("GrantAccess: %v", err)
	}
	status, raw = w.rpc("tok-ex", managementv1connect.AuthServiceGetCurrentUserProcedure, `{}`)
	me = nil
	if status != http.StatusOK || json.Unmarshal([]byte(raw), &me) != nil {
		t.Fatalf("GetCurrentUser after the grant: %d %s", status, raw)
	}
	if me["guest"] != nil || me["tenantId"] != ex.Tenant.ID.String() || me["hasOwnTenant"] != true {
		t.Errorf("after the grant = %v; want their own tenant and no guest scope", me)
	}
}
