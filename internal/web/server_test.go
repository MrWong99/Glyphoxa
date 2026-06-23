package web_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.uber.org/goleak"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/web"
)

// TestMain runs goleak so a leaked listener/goroutine from a botched shutdown
// fails the package rather than silently hanging around (ADR-0033 keyless gate).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeCampaignService is a keyless, deterministic CampaignServiceHandler. It
// lets the web-server test prove routing of a Connect handler without pulling in
// internal/rpc or internal/storage — the server is decoupled from campaign
// specifics, so a canned handler is enough.
type fakeCampaignService struct {
	campaign *managementv1.Campaign
}

func (f fakeCampaignService) GetActiveCampaign(
	context.Context,
	*connect.Request[managementv1.GetActiveCampaignRequest],
) (*connect.Response[managementv1.GetActiveCampaignResponse], error) {
	return connect.NewResponse(&managementv1.GetActiveCampaignResponse{Campaign: f.campaign}), nil
}

// startServer brings a Server up on an ephemeral port with the given mounts and
// returns its resolved base URL plus a stop func that cancels and waits for a
// clean shutdown.
func startServer(t *testing.T, mounts ...web.Mount) (string, context.CancelFunc) {
	t.Helper()
	srv := web.NewServer(web.Config{
		Addr:     "127.0.0.1:0",
		Mounts:   mounts,
		Recorder: observe.NewPrometheusRecorder(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return "http://" + srv.Addr(), cancel
}

func TestServerServesHealthzAndConnectThenShutsDown(t *testing.T) {
	want := &managementv1.Campaign{
		Id:       uuid.NewString(),
		TenantId: uuid.NewString(),
		Name:     "Lost Mine",
		System:   "dnd5e",
		Language: "en",
	}
	path, handler := managementv1connect.NewCampaignServiceHandler(fakeCampaignService{campaign: want})

	base, cancel := startServer(t, web.Mount{Path: path, Handler: handler})

	// /healthz proves the observability endpoints are mounted on the same mux.
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", resp.StatusCode)
	}

	// GetActiveCampaign over Connect-JSON proves the server ROUTES the Connect
	// handler over the cleartext port.
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

	// Cancelling the context shuts the listener down; a subsequent dial fails.
	cancel()
	down := time.Now().Add(2 * time.Second)
	for {
		if _, err := http.Get(base + "/healthz"); err != nil {
			break // listener closed
		}
		if time.Now().After(down) {
			t.Fatal("server did not shut down on context cancel")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
