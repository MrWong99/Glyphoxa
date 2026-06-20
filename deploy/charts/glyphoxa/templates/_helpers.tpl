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
Hook ordering weights. The DB resources (Secret, Service, StatefulSet) come up
first, then the migrate Job, then (later) the serving workloads. All are
pre-install/pre-upgrade hooks EXCEPT the serving workloads (#35/#36), which are
plain resources applied after every hook — so the migration always precedes
them. Weights sort ascending; lower runs first.

  -10  DB Secret + Postgres Service + StatefulSet
   -5  migrate Job
    0  (serving workloads, not in this slice)
*/}}
{{- define "glyphoxa.dbHookWeight" -}}-10{{- end }}

{{/*
The DB connection URL. When the operator sets database.url it wins verbatim
(external Postgres); otherwise the chart assembles a DSN against the in-cluster
Postgres Service so the host can never drift from the Service name.
*/}}
{{- define "glyphoxa.databaseURL" -}}
{{- if .Values.database.url -}}
{{- .Values.database.url -}}
{{- else -}}
{{- $host := include "glyphoxa.postgres.fullname" . -}}
{{- $port := .Values.postgres.service.port | int -}}
{{- printf "postgres://%s:%s@%s:%d/%s?sslmode=%s" .Values.database.user .Values.database.password $host $port .Values.database.name .Values.database.sslmode -}}
{{- end -}}
{{- end }}
