package wirenpc

import (
	"context"
	"fmt"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/MrWong99/Glyphoxa/internal/discordmsg"
)

// newVoiceChannelPoster builds the Butler's text-delivery sink (#299, #297
// decision 2): a func that posts text into the voice channel's text chat over the
// borrowed Discord client, splitting answers longer than [discordmsg.Limit]
// runes into ordered messages (the shared splitter the recap Followups use).
// The ctx bounds each REST call (a barge cancels a mid-post). It is wired as
// the [agent.Config.TextSink] on butler-role specs only.
func newVoiceChannelPoster(client *bot.Client, channel snowflake.ID) func(ctx context.Context, text string) error {
	return func(ctx context.Context, text string) error {
		for _, part := range discordmsg.Split(text, discordmsg.Limit) {
			if _, err := client.Rest.CreateMessage(channel, discord.MessageCreate{Content: part}, rest.WithCtx(ctx)); err != nil {
				return fmt.Errorf("wirenpc: post Butler text to channel %s: %w", channel, err)
			}
		}
		return nil
	}
}
