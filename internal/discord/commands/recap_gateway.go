package commands

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	gw "github.com/MrWong99/glyphoxa/internal/gateway"
	"github.com/MrWong99/glyphoxa/internal/session"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	"github.com/MrWong99/glyphoxa/pkg/provider/llm"
)

// GatewayRecapCommands handles the /session recap slash command in gateway mode.
// It reads transcripts from the shared PostgreSQL session store and optionally
// summarises them via the Summariser. Voice recap is text-only for MVP.
type GatewayRecapCommands struct {
	ctrl         gw.SessionController
	npcCtrl      gw.NPCController
	perms        *discordbot.PermissionChecker
	sessionStore memory.SessionStore
	summariser   session.Summariser
}

// GatewayRecapConfig holds dependencies for creating GatewayRecapCommands.
type GatewayRecapConfig struct {
	GatewayBot   *gw.GatewayBot
	Ctrl         gw.SessionController
	NPCCtrl      gw.NPCController
	Perms        *discordbot.PermissionChecker
	SessionStore memory.SessionStore
	Summariser   session.Summariser // optional; if nil, raw transcript is shown
}

// NewGatewayRecapCommands creates a GatewayRecapCommands and registers the
// recap handler with the gateway bot's router.
func NewGatewayRecapCommands(cfg GatewayRecapConfig) *GatewayRecapCommands {
	rc := &GatewayRecapCommands{
		ctrl:         cfg.Ctrl,
		npcCtrl:      cfg.NPCCtrl,
		perms:        cfg.Perms,
		sessionStore: cfg.SessionStore,
		summariser:   cfg.Summariser,
	}
	rc.Register(cfg.GatewayBot.Router())
	return rc
}

// Register registers the /session recap subcommand handler with the router.
// The parent /session command definition is expected to be already registered
// by GatewaySessionCommands; this only adds the handler for the recap subcommand.
func (rc *GatewayRecapCommands) Register(router *discordbot.CommandRouter) {
	router.RegisterHandler("session/recap", rc.handleRecap)
}

// handleRecap handles /session recap.
func (rc *GatewayRecapCommands) handleRecap(e *events.ApplicationCommandInteractionCreate) {
	if !rc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to view session recaps.")
		return
	}

	// Resolve session ID: explicit param > active session > most recent from store.
	data := e.SlashCommandInteractionData()
	sessionID, _ := data.OptString("session_id")

	guildStr := e.GuildID().String()
	var info gw.SessionInfo
	var hasInfo bool

	if sessionID == "" {
		if rc.ctrl.IsActive(guildStr) {
			info, hasInfo = rc.ctrl.Info(guildStr)
			sessionID = info.SessionID
		}
	} else {
		// Even with explicit session_id, try to get info for display.
		if rc.ctrl.IsActive(guildStr) {
			info, hasInfo = rc.ctrl.Info(guildStr)
		}
	}

	if sessionID == "" && rc.sessionStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		sessions, err := rc.sessionStore.ListSessions(ctx, 1)
		if err == nil && len(sessions) > 0 {
			sessionID = sessions[0].SessionID
		}
	}

	if sessionID == "" {
		discordbot.RespondEphemeral(e, "No session data available. Start a session first with `/session start`.")
		return
	}

	discordbot.DeferReply(e)

	status := "Ended"
	if hasInfo && rc.ctrl.IsActive(guildStr) {
		status = "Active"
	}

	npcList := rc.buildNPCList(info.SessionID)
	summary := rc.buildSummary(sessionID)

	fields := []discord.EmbedField{
		{Name: "Status", Value: status, Inline: new(true)},
		{Name: "Session ID", Value: fmt.Sprintf("`%s`", sessionID), Inline: new(true)},
	}

	if hasInfo {
		fields = append(fields,
			discord.EmbedField{Name: "Campaign", Value: info.CampaignName, Inline: new(true)},
			discord.EmbedField{Name: "Started By", Value: fmt.Sprintf("<@%s>", info.StartedBy), Inline: new(true)},
			discord.EmbedField{Name: "Duration", Value: time.Since(info.StartedAt).Truncate(time.Second).String(), Inline: new(true)},
			discord.EmbedField{Name: "Channel", Value: fmt.Sprintf("<#%s>", info.ChannelID), Inline: new(true)},
		)
	}

	if npcList != "" {
		fields = append(fields, discord.EmbedField{Name: "NPCs", Value: npcList})
	}

	if rc.sessionStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		count, err := rc.sessionStore.EntryCount(ctx, sessionID)
		if err != nil {
			slog.Warn("recap: failed to get entry count", "session_id", sessionID, "err", err)
		} else {
			fields = append(fields, discord.EmbedField{
				Name: "Transcript Entries", Value: fmt.Sprintf("%d", count), Inline: new(true),
			})
		}
	}

	embeds := buildGatewayRecapEmbeds(fields, summary)

	if len(embeds) > 0 {
		discordbot.FollowUpEmbed(e, embeds[0])
	}
	for _, extra := range embeds[1:] {
		discordbot.FollowUpEmbed(e, extra)
	}
}

