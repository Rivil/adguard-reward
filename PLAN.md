# adguard-reward — design & plan

A small self-hosted app for households that run AdGuard Home as their
parental-controls DNS. Parents get big buttons — *"YouTube · 1 hour"*,
*"TikTok · 30 min"* — that temporarily unblock a service for a child's
devices, and the server puts the block back when the time is up, whether or
not anyone remembers.

Status: design, 2026-09-19. Nothing built yet.

## Goals

1. **Reward buttons.** One tap grants a child a service for a fixed time. The
   revert is scheduled *server-side* and survives restarts. Parents can see
   what is currently unlocked, extend, or end early.
2. **See and change what's blocked.** Show each child's blocked services (from
   AdGuard) and toggle them permanently, without opening AdGuard's own UI.
3. **Add services AdGuard doesn't know.** AdGuard ships ~100 built-in services.
   For anything else a parent types a name ("Roblox", "that homework-help
   site") and, with a bring-your-own AI key, the app proposes the set of
   domains that service actually uses — grounded in the child's real DNS
   traffic — for the parent to review and save.
4. **Configurable buttons.** Which child, which service(s), how long, label,
   icon, order. Plus an ad-hoc form for anything without a button.
5. **Log in with AdGuard.** No user database: the app checks credentials
   against AdGuard's own login. Optional per-device PIN for quick re-entry on
   a phone.
6. **Runs anywhere AdGuard runs.** One static binary or a container. No
   assumptions about Tailscale, LAN layout, or reverse proxies. TLS is
   optional and bring-your-own. Open source (AGPL-3.0).

## Non-goals (v1)

- Replacing AdGuard's UI. Filters, upstreams, DHCP, per-client safe-search
  etc. stay in AdGuard. The app touches only `blocked_services` per client,
  a clearly-marked block of `user_rules`, and reads the query log.
- Per-parent roles. Every AdGuard user is an admin; the app inherits that.
  Anyone who can log in can do everything.
- Screen-time accounting, allowances, kid-facing UI. Maybe later.
- Multiple AdGuard instances.
- Android/iOS native apps. PWA only.

## Licence boundary

AGPL-3.0 (changed from Apache-2.0 on 2026-09-19: a hosted fork must publish
its changes). AdGuard Home is GPL-3.0, which AGPL-3.0 is compatible with, but
keep the integration **API-only** anyway — fetch the service catalogue at
runtime via `/control/blocked_services/all` rather than copying code or
assets, so the boundary stays clean and upgrades don't drag in their
internals. Dependencies: anything AGPL-compatible (Apache/MIT/BSD/GPL).

## Architecture

```
┌──────────────┐   HTTPS/HTTP    ┌───────────────────────────────┐   HTTP (basic auth)   ┌──────────────┐
│ Parent phone │ ──────────────▶ │ adguard-reward (Go, 1 binary) │ ────────────────────▶ │ AdGuard Home │
│ (Svelte PWA) │ ◀────────────── │  ├─ embedded SPA + PWA assets  │ ◀──────────────────── │ /control/*   │
└──────────────┘                 │  ├─ REST API  /api/v1/*        │                       └──────────────┘
                                 │  ├─ grant scheduler            │
                                 │  ├─ reconciler (1/min)         │   HTTPS (BYO key)     ┌──────────────┐
                                 │  └─ SQLite (state)             │ ────────────────────▶ │ AI provider  │
                                 └───────────────────────────────┘                       └──────────────┘
```

- **Backend: Go** (single static binary, `embed` for the built frontend).
  Standard library HTTP mux, `modernc.org/sqlite` (pure Go, no cgo), no
  framework. Structured logging via `log/slog`. Deploys like AdGuard itself.
- **Frontend: Svelte 5 + Vite**, TypeScript, built to static assets and
  embedded. PWA manifest + service worker (app shell only; API is always
  online). Mobile-first; the primary surface is a phone.
- **State: SQLite** file next to the binary/config. Contains: children,
  child↔client mapping, custom services, buttons, grants (active + history),
  device sessions/PINs, AI settings. Mode 0600.
- **AdGuard is the source of truth for "what is blocked right now".** The app
  never caches that as authoritative; it reads before it writes and its
  writes are idempotent so drift (a parent editing in AdGuard's UI) can't
  strand a block or an unblock.

### How a grant works

Two kinds of service, two mechanisms:

| Service kind | Blocked by | Unblocked by | Reverted by |
|---|---|---|---|
| **Built-in** (AdGuard catalogue: `youtube`, `tiktok`, …) | client's `blocked_services` list | remove id from every mapped client's list (`POST /control/clients/update`) | add id back (set-union, idempotent) |
| **Custom** (app-defined domain set) | managed `user_rules`: `\|\|domain^$client='Client Name'` per domain per client | add exception rules `@@\|\|domain^$client='Client Name'` (exceptions win; the block rules stay put) | remove the exception rules |

All app-written `user_rules` live between marker comments
(`# >>> adguard-reward managed — do not edit` / `# <<< adguard-reward`) and the
app rewrites only that region, preserving everything else the parent has in
there. `set_rules` replaces the whole list, so read-modify-write under a mutex.

Grant lifecycle:

1. Parent taps a button → `POST /api/v1/grants` `{child, services[], duration}`.
2. Server writes the grant row (`status=active`, `ends_at`), applies the
   unblock to AdGuard, returns. UI shows a countdown.
3. An in-process timer fires at `ends_at` → revert → `status=expired`.
4. **Reconciler** runs every 60 s regardless: for every active grant past
   `ends_at`, revert; for every active grant, verify the unblock is still in
   place (re-apply if a parent's AdGuard edit undid it). On startup the
   reconciler runs first, so a restart mid-grant loses nothing.
5. Manual: *extend* (push `ends_at`), *end now* (revert immediately).

DNS takes effect within seconds: AdGuard's blocked responses carry a 10 s
TTL; on revert, a device may keep a cached positive answer for the domain's
own TTL (minutes). Documented, not fought.

### Children and devices

AdGuard "persistent clients" are the device inventory (name + IDs: IP, MAC,
CIDR, ClientID). The app reads them and lets a parent group clients into a
**child** (`Kid A` = phone + tablet + laptop). Grants and toggles apply to all
of a child's clients. The app can create/rename AdGuard clients from the
auto-discovered list (`auto_clients`) so a parent never has to open AdGuard
to label a new phone.

### Auth

- Login form → app calls `POST /control/login` on AdGuard with the submitted
  credentials. 200 → app issues its own session (random 256-bit token,
  SHA-256 stored, `HttpOnly; SameSite=Strict; Secure` when served over TLS).
  Nothing else is stored about the user.
- The app's *own* calls to AdGuard use a separate service credential from the
  config (HTTP basic auth), so grants revert even when no parent is logged in.
- Optional **PIN**: after login on a device, parent sets a 4–6 digit PIN. The
  device keeps a long-lived token; unlocking with the PIN mints a short
  session. PIN stored with Argon2id; 5 failures → device token revoked, full
  login required. This is the phone UX; laptops just log in.
- Rate-limit `/api/v1/login` (per IP, and globally) so the app doesn't become
  a brute-force proxy against AdGuard.
- CSRF: `SameSite=Strict` + custom header requirement on mutating requests.

### AI service discovery (BYO key)

Provider adapters behind one interface `Suggest(ctx, req) (Suggestion, error)`:
- **Anthropic** (Messages API, default model configurable; use the latest
  Claude model at build time)
- **OpenAI-compatible** (`base_url` + key + model — covers OpenAI, Ollama,
  OpenRouter, LM Studio, etc.)

Flow for "add a service":

1. Parent enters a name and optional note ("Roblox", "the game with the
   blocky avatars"), picks the child whose traffic to use (optional).
2. Server gathers **candidates**: the last N hours of the child's query log
   (`GET /control/querylog?search=<client ip>`), deduped to registrable
   domains with hit counts. Plus the AdGuard catalogue entry if a built-in
   service with a similar name exists (then suggest using that instead).
3. Model is asked, with the candidates as context, for: the domains the
   service depends on (grouped: core / CDN-media / auth / analytics), a
   confidence per domain, and which candidates from the log it recognises as
   belonging to the service. Structured JSON output; the app does the rule
   syntax, not the model.
4. Parent sees a checklist (pre-ticked core + media), can add/remove, then
   **Test**: `GET /control/filtering/check_host` per domain, both as-is and
   after a dry-run rule build, showing what would be blocked.
5. Save → custom service. Provenance kept (model, prompt version, timestamp)
   so a later "refresh domains" is possible.

Rules are AdGuard/adblock syntax (`||domain^`), not regex, by default;
regex (`/…/`) allowed as an advanced manual entry with a syntax check. The
model is never allowed to write regex directly — domain lists are reviewable,
regexes aren't.

No key configured → the "add service" flow still works with manual domain
entry and the query-log candidate list (which is useful on its own).

### Configuration

`config.yaml` (path via `--config`, default next to the binary) with env
overrides for container use:

```yaml
listen: ":8080"            # or "127.0.0.1:8080" behind a proxy
base_url: ""               # public URL if behind a proxy (for PWA scope/cookies)
tls:
  cert: ""                 # optional; both empty = plain HTTP
  key: ""
adguard:
  url: "http://127.0.0.1:80"
  username: "..."
  password: "..."          # or password_file
data_dir: "./data"         # sqlite + settings
ai:                        # optional; also settable in the UI (stored in sqlite)
  provider: "anthropic"    # anthropic | openai
  api_key: ""              # or api_key_file
  base_url: ""             # openai-compatible only
  model: ""
log_level: "info"
```

Buttons, children and custom services are UI-managed and live in SQLite;
they are exportable/importable as JSON for backup.

### Deployment targets

- **Binary + systemd** (the nora case): `adguard-reward --config /etc/adguard-reward/config.yaml`,
  `DynamicUser=yes`, `StateDirectory=adguard-reward`.
- **Docker**: `ghcr.io/rivil/adguard-reward`, distroless, config via env/volume.
- **TLS options documented, not implemented in-app beyond cert+key**:
  reverse proxy (Caddy/Traefik/nginx), `tailscale serve`, or plain HTTP on a
  trusted LAN. The UI detects a non-secure context and explains why "install
  app" is unavailable there.

## Repo layout

```
cmd/adguard-reward/       main.go
internal/adguard/         API client (typed), rule-region manager, catalogue
internal/grants/          scheduler, reconciler
internal/ai/              provider interface, anthropic, openai
internal/auth/            sessions, PIN, rate limit
internal/store/           sqlite, migrations
internal/httpapi/         handlers, middleware
web/                      Svelte 5 + Vite app → web/dist embedded
deploy/                   systemd unit, Dockerfile, example config
docs/
```

## Milestones

Each is shippable; nora runs it from M1.

**M1 — Reward loop.** Config, AdGuard client, login via AdGuard, session.
Read clients + blocked services. Children (mapping) via a minimal settings
page. Grants for *built-in* services with server-side revert + reconciler.
Buttons defined in a JSON settings blob (edited as a form, not a full editor
yet). Active-grant panel with countdown, extend, end. PWA shell. systemd unit
and deploy on nora.

**M2 — Manage.** Full button editor. Permanent block/unblock toggles per
child. Custom services with manual domain entry, managed `user_rules`
region, exception-rule grants. Grant history. Auto-client labelling. PIN
unlock. JSON export/import.

**M3 — AI discovery.** Provider adapters + settings UI. Query-log candidate
gathering. Suggest → review → test → save flow. "Refresh domains" on an
existing custom service.

**M4 — Open-source release.** Dockerfile + GHCR publish, goreleaser binaries
(linux/amd64, linux/arm64, darwin, windows), README with screenshots, TLS/
proxy guides, CI (lint, test, build; supply-chain hardened per the usual
checklist). Issue templates.

## Open questions

1. ~~Global vs per-client blocked services.~~ **Decided 2026-09-19:** on
   first run, if the global `blocked_services` list is non-empty and clients
   have `use_global_blocked_services: true`, offer a one-time migration that
   copies the global list into each client's own list and flips the flag,
   after an explicit confirmation showing exactly what will change. Global
   list is left as-is.
2. **What if a child has no persistent client?** Grants need a client to
   target. The app can create one from `auto_clients`, but if a device isn't
   in AdGuard at all, the app can't help. UI should say so plainly.
3. **PIN vs biometrics.** WebAuthn/passkeys would be nicer than a PIN and
   are viable in a PWA over HTTPS. Not over plain HTTP. Start with PIN, add
   passkeys as an M2/M3 option?
4. **Concurrent grants on the same service.** Two parents, two taps: extend
   the existing grant rather than create a second. Decided: extend.
5. **Name of the child-facing block page.** Out of scope, but AdGuard's
   `blocking_mode` could point custom-service blocks at a friendly page.
   Note only.

## Decisions log

- 2026-09-19: Go + Svelte 5, single binary. Login via AdGuard credentials, no
  user table. Both LAN-HTTP and HTTPS supported; TLS bring-your-own. Repo
  `github.com/Rivil/adguard-reward`, AGPL-3.0. Custom services use
  `$client=` user rules with a managed region; grants on custom services use
  exception rules rather than removing block rules. Model outputs domain
  lists, never regex. First-run offers global→per-client blocked-services
  migration with confirmation.
