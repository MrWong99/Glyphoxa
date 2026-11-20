// Command voiceprobe is the command-line face of [voiceprobe]: a second Discord
// bot that joins a voice channel, speaks WAV clips into it, and records what the
// Glyphoxa Bot says back. See the package for what it does and does not cover.
//
// Build (same tags as the product binary; needs libopus for the encoder):
//
//	CGO_ENABLED=1 go build -tags "opus dave nolibopusfile" -o bin/voiceprobe ./cmd/voiceprobe
//
// Run (the token is read from the environment, never from a flag):
//
//	GLYPHOXA_PROBE_BOT_TOKEN=… bin/voiceprobe -guild G -channel C -reply-from BOT \
//	    -require-reply -max-latency 4s -out /tmp/probe clip1.wav clip2.wav
//
// Exit status: 0 on success, 1 on an operational error (bad flags, could not
// join, could not play), 2 when -require-reply or -max-latency was not met.
// Recordings and summary.json are written either way.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceprobe"
)

const tokenEnv = "GLYPHOXA_PROBE_BOT_TOKEN"

// errExpectation marks an unmet -require-reply / -max-latency (exit status 2).
var errExpectation = errors.New("expectation not met")

func main() {
	err := run()
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "voiceprobe:", err)
	if errors.Is(err, errExpectation) {
		os.Exit(2)
	}
	os.Exit(1)
}

// Summary is the machine-readable result written to summary.json.
type Summary struct {
	Guild           string          `json:"guild"`
	Channel         string          `json:"channel"`
	DurationSeconds float64         `json:"duration_seconds"`
	Clips           []ClipResult    `json:"clips"`
	Speakers        []SpeakerResult `json:"speakers"`
	Failures        []string        `json:"failures,omitempty"`
}

// ClipResult is what happened after one spoken clip.
type ClipResult struct {
	Clip                string  `json:"clip"`
	ClipSeconds         float64 `json:"clip_seconds"`
	Replied             bool    `json:"replied"`
	ReplyLatencySeconds float64 `json:"reply_latency_seconds,omitempty"`
	ReplySeconds        float64 `json:"reply_seconds,omitempty"`
	ReplyFile           string  `json:"reply_file,omitempty"`
	// Barge-in mode only: the clip whose reply this clip interrupted, and how
	// long the Bot kept talking once the interruption began.
	Interrupted     string  `json:"interrupted,omitempty"`
	TalkoverSeconds float64 `json:"talkover_seconds,omitempty"`
}

// SpeakerResult is one recorded speaker.
type SpeakerResult struct {
	UserID       string  `json:"user_id"`
	File         string  `json:"file"`
	AudioSeconds float64 `json:"audio_seconds"`
	Frames       int     `json:"frames"`
	DecodeErrors int     `json:"decode_errors"`
}

