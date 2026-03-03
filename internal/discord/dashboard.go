package discord

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// SessionData provides the data needed to render a dashboard embed.
// This interface decouples the dashboard from the SessionManager implementation.
type SessionData interface {
	IsActive() bool
	SessionID() string
	CampaignName() string
	StartedAt() time.Time
	NPCCount() int
	MutedNPCCount() int
	MemoryEntries() int
}

// embedColorGreen is the embed sidebar color for an active session.
const embedColorGreen = 0x2ECC71

// embedColorRed is the embed sidebar color when a session has ended.
const embedColorRed = 0xE74C3C

// defaultInterval is the default dashboard update interval.
const defaultInterval = 10 * time.Second

// dashboardMessenger abstracts the Discord REST calls needed by Dashboard.
// In production this is satisfied by rest.Channels (from bot.Client.Rest).
type dashboardMessenger interface {
	CreateMessage(channelID snowflake.ID, messageCreate discord.MessageCreate, opts ...rest.RequestOpt) (*discord.Message, error)
	UpdateMessage(channelID snowflake.ID, messageID snowflake.ID, messageUpdate discord.MessageUpdate, opts ...rest.RequestOpt) (*discord.Message, error)
}

// Dashboard renders and periodically updates a Discord embed showing
// live session metrics. The embed is created on Start and edited in place
// every update interval.
//
// Thread-safe for concurrent use.
type Dashboard struct {
	mu        sync.Mutex
	rest      dashboardMessenger
	channelID snowflake.ID
	messageID snowflake.ID // embed message; created on first update
	interval  time.Duration
	getData   func() SessionData
	stats     *PipelineStats
	done      chan struct{}
	stopOnce  sync.Once
}

// DashboardConfig holds dependencies for creating a Dashboard.
type DashboardConfig struct {
	Rest      dashboardMessenger
	ChannelID snowflake.ID
	Interval  time.Duration // Default: 10 seconds
	GetData   func() SessionData
	Stats     *PipelineStats
}

// NewDashboard creates a Dashboard.
func NewDashboard(cfg DashboardConfig) *Dashboard {
	interval := cfg.Interval
	if interval == 0 {
		interval = defaultInterval
	}
	return &Dashboard{
		rest:      cfg.Rest,
		channelID: cfg.ChannelID,
		interval:  interval,
		getData:   cfg.GetData,
		stats:     cfg.Stats,
		done:      make(chan struct{}),
	}
}

// Stats returns the pipeline stats collector for this dashboard,
// allowing callers to record latency and counter values.
func (d *Dashboard) Stats() *PipelineStats {
	return d.stats
}

// Start begins the periodic update loop in a background goroutine.
func (d *Dashboard) Start(ctx context.Context) {
	go d.loop(ctx)
}

// Stop halts the periodic update loop and posts a final "session ended" embed.
func (d *Dashboard) Stop(ctx context.Context) {
	d.stopOnce.Do(func() {
		close(d.done)
		d.postFinalEmbed(ctx)
	})
}

// loop runs the periodic embed update until Stop is called or ctx is cancelled.
func (d *Dashboard) loop(ctx context.Context) {
	// Post immediately on start.
	d.update(ctx)

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-d.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.update(ctx)
		}
	}
}

// update builds the embed from current data and creates or edits the message.
func (d *Dashboard) update(_ context.Context) {
	data := d.getData()
	var snap Snapshot
	if d.stats != nil {
		snap = d.stats.Snapshot()
	}
	embed := buildEmbed(data, snap)

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.messageID == 0 {
		msg, err := d.rest.CreateMessage(d.channelID, discord.MessageCreate{
			Embeds: []discord.Embed{embed},
		})
		if err != nil {
			slog.Warn("dashboard: failed to create embed message", "channel", d.channelID, "err", err)
			return
		}
		d.messageID = msg.ID
		slog.Debug("dashboard: created embed message", "message_id", msg.ID, "channel", d.channelID)
	} else {
		embeds := []discord.Embed{embed}
		_, err := d.rest.UpdateMessage(d.channelID, d.messageID, discord.MessageUpdate{
			Embeds: &embeds,
		})
		if err != nil {
			slog.Warn("dashboard: failed to edit embed message", "message_id", d.messageID, "err", err)
		}
	}
}

// postFinalEmbed posts a "session ended" version of the embed.
func (d *Dashboard) postFinalEmbed(_ context.Context) {
	data := d.getData()
	var snap Snapshot
	if d.stats != nil {
		snap = d.stats.Snapshot()
	}
	embed := buildEndedEmbed(data, snap)

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.messageID == 0 {
		return
	}
	embeds := []discord.Embed{embed}
	_, err := d.rest.UpdateMessage(d.channelID, d.messageID, discord.MessageUpdate{
		Embeds: &embeds,
	})
	if err != nil {
		slog.Warn("dashboard: failed to post final embed", "message_id", d.messageID, "err", err)
	}
}

