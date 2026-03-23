package elevenlabs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/provider/stt"
	"github.com/coder/websocket"
)

// startPoolTestServer launches a minimal WebSocket server for pool tests.
// It accepts connections and keeps them open until the client closes.
func startPoolTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		// Keep the connection open until the client disconnects.
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// poolTestDialer returns a dial function that connects to the test server and
// increments the counter on each call.
func poolTestDialer(t *testing.T, srv *httptest.Server, counter *atomic.Int64) func(ctx context.Context, wsURL string) (*websocket.Conn, error) {
	t.Helper()
	return func(ctx context.Context, _ string) (*websocket.Conn, error) {
		counter.Add(1)
		url := "ws" + srv.URL[len("http"):]
		conn, _, err := websocket.Dial(ctx, url, nil)
		if err != nil {
			return nil, err
		}
		conn.SetReadLimit(1 << 20)
		return conn, nil
	}
}

func TestConnPool_GetMiss(t *testing.T) {
	t.Parallel()

	srv := startPoolTestServer(t)
	var dials atomic.Int64
	p := newConnPool(2, 30*time.Second, poolTestDialer(t, srv, &dials))
	t.Cleanup(p.close)

	conn, err := p.get(context.Background(), "ws://example.com/stt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer conn.CloseNow()

	if got := dials.Load(); got != 1 {
		t.Errorf("expected 1 dial on pool miss, got %d", got)
	}
}

func TestConnPool_GetHit(t *testing.T) {
	t.Parallel()

	srv := startPoolTestServer(t)
	var dials atomic.Int64
	p := newConnPool(2, 30*time.Second, poolTestDialer(t, srv, &dials))
	t.Cleanup(p.close)

	url := "ws://example.com/stt"

	// Warm a connection manually.
	ctx := context.Background()
	warmConn, err := p.dialFunc(ctx, url)
	if err != nil {
		t.Fatalf("dialFunc: %v", err)
	}
	p.mu.Lock()
	p.idle[url] = append(p.idle[url], poolEntry{conn: warmConn, createdAt: time.Now()})
	p.mu.Unlock()

	dialsBefore := dials.Load()

	// Get should return the warmed connection without dialing.
	got, err := p.get(ctx, url)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer got.CloseNow()

	if dials.Load() != dialsBefore {
		t.Error("expected no new dials on pool hit")
	}
	if got != warmConn {
		t.Error("expected get to return the pre-warmed connection")
	}
	if p.idleCount(url) != 0 {
		t.Errorf("expected 0 idle after get, got %d", p.idleCount(url))
	}
}

func TestConnPool_GetEvictsStale(t *testing.T) {
	t.Parallel()

	srv := startPoolTestServer(t)
	var dials atomic.Int64
	// Very short TTL so the pre-warmed connection is stale immediately.
	p := newConnPool(2, 1*time.Millisecond, poolTestDialer(t, srv, &dials))
	t.Cleanup(p.close)

	url := "ws://example.com/stt"

	// Add a connection that's already stale.
	ctx := context.Background()
	staleConn, err := p.dialFunc(ctx, url)
	if err != nil {
		t.Fatalf("dialFunc: %v", err)
	}
	p.mu.Lock()
	p.idle[url] = append(p.idle[url], poolEntry{conn: staleConn, createdAt: time.Now().Add(-time.Second)})
	p.mu.Unlock()

	dialsBefore := dials.Load()

	// get should evict the stale connection and dial fresh.
	got, err := p.get(ctx, url)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer got.CloseNow()

	if dials.Load() <= dialsBefore {
		t.Error("expected a new dial after stale eviction")
	}
	if got == staleConn {
		t.Error("expected a fresh connection, not the stale one")
	}
}

func TestConnPool_Warm(t *testing.T) {
	t.Parallel()

	srv := startPoolTestServer(t)
	var dials atomic.Int64
	p := newConnPool(2, 30*time.Second, poolTestDialer(t, srv, &dials))
	t.Cleanup(p.close)

	url := "ws://example.com/stt"
	p.warm(url)

	// Wait for the warm-up goroutine to complete.
	deadline := time.After(5 * time.Second)
	for {
		if p.idleCount(url) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for warm-up to complete")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	if got := dials.Load(); got != 1 {
		t.Errorf("expected 1 dial from warm, got %d", got)
	}
	if p.idleCount(url) != 1 {
		t.Errorf("expected 1 idle after warm, got %d", p.idleCount(url))
	}
}

func TestConnPool_WarmNoOpWhenFull(t *testing.T) {
	t.Parallel()

	srv := startPoolTestServer(t)
	var dials atomic.Int64
	p := newConnPool(1, 30*time.Second, poolTestDialer(t, srv, &dials))
	t.Cleanup(p.close)

	url := "ws://example.com/stt"

	// Manually fill the pool to maxIdle.
	ctx := context.Background()
	conn, err := p.dialFunc(ctx, url)
	if err != nil {
		t.Fatalf("dialFunc: %v", err)
	}
	p.mu.Lock()
	p.idle[url] = append(p.idle[url], poolEntry{conn: conn, createdAt: time.Now()})
	p.mu.Unlock()

	dialsBefore := dials.Load()

	// warm should be a no-op.
	p.warm(url)
	// Give any potential goroutine a chance to run.
	time.Sleep(50 * time.Millisecond)

	if dials.Load() != dialsBefore {
		t.Error("expected no new dials when pool is full")
	}
}

func TestConnPool_WarmNoOpWhenInProgress(t *testing.T) {
	t.Parallel()

	srv := startPoolTestServer(t)
	var dials atomic.Int64
	p := newConnPool(4, 30*time.Second, poolTestDialer(t, srv, &dials))
	t.Cleanup(p.close)

	url := "ws://example.com/stt"

	// Start two warm-ups concurrently; only one should dial.
	p.warm(url)
	p.warm(url)

	// Wait for warm-up to complete.
	deadline := time.After(5 * time.Second)
	for {
		if p.idleCount(url) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for warm-up")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	if got := dials.Load(); got != 1 {
		t.Errorf("expected exactly 1 dial (deduped), got %d", got)
	}
}

func TestConnPool_EvictStale(t *testing.T) {
	t.Parallel()

	srv := startPoolTestServer(t)
	var dials atomic.Int64
	p := newConnPool(4, 50*time.Millisecond, poolTestDialer(t, srv, &dials))
	t.Cleanup(p.close)

	url := "ws://example.com/stt"

	// Add two connections: one fresh, one stale.
	ctx := context.Background()
	freshConn, err := p.dialFunc(ctx, url)
	if err != nil {
		t.Fatalf("dialFunc: %v", err)
	}
	staleConn, err := p.dialFunc(ctx, url)
	if err != nil {
		t.Fatalf("dialFunc: %v", err)
	}
	p.mu.Lock()
	p.idle[url] = []poolEntry{
		{conn: staleConn, createdAt: time.Now().Add(-time.Second)}, // stale
		{conn: freshConn, createdAt: time.Now()},                   // fresh
	}
	p.mu.Unlock()

	p.evictStale()

	if p.idleCount(url) != 1 {
		t.Errorf("expected 1 idle after eviction, got %d", p.idleCount(url))
	}
}

func TestConnPool_Close(t *testing.T) {
	t.Parallel()

	srv := startPoolTestServer(t)
	var dials atomic.Int64
	p := newConnPool(2, 30*time.Second, poolTestDialer(t, srv, &dials))

	url := "ws://example.com/stt"
	ctx := context.Background()
	conn, err := p.dialFunc(ctx, url)
	if err != nil {
		t.Fatalf("dialFunc: %v", err)
	}
	p.mu.Lock()
	p.idle[url] = append(p.idle[url], poolEntry{conn: conn, createdAt: time.Now()})
	p.mu.Unlock()

	// Close should not block and should clean up.
	p.close()

	if p.idleCount(url) != 0 {
		t.Errorf("expected 0 idle after close, got %d", p.idleCount(url))
	}

	// Second close should be safe.
	p.close()
}

func TestConnPool_WarmAfterClose(t *testing.T) {
	t.Parallel()

	srv := startPoolTestServer(t)
	var dials atomic.Int64
	p := newConnPool(2, 30*time.Second, poolTestDialer(t, srv, &dials))

	p.close()

	dialsBefore := dials.Load()
	p.warm("ws://example.com/stt")
	time.Sleep(50 * time.Millisecond)

	if dials.Load() != dialsBefore {
		t.Error("expected no dials after pool is closed")
	}
}

func TestStartStream_UsesPool(t *testing.T) {
	t.Parallel()

	srv := startElevenLabsServer(t, func(conn *websocket.Conn) {
		ctx := context.Background()
		_ = conn.Write(ctx, websocket.MessageText,
			[]byte(`{"message_type":"session_started"}`))
		for {
			_, msg, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var chunk audioChunkMessage
			if err := json.Unmarshal(msg, &chunk); err != nil {
				continue
			}
			if chunk.Commit {
				resp := `{"message_type":"committed_transcript","text":"pool test","language_code":"en"}`
				_ = conn.Write(ctx, websocket.MessageText, []byte(resp))
				for {
					if _, _, err := conn.Read(ctx); err != nil {
						return
					}
				}
			}
		}
	})

	p, err := New("test-key", WithBaseURL(wsURL(srv)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	// First StartStream triggers a pool miss + a warm-up.
	sess1, err := p.StartStream(context.Background(), stt.StreamConfig{
		SampleRate: 16000,
		Language:   "en",
	})
	if err != nil {
		t.Fatalf("StartStream 1: %v", err)
	}

	// Wait for the warm-up connection to be established.
	wsURLStr, _ := p.buildURL(stt.StreamConfig{SampleRate: 16000, Language: "en"})
	deadline := time.After(5 * time.Second)
	for {
		if p.pool.idleCount(wsURLStr) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for pool warm-up")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	if err := sess1.SendAudio(make([]byte, 320)); err != nil {
		t.Fatalf("SendAudio: %v", err)
	}
	_ = sess1.Close()

	// Second StartStream should get a pool hit (the warmed connection).
	sess2, err := p.StartStream(context.Background(), stt.StreamConfig{
		SampleRate: 16000,
		Language:   "en",
	})
	if err != nil {
		t.Fatalf("StartStream 2: %v", err)
	}
	_ = sess2.Close()
}
