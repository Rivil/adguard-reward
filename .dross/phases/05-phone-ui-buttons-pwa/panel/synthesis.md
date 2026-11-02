# Synthesis — 05-phone-ui-buttons-pwa

Judge read the spec, project/rules, all three drafts, and the existing source
(`cmd/adguard-reward/main{,_test}.go`, `internal/api/{api,grants}.go`,
`internal/api/login_test.go`, `internal/store/{store,children}.go` + migrations,
`Makefile`, `deploy/Dockerfile`, `.gitignore`, `web/.gitignore`, `web/stryker.config.mjs`,
`web/vite.config.ts`, `web/index.html`, `web/src/{main.ts,App.svelte}`,
`web/src/lib/{api.ts,Home.svelte,Login.svelte}`, `web/reports/mutation/mutation.json`,
`.dross/survivors.toml`, phase 02/03 `spec.toml` + `tests.json`). Facts that shaped the
scoring — each one is a point on which at least one draft was wrong:

- `web/dist/` is ignored by the root `.gitignore` **and** `dist` by `web/.gitignore`; a
  committed `web/dist/.gitkeep` needs the `!` exception in both files (verification lists
  only the root file). `web/dist` currently holds a local build (`index.html`, `assets/`,
  `favicon.svg`, `icons.svg`). There is no root `Dockerfile`; `deploy/Dockerfile` runs
  `pnpm build` then `COPY --from=web /src/web/dist ./web/dist` before `go build`.
- `decodeGrantBody` caps at `maxGrantBody = 16 << 10` (risk is right; mvp and
  verification say 64 KiB).
- `main.go`: `mux.HandleFunc("GET /healthz", …)` and `mux.Handle("/api/v1/", apiHandler)`
  on Go 1.26's mux. `api.Deps.Children` is `ChildStore{ListChildren, GetChild,
  CreateChild, UpdateChild, DeleteChild(ctx, id) (bool, error)}`; `checkDuration(w, secs)`
  and exported `grants.MinDuration/MaxDuration` exist. Grant wire: list items
  `{id, child_id, services, clients, started_at, ends_at}`, 201 `{id, ends_at, applied,
  failed}`, 409 `{error:"conflict", …, grant_id}`, extend 200 `{id, ends_at}`.
- Store: `SetMaxOpenConns(1)`, `foreign_keys(ON)`, migrations `0001..0003`.
- Makefile: `test-go` = `go test -race ./...` with no `build-web` dependency; `lint`/`fmt`
  exclude all of `./web/` (so a `web/embed.go` would never be gofmt-checked).
- `web/public` holds `favicon.svg` and `icons.svg` only; `index.html`'s title is `web`;
  `main.ts` and `vite.config.ts` are `Stryker disable all`.
- `stryker.config.mjs`: `ignoreStatic: true`, `mutate: src/**/*.{ts,svelte}` (a file under
  `web/public/` is outside the glob). **`dross verify` mutates only files in the phase
  diff**: phase 03's `mutation.json` has no `Login.svelte` entry. mvp's "Login.svelte must
  be in the diff or its four keys cannot be re-scored" is correct; risk and verification
  both treat Login as test-only and would leave those keys unscored.
- The spec's 14 keys are the phase 02/03 `[[deferred]]` entries. Live in the phase-03
  report today: `api.ts:49` ConditionalExpression→`true` (`d2e9f9c6`) and `&&`→`||`
  (`47163e3c`); `Home.svelte:8/30/36` (`c68261e3`, `a094a307`, `beec743e`); `App.svelte:17`
  catch body (`d248d03c`); `Login.svelte:6/7/13/44` (`56313b4a`, `95210673`, `f868399f`,
  `77ea4718`, unscored since phase 02). Dead or unreachable by key: `321f5af2` (api.ts:31,
  the phase-02 `routeFor` ternary, rewritten in 03); `7c2c1aaf` (api.ts:34 BlockStatement =
  `currentPath` body — it runs at module load through `route = writable(routeFor(
  currentPath()))`, so `ignoreStatic` emits no mutant for :39-41 today); `04a39c82`
  (api.ts:43, navigate's path literal, now the `PATHS` object at :30 — status `Ignored`);
  `fd433887` (App.svelte:21 `navigate('login')` literal — `Killed` in the phase-03 report).
  Verification's map is the accurate one. mvp maps api.ts:31/34 to the `routeFor`
  `r !== 'home'` mutants and App:21 to `$route === 'children' && username !== null` — those
  are keys `4e3c141d`/`2e5c825a`/`8ef83e30`, already **accepted** in phase 03, not routed.
  Risk's map also mislabels (":31 → PATHS.login", ":43 → :30") but its kills hit the right
  behaviour. `.dross/survivors.toml` records category `stryker-whole-file-attribution`:
  kills verified by hand are sometimes not credited by Stryker's per-test attribution —
  risk's F33 exit gate addresses a documented problem.

## Scores

Scale 1–5 per dimension.

| draft | criteria coverage | test-contract specificity | granularity | wave correctness |
|---|---|---|---|---|
| risk | 4 — all 8 owned, plus the failure modes the spec implies (double tap, stale 409, poll overlap, backgrounded tab, catalogue down, draft wiped by 422); but c-7's four Login keys cannot be re-scored because Login.svelte is not in its diff | 5 — the sharpest overall: `-race` replace race, fake-timer poll counts, "entry visible before any GET resolves" (merge proves itself), 404-on-extend retry, `[data-error="partial"]`; two soft spots: a PNG-encoder determinism test and a hand-rolled survivor map | 5 — 13 tasks, every one ≤ 6 files and one concern; store/HTTP/client/panel/page are separate commits | 4 — 4 parallel tasks in wave 1, minimal deps; the extra wave-5 task is a real cost, and t-13's rationale ("kills would be undone") is wrong if the kills live in the rewriting tasks |
| mvp | 3 — all 8 ticked; the only draft that saw Login.svelte must enter the verify diff; but its survivor map is factually wrong on 3 of 14 keys, its api.ts fix is guard removal (keys vanish rather than die), panel expiry is client-clock only, and no task owns the failed-poll/visibility path | 3 — good store/API/SPA contracts (traversal, IHDR bytes, sw.js with stubbed `self`); Home contracts are broad ("appears without advancing timers beyond one tick"), label is 400 not 422, 64 KiB is not what `decodeGrantBody` does | 3 — 10 tasks; t-8 Home carries polling + tap + 409 + form + disclosure + survivors in one commit | 3 — 3 waves; t-10's binary proofs assert `/manifest.webmanifest` and `/sw.js` but do not depend on t-3 |
| verification | 4 — all 8, a wire-contract block, every locked decision pinned to a named test, the only correct survivor map (keys verified against phase artefacts, dead keys identified); misses that Login.svelte must be in the diff | 5 — `buttons[i].field` messages, byte-equal PUT/GET, `TestRun_SWPrecacheListIsServed` (every precache entry 200 from the binary), IHDR parse, 404 for a missing asset with an extension; the `resetModules` initial-route test targets static-ignored code (harmless) | 3 — 11 tasks; t-10 Home has 12 contracts, t-9 bundles Buttons page + App route + Login/App kills | 3 — 6 parallel tasks in wave 1, but t-2 and t-4 both edit `web/vite.config.ts` in that wave (`emptyOutDir` vs the SW plugin) |

**Skeleton: risk.** It has the best granularity, the only wave structure without a
file conflict, and it is the only draft whose structure owns the paths the locked
decisions actually depend on (`unreachable_countdown` needs a poller whose failure keeps
state; `overlap_offer` needs a stale-409 path; `no_confirm` needs double-tap suppression).
Verification's contracts are grafted wherever sharper (wire contract, message format,
precache-served-from-binary, 404 for missing assets, install/activate glue tests) and its
survivor map replaces risk's. mvp contributes the one thing both others missed — the
Login.svelte change that puts the file into the verify diff — and is otherwise the
dissenting voice recorded under Disagreements.

## Merged plan

Phase 05-phone-ui-buttons-pwa — 13 tasks across 5 waves.

Wire contract (verification's block, adopted with D2/D15 applied):

```
GET  /api/v1/buttons               -> 200 {"buttons":[{"id":1,"label":"YouTube 1h","child_id":1,"services":["youtube"],"duration":3600}]}
PUT  /api/v1/buttons {"buttons":[{"label","child_id","services":[…],"duration"}]}
                                   -> 200 same shape as GET (server-assigned ids, stored order)
                                    | 400 bad_request (unknown field incl. "id", non-object, buttons missing/null, > 16 KiB)
                                    | 422 unprocessable, message "buttons[i].<field>: …" naming the first bad item
                                    | 502 adguard_unavailable (catalogue unreachable; nothing stored)
duration is whole seconds, bounds grants.MinDuration/MaxDuration (60..86400), same as POST /grants.
The list is replaced wholesale and atomically; ids are reassigned on every PUT (nothing references them).
A deleted child's buttons go with it (FK ON DELETE CASCADE; foreign_keys is ON in store.Open).

