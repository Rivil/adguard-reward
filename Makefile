# adguard-reward — developer entry points. `make help` lists targets.
BIN      := adguard-reward
MODULE   := github.com/Rivil/adguard-reward
WEB      := web
GOFLAGS  ?= -trimpath
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# Pin it: `?=` leaves VERSION recursive, so every reference would re-run git
# describe and a deploy could stamp the binary and gate its /healthz on two
# different strings. A command-line VERSION= still overrides both lines.
VERSION  := $(VERSION)
LDFLAGS  ?= -s -w -X main.version=$(VERSION)

# The linux/amd64 artefact `make deploy-nora` ships. Overridable so the build
# test can drop it in a temp dir instead of bin/.
LINUX_BIN ?= bin/$(BIN)-linux-amd64

# ssh host alias for the deploy target.
NORA_HOST ?= nora

.PHONY: help dev dev-web dev-api build build-web build-linux build-linux-bin deploy-nora test test-go test-web smoke typecheck lint fmt clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-12s %s\n", $$1, $$2}'

dev: ## Run the Go API against ./config.yaml (frontend served separately via `make dev-web`)
	go run -ldflags '-X main.version=$(VERSION)' ./cmd/$(BIN) --config config.yaml

dev-web: ## Run the Vite dev server (proxies /api to the Go API)
	cd $(WEB) && pnpm dev

# vite empties outDir, so restore the placeholder that keeps `//go:embed all:dist`
# compiling on an unbuilt tree.
build-web: ## Build the frontend into web/dist
	cd $(WEB) && pnpm build && touch dist/.gitkeep

build: build-web ## Build the single static binary
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/$(BIN) ./cmd/$(BIN)

build-linux: build-web build-linux-bin ## Build the linux/amd64 binary for nora (frontend included)

# Split out so the build test can compile a binary without build-web: pnpm
# empties web/dist first, and under `go test ./...` another package may be
# compiling its embed of that directory at the same moment.
build-linux-bin: ## Cross-compile the linux/amd64 binary only (no frontend rebuild)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(LINUX_BIN) ./cmd/$(BIN)

# The install script runs as root on the host and reads VERSION from the
# environment; it gates the deploy on nora's /healthz reporting exactly the
# version stamped into the binary this chain just built.
deploy-nora: build-linux ## Build and deploy to nora over ssh, gated on /healthz
	@test "$$(ssh $(NORA_HOST) uname -m)" = x86_64 || \
		{ echo "deploy-nora: $(NORA_HOST) is not x86_64; $(LINUX_BIN) will not run there"; exit 1; }
	scp $(LINUX_BIN) $(NORA_HOST):/tmp/adguard-reward.new
	scp deploy/adguard-reward.service deploy/config.nora.yaml $(NORA_HOST):/tmp/
	ssh $(NORA_HOST) sudo -n env VERSION=$(VERSION) bash -s < deploy/install-nora.sh

test: test-go test-web ## Run the Go and web test suites (needs web/node_modules)

# ADGUARD_REWARD_BUILD_TEST opts the cross-build test in. It shells out to
# make and compiles a second binary, so a bare `go test ./...` skips it.
test-go: build-web ## Run Go tests (builds the frontend first: the binary tests serve web/dist)
	ADGUARD_REWARD_BUILD_TEST=1 go test -race ./...

test-web: ## Run web unit tests (vitest)
	cd $(WEB) && pnpm test

smoke: build ## Build, then check the binary exits 1 on a missing --config path
	@./bin/$(BIN) --config missing.yaml 2>/dev/null; rc=$$?; \
	if [ "$$rc" -ne 1 ]; then echo "smoke: --config missing.yaml exited $$rc, want 1"; exit 1; fi; \
	echo 'smoke: ok'

typecheck: ## Type-check Go (vet) and Svelte/TS (svelte-check)
	go vet ./...
	cd $(WEB) && pnpm check

# Go sources everywhere except the frontend's dependency and mutation sandboxes,
# so web/embed.go is covered like any other file.
GO_SOURCES = $$(find . -name '*.go' -not -path './$(WEB)/node_modules/*' -not -path './$(WEB)/.stryker-tmp/*')

lint: ## gofmt check (extend with golangci-lint later)
	@test -z "$$(gofmt -l $(GO_SOURCES))" || { gofmt -l $(GO_SOURCES); echo 'gofmt: files need formatting'; exit 1; }

fmt: ## Format Go sources
	gofmt -w $(GO_SOURCES)

clean: ## Remove build output (keeps web/dist/.gitkeep so the embed still compiles)
	rm -rf bin $(WEB)/dist && mkdir -p $(WEB)/dist && touch $(WEB)/dist/.gitkeep
