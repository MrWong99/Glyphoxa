package web_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.uber.org/goleak"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/web"
)

// TestMain runs goleak so a leaked listener/goroutine from a botched shutdown
// fails the package rather than silently hanging around (ADR-0033 keyless gate).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeCampaignService is a keyless, deterministic CampaignServiceHandler. It
// lets the web-server test prove routing of a Connect handler — success AND
// error-code — without pulling in internal/rpc or internal/storage. The server
// is decoupled from campaign specifics, so a canned handler is enough. The
// embedded Unimplemented handler supplies the roster + CRUD methods (#71) the
// routing test does not exercise.
type fakeCampaignService struct {
	managementv1connect.UnimplementedCampaignServiceHandler
	campaign *managementv1.Campaign
	err      error
}

func (f fakeCampaignService) GetActiveCampaign(
	context.Context,
	*connect.Request[managementv1.GetActiveCampaignRequest],
) (*connect.Response[managementv1.GetActiveCampaignResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&managementv1.GetActiveCampaignResponse{Campaign: f.campaign}), nil
}

// startServer brings a Server up on an ephemeral port with the given mounts and
// returns its resolved base URL plus a stop func that cancels the context and
// blocks until the server has fully drained (Server.Wait). The observability
// endpoints are intentionally NOT here — the web tier serves only the API
// (ADR-0039) — so the tests assert via the Connect handler, not /healthz.
func startServer(t *testing.T, mounts ...web.Mount) (base string, stop func()) {
	t.Helper()
	srv := web.NewServer(web.Config{
		Addr:   "127.0.0.1:0",
		Mounts: mounts,
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	stop = func() {
		cancel()
		srv.Wait()
	}
	t.Cleanup(stop)
	return "http://" + srv.Addr(), stop
}

// mountFake mounts the fake CampaignService under /api, mirroring the production
// wiring (cmd/glyphoxa runWeb): the browser dials Connect at baseUrl "/api", so
// the generated handler is wrapped in http.StripPrefix("/api", …) and registered
// under "/api" + its method path. Connect clients in the tests therefore dial
// base + "/api".
func mountFake(t *testing.T, svc fakeCampaignService) web.Mount {
	t.Helper()
	path, handler := managementv1connect.NewCampaignServiceHandler(svc)
	return web.APIMount(path, handler)
}

func TestServerRoutesConnectThenShutsDown(t *testing.T) {
	want := &managementv1.Campaign{
		Id:       uuid.NewString(),
		TenantId: uuid.NewString(),
		Name:     "Lost Mine",
		System:   "dnd5e",
		Language: "en",
	}
	base, stop := startServer(t, mountFake(t, fakeCampaignService{campaign: want}))

	// GetActiveCampaign over Connect-JSON proves the server ROUTES the Connect
	// handler over the cleartext port (WithProtoJSON forces the JSON codec). The
	// API is mounted under /api now (the SPA owns /), so the client dials there.
	client := managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, base+"/api", connect.WithProtoJSON(),
	)
	got, err := client.GetActiveCampaign(
		context.Background(),
		connect.NewRequest(&managementv1.GetActiveCampaignRequest{}),
	)
	if err != nil {
		t.Fatalf("GetActiveCampaign: %v", err)
	}
	if got.Msg.GetCampaign().GetName() != want.Name {
		t.Errorf("name = %q, want %q", got.Msg.GetCampaign().GetName(), want.Name)
	}

	// stop cancels the context and waits for the graceful shutdown to drain; a
	// subsequent dial must then fail because the listener is closed.
	stop()
	if _, err := http.Get(base + "/"); err == nil {
		t.Error("server still serving after shutdown")
	}
}

// TestServerAPINamespace404s pins the /api/ fence (#153): any method on an
// /api/... path no handler claims must get a plain 404 — never the SPA's
// 200+index.html (which sends EventSource into a reconnect loop and turns
// version-skewed Connect calls into misleading errors) and never a file-server
// 405. Real mounts keep winning: the Connect handler and the {id}-wildcard SSE
// route resolve exactly as before, and the SPA still owns non-API routes.
func TestServerAPINamespace404s(t *testing.T) {
	const rootBody = "<div id=\"root\"></div>"
	root := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(rootBody))
	})
	sse := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	})

	srv := web.NewServer(web.Config{
		Addr: "127.0.0.1:0",
		Mounts: []web.Mount{
			mountFake(t, fakeCampaignService{campaign: &managementv1.Campaign{Name: "Lost Mine"}}),
			{Path: "GET /api/v1/sessions/{id}/events", Handler: sse},
		},
		Root: root,
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		srv.Wait()
	})
	base := "http://" + srv.Addr()

	// Unmounted /api/... paths — including the empty-wildcard SSE shape a
	// malformed EventSource URL produces — must 404 with a non-HTML body, for
	// GET and POST alike.
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/nope"},
		{http.MethodPost, "/api/v1/nope"},
		{http.MethodPost, "/api/glyphoxa.management.v1.GhostService/Call"},
		{http.MethodGet, "/api/v1/sessions//events"},
	} {
		req, err := http.NewRequest(tc.method, base+tc.path, nil)
		if err != nil {
			t.Fatalf("NewRequest %s %s: %v", tc.method, tc.path, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s: status=%d, want 404", tc.method, tc.path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "text/html") {
			t.Errorf("%s %s: Content-Type=%q, want non-HTML", tc.method, tc.path, ct)
		}
		if strings.Contains(string(body), rootBody) {
			t.Errorf("%s %s: body is the SPA shell %q, want a 404 body", tc.method, tc.path, body)
		}
	}

	// The {id}-wildcard SSE route still resolves past the fence.
	resp, err := http.Get(base + "/api/v1/sessions/" + uuid.NewString() + "/events")
	if err != nil {
		t.Fatalf("GET SSE route: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("SSE route: status=%d Content-Type=%q, want 200 text/event-stream", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// The Connect mount still resolves past the fence.
	client := managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, base+"/api", connect.WithProtoJSON(),
	)
	if _, err := client.GetActiveCampaign(
		context.Background(),
		connect.NewRequest(&managementv1.GetActiveCampaignRequest{}),
	); err != nil {
		t.Fatalf("GetActiveCampaign through the fence: %v", err)
	}

	// The SPA catch-all still owns non-API app routes.
	spaResp, err := http.Get(base + "/t/foo/configuration")
	if err != nil {
		t.Fatalf("GET SPA route: %v", err)
	}
	spaBody, _ := io.ReadAll(spaResp.Body)
	spaResp.Body.Close()
	if spaResp.StatusCode != http.StatusOK || !strings.Contains(string(spaBody), rootBody) {
		t.Errorf("SPA route: status=%d body=%q, want 200 + SPA root", spaResp.StatusCode, spaBody)
	}
}

// TestServerSurfacesConnectErrorCodes proves the web server propagates Connect
// status codes from the mounted handler over the wire, not just success bodies.
func TestServerSurfacesConnectErrorCodes(t *testing.T) {
	base, _ := startServer(t, mountFake(t, fakeCampaignService{
		err: connect.NewError(connect.CodeNotFound, errors.New("no active campaign")),
	}))

	client := managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, base+"/api", connect.WithProtoJSON(),
	)
	_, err := client.GetActiveCampaign(
		context.Background(),
		connect.NewRequest(&managementv1.GetActiveCampaignRequest{}),
	)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want %v", got, connect.CodeNotFound)
	}
}

