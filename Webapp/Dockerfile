# sAId-Kick Appstore Webapp — production image.
#
# Multi-stage build:
#   1. builder  — installs deps + runs `nuxt build`
#   2. runner   — minimal Node.js image with only the .output/ bundle

# ---- builder ---------------------------------------------------------------
FROM node:26-alpine AS builder
WORKDIR /app

# Install dependencies first (layer-cached as long as package*.json unchanged).
COPY package.json package-lock.json* ./
# --legacy-peer-deps matches how the lock file is generated and how CI installs;
# without it npm ci computes peer dependencies the lock file leaves out and fails.
RUN npm ci --ignore-scripts --legacy-peer-deps

# Copy source and build.
COPY . .
RUN npm run build

# ---- runner ----------------------------------------------------------------
FROM node:26-alpine AS runner
WORKDIR /app

ENV NODE_ENV=production
ENV HOST=0.0.0.0
ENV PORT=3000

# Nuxt's SSR server output is self-contained in .output/.
COPY --from=builder /app/.output ./

# Run as non-root.
RUN addgroup -S nuxt && adduser -S nuxt -G nuxt
USER nuxt

EXPOSE 3000

CMD ["node", "server/index.mjs"]
