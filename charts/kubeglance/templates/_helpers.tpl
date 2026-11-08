{{- define "kubeglance.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "kubeglance.fullname" -}}
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

{{- define "kubeglance.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "kubeglance.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "kubeglance.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubeglance.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kubeglance.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kubeglance.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Alerts are on when a receiver is configured, as a value or from a Secret.
*/}}
{{- define "kubeglance.alerts" -}}
{{- if or .Values.alerts.webhook (and .Values.alerts.existingSecret .Values.alerts.urlKey) -}}true{{- end -}}
{{- end }}

{{/*
Alert headers come from the Secret when both are named.
*/}}
{{- define "kubeglance.alertHeaders" -}}
{{- if and (include "kubeglance.alerts" .) .Values.alerts.existingSecret .Values.alerts.headersKey -}}true{{- end -}}
{{- end }}
