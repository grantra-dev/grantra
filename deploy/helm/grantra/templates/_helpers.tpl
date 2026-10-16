{{- define "grantra.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "grantra.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "grantra.selector" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "grantra.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "grantra.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "grantra.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{- define "grantra.postgresql.host" -}}
{{- printf "%s-postgresql" (include "grantra.fullname" .) -}}
{{- end -}}

{{/* Secret holding database-url and migrate-database-url. */}}
{{- define "grantra.databaseSecret" -}}
{{- default (include "grantra.fullname" .) .Values.database.existingSecret -}}
{{- end -}}

{{- define "grantra.databaseURL" -}}
{{- if .Values.postgresql.enabled -}}
{{- printf "postgres://grantra_app:%s@%s:5432/grantra?sslmode=disable" (required "postgresql.auth.appPassword is required" .Values.postgresql.auth.appPassword | urlquery) (include "grantra.postgresql.host" .) -}}
{{- else -}}
{{- required "set database.url, database.existingSecret or postgresql.enabled" .Values.database.url -}}
{{- end -}}
{{- end -}}

{{- define "grantra.migrateDatabaseURL" -}}
{{- if .Values.postgresql.enabled -}}
{{- printf "postgres://grantra_owner:%s@%s:5432/grantra?sslmode=disable" (required "postgresql.auth.ownerPassword is required" .Values.postgresql.auth.ownerPassword | urlquery) (include "grantra.postgresql.host" .) -}}
{{- else -}}
{{- required "set database.migrateUrl, database.existingSecret or postgresql.enabled" .Values.database.migrateUrl -}}
{{- end -}}
{{- end -}}

{{- define "grantra.oidcIssuer" -}}
{{- if .Values.oidc.issuer -}}
{{- .Values.oidc.issuer -}}
{{- else if .Values.login.enabled -}}
{{- printf "%s/realms/grantra" (trimSuffix "/" .Values.login.hostname) -}}
{{- end -}}
{{- end -}}

{{- define "grantra.oidcSecret" -}}
{{- default (include "grantra.fullname" .) .Values.oidc.existingSecret -}}
{{- end -}}

{{- define "grantra.smtpSecret" -}}
{{- default (include "grantra.fullname" .) .Values.smtp.existingSecret -}}
{{- end -}}

{{- define "grantra.vaultSecret" -}}
{{- default (include "grantra.fullname" .) .Values.vault.existingSecret -}}
{{- end -}}

{{- define "grantra.loginSecret" -}}
{{- default (printf "%s-login" (include "grantra.fullname" .)) .Values.login.existingSecret -}}
{{- end -}}

{{- define "grantra.podSecurity" -}}
runAsNonRoot: true
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "grantra.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end -}}
