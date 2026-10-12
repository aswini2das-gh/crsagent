{{/*
Expand the name of the chart.
*/}}
{{- define "crsproxyagent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
*/}}
{{- define "crsproxyagent.fullname" -}}
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
{{- define "crsproxyagent.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "crsproxyagent.labels" -}}
helm.sh/chart: {{ include "crsproxyagent.chart" . }}
{{ include "crsproxyagent.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "crsproxyagent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "crsproxyagent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use.
*/}}
{{- define "crsproxyagent.serviceAccountName" -}}
{{- if .Values.operator.serviceAccount.create }}
{{- default (include "crsproxyagent.fullname" .) .Values.operator.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.operator.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Return the image tag, defaulting to appVersion.
*/}}
{{- define "crsproxyagent.imageTag" -}}
{{- default .Chart.AppVersion .Values.operator.image.tag }}
{{- end }}
