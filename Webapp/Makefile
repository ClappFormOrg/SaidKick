# sAId-Kick Appstore Webapp — make targets.
#
# Quick start:
#   make install   # npm install
#   make dev       # start dev server on :3000 with hot-reload
#   make build     # production SSR build → .output/
#   make preview   # serve the production build locally
#   make typecheck # run vue-tsc / nuxi typecheck
#   make lint      # same as typecheck
#   make test      # vitest unit tests
#   make clean     # remove .nuxt + .output + Vite dep cache (keeps node_modules)
#   make clean-all # also removes node_modules

NPM     ?= npm
NUXI    ?= npx nuxi

.PHONY: all install dev build preview generate typecheck lint test clean clean-all

all: build

# ---------------------------------------------------------------------------
# Dependencies
# ---------------------------------------------------------------------------
install:
	$(NPM) install

# ---------------------------------------------------------------------------
# Development
# ---------------------------------------------------------------------------
dev:
	$(NPM) run dev

# ---------------------------------------------------------------------------
# Production build
# ---------------------------------------------------------------------------

# SSR build — produces a Node.js server bundle in .output/.
# Used by the Docker image and the Helm chart deployment.
build:
	$(NPM) run build

# Static export — produces a fully pre-rendered site in .output/public/.
# Use this when deploying to a CDN without a Node.js runtime.
generate:
	$(NPM) run generate

# Serve the production .output/ bundle locally (requires a prior `make build`).
preview:
	$(NPM) run preview

# ---------------------------------------------------------------------------
# Quality
# ---------------------------------------------------------------------------
typecheck:
	$(NUXI) typecheck

lint: typecheck

# Node 25+ ships a global localStorage that shadows happy-dom's in Vitest.
# Node 24 accepts the flag too.
test: export NODE_OPTIONS := --no-experimental-webstorage $(NODE_OPTIONS)
test:
	$(NPM) run test

# ---------------------------------------------------------------------------
# Docker image
# ---------------------------------------------------------------------------
DOCKER_IMAGE  ?= ghcr.io/clappformorg/saidkick-webapp
DOCKER_TAG    ?= dev

docker-build:
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) -f Dockerfile .

docker-run:
	docker run --rm -p 3000:3000 \
	  -e API_BASE_URL=http://localhost:8080 \
	  $(DOCKER_IMAGE):$(DOCKER_TAG)

# ---------------------------------------------------------------------------
# Clean
# ---------------------------------------------------------------------------
# node_modules/.cache/vite holds Vite's pre-bundled dependency cache. Leaving it
# behind is why "I cleaned and it's still slow" used to be a thing — a stale dep
# cache survives a .nuxt wipe, so clearing it is the whole point of a clean.
clean:
	@powershell -Command "Remove-Item -Recurse -Force -ErrorAction SilentlyContinue .nuxt, .output, node_modules/.cache/vite, node_modules/.vite"

clean-all: clean
	@powershell -Command "Remove-Item -Recurse -Force -ErrorAction SilentlyContinue node_modules"
