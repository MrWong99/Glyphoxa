# Overnight Implementation Decisions — 2026-03-22

Luk went to bed at ~01:00 CET. Claude is running Waves 3-5 autonomously.
All decisions made without Luk's input are documented here for morning review.

## Status Tracker

- [ ] Wave 3 Lane J: NPC gRPC extensions + recap
- [x] Wave 3 Lane K: Vault + security
- [ ] Wave 4 Lane L: End-to-end testing + docs
- [ ] Wave 5: Exploratory — actual Discord voice test in "openclaw" channel
- [ ] Deploy updated gateway to K3s
- [ ] Verify bot comes online with slash commands

## Decisions Log

### D1: Vault Transit for bot token encryption (01:00 CET)
**Decision:** Use Vault HTTP API directly (no Go Vault SDK dependency) for Transit encrypt/decrypt of bot tokens. Graceful degradation: if Vault is unavailable, store tokens in plaintext with a log warning. This avoids adding a heavy dependency and keeps the code simple.
**Why:** Vault SDK adds ~40 transitive deps. The Transit API is 2 HTTP calls (encrypt + decrypt). The admin store is the only consumer.

### D2: Vault PKI scope (01:00 CET)
**Decision:** Set up the Vault PKI role and document cert issuance. Full auto-rotation of gRPC certs is deferred — it requires a sidecar or init container pattern that's too complex for one night. For now, the TLS env var support from Wave 2 is sufficient. Manual cert issuance from Vault PKI is documented.
**Why:** Auto-rotation requires either a Vault agent sidecar or a Go-level cert watcher. Both are multi-day efforts. The TLS plumbing is already there.

### D3: K8s Secrets for API keys (01:00 CET)
**Decision:** Move ElevenLabs API key, Gemini API key, and Discord bot token from ConfigMap to K8s Secrets. The gateway deployment mounts them as env vars. The config.yaml in the ConfigMap references env var placeholders.
**Why:** ConfigMaps are not encrypted at rest. Secrets get etcd encryption by default in K3s.

### D4: NPC recap in gateway mode (01:00 CET)
**Decision:** Text-only recap for MVP. The gateway can query the shared PostgreSQL session store for transcript data. Voice recap (TTS narration) is deferred since the gateway doesn't have TTS providers loaded.
**Why:** Voice recap requires TTS infrastructure on the gateway. Text recap works immediately via shared DB.

### D5: Monitoring loop approach (01:00 CET)
**Decision:** 30-minute CronCreate loop checks agent progress, merges PRs, launches next waves. Stops at 05:00 CET or when all waves complete.

## Progress Log

### 01:54 CET — First check
- Wave 3 agents running (2 processes alive)
- No PRs or branches pushed yet
- Agents still working on proto changes and Vault integration

### 02:01 CET — Second check
- Both agents still running (2 processes)
- No PRs, no branches, no Discord reports yet
- Expected — these are heavy lanes (proto regen, Vault HTTP client, K8s Secrets)

### ~02:05 CET — Lane K complete
- Vault Transit client implemented (`internal/gateway/vault/transit.go`)
  - HTTP API based — no `hashicorp/vault/api` dependency (D1)
  - Graceful degradation: Encrypt returns plaintext, Decrypt errors only for vault: prefixed data
  - NoopEncryptor for when Vault is not configured
- PostgresAdminStore updated to encrypt/decrypt bot tokens on write/read
  - Constructor now accepts `vault.TokenEncryptor` (nil → NoopEncryptor)
  - Pre-existing plaintext tokens pass through transparently (no "vault:v1:" prefix)
- Vault PKI client implemented (`internal/gateway/vault/pki.go`)
  - Issues certs from Vault PKI, writes to disk (D2)
  - Full auto-rotation deferred per D2
  - `scripts/vault-setup.sh` configures Transit + PKI + policy
- K8s Secrets for sensitive config (D3)
  - Added `secrets.*` values for API keys, DB DSN, Vault token
  - Gateway + worker deployments mount via secretKeyRef
  - Database DSN from secrets takes precedence over `database.dsn`
- Fixed pre-existing duplicate variable declarations in main.go (orch, callbackBridge, usageStore)
- All tests pass (vault package: 100%, existing tests: unaffected)
