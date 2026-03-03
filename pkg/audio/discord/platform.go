// Package discord provides an [audio.Platform] implementation backed by
// Discord voice channels via the disgoorg/disgo library. It bridges
// Discord's Opus-based voice transport with Glyphoxa's PCM [audio.AudioFrame]
// pipeline.
//
// The platform requires a [voice.Manager] (owned by the bot layer) and a guild
// ID. Each call to [Platform.Connect] joins the specified voice channel and
// returns a [Connection] that demuxes per-participant audio input and muxes NPC
// audio output.
package discord

import (
	"context"
	"fmt"

	"github.com/MrWong99/glyphoxa/pkg/audio"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// Compile-time interface assertion.
var _ audio.Platform = (*Platform)(nil)

// Platform implements [audio.Platform] using a disgo voice connection.
// It requires a [voice.Manager] (owned by the bot layer).
//
// Platform is safe for concurrent use.
type Platform struct {
	voiceMgr voice.Manager
	guildID  snowflake.ID
}

// New creates a new Discord Platform for the given voice manager and guild.
func New(voiceMgr voice.Manager, guildID snowflake.ID) *Platform {
	return &Platform{
		voiceMgr: voiceMgr,
		guildID:  guildID,
	}
}

// Connect joins the voice channel identified by channelID and returns an active
// [audio.Connection]. The supplied ctx governs the connection-setup phase only;
// once the Connection is returned it lives until [Connection.Disconnect] is called.
func (p *Platform) Connect(ctx context.Context, channelID string) (audio.Connection, error) {
	chID, err := snowflake.Parse(channelID)
	if err != nil {
		return nil, fmt.Errorf("discord: parse channel ID: %w", err)
	}

	conn := p.voiceMgr.CreateConn(p.guildID)
	if err := conn.Open(ctx, chID, false, false); err != nil {
		p.voiceMgr.RemoveConn(p.guildID)
		return nil, fmt.Errorf("discord: open voice connection: %w", err)
	}

	c := newConnection(conn, p.guildID)
	return c, nil
}
