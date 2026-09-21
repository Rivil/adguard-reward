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

- adguardtest.New — internal/adguard/adguardtest/server.go:82

_introduced 01-config-adguard-client · 5e65f87_

### API request hardening

Every `/api/v1` response carries `Cache-Control: no-store`, every non-GET/HEAD/OPTIONS request must carry `X-Requested-With: adguard-reward` or is refused 403 before routing, and errors share one JSON envelope `{error, message}`; anonymous requests to unknown `/api/v1` paths get 401, not 404.

- API.Handler — internal/api/api.go:110
- csrf — internal/api/middleware.go:25
- noStore — internal/api/middleware.go:16
- writeError — internal/api/errors.go:28

_introduced 02-auth-sessions · fbbf1fc_

### App startup

`--config` (default `config.yaml` beside the binary, env-only when absent) → config load → slog at the configured level → AdGuard client → startup probe (rejected credential is fatal, unreachable AdGuard is logged and served through) → SQLite store under `data_dir` → login limiter + session manager (cookie `Secure` when serving TLS or `base_url` is https) → `GET /healthz` open, `/api/v1` mounted behind the hardening chain → expired-session sweeper → HTTP or TLS serve with graceful shutdown; `version` is bound via `-X main.version`.

- run — cmd/adguard-reward/main.go:48

_introduced 01-config-adguard-client · 3951cf9 · extended 02-auth-sessions · e58f0e9_

### Children

`/api/v1/children` CRUD over `{id, name, clients[]}` rows in SQLite (`children` + `child_clients`, migration 0002): names unique case-insensitively, an AdGuard client name owned by at most one child, both enforced in one transaction that answers 409 `conflict` naming the owning child (and client); clients are trimmed / de-duplicated / sorted, listing is a single LEFT JOIN query, and nothing is ever auto-pruned when AdGuard forgets a client.

- Store.ListChildren — internal/store/children.go:90
- Store.CreateChild — internal/store/children.go:134
- Store.UpdateChild — internal/store/children.go:165
- API.handleChildCreate — internal/api/children.go:136
- API.handleChildUpdate — internal/api/children.go:166

_introduced 03-children-and-blocked-view · 422a0f9 · 0333f40_

### Children settings page

`/children` route where a parent creates, renames and deletes children and assigns AdGuard clients by checkbox: a client owned by another child is disabled and labelled with that child's name, a client AdGuard no longer knows is struck through with a remove control, every mutation re-renders from the server's response, and the route survives a reload.

- Children.load — web/src/lib/Children.svelte:26

_introduced 03-children-and-blocked-view · ed235b1 · 275c341 · 63d8347_

### Configuration loading

YAML file plus `ADGUARD_REWARD_*` env overrides (defaults < file < env; a blank env var overrides to empty) with `password_file` / `api_key_file` indirection, validation that names the dotted key (`tls.cert` / `tls.key` both-or-neither, `trusted_proxies` as CIDRs), and a `Secret` type that redacts under fmt, JSON and slog.

- config.Load — internal/config/config.go:112
- config.Secret — internal/config/secret.go:14

_introduced 01-config-adguard-client · f7db57d · extended 02-auth-sessions · a7b630a_

### Global-list migration

When AdGuard's global blocked-services list is non-empty and a mapped client still has `use_global_blocked_services=true`, the home page shows a banner listing exactly which clients gain which service ids; `GET /api/v1/migration` is read-only, `POST /api/v1/migration` writes each client's own list and clears its flag in one locked read-modify-write per client (the global list is never touched), a mid-run failure answers 502 naming the client and keeps earlier writes. "Not now" hides the banner until the next login; nothing is persisted.

- Client.MigrateFromGlobal — internal/adguard/clients.go:131
- blocked.Offer — internal/blocked/blocked.go:125
- API.handleMigrationOffer — internal/api/migration.go:54
- API.handleMigrationApply — internal/api/migration.go:67
- MigrationBanner.visible — web/src/lib/MigrationBanner.svelte:24
- Server.SetUpdateStatus — internal/adguard/adguardtest/server.go:153

_introduced 03-children-and-blocked-view · 4fe1606 · 5fb4414 · bffe283_

### Health endpoint

