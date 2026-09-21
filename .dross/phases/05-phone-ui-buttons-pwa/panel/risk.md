# Risk-lens plan — 05-phone-ui-buttons-pwa

Lens: every task exists because a specific thing can break, and exactly one task owns
proving it doesn't. The graph is shaped by failure modes, not by tech layers.

Failure-mode inventory that drove the decomposition (each is owned by one task below):

| # | What breaks | Owner |
|---|---|---|
| F1 | PUT /buttons half-applied (old rows gone, new rows rejected) | t-1 |
| F2 | Button order or services order lost (first service = icon, locked `button_icon`) | t-1 |
| F3 | Orphan button after its child is deleted → tap 404s forever | t-1 |
| F4 | Child deleted between validation and insert (TOCTOU) | t-1 |
| F5 | Two PUTs interleave into a merged list | t-1, t-5 |
| F6 | `go test ./...` / `go vet` fail to compile when web/dist is absent (`//go:embed` no match) | t-2 |
| F7 | Binary built without a web build serves a confusing 404 instead of saying so | t-2 |
| F8 | SPA fallback answers HTML on an API-looking path, or lists a directory | t-2 |
| F9 | index.html / sw.js cached by the browser → deploy never picked up; hashed assets not immutable | t-2 |
| F10 | `.webmanifest` served with a platform-dependent Content-Type | t-2 |
| F11 | SVG-only icons fail Chrome's installability check | t-3 |
| F12 | Manifest / index.html / registration drift apart | t-3, t-6 |
| F13 | Service worker caches /api/* (stale grants, cached 401) | t-6 |
| F14 | SW precache list hand-written → hashed asset names wrong after every build | t-6 |
| F15 | Old shell served forever after a deploy (cache name never changes) | t-6 |
| F16 | SW registered under `vite dev` → HMR broken | t-6 |
| F17 | Duration seconds vs minutes mixed up (60× error) | t-4, t-8 |
| F18 | Extend of an already-extended 24 h grant → 422 (own duration > max) | t-4 |
| F19 | Poll keeps running after unmount / logout; overlapping polls | t-7 |
| F20 | Poll failure blanks the panel (locked `unreachable_countdown`) | t-7 |
| F21 | Backgrounded phone tab: throttled interval, stale on return | t-7 |
| F22 | Countdown decrements a counter instead of recomputing from ends_at → drifts | t-9 |
| F23 | Grant removed from the panel by the client clock instead of by the server list | t-9 |
| F24 | End-early 502 shown as success | t-9 |
| F25 | Double tap → second POST → spurious 409 offer | t-12 |
| F26 | 201 with applied=false swallowed | t-12 |
| F27 | 409 offer computed from a stale list; extend on an expired grant (404) dead-ends the tap | t-12 |
| F28 | Multi-service overlap under-delivers (locked `overlap_offer`) | t-12 |
| F29 | Home unusable when AdGuard is down (icons/blocked views need AdGuard; grants/buttons do not) | t-12 |
| F30 | Entry not visible within 5 s because the follow-up GET failed | t-7, t-12 |
| F31 | Settings 422 wipes the parent's draft | t-10 |
| F32 | Button referencing a service AdGuard no longer lists silently dropped | t-8, t-10 |
| F33 | Survivor kills not credited by Stryker (attribution) | t-13 |

---

```
Phase 05-phone-ui-buttons-pwa — 13 tasks across 5 waves

Wave 1
  t-1  Buttons schema and atomic replace store
       files:    internal/store/migrations/0004_buttons.sql, internal/store/buttons.go,
                 internal/store/buttons_test.go
       covers:   c-1
       depends:  —
       description:
         0004_buttons.sql: buttons(id INTEGER PRIMARY KEY AUTOINCREMENT, position INTEGER NOT NULL
         UNIQUE, label TEXT NOT NULL, child_id INTEGER NOT NULL REFERENCES children(id) ON DELETE
         CASCADE, duration INTEGER NOT NULL /* seconds */); button_services(button_id REFERENCES
         buttons(id) ON DELETE CASCADE, position INTEGER NOT NULL, service_id TEXT NOT NULL,
         PK (button_id, service_id)). AUTOINCREMENT so an id is never reused across PUTs (a stale
         Home never mis-keys a button). FK cascade because a button has no reason to outlive its
         child (unlike grants, locked client_set). buttons.go: Button{ID, ChildID int64; Label string;
         Services []string (given order, deduped, never nil); Duration time.Duration}; typed
         *ErrUnknownChild{ChildID}. ListButtons(ctx) position ASC, services by position, two
         queries each scanned to completion (single-connection rule), empty → []Button{}.
         ReplaceButtons(ctx, in []Button) ([]Button, error): ONE tx — DELETE FROM buttons; per
         item SELECT 1 FROM children WHERE id=? → *ErrUnknownChild (rollback, old list intact);
         INSERT with position = index; returns the stored list with fresh ids. Incoming IDs are
         ignored.
       contract:
         - if the replace is not one transaction (F1), TestButtons_ReplaceAtomic fails: list [A,B]
           stored; ReplaceButtons([C, D{ChildID: 999}]) → *ErrUnknownChild{999} and ListButtons
           still returns [A,B] with their original ids
         - if order is taken from rowid instead of position (F2), TestButtons_OrderPersists fails:
           Replace [C,A,B] → List returns labels [C,A,B]; Close, Open same dir → identical list;
           schema_migrations lists 0004_buttons.sql exactly once
         - if services are sorted or a duplicate keeps the wrong slot (F2 / locked button_icon),
           TestButtons_ServicesOrderKept fails: Services ["youtube","tiktok","youtube"] reads back
           ["youtube","tiktok"]; ["tiktok","youtube"] reads back in that order
         - if the FK cascade is missing or foreign_keys is off (F3), TestButtons_ChildCascade fails:
           buttons for ada and ben; DeleteChild(ada) → (true, nil) and List holds only ben's
           button; List on an empty store returns []Button{}, never nil; Services never nil
         - if the child check is outside the tx (F4), TestButtons_ChildCheckInTx fails: a child
           deleted on another goroutine between two Replace calls yields *ErrUnknownChild, never a
           raw "FOREIGN KEY constraint failed"
         - if ids are reused (AUTOINCREMENT lost), TestButtons_FreshIDs fails: after Replace [A]
           (id 1) then Replace [B], B.ID > 1
         - if two replaces interleave (F5), TestButtons_ReplaceRace fails under go test -race:
           10 goroutines each Replace a distinct 3-item list while 5 goroutines List for 200 ms →
           every List result equals one submitted list exactly (labels and services), never a mix,
           and finishes within 5 s

  t-2  Embedded SPA handler with safe fallback and caching
       files:    web/embed.go, internal/spa/spa.go, internal/spa/spa_test.go, Makefile, .gitignore,
                 web/.gitignore, web/dist/.gitkeep
       covers:   c-5
       depends:  —
       description:
         web/embed.go: package web, `//go:embed all:dist`, func Dist() fs.FS (fs.Sub). A committed
         web/dist/.gitkeep (root .gitignore: web/dist/* + !web/dist/.gitkeep; web/.gitignore:
         dist/* + !dist/.gitkeep) keeps the embed pattern matching when nothing is built, so
         go vet / go test compile on a fresh clone (F6). Makefile: build-web runs `pnpm build &&
         touch dist/.gitkeep` (vite empties outDir); test-go depends on build-web so the binary
         tests always see a real index.html; lint/fmt exclude ./web/node_modules instead of all of
         ./web so embed.go is gofmt-checked. internal/spa: Handler(fsys fs.FS) http.Handler —
         GET/HEAD only (405 otherwise); path with /api/ or /healthz prefix → 404 JSON
         {error:not_found} never HTML (F8); dotfiles and directories → treated as missing; an
         existing file → served via http.ServeFileFS with Cache-Control: `no-cache` for /, /index.html,
         /sw.js, /manifest.webmanifest; `public, max-age=31536000, immutable` under /assets/;
         `public, max-age=3600` otherwise; Content-Type application/manifest+json forced for
         .webmanifest (F10); missing file → index.html with no-cache (SPA fallback); index.html
         itself absent → 503 text "frontend not built: run make build-web" (F7). No directory
         listing ever.
       contract:
         - if the fallback serves the wrong thing (F8), TestSPA_Fallback fails: MapFS{index.html,
           assets/app-abc.js}; GET /buttons and /children/7 → 200 text/html body == index.html
           with Cache-Control no-cache; GET /assets/app-abc.js → 200 immutable; GET /assets/ → 200
           index.html (not a listing, body has no "app-abc.js" link); GET /.gitkeep → index.html
         - if an API-looking path falls through to HTML (F8), TestSPA_NeverHTMLForAPI fails:
           GET /api/v2/x and /api/v1/ and /healthz → 404 application/json {"error":"not_found"},
           body never contains "<html"
         - if the missing-build path is silent (F7), TestSPA_NotBuilt fails: MapFS{".gitkeep"} → GET /
           is 503 with body containing "make build-web"; MapFS{} same
         - if caching headers regress (F9), TestSPA_CacheHeaders fails: /sw.js and
           /manifest.webmanifest → no-cache; /icons/icon-192.png → max-age=3600; a HEAD carries the
           same headers and no body; POST / → 405
         - if the manifest type is left to the platform (F10), TestSPA_ManifestType fails:
           Content-Type for /manifest.webmanifest == "application/manifest+json" with the file
           present in MapFS
         - if the embed pattern breaks on an unbuilt tree (F6), TestEmbed_CompilesWithoutBuild
           (in internal/spa, using web.Dist()) fails to even compile; at runtime it asserts
           Dist() opens and either contains index.html or only .gitkeep

  t-3  Web manifest, PNG icons and shell metadata
       files:    web/public/manifest.webmanifest, web/public/icons/icon-192.png,
                 web/public/icons/icon-512.png, web/tools/icongen/main.go, web/index.html,
                 web/pwa_test.go
       covers:   c-6
       depends:  —
       description:
         manifest.webmanifest: name "adguard-reward", short_name "Reward", id "/", start_url "/",
         scope "/" (locked pwa_scope), display "standalone", background_color/theme_color, icons
         [{src:/icons/icon-192.png, sizes:192x192, type:image/png}, {…512…}] — PNG because Chrome's
         installability check does not accept SVG-only icon sets (F11). web/tools/icongen: stdlib
         image/draw + image/png program that renders the mark (rounded square + check glyph) at
         192 and 512 into web/public/icons; PNGs are committed; `go run ./web/tools/icongen`
         regenerates. index.html: <title>adguard-reward</title>, <link rel="manifest"
         href="/manifest.webmanifest">, <meta name="theme-color">, <link rel="apple-touch-icon"
         href="/icons/icon-192.png">, viewport unchanged. pwa_test.go (package web, reads
         public/ and index.html relative to the package dir): the cross-file consistency check
         (F12).
       contract:
         - if the manifest drops a field Chrome requires (F11/F12), TestManifest_Installable fails:
           parses public/manifest.webmanifest; name non-empty, start_url == "/", scope == "/",
           display == "standalone", icons contain sizes "192x192" and "512x512" with type image/png
           and src under /icons/
         - if an icon file drifts from the manifest (F11), TestManifest_IconsExist fails: every
           icons[].src exists under public/ and image.DecodeConfig reports exactly the declared
           width/height and format "png"
         - if index.html forgets the manifest link (F12), TestIndex_LinksManifest fails: index.html
           contains rel="manifest" href="/manifest.webmanifest", a theme-color meta, and a <title>
           that is not "web"
         - if the generator and the committed PNGs diverge, TestIcongen_Deterministic fails:
           running the generator's Render(192) in-test yields bytes equal to public/icons/icon-192.png

  t-4  API client: buttons, grants, route, pure helpers
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/lib/grants.ts,
                 web/src/lib/grants.test.ts
       covers:   c-1, c-2, c-3, c-4, c-8
       depends:  —
       description:
         api.ts: Route gains 'buttons' (PATHS.buttons = '/buttons'); ApiError gains readonly
         grantId?: number parsed from a numeric grant_id in the envelope (409); types Grant{id,
         child_id, services, clients, started_at, ends_at}, GrantCreated{id, ends_at, applied,
         failed}, Button{id, label, child_id, services, duration /* seconds */}, ButtonInput (Button
         without id); listGrants(), createGrant(child_id, services, duration), extendGrant(id,
         duration), endGrant(id), listButtons(), saveButtons(list) (PUT {buttons: list} → returns
         the stored list). grants.ts (pure, node env — best mutation attribution): MIN_DURATION=60,
         MAX_DURATION=86400 (mirror grants.go); remainingSeconds(endsAt, nowMs) = max(0, ceil);
         formatCountdown(s) → "h:mm:ss" above an hour else "m:ss"; formatDuration(s) → "1 h",
         "30 min", "1 h 30 min"; ownDuration(g) = clamp(round((ends−started)/1000), MIN, MAX)
         (locked extend_amount, bounded so a twice-extended 24 h grant still extends — F18);
         overlapsFor(childId, services, grants) → {overlapping: Grant[] (same child, any shared
         service, id ASC), remaining: string[] (services in none of them, input order)};
         minutesToSeconds / secondsToMinutes (F17).
       contract:
         - if an endpoint's URL, method or body drifts, api.test 'grants and buttons endpoints hit
           the exact URL' fails: createGrant(1, ['tiktok'], 1800) → POST /api/v1/grants body
           {"child_id":1,"services":["tiktok"],"duration":1800} with X-Requested-With;
           extendGrant(4, 600) → POST /api/v1/grants/4/extend {"duration":600}; endGrant(4) → POST
           /api/v1/grants/4/end resolving undefined on 204; listGrants() unwraps .grants;
           listButtons() unwraps .buttons; saveButtons([b]) → PUT /api/v1/buttons {"buttons":[b]}
           and returns the response's .buttons
         - if the 409 grant id is dropped, api.test '409 carries grantId' fails: a 409
           {error:conflict, message, grant_id: 7} rejects with ApiError.status 409, code
           'conflict', grantId 7; a 409 without grant_id → grantId undefined; a string grant_id →
           undefined
         - if the buttons route is unmapped, api.test 'maps the buttons route both ways' fails:
           navigate('buttons') → pathname '/buttons' and get(route) 'buttons'; popstate at
           '/buttons' → 'buttons'
         - if the countdown is computed from anything but ends_at (F22 precondition),
           grants.test 'remainingSeconds' fails: ends_at = now+90.2 s → 91; now+0 → 0; now−5 s → 0
           (never negative)
         - if formatting is wrong, grants.test 'formatCountdown' fails: 3661 → "1:01:01", 59 →
           "0:59", 600 → "10:00", 0 → "0:00", 86400 → "24:00:00"; 'formatDuration': 3600 → "1 h",
           1800 → "30 min", 5400 → "1 h 30 min", 60 → "1 min"
         - if own-duration is unclamped (F18), grants.test 'ownDuration' fails: a grant spanning
           1 h → 3600; spanning 48 h → 86400; spanning 10 s → 60
         - if overlap resolution is wrong (F28 precondition), grants.test 'overlapsFor' fails:
           grants A(child 1,[youtube]), B(child 1,[tiktok]), C(child 2,[youtube]); child 1
           [youtube,tiktok,roblox] → overlapping [A,B], remaining [roblox]; child 1 [roblox] → [],
           [roblox]; child 2 [youtube] → [C], []
         - if minutes/seconds conversion regresses (F17), grants.test 'minutes' fails:
           minutesToSeconds(90) → 5400; secondsToMinutes(5400) → 90; secondsToMinutes(90) → 2
           (rounded, never a fraction in the form)

