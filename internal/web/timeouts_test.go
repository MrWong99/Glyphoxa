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

// startServerWith is startServer with a full Config, for the tests that need a
// Root handler or a shortened body deadline.
func startServerWith(t *testing.T, cfg web.Config) (base string, stop func()) {
	t.Helper()
	cfg.Addr = "127.0.0.1:0"
	srv := web.NewServer(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return "http://" + srv.Addr(), func() { cancel(); srv.Wait() }
}

// stalledPOST opens a raw connection, declares a 1 KiB body, sends one byte and
// goes silent. It returns how long the server took to give up on it (answer
// and/or close the connection), or ok=false if it was still holding the
// connection open after wait.
func stalledPOST(t *testing.T, base, path string, wait time.Duration) (held time.Duration, ok bool) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	start := time.Now()
	if _, err := fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: x\r\nContent-Length: 1024\r\n\r\na", path); err != nil {
		t.Fatalf("write request head: %v", err)
	}
	// Read until the server closes the connection. A server that is still
	// waiting for the other 1023 bytes never closes it.
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	if _, err := io.Copy(io.Discard, conn); err != nil {
		return 0, false // our own deadline fired: the server was still holding on
	}
	return time.Since(start), true
}

// TestBodyReadDeadline_TrickledBodyIsCutOffOnEveryRoute is the #662 slow-body
// regression: a client that sends its headers promptly and then stalls
// mid-body used to hold the connection forever (only ReadHeaderTimeout was
// set). The deadline covers the WHOLE listener — an earlier version of this
// fix wrapped only the registered mounts, and a stalled `POST /` (the SPA
// root) or a stalled POST to an unclaimed /api/ path (the 404 fence) still
// held its connection indefinitely, because net/http drains an unread body
// after the handler returns.
func TestBodyReadDeadline_TrickledBodyIsCutOffOnEveryRoute(t *testing.T) {
	base, stop := startServerWith(t, web.Config{
		BodyReadTimeout: 150 * time.Millisecond,
		Mounts: []web.Mount{{
			Path: "/api/upload",
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.ReadAll(r.Body); err != nil {
					w.WriteHeader(http.StatusRequestTimeout)
				}
			}),
		}},
		// Like the real SPA handler, the root never reads a request body.
		Root: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>") }),
	})
	defer stop()

	for name, path := range map[string]string{
		"a mount that reads the body": "/api/upload",
		"the SPA root":                "/",
		"an SPA deep link":            "/t/foo/configuration",
		"the /api/ 404 fence":         "/api/unclaimed",
	} {
		held, ok := stalledPOST(t, base, path, 5*time.Second)
		if !ok {
			t.Errorf("%s (%s): the server was still holding a stalled upload's connection after 5s", name, path)
			continue
		}
		if held > 3*time.Second {
			t.Errorf("%s (%s): connection held for %v with a 150ms body deadline", name, path, held)
		}
	}
}

// TestBodyReadDeadline_CanBeExtendedAfterAuth pins the mechanism the upload
// routes rely on: the listener arms the short default first, and a handler
// further in (the auth layer, once the session check has passed) may replace
// it with a longer one. A body that arrives after the default would have
// fired, but within the extension, is read in full.
func TestBodyReadDeadline_CanBeExtendedAfterAuth(t *testing.T) {
	const short = 100 * time.Millisecond
	got := make(chan string, 1)
	base, stop := startServerWith(t, web.Config{
		BodyReadTimeout: short,
		Mounts: []web.Mount{{
			Path: "/api/upload",
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Minute))
				body, err := io.ReadAll(r.Body)
				if err != nil {
					got <- "read error: " + err.Error()
					return
				}
				got <- string(body)
			}),
		}},
	})
	defer stop()

	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "POST /api/upload HTTP/1.1\r\nHost: x\r\nContent-Length: 4\r\n\r\nsl"); err != nil {
		t.Fatalf("write head: %v", err)
	}
	time.Sleep(4 * short) // well past the default deadline
	if _, err := io.WriteString(conn, "ow"); err != nil {
		t.Fatalf("write rest of body: %v", err)
	}
	select {
	case body := <-got:
		if body != "slow" {
			t.Fatalf("handler read %q, want the whole slow body — the extended deadline did not replace the default", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler never finished reading the body")
	}
}

// TestBodyReadDeadline_DoesNotBoundTheHandler pins that the deadline covers the
// BODY and nothing after it: a handler that has read its body and then takes
// far longer than the deadline to answer (an LLM-backed call, the streaming
// chat exchange) is neither cut off nor has its request context cancelled.
func TestBodyReadDeadline_DoesNotBoundTheHandler(t *testing.T) {
	const deadline = 100 * time.Millisecond
	base, stop := startServerWith(t, web.Config{
		BodyReadTimeout: deadline,
		Mounts: []web.Mount{{
			Path: "/slow",
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
		}},
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
// stream that outlives the body deadline many times over keeps flowing and its
// context stays live.
func TestBodyReadDeadline_LeavesBodylessStreamsAlone(t *testing.T) {
	const deadline = 80 * time.Millisecond
	base, stop := startServerWith(t, web.Config{
		BodyReadTimeout: deadline,
		Mounts: []web.Mount{{
			Path: "/events",
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
		}},
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
	base, stop := startServerWith(t, web.Config{
		Mounts: []web.Mount{{Path: "/api/thing", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "{}")
		})}},
		Root: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>") }),
	})
	defer stop()

	for _, path := range []string{"/api/thing", "/", "/api/unclaimed"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s: X-Content-Type-Options = %q, want nosniff", path, got)
		}
	}
}
