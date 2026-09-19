# syntax=docker/dockerfile:1
# Multi-stage: build the Svelte frontend, embed it in a static Go binary, ship distroless.
# Build context is the repo root: `docker build -f deploy/Dockerfile .`

FROM node:24-alpine AS web
WORKDIR /src/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.26-alpine AS go
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/adguard-reward ./cmd/adguard-reward

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go /out/adguard-reward /adguard-reward
VOLUME ["/data"]
EXPOSE 8080
# No default --config: with none passed the binary looks for config.yaml
# beside itself (absent here) and runs from ADGUARD_REWARD_* env alone. To use
# a file instead, mount it and pass `--config /data/config.yaml` yourself.
ENTRYPOINT ["/adguard-reward"]
