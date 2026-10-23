# How to Submit Your App to the sAId-Kick App Store

A step-by-step guide for external developers.

---

## Before You Begin — Checklist

Before starting, make sure you have:

- [ ] A GitHub repository (public or private)
- [ ] A working Helm chart for your application
- [ ] All container images hosted on a public container registry (e.g. **ghcr.io** or Docker Hub)
- [ ] An account on the sAId-Kick platform (federated via EntraID / Keycloak)
- [ ] Four audit documents (see Step 2)

---

## Step 1 — Set Up Your Repository Structure

Your repository must follow this exact layout:

```
your-org/your-app/
│
├── appstore.yaml              ← Required: app manifest
│
├── chart/                     ← Your Helm chart
│   ├── Chart.yaml
│   ├── values.yaml
│   ├── values.schema.json     ← Required: defines the deploy form
│   └── templates/
│
├── docs/
│   └── screenshots/           ← Minimum 3 screenshots (PNG or JPEG)
│       ├── screenshot-1.png
│       ├── screenshot-2.png
│       └── screenshot-3.png
│
└── audit/                     ← All four audit documents
    ├── dpia.pdf
    ├── security-scan.sarif
    ├── ai-risk-assessment.pdf
    └── accessibility-report.pdf
```

> **Tip:** Keep this structure at the root of your `main` branch.

---

## Step 2 — Prepare Your Four Audit Documents

Every submission requires four documents placed in the `audit/` folder.

| Document | File | What it proves |
|---|---|---|
| Data Protection Impact Assessment | `dpia.pdf` | GDPR / AVG compliance, signed by your DPO |
| Security Scan | `security-scan.sarif` | No vulnerabilities with CVSS score ≥ 7.0 |
| AI Risk Assessment | `ai-risk-assessment.pdf` | EU AI Act compliance, model transparency |
| Accessibility Report | `accessibility-report.pdf` | WCAG 2.2 Level AA compliance |

**Security scan tools that produce SARIF:**
- Trivy, Snyk, SonarQube, OWASP ZAP

> **Important:** Any security finding with a CVSS score of 7.0 or higher will **automatically block** your submission. Fix all high-severity findings before submitting.

> **Phase-in:** The platform currently blocks findings reported at SARIF `level: "error"` whose CVSS score sits on the result itself. Two more cases are being phased in: findings at any other level (including `warning`, the SARIF default when `level` is omitted), and findings whose score is set on the rule in `tool.driver.rules[].properties.security-severity`, as Trivy and CodeQL report it. For now the platform records these without blocking. They will block from a date still to be announced, so fix them now.

---

## Step 3 — Create Your App Manifest (`appstore.yaml`)

Place this file at the **root** of your repository.

```yaml
apiVersion: appstore.saidkick.nl/v1
kind: AppManifest

metadata:
  name: mijn-ai-app              # Unique slug — lowercase, hyphens only. Cannot be changed later.
  display_name: Mijn AI-App      # Human-readable name shown in the catalogue
  vendor: Provincie Voorbeeld    # Your organisation name
  contact: beheer@voorbeeld.nl   # Support email or helpdesk URL
  license: EUPL-1.2              # Must be one of the allowed licenses (see below)

spec:
  chart:
    path: chart/                 # Relative path to your Helm chart folder
    type: helm

  categories:
    - chatbot                    # One or more: agent | chatbot | rag | docs | analyse | infrastruct

  ai:
    risk_class: limited          # EU AI Act class: minimal | limited | high
    base_model: GEITje-7B-ultra  # Name of the model your app uses
    data_residency: EU           # Must be exactly "EU"

  interfaces:
    - type: api
      openapi: docs/openapi.yaml # Path to your OpenAPI spec (if applicable)

  demo:
    description: >
      A short description of your app, max 500 characters.
      Shown in the catalogue listing.

    screenshots:
      - docs/screenshots/screenshot-1.png
      - docs/screenshots/screenshot-2.png
      - docs/screenshots/screenshot-3.png

  audit:
    dpia:            audit/dpia.pdf
    security_scan:   audit/security-scan.sarif
    ai_risk:         audit/ai-risk-assessment.pdf
    accessibility:   audit/accessibility-report.pdf
```

**Allowed licenses:** `EUPL-1.2` · `Apache-2.0` · `MIT` · `BSD-3-Clause` · `GPL-3.0-only` · `LGPL-3.0-only` · `MPL-2.0`

---

