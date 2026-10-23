{{/*
Expand the name of the chart.
*/}}
{{- define "saidkick-hub.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "saidkick-hub.fullname" -}}
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
Create chart label value.
*/}}
{{- define "saidkick-hub.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "saidkick-hub.labels" -}}
helm.sh/chart: {{ include "saidkick-hub.chart" . }}
{{ include "saidkick-hub.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels (shared by Deployment + Service).
*/}}
{{- define "saidkick-hub.selectorLabels" -}}
app.kubernetes.io/name: {{ include "saidkick-hub.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
API component labels.
*/}}
{{- define "saidkick-hub.api.labels" -}}
{{ include "saidkick-hub.labels" . }}
app.kubernetes.io/component: api
{{- end }}

{{/*
API selector labels.
*/}}
{{- define "saidkick-hub.api.selectorLabels" -}}
{{ include "saidkick-hub.selectorLabels" . }}
app.kubernetes.io/component: api
{{- end }}

{{/*
Agent listener Service labels. A distinct component keeps the agent Service
out of selectors that look up the main API Service by component=api.
*/}}
{{- define "saidkick-hub.apiAgent.labels" -}}
{{ include "saidkick-hub.labels" . }}
app.kubernetes.io/component: api-agent
{{- end }}

{{/*
Prints "true" when api.config.appEnv is prod-like, mirroring the API's
isProdLikeEnv: only dev/development/local/test/ci are non-production.
*/}}
{{- define "saidkick-hub.prodLikeEnv" -}}
{{- if not (has (lower .Values.api.config.appEnv) (list "dev" "development" "local" "test" "ci")) }}true{{ end }}
{{- end }}

{{/*
Fails the render when the agent listener is disabled under a prod-like
appEnv, where the API would refuse to boot.
*/}}
{{- define "saidkick-hub.agentListener.validate" -}}
{{- if and (not .Values.api.agentListener.enabled) (include "saidkick-hub.prodLikeEnv" .) }}
{{- fail (printf "api.agentListener.enabled=false is only allowed when api.config.appEnv is dev/development/local/test/ci (got %q): the API refuses to boot without the agent mTLS listener" .Values.api.config.appEnv) }}
{{- end }}
{{- end }}

{{/*
Webapp component labels.
*/}}
{{- define "saidkick-hub.webapp.labels" -}}
{{ include "saidkick-hub.labels" . }}
app.kubernetes.io/component: webapp
{{- end }}

{{/*
Webapp selector labels.
*/}}
{{- define "saidkick-hub.webapp.selectorLabels" -}}
{{ include "saidkick-hub.selectorLabels" . }}
app.kubernetes.io/component: webapp
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "saidkick-hub.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "saidkick-hub.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
API Secret name — uses existingSecret if provided, otherwise the chart-managed one.
*/}}
{{- define "saidkick-hub.api.secretName" -}}
{{- if .Values.api.secrets.existingSecret }}
{{- .Values.api.secrets.existingSecret }}
{{- else }}
{{- printf "%s-api" (include "saidkick-hub.fullname" .) }}
{{- end }}
{{- end }}

{{/*
API image reference (repository:tag, falling back to appVersion).
*/}}
{{- define "saidkick-hub.api.image" -}}
{{- $tag := .Values.api.image.tag | default .Chart.AppVersion }}
{{- printf "%s:%s" .Values.api.image.repository $tag }}
{{- end }}

{{/*
Webapp image reference.
*/}}
{{- define "saidkick-hub.webapp.image" -}}
{{- $tag := .Values.webapp.image.tag | default .Chart.AppVersion }}
{{- printf "%s:%s" .Values.webapp.image.repository $tag }}
{{- end }}

{{/*
PostgreSQL component labels.
*/}}
{{- define "saidkick-hub.postgres.labels" -}}
{{ include "saidkick-hub.labels" . }}
app.kubernetes.io/component: postgresql
{{- end }}

{{/*
PostgreSQL selector labels.
*/}}
{{- define "saidkick-hub.postgres.selectorLabels" -}}
{{ include "saidkick-hub.selectorLabels" . }}
app.kubernetes.io/component: postgresql
{{- end }}

{{/*
Zalando postgresql cluster name — used as the CR metadata.name and as the
Kubernetes Service name that Zalando creates for the master endpoint.
*/}}
{{- define "saidkick-hub.postgres.clusterName" -}}
{{- printf "%s-postgresql" (include "saidkick-hub.fullname" .) }}
{{- end }}

{{/*
Name of the Zalando-managed superuser credential Secret.
Format: postgres.{clusterName}.credentials.postgresql.acid.zalan.do
*/}}
{{- define "saidkick-hub.postgres.superuserSecretName" -}}
{{- printf "postgres.%s.credentials.postgresql.acid.zalan.do" (include "saidkick-hub.postgres.clusterName" .) }}
{{- end }}

{{/*
Name of the Zalando-managed app user credential Secret.
Format: {username}.{clusterName}.credentials.postgresql.acid.zalan.do
*/}}
{{- define "saidkick-hub.postgres.userSecretName" -}}
{{- printf "%s.%s.credentials.postgresql.acid.zalan.do" .Values.postgresql.username (include "saidkick-hub.postgres.clusterName" .) }}
{{- end }}
