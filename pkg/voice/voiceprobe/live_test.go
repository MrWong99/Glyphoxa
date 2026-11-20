//go:build opus && dave && live

// The live tier: a real probe bot speaks to a real Glyphoxa Bot over Discord.
// Never part of a PR gate — it needs a second bot token and a Voice Session
// already running in the channel. Run it by hand or from a scheduled job:
//
//	GLYPHOXA_PROBE_BOT_TOKEN=… GX_PROBE_GUILD=… GX_PROBE_CHANNEL=… \
//	GX_PROBE_REPLY_FROM=<Bot user ID> GX_PROBE_CLIP=path/to/addressed.wav \
//	go test -tags "opus dave nolibopusfile live" -run TestLive -v ./pkg/voice/voiceprobe/
//
// GX_PROBE_CLIP must address an NPC (or the Butler) the running session will
// answer; it defaults to the Butler-addressed hello-test clip.
// GX_PROBE_MAX_LATENCY (a Go duration) optionally bounds the reply latency.
package voiceprobe

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"
)

func TestLive_BotRepliesToAddressedClip(t *testing.T) {
	token := os.Getenv("GLYPHOXA_PROBE_BOT_TOKEN")
	guildEnv, channelEnv := os.Getenv("GX_PROBE_GUILD"), os.Getenv("GX_PROBE_CHANNEL")
	if token == "" || guildEnv == "" || channelEnv == "" {
		t.Skip("set GLYPHOXA_PROBE_BOT_TOKEN, GX_PROBE_GUILD and GX_PROBE_CHANNEL to run the live probe")
	}
	guild, err := snowflake.Parse(guildEnv)
	if err != nil {
		t.Fatalf("GX_PROBE_GUILD: %v", err)
	}
	channel, err := snowflake.Parse(channelEnv)
	if err != nil {
		t.Fatalf("GX_PROBE_CHANNEL: %v", err)
	}
	var replyFrom snowflake.ID
	if v := os.Getenv("GX_PROBE_REPLY_FROM"); v != "" {
		if replyFrom, err = snowflake.Parse(v); err != nil {
			t.Fatalf("GX_PROBE_REPLY_FROM: %v", err)
		}
	} else {
		t.Log("GX_PROBE_REPLY_FROM is unset: any speaker in the channel counts as the reply")
	}
	clipPath := os.Getenv("GX_PROBE_CLIP")
	if clipPath == "" {
		clipPath = "../../../tests/voice-clips/hello-test/audio.wav"
	}
	clip, err := LoadWAV(clipPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	p, err := Join(ctx, Config{Token: token, Guild: guild, Channel: channel, ReplyFrom: replyFrom})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Listen(ctx, 5*time.Second) // DAVE handshake, any join greeting

	reply, err := p.Say(ctx, clip)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Heard {
		t.Fatalf("no reply to %s — is a Voice Session running in the channel, and does the clip address one of its NPCs?", clipPath)
	}
	t.Logf("reply started %s after the clip, lasted %s", reply.Latency.Round(time.Millisecond), reply.Duration.Round(time.Millisecond))

	out := filepath.Join(t.TempDir(), "reply.wav")
	if dir := os.Getenv("GX_PROBE_OUT"); dir != "" {
		out = filepath.Join(dir, "reply.wav")
	}
	if err := WriteWAV(out, reply.PCM, SampleRate); err != nil {
		t.Fatal(err)
	}
	if reply.Duration < 300*time.Millisecond {
		t.Errorf("reply lasted only %s — a blip, not speech", reply.Duration)
	}
	if v := os.Getenv("GX_PROBE_MAX_LATENCY"); v != "" {
		limit, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("GX_PROBE_MAX_LATENCY: %v", err)
		}
		if reply.Latency > limit {
			t.Errorf("reply latency %s exceeds %s", reply.Latency, limit)
		}
	}
}
