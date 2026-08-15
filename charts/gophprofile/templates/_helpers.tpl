{{/*
Базовое имя чарта.
*/}}
{{- define "gophprofile.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Полное имя релиза. Используется как префикс имён ресурсов.
*/}}
{{- define "gophprofile.fullname" -}}
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
Общие метки для всех ресурсов чарта.
*/}}
{{- define "gophprofile.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{ include "gophprofile.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Метки для отбора подов. Менять их после установки нельзя:
selector в Deployment неизменяем.
*/}}
{{- define "gophprofile.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gophprofile.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Имя ServiceAccount.
*/}}
{{- define "gophprofile.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "gophprofile.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Имя Secret с учётными данными.
*/}}
{{- define "gophprofile.secretName" -}}
{{- if .Values.secrets.existingSecret }}
{{- .Values.secrets.existingSecret }}
{{- else }}
{{- printf "%s-secrets" (include "gophprofile.fullname" .) }}
{{- end }}
{{- end }}