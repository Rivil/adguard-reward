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

### API request hardening

Every `/api/v1` response carries `Cache-Control: no-store`, every non-GET/HEAD/OPTIONS request must carry `X-Requested-With: adguard-reward` or is refused 403 before routing, and errors share one JSON envelope `{error, message}`; anonymous requests to unknown `/api/v1` paths get 401, not 404.

- API.Handler — internal/api/api.go:83
- csrf — internal/api/middleware.go:25
- noStore — internal/api/middleware.go:16
- writeError — internal/api/errors.go:27

_introduced 02-auth-sessions · fbbf1fc_

### App startup

`--config` (default `config.yaml` beside the binary, env-only when absent) → config load → slog at the configured level → AdGuard client → startup probe (rejected credential is fatal, unreachable AdGuard is logged and served through) → SQLite store under `data_dir` → login limiter + session manager (cookie `Secure` when serving TLS or `base_url` is https) → `GET /healthz` open, `/api/v1` mounted behind the hardening chain → expired-session sweeper → HTTP or TLS serve with graceful shutdown; `version` is bound via `-X main.version`.

- run — cmd/adguard-reward/main.go:48

_introduced 01-config-adguard-client · 3951cf9 · extended 02-auth-sessions · e58f0e9_

### Configuration loading

YAML file plus `ADGUARD_REWARD_*` env overrides (defaults < file < env; a blank env var overrides to empty) with `password_file` / `api_key_file` indirection, validation that names the dotted key (`tls.cert` / `tls.key` both-or-neither, `trusted_proxies` as CIDRs), and a `Secret` type that redacts under fmt, JSON and slog.

- config.Load — internal/config/config.go:112
- config.Secret — internal/config/secret.go:14

_introduced 01-config-adguard-client · f7db57d · extended 02-auth-sessions · a7b630a_

### Health endpoint

Periodic `/control/status` prober classifying AdGuard as ok | unauthorized | unreachable under an RWMutex; `GET /healthz` always answers 200 with JSON `{status, adguard, adguard_version, version}` from the stored snapshot, never calling AdGuard inline.

- health.New — internal/health/health.go:53
- Prober.Handler — internal/health/health.go:100
- Prober.Run — internal/health/health.go:118

_introduced 01-config-adguard-client · 1c168a4_

### Login and logout

`POST /api/v1/login` proxies the submitted credentials to AdGuard `/control/login` behind a double-checked rate limiter and a single in-flight slot, answering 204 + session cookie, 401 `bad_credentials` (generic body) or 502 `adguard_unavailable`, and emitting one structured `event=login` line per attempt (username, ip, outcome; never the password); `POST /logout` revokes server-side and clears the cookie; `GET /me` returns `{username, expires_at}`.

- API.handleLogin — internal/api/login.go:40
- API.logLogin — internal/api/login.go:110
- API.handleLogout — internal/api/login.go:116
- API.handleMe — internal/api/login.go:128

_introduced 02-auth-sessions · f758129_

### Login page

Svelte 5 SPA shell: a username/password form showing a distinct fixed line per error code (wrong password, AdGuard unreachable, rate-limited with the Retry-After seconds, network failure), a single fetch wrapper that adds the CSRF header and same-origin cookie and routes any 401 to `/login`, and a placeholder home for a signed-in parent with logout; history back/forward follows the route store.

- request — web/src/lib/api.ts:95
- messageFor — web/src/lib/api.ts:135
- navigate — web/src/lib/api.ts:42
- submit — web/src/lib/Login.svelte:11
- refresh — web/src/App.svelte:10

_introduced 02-auth-sessions · 9dedea1_

### Login rate limiting

Failed logins are budgeted per client IP (5/min, lockout doubling 1m → 1h on each further trip, reset 1h after the lockout ends) and globally (20/min, flat); a limited attempt gets 429 with `Retry-After` and never reaches AdGuard. Client IP is the TCP peer unless the peer is in `trusted_proxies`, in which case the last untrusted `X-Forwarded-For` hop is used.

- ratelimit.Limiter — internal/ratelimit/limiter.go:62
- Limiter.Check — internal/ratelimit/limiter.go:103
- Limiter.Fail — internal/ratelimit/limiter.go:123
- api.ClientIP — internal/api/clientip.go:19

_introduced 02-auth-sessions · 6835089_

### Per-client blocked services

Typed read of AdGuard's persistent clients and the global blocked-services list (raw objects retained), the runtime service catalogue, and a mutex-serialised fresh-read read-modify-write that rewrites only a client's `blocked_services` (sorted, de-duplicated, never null) via `POST /control/clients/update`.

- Client.Clients — internal/adguard/clients.go:38
- Client.SetBlockedServices — internal/adguard/clients.go:111
- Client.Services — internal/adguard/services.go:17

_introduced 01-config-adguard-client · 1513504_

### Session authentication

256-bit random session tokens stored only as SHA-256, carried in an `HttpOnly; SameSite=Strict; Path=/` cookie (`Secure` under TLS) with a 30-day sliding expiry; `RequireSession` refreshes a live session or clears a dead one and answers 401, and the raw token never reaches a log line.

- auth.Manager — internal/auth/auth.go:88
- Manager.Issue — internal/auth/auth.go:125
- Manager.RequireSession — internal/auth/middleware.go:22
- newToken — internal/auth/token.go:21

_introduced 02-auth-sessions · c4a55d6_

### Session management API

`GET /api/v1/sessions` lists the caller's sessions `{id, created_at, last_seen_at, current}`; `DELETE /sessions/{id}` revokes one (a foreign id is indistinguishable from an unknown one); `POST /sessions/revoke-all` revokes every session but the caller's. A revoked cookie gets 401 on its next request.

- API.handleSessionsList — internal/api/sessions.go:21
- API.handleSessionDelete — internal/api/sessions.go:46
- API.handleSessionsRevokeAll — internal/api/sessions.go:71

_introduced 02-auth-sessions · 99d5eac_

### Session persistence

Sessions live in a single-connection WAL SQLite file under `data_dir` (0700 dir / 0600 file) with embedded, ordered, per-transaction migrations, so a restart never logs a phone out; the store implements `auth.SessionStore` (insert, lookup by hash, touch, delete, list, revoke-one, revoke-others, sweep expired).

- store.Open — internal/store/store.go:38
- migrate — internal/store/migrate.go:62
- Store.ByTokenHash — internal/store/sessions.go:54
- Store.DeleteOthers — internal/store/sessions.go:122
- Store.DeleteExpired — internal/store/sessions.go:136

_introduced 02-auth-sessions · 0a98a31_
