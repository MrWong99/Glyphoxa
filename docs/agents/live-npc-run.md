# Running the live NPC (`voice` mode)

The `glyphoxa` binary's `voice` mode joins a Discord voice channel and gives one
Character NPC ("Bart", the innkeeper) a live voice loop. By default the NPC is
**loaded from Postgres** (seed it first — see "Loading the NPC from the database"
below); pass `-hardcoded` to use the in-code NPC without a database:

```
Session.Inbound (Opus) → [codec] → VAD (Silero) → STT (ElevenLabs)
  → Address Detection → Agent loop (Gemini + dice tool) → TTS (ElevenLabs)
  → [codec] → Session.Play (Opus)
```

The reasoning pipeline (VAD → STT → routing → Agent loop → TTS) is wired and
covered by keyless cassette tests. The **audio codec** — Opus↔PCM transcoding,
48 kHz↔16 kHz resampling, and 20 ms reframing on both directions — is built and
wired: inbound frames are decoded to PCM for VAD/STT, and synthesized speech is
tee'd from the TTS stage, played one sentence at a time, and Opus-encoded back to
the channel (`internal/wirenpc.Run` shares one `codec.New()` between
`wire.NewPipeline` for hearing and the playback path for speaking).

The codec links **libopus** and is compiled in only under **`-tags opus`**. A
default build (no tag) links the codec stub, so the binary still connects and
constructs the whole pipeline but the audio loop exits immediately with
`wire: audio codec unavailable …` on the first inbound frame — useful for
checking wiring without the native dependency. **For an audible run you must
build with the audio tags** (see Build below).

## Prerequisites

- **A Discord bot** with the **message content** and **voice** privileges, added
  to your test server, currently in (or able to join) a voice channel.
- **CGO** toolchain (`CGO_ENABLED=1`, a C compiler): Silero VAD uses ONNX
  Runtime via cgo. This is the canonical build mode (see `Makefile`).
- **Provider API keys** (BYOK, ADR-0004), supplied as environment variables —
  never compiled in. The live LLM is **Gemini** (matching the deployment:
  `providers.llm.name "gemini"`, model `gemini-2.5-flash`; there is no Anthropic
  key). The binary reads, at request time:
  - `GEMINI_API_KEY` — the LLM the Agent loop calls (Gemini, via its
    OpenAI-compatibility endpoint). The wired adapter is Gemini for any NPC; a
    DB-loaded Agent's `provider_config` provider/model is recorded but does not
    yet drive adapter selection.
  - `ELEVENLABS_API_KEY` — STT (scribe) and TTS (eleven_v3).
  - `DISCORD_BOT_TOKEN` — the Discord gateway/voice connection.

  These three are the only credentials the binary consumes. Source them from
  the local OS keyring (see below) — never paste literal key values into the
  shell.
- **Discord IDs**: the target guild (server) and voice channel snowflake IDs
  (Discord → User Settings → Advanced → Developer Mode, then right-click → Copy
  ID).

## Build

The audio codec (libopus) and DAVE/MLS encryption are opt-in native dependencies
selected by build tags. For an **audible** run you need `opus`; for a real
encrypted Discord session you also need `dave`.

```sh
# Default build: codec + DAVE are stubs. The pipeline constructs and the gateway
# connects, but the audio loop exits with `wire: audio codec unavailable` on the
# first inbound frame — useful for wiring checks, NOT audible. Needs no native libs.
CGO_ENABLED=1 go build -o glyphoxa ./cmd/glyphoxa

# Audible + encrypted live run. Prereqs: system libopus (e.g. `libopus` 1.6.1)
# and the libdave native libs (`make dave-libs`, which prints the PKG_CONFIG_PATH
# / LD_LIBRARY_PATH exports to add).
make dave-libs
export PKG_CONFIG_PATH="$HOME/.local/lib/pkgconfig:$PKG_CONFIG_PATH"
export LD_LIBRARY_PATH="$HOME/.local/lib:$LD_LIBRARY_PATH"
CGO_ENABLED=1 go build -tags "opus dave nolibopusfile" -o glyphoxa ./cmd/glyphoxa
```

- `opus` — real Opus↔PCM codec (else the stub: no audio).
- `dave` — real DAVE/MLS encryption (mandatory on Discord since 2026-03-01 for
  production; else the stub, `DaveAvailable() == false`, unencrypted).
- `nolibopusfile` — compiles out the libopusfile dependency of the Opus binding
  (Glyphoxa does not use file decoding). **Required whenever `opus` is set.**

## Keys: keyring → env (never printed)

The runtime keys live in the local OS keyring (GNOME Keyring, `secret-tool`,
`service=glyphoxa`; see `~/claude_workspace/glyphoxa-secrets-NOTE.md`). Export
them into the env vars the binary reads using **command-substitution** so the
value goes straight into the variable — it is never printed, never written to a
file, and never appears as a command argument (so it stays out of `ps` and shell
history):

```sh
export DISCORD_BOT_TOKEN=$(secret-tool lookup service glyphoxa key discord-token)
export GEMINI_API_KEY=$(secret-tool lookup service glyphoxa key gemini)
export ELEVENLABS_API_KEY=$(secret-tool lookup service glyphoxa key elevenlabs)
```

