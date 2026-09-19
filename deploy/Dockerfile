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
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /out/adguard-reward ./cmd/adguard-reward

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go /out/adguard-reward /adguard-reward
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/adguard-reward"]
CMD ["--config", "/data/config.yaml"]
