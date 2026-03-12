package commands

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	"github.com/MrWong99/glyphoxa/internal/app"
	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	"github.com/MrWong99/glyphoxa/internal/session"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	"github.com/MrWong99/glyphoxa/pkg/provider/llm"
)

// recapColor is the embed sidebar color for recap embeds.
const recapColor = 0x3498DB

// maxEmbedDescriptionLen is the Discord embed description character limit.
const maxEmbedDescriptionLen = 4096

// RecapCommands handles the /session recap slash command.
type RecapCommands struct {
	sessionMgr   *app.SessionManager
	perms        *discordbot.PermissionChecker
	sessionStore memory.SessionStore
	summariser   session.Summariser
}

// RecapConfig holds dependencies for creating RecapCommands.
type RecapConfig struct {
	Bot          *discordbot.Bot
	SessionMgr   *app.SessionManager
	Perms        *discordbot.PermissionChecker
	SessionStore memory.SessionStore
	Summariser   session.Summariser // optional; if nil, raw transcript is shown
}

// NewRecapCommands creates a RecapCommands and registers the recap handler
// with the bot's router.
func NewRecapCommands(cfg RecapConfig) *RecapCommands {
	rc := &RecapCommands{
		sessionMgr:   cfg.SessionMgr,
		perms:        cfg.Perms,
		sessionStore: cfg.SessionStore,
		summariser:   cfg.Summariser,
	}
	rc.Register(cfg.Bot.Router())
	return rc
}

// Register registers the /session recap subcommand handler with the router.
// The parent /session command definition is expected to be already registered
// by SessionCommands; this only adds the handler for the recap subcommand.
func (rc *RecapCommands) Register(router *discordbot.CommandRouter) {
	router.RegisterHandler("session/recap", rc.handleRecap)
}

// handleRecap handles /session recap.
func (rc *RecapCommands) handleRecap(e *events.ApplicationCommandInteractionCreate) {
	if !rc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to view session recaps.")
		return
	}

	// Resolve session ID: explicit param > active session > most recent.
	var sessionID string
	var campaignName string

	data := e.SlashCommandInteractionData()
	if opt, ok := data.OptString("session_id"); ok && opt != "" {
		sessionID = opt
	}

	info := rc.sessionMgr.Info()

	if sessionID == "" {
		if info.SessionID != "" {
			sessionID = info.SessionID
			campaignName = info.CampaignName
		}
	} else if campaignName == "" {
		campaignName = info.CampaignName
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

	duration := time.Since(info.StartedAt).Truncate(time.Second)
	status := "Ended"
	if rc.sessionMgr.IsActive() {
		status = "Active"
	}

	_ = campaignName // used for future enrichment

	npcList := rc.buildNPCList()
	summary := rc.buildSummary(sessionID)

	fields := []discord.EmbedField{
		{Name: "Campaign", Value: info.CampaignName, Inline: new(true)},
		{Name: "Status", Value: status, Inline: new(true)},
		{Name: "Session ID", Value: fmt.Sprintf("`%s`", sessionID), Inline: new(true)},
		{Name: "Started By", Value: fmt.Sprintf("<@%s>", info.StartedBy), Inline: new(true)},
		{Name: "Duration", Value: duration.String(), Inline: new(true)},
		{Name: "Channel", Value: fmt.Sprintf("<#%s>", info.ChannelID), Inline: new(true)},
		{Name: "NPCs", Value: npcList},
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

	embeds := rc.buildRecapEmbeds(fields, summary)

	if len(embeds) > 0 {
		discordbot.FollowUpEmbed(e, embeds[0])
	}
	for _, extra := range embeds[1:] {
		discordbot.FollowUpEmbed(e, extra)
	}
}

// buildNPCList returns a formatted list of active NPCs with mute state.
func (rc *RecapCommands) buildNPCList() string {
	if !rc.sessionMgr.IsActive() {
		return "Session not active."
	}
	orch := rc.sessionMgr.Orchestrator()
	if orch == nil {
		return "No NPC data available."
	}
	agents := orch.ActiveAgents()
	if len(agents) == 0 {
		return "No NPCs active."
	}

	var sb strings.Builder
	for _, a := range agents {
		muted, _ := orch.IsMuted(a.ID())
		muteLabel := ""
		if muted {
			muteLabel = " (muted)"
		}
		fmt.Fprintf(&sb, "- %s%s\n", a.Name(), muteLabel)
	}
	return sb.String()
}

// buildSummary retrieves the session transcript and optionally summarises it.
func (rc *RecapCommands) buildSummary(sessionID string) string {
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
		messages := transcriptToMessages(entries)
		summary, err := rc.summariser.Summarise(ctx, messages)
		if err != nil {
			slog.Warn("recap: summarisation failed, falling back to raw transcript",
				"session_id", sessionID, "err", err)
		} else if summary != "" {
			return summary
		}
	}

	return formatTranscript(entries)
}

// transcriptToMessages converts transcript entries to LLM messages for
// summarisation.
func transcriptToMessages(entries []memory.TranscriptEntry) []llm.Message {
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

// formatTranscript creates a simple chronological transcript listing.
func formatTranscript(entries []memory.TranscriptEntry) string {
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

// buildRecapEmbeds builds one or more embeds for the recap, splitting the
// summary across multiple embeds if it exceeds Discord's 4096-char limit.
func (rc *RecapCommands) buildRecapEmbeds(fields []discord.EmbedField, summary string) []discord.Embed {
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
