# sAId-Kick Appstore: run the whole platform locally.
#
#   make dev    # docker services + seed + API + webapp, Ctrl+C stops API and webapp
#   make down   # stop the docker services
#
# The recipes use bash. On Windows run make from Git Bash (GNU make 3.81 from
# GnuWin32 works); the API and Webapp Makefiles are reused via $(MAKE) -C.

SHELL := bash

COMPOSE      ?= docker compose
API_BASE_URL ?= http://localhost:8080
SEED_FILE    ?= scripts/dev/seed.json
APP_ENV      ?= development
APP_DEV_MODE ?= true

# Same toolchain as API/go.mod. Override with GOTOOLCHAIN=local to use
# the installed Go as is.
GOTOOLCHAIN  ?= go1.26.9
export GOTOOLCHAIN

.DEFAULT_GOAL := help

.PHONY: help deps api webapp seed dev up down clean test lint kind-up kind-down api-env webapp-deps

help: ## List the targets
	@echo "sAId-Kick Appstore: local development"
	@echo ""
	@awk 'BEGIN { FS = ":.*## " } /^[a-z-]+:.*## / { printf "  %-10s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo ""
	@echo "API http://localhost:8080, webapp http://localhost:3000, OpenBao http://localhost:8200, VictoriaMetrics http://localhost:8480"

deps: ## Start Postgres, OpenBao and VictoriaMetrics and wait until healthy
	$(COMPOSE) up -d --wait --wait-timeout 180
	@$(COMPOSE) ps

api: api-env ## Run the API on :8080 (dev-mode auth, settings from API/.env)
	APP_ENV=$(APP_ENV) APP_DEV_MODE=$(APP_DEV_MODE) "$(MAKE)" --no-print-directory -C API run

webapp: webapp-deps ## Run the Nuxt dev server on :3000 against the local API
	API_BASE_URL=$(API_BASE_URL) "$(MAKE)" --no-print-directory -C Webapp dev

seed: deps api-env ## Load the fake dev data (two orgs, a user per role, one public cluster)
	cd API && DATABASE_URL="$$(sed -n 's/^DATABASE_URL=//p' .env | tail -n 1 | tr -d '\r')" \
	  APP_ENV=$(APP_ENV) APP_DEV_MODE=true go run ./cmd/seed ../$(SEED_FILE)
	$(COMPOSE) exec -T postgres psql -U postgres -d saidkick_appstore -v ON_ERROR_STOP=1 -q < scripts/dev/seed-cluster.sql
	@echo "Send a dev_user_id above as the X-Dev-User-ID header to call the API as that user."

dev: seed webapp-deps ## deps + seed, then API and webapp with prefixed logs (Ctrl+C stops both)
	APP_ENV=$(APP_ENV) APP_DEV_MODE=$(APP_DEV_MODE) API_BASE_URL=$(API_BASE_URL) MAKE="$(MAKE)" bash scripts/dev/run.sh

up: dev ## Alias for dev

down: ## Stop and remove the dev containers (Postgres data is not kept)
	$(COMPOSE) down --remove-orphans

clean: ## Like down, and also delete the volumes (VictoriaMetrics data)
	@echo "WARNING: removing the dev containers and their volumes; local metrics data is deleted."
	$(COMPOSE) down -v --remove-orphans

test: webapp-deps ## Run the API and Webapp unit tests
	"$(MAKE)" --no-print-directory -C API test
	"$(MAKE)" --no-print-directory -C Webapp test

lint: webapp-deps ## go vet, golangci-lint (when installed) and the Webapp typecheck
	"$(MAKE)" --no-print-directory -C API vet
	@if command -v golangci-lint >/dev/null 2>&1; then \
	  "$(MAKE)" --no-print-directory -C API lint; \
	else \
	  echo "golangci-lint not found, skipping (the project pins v2.14.0)"; \
	fi
	"$(MAKE)" --no-print-directory -C Webapp lint

kind-up: ## Run the kind hub+spoke end-to-end flow (scripts/kind-e2e/run.sh)
	bash scripts/kind-e2e/run.sh

kind-down: ## Delete the kind clusters and run state (scripts/kind-e2e/teardown.sh)
	bash scripts/kind-e2e/teardown.sh

api-env:
	@if [ ! -f API/.env ]; then \
	  cp API/.env.example API/.env; \
	  echo "Created API/.env from API/.env.example (gitignored; edit it for local settings)."; \
	fi

webapp-deps:
	@if [ ! -d Webapp/node_modules ]; then \
	  echo "Webapp/node_modules missing, running npm install"; \
	  "$(MAKE)" --no-print-directory -C Webapp install; \
	fi