func run() error {
	var (
		guildFlag    = flag.String("guild", "", "guild (server) snowflake")
		channelFlag  = flag.String("channel", "", "voice channel snowflake")
		replyFrom    = flag.String("reply-from", "", "only count audio from this user ID as a reply (default: anyone)")
		outDir       = flag.String("out", "voiceprobe-out", "directory for recorded WAVs and summary.json")
		settle       = flag.Duration("settle", 5*time.Second, "wait after joining before the first clip (DAVE handshake, Bot greeting)")
		quiet        = flag.Duration("quiet", 4*time.Second, "a reply is over after this much silence")
		maxWait      = flag.Duration("max-wait", 45*time.Second, "give up waiting for a reply to one clip after this long")
		linger       = flag.Duration("linger", 0, "keep listening this long after the last clip (soak, unprompted speech)")
		requireReply = flag.Bool("require-reply", false, "exit 2 unless every clip got a reply")
		maxLatency   = flag.Duration("max-latency", 0, "exit 2 if any reply started later than this after its clip (0 = no limit)")
		bargeAfter   = flag.Duration("barge-after", 0, "barge-in mode: clips are taken in pairs, the second spoken over the reply to the first once it has run this long (0 = off)")
		maxTalkover  = flag.Duration("max-talkover", 0, "exit 2 if the Bot kept talking longer than this after being interrupted (0 = no limit)")
		verbose      = flag.Bool("v", false, "log disgo/voice internals")
	)
	flag.Parse()

	cfg := voiceprobe.Config{Token: os.Getenv(tokenEnv), Quiet: *quiet, MaxWait: *maxWait}
	if cfg.Token == "" {
		return fmt.Errorf("%s is not set", tokenEnv)
	}
	var err error
	if cfg.Guild, err = snowflake.Parse(*guildFlag); err != nil {
		return fmt.Errorf("-guild: %w", err)
	}
	if cfg.Channel, err = snowflake.Parse(*channelFlag); err != nil {
		return fmt.Errorf("-channel: %w", err)
	}
	if *replyFrom != "" {
		if cfg.ReplyFrom, err = snowflake.Parse(*replyFrom); err != nil {
			return fmt.Errorf("-reply-from: %w", err)
		}
	}
	// Load every clip before joining: a typo should not cost a voice join.
	var clips []voiceprobe.Clip
	for _, path := range flag.Args() {
		clip, err := voiceprobe.LoadWAV(path)
		if err != nil {
			return err
		}
		clips = append(clips, clip)
	}
	if *bargeAfter > 0 && len(clips)%2 != 0 {
		return errors.New("-barge-after takes clips in pairs: a question, then the interruption")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	level := slog.LevelWarn
	if *verbose {
		level = slog.LevelDebug
	}
	cfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p, err := voiceprobe.Join(ctx, cfg)
	if err != nil {
		return err
	}
	defer p.Close()
	say := func(format string, a ...any) {
		fmt.Printf("[%6.1fs] %s\n", p.Elapsed().Seconds(), fmt.Sprintf(format, a...))
	}
	say("joined voice channel %s", cfg.Channel)
	p.Listen(ctx, *settle)

	summary := Summary{Guild: cfg.Guild.String(), Channel: cfg.Channel.String()}
	var runErr error
	for i := 0; i < len(clips); i++ {
		clip := clips[i]
		say("speaking %s (%.1fs)", clip.Name, clip.Duration().Seconds())
		var reply voiceprobe.Reply
		var err error
		res := ClipResult{Clip: clip.Name, ClipSeconds: clip.Duration().Seconds()}
		if *bargeAfter > 0 {
			over := clips[i+1]
			i++
			var barge voiceprobe.Barge
			if barge, err = p.Interrupt(ctx, clip, over, *bargeAfter); err == nil && barge.Heard {
				reply = barge.Reply
				res.Clip, res.ClipSeconds = over.Name, over.Duration().Seconds()
				res.Interrupted, res.TalkoverSeconds = clip.Name, barge.TalkedOver.Seconds()
				if !barge.Interrupted {
					say("the reply to %s had already ended when %s was spoken: nothing was interrupted", clip.Name, over.Name)
					summary.Failures = append(summary.Failures,
						fmt.Sprintf("%s: the reply to %s ended before the interruption; use a clip that draws a longer reply or a shorter -barge-after", over.Name, clip.Name))
				} else {
					say("interrupted the reply to %s with %s: the Bot talked on for %.2fs", clip.Name, over.Name, res.TalkoverSeconds)
				}
				if barge.Interrupted && *maxTalkover > 0 && barge.TalkedOver > *maxTalkover {
					summary.Failures = append(summary.Failures,
						fmt.Sprintf("%s: the Bot talked over the interruption for %s, more than %s", over.Name, barge.TalkedOver.Round(time.Millisecond), *maxTalkover))
				}
			}
		} else {
			reply, err = p.Say(ctx, clip)
		}
		if err != nil {
			runErr = err
			break
		}
		res.Replied = reply.Heard
		switch {
		case !reply.Heard:
			say("NO reply within %s", *maxWait)
			if *requireReply {
				summary.Failures = append(summary.Failures, fmt.Sprintf("%s: no reply within %s", clip.Name, *maxWait))
			}
		default:
			res.ReplyLatencySeconds = reply.Latency.Seconds()
			res.ReplySeconds = reply.Duration.Seconds()
			res.ReplyFile = filepath.Join(*outDir, fmt.Sprintf("reply-%02d.wav", i+1))
			if err := voiceprobe.WriteWAV(res.ReplyFile, reply.PCM, voiceprobe.SampleRate); err != nil {
				return err
			}
			say("reply: started %.2fs after the clip ended, lasted %.1fs", res.ReplyLatencySeconds, res.ReplySeconds)
			if *maxLatency > 0 && reply.Latency > *maxLatency {
				summary.Failures = append(summary.Failures,
					fmt.Sprintf("%s: reply latency %s exceeds %s", clip.Name, reply.Latency.Round(time.Millisecond), *maxLatency))
			}
		}
		summary.Clips = append(summary.Clips, res)
	}
	if runErr == nil && *linger > 0 {
		say("lingering %s", *linger)
		p.Listen(ctx, *linger)
	}
	_ = p.Close()

	for _, t := range p.Recording() {
		file := filepath.Join(*outDir, "speaker-"+t.UserID.String()+".wav")
		if err := voiceprobe.WriteWAV(file, t.PCM, voiceprobe.SampleRate); err != nil {
			return err
		}
		say("recorded %.1fs of audio from user %s -> %s", t.Voiced.Seconds(), t.UserID, file)
		summary.Speakers = append(summary.Speakers, SpeakerResult{
			UserID: t.UserID.String(), File: file, AudioSeconds: t.Voiced.Seconds(),
			Frames: t.Frames, DecodeErrors: t.DecodeErrors,
		})
	}
	summary.DurationSeconds = p.Elapsed().Seconds()
	b, _ := json.MarshalIndent(summary, "", "  ")
	if err := os.WriteFile(filepath.Join(*outDir, "summary.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	if runErr != nil {
		return runErr
	}
	if len(summary.Failures) > 0 {
		for _, f := range summary.Failures {
			fmt.Fprintln(os.Stderr, "FAIL:", f)
		}
		return errExpectation
	}
	return nil
}
