package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/web"
)

// fakeCampaignService is a canned CampaignServiceHandler so runWebTier can be
// exercised without Postgres: the keyless default gate (ADR-0021/0033) must
// prove the web tier boots, serves /healthz, and shuts down on ctx cancel with
// no DB or Discord credentials in play.
type fakeCampaignService struct{}

func (fakeCampaignService) GetActiveCampaign(
	context.Context,
	*connect.Request[managementv1.GetActiveCampaignRequest],
) (*connect.Response[managementv1.GetActiveCampaignResponse], error) {
	return connect.NewResponse(&managementv1.GetActiveCampaignResponse{}), nil
}

// TestRunWebTierBootsAndShutsDown is the keyless boot+shutdown gate for the
// web/all modes: runWebTier serves /healthz on an ephemeral port and returns
// cleanly once the context is cancelled.
func TestRunWebTierBootsAndShutsDown(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	path, handler := managementv1connect.NewCampaignServiceHandler(fakeCampaignService{})
	mounts := []web.Mount{{Path: path, Handler: handler}}

	srv := web.NewServer(web.Config{
		Addr:     "127.0.0.1:0",
		Mounts:   mounts,
		Recorder: observe.NewPrometheusRecorder(),
		Logger:   log,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWebTier(ctx, srv) }()

	// Poll until /healthz answers. runWebTier binds the listener inside its
	// goroutine, so re-read Addr each iteration until it resolves off the :0
	// placeholder and serves.
	deadline := time.Now().Add(2 * time.Second)
	for {
		var err error
		if addr := srv.Addr(); addr != "127.0.0.1:0" {
			var resp *http.Response
			resp, err = http.Get("http://" + addr + "/healthz")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("web tier never served /healthz: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runWebTier returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runWebTier did not return after ctx cancel")
	}
}
