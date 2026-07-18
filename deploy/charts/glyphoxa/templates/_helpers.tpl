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
OAuth keys on web, each `required` under its gate so a deploy can never start
with an empty credential. The one deliberate exception is `operator-ids`
(ADR-0055): `required` only in the `allowlist` Admission Mode, where it is the
admission gate; in `open` mode it is the platform-admin list and may be empty —
but the KEY still renders (as "") because the web Deployment's secretKeyRef
would fail pod start on a missing key. `open` mode instead requires a signup
plan slug, mirroring the binary's fatal open-mode boot preflight.
*/}}
{{- define "glyphoxa.secretStringData" -}}
database-url: {{ include "glyphoxa.databaseURL" . | quote }}
username: {{ .Values.database.user | quote }}
password: {{ .Values.database.password | quote }}
database: {{ .Values.database.name | quote }}
app-secret: {{ required "appSecret is required: a base64-encoded 32-byte credential-cipher key (ADR-0004) the seed Job uses to seal placeholder provider credentials. Generate one with `openssl rand -base64 32`." .Values.appSecret | quote }}
{{- if or .Values.voice.enabled .Values.web.enabled }}
discord-bot-token: {{ required "discordBotToken is required when voice.enabled or web.enabled: the Discord bot token the voice pod joins the gateway with (and the web tier's base session bot)." .Values.discordBotToken | quote }}
elevenlabs-api-key: {{ required "elevenLabsApiKey is required when voice.enabled or web.enabled: the ElevenLabs API key the STT/TTS adapters read." .Values.elevenLabsApiKey | quote }}
gemini-api-key: {{ required "geminiApiKey is required when voice.enabled or web.enabled: the Gemini API key." .Values.geminiApiKey | quote }}
groq-api-key: {{ required "groqApiKey is required when voice.enabled or web.enabled: the Groq API key the LLM adapter reads." .Values.groqApiKey | quote }}
{{- end }}
{{- if .Values.web.enabled }}
discord-oauth-client-id: {{ required "web.oauth.clientId is required when web.enabled: the Discord OAuth application's Client ID (ADR-0016/0039). A Web Instance refuses to boot without a usable login (ADR-0041)." .Values.web.oauth.clientId | quote }}
discord-oauth-client-secret: {{ required "web.oauth.clientSecret is required when web.enabled: the Discord OAuth application's Client Secret." .Values.web.oauth.clientSecret | quote }}
discord-oauth-redirect-url: {{ include "glyphoxa.web.oauthRedirectURL" . | quote }}
{{- $admissionMode := include "glyphoxa.web.admissionMode" .Values.web.admissionMode }}
{{- if eq $admissionMode "open" }}
{{- if not .Values.web.signupPlanSlug }}
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
