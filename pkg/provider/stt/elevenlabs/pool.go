package elevenlabs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	defaultMaxIdleConns = 2
	defaultIdleTTL      = 30 * time.Second
	poolCleanupInterval = 10 * time.Second
	poolWarmTimeout     = 10 * time.Second
)

// connPool maintains a set of pre-warmed WebSocket connections to reduce
// dial latency for ElevenLabs STT sessions. Connections are keyed by their
// full WebSocket URL (which encodes model, language, and sample rate).
//
// When [Provider.StartStream] takes a connection from the pool, it
// immediately starts a background warm-up dial so the next call finds a
// ready connection. A background goroutine periodically evicts connections
// that have exceeded their idle TTL.
//
// All methods are safe for concurrent use.
type connPool struct {
	mu       sync.Mutex
	idle     map[string][]poolEntry
	warming  map[string]bool // tracks in-progress warm-up dials per URL
	maxIdle  int
	idleTTL  time.Duration
	dialFunc func(ctx context.Context, wsURL string) (*websocket.Conn, error)
	done     chan struct{}
	closeO   sync.Once
	wg       sync.WaitGroup
}

// poolEntry is a single idle WebSocket connection with its creation timestamp.
type poolEntry struct {
	conn      *websocket.Conn
	createdAt time.Time
}

// newConnPool creates a connection pool and starts its background cleanup
// goroutine. The dialFn is called to establish new WebSocket connections.
func newConnPool(maxIdle int, idleTTL time.Duration, dialFn func(ctx context.Context, url string) (*websocket.Conn, error)) *connPool {
	p := &connPool{
		idle:     make(map[string][]poolEntry),
		warming:  make(map[string]bool),
		maxIdle:  maxIdle,
		idleTTL:  idleTTL,
		dialFunc: dialFn,
		done:     make(chan struct{}),
	}
	p.wg.Add(1)
	go p.cleanupLoop()
	return p
}

// get returns a pre-warmed connection for the given URL, or dials a fresh one
// if no idle connection is available.
func (p *connPool) get(ctx context.Context, wsURL string) (*websocket.Conn, error) {
	if conn := p.takeIdle(wsURL); conn != nil {
		return conn, nil
	}
	slog.Debug("elevenlabs: pool miss, dialing fresh", "url", wsURL)
	return p.dialFunc(ctx, wsURL)
}

// takeIdle removes and returns the freshest idle connection for the URL, or
// nil if none are available. Stale entries (older than idleTTL) are closed
// and discarded.
func (p *connPool) takeIdle(wsURL string) *websocket.Conn {
	p.mu.Lock()
	defer p.mu.Unlock()

	entries := p.idle[wsURL]
	for len(entries) > 0 {
		// LIFO: prefer the most recently warmed connection.
		e := entries[len(entries)-1]
		entries = entries[:len(entries)-1]

		if time.Since(e.createdAt) < p.idleTTL {
			if len(entries) == 0 {
				delete(p.idle, wsURL)
			} else {
				p.idle[wsURL] = entries
			}
			slog.Debug("elevenlabs: pool hit", "url", wsURL)
			return e.conn
		}
		// Stale — close and try the next entry.
		e.conn.CloseNow()
	}
	delete(p.idle, wsURL)
	return nil
}

// warm starts dialing a connection for the given URL in the background.
// It is a no-op if the pool already has enough idle connections, a warm-up
// is already in progress for this URL, or the pool has been closed.
func (p *connPool) warm(wsURL string) {
	p.mu.Lock()
	select {
	case <-p.done:
		p.mu.Unlock()
		return
	default:
	}
	if len(p.idle[wsURL]) >= p.maxIdle || p.warming[wsURL] {
		p.mu.Unlock()
		return
	}
	p.warming[wsURL] = true
	p.mu.Unlock()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() {
			p.mu.Lock()
			delete(p.warming, wsURL)
			p.mu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), poolWarmTimeout)
		defer cancel()

		select {
		case <-p.done:
			return
		default:
		}

		conn, err := p.dialFunc(ctx, wsURL)
		if err != nil {
			slog.Debug("elevenlabs: pool warm failed", "url", wsURL, "err", err)
			return
		}

		p.mu.Lock()
		defer p.mu.Unlock()

		select {
		case <-p.done:
			conn.CloseNow()
			return
		default:
		}

		if len(p.idle[wsURL]) >= p.maxIdle {
			conn.CloseNow()
			return
		}
		p.idle[wsURL] = append(p.idle[wsURL], poolEntry{
			conn:      conn,
			createdAt: time.Now(),
		})
		slog.Debug("elevenlabs: pool warmed", "url", wsURL, "idle", len(p.idle[wsURL]))
	}()
}

// close drains and closes all idle connections and stops the cleanup
// goroutine. It is safe to call multiple times.
func (p *connPool) close() {
	p.closeO.Do(func() {
		close(p.done)

		p.mu.Lock()
		for url, entries := range p.idle {
			for i := range entries {
				entries[i].conn.CloseNow()
			}
			delete(p.idle, url)
		}
		p.mu.Unlock()

		p.wg.Wait()
	})
}

// cleanupLoop periodically evicts idle connections that have exceeded their TTL.
func (p *connPool) cleanupLoop() {
	defer p.wg.Done()
	ticker := time.NewTicker(poolCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			p.evictStale()
		}
	}
}

// evictStale removes and closes connections older than idleTTL.
func (p *connPool) evictStale() {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for url, entries := range p.idle {
		alive := entries[:0]
		for _, e := range entries {
			if now.Sub(e.createdAt) >= p.idleTTL {
				e.conn.CloseNow()
			} else {
				alive = append(alive, e)
			}
		}
		if len(alive) == 0 {
			delete(p.idle, url)
		} else {
			p.idle[url] = alive
		}
	}
}

// idleCount returns the number of idle connections for the given URL.
// Intended for testing.
func (p *connPool) idleCount(wsURL string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.idle[wsURL])
}
