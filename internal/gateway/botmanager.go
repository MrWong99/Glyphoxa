// Package gateway manages shared Discord bot connections for multi-tenant
// operation. The BotManager multiplexes multiple bot tokens (one per tenant)
// within a single process, tracking in-flight event handlers so that bot
// removal never closes a client with outstanding work.
package gateway

import (
	"context"
	"fmt"
	"sync"

	"github.com/disgoorg/disgo/bot"
)

// botEntry wraps a disgo client with inflight tracking.
// Follows the serverConn pattern from internal/mcp/mcphost/host.go.
type botEntry struct {
	client   *bot.Client
	inflight sync.WaitGroup
}

// BotManager manages per-tenant Discord bot clients. It is safe for
// concurrent use by multiple goroutines.
//
// The zero value is NOT usable; create instances with [NewBotManager].
type BotManager struct {
	mu   sync.RWMutex
	bots map[string]*botEntry // tenant_id -> bot entry
}

// NewBotManager creates a ready-to-use BotManager.
func NewBotManager() *BotManager {
	return &BotManager{
		bots: make(map[string]*botEntry),
	}
}

// AddBot registers a bot client for the given tenant. It returns an error if
// a bot is already registered for tenantID.
func (bm *BotManager) AddBot(tenantID string, client *bot.Client) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if _, ok := bm.bots[tenantID]; ok {
		return fmt.Errorf("gateway: bot already registered for tenant %q", tenantID)
	}

	bm.bots[tenantID] = &botEntry{client: client}
	return nil
}

// RemoveBot removes the bot for tenantID. The entry is deleted from the map
// under the write lock, then inflight handlers are awaited and the client is
// closed outside the lock. This ensures the lock is never held during
// blocking I/O.
func (bm *BotManager) RemoveBot(tenantID string) error {
	bm.mu.Lock()
	entry, ok := bm.bots[tenantID]
	if !ok {
		bm.mu.Unlock()
		return fmt.Errorf("gateway: no bot registered for tenant %q", tenantID)
	}
	delete(bm.bots, tenantID)
	bm.mu.Unlock()

	// Wait for in-flight handlers and close outside the lock.
	entry.inflight.Wait()
	if entry.client != nil {
		entry.client.Close(context.Background())
	}
	return nil
}

// Get returns the bot client for tenantID, or false if none is registered.
func (bm *BotManager) Get(tenantID string) (*bot.Client, bool) {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	entry, ok := bm.bots[tenantID]
	if !ok {
		return nil, false
	}
	return entry.client, true
}

// RouteEvent dispatches handler with the bot client for tenantID. The client
// reference is copied and the inflight WaitGroup incremented under a read
// lock; the lock is released before calling handler. If no bot is registered
// for tenantID, handler is not called.
func (bm *BotManager) RouteEvent(tenantID string, handler func(*bot.Client)) {
	bm.mu.RLock()
	entry, ok := bm.bots[tenantID]
	if !ok {
		bm.mu.RUnlock()
		return
	}
	entry.inflight.Add(1)
	client := entry.client
	bm.mu.RUnlock()

	defer entry.inflight.Done()
	handler(client)
}

// Close removes and shuts down all registered bots gracefully. Each bot's
// inflight handlers are awaited and its client closed outside the lock.
func (bm *BotManager) Close() {
	bm.mu.Lock()
	snapshot := make(map[string]*botEntry, len(bm.bots))
	for id, entry := range bm.bots {
		snapshot[id] = entry
	}
	bm.bots = make(map[string]*botEntry)
	bm.mu.Unlock()

	for _, entry := range snapshot {
		entry.inflight.Wait()
		if entry.client != nil {
			entry.client.Close(context.Background())
		}
	}
}
