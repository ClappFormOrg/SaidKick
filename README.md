# sAId-Kick Appstore

App store for public organisations: a Go API (`API/`), a Nuxt webapp
(`Webapp/`), Helm charts (`charts/`) and the cluster agent.

## Run it locally

Prerequisites:

| Tool | Version |
|---|---|
| Docker | Docker Desktop (Windows, macOS) or Docker Engine with the compose plugin (Linux) |
| Go | any Go 1.21 or newer; the Makefile sets `GOTOOLCHAIN=go1.26.9`, so Go downloads the version in `API/go.mod` |
| Node.js | 24, the version CI uses. Node 26 works too: `make test` turns off its built-in `localStorage` for Vitest |
| GNU make | 3.81 or newer. The recipes run in bash: on Windows run `make` from Git Bash |

```sh
make dev
```

`make dev` starts Postgres, OpenBao and VictoriaMetrics in Docker, loads the
dev seed, then runs the API on http://localhost:8080 and the webapp on
http://localhost:3000 with `[api]` and `[web]` prefixed logs. Ctrl+C stops
both; the containers keep running until `make down`.

| Target | What it does |
|---|---|
| `make help` | List the targets (default) |
| `make deps` | Start the docker compose services and wait until they are healthy |
| `make api` | Run the API with `APP_ENV=development` and dev-mode auth. Settings come from `API/.env`, created from `API/.env.example` on first run |
| `make webapp` | Run the Nuxt dev server against the local API |
| `make seed` | Load `scripts/dev/seed.json` (fake org `dev-org` with one user per role, and `review-org` with an auditor who can approve `dev-org` submissions) through `cmd/seed`, plus one public cluster from `scripts/dev/seed-cluster.sql`. Safe to re-run |
| `make dev` / `make up` | `deps`, `seed`, then API and webapp together |
| `make down` | Stop and remove the containers. Postgres has no volume, so its data goes too |
| `make clean` | `down` plus the volumes (VictoriaMetrics data) |
| `make test` / `make lint` | Run the API and Webapp targets of the same name (`lint` also runs `go vet`, and skips `golangci-lint` when it is not installed). The API tests start Postgres containers with the testcontainers reaper off (`TESTCONTAINERS_RYUK_DISABLED=true`, overridable) |
| `make kind-up` / `make kind-down` | Run and tear down the kind end-to-end flow in [`scripts/kind-e2e`](scripts/kind-e2e/README.md) |

The API runs in dev mode: send a seeded user's id as the `X-Dev-User-ID`
header (`make seed` prints the ids):

```sh
curl -H "X-Dev-User-ID: <dev_user_id>" http://localhost:8080/api/v1/me
```

More on the API, OpenBao, metrics and Argo CD in
[`API/RUNBOOK.md`](API/RUNBOOK.md); contribution rules in
[`CONTRIBUTING.md`](CONTRIBUTING.md).