Static surface (t-2 / t-11):
GET /                              -> 200 text/html index.html, Cache-Control: no-cache
GET /<unknown, no extension>       -> 200 index.html (SPA fallback), no-cache
GET /<unknown>.js|.png|…           -> 404, never HTML (a stale hashed asset must not get HTML)
GET /assets/<hashed>               -> 200, Cache-Control: public, max-age=31536000, immutable
GET /sw.js, /manifest.webmanifest, /icon-192.png, /icon-512.png -> 200 no-cache; manifest is application/manifest+json
GET /api/… or /healthz through the static handler -> 404 JSON {"error":"not_found"}, never HTML
GET /api/v1/<unknown>              -> 401 without cookie, 404 with (unchanged)
POST /                             -> 405
```

### Wave 1

**t-1 — Buttons schema and atomic replace store** `[risk; contracts also verification+mvp]`
- files: `internal/store/migrations/0004_buttons.sql`, `internal/store/buttons.go`, `internal/store/buttons_test.go`
- covers: c-1
- depends_on: []
- description: `0004_buttons.sql`: `buttons(id INTEGER PRIMARY KEY AUTOINCREMENT, position INTEGER NOT NULL UNIQUE, label TEXT NOT NULL, child_id INTEGER NOT NULL REFERENCES children(id) ON DELETE CASCADE, duration INTEGER NOT NULL CHECK (duration > 0) /* seconds */)`; `button_services(button_id INTEGER NOT NULL REFERENCES buttons(id) ON DELETE CASCADE, position INTEGER NOT NULL, service_id TEXT NOT NULL, PRIMARY KEY (button_id, service_id))` (see D1). AUTOINCREMENT so an id is never reused across PUTs; FK cascade because a button has no reason to outlive its child (unlike grants). `buttons.go`: `Button{ID, ChildID int64; Label string; Services []string (given order, deduped on first occurrence, never nil); Duration time.Duration}`; typed `*ErrUnknownChild{ChildID}`. `ListButtons(ctx)` position ASC, services by position, two queries each scanned to completion (single-connection rule), empty → `[]Button{}`. `ReplaceButtons(ctx, in []Button) ([]Button, error)` in ONE tx: `DELETE FROM buttons` (cascade clears services); per item `SELECT 1 FROM children WHERE id=?` → `*ErrUnknownChild` (rollback, old list intact); INSERT with `position = index`; returns the stored list with fresh ids. Incoming IDs are ignored (see D2).
- test_contract:
  - `[risk+verification+mvp]` TestButtons_ReplaceAtomic: list [A,B] stored; `ReplaceButtons([C, D{ChildID: 999}])` → `*ErrUnknownChild{999}` and ListButtons still returns [A,B] with their original ids; likewise a second item with `Duration 0` (CHECK) leaves the previous list intact
  - `[risk+verification+mvp]` TestButtons_OrderPersists: Replace [C,A,B] → List returns labels [C,A,B] with ids ascending; Close, Open same dir → identical list (ids, labels, services, durations); `schema_migrations` lists `0004_buttons.sql` exactly once, after `0003_grants.sql`
  - `[risk+verification]` TestButtons_ServicesOrderKept: Services `["youtube","tiktok","youtube"]` reads back `["youtube","tiktok"]`; `["tiktok","youtube"]` reads back in that order (locked `button_icon`)
  - `[risk+verification+mvp]` TestButtons_ChildCascade: buttons for ada (two) and ben; `DeleteChild(ada)` → `(true, nil)`; List holds only ben's; `SELECT count(*) FROM button_services` counts only ben's rows
  - `[verification]` TestButtons_ReplaceEmpty: fresh store → `[]Button{}` non-nil; `Replace(nil)` → `[]` and `button_services` is empty; a button with `Services []` reads back `[]string{}` never nil
  - `[risk]` TestButtons_ChildCheckInTx: a child deleted on another goroutine between two Replace calls yields `*ErrUnknownChild`, never a raw "FOREIGN KEY constraint failed"
  - `[risk]` TestButtons_FreshIDs: after Replace [A] (id 1) then Replace [B], `B.ID > 1`
  - `[risk]` TestButtons_ReplaceRace under `go test -race`: 10 goroutines each Replace a distinct 3-item list while 5 goroutines List for 200 ms → every List result equals one submitted list exactly (labels and services), never a mix, finishing within 5 s

**t-2 — Embedded SPA handler with safe fallback, caching and build plumbing** `[risk; resolve table + missing-asset 404 from verification; traversal test from mvp]`
- files: `web/embed.go`, `web/dist/.gitkeep`, `.gitignore`, `web/.gitignore`, `internal/spa/spa.go`, `internal/spa/spa_test.go`, `Makefile`
- covers: c-5, c-6 (headers, manifest type)
- depends_on: []
- description: `web/embed.go`: `package web`, `//go:embed all:dist`, `Dist() fs.FS` via `fs.Sub`. A committed `web/dist/.gitkeep` (root: `web/dist/*` + `!web/dist/.gitkeep`; `web/.gitignore`: `dist/*` + `!dist/.gitkeep`) keeps the embed pattern matching when nothing is built, so `go vet`/`go test` compile on a fresh clone. Makefile: `build-web` runs `pnpm build && touch dist/.gitkeep` (vite empties outDir); `test-go` depends on `build-web` so the binary tests always see a real `index.html`; `lint`/`fmt` exclude `./web/node_modules` and `./web/.stryker-tmp` instead of all of `./web` so `embed.go` is gofmt-checked (see D12). `internal/spa`: pure `resolve(fsys fs.FS, path string) (file string, kind kind)` with a table test; `Handler(fsys) http.Handler` is thin: GET/HEAD only (405 with `Allow`); `path.Clean`, reject `..`; a path under `/api/` or equal to `/healthz` → 404 JSON `{"error":"not_found"}`, never HTML; dotfiles and directories → treated as missing; an existing file → `http.ServeFileFS` with `Cache-Control: no-cache` for `/`, `/index.html`, `/sw.js`, `/manifest.webmanifest`, `public, max-age=31536000, immutable` under `/assets/`, `public, max-age=3600` otherwise; `Content-Type: application/manifest+json` forced for `.webmanifest`; a missing path with a file extension → 404 (never HTML); any other missing path → `index.html` 200 no-cache (SPA fallback); `index.html` itself absent → 503 text "frontend not built: run make build-web". No directory listing ever.
- test_contract:
  - `[verification]` TestResolve_Table on `fstest.MapFS{index.html, assets/app-abc.js, sw.js, manifest.webmanifest, icon-192.png}`: `/` → index.html; `/children`, `/buttons/deep/path` → index.html; `/assets/app-abc.js` → that file, kind immutable; `/assets/gone.js`, `/favicon.ico`, `/old-hash.js` → 404; `/api/v2/x`, `/api/`, `/healthz` → api-404; `/sw.js` → file, kind no-cache; `/assets/` and `/.gitkeep` → index.html (directory/dotfile treated as missing)
  - `[risk]` TestSPA_Fallback: GET `/buttons` and `/children/7` → 200 `text/html`, body == index.html, `Cache-Control: no-cache`; GET `/assets/app-abc.js` → 200 immutable; GET `/assets/` → index.html body containing no "app-abc.js" link
  - `[risk+verification]` TestSPA_NeverHTMLForAPI: GET `/api/v2/x`, `/api/v1/`, `/healthz` → 404 `application/json` `{"error":"not_found"}`; body never contains `<html`
  - `[verification]` TestHandler_MissingAssetIs404: GET `/assets/old-hash.js` and `/favicon.ico` → 404 whose Content-Type is not `text/html`
  - `[risk+mvp]` TestSPA_NotBuilt: MapFS{".gitkeep"} → GET `/` is 503 with body containing "make build-web"; MapFS{} same
  - `[risk+verification]` TestSPA_CacheHeaders: `/sw.js` and `/manifest.webmanifest` → no-cache; `/icon-192.png` → `max-age=3600`; `/assets/app-abc.js` → `public, max-age=31536000, immutable`; a HEAD carries the same headers and no body; POST `/` → 405 with `Allow` containing GET
  - `[risk+verification]` TestSPA_ManifestType: Content-Type for `/manifest.webmanifest` == `application/manifest+json`; GET `/` Content-Type starts `text/html`
  - `[mvp]` TestSPA_NoListing: `/../embed.go` and `/assets/../index.html` → 200 index.html or 400, never a file outside fsys
  - `[risk+verification]` TestEmbed_CompilesWithoutBuild (in `internal/spa`, importing `web.Dist()`): fails to compile if the embed pattern breaks on an unbuilt tree; at runtime asserts `Dist()` opens and either contains `index.html` or only `.gitkeep`