// TestServerMountsRootSPAAlongsideAPI proves the web tier serves the SPA at "/"
// (Config.Root) WHILE the Connect API stays reachable under /api — the
// production all/web-Mode shape (the SPA owns "/", the API owns /api). It uses a
// canned root handler (not internal/spa) to keep the web package decoupled from
// the embedded bundle, mirroring how the fake Connect handler stands in for rpc.
func TestServerMountsRootSPAAlongsideAPI(t *testing.T) {
	const rootBody = "<div id=\"root\"></div>"
	root := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(rootBody))
	})

	srv := web.NewServer(web.Config{
		Addr:   "127.0.0.1:0",
		Mounts: []web.Mount{mountFake(t, fakeCampaignService{campaign: &managementv1.Campaign{Name: "Lost Mine"}})},
		Root:   root,
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		srv.Wait()
	})
	base := "http://" + srv.Addr()

	// "/" and an arbitrary client-side deep link both reach the SPA root.
	for _, path := range []string{"/", "/t/foo/configuration"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), rootBody) {
			t.Errorf("GET %s: status=%d body=%q, want 200 + SPA root", path, resp.StatusCode, body)
		}
	}

	// The API still routes under /api despite the "/" catch-all.
	client := managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, base+"/api", connect.WithProtoJSON(),
	)
	got, err := client.GetActiveCampaign(
		context.Background(),
		connect.NewRequest(&managementv1.GetActiveCampaignRequest{}),
	)
	if err != nil {
		t.Fatalf("GetActiveCampaign: %v", err)
	}
	if got.Msg.GetCampaign().GetName() != "Lost Mine" {
		t.Errorf("name = %q, want %q", got.Msg.GetCampaign().GetName(), "Lost Mine")
	}
}
