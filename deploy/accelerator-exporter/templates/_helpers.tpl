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
{{- $repository := .Values.image.repository -}}
{{- $registry := index (.Values.global | default dict) "imageRegistry" | default "" -}}
{{- if $registry -}}
{{- $repository = printf "%s/%s" $registry (regexReplaceAll "^[^/]+/" $repository "") -}}
{{- end -}}
{{- printf "%s:%s" $repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}

{{- define "accelerator-exporter.defaultSources" -}}
- name: nvidia-dcgm-exporter
  rule: nvidia-dcgm
  serviceMonitor:
    namespaceSelector:
      matchNames:
        - rune-system
        - gpu-operator
    selector:
      matchLabels:
        app: nvidia-dcgm-exporter
    port: gpu-metrics
    path: /metrics
    scheme: http
- name: volcano-vgpu-device-plugin
  rule: volcano-vgpu
  podMonitor:
    namespaceSelector:
      matchNames:
        - kube-system
    selector:
      matchLabels:
        app.kubernetes.io/name: volcano-device-plugin
        app.kubernetes.io/component: volcano-vgpu-device-plugin
    port: metrics
    path: /metrics
    scheme: http
- name: npu-exporter
  rule: ascend-npu
  podMonitor:
    namespaceSelector:
      matchNames:
        - npu-exporter
    selector:
      matchLabels:
        app: npu-exporter
    port: http
    path: /metrics
    scheme: http
{{- end }}
