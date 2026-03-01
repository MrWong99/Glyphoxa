---
nav_order: 1
---

# 🚀 Getting Started

Developer setup guide for building, running, and contributing to Glyphoxa.

---

## 📋 Prerequisites

### Go

Glyphoxa requires **Go 1.26+** with **CGo enabled** (`CGO_ENABLED=1`).

Install from [go.dev/dl](https://go.dev/dl/) or via your system package manager.

### System Libraries

#### Debian / Ubuntu

```bash
sudo apt update
sudo apt install -y build-essential cmake git \
  libopus-dev pkg-config
```

#### Arch Linux

```bash
sudo pacman -S base-devel cmake git opus
```

#### macOS (Homebrew)

```bash
brew install cmake opus pkg-config
```

### ONNX Runtime (Silero VAD)

The built-in Silero Voice Activity Detection provider requires the ONNX Runtime shared library.

1. Download the latest release for your platform from [onnxruntime releases](https://github.com/microsoft/onnxruntime/releases).
2. Extract and place the shared library where your linker can find it (e.g. `/usr/local/lib`).
3. Ensure the headers are accessible (e.g. `/usr/local/include/onnxruntime`).

### PostgreSQL with pgvector

The memory subsystem requires PostgreSQL with the [pgvector](https://github.com/pgvector/pgvector) extension.

```bash
# Debian/Ubuntu
sudo apt install -y postgresql postgresql-server-dev-all
# Then install pgvector from source — see https://github.com/pgvector/pgvector#installation

# Arch
sudo pacman -S postgresql
yay -S pgvector  # or build from source

# macOS
brew install postgresql@17 pgvector
```

Alternatively, use the Docker Compose setup (see [below](#-running-with-docker-compose)) which includes a pre-configured `pgvector/pgvector:pg17` image.

---

## 📥 Clone and Build

```bash
git clone https://github.com/MrWong99/glyphoxa.git
cd glyphoxa
```

Build the binary:

```bash
make build
```

This compiles the server to `./bin/glyphoxa`. Verify it built successfully:

```bash
./bin/glyphoxa --help
```

---

## 🔧 whisper.cpp Native Build

If you want to use the `whisper-native` STT provider (local speech-to-text via CGo instead of an HTTP server), you need to build the whisper.cpp static library first.

```bash
make whisper-libs
```

This clones whisper.cpp into `/tmp/whisper-src`, builds it, and installs headers and static libraries to `/tmp/whisper-install`.

After the build completes, set the environment variables before running other Make targets:

```bash
export C_INCLUDE_PATH=/tmp/whisper-install/include
export LIBRARY_PATH=/tmp/whisper-install/lib
export CGO_ENABLED=1
```

Then rebuild Glyphoxa so the whisper-native provider is linked:

```bash
make build
```

You will also need a GGML model file. Download one from the [Hugging Face whisper.cpp models](https://huggingface.co/ggerganov/whisper.cpp/tree/main):

```bash
# Example: download the base English model
curl -L -o ggml-base.en.bin \
  https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.en.bin
```

Then reference the model path in your config under `providers.stt`:

```yaml
stt:
  name: whisper-native
  model: /path/to/ggml-base.en.bin
  options:
    language: en
```

---

## ⚙️ Minimal Configuration

Copy the example config and edit it:

```bash
cp configs/example.yaml config.yaml
```

For a first run, you need at minimum:

1. A **Discord bot token** (from [discord.com/developers/applications](https://discord.com/developers/applications))
2. At least one **voice engine** path configured — either the cascaded pipeline (STT + LLM + TTS) or a speech-to-speech provider

Here is a minimal `config.yaml` using OpenAI for the cascaded pipeline and ElevenLabs for TTS:

```yaml
server:
  listen_addr: ":8080"
  log_level: info

providers:
  audio:
    name: discord
    api_key: "Bot YOUR_BOT_TOKEN_HERE"
    options:
      guild_id: "YOUR_GUILD_ID"

  llm:
    name: openai
    api_key: sk-...
    model: gpt-4o

  stt:
    name: deepgram
    api_key: dg-...
    model: nova-2
    options:
      language: en-US

  tts:
    name: elevenlabs
    api_key: el-...
    model: eleven_multilingual_v2

  vad:
    name: silero

npcs:
  - name: Greymantle the Sage
    personality: |
      You are Greymantle, an ancient wizard. You speak in measured,
      slightly archaic sentences and are helpful but mysterious.
    engine: cascaded
```

For a fully local setup (no API keys), use the Docker Compose local profile instead -- see [Running with Docker Compose](#-running-with-docker-compose).

See `configs/example.yaml` for the complete configuration reference including memory, embeddings, MCP tool servers, and multi-NPC setups.

---

## ▶️ Running Glyphoxa

Start the server:

```bash
./bin/glyphoxa -config config.yaml
```

On successful startup you will see the startup summary followed by a ready message:

```
╔═══════════════════════════════════════╗
║         Glyphoxa — startup summary    ║
╠═══════════════════════════════════════╣
║  LLM              : openai / gpt-4o   ║
║  STT              : deepgram / nova-2  ║
║  TTS              : elevenlabs / el…   ║
║  S2S              : (not configured)   ║
║  Embeddings       : (not configured)   ║
║  VAD              : silero             ║
║  Audio            : (not configured)   ║
║  Discord          : connected          ║
║  NPCs configured  : 1                  ║
║  MCP servers      : 0                  ║
║  Listen addr      : :8080              ║
╚═══════════════════════════════════════╝
time=... level=INFO msg="server ready — press Ctrl+C to shut down"
```

Press `Ctrl+C` to initiate graceful shutdown (15-second timeout).

If the config file is not found, Glyphoxa exits with:

```
glyphoxa: config file "config.yaml" not found — copy configs/example.yaml to get started
```

---

## 🐳 Running with Docker Compose

The `deployments/compose/` directory contains a full Docker Compose setup with two modes:

**Cloud API providers** (you supply API keys):

```bash
cd deployments/compose
cp config.yaml.example config.yaml
# Edit config.yaml with your API keys
docker compose up -d
```

**Fully local stack** (no API keys needed — uses Ollama, Whisper.cpp, Coqui TTS):

```bash
cd deployments/compose
cp config.local.yaml config.yaml
docker compose --profile local up -d
```

The local profile starts PostgreSQL with pgvector, Ollama (llama3.2 + nomic-embed-text), Whisper.cpp, and Coqui TTS automatically.

For GPU acceleration, service configuration, model selection, and troubleshooting, see the full guide at [`deployments/compose/README.md`](https://github.com/MrWong99/glyphoxa/blob/main/deployments/compose/README.md).

---

## 🛠️ Development Workflow

### Tests

Run the full test suite with the race detector:

```bash
make test
```

Run tests with verbose output:

```bash
make test-v
```

Generate a coverage report:

```bash
make test-cover
```

### Linting

Requires [golangci-lint](https://golangci-lint.run/welcome/install/):

```bash
make lint
```

### Pre-commit Check

Run formatting, vetting, and tests in one command:

```bash
make check
```

This runs `make fmt`, `make vet`, and `make test` sequentially. Run this before pushing.

### Branch Naming

Follow the project conventions:

- `feat/short-description` -- new features
- `fix/short-description` -- bug fixes
- `docs/short-description` -- documentation only
- `refactor/short-description` -- code cleanup

---

## ✅ Verifying the Setup

### Health Endpoints

Once Glyphoxa is running, check the health endpoints:

```bash
# Liveness probe — always returns 200 if the process is running
curl http://localhost:8080/healthz
```

```json
{"status":"ok"}
```

```bash
# Readiness probe — returns 200 only when all dependencies are healthy
curl http://localhost:8080/readyz
```

```json
{"status":"ok","checks":{"database":"ok","providers":"ok"}}
```

If any check fails, `/readyz` returns HTTP 503 with the failing check details:

```json
{"status":"fail","checks":{"database":"fail: connection refused","providers":"ok"}}
```

### First NPC Interaction

1. Invite your Discord bot to a server with the guild ID from your config.
2. Join a voice channel in that server.
3. Use the bot's slash commands to start a session and summon an NPC into the voice channel.
4. Speak to the NPC -- you should hear a voiced response within ~2 seconds.

If you configured a `dm_role_id`, ensure your Discord user has that role to access DM commands (`/session`, `/npc`, `/entity`, `/campaign`). Leave `dm_role_id` empty during development to allow all users.

---

## 📖 See Also

- [Architecture](design/01-architecture.md) -- system layers and data flow
- [Configuration](configuration.md) -- full configuration reference
- [Deployment](deployment.md) -- production deployment guide
- [Testing](testing.md) -- testing strategy and conventions
- [Contributing](https://github.com/MrWong99/glyphoxa/blob/main/CONTRIBUTING.md) -- code style, workflow, and PR guidelines