**t-3 — Web manifest, PNG icons and shell metadata** `[risk+verification+mvp]`
- files: `web/public/manifest.webmanifest`, `web/public/icon-192.png`, `web/public/icon-512.png`, `web/index.html`, `web/src/pwa.test.ts`
- covers: c-6 (locked `pwa_scope`)
- depends_on: []
- description: `manifest.webmanifest`: `name "adguard-reward"`, `short_name "Reward"`, `id "/"`, `start_url "/"`, `scope "/"` (locked `pwa_scope`), `display "standalone"`, `background_color`/`theme_color`, icons `[{src:"/icon-192.png", sizes:"192x192", type:"image/png"}, {src:"/icon-512.png", sizes:"512x512", type:"image/png", purpose:"any maskable"}]` — PNG because Chrome's installability check does not accept SVG-only icon sets. PNGs are generated once (solid colour + glyph from the existing mark) and committed; the generator is not committed (see D13). `index.html`: `<title>adguard-reward</title>`, `<link rel="manifest" href="/manifest.webmanifest">`, `<meta name="theme-color">`, `<link rel="apple-touch-icon" href="/icon-192.png">`, viewport unchanged. `pwa.test.ts` runs under node and reads the static files from disk. SW registration in `main.ts` lands with the worker in t-6.
- test_contract:
  - `[risk+verification+mvp]` PWA "manifest is installable": `JSON.parse` of `public/manifest.webmanifest` has `display === 'standalone'`, `start_url === '/'`, `scope === '/'`, non-empty `name` and `short_name`, icons containing sizes `192x192` and `512x512` both `type image/png` with `src` under `/`
  - `[verification+mvp]` PWA "icons are real PNGs": for each manifest icon the file exists under `public/`, its first 8 bytes are the PNG signature, and the IHDR width/height (bytes 16–23) equal the declared size
  - `[risk+verification+mvp]` PWA "shell links the manifest": `index.html` contains `rel="manifest" href="/manifest.webmanifest"`, a `theme-color` meta, an `apple-touch-icon` link, and a `<title>` that is not `web`
  - `[risk+verification+mvp]` manual, recorded in the verify notes: Chrome on Android against the TLS listener shows the install prompt / Lighthouse "installable" and the installed app opens standalone at `/` (HTTPS is a precondition the tests cannot supply)

**t-4 — API client, pure helpers, Login phone tweak; api.ts and Login kills** `[risk+verification; Login.svelte change from mvp]`
- files: `web/src/lib/api.ts`, `web/src/lib/api.test.ts`, `web/src/lib/grants.ts`, `web/src/lib/grants.test.ts`, `web/src/lib/Login.svelte`, `web/src/lib/Login.test.ts`
- covers: c-1, c-2, c-3, c-4, c-7 (api.ts and Login keys), c-8; locked `extend_amount`, `overlap_offer`
- depends_on: []
- description: `api.ts`: `Route` gains `'buttons'` (`PATHS.buttons = '/buttons'`); `ApiError` gains `readonly grantId?: number` parsed from a numeric `grant_id` in the envelope (409); types `Grant{id, child_id, services, clients, started_at, ends_at}`, `GrantCreated{id, ends_at, applied, failed}`, `ButtonInput{label, child_id, services, duration /* seconds */}`, `Button extends ButtonInput{id}`; `listGrants()`, `createGrant(child_id, services, duration)`, `extendGrant(id, duration)`, `endGrant(id)`, `listButtons()`, `saveButtons(ButtonInput[])` (PUT `{buttons}` → the stored `Button[]`). The `typeof history/location/window` guards stay as they are (see D9); the routed api.ts keys die by test. `grants.ts` (pure, node env — best mutation attribution): `MIN_DURATION = 60`, `MAX_DURATION = 86400` (mirror `grants.go`); `remainingSeconds(endsAt, nowMs) = max(0, ceil)`; `formatCountdown(s)` → `h:mm:ss` from an hour else `m:ss`; `formatDuration(s)` → `1 h`, `30 min`, `1 h 30 min`, `24 h`; `ownDuration(g) = clamp(round((ends − started)/1000), MIN, MAX)` (locked `extend_amount`, bounded so a twice-extended 24 h grant still extends — see D14); `overlapsFor(childId, services, grants)` → `{overlapping: Grant[] (same child, any shared service, id ASC), remaining: string[] (services in none of them, input order)}`; `minutesToSeconds`/`secondsToMinutes`. `Login.svelte`: the username input gains `autocapitalize="none" spellcheck="false"` — a real phone-UI change so the file enters the verify diff and its four routed keys are re-scored (see D10). `Login.test.ts` gains the idle/in-flight assertions.
- test_contract:
  - `[risk+verification+mvp]` api.test "grants and buttons endpoints hit the exact URL, method and body": `createGrant(1, ['tiktok'], 1800)` → POST `/api/v1/grants` body `{"child_id":1,"services":["tiktok"],"duration":1800}` with `X-Requested-With`; `extendGrant(4, 600)` → POST `/api/v1/grants/4/extend` `{"duration":600}`; `endGrant(4)` → POST `/api/v1/grants/4/end`, no body, resolves `undefined` on 204; `listGrants()` unwraps `.grants`; `listButtons()` unwraps `.buttons`; `saveButtons([b])` → PUT `/api/v1/buttons` `{"buttons":[b]}` and returns the response's `.buttons`
  - `[risk+verification+mvp]` api.test "409 carries grantId": a 409 `{error:'conflict', message, grant_id: 7}` rejects with `ApiError` status 409, code `'conflict'`, `grantId 7`; a 409 without `grant_id` → `grantId undefined`; a string `grant_id` → `undefined`
  - `[risk+verification+mvp]` api.test "maps the buttons route both ways": `navigate('buttons')` → pathname `/buttons` and `get(route)` `'buttons'`; `replaceState('/buttons')` + popstate → `'buttons'`; `/x` → `'home'`; `/` → `'home'`
  - `[verification+mvp]` api.test "navigate to the current path pushes nothing" (kills `d2e9f9c6` and `47163e3c`): at `/`, `vi.spyOn(history, 'pushState')`; `navigate('home')` → spy called 0 times; `navigate('buttons')` → called exactly once with third arg `'/buttons'` and `location.pathname === '/buttons'`; `navigate('buttons')` again → still once
  - `[verification]` api.test "route reflects the URL at load": for each of `/`, `/login`, `/children`, `/buttons`, `/nope`: `history.replaceState(null, '', p)`; `vi.resetModules()`; `const m = await import('./api')`; `get(m.route)` === `'home'|'login'|'children'|'buttons'|'home'` (pins the successors of the dead `321f5af2`/`7c2c1aaf` keys behaviourally, even though `ignoreStatic` will not score them)
  - `[risk]` api.test "maps the login route both ways": `navigate('login')` → pathname `/login` and route `'login'`; `replaceState('/login')` + popstate → `'login'`; `navigate('home')` → `/`
  - `[risk]` grants.test "remainingSeconds": `ends_at = now+90.2 s` → 91; `now+0` → 0; `now−5 s` → 0 (never negative)
  - `[risk+verification]` grants.test "formatCountdown": 3661 → `1:01:01`; 59 → `0:59`; 600 → `10:00`; 0 → `0:00`; 86400 → `24:00:00`; "formatDuration": 3600 → `1 h`, 1800 → `30 min`, 5400 → `1 h 30 min`, 60 → `1 min`, 86400 → `24 h`
  - `[risk]` grants.test "ownDuration": a grant spanning 1 h → 3600; spanning 48 h → 86400; spanning 10 s → 60
  - `[risk+verification]` grants.test "overlapsFor": grants A(child 1, [youtube]), B(child 1, [tiktok]), C(child 2, [youtube]); child 1 `[youtube, tiktok, roblox]` → overlapping [A,B], remaining [roblox]; child 1 `[roblox]` → [], [roblox]; child 2 `[youtube]` → [C], []; child 1 `[]` → [], []
  - `[risk]` grants.test "minutes": `minutesToSeconds(90)` → 5400; `secondsToMinutes(5400)` → 90; `secondsToMinutes(90)` → 2 (rounded, never a fraction in the form)
  - `[mvp+verification+risk]` Login.test "fields start empty and idle" (kills `56313b4a`, `95210673`): on render both inputs have `.value === ''` and are enabled, the username input has `autocapitalize="none"`, the submit button is enabled with text `Sign in`
  - `[mvp+verification+risk]` Login.test "locks the form while signing in" (kills `f868399f`, `77ea4718`): POST `/login` returns a never-settling promise → after submit both inputs and the button are disabled and the button reads `Signing in…`; resolve with 204 → all enabled, text `Sign in`

### Wave 2

