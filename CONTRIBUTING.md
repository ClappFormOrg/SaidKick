# Contributing to sAId-Kick Appstore

## Licence

The sAId-Kick Appstore platform (this repository) is licensed under the
**European Union Public Licence v1.2 (EUPL-1.2)**. By contributing you agree
that your changes are made available under that licence.

Client SDKs and integrations intended for general developer consumption live in
separate repositories (e.g. `ClappFormOrg/saidkick-sdk-*`) and are released under
the **Apache License 2.0**. Contributions to those repos are governed by their
own `LICENSE` files.

## Developer Certificate of Origin

All commits must carry a `Signed-off-by` trailer certifying that you have the
right to submit the work under the project licence (see
[developercertificate.org](https://developercertificate.org)):

```
Signed-off-by: Your Name <your.email@example.com>
```

Add it automatically with:

```sh
git commit -s -m "type(scope): subject"
```

Pull requests without a `Signed-off-by` on every commit will not be merged.

## Prerequisites

| Tool | Version | Install |
|---|---|---|
| Go | 1.21 or newer. The Makefile sets `GOTOOLCHAIN=go1.26.9`, so Go fetches the toolchain named in `API/go.mod` | [go.dev/dl](https://go.dev/dl/) |
| Node.js | 24 or newer, for the webapp | [nodejs.org](https://nodejs.org/) |
| Docker | Docker Desktop, or Docker Engine with the compose plugin. The database tests need it | [docker.com](https://www.docker.com/products/docker-desktop/) |
| GNU make | 3.81 or newer. The recipes run in bash, so use Git Bash on Windows | `choco install make` (Windows) or included with WSL/macOS |

## Local development setup

```sh
git clone https://github.com/ClappFormOrg/SaidKick.git
cd SaidKick
make dev
```

`make dev` starts Postgres, OpenBao and VictoriaMetrics in Docker, loads the
dev seed, then runs the API on http://localhost:8080 and the webapp on
http://localhost:3000. `make help` lists the rest; the [README](README.md)
describes each target.

To work on the API alone:

```sh
cd API

# Download Go modules
make tidy

# Start the dev Postgres container: a custom postgres:18-alpine image with
# pgcrypto, citext, and the app_rw and audit_writer roles
make dev-db

# Create your local env file
cp .env.example .env

# Run migrations and start the server (listens on :8080)
make run
```

Verify it works:

```sh
curl http://localhost:8080/healthz   # → {"status":"ok"}
```

See [API/RUNBOOK.md](API/RUNBOOK.md) for seeding a dev user, configuring GitHub
App credentials, Vault, and Argo CD.

## Making changes

1. Fork the repository and create a feature branch from `main`.
2. Write your code. Follow the existing style (Echo handlers, GORM models,
   `apierror` for all error responses).
3. For schema changes, add a versioned migration entry to
   `API/internal/db/migrate.go` — never modify existing entries.
   See the "When the model changes" section of RUNBOOK.md.
4. Run `make vet` and `make test` before opening a PR.
5. Open a pull request against `main`. Fill in the PR description with what
   changed and why.

## Commit style

Follow [Conventional Commits](https://www.conventionalcommits.org/):
`type(scope): subject` — imperative mood, no trailing period, ≤ 72 characters.

Common types: `feat` · `fix` · `chore` · `docs` · `refactor` · `test` · `ci`

Example:

```
feat(deployments): add rollback endpoint

Implements POST /deployments/:id/rollback. Delegates to
orchestrator.Deployer.Rollback with the target git SHA.
```

## Code style

Follow the patterns already present in the codebase:

- **Error handling** — errors are either logged *or* returned, never both. Wrap
  with `fmt.Errorf("context: %w", err)` when propagating; use `%v` only at the
  outermost boundary (HTTP response). Never silently discard with `_` unless the
  call is truly fire-and-forget (add a comment explaining why).
- **Handlers** — use `apierror.New` / `apierror.FieldError` for all error
  responses; never write raw `c.JSON(4xx, ...)` inline.
- **Models** — embed `IDModel` for UUIDv7 primary keys; add new schema changes as
  timestamped entries in `API/internal/db/migrate.go`, never modify existing ones.
- **Logging** — use `slog` (structured, JSON in production). Keep message strings
  low-cardinality; attach IDs and counts as key-value attributes.

## Documentation standards

Every exported symbol **must** have a doc comment. The comment starts with the
symbol name and a verb phrase that explains *why* it exists, not just *what* it is:

```go
// BatchStateUpdater processes pending deployment state transitions from the
// agent heartbeat queue. It caps batch size to avoid oversized DB transactions.
type BatchStateUpdater struct { ... }
```

Every package must have a package comment in one file (or `doc.go`):

```go
// Package handlers implements the Echo HTTP handlers for every API endpoint.
// Each domain (apps, deployments, clusters, audit) lives in its own file.
package handlers
```

Run `go doc ./...` to preview how your comments render before opening a PR.

## Testing

```sh
cd API
make test        # go test ./...
make vet         # static analysis
make fmt         # auto-format
```

`make test` and `make lint` from the repository root run the API and the webapp
together.

**Unit tests** — cover pure logic that needs no database: validation helpers,
manifest parsing, crypto functions, mapping utilities. Don't mock the DB layer;
use a test container instead.

**Database tests** — `internal/testutil` starts a real Postgres container
through `testcontainers-go`, so Docker has to be running: without it those
packages fail with `failed to create Docker provider`. The API's `test` target
disables the testcontainers reaper (`TESTCONTAINERS_RYUK_DISABLED=true`), which
misbehaves under Docker Desktop on Windows — the tests remove their own
containers. Set it to `false` to turn the reaper back on.

**Testing handlers** — Echo handlers can be tested with `httptest.NewRecorder`
and a real `*db.Handle` pointed at a test container. Avoid mocking GORM — the
mocked interface diverges from real Postgres behaviour in subtle ways.

## PR review expectations

- PRs are reviewed within 2 business days.
- One approving review from a maintainer is required to merge.
- `make lint` and `make test` pass. This repository runs no CI, so check them
  yourself before opening the PR.
- Squash-merge is preferred for feature branches; history is preserved for
  larger refactors.
