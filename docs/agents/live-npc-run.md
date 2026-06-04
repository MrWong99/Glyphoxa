# Running the live NPC (`voice` mode)

The `glyphoxa` binary's `voice` mode joins a Discord voice channel and gives one
hardcoded Character NPC ("Bart", the innkeeper) a live voice loop:

```
Session.Inbound (Opus) → [codec] → VAD (Silero) → STT (ElevenLabs)
  → Address Detection → Agent loop (Anthropic + dice tool) → TTS (ElevenLabs)
  → [codec] → Session.Play (Opus)
```

The reasoning pipeline (VAD → STT → routing → Agent loop → TTS) is wired and
covered by keyless cassette tests. The **audio codec** — Opus↔PCM transcoding,
48 kHz↔16 kHz resampling, and 20 ms reframing on both directions — is **not yet
built** (tracked separately). Until it lands, the binary connects and
constructs the whole pipeline but the audio loop exits immediately with
`wire: audio codec unavailable …` on the first inbound frame. The steps below
are the procedure to follow once a real `wire.Codec` is wired into
`internal/wirenpc` (replace `wire.UnavailableCodec()` in `wirenpc.Run`).

## Prerequisites

- **A Discord bot** with the **message content** and **voice** privileges, added
  to your test server, currently in (or able to join) a voice channel.
- **CGO** toolchain (`CGO_ENABLED=1`, a C compiler): Silero VAD uses ONNX
  Runtime via cgo. This is the canonical build mode (see `Makefile`).
- **Provider API keys** (BYOK, ADR-0004), supplied as environment variables —
  never compiled in:
  - `ANTHROPIC_API_KEY` — the LLM (Claude) the Agent loop calls.
  - `ELEVENLABS_API_KEY` — STT (scribe) and TTS (eleven_v3).
- **Discord IDs**: the target guild (server) and voice channel snowflake IDs
  (Discord → User Settings → Advanced → Developer Mode, then right-click → Copy
  ID).

## Build

```sh
# Default build: DAVE/MLS is a stub (DaveAvailable() == false); voice is
# unencrypted. Fine for local testing.
CGO_ENABLED=1 go build -o glyphoxa ./cmd/glyphoxa

# Real end-to-end DAVE/MLS encryption (mandatory on Discord since 2026-03-01 for
# production) needs the libdave native libs and the build tag:
make dave-libs
CGO_ENABLED=1 go build -tags dave -o glyphoxa ./cmd/glyphoxa
```

## Run

```sh
export DISCORD_BOT_TOKEN="<bot token>"
export ANTHROPIC_API_KEY="<claude key>"
export ELEVENLABS_API_KEY="<elevenlabs key>"

./glyphoxa -mode voice \
  -guild   <guild-snowflake-id> \
  -channel <voice-channel-snowflake-id>
```

The bot opens the Discord gateway, joins the channel, and logs
`joined voice channel … npc=Bart`. Stop with Ctrl-C (SIGINT) — it leaves the
channel and closes the session cleanly.

## What to expect (once the codec is wired)

1. Speak in the channel. Address Detection (the ADR-0024 scoring matcher) routes
   to Bart both when you **name him** — *"Bart, do you have a room?"* (or an
   alias: "innkeeper", "barkeep") — and, because he is the lone Character NPC
   and not Address-Only, when you say nothing addressed at all (the single-NPC
   fallback). Either way the utterance reaches his Agent loop.
2. The Agent loop assembles Hot Context (Bart's Persona + the recent transcript)
   and calls Claude; the reply is spoken back through ElevenLabs in Bart's
   voice.
3. Ask Bart to **roll dice** (*"Bart, roll a d20 for my luck"*) to exercise the
   tool-use loop: the model calls the `dice` tool, the result is fed back, and
   Bart narrates the outcome.

## Determinism note (tests vs. live)

Unit tests never touch live services: STT/TTS use the recorded cassettes
(`tests/voice-cassettes/`), and the LLM uses the prompt-hash LLM cassette
(`llm-*.yaml`) via `voicecassette.LoadLLM`. To refresh those against the live
providers after a prompt or model change, run the relevant tests with
`-tags=record` and the API keys set (see `pkg/voice/voicecassette`). The live
`voice` mode above is the only path that hits real Discord audio.