// buildNPCList fetches NPC status from the worker via gRPC and formats it.
func (rc *GatewayRecapCommands) buildNPCList(sessionID string) string {
	if sessionID == "" || rc.npcCtrl == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	npcs, err := rc.npcCtrl.ListNPCs(ctx, sessionID)
	if err != nil {
		slog.Warn("recap: failed to list npcs", "session_id", sessionID, "err", err)
		return "NPC data unavailable."
	}
	if len(npcs) == 0 {
		return "No NPCs active."
	}

	var sb strings.Builder
	for _, n := range npcs {
		muteLabel := ""
		if n.Muted {
			muteLabel = " (muted)"
		}
		fmt.Fprintf(&sb, "- %s%s\n", n.Name, muteLabel)
	}
	return sb.String()
}

// buildSummary retrieves the session transcript and optionally summarises it.
func (rc *GatewayRecapCommands) buildSummary(sessionID string) string {
	if rc.sessionStore == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	entries, err := rc.sessionStore.GetRecent(ctx, sessionID, 24*time.Hour)
	if err != nil {
		slog.Warn("recap: failed to get transcript", "session_id", sessionID, "err", err)
		return "Failed to retrieve transcript."
	}
	if len(entries) == 0 {
		return "No transcript entries recorded."
	}

	if rc.summariser != nil {
		messages := gatewayTranscriptToMessages(entries)
		summary, err := rc.summariser.Summarise(ctx, messages)
		if err != nil {
			slog.Warn("recap: summarisation failed, falling back to raw transcript",
				"session_id", sessionID, "err", err)
		} else if summary != "" {
			return summary
		}
	}

	return gatewayFormatTranscript(entries)
}

// gatewayTranscriptToMessages converts transcript entries to LLM messages.
func gatewayTranscriptToMessages(entries []memory.TranscriptEntry) []llm.Message {
	messages := make([]llm.Message, 0, len(entries))
	for _, e := range entries {
		role := "user"
		if e.IsNPC() {
			role = "assistant"
		}
		messages = append(messages, llm.Message{
			Role:    role,
			Name:    e.SpeakerName,
			Content: e.Text,
		})
	}
	return messages
}

// gatewayFormatTranscript creates a simple chronological transcript listing.
func gatewayFormatTranscript(entries []memory.TranscriptEntry) string {
	var sb strings.Builder
	for _, e := range entries {
		ts := e.Timestamp.Format("15:04:05")
		fmt.Fprintf(&sb, "**[%s] %s:** %s\n", ts, e.SpeakerName, e.Text)
	}
	result := sb.String()
	if len(result) > maxEmbedDescriptionLen-100 {
		result = result[:maxEmbedDescriptionLen-150] + "\n\n*... (truncated)*"
	}
	return result
}

// buildGatewayRecapEmbeds builds one or more embeds for the recap.
func buildGatewayRecapEmbeds(fields []discord.EmbedField, summary string) []discord.Embed {
	now := time.Now().UTC()

	if summary == "" {
		return []discord.Embed{{
			Title:     "Session Recap",
			Color:     recapColor,
			Fields:    fields,
			Footer:    &discord.EmbedFooter{Text: "Session recap"},
			Timestamp: &now,
		}}
	}

	if len(summary) <= maxEmbedDescriptionLen {
		return []discord.Embed{{
			Title:       "Session Recap",
			Description: summary,
			Color:       recapColor,
			Fields:      fields,
			Footer:      &discord.EmbedFooter{Text: "Session recap"},
			Timestamp:   &now,
		}}
	}

	var embeds []discord.Embed
	remaining := summary
	first := true
	for len(remaining) > 0 {
		chunk := remaining
		if len(chunk) > maxEmbedDescriptionLen {
			chunk = remaining[:maxEmbedDescriptionLen]
			remaining = remaining[maxEmbedDescriptionLen:]
		} else {
			remaining = ""
		}

		embed := discord.Embed{
			Description: chunk,
			Color:       recapColor,
		}
		if first {
			embed.Title = "Session Recap"
			embed.Fields = fields
			first = false
		}
		if remaining == "" {
			embed.Footer = &discord.EmbedFooter{Text: "Session recap"}
			embed.Timestamp = &now
		}
		embeds = append(embeds, embed)
	}
	return embeds
}
