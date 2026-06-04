// Command glyphoxa is the Glyphoxa v2 binary. In v1.0 it runs one Mode at a
// time; this MVP slice ships the `voice` mode that joins a Discord voice
// channel and gives one Character NPC a live voice loop (issue #1–#5), plus the
// `migrate` subcommand (ADR-0031) that applies the schema migrations.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// `migrate` and `seed` are subcommands with their own argument grammar,
	// dispatched before flag parsing. The full Mode dispatcher (all/web) and
	// root command surface belong to the control-plane task (#6); this slice
	// wires `migrate` (ADR-0031), `seed` (task #5), and the `voice` mode.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "migrate":
			if err := RunMigrate(context.Background(), os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "seed":
			if err := RunSeed(context.Background(), log, os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}

	mode := flag.String("mode", "voice", "process mode: voice")
	var cfg wirenpc.Config
	flag.StringVar(&cfg.Guild, "guild", "", "Discord guild (server) snowflake ID")
	flag.StringVar(&cfg.Channel, "channel", "", "Discord voice channel snowflake ID")
	fromDB := flag.Bool("db", false, "load the NPC from the database (seed it first with `glyphoxa seed`) instead of the in-code default")
	flag.Parse()

	switch *mode {
	case "voice":
		if err := runVoice(log, cfg, *fromDB); err != nil {
			log.Error("voice mode exited with error", "err", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q (only \"voice\" is supported in this slice)\n", *mode)
		os.Exit(2)
	}
}

// runVoice resolves runtime credentials from the environment, builds the live
// NPC voice loop, and runs it until SIGINT/SIGTERM. Credentials are never
// compiled in: DISCORD_BOT_TOKEN, plus the provider keys the STT/TTS/LLM
// adapters read from their own env vars / keyring (the encrypted provider_config
// credential is the web-app BYOK path, not the self-host voice path).
//
// When fromDB is set, the NPC's Persona/Voice/identity load from Postgres
// ($GLYPHOXA_DATABASE_URL) via the task-#5 path; otherwise the in-code NPC runs.
func runVoice(log *slog.Logger, cfg wirenpc.Config, fromDB bool) error {
	cfg.Token = os.Getenv("DISCORD_BOT_TOKEN")
	if cfg.Token == "" {
		return fmt.Errorf("DISCORD_BOT_TOKEN is not set")
	}
	if cfg.Guild == "" || cfg.Channel == "" {
		return fmt.Errorf("-guild and -channel are required for voice mode")
	}
	cfg.Logger = log

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if fromDB {
		dsn := databaseURL()
		if dsn == "" {
			return fmt.Errorf("-db requires $GLYPHOXA_DATABASE_URL (or $DATABASE_URL)")
		}
		return wirenpc.RunFromDB(ctx, cfg, dsn)
	}
	return wirenpc.Run(ctx, cfg)
}
