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
The value of SIGHUP_TARGETS: the targets list, with each target's namespace defaulted to the release
namespace and required fields enforced. Emitted as JSON because the binary parses it as such -- one
env var carrying a list, rather than N indexed vars.
*/}}
{{- define "sighup.targets" -}}
{{- if not .Values.targets -}}
{{- fail "targets is required: name at least one {repository,tag,deployment}" -}}
{{- end -}}
{{- $ns := .Release.Namespace -}}
{{- $out := list -}}
{{- range $i, $t := .Values.targets -}}
{{- $out = append $out (dict
      "repository" (required (printf "targets[%d].repository is required" $i) $t.repository)
      "tag" (required (printf "targets[%d].tag is required" $i) $t.tag)
      "namespace" (default $ns $t.namespace)
      "deployment" (required (printf "targets[%d].deployment is required" $i) $t.deployment)
      "annotation" $t.annotation) -}}
{{- end -}}
{{- toJson $out -}}
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
