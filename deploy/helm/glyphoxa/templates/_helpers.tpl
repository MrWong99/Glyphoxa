{{/*
Expand the name of the chart.
*/}}
{{- define "glyphoxa.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name.
*/}}
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

{{/*
Common labels.
*/}}
{{- define "glyphoxa.labels" -}}
helm.sh/chart: {{ include "glyphoxa.chart" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: glyphoxa
{{- end }}

{{/*
Chart label.
*/}}
{{- define "glyphoxa.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Selector labels for gateway.
*/}}
{{- define "glyphoxa.gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "glyphoxa.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: gateway
{{- end }}

{{/*
Selector labels for worker.
*/}}
{{- define "glyphoxa.worker.selectorLabels" -}}
app.kubernetes.io/name: {{ include "glyphoxa.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: worker
{{- end }}

{{/*
Container image reference.
*/}}
{{- define "glyphoxa.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}

{{/*
Gateway service account name.
*/}}
{{- define "glyphoxa.gateway.serviceAccountName" -}}
{{- if .Values.gateway.serviceAccount.create }}
{{- default (printf "%s-gateway" (include "glyphoxa.fullname" .)) .Values.gateway.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.gateway.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Worker resource limits based on profile.
*/}}
{{- define "glyphoxa.worker.resources" -}}
{{- $profile := .Values.worker.resourceProfile }}
{{- $profiles := .Values.worker.profiles }}
{{- if hasKey $profiles $profile }}
{{- toYaml (index $profiles $profile) }}
{{- else }}
{{- toYaml (index $profiles "cloud") }}
{{- end }}
{{- end }}

{{/*
PostgreSQL DSN. Uses explicit dsn if set, otherwise constructs from subchart values.
*/}}
{{- define "glyphoxa.databaseDSN" -}}
{{- if .Values.database.dsn }}
{{- .Values.database.dsn }}
{{- else if .Values.postgresql.enabled }}
{{- printf "postgres://postgres:%s@%s-postgresql:5432/%s?sslmode=disable" .Values.postgresql.auth.postgresPassword (include "glyphoxa.fullname" .) .Values.postgresql.auth.database }}
{{- else }}
{{- "" }}
{{- end }}
{{- end }}
