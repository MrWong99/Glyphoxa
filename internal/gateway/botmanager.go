package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sync"

	"github.com/disgoorg/disgo/bot"
)

type botEntry struct {
	client   *bot.Client
	gwBot    *GatewayBot
	guildIDs map[string]struct{}
	inflight sync.WaitGroup
}

// BotManager manages per-tenant Discord bot clients. It is safe for
// concurrent use by multiple goroutines.
//
// The zero value is NOT usable; create instances with [NewBotManager].
type BotManager struct {
	mu   sync.RWMutex
	bots map[string]*botEntry
}

// NewBotManager creates a ready-to-use BotManager.
func NewBotManager() *BotManager {
	return &BotManager{
		bots: make(map[string]*botEntry),
	}
}

// AddBot registers a bot client for the given tenant with an optional guild
// allowlist.
func (bm *BotManager) AddBot(tenantID string, client *bot.Client, guildIDs []string) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if _, ok := bm.bots[tenantID]; ok {
		return fmt.Errorf("gateway: bot already registered for tenant %q", tenantID)
	}

	allowed := make(map[string]struct{}, len(guildIDs))
	for _, id := range guildIDs {
		allowed[id] = struct{}{}
	}
	bm.bots[tenantID] = &botEntry{client: client, guildIDs: allowed}
	return nil
}

// AddGatewayBot registers a [GatewayBot] for the given tenant.
func (bm *BotManager) AddGatewayBot(tenantID string, gwBot *GatewayBot) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if _, ok := bm.bots[tenantID]; ok {
		return fmt.Errorf("gateway: bot already registered for tenant %q", tenantID)
	}

	allowed := make(map[string]struct{}, len(gwBot.guildIDs))
	for _, id := range gwBot.guildIDs {
		allowed[id.String()] = struct{}{}
	}
	bm.bots[tenantID] = &botEntry{
		client:   gwBot.Client(),
		gwBot:    gwBot,
		guildIDs: allowed,
	}
	return nil
}

// RemoveBot removes and closes the bot for tenantID.
func (bm *BotManager) RemoveBot(tenantID string) error {
	bm.mu.Lock()
	entry, ok := bm.bots[tenantID]
	if !ok {
		bm.mu.Unlock()
		return fmt.Errorf("gateway: no bot registered for tenant %q", tenantID)
	}
	delete(bm.bots, tenantID)
	bm.mu.Unlock()

	entry.inflight.Wait()
	ctx := context.Background()
	if entry.gwBot != nil {
		entry.gwBot.Close(ctx)
	} else if entry.client != nil {
		entry.client.Close(ctx)
	}
	return nil
}

// Get returns the bot client for tenantID.
func (bm *BotManager) Get(tenantID string) (*bot.Client, bool) {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	entry, ok := bm.bots[tenantID]
	if !ok {
		return nil, false
	}
	return entry.client, true
}

// GetBot returns the [GatewayBot] for tenantID.
func (bm *BotManager) GetBot(tenantID string) (*GatewayBot, bool) {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	entry, ok := bm.bots[tenantID]
	if !ok || entry.gwBot == nil {
		return nil, false
	}
	return entry.gwBot, true
}

// RouteEvent dispatches handler with the bot client for tenantID.
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

// RouteEventForGuild dispatches handler only if guildID is in the allowlist.
func (bm *BotManager) RouteEventForGuild(tenantID, guildID string, handler func(*bot.Client)) {
	bm.mu.RLock()
	entry, ok := bm.bots[tenantID]
	if !ok {
		bm.mu.RUnlock()
		return
	}
	if len(entry.guildIDs) > 0 {
		if _, allowed := entry.guildIDs[guildID]; !allowed {
			bm.mu.RUnlock()
			slog.Debug("gateway: filtered event for non-allowed guild",
				"tenant_id", tenantID, "guild_id", guildID)
			return
		}
	}
	entry.inflight.Add(1)
	client := entry.client
	bm.mu.RUnlock()

	defer entry.inflight.Done()
	handler(client)
}

// IsBotConnected reports whether a tenant has a connected bot and the number
// of guilds in its allowlist.
func (bm *BotManager) IsBotConnected(tenantID string) (connected bool, guildCount int) {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	entry, ok := bm.bots[tenantID]
	if !ok {
		return false, 0
	}
	return true, len(entry.guildIDs)
}

// Close removes and shuts down all registered bots gracefully.
func (bm *BotManager) Close() {
	bm.mu.Lock()
	snapshot := make(map[string]*botEntry, len(bm.bots))
	maps.Copy(snapshot, bm.bots)
	bm.bots = make(map[string]*botEntry)
	bm.mu.Unlock()

	ctx := context.Background()
	for _, entry := range snapshot {
		entry.inflight.Wait()
		if entry.gwBot != nil {
			entry.gwBot.Close(ctx)
		} else if entry.client != nil {
			entry.client.Close(ctx)
		}
	}
}
