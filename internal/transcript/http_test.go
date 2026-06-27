package transcript_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/transcript"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

type fakeSessions struct {
	id     uuid.UUID
	active bool
}

func (f *fakeSessions) Snapshot() (storage.VoiceSession, bool) {
	return storage.VoiceSession{ID: f.id}, f.active
}

// sseFrame is one parsed SSE frame from the response stream.
type sseFrame struct {
	id    uint64
	event string
	data  string
}

// readFrames parses SSE frames off body into a channel until the body closes.
func readFrames(body io.Reader) <-chan sseFrame {
	out := make(chan sseFrame)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(body)
		var cur sseFrame
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur.event != "" {
					out <- cur
				}
				cur = sseFrame{}
			case strings.HasPrefix(line, "id: "):
				cur.id, _ = strconv.ParseUint(strings.TrimPrefix(line, "id: "), 10, 64)
			case strings.HasPrefix(line, "event: "):
				cur.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return out
}

func nextFrame(t *testing.T, ch <-chan sseFrame) sseFrame {
	t.Helper()
	select {
	case f, ok := <-ch:
		if !ok {
			t.Fatal("frame stream closed early")
		}
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE frame")
		return sseFrame{}
	}
}

// nextLine reads frames until a "line" frame and returns its decoded line.
func nextLine(t *testing.T, ch <-chan sseFrame) transcript.Line {
	t.Helper()
	for {
		f := nextFrame(t, ch)
		if f.event != "line" {
			continue
		}
		var l transcript.Line
		if err := json.Unmarshal([]byte(f.data), &l); err != nil {
			t.Fatalf("unmarshal line: %v", err)
		}
		return l
	}
}

func mux(r *transcript.Relay) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/sessions/{id}/events", r.ServeEvents)
	m.HandleFunc("GET /api/v1/sessions/{id}", r.ServeSnapshot)
	return m
}

// connect opens the SSE stream for id (optionally resuming from lastID) and
// returns the frame channel + a cancel that closes the connection.
func connect(t *testing.T, base, id string, lastID uint64) (<-chan sseFrame, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/sessions/"+id+"/events", nil)
	if err != nil {
		cancel()
		t.Fatalf("new request: %v", err)
	}
	if lastID > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatUint(lastID, 10))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("connect: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		cancel()
		t.Fatalf("content-type %q", ct)
	}
	ch := readFrames(resp.Body)
	return ch, func() { cancel(); resp.Body.Close() }
}

// TestSSE_ReplayThenLive: buffered frames replay on connect, then a fresh event
// streams live; a reconnect with Last-Event-ID skips already-seen frames.
func TestSSE_ReplayThenLive(t *testing.T) {
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), active: true}
	relay := transcript.NewRelay(bus, fs, nil, nil)
	srv := httptest.NewServer(mux(relay))
	defer srv.Close()
	id := fs.id.String()

	// Buffered before any client connects.
	bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "first", TurnID: "t1"})

	ch, stop := connect(t, srv.URL, id, 0)
	// Replay delivers the buffered human line.
	if l := nextLine(t, ch); l.Text != "first" || l.Kind != transcript.KindPlayer {
		t.Fatalf("replayed line = %+v", l)
	}

	// A new event streams live.
	bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "second", TurnID: "t2"})
	live := nextLine(t, ch)
	if live.Text != "second" {
		t.Fatalf("live line = %+v", live)
	}
	seenSeq := relay.Frames(id, 0)
	stop()

	// Reconnect resuming after the live "second" line's seq: no replay of it.
	lastID := seenSeq[len(seenSeq)-1].Seq
	bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "third", TurnID: "t3"})
	ch2, stop2 := connect(t, srv.URL, id, lastID)
	defer stop2()
	got := nextLine(t, ch2)
	if got.Text != "third" {
		t.Fatalf("after reconnect want 'third' first, got %+v", got)
	}
}

// TestSnapshotEndpoint returns the current lines + derived status as JSON.
func TestSnapshotEndpoint(t *testing.T) {
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), active: true}
	relay := transcript.NewRelay(bus, fs, nil, nil)
	srv := httptest.NewServer(mux(relay))
	defer srv.Close()
	id := fs.id.String()

	bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "snap me", TurnID: "t1"})

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + id)
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	defer resp.Body.Close()
	var v transcript.View
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Status != "live" || !v.Typing.Active {
		t.Fatalf("snapshot status=%q typing=%+v", v.Status, v.Typing)
	}
	if len(v.Lines) != 1 || v.Lines[0].Text != "snap me" {
		t.Fatalf("snapshot lines = %+v", v.Lines)
	}
}

// TestSnapshotIdle returns idle when no session is active.
func TestSnapshotIdle(t *testing.T) {
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), active: false}
	relay := transcript.NewRelay(bus, fs, nil, nil)
	srv := httptest.NewServer(mux(relay))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + fs.id.String())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	var v transcript.View
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Status != "idle" || len(v.Lines) != 0 {
		t.Fatalf("idle snapshot = %+v", v)
	}
}
