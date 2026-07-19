package main

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
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

func (s staticSessions) Resolve(id uuid.UUID) (storage.VoiceSession, bool) {
	if id != s.id {
		return storage.VoiceSession{}, false
	}
	return storage.VoiceSession{ID: s.id}, true
}

// TestWebTierEmptyWildcardSessionPathEnds404 pins the #153 flagship case with
// production mount parity (runWeb's plain mounts: the {id}/events SSE route AND
// the {id} snapshot route, plus the SPA at "/"): GET /api/v1/sessions//events —
// the empty {id} wildcard a malformed EventSource URL produces — is path-cleaned
// by ServeMux into a redirect onto /api/v1/sessions/events, which the SNAPSHOT
// route claims with id="events". The /api/ fence never sees the cleaned path, so
// the snapshot handler itself must reject the unparseable id: a
// redirect-following client must END at 404 with a non-HTML body, not the empty
// idle view as 200 application/json. A valid-UUID unknown session keeps its
// 200 JSON snapshot (the #74 reload contract).
func TestWebTierEmptyWildcardSessionPathEnds404(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := voiceevent.NewBus()
	relay := transcript.NewRelay(bus, staticSessions{id: uuid.New()}, nil, log)

	const rootBody = "<div id=\"root\"></div>"
	root := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(rootBody))
	})

	srv := web.NewServer(web.Config{
		Addr: "127.0.0.1:0",
		Mounts: []web.Mount{
			{Path: "GET /api/v1/sessions/{id}/events", Handler: http.HandlerFunc(relay.ServeEvents)},
			{Path: "GET /api/v1/sessions/{id}", Handler: http.HandlerFunc(relay.ServeSnapshot)},
		},
		Root:   root,
		Logger: log,
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

	resp, err := http.Get(base + "/api/v1/sessions//events") // follows the mux's clean-path redirect
	if err != nil {
		t.Fatalf("GET //events: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /api/v1/sessions//events ended at status=%d Content-Type=%q, want 404", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "text/html") {
		t.Errorf("GET /api/v1/sessions//events: Content-Type=%q, want non-HTML", ct)
	}
	if strings.Contains(string(body), rootBody) {
		t.Errorf("GET /api/v1/sessions//events: body is the SPA shell %q", body)
	}

	// The snapshot contract for a well-formed id is untouched: a valid-UUID
	// session (known or not) still gets its 200 JSON view.
	snap, err := http.Get(base + "/api/v1/sessions/" + uuid.NewString())
	if err != nil {
		t.Fatalf("GET snapshot: %v", err)
	}
	snap.Body.Close()
	if snap.StatusCode != http.StatusOK || snap.Header.Get("Content-Type") != "application/json" {
		t.Errorf("valid-UUID snapshot: status=%d Content-Type=%q, want 200 application/json", snap.StatusCode, snap.Header.Get("Content-Type"))
	}
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
			// Stamp the event with the session id (#487): on the process bus every
			// event carries its origin SessionID via voiceevent.Forward, and the relay
			// drops unstamped events — so this seeds the session's "status: live" frame.
			bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "attach", TurnID: "t1", SessionID: sessions.id.String()})
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
