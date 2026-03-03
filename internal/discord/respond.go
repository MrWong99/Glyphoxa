package discord

import (
	"fmt"
	"log/slog"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// messageCreator is implemented by ApplicationCommandInteractionCreate,
// ComponentInteractionCreate, and ModalSubmitInteractionCreate.
type messageCreator interface {
	CreateMessage(discord.MessageCreate, ...rest.RequestOpt) error
	DeferCreateMessage(bool, ...rest.RequestOpt) error
}

// modalResponder is implemented by ApplicationCommandInteractionCreate
// and ComponentInteractionCreate.
type modalResponder interface {
	Modal(discord.ModalCreate, ...rest.RequestOpt) error
}

// followUpContext provides the fields needed for follow-up messages.
type followUpContext interface {
	Client() *bot.Client
	ApplicationID() snowflake.ID
	Token() string
}

// RespondEphemeral sends an ephemeral text response to an interaction.
func RespondEphemeral(e messageCreator, content string) {
	err := e.CreateMessage(discord.MessageCreate{
		Content: content,
		Flags:   discord.MessageFlagEphemeral,
	})
	if err != nil {
		slog.Warn("discord: failed to send ephemeral response", "err", err)
	}
}

// RespondEmbed sends an ephemeral embed response to an interaction.
func RespondEmbed(e messageCreator, embed discord.Embed) {
	err := e.CreateMessage(discord.MessageCreate{
		Embeds: []discord.Embed{embed},
		Flags:  discord.MessageFlagEphemeral,
	})
	if err != nil {
		slog.Warn("discord: failed to send embed response", "err", err)
	}
}

// RespondError sends a formatted error response (ephemeral).
func RespondError(e messageCreator, err error) {
	RespondEphemeral(e, fmt.Sprintf("Error: %v", err))
}

// RespondModal opens a modal dialog.
func RespondModal(e modalResponder, modal discord.ModalCreate) {
	if err := e.Modal(modal); err != nil {
		slog.Warn("discord: failed to open modal", "err", err)
	}
}

// DeferReply sends a deferred response (for long-running commands).
func DeferReply(e messageCreator) {
	if err := e.DeferCreateMessage(true); err != nil {
		slog.Warn("discord: failed to defer reply", "err", err)
	}
}

// FollowUp sends a follow-up message after a deferred response.
func FollowUp(e followUpContext, content string) {
	_, err := e.Client().Rest.CreateFollowupMessage(e.ApplicationID(), e.Token(), discord.MessageCreate{
		Content: content,
		Flags:   discord.MessageFlagEphemeral,
	})
	if err != nil {
		slog.Warn("discord: failed to send follow-up", "err", err)
	}
}

// FollowUpEmbed sends an embed follow-up message after a deferred response.
func FollowUpEmbed(e followUpContext, embed discord.Embed) {
	_, err := e.Client().Rest.CreateFollowupMessage(e.ApplicationID(), e.Token(), discord.MessageCreate{
		Embeds: []discord.Embed{embed},
		Flags:  discord.MessageFlagEphemeral,
	})
	if err != nil {
		slog.Warn("discord: failed to send embed follow-up", "err", err)
	}
}