## Step 4 — Create Your Helm Chart Schema (`values.schema.json`)

This file drives the deployment form that other organisations see when they deploy your app. It must live at `chart/values.schema.json`.

```json
{
  "$schema": "https://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["image"],
  "properties": {
    "replicaCount": {
      "type": "integer",
      "default": 1,
      "description": "Number of replicas to run"
    },
    "image": {
      "type": "object",
      "required": ["repository"],
      "properties": {
        "repository": {
          "type": "string",
          "description": "Container image (e.g. ghcr.io/your-org/your-app)"
        },
        "tag": {
          "type": "string",
          "description": "Image tag or digest"
        }
      }
    },
    "apiKey": {
      "type": "string",
      "format": "secret",
      "description": "API key for the external service"
    }
  }
}
```

> **Secret fields:** Any field with `"format": "secret"` is routed directly to Vault — it is **never** stored in plaintext and does not appear in logs or the deployment UI.

---

## Step 5 — Submit Your App

**5a. Sign in** to the sAId-Kick platform with your organisation's account.

**5b.** Click **"Submit new app"** in the dashboard.

**5c.** You will be redirected to **GitHub** to install the sAId-Kick GitHub App on your repository.

Grant these permissions:
- Contents: **Read** — to fetch your manifest and audit files
- Metadata: **Read** — to read repository info
- Webhooks: **Read & Write** — to receive push notifications

**5d.** Select your repository and confirm.

**5e.** The platform runs **automated validation** (< 60 seconds):

| Check | What fails it |
|---|---|
| Manifest schema | Missing fields, wrong enum values, invalid license |
| Audit files | Missing file, empty file, wrong format |
| Security scan | Any CVSS ≥ 7.0 finding |
| Helm chart | Invalid `Chart.yaml` or `values.schema.json` |

If validation passes, your app enters **Draft** status.

**5f.** Fill in the **audit questionnaire** in the platform UI (AI risk, data residency confirmation, accessibility checklist).

**5g.** Click **"Submit for review"**. Your app moves to **Submitted** status.

---

## Step 6 — Cross-Organisational Audit

An auditor from a **different organisation** reviews your submission. This is enforced — someone from your own organisation cannot approve your app.

The auditor can:

- **Request changes** → your app returns to Draft; fix and resubmit
- **Approve** → your app is published to the catalogue
- **Reject** → you may submit a new version

**Typical review time: 2–5 business days.**

---

## Step 7 — Your App Is Live

Once approved, your app appears in the public catalogue within 30 seconds.

Other organisations can:
1. Find your app in the catalogue
2. Click **Deploy**
3. Select their target cluster
4. Fill in the deployment form (generated from your `values.schema.json`)
5. Provide secrets for any `"format": "secret"` fields
6. Click **Deploy** — Argo CD reconciles your chart on their cluster

---

## Keeping Your App Up to Date

Every time you push to your `main` branch:

1. Automated validation re-runs automatically (via webhook)
2. If it passes → a new version is created and sent for re-audit
3. If it fails → existing deployments are unaffected (they pin to an immutable git SHA)
4. Once the new version is approved → deployers see **"Update available"** in their dashboard

> Existing deployments are **never** auto-updated. Each update requires explicit approval from the deploying organisation.

---

## Common Mistakes to Avoid

| Mistake | What happens |
|---|---|
| No `values.schema.json` in the chart | Submission rejected immediately |
| SARIF file with CVSS ≥ 7.0 finding | Auto-rejected — fix findings first |
| `data_residency` is not exactly `"EU"` | Auto-rejected |
| App `name` (slug) changed after first publish | Immutable — create a new submission |
| Secrets in plain `values.yaml` | Use `"format": "secret"` in the schema instead |
| Auditor from your own org reviewing | Platform returns a 403 error — this is by design |
| Screenshots missing or fewer than 3 | Submission blocked at validation |

---

## Quick Reference

```
Minimum screenshots:    3  (max 20)
Allowed image formats:  PNG, JPEG
Max video size:         100 MB (MP4)
Max SARIF file size:    100 KB
App name (slug):        3–60 chars, lowercase + digits + hyphens, starts with a letter
Data residency:         Must be "EU"
Security threshold:     No CVSS ≥ 7.0 findings
Image registry:         Any public OCI registry (ghcr.io recommended)
```

---

*Questions? Contact platform@clappform.com or open an issue at github.com/ClappFormOrg/SaidKick*
