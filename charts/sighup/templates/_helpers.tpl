{{/*
Expand the name of the chart.
*/}}
{{- define "sighup.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "sighup.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "sighup.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "sighup.labels" -}}
helm.sh/chart: {{ include "sighup.chart" . }}
{{ include "sighup.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "sighup.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sighup.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "sighup.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "sighup.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
The namespace holding the Deployment being rolled. Defaults to the release namespace, in which
case nothing special happens; when it differs, only the Role and RoleBinding land over there.
*/}}
{{- define "sighup.targetNamespace" -}}
{{- .Values.target.namespace | default .Release.Namespace }}
{{- end }}

{{/*
The container port, named so the probes can reference it by name.
*/}}
{{- define "sighup.port" -}}{{ .Values.service.port }}{{- end }}

{{/*
Whether the webhook auth header is configured at all. An empty secret means no auth: safe only
when the Service is unreachable from anywhere that should not be rolling the Deployment.
*/}}
{{- define "sighup.webhookAuthEnabled" -}}
{{- if or .Values.webhook.secret.existingSecret .Values.webhook.secret.create -}}true{{- end -}}
{{- end }}

{{/*
Name of the Secret carrying the webhook auth header (created here, or provided).
*/}}
{{- define "sighup.webhookSecretName" -}}
{{- if .Values.webhook.secret.existingSecret -}}
{{- .Values.webhook.secret.existingSecret -}}
{{- else -}}
{{- printf "%s-webhook" (include "sighup.fullname" .) -}}
{{- end -}}
{{- end }}
