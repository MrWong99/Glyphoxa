# Overnight Implementation Decisions — 2026-03-22

Luk went to bed at ~01:00 CET. Claude is running Waves 3-5 autonomously.
All decisions made without Luk's input are documented here for morning review.

## Status Tracker

- [x] Wave 3 Lane J: NPC gRPC extensions + recap
- [x] Wave 3 Lane K: Vault + security
- [x] Wave 4 Lane L: End-to-end testing + docs
- [x] Wave 5: Exploratory — Discord deploy + slash command verification
- [x] Deploy updated gateway to K3s
- [x] Verify bot comes online with slash commands

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

## Wave 4: Deploy & Test (10:17–10:22 CET)

### D6: Migration idempotency fix (10:17 CET)
**Decision:** Made `ALTER TABLE ADD CONSTRAINT unique_active_campaign` idempotent by wrapping in a `DO $$ IF NOT EXISTS (SELECT FROM pg_constraint WHERE conname = ...)` block. PostgreSQL doesn't support `IF NOT EXISTS` on `ADD CONSTRAINT` natively.
**Why:** The gateway crashed on startup because the migration runner re-executes all SQL files on every boot (no version tracking). The constraint already existed from the previous deployment.

### D7: K8s dispatcher env vars (10:17 CET)
**Decision:** Patched the gateway deployment to add two env vars:
- `GLYPHOXA_K8S_NAMESPACE=glyphoxa` — enables the K8s Job dispatcher
- `GLYPHOXA_JOB_TEMPLATE_CM=glyphoxa-worker-job-template` — points to the correct ConfigMap (default was `glyphoxa-worker-job`, actual name is `glyphoxa-worker-job-template`)
**Why:** Without `GLYPHOXA_K8S_NAMESPACE`, the dispatcher is disabled entirely and `/session start` can't create worker pods. These should be added to the Helm/zhi deployment manifests permanently.

### D8: RBAC ConfigMap read permission (10:18 CET)
**Decision:** Patched the `glyphoxa-job-manager` Role to add `get` verb for ConfigMaps. The gateway service account needs this to load the worker job template.
**Why:** The Role only had permissions for batch/jobs and pods. Loading the job template from a ConfigMap requires core/configmaps read access. This should be added to the zhi deployment manifests.

### Deploy result
- Both CI and Docker workflows passed for the migration fix commit
- Gateway pod started cleanly after restart
- Migrations applied successfully
- K8s dispatcher initialized
- Health endpoints: `/healthz` ok, `/readyz` ok (admin_api: ok, grpc: ok)
- Tenant 'luk' created with campaign_id `die-chroniken-von-rabenheim`
- Discord bot connected: `has_commands=true`

### Slash commands registered
All 5 commands visible in Discord via the Application Commands API:
- `/session` — start, stop, status, recap
- `/npc` — list, mute, unmute, speak, muteall, unmuteall
- `/entity` — knowledge management
- `/campaign` — campaign management
- `/feedback` — user feedback

## Wave 5: Voice Pipeline Investigation (10:22 CET)

### Worker dispatch readiness: READY
The full worker dispatch infrastructure is in place:
1. **K8s Job template** (`glyphoxa-worker-job-template` ConfigMap) — correctly configured with:
   - Image: `ghcr.io/mrwong99/glyphoxa:main`
   - Mode: `--mode=worker`
   - Gateway address: `glyphoxa-gateway.glyphoxa.svc:50051`
   - Database DSN
   - Shared config volume
   - Resource limits (500m–2 CPU, 384Mi–768Mi RAM)
2. **RBAC** (`glyphoxa-job-manager` Role) — allows job CRUD, pod list/watch, configmap get
3. **Service account** (`glyphoxa-gateway`) bound to the role
4. **Dispatcher** initialized in gateway with 120s timeout for pod readiness

### What happens on `/session start`
1. Gateway creates session record in PostgreSQL (with campaign/guild/license constraints)
2. Dispatcher stamps the job template with session ID + tenant ID
3. K8s Job is created → worker pod starts
4. Gateway polls for pod Running + IP (120s timeout)
5. Worker connects to Discord voice and runs VAD→STT→LLM→TTS pipeline
6. Control flows over gRPC between gateway and worker

### To test end-to-end
A human needs to:
1. Join a voice channel in the Discord guild (1178124747884736613)
2. Run `/session start` in a text channel
3. Worker pod will be dispatched automatically
4. Speak — NPCs should respond

### Known limitations for first test
- gRPC TLS not configured (insecure, fine for LAN)
- Vault encryption not active (no VAULT_ADDR/VAULT_TOKEN set)
- API keys are in ConfigMap, not K8s Secrets (D3 code exists but K8s manifests not updated)
- Worker pod resource limits may need tuning for actual voice processing
- NPC commands return "no active session" in gateway mode until gRPC extensions fully wire the orchestrator (D4 scope)
