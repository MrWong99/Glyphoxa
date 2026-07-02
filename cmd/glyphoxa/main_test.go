package main

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/transcript"
	"github.com/MrWong99/Glyphoxa/internal/web"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// fakeCampaignService is a canned CampaignServiceHandler so runWebTier can be
// exercised without Postgres: the keyless default gate (ADR-0021/0033) must
// prove the web tier boots, serves the Connect API, and shuts down on ctx cancel
// with no DB or Discord credentials in play. The embedded Unimplemented handler
// supplies the roster + CRUD methods (#71) this test does not exercise.
type fakeCampaignService struct {
	managementv1connect.UnimplementedCampaignServiceHandler
}

func (fakeCampaignService) GetActiveCampaign(
	context.Context,
	*connect.Request[managementv1.GetActiveCampaignRequest],
) (*connect.Response[managementv1.GetActiveCampaignResponse], error) {
	return connect.NewResponse(&managementv1.GetActiveCampaignResponse{
		Campaign: &managementv1.Campaign{Name: "test"},
	}), nil
}

// TestRunWebTierBootsAndShutsDown is the keyless boot+shutdown gate for the
// web/all modes: runWebTier serves the Connect API on an ephemeral port and
// returns cleanly once the context is cancelled. Observability lives on a
// separate port (ADR-0039), so this asserts boot via the API, not /healthz.
func TestRunWebTierBootsAndShutsDown(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	path, handler := managementv1connect.NewCampaignServiceHandler(fakeCampaignService{})
	srv := web.NewServer(web.Config{
		Addr:   "127.0.0.1:0",
		Mounts: []web.Mount{{Path: path, Handler: handler}},
		Logger: log,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWebTier(ctx, srv) }()

	// Poll until the Connect API answers. runWebTier binds the listener inside
	// its goroutine, so re-read Addr each iteration until it resolves off the :0
	// placeholder and serves.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if addr := srv.Addr(); addr != "127.0.0.1:0" {
			client := managementv1connect.NewCampaignServiceClient(
				http.DefaultClient, "http://"+addr, connect.WithProtoJSON(),
			)
			if _, err := client.GetActiveCampaign(
				context.Background(),
				connect.NewRequest(&managementv1.GetActiveCampaignRequest{}),
			); err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("web tier never served the Connect API")
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

// staticSessions fakes the relay's Sessions read with a fixed active session, so
// the SSE tail below attaches to a live id without a Manager or DB.
type staticSessions struct{ id uuid.UUID }

func (s staticSessions) Snapshot() (storage.VoiceSession, bool) {
	return storage.VoiceSession{ID: s.id}, true
}

// TestRunWebTierClosesSSEStreamsOnShutdown is the end-to-end half of the issue
// #138 fix, wired exactly like runWeb: the transcript relay's SSE tail mounted
// as a plain (non-APIMount) route and CloseStreams registered as the server's
// shutdown hook. An open, never-idle SSE stream must not stall the graceful
// drain: after ctx cancel, runWebTier (Start + Wait) must return promptly via
// the released stream — far below observe.ShutdownGrace — not at grace expiry.
func TestRunWebTierClosesSSEStreamsOnShutdown(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := voiceevent.NewBus()
	sessions := staticSessions{id: uuid.New()}
	relay := transcript.NewRelay(bus, sessions, nil, log)

	srv := web.NewServer(web.Config{
		Addr: "127.0.0.1:0",
		// The SSE route is a plain mount in runWeb too (no auth here: the gate is
		// orthogonal to shutdown behaviour and needs a DB-backed store).
		Mounts: []web.Mount{{Path: "GET /api/v1/sessions/{id}/events", Handler: http.HandlerFunc(relay.ServeEvents)}},
		Logger: log,
	})
	srv.RegisterOnShutdown(relay.CloseStreams)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runWebTier(ctx, srv) }()

	// Wait for the listener, then open the SSE tail and read until the first
	// frame (the seeded "status: live") proves the stream is attached.
	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for {
		if addr := srv.Addr(); addr != "127.0.0.1:0" {
			bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "attach", TurnID: "t1"})
			r, err := http.Get("http://" + addr + "/api/v1/sessions/" + sessions.id.String() + "/events")
			if err == nil {
				resp = r
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("web tier never served the SSE endpoint")
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	streamAttached := false
	for sc.Scan() {
		if sc.Text() != "" {
			streamAttached = true
			break
		}
	}
	if !streamAttached {
		t.Fatal("SSE stream produced no frames before shutdown")
	}

	cancelled := time.Now()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runWebTier returned error: %v", err)
		}
		if elapsed := time.Since(cancelled); elapsed >= observe.ShutdownGrace/2 {
			t.Fatalf("runWebTier took %v after cancel — the SSE stream stalled the drain to grace expiry", elapsed)
		}
	case <-time.After(observe.ShutdownGrace + 2*time.Second):
		t.Fatal("runWebTier never returned after cancel with an open SSE stream")
	}
}
