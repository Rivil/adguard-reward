# Architecture

This document describes what the system *does*, organized by feature — one entry
per user-facing capability, never one per phase and never one per module. Read it
top-to-bottom to learn the capabilities; follow the symbol links to find the code.

Every entry follows one fixed template:

### <Feature name — a user-facing capability, not a module or a phase>

<One line: what this capability does.>

- Symbol.Name — path/to/file.ext:line
- Another.Symbol — path/to/other.ext:line

_introduced <phase-id> · extended <phase-id> · <short-sha>_

Entries are maintained automatically: dross-ship merges each phase's landmarks
into the matching feature entry (updating in place), and /dross-architecture can
regenerate the whole document from a scan of the code and git history.

<!-- entries below, alphabetical by feature -->

### AdGuard API client

Basic-auth JSON client for AdGuard Home's `/control/*` API with a small error vocabulary (`ErrBadCredentials` for 401/403 — Login also maps 400 — `ErrRateLimited` for 429, `*StatusError` otherwise), no redirect following, bounded error-body snippets and secret-free debug logging.

- adguard.New — internal/adguard/client.go:68
- Client.Login — internal/adguard/client.go:98
- Client.Status — internal/adguard/client.go:117

_introduced 01-config-adguard-client · 84c1bc9_

### AdGuard test double

In-process fake of `/control/*` for tests: enforces basic auth, answers login, serves v0.107.x fixtures for clients / blocked services / status, echoes `clients/update` into the next read, records every request, and exposes SetStatus / Hang / SetAuth / MutateClient failure knobs.

- adguardtest.New — internal/adguard/adguardtest/server.go:81

_introduced 01-config-adguard-client · 5e65f87_

### App startup

`--config` (default `config.yaml` beside the binary, env-only when absent) → config load → slog at the configured level → AdGuard client → startup probe (rejected credential is fatal, unreachable AdGuard is logged and served through) → `GET /healthz` → HTTP serve with graceful shutdown; `version` is bound via `-X main.version`.

- run — cmd/adguard-reward/main.go:39

_introduced 01-config-adguard-client · 3951cf9_

### Configuration loading

YAML file plus `ADGUARD_REWARD_*` env overrides (defaults < file < env; a blank env var overrides to empty) with `password_file` / `api_key_file` indirection, validation that names the dotted key, and a `Secret` type that redacts under fmt, JSON and slog.

- config.Load — internal/config/config.go:106
- config.Secret — internal/config/secret.go:14

_introduced 01-config-adguard-client · f7db57d_

### Health endpoint

Periodic `/control/status` prober classifying AdGuard as ok | unauthorized | unreachable under an RWMutex; `GET /healthz` always answers 200 with JSON `{status, adguard, adguard_version, version}` from the stored snapshot, never calling AdGuard inline.

- health.New — internal/health/health.go:53
- Prober.Handler — internal/health/health.go:100
- Prober.Run — internal/health/health.go:118

_introduced 01-config-adguard-client · 1c168a4_

### Per-client blocked services

Typed read of AdGuard's persistent clients and the global blocked-services list (raw objects retained), the runtime service catalogue, and a mutex-serialised fresh-read read-modify-write that rewrites only a client's `blocked_services` (sorted, de-duplicated, never null) via `POST /control/clients/update`.

- Client.Clients — internal/adguard/clients.go:38
- Client.SetBlockedServices — internal/adguard/clients.go:111
- Client.Services — internal/adguard/services.go:17

_introduced 01-config-adguard-client · 1513504_
