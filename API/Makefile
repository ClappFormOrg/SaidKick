# sAId-Kick Appstore API — make targets.
#
# Quick start:
#   make tidy            # fetch all Go modules + sync go.sum
#   make build           # build the api binary into ./bin
#   make migrate         # run migrations against the configured DATABASE_URL
#   make run             # connect + migrate + block until SIGTERM
#   make dev-db          # spin up a throwaway Postgres in docker for local work
#   make fmt vet         # housekeeping

GO       ?= go
BIN_DIR  ?= bin
BIN      ?= $(BIN_DIR)/api
PKG      ?= ./cmd/api
DEMO_NAMES ?= ../scripts/demo-names.txt

.PHONY: all help tidy build run migrate drain-audit-dlq sarif-gate-report cleanup-demo fmt vet lint test clean dev-db dev-db-down dev-db-build dev-db-run dev-db-logs dev-db-psql

all: build

help:
	@echo "sAId-Kick Appstore API — available make targets"
	@echo ""
	@echo "  tidy             Download and sync Go modules (go mod tidy)"
	@echo "  build            Compile the API binary to $(BIN)"
	@echo "  run              Run migrations and start the server (:8080)"
	@echo "  migrate          Run DB migrations only, then exit"
	@echo "  drain-audit-dlq  Replay entries from the audit dead-letter queue"
	@echo "  sarif-gate-report  Count recent submissions SARIF_GATE_MODE=enforce would newly block"
	@echo "  cleanup-demo     List demo apps and clusters (APPLY=1 deletes them)"
	@echo "  fmt              Format all Go source files (go fmt ./...)"
	@echo "  vet              Run static analysis (go vet ./...)"
	@echo "  lint             Run golangci-lint"
	@echo "  test             Run all tests (go test ./...)"
	@echo "  clean            Remove the $(BIN_DIR)/ directory"
	@echo ""
	@echo "  dev-db           Build + start the local Postgres dev container"
	@echo "  dev-db-down      Stop and remove the dev Postgres container"
	@echo "  dev-db-logs      Tail dev Postgres container logs"
	@echo "  dev-db-psql      Open a psql shell on the dev Postgres container"

tidy:
	$(GO) mod tidy

build:
	@powershell -Command "if (-not (Test-Path '$(BIN_DIR)')) { New-Item -ItemType Directory -Path '$(BIN_DIR)' | Out-Null }"
	$(GO) build -o $(BIN) $(PKG)

run:
	$(GO) run $(PKG)

migrate:
	$(GO) run $(PKG) -migrate-only

drain-audit-dlq:
	$(GO) run ./cmd/drain-audit-dlq

cleanup-demo:
	$(GO) run ./cmd/cleanup-demo -names $(DEMO_NAMES) $(if $(APPLY),-apply)

sarif-gate-report:
	$(GO) run ./cmd/sarif-gate-report

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run

# The testcontainers reaper (Ryuk) fails under Docker Desktop on Windows; the
# tests remove their own containers. Set TESTCONTAINERS_RYUK_DISABLED=false to
# turn it back on.
test: export TESTCONTAINERS_RYUK_DISABLED ?= true
test:
	$(GO) test ./...

clean:
	powershell -Command "Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $(BIN_DIR)"

# Local dev Postgres via docker. Builds + runs the custom image at
# ../docker/postgres which pre-creates the production roles + extensions.
# Drops + recreates on every `make dev-db`.
DEV_DB_NAME  ?= saidkick-appstore-pg
DEV_DB_IMAGE ?= saidkick-appstore-pg:dev
DOCKER_CTX   ?= ../docker/postgres

dev-db: dev-db-build dev-db-run

dev-db-build:
	docker build -t $(DEV_DB_IMAGE) $(DOCKER_CTX)

dev-db-run:
	-docker rm -f $(DEV_DB_NAME)
	docker run -d --name $(DEV_DB_NAME) -p 5432:5432 $(DEV_DB_IMAGE)

dev-db-down:
	-docker rm -f $(DEV_DB_NAME)

dev-db-logs:
	docker logs -f $(DEV_DB_NAME)

dev-db-psql:
	docker exec -it $(DEV_DB_NAME) psql -U postgres -d saidkick_appstore
