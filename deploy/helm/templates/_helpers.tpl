{{/*
Expand the name of the chart.
*/}}
{{- define "alibabacloud-erdma-controller.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "alibabacloud-erdma-controller.fullname" -}}
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
{{- define "alibabacloud-erdma-controller.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "alibabacloud-erdma-controller.labels" -}}
helm.sh/chart: {{ include "alibabacloud-erdma-controller.chart" . }}
{{ include "alibabacloud-erdma-controller.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "alibabacloud-erdma-controller.selectorLabels" -}}
app.kubernetes.io/name: {{ include "alibabacloud-erdma-controller.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "alibabacloud-erdma-controller.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "alibabacloud-erdma-controller.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Require explicit eRDMA opt-in on Lingjun nodes, AND each user affinity term.
Retain preferred affinity, pod affinity and matchFields.
*/}}
{{- define "alibabacloud-erdma-controller.agentAffinity" -}}
{{- $affinity := deepCopy (.Values.affinity | default dict) -}}
{{- $node := get $affinity "nodeAffinity" | default dict -}}
{{- $hasRequired := hasKey $node "requiredDuringSchedulingIgnoredDuringExecution" -}}
{{- $required := get $node "requiredDuringSchedulingIgnoredDuringExecution" | default dict -}}
{{- $terms := list (dict) -}}
{{- if $hasRequired -}}
{{- $terms = get $required "nodeSelectorTerms" | default list -}}
{{- end -}}
{{- $filtered := list -}}
{{- $workerLabel := "alibabacloud.com/lingjun-worker" -}}
{{- $ordinaryCondition := dict "key" $workerLabel "operator" "NotIn" "values" (list "true") -}}
{{- $lingjunConditions := list
    (dict "key" $workerLabel "operator" "In" "values" (list "true"))
    (dict "key" "network.alibabacloud.com/erdma-enabled" "operator" "In" "values" (list "true"))
-}}
{{- range $term := $terms -}}
{{- $expressions := get $term "matchExpressions" | default list -}}
{{- if and $hasRequired (empty $expressions) (empty (get $term "matchFields")) -}}
{{- $filtered = append $filtered $term -}}
{{- else -}}
{{- $ordinary := deepCopy $term -}}
{{- $_ := set $ordinary "matchExpressions" (append $expressions $ordinaryCondition) -}}
{{- $filtered = append $filtered $ordinary -}}
{{- $lingjun := deepCopy $term -}}
{{- $_ := set $lingjun "matchExpressions" (concat $expressions $lingjunConditions) -}}
{{- $filtered = append $filtered $lingjun -}}
{{- end -}}
{{- end -}}
{{- $_ := set $required "nodeSelectorTerms" $filtered -}}
{{- $_ := set $node "requiredDuringSchedulingIgnoredDuringExecution" $required -}}
{{- $_ := set $affinity "nodeAffinity" $node -}}
{{- toYaml $affinity -}}
{{- end -}}