**t-5 — Buttons HTTP endpoints, validation, Deps wiring** `[risk+verification+mvp]`
- files: `internal/api/buttons.go`, `internal/api/buttons_test.go`, `internal/api/api.go`, `internal/api/login_test.go`, `cmd/adguard-reward/main.go`
- covers: c-1, c-4
- depends_on: [t-1]
- description: `api.go`: `ButtonStore interface{ListButtons, ReplaceButtons}`, `Deps.Buttons`, routes `GET`/`PUT /api/v1/buttons` behind `requireSession`. `buttons.go`: view `{id, label, child_id, services (never null), duration (seconds)}`; GET → `{buttons:[…]}` never null. PUT body `{buttons:[{label, child_id, services, duration}]}` decoded with `decodeGrantBody` (16 KiB cap, `DisallowUnknownFields` so a stray `"id"` is 400 — see D2; single object, `buttons` missing/null → 400). Validation cheapest-first, stopping at the first bad item with message `buttons[i].<field>: …` (see D15): label trimmed 1–64 runes; duration via `checkDuration`'s bounds with the index in the message; services trimmed/deduped in given order, empty → 422; then every distinct `child_id` via `Children.GetChild` (`ErrChildNotFound` → 422, before any AdGuard call); then ONE `AdGuard.Services` read, skipped when the list is empty (unknown id → 422 naming it; unreachable → 502, nothing stored); then `store.ReplaceButtons` (`*ErrUnknownChild` → 422 — the in-tx re-check). 200 returns the stored list. `login_test.go` `newHarness` passes `Buttons: st`; `main.go` passes `Buttons: st` so the binary never nil-derefs between this task and t-11.
- test_contract:
  - `[risk+verification+mvp]` TestButtons_Gated: GET and PUT without a cookie → 401; PUT with a cookie but no `X-Requested-With` → 403 and `ListButtons` unchanged
  - `[risk+verification+mvp]` TestButtons_RoundTrip: PUT `[{"TikTok 30m", ada, ["tiktok"], 1800}, {"YouTube 1h", ada, ["youtube","tiktok"], 3600}]` → 200 whose buttons have ids > 0 ascending, that label order, services `["youtube","tiktok"]` unsorted; GET body byte-equal to the PUT response; PUT `{"buttons":[]}` → 200 `{"buttons":[]}` and GET body is exactly that; raw bodies never contain `:null`; `duration` is an integer
  - `[risk+verification+mvp]` TestButtons_Validation: duration 59 → 422 containing `buttons[0].duration`; 86401 → 422; 60 and 86400 → 200; `child_id` 999 → 422 containing `buttons[0].child_id` and `999`, with zero `/control/blocked_services/all` requests; services `["nope"]` → 422 containing `nope` and `buttons[0].services`; services `[]` and `[""]` → 422; label `""` and `"   "` → 422 containing `buttons[0].label`; a 65-rune label → 422; a second item bad → message names `buttons[1]`; a body with both an unknown child and an unknown service → 422 naming the child; `{"buttons":[{"label":"x","id":1,…}]}` → 400 (unknown field); `{"buttons":null}`, `{}`, a bare array and a 20 KiB body → 400; after every non-200 the GET equals the GET taken before the call
  - `[risk+verification+mvp]` TestButtons_CatalogueDown: `SetStatus("/control/blocked_services/all", 500)` → PUT is 502 `adguard_unavailable`, list unchanged; PUT `[]` with the catalogue down → 200 (no catalogue read needed); GET `/buttons` issues zero catalogue requests
  - `[risk]` TestButtons_IDsReassigned: two PUTs of the same body yield different (larger) ids
  - `[risk+verification]` TestButtons_ChildDeleteCascades: buttons for ada and ben; `DELETE /children/{ada}` → 204; GET `/buttons` lists only ben's, same id as before
  - `[risk]` TestButtons_ConcurrentPut under `go test -race`: 8 goroutines PUT 8 distinct lists → all 200; the final GET equals exactly one of the 8 bodies

