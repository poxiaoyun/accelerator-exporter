{{- define "accelerator-exporter.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "accelerator-exporter.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else if contains (include "accelerator-exporter.name" .) .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name (include "accelerator-exporter.name" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "accelerator-exporter.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "accelerator-exporter.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "accelerator-exporter.selectorLabels" -}}
app.kubernetes.io/name: {{ include "accelerator-exporter.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "accelerator-exporter.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "accelerator-exporter.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "accelerator-exporter.clusterRoleName" -}}
{{- printf "%s-%s" .Release.Namespace (include "accelerator-exporter.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "accelerator-exporter.image" -}}
{{- printf "%s:%s" .Values.image.repository .Values.image.tag }}
{{- end }}

{{- define "accelerator-exporter.sources" -}}
{{- $sources := list -}}
{{- range $preset := .Values.config.sources -}}
{{- $source := required (printf "config.sources contains unknown preset %q" $preset) (index $.Values.config.sourceDefinitions $preset) -}}
{{- $sources = append $sources $source -}}
{{- end -}}
{{- range .Values.config.customSources -}}
{{- $sources = append $sources . -}}
{{- end -}}
{{- toYaml $sources -}}
{{- end }}
