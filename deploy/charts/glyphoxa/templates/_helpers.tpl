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

{{- define "glyphoxa.voice.fullname" -}}
{{- printf "%s-voice" (include "glyphoxa.fullname" .) | trunc 63 | trimSuffix "-" }}
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
independently — handy for pinning the voice pod to a build with the native opus/
ONNX tags without moving the migrate/seed Jobs.
*/}}
{{- define "glyphoxa.voice.image" -}}
{{- $repo := .Values.voice.image.repository | default .Values.image.repository -}}
{{- $tag := .Values.voice.image.tag | default .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end }}

{{/*
Hook ordering weights. The DB resources (Secret, Service, StatefulSet) come up
first, then the migrate Job, then the seed Job, then the serving workloads. All
are pre-install/pre-upgrade hooks EXCEPT the voice Deployment (#36), which is a
plain resource applied after every hook — so the migration and seed always
precede it. Weights sort ascending; lower runs first; Helm waits for each
weight's hook Jobs to complete before the next, so the seed only starts once the
migration has finished and the schema is current.

  -10  DB Secret + Postgres Service + StatefulSet
   -5  migrate Job
   -4  seed Job
    0  voice Deployment (#36 — a plain resource, applied after every hook)
*/}}
{{- define "glyphoxa.dbHookWeight" -}}-10{{- end }}

{{/*
The DB connection URL. When the operator sets database.url it wins verbatim
(external Postgres); otherwise the chart assembles a DSN against the in-cluster
Postgres Service so the host can never drift from the Service name.

User and password are percent-encoded (#151): the raw values also feed
POSTGRES_USER/POSTGRES_PASSWORD, so any URL-reserved character Postgres happily
accepts would otherwise make the DSN unparseable (or parse to the wrong
credential) for the migrate hook and the app. urlquery leaves alphanumerics
untouched, so default-style credentials render exactly as before. Host and DB
name come from the chart, not operator free-text, and stay unescaped.
*/}}
{{- define "glyphoxa.databaseURL" -}}
{{- if .Values.database.url -}}
{{- .Values.database.url -}}
{{- else -}}
{{- $host := include "glyphoxa.postgres.fullname" . -}}
{{- $port := .Values.postgres.service.port | int -}}
{{- printf "postgres://%s:%s@%s:%d/%s?sslmode=%s" (.Values.database.user | urlquery) (.Values.database.password | urlquery) $host $port .Values.database.name .Values.database.sslmode -}}
{{- end -}}
{{- end }}
