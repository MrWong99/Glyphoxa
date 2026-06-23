package web_test

import (
	"context"
	"errors"
	"net/http"
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
// is decoupled from campaign specifics, so a canned handler is enough.
type fakeCampaignService struct {
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

func mountFake(t *testing.T, svc fakeCampaignService) web.Mount {
	t.Helper()
	path, handler := managementv1connect.NewCampaignServiceHandler(svc)
	return web.Mount{Path: path, Handler: handler}
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
	// handler over the cleartext port (WithProtoJSON forces the JSON codec).
	client := managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, base, connect.WithProtoJSON(),
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

// TestServerSurfacesConnectErrorCodes proves the web server propagates Connect
// status codes from the mounted handler over the wire, not just success bodies.
func TestServerSurfacesConnectErrorCodes(t *testing.T) {
	base, _ := startServer(t, mountFake(t, fakeCampaignService{
		err: connect.NewError(connect.CodeNotFound, errors.New("no active campaign")),
	}))

	client := managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, base, connect.WithProtoJSON(),
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
