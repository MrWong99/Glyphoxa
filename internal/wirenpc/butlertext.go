package wirenpc

import (
	"context"
	"fmt"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// discordMessageLimit is Discord's per-message character cap (#299): a Butler
// text answer longer than this is split across several ordered messages.
const discordMessageLimit = 2000

// newVoiceChannelPoster builds the Butler's text-delivery sink (#299, #297
// decision 2): a func that posts text into the voice channel's text chat over the
// borrowed Discord client, splitting answers longer than [discordMessageLimit]
// runes into ordered messages. The ctx bounds each REST call (a barge cancels a
// mid-post). It is wired as the [agent.Config.TextSink] on butler-role specs only.
func newVoiceChannelPoster(client *bot.Client, channel snowflake.ID) func(ctx context.Context, text string) error {
	return func(ctx context.Context, text string) error {
		for _, part := range splitDiscordMessage(text, discordMessageLimit) {
			if _, err := client.Rest.CreateMessage(channel, discord.MessageCreate{Content: part}, rest.WithCtx(ctx)); err != nil {
				return fmt.Errorf("wirenpc: post Butler text to channel %s: %w", channel, err)
			}
		}
		return nil
	}
}

// splitDiscordMessage breaks text into ordered chunks each at most limit RUNES
// (never bytes — a German Butler answer), preferring a newline then a space break
// so a chunk ends at a natural boundary; a single unbroken run longer than limit
// is hard-cut. Every rune is delivered (never truncated). It mirrors the recap
// splitter's contract (internal/presence.splitFollowups); it is duplicated rather
// than imported because internal/presence imports wirenpc (an import cycle).
func splitDiscordMessage(text string, limit int) []string {
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return []string{text}
	}
	var parts []string
	for len(runes) > limit {
		cut := limit
		if i := lastBreakRune(runes, limit, '\n'); i > 0 {
			cut = i
		} else if i := lastBreakRune(runes, limit, ' '); i > 0 {
			cut = i
		}
		parts = append(parts, string(runes[:cut]))
		rest := runes[cut:]
		if cut < limit && len(rest) > 0 && (rest[0] == '\n' || rest[0] == ' ') {
			rest = rest[1:] // drop the boundary whitespace we broke on
		}
		runes = rest
	}
	if len(runes) > 0 {
		parts = append(parts, string(runes))
	}
	return parts
}

// lastBreakRune returns the last index in [1, limit) where runes[i]==ch, or -1.
func lastBreakRune(runes []rune, limit int, ch rune) int {
	for i := limit - 1; i > 0; i-- {
		if runes[i] == ch {
			return i
		}
	}
	return -1
}