(The keyring's logical key names — `discord-token`, `gemini`, `elevenlabs` — map
onto the three env var names above, which are what the binary actually reads.
The `gemini` key backs the LLM here and also the deployment's S2S/embeddings.)
Do **not** `echo`/`cat` a key; to spot-check, use the exit code only:
`secret-tool lookup service glyphoxa key gemini >/dev/null; echo $?`.

## Run (DB-loaded NPC — the default)

`voice` mode loads the NPC from Postgres by default, so apply the schema and seed
the NPC once (in addition to the three keyring keys above), then run:

```sh
# Postgres connection string and the app credential-encryption secret.
export GLYPHOXA_DATABASE_URL="postgres://user:pass@host:5432/glyphoxa?sslmode=disable"
export GLYPHOXA_SECRET="$(openssl rand -base64 32)"   # ADR-0004 single app secret (base64, 32 bytes)

./glyphoxa migrate up          # apply the schema (idempotent)
./glyphoxa seed                # create the demo Tenant/Campaign + Bart (idempotent)

./glyphoxa -mode voice \
  -guild   <guild-snowflake-id> \
  -channel <voice-channel-snowflake-id>
```

For an **audio smoke test without Postgres**, use the `-hardcoded` escape hatch —
it voices the in-code Bart (Gemini + ElevenLabs at pcm_48000) and needs no DB,
`migrate`, or `seed`:

```sh
./glyphoxa -mode voice -hardcoded \
  -guild <guild-snowflake-id> -channel <voice-channel-snowflake-id>
```

It logs `loaded NPC from DB npc=Bart …`. The bot opens the Discord gateway,
joins the channel, and logs `joined voice channel … npc=Bart`. Stop with Ctrl-C
(SIGINT) — it leaves the channel and closes the session cleanly. The seed is
idempotent (it no-ops if the demo Tenant already exists), so re-running it on
every boot is safe.

## Running without a database (`-hardcoded`)

To smoke-test audio without Postgres, `-hardcoded` uses the in-code NPC instead
of loading from the DB (no `migrate`/`seed`, no `$GLYPHOXA_DATABASE_URL`):

```sh
./glyphoxa -mode voice -hardcoded \
  -guild   <guild-snowflake-id> \
  -channel <voice-channel-snowflake-id>
```

The assembled pipeline is identical either way; only the *source* of the NPC's
Persona/Voice/identity differs.

### Credential home (the `provider_config` ciphertext is *not* the live key)

The seed writes a `provider_config` row per Component (LLM=gemini, TTS/STT=
elevenlabs) with **encrypted placeholder** credentials and `last4="env"` — it
never stores a real provider key. For the self-host `voice` binary the real keys
come from the environment (above) / the OS keyring (task #10); the encrypted
`provider_config.credentials_ciphertext` column is the **web-app BYOK path**
(ADR-0004), which the control-plane (task #6) will populate and decrypt. So
seeding the NPC does **not** put any secret in the database.

`GLYPHOXA_SECRET` is only used to seal/open those placeholders. It must be a
base64-encoded 32-byte random key (`openssl rand -base64 32`) — a full-entropy
AES-256 key, never a passphrase, so leaked ciphertext cannot be brute-forced
offline. Keep the value you seed with: the same key opens the blobs later.

## What to expect (audible build)

1. Speak in the channel. Address Detection (the ADR-0024 scoring matcher) routes
   to Bart both when you **name him** — *"Bart, do you have a room?"* (or an
   alias: "innkeeper", "barkeep") — and, because he is the lone Character NPC
   and not Address-Only, when you say nothing addressed at all (the single-NPC
   fallback). Either way the utterance reaches his Agent loop.
2. The Agent loop assembles Hot Context (Bart's Persona + the recent transcript)
   and calls Gemini; the reply is spoken back through ElevenLabs in Bart's
   voice.
3. Ask Bart to **roll dice** (*"Bart, roll a d20 for my luck"*) to exercise the
   tool-use loop: the model calls the `dice` tool, the result is fed back, and
   Bart narrates the outcome.

## Determinism note (tests vs. live)

Unit tests never touch live services: STT/TTS use the recorded cassettes
(`tests/voice-cassettes/`), and the LLM uses the prompt-hash LLM cassette
(`llm-*.yaml`) via `voicecassette.LoadLLM`. The cassette **record** path
(`-tags=record`) still drives the **Anthropic** adapter — the cassettes were
recorded against Claude and the prompt hashes are pinned to them, so swapping
the *live* provider to Gemini does not touch the keyless test path. The
provider interface (`llm.Provider`) is the same for both adapters, which is why
the live swap (Anthropic → Gemini in `internal/wirenpc`) needed no change to the
Agent loop, the tool-use bridge, or the cassette tests. To refresh cassettes
after a prompt or model change, run the relevant tests with `-tags=record` and
the API keys set (see `pkg/voice/voicecassette`). The live `voice` mode above is
the only path that hits real Discord audio — and now real Gemini.