Periodic `/control/status` prober classifying AdGuard as ok | unauthorized | unreachable under an RWMutex; `GET /healthz` always answers 200 with JSON `{status, adguard, adguard_version, version}` from the stored snapshot, never calling AdGuard inline.

- health.New — internal/health/health.go:53
- Prober.Handler — internal/health/health.go:100
- Prober.Run — internal/health/health.go:118

_introduced 01-config-adguard-client · 1c168a4_

### Home page

Signed-in landing page listing each child with its currently blocked services (name + icon), marking a service partial when the child's devices disagree and naming the clients that differ, badging clients that still use AdGuard's global list; the view is re-fetched on every mount and an unreachable AdGuard replaces the list with an error state rather than showing stale data. Hosts the global-list migration banner.

- Home.load — web/src/lib/Home.svelte:14

_introduced 03-children-and-blocked-view · 88699ad_

### Login and logout

`POST /api/v1/login` proxies the submitted credentials to AdGuard `/control/login` behind a double-checked rate limiter and a single in-flight slot, answering 204 + session cookie, 401 `bad_credentials` (generic body) or 502 `adguard_unavailable`, and emitting one structured `event=login` line per attempt (username, ip, outcome; never the password); `POST /logout` revokes server-side and clears the cookie; `GET /me` returns `{username, expires_at}`.

- API.handleLogin — internal/api/login.go:40
- API.logLogin — internal/api/login.go:110
- API.handleLogout — internal/api/login.go:116
- API.handleMe — internal/api/login.go:128

_introduced 02-auth-sessions · f758129_

### Login page

Svelte 5 SPA shell: a username/password form showing a distinct fixed line per error code (wrong password, AdGuard unreachable, rate-limited with the Retry-After seconds, network failure); on sign-in the shell resolves `/me` and routes to the home page, and history back/forward follows the route store.

- submit — web/src/lib/Login.svelte:11
- refresh — web/src/App.svelte:11

_introduced 02-auth-sessions · 9dedea1 · extended 03-children-and-blocked-view · 295189b_

### Login rate limiting

Failed logins are budgeted per client IP (5/min, lockout doubling 1m → 1h on each further trip, reset 1h after the lockout ends) and globally (20/min, flat); a limited attempt gets 429 with `Retry-After` and never reaches AdGuard. Client IP is the TCP peer unless the peer is in `trusted_proxies`, in which case the last untrusted `X-Forwarded-For` hop is used.

- ratelimit.Limiter — internal/ratelimit/limiter.go:62
- Limiter.Check — internal/ratelimit/limiter.go:103
- Limiter.Fail — internal/ratelimit/limiter.go:123
- api.ClientIP — internal/api/clientip.go:19

_introduced 02-auth-sessions · 6835089_

### Per-child blocked view

`GET /api/v1/clients` (persistent clients plus the child each is assigned to), `GET /api/v1/services` (the runtime catalogue) and `GET /api/v1/children/{id}/blocked` all read AdGuard live on every request; the blocked view folds each mapped client's effective set (its own list, or the global list when `use_global_blocked_services` is set) into one state per catalogue service — blocked when every present client blocks it, unblocked when none does, partial otherwise with the differing clients named — reports a client AdGuard no longer knows as `missing`, and answers 502 for any AdGuard failure.

- blocked.Compute — internal/blocked/blocked.go:59
- API.handleClients — internal/api/clients.go:54
- API.handleServices — internal/api/clients.go:84
- API.handleBlocked — internal/api/blocked.go:24

_introduced 03-children-and-blocked-view · 3a50df3 · 1ec458d_

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

### SPA API client

Single typed fetch wrapper for the Svelte SPA: adds the CSRF header and same-origin cookie, routes any 401 to `/login`, maps error codes to fixed user messages (a 409 echoes the server's message naming the child or client), exposes typed functions for the children / clients / services / blocked / migration endpoints, a history-backed route store (`login | home | children`), and a per-login `migrationDismissed` store reset on login and logout.

- request — web/src/lib/api.ts:100
- messageFor — web/src/lib/api.ts:263
- navigate — web/src/lib/api.ts:47
- migrationDismissed — web/src/lib/api.ts:131

_introduced 02-auth-sessions · 9dedea1 · extended 03-children-and-blocked-view · 295189b_
