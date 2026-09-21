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

In-process fake of `/control/*` for tests: enforces basic auth, answers login, serves v0.107.x fixtures for clients / blocked services / status, echoes `clients/update` into the next read, records every request, and exposes SetStatus / Hang / SetAuth / MutateClient / SetUpdateStatus failure knobs plus BlockedServices / RemoveClient / CountRequests observers for grant tests.

- adguardtest.New — internal/adguard/adguardtest/server.go:82
- Server.BlockedServices — internal/adguard/adguardtest/server.go:189
- Server.RemoveClient — internal/adguard/adguardtest/server.go:211
- Server.CountRequests — internal/adguard/adguardtest/server.go:224

_introduced 01-config-adguard-client · 5e65f87 · extended 04-grants-scheduler-reconciler · 3123562_

### API request hardening

Every `/api/v1` response carries `Cache-Control: no-store`, every non-GET/HEAD/OPTIONS request must carry `X-Requested-With: adguard-reward` or is refused 403 before routing, and errors share one JSON envelope `{error, message}`; anonymous requests to unknown `/api/v1` paths get 401, not 404.

- API.Handler — internal/api/api.go:127
- csrf — internal/api/middleware.go:25
- noStore — internal/api/middleware.go:16
- writeError — internal/api/errors.go:29

_introduced 02-auth-sessions · fbbf1fc_

### App startup

`--config` (default `config.yaml` beside the binary, env-only when absent) → config load → slog at the configured level → AdGuard client → startup probe (rejected credential is fatal, unreachable AdGuard is logged and served through) → SQLite store under `data_dir` → grant engine, whose startup reconcile pass runs under a 30 s bound before `net.Listen` (error logged, never fatal) → login limiter + session manager (cookie `Secure` when serving TLS or `base_url` is https) → `GET /healthz` open, `/api/v1` mounted behind the hardening chain with `Grants` in `api.Deps` → expired-session sweeper and the 60 s grant reconciler loop (`reconcileInterval`, test-overridable) beside the prober → HTTP or TLS serve with graceful shutdown; `version` is bound via `-X main.version`.

- run — cmd/adguard-reward/main.go:57
- reconcileInterval — cmd/adguard-reward/main.go:41

_introduced 01-config-adguard-client · 3951cf9 · extended 02-auth-sessions · e58f0e9 · extended 04-grants-scheduler-reconciler · cdc438e_

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

- Client.MigrateFromGlobal — internal/adguard/clients.go:128
- blocked.Offer — internal/blocked/blocked.go:125
- API.handleMigrationOffer — internal/api/migration.go:54
- API.handleMigrationApply — internal/api/migration.go:67
- MigrationBanner.visible — web/src/lib/MigrationBanner.svelte:24
- Server.SetUpdateStatus — internal/adguard/adguardtest/server.go:153

_introduced 03-children-and-blocked-view · 4fe1606 · 5fb4414 · bffe283_

### Grant persistence

`grants` + `grant_services` + `grant_clients` rows (migration 0003) with a partial index over live grants: `CreateGrant` checks (child, service) overlap and inserts in one transaction, answering `*ErrGrantOverlap` with the existing grant id; `SetGrantStatus` is a compare-and-swap and the only exit from `active`, so a timer and an explicit end cannot both revert the same grant.

- Store.CreateGrant — internal/store/grants.go:49
- Store.ListActiveGrants — internal/store/grants.go:99
- Store.ExtendGrant — internal/store/grants.go:126
- Store.SetGrantStatus — internal/store/grants.go:154

_introduced 04-grants-scheduler-reconciler · 13dc7bb_

### Grant reconciliation

One pass under the engine mutex reads AdGuard once, reverts every grant past `ends_at` the timer missed, removes any granted service id that has reappeared in a covered client's `blocked_services` (a parent re-blocked it in AdGuard's UI), and re-arms missing expiry timers; a pass with nothing to do issues no writes. `Start` runs the pass before the HTTP listener accepts, `Run` loops it on an injectable interval (60 s in production).

- Engine.Reconcile — internal/grants/reconcile.go:32
- Engine.Start — internal/grants/reconcile.go:112
- Engine.Run — internal/grants/reconcile.go:123
- run — cmd/adguard-reward/main.go:57

_introduced 04-grants-scheduler-reconciler · 29444c6 · cdc438e_

### Grants API

`GET /api/v1/grants` lists active grants as `{id, child_id, services[], clients[], started_at, ends_at}`; `POST /api/v1/grants {child_id, services[], duration}` validates duration (1 min – 24 h) and service ids to 422 before the 404 child lookup before any AdGuard call, answers 409 `{grant_id}` on overlap and 201 `{id, ends_at, applied, failed[]}` on success (partial apply is reported, not rolled back); `POST /grants/{id}/extend` and `/grants/{id}/end` return 404 for unknown or non-active grants and `end` answers 502 naming the clients whose revert failed. All behind the session gate.

- API.handleGrantsList — internal/api/grants.go:125
- API.handleGrantCreate — internal/api/grants.go:144
- API.handleGrantExtend — internal/api/grants.go:223
- API.handleGrantEnd — internal/api/grants.go:250

_introduced 04-grants-scheduler-reconciler · 21a1233_

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

Typed read of AdGuard's persistent clients and the global blocked-services list (raw objects retained), the runtime service catalogue, and a mutex-serialised fresh-read read-modify-write that rewrites only a client's `blocked_services` (sorted, de-duplicated, never null) via `POST /control/clients/update`; `AddBlockedServices` / `RemoveBlockedServices` edit the live list as set-union / set-difference under the same mutex and skip the POST when nothing changes.

- Client.Clients — internal/adguard/clients.go:38
- Client.SetBlockedServices — internal/adguard/clients.go:111
- Client.AddBlockedServices — internal/adguard/clients.go:158
- Client.RemoveBlockedServices — internal/adguard/clients.go:149
- editBlockedServices — internal/adguard/clients.go:166
- Client.Services — internal/adguard/services.go:17

_introduced 01-config-adguard-client · 1513504 · extended 04-grants-scheduler-reconciler · 3123562_

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

### Timed grants

A grant temporarily unblocks one or more catalogue services for one child: the engine owns every grant-related AdGuard write under one mutex with one `ApplyTimeout` context per operation. `Create` removes the services from each of the child's mapped clients and stores exactly that client list on the row (a client moved to another child mid-grant is still re-blocked at expiry); an in-process timer at `ends_at` re-blocks by set-union — idempotent — and CASes the grant to `expired`, while a failed AdGuard write leaves it `active` for the reconciler to retry; `Extend` reschedules the timer, `End` reverts immediately.

- grants.New — internal/grants/grants.go:104
- Engine.Create — internal/grants/grants.go:141
- Engine.Extend — internal/grants/grants.go:260
- Engine.End — internal/grants/grants.go:282

_introduced 04-grants-scheduler-reconciler · 6064863_
