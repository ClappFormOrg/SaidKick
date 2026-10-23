# ── Stage 1: build ────────────────────────────────────────────────────────────
FROM golang:1.27-alpine AS builder

# ca-certificates is needed for HTTPS calls (GitHub, Vault) made by the binary.
RUN apk add --no-cache ca-certificates git

WORKDIR /app

# Download modules before copying source so Docker caches this layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w -extldflags=-static" \
    -o /out/api ./cmd/api

# ── Stage 2: runtime ──────────────────────────────────────────────────────────
# distroless/static has no shell, no package manager, minimal attack surface.
FROM gcr.io/distroless/static-debian12

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /out/api /api

# Run as nobody (UID 65534) — matches the Helm podSecurityContext.
USER 65534:65534

EXPOSE 8080

ENTRYPOINT ["/api"]
