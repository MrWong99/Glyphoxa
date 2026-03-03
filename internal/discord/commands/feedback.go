// Package commands implements Discord slash command handlers for the Glyphoxa
// DM experience.
package commands

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
)

const (
	feedbackModalID = "feedback_modal"
)

// FeedbackStore persists post-session feedback.
type FeedbackStore interface {
	SaveFeedback(sessionID string, feedback Feedback) error
}

// Feedback holds a DM's post-session feedback ratings and comments.
type Feedback struct {
	SessionID      string
	UserID         string
	VoiceLatency   int // 1-5
	NPCPersonality int // 1-5
	MemoryAccuracy int // 1-5
	DMWorkflow     int // 1-5
	Comments       string
}

// FeedbackCommands handles the /feedback slash command.
type FeedbackCommands struct {
	perms        *discordbot.PermissionChecker
	store        FeedbackStore
	getSessionID func() string // returns current or last session ID
}

// NewFeedbackCommands creates a FeedbackCommands handler.
func NewFeedbackCommands(perms *discordbot.PermissionChecker, store FeedbackStore, getSessionID func() string) *FeedbackCommands {
	return &FeedbackCommands{
		perms:        perms,
		store:        store,
		getSessionID: getSessionID,
	}
}

// Register registers the /feedback command and modal handler with the router.
func (fc *FeedbackCommands) Register(router *discordbot.CommandRouter) {
	router.RegisterCommand("feedback", fc.Definition(), fc.handleFeedback)
	router.RegisterModal(feedbackModalID, fc.handleFeedbackModal)
}

// Definition returns the /feedback SlashCommandCreate for Discord registration.
func (fc *FeedbackCommands) Definition() discord.SlashCommandCreate {
	return discord.SlashCommandCreate{
		Name:        "feedback",
		Description: "Submit post-session feedback",
	}
}

// handleFeedback opens the feedback modal.
func (fc *FeedbackCommands) handleFeedback(e *events.ApplicationCommandInteractionCreate) {
	sessionID := fc.getSessionID()
	if sessionID == "" {
		discordbot.RespondEphemeral(e, "No session has been run yet. Start and stop a session before submitting feedback.")
		return
	}

	minLen := 1
	discordbot.RespondModal(e, discord.ModalCreate{
		CustomID: feedbackModalID,
		Title:    "Session Feedback",
		Components: []discord.LayoutComponent{
			discord.NewLabel("Voice latency (1=terrible, 5=great)", discord.TextInputComponent{
				CustomID:    "voice_latency",
				Style:       discord.TextInputStyleShort,
				Placeholder: "1-5",
				Required:    true,
				MinLength:   &minLen,
				MaxLength:   1,
			}),
			discord.NewLabel("NPC personality quality (1-5)", discord.TextInputComponent{
				CustomID:    "npc_personality",
				Style:       discord.TextInputStyleShort,
				Placeholder: "1-5",
				Required:    true,
				MinLength:   &minLen,
				MaxLength:   1,
			}),
			discord.NewLabel("Memory accuracy (1-5)", discord.TextInputComponent{
				CustomID:    "memory_accuracy",
				Style:       discord.TextInputStyleShort,
				Placeholder: "1-5",
				Required:    true,
				MinLength:   &minLen,
				MaxLength:   1,
			}),
			discord.NewLabel("DM workflow (1-5)", discord.TextInputComponent{
				CustomID:    "dm_workflow",
				Style:       discord.TextInputStyleShort,
				Placeholder: "1-5",
				Required:    true,
				MinLength:   &minLen,
				MaxLength:   1,
			}),
			discord.NewLabel("Comments (optional)", discord.TextInputComponent{
				CustomID:    "comments",
				Style:       discord.TextInputStyleParagraph,
				Placeholder: "What worked well? What needs improvement?",
				MaxLength:   1000,
			}),
		},
	})
}

// handleFeedbackModal processes the submitted feedback form.
func (fc *FeedbackCommands) handleFeedbackModal(e *events.ModalSubmitInteractionCreate) {
	fb := Feedback{
		SessionID:      fc.getSessionID(),
		UserID:         e.User().ID.String(),
		VoiceLatency:   parseRating(e.Data.Text("voice_latency")),
		NPCPersonality: parseRating(e.Data.Text("npc_personality")),
		MemoryAccuracy: parseRating(e.Data.Text("memory_accuracy")),
		DMWorkflow:     parseRating(e.Data.Text("dm_workflow")),
		Comments:       strings.TrimSpace(e.Data.Text("comments")),
	}

	if fc.store != nil {
		if err := fc.store.SaveFeedback(fb.SessionID, fb); err != nil {
			slog.Error("discord: failed to save feedback", "error", err)
			discordbot.RespondEphemeral(e, fmt.Sprintf("Failed to save feedback: %v", err))
			return
		}
	}

	avg := float64(fb.VoiceLatency+fb.NPCPersonality+fb.MemoryAccuracy+fb.DMWorkflow) / 4.0
	discordbot.RespondEphemeral(e, fmt.Sprintf(
		"Thank you for your feedback! Average rating: %.1f/5\n\nSession: `%s`",
		avg, fb.SessionID,
	))
}

// parseRating converts a string to an int rating (1-5), clamping to range.
func parseRating(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 {
		return 1
	}
	if n > 5 {
		return 5
	}
	return n
}
