# sAId-Kick Appstore — Bruno collection

Open this folder in [Bruno](https://www.usebruno.com/) → **Open Collection**.

## Setup

1. Start the API per [`API/RUNBOOK.md`](../../API/RUNBOOK.md). Confirm
   `curl http://localhost:8080/healthz` returns `{"status":"ok"}`.
2. Seed a dev user (see the runbook section "Seeding a dev user").
3. In Bruno, select the **Local** environment (top-right).
4. Open environment variables (`...` menu) and paste the dev user's UUID
   into `devUserId`. The other variables auto-fill as you exercise
   create endpoints (or you can paste them manually).
5. Run any request from the sidebar.

## How auth works in V0

Every authenticated request sends:

```
X-Dev-User-ID: {{devUserId}}
```

The `DevModeAuthenticator` (active when `APP_DEV_MODE=true`) looks up
the User by id, attaches a Principal with all active roles, and lets
the handler run RBAC checks. Anonymous requests omit the header and
get either a 200 (catalogue read) or a 401 (anything else).

In production this header is ignored — the `OIDCAuthenticator` (shipped
in Sprint 3) handles auth via the per-org OIDC authorization code flow.
See the `10 Auth` folder for the login / callback / logout requests.
The API requests in this collection don't change; only the auth source
does (cookie vs. header).

## Folder map

| Folder           | Requests | Status                     |
|------------------|----------|----------------------------|
| `00 Health`      | 2        | ✅ all implemented          |
| `01 Catalog`     | 2        | ✅ all implemented          |
| `02 Apps`        | 4        | ✅ all implemented          |
| `03 Audit`       | 4        | ✅ all implemented          |
| `04 Clusters`    | 5        | ✅ all implemented          |
| `05 Deployments` | 8        | ✅ all implemented          |
| `06 Orgs`        | 4        | ✅ all implemented          |
| `07 Webhooks`    | 1        | ✅ all implemented          |
| `08 Branding`    | 6        | ✅ all implemented          |
| `10 Auth`        | 3        | ✅ all implemented          |

Requests that target an integration not yet wired (Argo CD, Vault PKI,
secrets store) return `503 *_not_configured` with a clear `code` field
so the webapp can pattern-match cleanly. All V1 handlers are implemented;
there are no 501 stubs.

Note: The following implemented endpoints have no Bruno requests yet:
- `GET /api/v1/community/discussions` — community discussions proxy
- `GET /api/v1/advisories` / `POST /api/v1/advisories` — platform advisories
- `GET /api/v1/api-keys` / `POST /api/v1/api-keys` / `DELETE /api/v1/api-keys/:id` — API key management
- `GET /api/v1/orgs` — federated org discovery (used by login page)
- `GET /api/v1/me` — current user
- `POST /api/v1/audit-submissions/:id/comments` / `GET /api/v1/audit-submissions/:id/comments` — audit comments
- `GET /api/v1/audit-submissions/:id/evidence/:name` — evidence file download
- `GET /api/v1/clusters/:id` / `PATCH /api/v1/clusters/:id` — get/patch cluster detail
- `GET /api/v1/clusters/:id/access` / `POST /api/v1/clusters/:id/access` / `DELETE /api/v1/clusters/:id/access/:org_id` — cluster access grants
- `POST /api/v1/clusters/:id/heartbeat` / `POST /api/v1/clusters/:id/deployment-states` / `POST /api/v1/clusters/:id/commands/:cmd_id/ack` — agent endpoints (served on the agent mTLS listener, `AGENT_TLS_ADDR`; a dev-mode hub also serves them on the main URL with `X-Agent-Cluster-ID`)