Wave 2
  t-5  Buttons HTTP endpoints, validation, Deps wiring
       files:    internal/api/buttons.go, internal/api/buttons_test.go, internal/api/api.go,
                 internal/api/login_test.go, cmd/adguard-reward/main.go
       covers:   c-1
       depends:  t-1
       description:
         api.go: ButtonStore interface (ListButtons, ReplaceButtons), Deps.Buttons, routes
         GET/PUT /api/v1/buttons behind requireSession. buttons.go: view {id, label, child_id,
         services, duration} (seconds, never-null services); GET → {buttons:[…]}; PUT body
         {buttons:[{id?, label, child_id, services, duration}]} decoded like decodeGrantBody (16 KiB,
         no unknown fields, id accepted and ignored). Validation cheapest-first, naming the item
         index in every 422 ("button 2: …"): label trimmed, 1–60 chars; duration via checkDuration
         (shared bounds); services trimmed/deduped in given order, empty → 422; then every distinct
         child_id via Children.GetChild (unknown → 422, before any AdGuard call); then ONE
         AdGuard.Services read (unknown id → 422 naming it; unreachable → 502, nothing stored);
         then store.ReplaceButtons (*ErrUnknownChild → 422 — the in-tx re-check). 200 returns the
         stored list. login_test.go newHarness passes Buttons: st; main.go passes Buttons: st so
         the binary never nil-derefs between this task and t-11.
       contract:
         - if a route escapes the gates, TestButtons_Gated fails: GET and PUT without a cookie →
           401; PUT with a cookie but no X-Requested-With → 403 and ListButtons unchanged
         - if the wire shape drifts, TestButtons_RoundTrip fails: PUT [{"Ada YouTube 1 h",ada,
           ["youtube","tiktok"],3600},{…ben…}] → 200 whose buttons have ids > 0, that label order and
           services in the given order; GET body byte-equal to the PUT response; PUT {"buttons":[]}
           → 200 {"buttons":[]}; raw bodies never contain :null
         - if validation or its order drifts, TestButtons_Validation fails: label "" and "   " →
           422 containing "button 1"; 61-char label → 422; duration 59 and 86401 → 422; 60 and
           86400 → 200; services [] and [""] → 422; child_id 999 → 422 naming 999 with zero
           /control/blocked_services/all requests; services ["nope"] → 422 containing "nope"; a
           body with both an unknown child and an unknown service → 422 naming the child; unknown
           field / bare array / 20 KiB → 400; after every non-200 the GET equals the GET taken
           before the call (F1 at the API layer)
         - if an unreadable catalogue stores anything, TestButtons_CatalogueDown fails: SetStatus
           (/control/blocked_services/all, 500) → 502 adguard_unavailable, list unchanged; GET
           /buttons issues zero catalogue requests
         - if the id is trusted from the body, TestButtons_IDIgnored fails: PUT with id 42 → the
           stored id is not 42 and a second PUT of the same body yields larger ids
         - if the cascade is not visible through the API (F3), TestButtons_ChildDeleteCascades
           fails: buttons for ada and ben; DELETE /children/{ada} → 204; GET /buttons lists only
           ben's
         - if concurrent PUTs merge (F5), TestButtons_ConcurrentPut fails under go test -race: 8
           goroutines PUT 8 distinct lists → all 200; the final GET equals exactly one of the 8
           bodies

  t-6  Service worker: routing module, precache build step, registration
       files:    web/src/sw.ts, web/src/lib/sw-routing.ts, web/src/lib/sw-routing.test.ts,
                 web/vite.config.ts, web/src/main.ts
       covers:   c-6
       depends:  t-3
       description:
         sw-routing.ts (pure, node env): PUBLIC_SHELL = ['/', '/manifest.webmanifest',
         '/favicon.svg', '/icons/icon-192.png', '/icons/icon-512.png']; decide({method, url,
         mode, origin}, precache: Set<string>) → 'bypass' (non-GET, cross-origin, path starting
         /api/ or /healthz, or /sw.js) | 'shell' (mode navigate → network-first, fall back to
         cached '/') | 'asset' (path in precache → cache-first) | 'bypass' otherwise (F13);
         cacheName(precache) = 'shell-' + FNV-1a hex of the sorted list (F15). sw.ts: install →
         cache.addAll(self.__PRECACHE__) into cacheName; activate → delete every other cache,
         clients.claim(); fetch → decide(); never cache.put anything outside precache. vite.config.ts:
         second rollup input sw: src/sw.ts; output.entryFileNames puts that chunk at /sw.js
         (unhashed); an inline `precache` plugin in generateBundle replaces the literal
         `self.__PRECACHE__` in the sw chunk with JSON of every emitted chunk/asset fileName except
         sw.js plus PUBLIC_SHELL (F14), and throws if the sw chunk still contains `import ` /
         `export ` (must be a classic script) or the token was not found. main.ts: register
         '/sw.js' only when import.meta.env.PROD && 'serviceWorker' in navigator (F16); main.ts
         stays `Stryker disable all`, sw.ts gets the same header (event glue; the logic under
         mutation is sw-routing.ts).
       contract:
         - if /api ever reaches the cache (F13), sw-routing.test 'API and non-GET always bypass'
           fails: GET /api/v1/grants, GET /api/v1/buttons, POST /, GET /healthz, GET /sw.js and a
           cross-origin GET each → 'bypass' regardless of precache contents; a navigate to
           /api/v1/x → 'bypass'
         - if navigations stop falling back to the shell, 'navigations are shell' fails: GET
           mode navigate to /, /buttons, /children/3 → 'shell'; a non-navigate GET of /children →
           'bypass'
         - if precached assets are not cache-first, 'precached assets are cache-first' fails:
           /assets/app-abc.js in the set → 'asset'; /assets/other.js not in the set → 'bypass';
           /manifest.webmanifest → 'asset'
         - if the cache name stops tracking the build (F15), 'cacheName changes with the list'
           fails: two lists differing in one hash → different names; same list in another order →
           same name
         - if PUBLIC_SHELL drifts from public/ (F12), 'PUBLIC_SHELL entries exist' fails: for every
           entry except '/', readdirSync-based existence under web/public
         - if the build step regresses (F14), 'vite build emits sw.js with a precache list'
           (sw-routing.test, spawns `pnpm build` into a temp outDir via vite's build API with
           build.outDir overridden) fails: dist/sw.js exists, contains no `__PRECACHE__`, contains
           no `import `, and every listed path except '/' exists in the temp dist

  t-7  Active-grants store: poll, merge, reachability
       files:    web/src/lib/activeGrants.ts, web/src/lib/activeGrants.test.ts
       covers:   c-2, c-3
       depends:  t-4
       description:
         Svelte store module: activeGrants = readable {grants: Grant[]; unreachable: boolean;
         loaded: boolean}. refresh(): a call while one is in flight returns the same promise (no
         overlap); success → grants replaced, unreachable=false, loaded=true; ApiError 401 →
         state untouched (the api layer routes to login; a session lapse is not "unreachable");
         any other failure → unreachable=true, grants kept as they were (locked
         unreachable_countdown, F20). merge(g: Grant): upsert by id (F30 — a 201 is server truth,
         not optimism). remove(id). start(intervalMs = 15000): immediate refresh, setInterval,
         document visibilitychange → refresh when visible (F21); returns stop; a second start
         while started is a no-op returning the same stop; stop clears the interval and listener
         (F19). onUnauthorized untouched.
       contract:
         - if polling overlaps or runs after stop (F19), 'polls on the interval and stops' fails
           (fake timers): start(15000) → one GET immediately; +45 s → exactly 4 GETs; a GET that
           never resolves while +30 s elapse → still 2 GETs total; stop(); +60 s → no further GET;
           start twice → one interval (GET count as for one)
         - if a failed poll blanks the list (F20), 'failure keeps grants and sets unreachable'
           fails: first poll returns [g1]; second rejects with TypeError → grants still [g1],
           unreachable true; third succeeds → unreachable false and grants from the response
         - if a 401 is mislabelled, '401 is not unreachable' fails: a 401 → unreachable stays
           false, grants unchanged, and the onUnauthorized spy was called once
         - if a backgrounded tab never catches up (F21), 'visibilitychange refreshes' fails:
           with the interval far away, dispatching visibilitychange with visibilityState
           'visible' → one extra GET; 'hidden' → none
         - if merge is not an upsert (F30), 'merge upserts by id' fails: merge(g2) on [g1] →
           [g1,g2]; merge(g1') (same id, later ends_at) → [g1',g2] with one entry for that id;
           remove(2) → [g1']; the next successful poll replaces everything

  t-8  GrantForm: child, services, duration fieldset
       files:    web/src/lib/GrantForm.svelte, web/src/lib/GrantForm.test.ts
       covers:   c-4, c-8
       depends:  t-4
       description:
         Presentational fieldset shared by the ad-hoc form (Home) and the settings page (Buttons)
         so validation lives once. Props: children: Child[], services: Service[] | null (null =
         catalogue unavailable), value: {child_id: number | null, services: string[], minutes:
         number} ($bindable), disabled. Renders a child <select aria-label="Child">, one checkbox
         per catalogue service (icon via iconUrl, name), a minutes <input type=number
         aria-label="Minutes" min=1 max=1440>. Exposes valid ($bindable, derived): child chosen,
         services non-empty, 1 ≤ minutes ≤ 1440 and integer. A value.services id that is not in the
         catalogue renders as a checked row labelled with the raw id and a "not in AdGuard Home"
         hint, still unticks like any other, and is never dropped silently (F32). services null →
         a "service list unavailable" note, no checkboxes, valid false unless value.services is
         already non-empty (a stored button can still be saved as-is).
       contract:
         - if the bound value drifts from the DOM, 'binds child, services and minutes' fails:
           select Ada, tick YouTube and TikTok, type 90 → value {child_id:1, services:
           ['youtube','tiktok'] in tick order, minutes 90}; untick YouTube → ['tiktok']
         - if the bounds regress (F17), 'valid tracks the bounds' fails: minutes 0 → valid false;
           1 → true; 1440 → true; 1441 → false; 1.5 → false; no child → false; no services → false
         - if an unknown stored service is silently dropped (F32), 'unknown service id is kept
           and marked' fails: value.services ['gone'] with catalogue [youtube] → a checked row with
           text containing "gone" and "not in AdGuard Home"; value.services still ['gone'];
           unticking it → []
         - if disabled does not propagate, 'disabled greys every control' fails: disabled true →
           select, every checkbox and the minutes input have disabled === true
         - if catalogue-down blocks editing existing buttons (F29), 'null catalogue' fails:
           services null with value.services ['youtube'] → note visible, valid true; with [] →
           valid false

Wave 3
  t-9  ActiveGrants panel: countdown, extend, end, badge
       files:    web/src/lib/ActiveGrants.svelte, web/src/lib/ActiveGrants.test.ts
       covers:   c-3
       depends:  t-7
       description:
         Reads activeGrants; props childNames: Map<number,string>, serviceNames: Record<string,
         string> (fallback: raw id). <section aria-label="Active grants"> with data-badge=
         "unreachable" badge when store.unreachable; empty → "Nothing is unlocked right now". Per
         grant <li data-grant={id}>: child name, service names, <time data-countdown> from
         formatCountdown(remainingSeconds(ends_at, now)), buttons "Extend {formatDuration(
         ownDuration(g))}" and "End". One setInterval(1000) updates `now` for the whole list;
         onDestroy clears it. Countdown is recomputed from ends_at every tick, never decremented
         (F22). When a grant's remaining hits 0 it shows "0:00" and triggers ONE refresh() for that
         id (Set guard); the entry leaves only when the server list no longer has it (F23).
         Extend → extendGrant(id, ownDuration(g)) then refresh(); 404 → refresh() silently (already
         gone); other error → <p role="alert"> inside that li with messageFor. End → endGrant(id)
         then refresh(); 502 → alert with the server message (names the clients), entry stays
         (F24); 404 → refresh() silently.
       contract:
         - if the countdown decrements instead of recomputing (F22), 'countdown follows ends_at'
           fails (fake timers + setSystemTime): grant ends in 10 min → "10:00"; advance 1 s →
           "9:59"; setSystemTime(+5 min) without running timers, then advance 1 s → "4:58"
         - if removal is driven by the client clock (F23), 'expiry asks the server' fails: at
           0:00 exactly one extra GET /api/v1/grants; while the mock still lists the grant it stays
           rendered at "0:00" and 5 more ticks add no further GET; when the mock drops it, the li
           disappears
         - if extend uses the wrong amount (locked extend_amount / F18), 'extend posts own
           duration' fails: started/ends 1 h apart → button text "Extend 1 h" and POST body
           {"duration":3600}; 48 h apart → {"duration":86400}; then GET is re-issued and the new
           ends_at is reflected in the countdown
         - if end swallows a failed revert (F24), 'end 502 keeps the entry' fails: end → 502
           adguard_unavailable "could not re-block Kid phone" → alert inside li[data-grant] with text
           containing "Kid phone", li still present, no extra GET; end → 204 → GET re-issued and
           the mock's list without it empties the panel
         - if a stale control dead-ends, 'extend 404 refreshes silently' fails: extend → 404 →
           no alert, one GET, li gone when the mock omits it
         - if the badge is missing or blanks the list (F20), 'unreachable badge' fails: poll
           rejects → [data-badge="unreachable"] visible, every li still present and the countdown
           still ticks (advance 1 s → decremented text); next success → badge gone
         - if the ticker leaks (F19), 'unmount stops ticking' fails: unmount(); advance 60 s →
           no GET and no thrown error (setInterval count via vi.getTimerCount() is 0)
         - if names fall back wrongly, 'names' fails: childNames has 1→Ada; serviceNames lacks
           'gone' → li text contains "Ada", "YouTube" and "gone"

  t-10 Buttons settings page and route
       files:    web/src/lib/Buttons.svelte, web/src/lib/Buttons.test.ts, web/src/App.svelte,
                 web/src/App.test.ts
       covers:   c-4
       depends:  t-8
       description:
         Buttons.svelte props {onBack}. Load: Promise.all(listButtons, listChildren) plus an
         optional listServices (failure → services null, page still editable, F29). Drafts: one row
         per button {label, child_id, services, minutes} built from the server (secondsToMinutes);
         "Add button" appends a blank row; per-row "Delete" removes it from the draft; ONE "Save"
         button (disabled while pending or any row invalid) → saveButtons(drafts mapped with
         minutesToSeconds) → drafts rebuilt from the response (server truth, fresh ids). A 422 or
         any failure → <p role="alert" data-error> with messageFor; drafts are KEPT so the parent
         fixes the row instead of losing it (F31 — deliberately unlike Children's reload-on-error).
         Unknown service ids flow through GrantForm's marking (F32). App.svelte: 'buttons' route →
         <Buttons onBack>; refresh() already keeps a reload on /buttons there.
       contract:
         - if the round trip mangles units or order (F17), 'edit and save' fails: GET returns
           [{id:3, label:"YouTube", child_id:1, services:["youtube"], duration:5400}] → row shows
           minutes 90; change label to "YT", tick TikTok, minutes 45, Save → PUT body
           {"buttons":[{"label":"YT","child_id":1,"services":["youtube","tiktok"],"duration":2700}]}
           (no id sent) and the rows re-render from the response
         - if add/delete are wrong, 'add and delete' fails: Add → a new empty row; Save disabled
           while it is invalid; fill it → enabled; Delete on the first row → PUT carries only the
           second
         - if a 422 wipes the draft (F31), '422 keeps the draft' fails: PUT → 422 "button 1:
           unknown service \"gone\"" → alert with that text, the row's label/minutes unchanged, no
           GET re-issued
         - if catalogue-down blocks the page (F29), 'services 502 still lists buttons' fails:
           /api/v1/services → 502 → rows render with the "service list unavailable" note; Save of
           an untouched list is still possible and PUTs the stored services
         - if the route is unwired, App.test 'reload on /buttons stays there' fails:
           replaceState('/buttons'), route.set('buttons'), /me 200 → heading "Buttons", pathname
           '/buttons'; 'buttons needs a session': /me 401 → login page, no /api/v1/buttons call;
           'Back from Buttons returns home' → pathname '/'

  t-11 Wire SPA into main; binary-level SPA, PWA and restart proofs
       files:    cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go
       covers:   c-1, c-4, c-5, c-6
       depends:  t-2, t-3, t-5, t-6
       description:
         main.go: mux.Handle("/", spa.Handler(web.Dist())) after /healthz and /api/v1/ (Go's mux
         prefers the longer pattern, so the API and liveness routes are untouched). main_test.go
         runs against the real embed (test-go depends on build-web, so an unbuilt tree fails
         loudly with the 503 body rather than skipping — F7). Manual step recorded in the verify
         notes: Chrome on Android against the TLS listener shows the install prompt / Lighthouse
         "installable" (HTTPS is a precondition the tests cannot supply).
       contract:
         - if the SPA is not served from the binary (c-5), TestRun_SPAFallback fails: GET /buttons
           and /children/7 with no cookie → 200 text/html, body contains `<div id="app">` and
           `rel="manifest"`, Cache-Control no-cache; the hashed /assets/*.js referenced by that body
           → 200 immutable; GET /api/v1/nothing with a cookie → 404 JSON not_found; without → 401;
           GET /api/v2/x → 404 JSON never HTML; GET /healthz unchanged (existing test still green)
         - if the PWA files are not reachable or mistyped (c-6), TestRun_PWAServed fails:
           /manifest.webmanifest → 200 application/manifest+json parsing to start_url "/", scope "/",
           display standalone, two png icons each GET → 200 image/png decoding to 192² / 512²;
           /sw.js → 200 with a JavaScript Content-Type, no-cache, body without `__PRECACHE__` and
           without "/api/"; every path in the precache JSON embedded in sw.js GETs 200 from the
           same binary
         - if the API ever becomes cacheable via the SPA layer, TestRun_APINoStore fails: GET
           /api/v1/buttons with a cookie → Cache-Control no-store (unchanged), and the same path
           via HEAD is not HTML
         - if buttons do not survive a restart (c-1, c-4), TestRun_ButtonsSurviveRestart fails:
           login, POST child, PUT two buttons, stop (exit 0), start on the same data_dir, GET
           /buttons with the old cookie → body byte-equal to the first run's GET (ids, order,
           services order, durations)

Wave 4
  t-12 Home: buttons per child, tap, 409 offer, ad-hoc form, disclosure
       files:    web/src/lib/Home.svelte, web/src/lib/Home.test.ts
       covers:   c-2, c-3, c-8
       depends:  t-7, t-8, t-9
       description:
         Layout per locked home_layout: nav (Children, Buttons, Log out — signOut/pending kept
         verbatim), MigrationBanner, <ActiveGrants>, then per child: h2, its buttons in stored
         order (<button data-button={id}> with the first service's icon via iconUrl when the
         catalogue loaded, label, formatDuration), a <details data-blocked={child.id}>
         "Blocked services" whose childBlocked() fires on first open only, with its own error line
         inside (F29: AdGuard down never blocks the tap surface); a child with no buttons shows
         "No buttons yet" with a Buttons link. Bottom: "Unlock something else" — GrantForm + an
         "Unlock" button (c-8) feeding the SAME tap() as a button. load(): Promise.all(listChildren,
         listButtons) → page-level alert on failure; listServices separately, failure → icons and
         names fall back (F29); activeGrants.start() in onMount, stop in onDestroy.
         tap(spec{key, child_id, services, duration}): key in an inflight Set → ignored and the
         control is disabled while in flight (F25, locked no_confirm: no dialog). Outcomes: 201
         applied → merge(grant built from the 201 + spec + child.clients) then refresh() (F30);
         201 applied=false → merge AND <p role="alert" data-error="partial"> "Unlocked, except on
         Kid tablet" naming failed (F26); network/5xx/422/404 → alert with messageFor next to
         that child's buttons. 409 (F27/F28): await refresh() (ignore its failure), targets =
         overlapsFor(child, services, grants).overlapping, falling back to [{id: err.grantId}] when
         empty; render <div role="dialog" data-offer> "YouTube is already unlocked for Ada —
         extend by 1 h?" with Extend / Cancel. Extend: for each target extendGrant(id, spec.
         duration) (404 → drop that target and count its services as remaining); if remaining
         non-empty → createGrant(remaining) through the same outcome handling (a second 409 shows
         a plain alert — no loop); then refresh(). Errors name what failed. onMigrated → load()
         and clear cached blocked views (open disclosures refetch).
       contract:
         - if stored order or grouping breaks (c-2), 'renders buttons per child in stored order'
           fails: buttons [b3(ben), b1(ada), b2(ada)] → section[data-child=1] holds b1 then b2,
           section 2 holds b3; a button with child_id 99 renders nowhere and nothing throws
         - if a tap is not one POST with the button's spec (locked no_confirm), 'tap posts the
           grant immediately' fails: click b1 → exactly one POST /api/v1/grants body
           {child_id:1, services:["youtube","tiktok"], duration:3600}, no dialog; 201 → li[data-grant]
           with the 201's id visible and its countdown text "60:00" before any GET resolves
           (the mock GET is left pending, so only merge can have rendered it — F30)
         - if a double tap fires twice (F25), 'double tap is one POST' fails: two clicks before
           the POST resolves → one POST, button disabled meanwhile, enabled after
         - if partial apply is swallowed (F26), 'applied=false names the client' fails: 201
           {applied:false, failed:["Kid tablet"]} → alert data-error="partial" containing "Kid
           tablet" and the grant still listed in the panel
         - if failures are swallowed, 'failed POST shows an inline error' fails: fetch TypeError →
           alert data-error="network"; 422 "unknown service \"gone\"" → alert with that text; 502
           → adguard_unavailable line; each alert sits inside that child's section
         - if the 409 does not become an offer (c-3 / F27), '409 offers extend' fails: POST → 409
           grant_id 5; the refetched list has grant 5 (ada, [youtube], ends in 42 min) → dialog
           text contains "YouTube", "Ada" and "1 h"; Cancel → dialog gone, no further request
         - if accepting under-delivers (locked overlap_offer / F28), 'accept extends and creates
           the rest' fails: button [youtube,tiktok,roblox], list has A(ada,[youtube]) and
           B(ada,[tiktok]) → Extend → POST /grants/A/extend {3600}, POST /grants/B/extend {3600},
           POST /grants {services:["roblox"]}, then GET; order of extends by id ASC
         - if a stale offer dead-ends (F27), 'expired target retries create' fails: extend → 404 →
           POST /grants with the full services list once; a second 409 there → plain alert, no
           third POST
         - if the ad-hoc form diverges from a tap (c-8), 'ad-hoc form creates a grant' fails: with
           zero buttons configured, select Ben, tick TikTok, 30 min, Unlock → POST {child_id:2,
           services:["tiktok"],duration:1800}; 201 → panel entry within the same tick; a 409 here
           opens the same dialog
         - if AdGuard-down blocks Home (F29), 'services 502 still renders buttons' fails:
           /api/v1/services → 502 → buttons render without <img>, tapping works, no page-level
           alert; the panel names services by id
         - if the disclosure fetches eagerly or hides its error (locked home_layout / F29),
           'blocked list loads on open' fails: no /children/1/blocked call at mount; open <details>
           → one call and the blocked names render inside; a 502 there → alert inside the details
           only, buttons still tappable; migration applied while open → the call is re-issued
         - if the poller outlives Home (F19), 'unmount stops polling' fails: unmount, advance
           60 s → no GET /api/v1/grants
         - if the sign-out surface regressed, the existing 'Children button routes' and log-out
           App tests still pass (kept verbatim)

Wave 5
  t-13 Kill the 14 routed Stryker survivors
       files:    web/src/lib/api.test.ts, web/src/lib/Home.test.ts, web/src/lib/Login.test.ts,
                 web/src/App.test.ts
       covers:   c-7
       depends:  t-10, t-12
       description:
         Test-only. Runs last so every kill targets the final component code. Each mutant is
         named with its present-day location (phase-02 line → today):
         api.ts:31 StringLiteral PATHS.login '/login' → today api.ts:30; api.ts:34 BlockStatement
         currentPath body → today :39-41; api.ts:43 StringLiteral navigate's '/login' → today :30
         (same literal); api.ts:44 ×4 (whole condition→true, `typeof history !== 'undefined'`→true,
         `currentPath() !== path`→true, &&→||) → today :49. Home.svelte:6/9/15 → today :8/:30/:36
         (pending initial, set true in signOut, set false in finally). Login.svelte:6/7/13/44 —
         unchanged lines. App.svelte:15 BlockStatement (refresh's catch body) → today :17-22.
         Exit gate (F33): run `pnpm exec stryker run --mutate src/lib/api.ts,src/lib/Home.svelte,
         src/lib/Login.svelte,src/App.svelte` in web/ and read reports/mutation/mutation.json —
         zero "Survived" at those lines is observed before the commit; the result is quoted in the
         commit body. Kills land in the owning component's test file so Stryker's per-test
         attribution has the best chance of crediting them.
       contract:
         - if PATHS.login or navigate's login literal is blanked (api.ts:31, :43), api.test 'maps
           the login route both ways' fails: navigate('login') → pathname '/login' and get(route)
           'login'; replaceState('/login') + popstate → 'login'; navigate('home') → '/'
         - if currentPath's body is emptied or the pushState guard is forced true / ||-ed
           (api.ts:34, :44 whole→true, `currentPath() !== path`→true, &&→||), api.test 'navigate
           does not push a duplicate entry' fails: navigate('children'); n = history.length;
           navigate('children') → history.length === n and pathname '/children'
         - if the history guard is forced true (api.ts:44 `typeof history`→true), api.test
           'navigate without a history object' fails: vi.stubGlobal('history', undefined) →
           navigate('children') does not throw and get(route) === 'children'
         - if Home's pending starts true (Home:6), Home.test 'Log out is enabled before a click'
           fails: after mount, the Log out button's disabled === false
         - if signOut does not set pending (Home:9), Home.test 'Log out is disabled while the
           request is in flight' fails: POST /logout left pending → button disabled === true
         - if finally does not clear pending (Home:15), Home.test 'Log out re-enables after the
           request' fails: onLogout is a spy (Home stays mounted); after /logout resolves 204 the
           button's disabled === false and the spy was called once; same after a rejected logout
         - if the login fields start non-empty (Login:6, :7), Login.test 'fields start empty'
           fails: both inputs' .value === '' at mount
         - if pending is not set on submit (Login:13) or the in-flight label is blanked (Login:44),
           Login.test 'shows Signing in… and disables the form while in flight' fails: POST /login
           left pending → submit button text 'Signing in…', button and both inputs disabled; after
           204 the label is 'Sign in' again
         - if refresh's catch is emptied (App:15), App.test 'a non-401 failure on /me lands on
           /login' fails: /me → 502 at '/' → Sign in button visible AND location.pathname ===
           '/login' (with the catch emptied the pathname stays '/'); same from '/buttons'
         - if any of the 14 still survives, the exit gate fails: mutation.json lists no
           "Survived" mutant for api.ts:30/:39-41/:49, Home.svelte:8/:30/:36, Login.svelte:6/7/13/44,
           App.svelte:17-22
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 | t-1 (schema, atomic replace, order), t-5 (GET/PUT, 422 cases), t-11 (survives restart against the binary), t-4 (client) |
| c-2 | t-12 (render, tap, inline errors), t-7 (merge so the entry is visible without a second round trip), t-4 |
| c-3 | t-9 (panel, countdown, extend/end, leave-on-expiry), t-7 (poll/badge), t-12 (409 → offer), t-4 |
| c-4 | t-10 (settings page, route), t-8 (form fieldset), t-11 (renders after restart: data proof), t-4 |
| c-5 | t-2 (handler + embed + build hygiene), t-11 (binary-level fallback / 404 proofs) |
| c-6 | t-3 (manifest, PNG icons, index.html), t-6 (SW never caches /api, precache build), t-2 (manifest type / no-cache headers), t-11 (served from the binary; manual phone check noted) |
| c-7 | t-13 |
| c-8 | t-12 (ad-hoc form through the same tap path), t-8 (fieldset), t-4 |

All 8 criteria accounted for. Locked decisions honoured: home_layout (t-12), extend_amount (t-4/t-9), overlap_offer (t-12), no_confirm (t-12), button_icon (t-1 order + t-12), button_order (t-1/t-10 — no reorder controls), pwa_scope (t-3), unreachable_countdown (t-7/t-9).

## Judgment calls

- **Buttons cascade on child delete (FK ON DELETE CASCADE)** over grants-style "no FK": a button that outlives its child can only ever 404 on tap; grants outlive children because the revert must still run — buttons have no such obligation. Rejected: filtering orphans at read time (leaves junk rows and a second code path).
- **Server-assigned button ids, AUTOINCREMENT, PUT ignores incoming ids** over preserving client ids: nothing on the server references a button by id (a tap POSTs child/services/duration, not a button id), so preservation buys only keyed-DOM stability at the cost of duplicate-id validation and reuse-after-delete confusion. AUTOINCREMENT costs one sqlite_sequence row and removes id reuse entirely.
- **Child-existence check both before the catalogue read and inside the store tx**: the pre-check gives a 422 with zero AdGuard calls (mirrors grants' 404-before-catalogue); the in-tx check closes the TOCTOU. Rejected: relying on the FK error string from modernc.
- **`.gitkeep` un-ignored + `test-go: build-web`** over a Go build tag or a Makefile-only stub: a bare `go test ./...` on a fresh clone must compile (embed pattern), and the binary tests must fail loudly — not skip — when dist is unbuilt. Rejected: `emptyOutDir: false` (stale hashed assets get embedded into the binary).
- **spa package refuses `/api/` and `/healthz` prefixes outright** even though the mux already routes them: a future route typo (`/api/v2`, `/apis`) must never get HTML parsed as JSON by the client.
- **Grants list drives panel removal; the client clock only drives the number**: skew can make a countdown read wrong for a few seconds, but it can never make the panel disagree with the server about which grants exist. Rejected: correcting skew from the `Date` response header (needs header access in `request()` for a cosmetic gain).
- **Own-duration clamped to MAX_DURATION client-side**: the locked rule (ends − started) exceeds 24 h after one extension of a 24 h grant, which the server 422s. Clamping keeps the control working; the server bound is untouched.
- **On 409, refresh the list before computing overlaps; fall back to the 409's grant_id; retry create once when a target 404s**: the 409 itself proves the local list was stale. No loop: a second 409 is a plain error.
- **Merge the 201 into the panel before the follow-up GET**: the 201 body is server truth (id, ends_at), so this is not optimistic state; it makes "visible within 5 s" independent of a second round trip succeeding.
- **Lazy blocked-services disclosure, optional services catalogue**: the tap surface and the panel are store-backed and must work when AdGuard is down; only the disclosure and the icons need AdGuard, so only they degrade. This changes two existing Home tests (error moves inside the disclosure). Rejected: eager per-child blocked fetches on every Home load (N AdGuard round trips before the first tap).
- **Settings 422 keeps the draft** (unlike Children's reload-on-error): the failing row is the parent's work in progress; reloading would delete exactly what they need to fix.
- **Shared GrantForm fieldset** for the ad-hoc form and the settings rows: the minutes/seconds boundary and the unknown-service marking exist once, so the two surfaces cannot drift.
- **SW navigations network-first, assets cache-first, cache name = hash of the precache list**: freshness for the shell of a tool whose correctness lives server-side; offline still opens from cache; a deploy with identical assets does not churn caches.
- **SW precache list injected by an inline vite plugin** over vite-plugin-pwa/workbox: no new dependency in a supply-chain-sensitive year; ~25 lines of config; a build-time throw if the token is missing or the chunk is ESM.
- **PNG icons from a committed stdlib Go generator** over SVG-only or hand-made binaries: Chrome's installability check needs PNG 192/512; the generator makes the binaries reproducible and a test pins them.
- **c-7 runs last and gates on an observed Stryker report**: killing survivors in files t-10/t-12 rewrite would be undone; the local scoped Stryker run is the only way to see attribution credit before verify.
- **c-6 installability from a phone is a recorded manual step**: it needs HTTPS and a real browser; the tests pin every field Chrome checks and the served headers/types, which is the automatable part.
