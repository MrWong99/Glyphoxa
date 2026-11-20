package web_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/web"
)

// TestBodyReadDeadline_TrickledBodyIsCutOff is the #662 slow-body regression:
// a client that sends its headers promptly and then stalls mid-body used to
// hold the connection and the handler goroutine forever (only
// ReadHeaderTimeout was set). The mount's body deadline must fail the read.
func TestBodyReadDeadline_TrickledBodyIsCutOff(t *testing.T) {
	readErr := make(chan error, 1)
	base, stop := startServer(t, web.Mount{
		Path:            "/upload",
		BodyReadTimeout: 150 * time.Millisecond,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			readErr <- err
			w.WriteHeader(http.StatusRequestTimeout)
		}),
	})
	defer stop()

	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	// Declare 1 KiB, send one byte, then go silent.
	if _, err := fmt.Fprintf(conn, "POST /upload HTTP/1.1\r\nHost: x\r\nContent-Length: 1024\r\n\r\na"); err != nil {
		t.Fatalf("write request head: %v", err)
	}

	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("body read succeeded on a stalled upload, want a deadline error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler still blocked on a stalled body after 5s — no body read deadline is armed")
	}
}

// TestBodyReadDeadline_DoesNotBoundTheHandler pins that the deadline covers the
// BODY and nothing after it: a handler that has read its body and then takes
// far longer than the deadline to answer (an LLM-backed call, the streaming
// chat exchange) is neither cut off nor has its request context cancelled.
func TestBodyReadDeadline_DoesNotBoundTheHandler(t *testing.T) {
	const deadline = 100 * time.Millisecond
	base, stop := startServer(t, web.Mount{
		Path:            "/slow",
		BodyReadTimeout: deadline,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := io.ReadAll(r.Body); err != nil {
				http.Error(w, "read: "+err.Error(), http.StatusBadRequest)
				return
			}
			select {
			case <-r.Context().Done():
				// Nothing can be written to a cancelled request reliably; the
				// client sees a broken response and the test fails on it.
				return
			case <-time.After(5 * deadline):
			}
			_, _ = io.WriteString(w, "done")
		}),
	})
	defer stop()

	resp, err := http.Post(base+"/slow", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("POST: %v — the handler was cut off after its body had been read", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(got) != "done" {
		t.Fatalf("status=%d body=%q, want 200 %q", resp.StatusCode, got, "done")
	}
}

// TestBodyReadDeadline_LeavesBodylessStreamsAlone guards the SSE transcript
// tail: a GET carries no body, so it must never be given a read deadline — a
// stream that outlives the mount's body deadline many times over keeps
// flowing and its context stays live.
func TestBodyReadDeadline_LeavesBodylessStreamsAlone(t *testing.T) {
	const deadline = 80 * time.Millisecond
	base, stop := startServer(t, web.Mount{
		Path:            "/events",
		BodyReadTimeout: deadline,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			rc := http.NewResponseController(w)
			for i := 0; i < 5; i++ {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(deadline):
				}
				_, _ = fmt.Fprintf(w, "data: %d\n\n", i)
				_ = rc.Flush()
			}
		}),
	})
	defer stop()

	resp, err := http.Get(base + "/events")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	events := 0
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: ") {
			events++
		}
	}
	if events != 5 {
		t.Fatalf("stream delivered %d/5 events — it was cut off by a read deadline it should never have had", events)
	}
}

// TestEveryResponseIsNoSniff pins the listener-wide header (#662): an API
// mount, the root handler and the fenced /api/ 404 all answer nosniff, so no
// response can be content-sniffed into something executable.
func TestEveryResponseIsNoSniff(t *testing.T) {
	srv := web.NewServer(web.Config{
		Addr: "127.0.0.1:0",
		Mounts: []web.Mount{{Path: "/api/thing", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "{}")
		})}},
		Root: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>") }),
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { cancel(); srv.Wait() }()

	for _, path := range []string{"/api/thing", "/", "/api/unclaimed"} {
		resp, err := http.Get("http://" + srv.Addr() + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s: X-Content-Type-Options = %q, want nosniff", path, got)
		}
	}
}
