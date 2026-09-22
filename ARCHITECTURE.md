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

### Active grants panel

Home's first block: every active grant as a row (child, services, countdown recomputed each second from the server's `ends_at` on one shared clock) with Extend (by the grant's own span) and End controls, per-row busy/alert state; expiry asks the server once and the row leaves on the answer. The list is a polled store — one shared in-flight refresh, 15 s interval plus a `visibilitychange` catch-up — and a failed poll keeps the last list ticking under a can't-reach-server badge (a 401 still routes to login).

- refresh — web/src/lib/activeGrants.ts:24
- ActiveGrants.svelte — web/src/lib/ActiveGrants.svelte:1
- run — web/src/lib/ActiveGrants.svelte:45

_introduced 05-phone-ui-buttons-pwa · e2b9115 · 2dab1f8_

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

- API.Handler — internal/api/api.go:137
- csrf — internal/api/middleware.go:25
- noStore — internal/api/middleware.go:16
- writeError — internal/api/errors.go:29

_introduced 02-auth-sessions · fbbf1fc_

### App startup

`--config` (default `config.yaml` beside the binary, env-only when absent) → config load → slog at the configured level → AdGuard client → startup probe (rejected credential is fatal, unreachable AdGuard is logged and served through) → SQLite store under `data_dir` → grant engine, whose startup reconcile pass runs under a 30 s bound before `net.Listen` (error logged, never fatal) → login limiter + session manager (cookie `Secure` when serving TLS or `base_url` is https) → `GET /healthz` open, `/api/v1` mounted behind the hardening chain with `Grants` and `Buttons` in `api.Deps`, the embedded SPA at `/` → expired-session sweeper and the 60 s grant reconciler loop (`reconcileInterval`, test-overridable) beside the prober → HTTP or TLS serve with graceful shutdown; `version` is bound via `-X main.version`.

- run — cmd/adguard-reward/main.go:59
- reconcileInterval — cmd/adguard-reward/main.go:43

_introduced 01-config-adguard-client · 3951cf9 · extended 02-auth-sessions · e58f0e9 · extended 04-grants-scheduler-reconciler · cdc438e · extended 05-phone-ui-buttons-pwa · 2109098_

### Button presets

A parent's one-tap presets `{id, label, child_id, services[], duration}` stored in order (`buttons` + `button_services`, migration 0004) and replaced atomically as a whole list — `ReplaceButtons` answers a typed `ErrUnknownChild`, `ListButtons` reads one snapshot. The `/buttons` settings page lists them in array order with Edit/Delete and one label + grant-form editor; every save PUTs the whole list (minutes → seconds, ids stripped) and re-renders from the response, a failure keeps the draft. No reorder controls and no custom icon (M2 button editor).

- Store.ListButtons — internal/store/buttons.go:48
- Store.ReplaceButtons — internal/store/buttons.go:65
- Buttons.svelte — web/src/lib/Buttons.svelte:1
- Buttons.run — web/src/lib/Buttons.svelte:52

_introduced 05-phone-ui-buttons-pwa · 2b8fa07 · efa1c46_

### Buttons API

`GET /api/v1/buttons` returns the stored list in order; `PUT /api/v1/buttons` replaces it whole, validating cheapest-first (label 1–64 runes, duration 1 min – 24 h, service ids against one catalogue read, then child existence) and answering 422 naming `buttons[i].<field>` with the stored list untouched. Behind the session gate; `ButtonStore` is wired through `api.Deps`.

- API.handleButtonsList — internal/api/buttons.go:60
- API.handleButtonsReplace — internal/api/buttons.go:82
- indexOfChild — internal/api/buttons.go:180

_introduced 05-phone-ui-buttons-pwa · 3ae3217_

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

### Grant form

Shared child / services / minutes fieldset used by the ad-hoc unlock on Home and the button editor: bindable `value` + `valid` (a child is required, not just services; minutes mirror the server's 1 min – 24 h bounds), services listed in tick order, a single child is preselected, rows for ids the catalogue no longer knows are kept and marked, and a null catalogue shows a note instead of an empty picker.

- GrantForm.svelte — web/src/lib/GrantForm.svelte:16

_introduced 05-phone-ui-buttons-pwa · 60f0d6c_

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
- run — cmd/adguard-reward/main.go:59

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

Signed-in landing page: the active-grant panel first, then each child's buttons in stored order (first service's AdGuard icon) — one tap creates the grant with no confirm, an inline line names any client the apply failed on, and a 409 becomes an extend-offer dialog that extends the overlapping grant by the tap's duration and creates the rest; an ad-hoc unlock form (grant form) runs through the same tap path when no button fits. Each child's currently blocked services (partial when its devices disagree, clients that differ named, global-list badge) sit under a lazily loaded disclosure that is open only while no buttons exist; an unreachable AdGuard replaces that list with an error state. Hosts the global-list migration banner.

- Home.load — web/src/lib/Home.svelte:55
- Home.migrated — web/src/lib/Home.svelte:191

_introduced 03-children-and-blocked-view · 88699ad · extended 05-phone-ui-buttons-pwa · f323709_

### Login and logout

`POST /api/v1/login` proxies the submitted credentials to AdGuard `/control/login` behind a double-checked rate limiter and a single in-flight slot, answering 204 + session cookie, 401 `bad_credentials` (generic body) or 502 `adguard_unavailable`, and emitting one structured `event=login` line per attempt (username, ip, outcome; never the password); `POST /logout` revokes server-side and clears the cookie; `GET /me` returns `{username, expires_at}`.

- API.handleLogin — internal/api/login.go:40
- API.logLogin — internal/api/login.go:110
- API.handleLogout — internal/api/login.go:116
- API.handleMe — internal/api/login.go:128

_introduced 02-auth-sessions · f758129_

### Login page

Svelte 5 SPA shell: a username/password form showing a distinct fixed line per error code (wrong password, AdGuard unreachable, rate-limited with the Retry-After seconds, network failure), the username input opting out of phone auto-capitalise / autocorrect and the form locked while a sign-in is in flight; on sign-in the shell resolves `/me` and routes to the home page, and history back/forward follows the route store.

- submit — web/src/lib/Login.svelte:11
- refresh — web/src/App.svelte:12

_introduced 02-auth-sessions · 9dedea1 · extended 03-children-and-blocked-view · 295189b · extended 05-phone-ui-buttons-pwa · 4caa9aa_

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

### PWA shell

The SPA installs to a phone's home screen: `manifest.webmanifest` (standalone display, root scope and `start_url`, 192/512 PNG icons including maskable) linked from `index.html` with theme-color and apple-touch-icon; a precache-only service worker built as a classic `/sw.js` with the emitted asset list injected at build serves the shell network-first with the cached `/` as offline fallback and `/assets` cache-first, while `/api/*`, `/healthz` and every non-GET always bypass the worker. Registered in production builds only.

- manifest.webmanifest — web/public/manifest.webmanifest:1
- decide — web/src/lib/sw-routing.ts:24

_introduced 05-phone-ui-buttons-pwa · 7d1dc40 · cf5d7b7_

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

Single typed fetch wrapper for the Svelte SPA: adds the CSRF header and same-origin cookie, routes any 401 to `/login`, maps error codes to fixed user messages (a 409 echoes the server's message naming the child or client, and carries the overlapping `grant_id`), exposes typed functions for the children / clients / services / blocked / migration / grants / buttons endpoints, a history-backed route store (`login | home | children | buttons`), and a per-login `migrationDismissed` store reset on login and logout.

- request — web/src/lib/api.ts:106
- messageFor — web/src/lib/api.ts:333
- navigate — web/src/lib/api.ts:50
- migrationDismissed — web/src/lib/api.ts:137

_introduced 02-auth-sessions · 9dedea1 · extended 03-children-and-blocked-view · 295189b · extended 05-phone-ui-buttons-pwa · 2322e19_

### SPA serving

The built frontend is embedded in the Go binary (`go:embed all:dist` behind a tracked `.gitkeep`, so an unbuilt clone still compiles) and mounted at `/` beside `/healthz` and `/api/v1` — one origin, one process. The handler serves static files, falls back to `index.html` for extensionless client routes, answers a plain 404 for an extensioned miss and a JSON 404 under `/api` and `/healthz`, marks shell / `sw.js` / manifest no-cache and `/assets` immutable, and returns 503 while `dist` is unbuilt.

- Handler — internal/spa/spa.go:85
- Dist — web/embed.go:17
- run — cmd/adguard-reward/main.go:59

_introduced 05-phone-ui-buttons-pwa · 1b8647f · 4de11cf · 2109098_

### Timed grants

A grant temporarily unblocks one or more catalogue services for one child: the engine owns every grant-related AdGuard write under one mutex with one `ApplyTimeout` context per operation. `Create` removes the services from each of the child's mapped clients and stores exactly that client list on the row (a client moved to another child mid-grant is still re-blocked at expiry); an in-process timer at `ends_at` re-blocks by set-union — idempotent — and CASes the grant to `expired`, while a failed AdGuard write leaves it `active` for the reconciler to retry; `Extend` reschedules the timer, `End` reverts immediately. On the phone, `runTap` is the one path from a tap to a grant: a single in-flight POST per key, the 201 merged into the active list as server truth, a partial apply naming the failed clients, and a 409 turned into an extend offer resolved client-side against the loaded list (`overlapsFor`) — accepting it extends the overlapping grant by the tap's duration and creates a new grant for the remaining services. Pure countdown / duration / own-span helpers mirror the server bounds.

- grants.New — internal/grants/grants.go:104
- Engine.Create — internal/grants/grants.go:141
- Engine.Extend — internal/grants/grants.go:260
- Engine.End — internal/grants/grants.go:282
- runTap — web/src/lib/tap.ts:93
- acceptOffer — web/src/lib/tap.ts:114
- overlapsFor — web/src/lib/grants.ts:54

_introduced 04-grants-scheduler-reconciler · 6064863 · extended 05-phone-ui-buttons-pwa · 2322e19 · 257ccc3_
