package web_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/web"
)

// TestWaitBlocksUntilDrainCompletes pins the Wait contract (issue #138): a
// request is parked inside a handler, the server ctx is cancelled, and Wait must
// not return until the handler finishes draining. Every step is
// channel-synchronized; the timeouts are generous upper bounds that fail instead
// of deadlocking, and the handler is released well within observe.ShutdownGrace
// so the test observes a genuine graceful drain — asserted by the in-flight
// client receiving its full 200 response.
func TestWaitBlocksUntilDrainCompletes(t *testing.T) {
	handlerEntered := make(chan struct{})
	releaseHandler := make(chan struct{})
	handlerFinished := make(chan struct{})

	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(handlerEntered)
		<-releaseHandler
		fmt.Fprint(w, "drained-ok")
		close(handlerFinished)
	})

	srv := web.NewServer(web.Config{
		Addr:   "127.0.0.1:0",
		Mounts: []web.Mount{{Path: "/slow", Handler: slow}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	type clientResult struct {
		body string
		code int
		err  error
	}
	clientDone := make(chan clientResult, 1)
	go func() {
		resp, err := http.Get("http://" + srv.Addr() + "/slow")
		if err != nil {
			clientDone <- clientResult{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		clientDone <- clientResult{body: string(b), code: resp.StatusCode, err: err}
	}()

	select {
	case <-handlerEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler never entered")
	}

	cancel() // shutdown begins while the handler is still in flight

	waitReturned := make(chan struct{})
	go func() {
		srv.Wait()
		close(waitReturned)
	}()

	// releaseHandler has NOT been closed, so the handler is definitionally still
	// in flight: Wait returning here is the #138 defect.
	select {
	case <-waitReturned:
		t.Fatal("issue #138: Wait() returned before the in-flight handler finished draining")
	case <-time.After(500 * time.Millisecond):
	}

	close(releaseHandler)

	select {
	case <-handlerFinished:
	case <-time.After(3 * time.Second):
		t.Fatal("handler never finished after release")
	}
	select {
	case <-waitReturned:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait() never returned after the drain completed")
	}

	select {
	case res := <-clientDone:
		if res.err != nil {
			t.Fatalf("client error (drain was not graceful): %v", res.err)
		}
		if res.code != http.StatusOK || res.body != "drained-ok" {
			t.Fatalf("unexpected response %d %q — drain was not graceful", res.code, res.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client never got a response")
	}
}

// TestRegisterOnShutdownReleasesStreamingHandler pins the long-lived-stream
// story (issue #138 fix follow-through): a never-idle streaming handler — the
// shape of the transcript SSE tail — blocks on a channel that an OnShutdown
// callback closes. Cancelling the server ctx must (a) run the callback, (b) let
// the handler exit, and (c) let Wait return promptly, NOT by waiting out the
// full observe.ShutdownGrace.
func TestRegisterOnShutdownReleasesStreamingHandler(t *testing.T) {
	streamEntered := make(chan struct{})
	stopStreaming := make(chan struct{})

	stream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(streamEntered)
		<-stopStreaming // released only by the OnShutdown callback
	})

	srv := web.NewServer(web.Config{
		Addr:   "127.0.0.1:0",
		Mounts: []web.Mount{{Path: "/stream", Handler: stream}},
	})
	srv.RegisterOnShutdown(func() { close(stopStreaming) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	go func() {
		resp, err := http.Get("http://" + srv.Addr() + "/stream")
		if err != nil {
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
	}()

	select {
	case <-streamEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("streaming handler never entered")
	}

	cancelled := time.Now()
	cancel()
	waitReturned := make(chan struct{})
	go func() {
		srv.Wait()
		close(waitReturned)
	}()

	// Wait must return via the released stream, well before the grace deadline
	// would abandon the drain. The bound is deliberately far below ShutdownGrace
	// so a grace-expiry exit cannot sneak through as a pass.
	graceBound := observe.ShutdownGrace / 2
	select {
	case <-waitReturned:
		if elapsed := time.Since(cancelled); elapsed >= graceBound {
			t.Fatalf("Wait() took %v after cancel — the stream stalled the drain to grace expiry instead of being released on shutdown", elapsed)
		}
	case <-time.After(observe.ShutdownGrace + 2*time.Second):
		t.Fatal("Wait() never returned")
	}
}
