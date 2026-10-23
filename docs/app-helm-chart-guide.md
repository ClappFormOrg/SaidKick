# App Helm Chart Guide

This guide covers what every application Helm chart submitted to the sAId-Kick Appstore must include to work correctly with the platform's deploy form, ingress automation, and ExternalDNS integration.

---

## Required: `values.schema.json`

The platform generates the deploy form dynamically from your chart's `values.schema.json`. Without it, submission fails. The schema must live at `<chart-path>/values.schema.json` relative to your repo root.

### Minimal schema

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "replicaCount": {
      "type": "integer",
      "minimum": 1,
      "maximum": 10,
      "default": 1,
      "title": "Aantal replicas",
      "description": "Aantal pod-replicas voor deze installatie."
    }
  }
}
```

### Secret fields

Fields the deployer must keep confidential (API keys, passwords) use `"format": "secret"`. The platform renders them as masked inputs and routes them directly to the secret store — they are **never** persisted in `Deployment.values`.

```json
"openai_api_key": {
  "type": "string",
  "format": "secret",
  "title": "OpenAI API Key",
  "description": "Uw OpenAI API-sleutel (sk-...)."
}
```

### Nested objects

Use nested objects to group related settings. The deploy form renders them as collapsible groups with a heading.

```json
"image": {
  "type": "object",
  "properties": {
    "repository": { "type": "string", "default": "nginx", "title": "Image" },
    "tag":        { "type": "string", "default": "1.27",  "title": "Tag" },
    "pullPolicy": {
      "type": "string",
      "enum": ["Always", "IfNotPresent", "Never"],
      "default": "IfNotPresent",
      "title": "Pull policy"
    }
  },
  "required": ["repository", "tag"]
}
```

---

## Recommended: ingress section

Including an ingress section in your schema enables automatic URL provisioning. When the deployer selects a cluster, the platform pre-fills `ingress.className` from the cluster's ingress class and suggests `<app-slug>.<cluster-base-domain>` as the hostname. The deployer just confirms and clicks install.

### Schema

```json
"ingress": {
  "type": "object",
  "title": "Ingress / externe toegang",
  "properties": {
    "enabled": {
      "type": "boolean",
      "default": false,
      "title": "Ingress inschakelen"
    },
    "className": {
      "type": "string",
      "default": "nginx",
      "title": "Ingress class",
      "description": "Wordt automatisch ingevuld vanuit de clusterinstellingen."
    },
    "host": {
      "type": "string",
      "title": "Hostname",
      "description": "bijv. mijnapp.apps.cluster.gemeente.nl"
    },
    "tls": {
      "type": "boolean",
      "default": true,
      "title": "TLS (HTTPS)",
      "description": "Vereist cert-manager op het cluster."
    }
  }
}
```

### Helm template — `templates/ingress.yaml`

```yaml
{{- if .Values.ingress.enabled }}
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: {{ .Release.Name }}
  labels:
    {{- include "<chart-name>.labels" . | nindent 4 }}
  annotations:
    {{- if .Values.ingress.tls }}
    cert-manager.io/cluster-issuer: letsencrypt-prod
    {{- end }}
spec:
  ingressClassName: {{ .Values.ingress.className | default "nginx" }}
  {{- if .Values.ingress.tls }}
  tls:
    - hosts:
        - {{ .Values.ingress.host | quote }}
      secretName: {{ .Release.Name }}-tls
  {{- end }}
  rules:
    - host: {{ .Values.ingress.host }}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: {{ .Release.Name }}
                port:
                  number: {{ .Values.service.port | default 80 }}
{{- end }}
```

When `ingress.enabled: true` and `ingress.host` is set:
- **ExternalDNS** (if installed on the cluster) automatically creates the DNS record pointing at the ingress-nginx load balancer IP.
- **cert-manager** (if installed and `ingress.tls: true`) automatically provisions a Let's Encrypt certificate.
- The sAId-Kick deployment detail page shows a clickable `https://<host>` link once the deployment reaches `ready`.

---

## Service template

The ingress template references `service.port`. Include a standard Service template:

```yaml
# templates/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: {{ .Release.Name }}
spec:
  selector:
    app: {{ .Release.Name }}
  ports:
    - port: {{ .Values.service.port | default 80 }}
      targetPort: http
```

And add to the schema:
```json
"service": {
  "type": "object",
  "properties": {
    "port": {
      "type": "integer",
      "default": 80,
      "minimum": 1,
      "maximum": 65535,
      "title": "Service port"
    }
  }
}
```

---

## `appstore.yaml` manifest

The platform validates your submission against the manifest at the repo root. The `spec.chart.path` must point to your chart directory. For a chart at `chart/`:

```yaml
apiVersion: appstore.saidkick.nl/v1
kind: AppManifest
metadata:
  name: my-app
  display_name: Mijn Applicatie
  vendor: Mijn Organisatie
  contact: platform@organisatie.nl
  license: EUPL-1.2
spec:
  chart:
    path: chart/
    type: helm
  ai:
    risk_class: minimal        # minimal | limited | high
    base_model: "n/a"
    data_residency: EU
  categories:
    - chatbot                  # agent | chatbot | rag | docs | analyse | infrastruct
  audit:
    dpia: audit/dpia.pdf
    security_scan: audit/security-scan.sarif
    ai_risk: audit/ai-risk-assessment.pdf
    accessibility: audit/accessibility-report.pdf
```

The platform fetches `values.schema.json` from `<spec.chart.path>/values.schema.json` at submission time. A missing or invalid schema blocks submission.

---

## Checklist before submitting

- [ ] `appstore.yaml` present at repo root with correct `spec.chart.path`
- [ ] `<chart-path>/values.schema.json` is valid JSON Schema (draft-07+)
- [ ] Secret fields use `"format": "secret"`
- [ ] Ingress section included with `enabled`, `className`, `host`, `tls` properties
- [ ] `templates/ingress.yaml` guards on `{{ if .Values.ingress.enabled }}`
- [ ] All required audit evidence files present (`audit/dpia.pdf`, etc.)
- [ ] `helm lint <chart-path>` exits 0
