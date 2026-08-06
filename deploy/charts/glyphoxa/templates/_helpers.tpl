{{/*
Naming + label helpers shared by every template in the chart.

The names here are load-bearing: the Postgres Service name is what both the
assembled connection URL and the migrate Job's pg_isready wait target resolve
by DNS, so they all flow from one helper.
*/}}

{{/* Base name, honouring nameOverride / fullnameOverride like the Helm starter. */}}
{{- define "glyphoxa.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "glyphoxa.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "glyphoxa.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Common labels stamped on every object. */}}
{{- define "glyphoxa.labels" -}}
helm.sh/chart: {{ include "glyphoxa.chart" . }}
{{ include "glyphoxa.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "glyphoxa.selectorLabels" -}}
app.kubernetes.io/name: {{ include "glyphoxa.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* Object names derived from the release. */}}
{{- define "glyphoxa.postgres.fullname" -}}
{{- printf "%s-postgres" (include "glyphoxa.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "glyphoxa.secretName" -}}
{{- printf "%s-db" (include "glyphoxa.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "glyphoxa.migrate.fullname" -}}
{{- printf "%s-migrate" (include "glyphoxa.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "glyphoxa.seed.fullname" -}}
{{- printf "%s-seed" (include "glyphoxa.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "glyphoxa.plans.fullname" -}}
{{- printf "%s-plans" (include "glyphoxa.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "glyphoxa.voice.fullname" -}}
{{- printf "%s-voice" (include "glyphoxa.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "glyphoxa.web.fullname" -}}
{{- printf "%s-web" (include "glyphoxa.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "glyphoxa.ollama.fullname" -}}
{{- printf "%s-ollama" (include "glyphoxa.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "glyphoxa.cloudflared.fullname" -}}
{{- printf "%s-cloudflared" (include "glyphoxa.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "glyphoxa.backup.fullname" -}}
{{- printf "%s-backup" (include "glyphoxa.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
The backup CronJob's dump container (#520), factored out because it runs in a
different slot depending on the off-site flag: as an initContainer when a push
follows it (Kubernetes gives no ordering between two regular containers, and a
partially-written dump must never be pushed), and as the pod's only container
when it does not.

The image is the SAME Postgres image the chart runs (glyphoxa.postgres.image),
so pg_dump's version always matches the server it dumps — a newer server than
client is the classic "server version mismatch" failure, and pinning them
together makes it unrepresentable.

The dump is custom-format (-Fc): compressed, and the input `pg_restore` takes
(see docs/deploy/backup-restore.md). The DSN comes from the same app Secret key
the app itself uses, so a backup can never target a different database than the
deployment. `set -eu` plus writing the produced filename into /backup/.latest
gives the off-site push an unambiguous handle; rotation runs only AFTER a
successful dump, so a failing dump never deletes the last good one.

The dump is written under a .partial name and renamed only on success: a
pg_dump killed mid-write (DB restart, OOM, ENOSPC on the PVC) must never leave
a truncated file matching the glyphoxa-*.dump glob the restore runbook (and the
next retry's rotation) trusts. Stale partials from crashed runs are swept at
start so retried Jobs cannot accumulate them toward a disk-full wedge.
*/}}
{{- define "glyphoxa.backup.dumpContainer" -}}
- name: pg-dump
  image: {{ include "glyphoxa.postgres.image" . }}
  imagePullPolicy: {{ .Values.postgres.image.pullPolicy }}
  command: ["/bin/sh", "-c"]
  args:
    - |
      set -eu
      rm -f /backup/*.dump.partial
      dump="/backup/glyphoxa-$(date -u +%Y%m%dT%H%M%SZ).dump"
      pg_dump "$GLYPHOXA_DATABASE_URL" -Fc -f "${dump}.partial"
      mv "${dump}.partial" "${dump}"
      printf '%s' "${dump}" > /backup/.latest
      find /backup -name 'glyphoxa-*.dump' -mtime +{{ .Values.backup.retentionDays }} -delete
      echo "backup: wrote ${dump} (retention {{ .Values.backup.retentionDays }}d)"
  env:
    - name: GLYPHOXA_DATABASE_URL
      valueFrom:
        secretKeyRef:
          name: {{ include "glyphoxa.secretName" . }}
          key: database-url
  volumeMounts:
    - name: backup
      mountPath: /backup
  resources:
    {{- toYaml .Values.backup.resources | nindent 4 }}
{{- end }}

{{/*
The embeddings endpoint the web/voice pods dial (GLYPHOXA_OLLAMA_URL, ADR-0011).

An explicit ollamaUrl always wins (an external server, a host-network Ollama).
Otherwise, when the chart deploys its OWN Ollama (#517), it derives the
in-cluster Service URL — same helper the Service name comes from, so the two can
never drift. With neither, this renders EMPTY and the callers omit the env var:
the binary keeps its loopback default, which cannot work in a pod, so semantic
memory (L2) stalls loudly while everything else keeps working — the documented
pre-#517 behaviour.
*/}}
{{- define "glyphoxa.ollamaURL" -}}
{{- if .Values.ollamaUrl -}}
{{- .Values.ollamaUrl -}}
{{- else if .Values.ollama.enabled -}}
{{- printf "http://%s:%d" (include "glyphoxa.ollama.fullname" .) (int .Values.ollama.port) -}}
{{- end -}}
{{- end }}

{{/*
Validate the Web Instance Mode (ADR-0005). `web` serves the operator console +
Connect API only; `all` additionally drives the voice loop in-process for
single-pod sessions (ADR-0039). Any other value would deploy a pod that exits(2)
at runtime ("unknown mode"), so reject it at render time with an actionable
message instead — mirroring the snowflake guard's fail-fast philosophy.
*/}}
{{- define "glyphoxa.web.mode" -}}
{{- if or (eq . "web") (eq . "all") -}}
{{- . -}}
{{- else -}}
{{- fail (printf "web.mode must be \"web\" or \"all\", got %q — \"web\" serves the console + Connect API only; \"all\" additionally drives the in-process voice loop (ADR-0039, single-pod)." .) -}}
{{- end -}}
{{- end }}

{{/*
Validate the Admission Mode (ADR-0055). `allowlist` is exactly ADR-0041: only
the operator allowlist may complete a login. `open` admits any Discord User who
completes OAuth — each signup founds a fresh Tenant, and the allowlist becomes
the platform-administration list rather than the admission gate. The binary
refuses to boot on an unparsable GLYPHOXA_ADMISSION_MODE, so — like
[glyphoxa.web.mode] — reject a bad value at render time with an actionable
message instead of deploying a pod that dies on its boot preflight.
*/}}
{{- define "glyphoxa.web.admissionMode" -}}
{{- if or (eq . "allowlist") (eq . "open") -}}
{{- . -}}
{{- else -}}
{{- fail (printf "web.admissionMode must be \"allowlist\" or \"open\", got %q — \"allowlist\" admits only the web.operatorIds snowflakes (ADR-0041); \"open\" admits any Discord User who completes OAuth (self-signup, ADR-0055)." .) -}}
{{- end -}}
{{- end }}

{{/*
Render a Discord snowflake ID (guild/channel) as an exact string, rejecting any
non-string value with an actionable error.

Snowflakes are 64-bit IDs whose magnitude exceeds float64's 53-bit integer
precision, so a value parsed as a NUMBER is already truncated before any
template logic runs — `111111111111111111` becomes `111111111111111104`. That
happens at the YAML/`--set` boundary (a bare `--set voice.guild=111...`, or
Helm's --reuse-values re-serializing through JSON), where int64 coercion can no
longer recover the lost digits. So rather than silently deploy a pod with a
wrong ID that fails confusingly at runtime, fail the render and tell the operator
to quote it (a YAML string, or `--set-string`).
*/}}
{{- define "glyphoxa.voice.snowflake" -}}
{{- if kindIs "string" . -}}
{{- . -}}
{{- else -}}
{{- fail (printf "Discord snowflake ID %v must be a quoted string, not a number — a 64-bit ID loses precision as a float. Set it as a YAML string (guild: \"111...\") or use --set-string." .) -}}
{{- end -}}
{{- end }}

{{/*
The single Glyphoxa image reference (ADR-0034). tag falls back to the chart
appVersion so an unset tag still pins a matching image.
*/}}
{{- define "glyphoxa.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end }}

{{/* The pgvector Postgres image reference. */}}
{{- define "glyphoxa.postgres.image" -}}
{{- printf "%s:%s" .Values.postgres.image.repository .Values.postgres.image.tag -}}
{{- end }}

{{/*
The voice Deployment's image. It defaults to the shared [glyphoxa.image] (one
image, ADR-0034) but lets voice.image.repository/tag override either field
independently — handy for pinning the voice pod to a different build without
moving the migrate/seed Jobs.
*/}}
{{- define "glyphoxa.voice.image" -}}
{{- $repo := .Values.voice.image.repository | default .Values.image.repository -}}
{{- $tag := .Values.voice.image.tag | default .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end }}

{{/*
The web Deployment's image. Like [glyphoxa.voice.image] it defaults to the
shared [glyphoxa.image] (one image, ADR-0034) but lets web.image.repository/tag
override either field independently.
*/}}
{{- define "glyphoxa.web.image" -}}
{{- $repo := .Values.web.image.repository | default .Values.image.repository -}}
{{- $tag := .Values.web.image.tag | default .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end }}

{{/*
The app Secret's stringData body (the key: value lines under `stringData:`),
factored into a named template so both secret.yaml renders it AND the web
Deployment can sha256 it for its checksum/secret pod annotation (#121) — a
cross-template `include` of the whole Secret file is not visible to a
single-template helm-unittest suite, but a named partial always is. Keys are
unindented here; the caller applies `nindent 2`. The DB + cipher keys are
unconditional; the shared credential keys are gated on voice-or-web and the web
OAuth keys on web. The bot token and the OAuth keys are `required` under their
gates so a deploy can never start without a working gateway login. The three
provider keys are NOT required: they are only the env fallback of the hybrid
BYOK policy (ADR-0004/ADR-0039) — a BYOK deployment leaves them empty and
Tenants bring their own keys via Provider Configs; only a deployment providing
managed provider usage (platform keys, ADR-0054) fills them. They still always
render under the gate, possibly empty, because the voice/web Deployments
reference them by secretKeyRef unconditionally — omitting the keys would
dead-end the pods at CreateContainerConfigError. `operator-ids` follows the
same render-but-maybe-empty shape with a mode-conditional requirement
(ADR-0055): `required` only in the `allowlist` Admission Mode, where it is the
admission gate; in `open` mode it is the platform-admin list and may be empty.
`open` mode instead requires a non-blank signup plan slug (trimmed, exactly
like the binary's fatal open-mode boot preflight); the slug value itself is
passed through verbatim — the binary trims it again on read.
*/}}
{{- define "glyphoxa.secretStringData" -}}
database-url: {{ include "glyphoxa.databaseURL" . | quote }}
username: {{ .Values.database.user | quote }}
password: {{ .Values.database.password | quote }}
database: {{ .Values.database.name | quote }}
app-secret: {{ required "appSecret is required: a base64-encoded 32-byte credential-cipher key (ADR-0004) the seed Job uses to seal placeholder provider credentials. Generate one with `openssl rand -base64 32`." .Values.appSecret | quote }}
{{- if or .Values.voice.enabled .Values.web.enabled }}
discord-bot-token: {{ required "discordBotToken is required when voice.enabled or web.enabled: the Discord bot token the voice pod joins the gateway with (and the web tier's base session bot)." .Values.discordBotToken | quote }}
elevenlabs-api-key: {{ .Values.elevenLabsApiKey | quote }}
gemini-api-key: {{ .Values.geminiApiKey | quote }}
groq-api-key: {{ .Values.groqApiKey | quote }}
{{- end }}
{{- if .Values.web.enabled }}
discord-oauth-client-id: {{ required "web.oauth.clientId is required when web.enabled: the Discord OAuth application's Client ID (ADR-0016/0039). A Web Instance refuses to boot without a usable login (ADR-0041)." .Values.web.oauth.clientId | quote }}
discord-oauth-client-secret: {{ required "web.oauth.clientSecret is required when web.enabled: the Discord OAuth application's Client Secret." .Values.web.oauth.clientSecret | quote }}
discord-oauth-redirect-url: {{ include "glyphoxa.web.oauthRedirectURL" . | quote }}
{{- $admissionMode := include "glyphoxa.web.admissionMode" .Values.web.admissionMode }}
{{- if eq $admissionMode "open" }}
{{- /* Trimmed, like the binary's boot preflight (signupPlanPreflight trims
before its empty check): a whitespace-only slug would slip past a naive
non-empty guard here yet still fatally refuse at boot — the whole point of
this guard is to catch that at render time instead. */}}
{{- if not (.Values.web.signupPlanSlug | default "" | trim) }}
{{- fail "open admission needs a signup plan (ADR-0055): every open-mode signup is bound to a default Plan at Tenant creation, and the web pod's boot preflight is fatal without one. Set web.signupPlanSlug to a plan slug and include that plan in plans.catalog (plans.enabled=true) so the plans-sync hook syncs it before the pod boots." }}
{{- end }}
operator-ids: {{ .Values.web.operatorIds | default "" | quote }}
{{- else }}
operator-ids: {{ required "web.operatorIds is required when web.enabled: a comma/whitespace-separated list of Discord User snowflakes (the operator allowlist, ADR-0041). A Web Instance refuses to boot without at least one. (In the `open` Admission Mode — web.admissionMode, ADR-0055 — the list is the platform-admin roster instead and may be empty.)" .Values.web.operatorIds | quote }}
{{- end }}
admission-mode: {{ $admissionMode | quote }}
signup-plan-slug: {{ .Values.web.signupPlanSlug | default "" | quote }}
{{- end }}
{{- end }}

{{/*
The Web Instance's TLS Secret name (#121). An externally supplied
ingress.tls.secretName wins verbatim; otherwise a release-derived name that
cert-manager provisions the certificate into when the cert-manager path is on.
*/}}
{{- define "glyphoxa.web.tlsSecretName" -}}
{{- .Values.ingress.tls.secretName | default (printf "%s-tls" (include "glyphoxa.web.fullname" .)) -}}
{{- end }}

{{/*
The external scheme the Ingress presents (#121). TLS terminates at the ingress
(ADR-0039) whenever a Secret is referenced — either an external one or the
cert-manager-provisioned one — so the operator reaches the console over https;
a host-only Ingress with no TLS is plain http (e.g. TLS terminated further out).
This drives the OAuth redirect URL's scheme so the advertised callback matches
what the browser actually hits.
*/}}
{{- define "glyphoxa.web.ingressScheme" -}}
{{- if or .Values.ingress.certManager.enabled .Values.ingress.tls.secretName -}}https{{- else -}}http{{- end -}}
{{- end }}

{{/*
The Discord OAuth redirect URL the Web Instance advertises (DISCORD_OAUTH_REDIRECT_URL).

An explicitly set web.oauth.redirectUrl ALWAYS wins — an operator override is
authoritative regardless of the Ingress. This is the escape hatch for an external
load balancer that terminates TLS in front of a plain-HTTP Ingress: the app's own
Ingress is http but the browser hits https, so the operator registers the https
callback explicitly and the chart must not clobber it with a derived http:// value
(else the browser withholds the Secure state cookie → login dead-ends).

Only when it is unset is the URL DERIVED from ingress.host plus the fixed callback
path the OAuth handler serves (/auth/discord/callback, cmd/glyphoxa/main.go), so
the redirect can never drift from the host the Ingress terminates (AC #121). With
the Ingress disabled AND no explicit value there is nothing to advertise, so the
render fails fast (required). Keeping the resolution here means the app Secret
(which the pod reads) and the install notes share one source of truth.
*/}}
{{- define "glyphoxa.web.oauthRedirectURL" -}}
{{- if .Values.web.oauth.redirectUrl -}}
{{- .Values.web.oauth.redirectUrl -}}
{{- else if .Values.ingress.enabled -}}
{{- $host := required "ingress.host is required when ingress.enabled: it drives both the Ingress route and the Discord OAuth redirect URL the Web Instance advertises (#121)." .Values.ingress.host -}}
{{- printf "%s://%s/auth/discord/callback" (include "glyphoxa.web.ingressScheme" .) $host -}}
{{- else -}}
{{- required "web.oauth.redirectUrl is required when web.enabled and the Ingress is disabled: the Discord OAuth redirect URL registered on the application. With an Ingress enabled it is derived from ingress.host instead." .Values.web.oauth.redirectUrl -}}
{{- end -}}
{{- end }}

{{/*
The privacy-policy URL the Bot links from its Voice Session transcription
disclosure (GLYPHOXA_PRIVACY_POLICY_URL, #519).

An explicit privacyPolicyUrl always wins (the policy may live on another host).
Otherwise it is DERIVED from the Ingress — same host and scheme the console is
served on, plus the SPA's /privacy route — so the link can never drift from the
deployment the players are actually using. With no explicit value and no
Ingress there is nothing trustworthy to advertise, so this renders EMPTY and the
callers omit the env var entirely: the disclosure still posts, just without a
link. Deliberately not `required` — a missing policy link must never keep a
Voice Session from starting.
*/}}
{{- define "glyphoxa.privacyPolicyURL" -}}
{{- if .Values.privacyPolicyUrl -}}
{{- .Values.privacyPolicyUrl -}}
{{- else if and .Values.ingress.enabled .Values.ingress.host -}}
{{- printf "%s://%s/privacy" (include "glyphoxa.web.ingressScheme" .) .Values.ingress.host -}}
{{- end -}}
{{- end }}

{{/*
Idle Close env (ADR-0061), shared by the voice and web pods so a split
deployment and an `all`-Mode one cannot drift: the voice loop runs in the voice
pod under `mode: split` and in the web pod under `mode: all`, and the watchdog
lives beside it either way.

Every key is emitted ONLY when set away from its default, so a stock install
renders NO env var here and the binary's own defaults (15m window, 30s sweep,
200 connect cycles, both process ceilings off) apply — the same "unset means
byte-identical" posture maxVoiceSessions uses. Include it inside a container's
`env:` list; it emits nothing at all when idleClose is left untouched.
*/}}
{{- define "glyphoxa.idleCloseEnv" -}}
{{- with .Values.idleClose }}
{{- if .window }}
# How long a Voice Session may process no audio before its Voice Instance closes
# it (ADR-0061). "off" disables idle closing.
- name: GLYPHOXA_VOICE_IDLE_CLOSE_WINDOW
  value: {{ .window | quote }}
{{- end }}
{{- if .sweep }}
# How often the Idle Close watchdog checks (default 30s).
- name: GLYPHOXA_VOICE_IDLE_CLOSE_SWEEP
  value: {{ .sweep | quote }}
{{- end }}
{{- if gt (int .maxConnectCycles) 0 }}
# Reconnect-churn ceiling: a Voice Session past this many Discord connect cycles
# has leaked a per-cycle world that many times and is closed.
- name: GLYPHOXA_VOICE_MAX_CONNECT_CYCLES
  value: {{ .maxConnectCycles | quote }}
{{- end }}
{{- if gt (int .heapCeilingMiB) 0 }}
# Process heap ceiling in MiB: over it, the Voice Instance sheds its quietest
# Voice Session. Size it comfortably under the container memory limit.
- name: GLYPHOXA_VOICE_HEAP_CEILING_MIB
  value: {{ .heapCeilingMiB | quote }}
{{- end }}
{{- if gt (int .goroutineCeiling) 0 }}
# Process goroutine ceiling: same shed as the heap ceiling.
- name: GLYPHOXA_VOICE_GOROUTINE_CEILING
  value: {{ .goroutineCeiling | quote }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Hook ordering weights. The DB resources (Secret, Service, StatefulSet) come up
first, then the migrate Job, then the seed Job, then the serving workloads. All
are pre-install/pre-upgrade hooks EXCEPT the voice + web Deployments, which are
plain resources applied after every hook — so the migration and seed always
precede them. Weights sort ascending; lower runs first; Helm waits for each
weight's hook Jobs to complete before the next, so the seed only starts once the
migration has finished and the schema is current.

  -10  DB Secret + Postgres Service + StatefulSet
   -5  migrate Job
   -4  seed Job
    0  voice Deployment (#36) + web Deployment/Service (#118) — plain resources,
       applied after every hook
*/}}
{{- define "glyphoxa.dbHookWeight" -}}-10{{- end }}

{{/*
The DB connection URL. When the operator sets database.url it wins verbatim
(external Postgres); otherwise the chart assembles a DSN against the in-cluster
Postgres Service so the host can never drift from the Service name.

User and password are percent-encoded (#151): the raw values also feed
POSTGRES_USER/POSTGRES_PASSWORD, so any URL-reserved character Postgres happily
accepts would otherwise make the DSN unparseable (or parse to the wrong
credential) for the migrate hook and the app. urlquery (Go's QueryEscape)
encodes a SPACE as '+', but net/url userinfo decoding — what pgx uses — keeps
'+' literal, so that one character must be re-encoded as %20; a literal '+' in
the credential is already %2B at that point, so the replace can only ever hit
an encoded space. urlquery leaves alphanumerics untouched, so default-style
credentials render exactly as before. Host and DB name come from the chart,
not operator free-text, and stay unescaped.
*/}}
{{- define "glyphoxa.databaseURL" -}}
{{- if .Values.database.url -}}
{{- .Values.database.url -}}
{{- else -}}
{{- $host := include "glyphoxa.postgres.fullname" . -}}
{{- $port := .Values.postgres.service.port | int -}}
{{- $user := .Values.database.user | urlquery | replace "+" "%20" -}}
{{- $password := .Values.database.password | urlquery | replace "+" "%20" -}}
{{- printf "postgres://%s:%s@%s:%d/%s?sslmode=%s" $user $password $host $port .Values.database.name .Values.database.sslmode -}}
{{- end -}}
{{- end }}
