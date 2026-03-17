{{/*
Expand the name of the chart.
*/}}
{{- define "tahini.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Full name: release-chart, truncated to 63 chars.
*/}}
{{- define "tahini.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "tahini.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "tahini.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "tahini.selectorLabels" -}}
app.kubernetes.io/name: {{ include "tahini.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