// buildEmbed creates the live dashboard embed from session data and pipeline stats.
func buildEmbed(data SessionData, snap Snapshot) discord.Embed {
	duration := time.Since(data.StartedAt()).Truncate(time.Second)
	npcField := fmt.Sprintf("%d", data.NPCCount())
	if muted := data.MutedNPCCount(); muted > 0 {
		npcField = fmt.Sprintf("%d (%d muted)", data.NPCCount(), muted)
	}

	fields := []discord.EmbedField{
		{Name: "Campaign", Value: data.CampaignName(), Inline: new(true)},
		{Name: "Session ID", Value: fmt.Sprintf("`%s`", data.SessionID()), Inline: new(true)},
		{Name: "Duration", Value: duration.String(), Inline: new(true)},
		{Name: "Active NPCs", Value: npcField, Inline: new(true)},
		{Name: "Utterances", Value: fmt.Sprintf("%d", snap.Utterances), Inline: new(true)},
		{Name: "Errors", Value: fmt.Sprintf("%d", snap.Errors), Inline: new(true)},
	}

	// Add latency fields if we have samples.
	if latency := formatLatencyField(snap); latency != "" {
		fields = append(fields, discord.EmbedField{
			Name:  "Pipeline Latency",
			Value: latency,
		})
	}

	// Add memory entry count.
	fields = append(fields, discord.EmbedField{
		Name:   "Memory Entries",
		Value:  fmt.Sprintf("%d", data.MemoryEntries()),
		Inline: new(true),
	})

	now := time.Now().UTC()
	return discord.Embed{
		Title:     "Session Dashboard",
		Color:     embedColorGreen,
		Fields:    fields,
		Footer:    &discord.EmbedFooter{Text: "Live session"},
		Timestamp: &now,
	}
}

// buildEndedEmbed creates the final "session ended" embed.
func buildEndedEmbed(data SessionData, snap Snapshot) discord.Embed {
	duration := time.Since(data.StartedAt()).Truncate(time.Second)

	fields := []discord.EmbedField{
		{Name: "Campaign", Value: data.CampaignName(), Inline: new(true)},
		{Name: "Session ID", Value: fmt.Sprintf("`%s`", data.SessionID()), Inline: new(true)},
		{Name: "Duration", Value: duration.String(), Inline: new(true)},
		{Name: "Utterances", Value: fmt.Sprintf("%d", snap.Utterances), Inline: new(true)},
		{Name: "Errors", Value: fmt.Sprintf("%d", snap.Errors), Inline: new(true)},
		{Name: "Memory Entries", Value: fmt.Sprintf("%d", data.MemoryEntries()), Inline: new(true)},
	}

	now := time.Now().UTC()
	return discord.Embed{
		Title:       "Session Dashboard",
		Description: "Session has ended.",
		Color:       embedColorRed,
		Fields:      fields,
		Footer:      &discord.EmbedFooter{Text: "Session ended"},
		Timestamp:   &now,
	}
}

// formatLatencyField builds a compact multi-line string showing pipeline
// latencies. Returns empty string if no latency data is available.
func formatLatencyField(snap Snapshot) string {
	var lines []string
	if snap.STT.P50 > 0 || snap.STT.P95 > 0 {
		lines = append(lines, fmt.Sprintf("STT: p50=%s p95=%s", formatMs(snap.STT.P50), formatMs(snap.STT.P95)))
	}
	if snap.LLM.P50 > 0 || snap.LLM.P95 > 0 {
		lines = append(lines, fmt.Sprintf("LLM: p50=%s p95=%s", formatMs(snap.LLM.P50), formatMs(snap.LLM.P95)))
	}
	if snap.TTS.P50 > 0 || snap.TTS.P95 > 0 {
		lines = append(lines, fmt.Sprintf("TTS: p50=%s p95=%s", formatMs(snap.TTS.P50), formatMs(snap.TTS.P95)))
	}
	if snap.S2S.P50 > 0 || snap.S2S.P95 > 0 {
		lines = append(lines, fmt.Sprintf("Total: p50=%s p95=%s", formatMs(snap.S2S.P50), formatMs(snap.S2S.P95)))
	}
	if len(lines) == 0 {
		return ""
	}
	var result strings.Builder
	result.WriteString("```\n")
	for _, line := range lines {
		result.WriteString(line + "\n")
	}
	result.WriteString("```")
	return result.String()
}

// formatMs formats a duration as milliseconds with one decimal place.
func formatMs(d time.Duration) string {
	ms := float64(d) / float64(time.Millisecond)
	return fmt.Sprintf("%.1fms", ms)
}

// formatDuration formats a duration as "Xh Ym Zs".
func formatDuration(d time.Duration) string {
	d = d.Truncate(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
