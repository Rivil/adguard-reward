# adguard-reward — developer entry points. `make help` lists targets.
BIN      := adguard-reward
MODULE   := github.com/Rivil/adguard-reward
WEB      := web
GOFLAGS  ?= -trimpath
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  ?= -s -w -X main.version=$(VERSION)

.PHONY: help dev dev-web dev-api build build-web test smoke typecheck lint fmt clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-12s %s\n", $$1, $$2}'

dev: ## Run the Go API against ./config.yaml (frontend served separately via `make dev-web`)
	go run -ldflags '-X main.version=$(VERSION)' ./cmd/$(BIN) --config config.yaml

dev-web: ## Run the Vite dev server (proxies /api to the Go API)
	cd $(WEB) && pnpm dev

build-web: ## Build the frontend into web/dist
	cd $(WEB) && pnpm build

build: build-web ## Build the single static binary
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/$(BIN) ./cmd/$(BIN)

test: ## Run Go tests
	go test -race ./...

smoke: build ## Build, then check the binary exits 1 on a missing --config path
	@./bin/$(BIN) --config missing.yaml 2>/dev/null; rc=$$?; \
	if [ "$$rc" -ne 1 ]; then echo "smoke: --config missing.yaml exited $$rc, want 1"; exit 1; fi; \
	echo 'smoke: ok'

typecheck: ## Type-check Go (vet) and Svelte/TS (svelte-check)
	go vet ./...
	cd $(WEB) && pnpm check

lint: ## gofmt check (extend with golangci-lint later)
	@test -z "$$(gofmt -l . | grep -v '^$(WEB)/')" || { gofmt -l . | grep -v '^$(WEB)/'; echo 'gofmt: files need formatting'; exit 1; }

fmt: ## Format Go sources
	gofmt -w $$(find . -name '*.go' -not -path './$(WEB)/*')

clean: ## Remove build output
	rm -rf bin $(WEB)/dist