**t-6 — Service worker: routing module, precache build step, registration** `[risk; install/activate glue tests from verification]`
- files: `web/src/sw.ts`, `web/src/sw.test.ts`, `web/src/lib/sw-routing.ts`, `web/src/lib/sw-routing.test.ts`, `web/vite.config.ts`, `web/src/main.ts`
- covers: c-6
- depends_on: [t-3]
- description: `sw-routing.ts` (pure, node env): `PUBLIC_SHELL = ['/', '/manifest.webmanifest', '/favicon.svg', '/icon-192.png', '/icon-512.png']`; `decide({method, url, mode, origin}, precache: Set<string>)` → `'bypass'` (non-GET, cross-origin, path starting `/api/` or `/healthz`, or `/sw.js`) | `'shell'` (mode navigate → network-first, fall back to cached `/`) | `'asset'` (path in precache → cache-first) | `'bypass'` otherwise; `cacheName(precache) = 'shell-' + FNV-1a hex of the sorted list` so a deploy with identical assets does not churn caches. `sw.ts`: install → `cache.addAll(self.__PRECACHE__)` into `cacheName`, `skipWaiting`; activate → delete every other cache, `clients.claim()`; fetch → `decide()`; never `cache.put` anything at runtime (precache only). `vite.config.ts` (already Stryker-disabled): second rollup input `sw: src/sw.ts`, `output.entryFileNames` puts that chunk at `/sw.js` (unhashed); an inline `precache` plugin in `generateBundle` replaces the literal `self.__PRECACHE__` in the sw chunk with JSON of every emitted chunk/asset fileName except `sw.js` plus `PUBLIC_SHELL`, and throws if the sw chunk still contains `import `/`export ` (must be a classic script) or the token was not found (see D3 for the alternative mechanism). `main.ts`: register `/sw.js` only when `import.meta.env.PROD && 'serviceWorker' in navigator` (dev server stays uncached); `main.ts` keeps `Stryker disable all`; `sw.ts` is covered by `sw.test.ts` (stubbed `self`/`caches`/`fetch`) so it carries no disable header.
- test_contract:
  - `[risk+verification+mvp]` sw-routing.test "API and non-GET always bypass": GET `/api/v1/grants`, GET `/api/v1/buttons`, POST `/`, GET `/healthz`, GET `/sw.js` and a cross-origin GET each → `'bypass'` regardless of precache contents; a navigate to `/api/v1/x` → `'bypass'`
  - `[risk+verification+mvp]` "navigations are shell": GET mode navigate to `/`, `/buttons`, `/children/3` → `'shell'`; a non-navigate GET of `/children` → `'bypass'`
  - `[risk+verification]` "precached assets are cache-first": `/assets/app-abc.js` in the set → `'asset'`; `/assets/other.js` not in the set → `'bypass'`; `/manifest.webmanifest` → `'asset'`
  - `[risk]` "cacheName changes with the list": two lists differing in one hash → different names; the same list in another order → the same name
  - `[risk]` "PUBLIC_SHELL entries exist": for every entry except `/`, `readdirSync`-based existence under `web/public`
  - `[verification]` sw.test "api requests bypass the worker": a fetch event for GET `http://localhost/api/v1/grants`, POST `/api/v1/grants`, GET `/healthz` and GET `https://other.example/x` each leave `respondWith` uncalled and `caches.open/match` uncalled
  - `[verification+mvp]` sw.test "offline navigation serves the shell": mode `navigate` for `/children` with fetch resolving 200 → `respondWith` resolves to that response and `caches.match` is not consulted; with fetch rejecting → resolves to `caches.match('/')`
  - `[verification]` sw.test "assets come from the precache, never put at runtime": GET `/assets/app-abc.js` present in the fake cache → cached response, fetch not called; a URL absent from the cache falls through to fetch and is not `cache.put` afterwards
  - `[verification]` sw.test "install precaches and activate prunes": dispatching install with `__PRECACHE__ = ['/', '/assets/a.js']` calls `cache.addAll(['/', '/assets/a.js'])` on `cacheName(...)` and `skipWaiting`; activate with caches `['shell-old', <current>]` deletes only `shell-old` and calls `clients.claim`
  - `[risk]` sw-routing.test "vite build emits sw.js with a precache list" (spawns vite's build API into a temp `outDir`): `dist/sw.js` exists, contains no `__PRECACHE__`, contains no `import `, and every listed path except `/` exists in the temp dist

**t-7 — Active-grants store: poll, merge, reachability** `[risk]`
- files: `web/src/lib/activeGrants.ts`, `web/src/lib/activeGrants.test.ts`
- covers: c-2, c-3; locked `unreachable_countdown`
- depends_on: [t-4]
- description: Svelte store module `activeGrants = readable {grants: Grant[]; unreachable: boolean; loaded: boolean}` (see D4). `refresh()`: a call while one is in flight returns the same promise (no overlap); success → grants replaced, `unreachable=false`, `loaded=true`; `ApiError` 401 → state untouched (the api layer routes to login; a session lapse is not "unreachable"); any other failure → `unreachable=true`, grants kept as they were. `merge(g: Grant)`: upsert by id — a 201 body is server truth (id, ends_at), not optimism, so the entry is visible without a second round trip. `remove(id)`. `start(intervalMs = 15000)`: immediate refresh, `setInterval`, `document visibilitychange` → refresh when visible (a backgrounded phone tab catches up on return); returns `stop`; a second `start` while started is a no-op returning the same stop; `stop` clears the interval and the listener.
- test_contract:
  - `[risk]` "polls on the interval and stops" (fake timers): `start(15000)` → one GET immediately; +45 s → exactly 4 GETs; a GET that never resolves while +30 s elapse → still 2 GETs total; `stop()`; +60 s → no further GET; `start` twice → one interval
  - `[risk]` "failure keeps grants and sets unreachable": first poll returns [g1]; second rejects with TypeError → grants still [g1], `unreachable` true; third succeeds → `unreachable` false and grants from the response
  - `[risk]` "401 is not unreachable": a 401 → `unreachable` stays false, grants unchanged, the `onUnauthorized` spy was called once
  - `[risk]` "visibilitychange refreshes": with the interval far away, dispatching `visibilitychange` with `visibilityState 'visible'` → one extra GET; `'hidden'` → none
  - `[risk]` "merge upserts by id": `merge(g2)` on [g1] → [g1,g2]; `merge(g1')` (same id, later ends_at) → [g1',g2] with one entry for that id; `remove(2)` → [g1']; the next successful poll replaces everything

**t-8 — GrantForm: child, services, duration fieldset** `[risk+verification; mvp dissent D16]`
- files: `web/src/lib/GrantForm.svelte`, `web/src/lib/GrantForm.test.ts`
- covers: c-4, c-8
- depends_on: [t-4]
- description: Presentational fieldset shared by the ad-hoc form (Home) and the settings page (Buttons) so validation lives once. Props: `children: Child[]`, `services: Service[] | null` (null = catalogue unavailable), `value: {child_id: number | null, services: string[], minutes: number}` (`$bindable`, minutes default 60), `disabled`. Renders `<select name="child" aria-label="Child">` (placeholder option when `child_id` is null; auto-selects the only child when exactly one exists), one `<input type="checkbox" name="service" value=id>` per catalogue service with its `iconUrl` image and name, `<input type="number" name="minutes" inputmode="numeric" min=1 max=1440 step=1 aria-label="Minutes">`. Exposes `valid` (`$bindable`, derived): child chosen, services non-empty, `1 ≤ minutes ≤ 1440` and integer. Services are kept in tick order (the first service picks the icon — locked `button_icon`). A `value.services` id not in the catalogue renders as a checked row labelled with the raw id and a "not in AdGuard Home" hint, still unticks like any other, and is never dropped silently. `services === null` → a "service list unavailable" note, no checkboxes, `valid` false unless `value.services` is already non-empty (a stored button can still be saved as-is). No submit button of its own; the label field lives in the page.
- test_contract:
  - `[risk+verification+mvp]` "binds child, services and minutes": select Ada, tick YouTube then TikTok, type 90 → value `{child_id:1, services:['youtube','tiktok'] in tick order, minutes 90}`; untick YouTube → `['tiktok']`
  - `[risk+verification+mvp]` "valid tracks the bounds": nothing selected → false; child only → false; child + service → true; minutes 0 → false; 1 → true; 1440 → true; 1441 → false; 1.5 → false; no services → false
  - `[verification]` "one child is preselected": children [Ada] → select value `'1'` and `child_id 1` on mount; children [Ada, Ben] → select value `''` and `child_id null`
  - `[risk]` "unknown service id is kept and marked": `value.services ['gone']` with catalogue [youtube] → a checked row with text containing `gone` and `not in AdGuard Home`; `value.services` still `['gone']`; unticking it → `[]`
  - `[risk+verification]` "disabled greys every control": `disabled` true → select, every checkbox and the minutes input have `disabled === true`
  - `[risk]` "null catalogue": `services null` with `value.services ['youtube']` → note visible, `valid` true; with `[]` → `valid` false

### Wave 3

**t-9 — ActiveGrants panel: countdown, extend, end, badge** `[risk; row-busy contract from verification]`
- files: `web/src/lib/ActiveGrants.svelte`, `web/src/lib/ActiveGrants.test.ts`
- covers: c-3; locked `extend_amount`, `unreachable_countdown`
- depends_on: [t-7]
- description: Reads `activeGrants`; props `childNames: Map<number,string>`, `serviceNames: Record<string,string>` (fallback: raw id). `<section aria-label="Active grants">` with heading "Unlocked now" and `data-badge="unreachable"` badge "Can't reach server" when `store.unreachable`; empty → "Nothing is unlocked right now". Per grant `<li data-grant={id}>`: child name, service names joined ", ", `<time data-countdown>` from `formatCountdown(remainingSeconds(ends_at, now))`, buttons `Extend {formatDuration(ownDuration(g))}` and `End`. One `setInterval(1000)` updates `now` for the whole list; `onDestroy` clears it. The countdown is recomputed from `ends_at` every tick, never decremented. When a grant's remaining hits 0 it shows `0:00` and triggers ONE `refresh()` for that id (Set guard); the entry leaves only when the server list no longer has it (see D5). A row's two buttons are disabled while its own request is pending. Extend → `extendGrant(id, ownDuration(g))` then `refresh()`; 404 → `refresh()` silently (already gone); other error → `<p role="alert">` inside that li with `messageFor`. End → `endGrant(id)` then `refresh()`; 502 → alert with the server message (names the clients), entry stays; 404 → `refresh()` silently.
- test_contract:
  - `[risk+verification+mvp]` "countdown follows ends_at" (fake timers + `setSystemTime`): grant ends in 10 min → `10:00`; advance 1 s → `9:59`; `setSystemTime(+5 min)` without running timers, then advance 1 s → `4:58`; a 2 h grant reads `2:00:00`
  - `[risk]` "expiry asks the server": at `0:00` exactly one extra GET `/api/v1/grants`; while the mock still lists the grant it stays rendered at `0:00` and 5 more ticks add no further GET; when the mock drops it, the li disappears and, if it was the only grant, "Nothing is unlocked right now" shows
  - `[risk+verification+mvp]` "extend posts own duration": started/ends 1 h apart → button text `Extend 1 h` and POST body `{"duration":3600}`; 1 h 30 min apart → `Extend 1 h 30 min` and `{"duration":5400}`; 48 h apart → `{"duration":86400}`; then GET is re-issued and the new `ends_at` is reflected in the countdown
  - `[risk+verification+mvp]` "end 502 keeps the entry": end → 502 `adguard_unavailable` "could not re-block Kid phone" → alert inside `li[data-grant]` with text containing `Kid phone`, li still present, no extra GET; end → 204 → GET re-issued and the mock's list without it empties the panel
  - `[verification]` "row is busy while its own request is pending": click End on row 7 with a never-settling POST → both of row 7's buttons disabled and row 8's not; after it resolves they are enabled
  - `[risk]` "extend 404 refreshes silently": extend → 404 → no alert, one GET, li gone when the mock omits it
  - `[risk+verification+mvp]` "unreachable badge": poll rejects → `[data-badge="unreachable"]` visible, every li still present and the countdown still ticks (advance 1 s → decremented text); next success → badge gone
  - `[risk]` "unmount stops ticking": `unmount()`; advance 60 s → no GET, no thrown error, `vi.getTimerCount()` is 0
  - `[risk+verification]` "names": `childNames` has 1→Ada; `serviceNames` lacks `gone` → li text contains `Ada`, `YouTube` and `gone`

**t-10 — Buttons settings page, route, App kills** `[mvp+verification structure; draft retention + catalogue-down from risk; App kills all three]`
- files: `web/src/lib/Buttons.svelte`, `web/src/lib/Buttons.test.ts`, `web/src/App.svelte`, `web/src/App.test.ts`
- covers: c-4, c-7 (App key); locked `button_order`
- depends_on: [t-8]
- description: `Buttons.svelte` props `{onBack}`. Load: `Promise.all(listButtons, listChildren)` plus `listServices` separately (failure → `services null`, page still editable). A list of stored buttons in array order, each `<li data-button={id}>` with label, child name, service names, `formatDuration`, buttons Edit and Delete; no reorder controls (locked `button_order`). One form: `<input name="label">` + `<GrantForm>` + Save (disabled while pending, unless label non-blank and fields valid) + Cancel when editing (see D8). Save builds the next list (append, or replace the edited index in place), maps minutes → seconds, strips ids, `saveButtons`, replaces the list from the response (server truth, fresh ids), clears the form. Delete PUTs the list without that item. Any failure → `<p role="alert" data-error={code}>` with `messageFor`; the list is reloaded from the server and the form draft is KEPT so the parent fixes the row instead of losing it. Unknown service ids flow through GrantForm's marking. `App.svelte`: `{:else if $route === 'buttons' && username !== null}<Buttons onBack={() => navigate('home')} />`; `refresh()` already keeps a reload on `/buttons` there. `App.test.ts` adds the App kill.
- test_contract:
  - `[mvp+verification]` "add appends and PUTs everything": existing [b1 (id 3, youtube, 5400)]; fill label `TikTok 30m`, child Ada, tick tiktok, minutes 30, Save → exactly one PUT `/api/v1/buttons` whose body is `{buttons:[{label: b1.label, child_id, services, duration: 5400}, {label:'TikTok 30m', child_id:1, services:['tiktok'], duration:1800}]}` with no `id` keys; the list re-renders from the response (2 rows, the new id from the server)
  - `[verification+risk]` "edit keeps position and converts units": [b1,b2,b3], Edit b2 (shows minutes 90 for 5400), change label to `Renamed`, tick TikTok, minutes 45, Save → PUT body labels [b1,'Renamed',b3] with item 2 `{services:[…,'tiktok'], duration:2700}`
  - `[mvp+verification]` "delete PUTs without the item": [b1,b2], Delete b1 → PUT body `{buttons:[b2 without id]}` and one row remains
  - `[risk+verification+mvp]` "422 shows the server message and keeps the draft": PUT → 422 `{error:'unprocessable', message:'buttons[0].services: unknown service "gone"'}` → alert text equals that message, `data-error 'unprocessable'`, the form's label/minutes unchanged, GET `/buttons` re-fetched (calls count 2)
  - `[verification]` "Save waits for a valid form": on mount Save is disabled; label only → disabled; label + child + service → enabled
  - `[risk]` "services 502 still lists buttons": `/api/v1/services` → 502 → rows render with the "service list unavailable" note; Save of an untouched edit is still possible and PUTs the stored services
  - `[risk+verification+mvp]` App "reload on /buttons stays there": `replaceState('/buttons')`, `route.set('buttons')`, `/me` 200 → heading `Buttons`, pathname `/buttons`; "buttons needs a session": `/me` 401 → login page, no `/api/v1/buttons` call; "Back from Buttons returns home" → pathname `/`
  - `[risk+verification+mvp]` App "a non-401 failure on /me lands on /login" (kills `d248d03c`): `replaceState('/children')`, `route.set('children')`, `setOnUnauthorized(spy)`, `/me` → 502 → Sign in button rendered AND `location.pathname === '/login'`, `get(route) === 'login'`, spy not called; same from `/buttons`
  - `[mvp]` App "back to /children while signed out stays on login": 401 on `/me`, then `pushState('/children')` + popstate → Sign in still shown, no `Children` heading, fetch calls remain exactly `['/api/v1/me']`

**t-11 — Wire SPA into main; binary-level SPA, PWA and restart proofs** `[risk+verification+mvp]`
- files: `cmd/adguard-reward/main.go`, `cmd/adguard-reward/main_test.go`
- covers: c-1, c-4, c-5, c-6
- depends_on: [t-2, t-3, t-5, t-6]
- description: `main.go`: `mux.Handle("/", spa.Handler(web.Dist()))` beside `GET /healthz` and `/api/v1/` (Go's mux prefers the longer pattern, so the API chain and liveness are untouched — see D11). `main_test.go` runs against the real embed (`test-go` depends on `build-web`); `TestRun_SPA*` call `t.Fatalf("web/dist not built — run make build-web")` when `index.html` is absent (a skip would be a false pass). Helpers `putButtons`/`getButtons` over the running server for the restart test.
- test_contract:
  - `[risk+verification+mvp]` TestRun_SPAFallback: GET `/buttons` and `/children/7` with no cookie → 200 `text/html`, body byte-equal to the embedded `index.html`, containing `<div id="app">` and `rel="manifest"`, `Cache-Control: no-cache`; the hashed `/assets/*.js` referenced by that body → 200 immutable; GET `/assets/missing.js` → 404; GET `/api/v1/nothing` with a cookie → 404 JSON `not_found`; without → 401; GET `/api/v2/x` → 404 JSON never HTML; `/healthz` unchanged (existing `TestRun_Healthz` still green)
  - `[risk+verification+mvp]` TestRun_PWAServed: `/manifest.webmanifest` → 200 `application/manifest+json` parsing to `start_url "/"`, `scope "/"`, `display standalone`; `/icon-192.png` and `/icon-512.png` → 200 `image/png` decoding to 192² / 512²; `/sw.js` → 200 with a JavaScript Content-Type, `no-cache`, body without `__PRECACHE__` and without `/api/`
  - `[verification+risk]` TestRun_SWPrecacheListIsServed: parse the precache JSON out of `/sw.js`; it contains `/` and at least one `/assets/` entry; every entry answers 200 from the same binary, every `/assets/` entry with `Cache-Control` containing `immutable`; no entry starts with `/api/`
  - `[risk]` TestRun_APINoStore: GET `/api/v1/buttons` with a cookie → `Cache-Control: no-store` (unchanged); the same path via HEAD is not HTML
  - `[risk+verification+mvp]` TestRun_ButtonsSurviveRestart: login, POST child, PUT two buttons, stop (exit 0), start on the same `data_dir`, GET `/buttons` (old cookie or fresh login) → body byte-equal to the first run's GET (ids, order, services order, durations)
  - `[mvp]` TestRun_ButtonsWired: PUT with `child_id 999` → 422 (validation reached the store-backed children read)
  - `[verification]` existing TestRun_ApiChain still passes: `/me` without cookie is 401 JSON `no-store`, login without CSRF is 403

### Wave 4

**t-12 — Home: buttons per child, tap, 409 offer, ad-hoc form, disclosure** `[risk; layout/adhoc-open contracts from verification; Home kills all three]`
- files: `web/src/lib/Home.svelte`, `web/src/lib/Home.test.ts`
- covers: c-2, c-3, c-7 (Home keys), c-8; locked `home_layout`, `no_confirm`, `overlap_offer`, `extend_amount`, `button_icon`, `button_order`
- depends_on: [t-7, t-8, t-9]
- description: Layout per locked `home_layout`: nav (Children, Buttons, Log out — `signOut`/`pending` kept verbatim), `MigrationBanner`, `<ActiveGrants>`, then per child `<section data-child>`: h2, its buttons in stored order (`<button data-button={id}>` with the first service's icon via `iconUrl` when the catalogue loaded, label, `formatDuration`), a `<details data-blocked={child.id}>` "Blocked services" whose `childBlocked()` fires on first open only, with its own error line inside (AdGuard down never blocks the tap surface — see D7); a child with no buttons shows "No buttons yet" with a Buttons link. Bottom: `<details data-adhoc open={buttons.length === 0}>` "Unlock something else" — `GrantForm` + an "Unlock" button (c-8) feeding the SAME `tap()` as a button. `load()`: `Promise.all(listChildren, listButtons)` → page-level alert on failure; `listServices` separately, failure → icons and names fall back; `activeGrants.start()` in `onMount`, `stop` in `onDestroy`. `tap(spec{key, child_id, services, duration})`: key in an inflight Set → ignored and the control is disabled while in flight (locked `no_confirm`: no dialog). Outcomes: 201 applied → `merge(grant built from the 201 + spec + child.clients)` then `refresh()`; 201 `applied=false` → merge AND `<p role="alert" data-error="partial">` "Unlocked, except on Kid tablet" naming `failed`; network/5xx/422/404 → alert with `messageFor` next to that child's buttons. 409 (see D6): `await refresh()` (ignore its failure), `targets = overlapsFor(child, services, grants).overlapping`, falling back to `[{id: err.grantId}]` when empty; render `<div role="dialog" data-offer>` "YouTube is already unlocked for Ada — extend by 1 h?" with Extend / Cancel. Extend: for each target `extendGrant(id, spec.duration)` (404 → drop that target and count its services as remaining); if remaining non-empty → `createGrant(remaining)` through the same outcome handling (a second 409 shows a plain alert — no loop); then `refresh()`. Errors name what failed. `onMigrated` → `load()` and clear cached blocked views (open disclosures refetch).
- test_contract:
  - `[risk+verification+mvp]` "renders buttons per child in stored order": buttons [b3(ben), b1(ada, [youtube,tiktok]), b2(ada)] → `section[data-child=1]` holds b1 then b2, section 2 holds b3; b1's `<img src>` is `iconUrl` of youtube's icon; a button whose first service is not in the catalogue renders no `<img>`; a button with `child_id 99` renders nowhere and nothing throws
  - `[verification]` "layout order": the ActiveGrants section precedes every `section[data-child]`; within a section the DOM order is h2 → buttons → `details[data-blocked]` (not open)
  - `[risk+verification+mvp]` "tap posts the grant immediately": click b1 → exactly one POST `/api/v1/grants` body `{child_id:1, services:["youtube","tiktok"], duration:3600}`, no dialog; 201 → `li[data-grant]` with the 201's id visible and its countdown text `60:00` before any GET resolves (the mock GET is left pending, so only `merge` can have rendered it)
  - `[risk]` "double tap is one POST": two clicks before the POST resolves → one POST, button disabled meanwhile, enabled after
  - `[risk+verification+mvp]` "applied=false names the client": 201 `{applied:false, failed:["Kid tablet"]}` → alert `data-error="partial"` containing `Kid tablet` and the grant still listed in the panel
  - `[risk+verification+mvp]` "failed POST shows an inline error": fetch TypeError → alert `data-error="network"`; 422 `unknown service "gone"` → alert with that text; 502 → `adguard_unavailable` line "Can't reach AdGuard Home …"; each alert sits inside that child's section
  - `[risk+verification+mvp]` "409 offers extend": POST → 409 `grant_id 5`; the refetched list has grant 5 (ada, [youtube], ends in 42 min) → dialog text contains `YouTube`, `Ada` and `1 h`; Cancel → dialog gone, no further request
  - `[risk+verification+mvp]` "accept extends and creates the rest" (locked `overlap_offer`): button [youtube,tiktok,roblox], list has A(ada,[youtube]) and B(ada,[tiktok]) → Extend → POST `/grants/A/extend {3600}`, POST `/grants/B/extend {3600}`, POST `/grants {services:["roblox"]}`, then GET; extends ordered by id ASC; a tap of [youtube] alone then Extend → only the extend POST
  - `[risk]` "expired target retries create": extend → 404 → POST `/grants` with the full services list once; a second 409 there → plain alert, no third POST
  - `[risk+verification+mvp]` "ad-hoc form creates a grant" (c-8): with zero buttons configured `[data-adhoc]` is open and a hint mentions Buttons; select Ben, tick TikTok, 30 min, Unlock → POST `{child_id:2, services:["tiktok"], duration:1800}`; 201 → panel entry within the same tick showing `30:00`; a 409 here opens the same dialog; with buttons present `[data-adhoc]` renders closed
  - `[risk]` "services 502 still renders buttons": `/api/v1/services` → 502 → buttons render without `<img>`, tapping works, no page-level alert; the panel names services by id
  - `[risk]` "blocked list loads on open" (locked `home_layout`): no `/children/1/blocked` call at mount; open `<details>` → one call and the blocked names render inside (the phase-03 client badges and service rows, existing assertions retained inside it); a 502 there → alert inside the details only, buttons still tappable; migration applied while open → the call is re-issued
  - `[risk]` "unmount stops polling": unmount, advance 60 s → no GET `/api/v1/grants`
  - `[risk+verification+mvp]` "log out is enabled, then locked, then enabled" (kills `c68261e3`, `a094a307`, `beec743e`): on mount `Log out` is not disabled; POST `/logout` returns a never-settling promise → after click it is disabled; resolve 204 with `onLogout` a spy → not disabled and the spy called once; same after a rejected logout
  - `[risk+verification+mvp]` "Buttons button routes": click `Buttons` → `location.pathname '/buttons'` and `get(route) 'buttons'`; the existing `Children` and log-out App tests still pass

### Wave 5

**t-13 — c-7 exit gate: observed scoped Stryker run, residual kills** `[risk]`
- files: `web/src/lib/api.test.ts`, `web/src/lib/Home.test.ts`, `web/src/lib/Login.test.ts`, `web/src/App.test.ts` (only those a residual survivor demands)
- covers: c-7
- depends_on: [t-4, t-10, t-12]
- description: Test-only; runs last so the gate observes the final component code (see D9). The behavioural kills already live in t-4 (api.ts, Login), t-10 (App) and t-12 (Home). Here: run `pnpm exec stryker run --mutate src/lib/api.ts,src/lib/Home.svelte,src/lib/Login.svelte,src/App.svelte` in `web/` and read `reports/mutation/mutation.json`; every routed key's present-day location (api.ts:49 ×2, Home.svelte pending literals, App.svelte catch body, Login.svelte:6/7/13/44) must show no `Survived` mutant. If one survives because Stryker's per-test attribution missed it (the documented `stryker-whole-file-attribution` ceiling), move or duplicate the killing assertion into the owning component's test file where attribution is most direct, re-run, and observe again. The observed result is quoted in the commit body (execution rule 1: never claim a kill that was not seen). The four dead keys (`321f5af2`, `7c2c1aaf`, `04a39c82`, `fd433887`) cannot appear as survivors — the first is rewritten code, the next two are static-ignored, the last is already `Killed` — so c-7's "none of them surviving" holds for them by construction; the note goes in the verify notes.
- test_contract:
  - `[risk]` exit gate: `mutation.json` lists no `Survived` mutant for `api.ts:49` (ConditionalExpression→true, LogicalOperator), `Home.svelte` `pending = $state(false)` / `pending = true` / `pending = false`, `App.svelte` refresh's catch body, `Login.svelte:6/7/13/44`
  - `[risk]` if a residual kill is added: it fails against the mutant applied by hand (`vitest` run with the mutation patched in) and passes on the real code — recorded in the commit body

### Coverage

| criterion | tasks |
|---|---|
| c-1 buttons stored, ordered, survive restart, 422s | t-1, t-5, t-11 (restart against the binary), t-4 (client) |
| c-2 Home buttons, tap → countdown ≤ 5 s, inline errors | t-12 (render, tap, inline errors), t-7 (merge so the entry is visible without a second round trip), t-4 |
| c-3 active panel, ticking countdown, extend/end, 409 → extend, leaves on expiry | t-9 (panel), t-7 (poll/badge), t-12 (409 → offer), t-4 |
| c-4 buttons settings page, PUT, render after restart | t-10 (page, route), t-8 (fieldset), t-5, t-11 (restart proof), t-4 |
| c-5 embedded SPA, fallback, 404 for unknown /api/v1 | t-2 (handler + embed + build hygiene), t-11 (binary-level proofs) |
| c-6 manifest, SW shell-only, /api never cached, installable | t-3 (manifest, PNGs, index.html), t-6 (SW never caches /api, precache build), t-2 (manifest type / no-cache), t-11 (served from the binary; manual phone check) |
| c-7 14 routed survivors killed | t-4 (api.ts, Login — with Login.svelte in the diff), t-10 (App), t-12 (Home), t-13 (observed exit gate) |
| c-8 ad-hoc grant form | t-12 (same `tap()` path), t-8 (fieldset), t-4 |

Locked decisions pinned by a named test: `home_layout` (t-12 layout order + disclosure),
`extend_amount` (t-4 ownDuration, t-9 extend posts own duration), `overlap_offer` (t-4
overlapsFor, t-12 accept extends and creates the rest), `no_confirm` (t-12 tap posts
immediately, double tap), `button_icon` (t-1 services order, t-8 tick order, t-12 img src),
`button_order` (t-1 order, t-10 no reorder controls, t-12 render order), `pwa_scope` (t-3
start_url/scope), `unreachable_countdown` (t-7 failure keeps grants, t-9 badge).

## Disagreements

**D1 — Buttons schema: `button_services` table (risk, verification) vs a JSON text column (mvp).**
mvp: one `services TEXT` column holding a JSON array in the parent's order, one migration,
no fold logic. Risk and verification: a child table with its own `position`, mirroring
`child_clients` and `grant_services`. Provisional default: **table** (2-of-3 and the
store's established idiom; the `PRIMARY KEY (button_id, service_id)` also dedupes at the
DB level). Why it matters: the JSON column is genuinely simpler for a list that is only
ever read whole; flipping it changes t-1 only (drop the `button_services` count
assertions; add "raw column text is a JSON array in given order").

**D2 — PUT id semantics: reassign ids on every PUT (risk, verification) vs upsert that keeps ids (mvp); stray `id` ignored (risk) vs 400 (verification).**
mvp keeps ids via `INSERT … ON CONFLICT(id) DO UPDATE` so Home/Settings DOM keys do not
thrash across saves. Risk and verification reassign: nothing on the server references a
button id (a tap POSTs child/services/duration). Provisional default: **reassign, and a
body `id` is 400** via the existing `DisallowUnknownFields` (an ignored field is an
untestable branch; the client type `ButtonInput` has no id). Why it matters: it decides
whether the settings page must track ids at all and whether a stale Home can mis-key a
button (AUTOINCREMENT closes that). mvp's keyed-DOM stability is real but cosmetic for a
list a parent edits a few times a year.

**D3 — Service worker source and build: `src/sw.ts` + inline Vite plugin (risk, verification) vs hand-written `public/sw.js` with no build step (mvp).**
mvp's worker is ~40 lines of plain JS outside Stryker's `src/**` glob, runtime-caches
`/assets/*` rather than precaching them, and cannot know the hashed asset names.
Risk/verification generate the precache list at build time. Provisional default: **`src/sw.ts`
+ plugin**, with risk's pure `sw-routing.ts` (table-testable under node, `cacheName` hash)
and verification's glue tests on `sw.ts`. Sub-divergence on mechanism: risk adds `sw` as a
second rollup input and token-replaces `self.__PRECACHE__` (with a build-time throw if the
chunk is ESM); verification compiles `sw.ts` standalone with `transformWithEsbuild` and
`emitFile`. Default: **risk's** (lets `sw.ts` import `sw-routing.ts`); if rollup ever
hoists a shared chunk into the worker the guard throws and the executor switches to
verification's mechanism. Why it matters: c-6 says "precaches the app shell only" — a
worker that runtime-caches assets is arguably fine, but one that cannot precache hashed
assets serves a stale shell after every deploy until the second visit.

**D4 — Where polling and tap state live: a dedicated `activeGrants.ts` store (risk) vs Home owns polling and the panel is presentational (mvp, verification).**
Verification's `ActiveGrants` receives `now`, `grants`, `unreachable` and callbacks so its
tests need no fake timers; mvp likewise. Risk's store module owns refresh-dedupe, merge,
401-vs-unreachable and visibilitychange, and the panel reads it. Provisional default:
**risk's store** — it is the only draft in which the locked `unreachable_countdown` and
the backgrounded-tab/overlapping-poll cases are tested in isolation (5 contracts that would
otherwise sit inside a 14-contract Home test). Poll interval 15 s (risk, mvp) over 10 s
(verification): c-2's "within 5 s" is met by `merge` + the immediate `refresh`, not the
poll. Why it matters: it is 2-vs-1 for the simpler shape; the cost of the store is one
extra wave-2 task that runs in parallel with t-5/t-6/t-8.

**D5 — Panel removal on expiry: server list drives it (risk) vs the client clock hides the row at zero (mvp, verification).**
mvp: "advance past ends_at → the row is gone with no fetch made"; verification: `shown =
grants with ends_at > now`. Risk: at `0:00` trigger one refresh, and remove the row only
when the server list no longer has it. Provisional default: **risk** — the revert is
server-side and can fail (the engine leaves such a grant active), so hiding it at zero
would hide exactly the drift the project exists to prevent; a row reading `0:00` until the
next poll is truthful. Why it matters: c-3 literally says "leaves the panel when it
expires", which both readings satisfy; the choice decides whether the panel can ever
disagree with the server about which grants exist.

**D6 — Overlap handling: POST first and turn a 409 into the offer (risk, mvp) vs plan client-side first and POST only the non-overlapping part (verification).**
Verification's `planTap` inspects the loaded list before any request, so a known overlap
costs zero POSTs and the dialog appears instantly; a server 409 (stale list) refreshes and
re-plans once. Risk posts first (the server is the authority), refreshes on 409, computes
targets with `overlapsFor` falling back to the 409's `grant_id`, and retries create once
when an extend 404s. Provisional default: **risk** — the 409 path must exist regardless,
and one path serving both fresh and stale lists is simpler than two entries into the same
dialog; the locked decision only says overlaps "may" be resolved client-side. Why it
matters: verification's variant saves a round trip on every overlapping tap; if the
executor prefers it, `overlapsFor` already returns what `planTap` needs.

**D7 — Blocked-services disclosure: lazy fetch on first open (risk) vs eager per-child fetch on load as in phase 03 (mvp, verification).**
Risk: `childBlocked()` fires only when the `<details>` opens, with its own error line, so
an unreachable AdGuard never blocks the tap surface (buttons and grants are store-backed).
mvp/verification keep phase 03's `Promise.all` including blocked views, so the existing
Home tests survive with selectors moved into `details[data-blocked]`. Provisional
default: **risk** (lazy). Why it matters: 2-vs-1 for eager; eager costs N AdGuard round
trips before the first tap and turns AdGuard-down into a page-level error on the one
screen whose core action does not need AdGuard. Cost: two existing Home tests move their
error assertion inside the disclosure.

**D8 — Settings page shape: every row an editable draft with one Save (risk) vs a list with Edit/Delete plus one add/edit form (mvp, verification).**
Risk's variant PUTs the whole draft table in one tap and keeps every row on a 422. mvp and
verification render the stored list and edit one item at a time through the shared
fieldset, PUTting the rebuilt list on each Save/Delete. Provisional default: **list +
form** (2-of-3; fits a phone screen with more than three buttons), with risk's two
contracts grafted: the form draft is kept on any error, and a null catalogue still lets
stored buttons be saved. On error the list is reloaded from the server (verification) —
compatible with keeping the form draft, since the form and the list are separate state.
Why it matters: this is the surface the M2 button editor will replace; either shape
satisfies c-4.

**D9 — c-7 placement and method: a final gated task (risk) vs kills inside the rewriting tasks (mvp, verification); api.ts guard removal (mvp) vs a pushState spy test (risk, verification).**
Verification and mvp put each kill in the task that already rewrites that test file
(no parallel-edit conflict, no test-only commit); risk's stated reason for a final task —
"kills would be undone by the rewrite" — does not hold when the kill lives in the rewrite.
But `.dross/survivors.toml` documents that hand-verified kills are sometimes not credited
(`stryker-whole-file-attribution`), and c-7 is worded as "the verify run reports none
surviving". Provisional default: **kills in the owning tasks (t-4, t-10, t-12) plus risk's
t-13 as an observed exit gate** that runs a scoped Stryker and only adds tests if a kill
was not credited. On api.ts: mvp deletes the `typeof history/location/window` guards so
the routed keys cease to exist; default is **verification's spy test with no code change**
— it kills both live keys behaviourally, and guard removal would also invalidate the
already-accepted `d335c2ed` bookkeeping. Why it matters: a wave-5 task is a real cost;
if the executor drops t-13, the scoped Stryker run must still be observed before the last
web commit.

**D10 — Login.svelte must enter the verify diff (mvp only).**
Risk and verification treat Login as "unchanged lines" and add tests only. Verified: dross
verify mutates only files in the phase diff (phase 03's report has no Login.svelte), so
those four keys could not be reported killed. Provisional default: **mvp's
`autocapitalize="none" spellcheck="false"` on the username input** — a genuine phone-UI
change, not a whitespace touch. Not a disagreement the others argued; recorded because
the merged plan would fail c-7 without it.

**D11 — `/api/v2/x` never gets HTML: mount the API chain at `/api/` (mvp) vs the static handler refuses `/api/` and `/healthz` prefixes (risk, verification).**
mvp's one-line mount change makes every `/api/*` miss answer from the API mux. Risk and
verification keep `/api/v1/` and make `spa.Handler` return 404 JSON for the prefixes.
Provisional default: **static handler refuses** (2-of-3; no change to the API chain's
existing 401-before-404 behaviour, and a future route typo such as `/apis` is caught by the
same rule). Both are tested at the binary level in t-11.

**D12 — Static handler and build plumbing details.**
(a) A missing path with a file extension: 404 (verification) vs `index.html` (risk, mvp).
Default **404** — a stale hashed asset must never be answered with HTML that the browser
then parses as JS. (b) Keeping `web/dist/.gitkeep` alive: `pnpm build && touch
dist/.gitkeep` (risk) vs `emptyOutDir: false` + `rm -rf dist/assets` (verification).
Default **risk** — the build stays clean-slate and `vite.config.ts` is left to t-6, which
removes the wave-1 file conflict in verification's graph. (c) Which Make target depends on
`build-web`: `test-go` (risk, mvp) vs `test` (verification). Default **`test-go`**, so the
Go suite alone never sees an unbuilt dist. Also: `web/.gitignore` needs its own `!dist/.gitkeep`
(only risk and mvp list it).

**D13 — Icons: a committed Go generator with a determinism test (risk) vs PNGs generated once by an uncommitted script (verification, mvp).**
Provisional default: **committed PNGs, no committed generator**, tested by PNG signature +
IHDR dimensions (verification/mvp). Why it matters: risk's `TestIcongen_Deterministic`
pins `image/png` encoder bytes across Go versions, which the stdlib does not promise; the
installability check needs real PNGs, not reproducible ones. If the mark ever changes, the
PNGs are regenerated by hand and the IHDR test still guards size and format.

**D14 — Extend amount clamped to `MAX_DURATION` client-side (risk only).**
The locked rule (ends − started) exceeds 24 h after one extension of a 24 h grant, which the
server 422s; mvp and verification post the raw span. Provisional default: **clamp** (risk)
— it keeps the control working without touching the server bound; recorded because it
slightly bends the locked wording "adds the grant's own duration".

**D15 — Validation details: label bound and code, message format, body cap.**
Label 1–60 chars → 422 (risk), 1–64 runes → 422 (verification), 1–64 runes → 400 (mvp).
Messages: `button 2: …` (risk), `buttons[i].field: …` (verification), `button N: unknown
child M` (mvp). Cap: 16 KiB via `decodeGrantBody` (risk, matches the code) vs 64 KiB (mvp,
verification — not what `decodeGrantBody` does). Defaults: **64 runes, 422, `buttons[i].field:`
prefix, reuse `decodeGrantBody` at 16 KiB** (≈100 buttons; a bigger cap means a new
decoder for no present need).

**D16 — GrantForm contract: bindable value + `valid` with the label in the page (risk, verification) vs `onSubmit` callback with optional label and submit button inside (mvp); services in tick order (risk, verification) vs catalogue order (mvp).**
Provisional default: **bindable fieldset, tick order**. Why it matters: tick order is what
makes "first service picks the icon" (locked `button_icon`) a parent's choice rather than
the catalogue's; mvp's self-contained form is tidier but would put the label field into
the ad-hoc form where c-8 has none.
