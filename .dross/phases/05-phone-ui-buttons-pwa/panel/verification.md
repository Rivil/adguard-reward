# Verification-lens plan — 05-phone-ui-buttons-pwa

Method: for each criterion the ideal test was written first (which surface breaks, what
the test observes), then the smallest task that makes that test satisfiable. Go API tests
run the real store and the real `adguardtest` fake through `newHarness` in
`internal/api/login_test.go`; binary-level proofs go through `run()` via `start`/`startWith`
in `cmd/adguard-reward/main_test.go`; web tests are jsdom component tests with the
URL-keyed `mockFetch` stub that `Home.test.ts` / `App.test.ts` already use. Everything that
can be a pure function (countdown formatting, the overlap plan, the SPA fallback decision,
the service-worker routing) is one, with a table test, so gremlins/Stryker have a tight
target and the locked decisions are pinned by data.

## Survivor map for c-7 (what each routed mutant is, in today's coordinates)

The 14 keys were routed from two different verify runs and the files have moved since, so
the spec's line numbers are stale. Resolved against `git show 726b280` (phase-02 tree),
phase-03 `tests.json` (same keys, new lines) and today's files:

| spec ref | key | today | mutant | killed by |
|---|---|---|---|---|
| api.ts:31 StringLiteral | 321f5af2 | api.ts:34 (`routeFor` loop) | the phase-02 ternary `'/login' ? 'login' : 'home'` was rewritten in 03; its 03-era successors at :34 are accepted. Dead key — cannot resurface. | `route` initial-value test in t-5 still pins `routeFor` for every path |
| api.ts:34 BlockStatement | 7c2c1aaf | api.ts:39-41 `currentPath` body → `{}` | returns undefined → `route` always 'home' at load; `navigate` always pushes | t-5 "initial route from URL" + "no duplicate pushState" |
| api.ts:43 StringLiteral | 04a39c82 | api.ts:30 `PATHS` literals (phase-02 `'/login' : '/'`) | a wrong path string | t-5 "navigate pushes the exact path" per route |
| api.ts:44 ConditionalExpression ×(3 listed, 1 key) | d2e9f9c6 | api.ts:49 `if (...)` → `true` | pushState even when already on the path (duplicate history entry) | t-5 "navigate to the current path calls pushState zero times" |
| api.ts:44 LogicalOperator | 47163e3c | api.ts:49 `&&` → `\|\|` | same symptom in jsdom (history defined) | same test |
| Home.svelte:6 BooleanLiteral | c68261e3 | Home.svelte:8 `pending = $state(false)` → true | Log out disabled on mount | t-10 "sign-out button enabled on mount" |
| Home.svelte:9 BooleanLiteral | a094a307 | Home.svelte:30 `pending = true` → false | button clickable while logout in flight | t-10 "disabled while logout hangs" |
| Home.svelte:15 BooleanLiteral | beec743e | Home.svelte:36 `pending = false` → true | stays disabled after logout settles | t-10 "re-enabled after logout settles" |
| Login.svelte:6 StringLiteral | 56313b4a | Login.svelte:6 `$state('')` → "Stryker was here!" | username input pre-filled | t-9 "both inputs empty on mount" |
| Login.svelte:7 StringLiteral | 95210673 | Login.svelte:7 password `$state('')` | password input pre-filled | same |
| Login.svelte:13 BooleanLiteral | f868399f | Login.svelte:13 `pending = true` → false | inputs/button not disabled in flight | t-9 "disabled + 'Signing in…' while login hangs" |
| Login.svelte:44 StringLiteral | 77ea4718 | Login.svelte:44 `'Signing in…'` → '' | in-flight label blank | same |
| App.svelte:15 BlockStatement | d248d03c | App.svelte:17 `catch {}` body removed | non-401 failure leaves `username`/route untouched; URL never becomes /login | t-9 "502 on /me from /children lands on /login" |
| App.svelte:21 StringLiteral | fd433887 | App.svelte:21 `navigate('login')` → `navigate('')` | same symptom (pathname stays) | same test |

Whole-file `.svelte` mutation (ast-unavailable) means the verify run re-mutates every line
of the rewritten Home/App/Login; the tests above are written against behaviour, not lines,
so they kill the successors of these mutants too.

## Wire contract fixed here (Go and web halves build against it in parallel)

```
GET  /api/v1/buttons                -> 200 {"buttons":[{"id":1,"label":"YouTube 1h","child_id":1,"services":["youtube"],"duration":3600}]}
PUT  /api/v1/buttons {"buttons":[{"label","child_id","services":[],"duration"}]}
                                    -> 200 same shape as GET (server-assigned ids, stored order)
                                     | 400 bad_request (unknown field incl. "id", non-object, >64 KiB, buttons missing/null)
                                     | 422 unprocessable, message "buttons[i].<field>: ..." naming the first bad item
                                     | 502 adguard_unavailable (catalogue unreachable)
duration is whole seconds; bounds are grants.MinDuration/MaxDuration (60..86400) — same as POST /grants.
The list is replaced wholesale and atomically; ids are reassigned on every PUT (nothing references them).
A deleted child's buttons are deleted with it (FK ON DELETE CASCADE, foreign_keys is ON in store.Open).

Static surface (t-2/t-11):
GET /                       -> 200 text/html index.html, Cache-Control: no-cache
GET /<unknown, no ext>      -> 200 index.html (SPA fallback), no-cache
GET /<unknown>.js|.png|...  -> 404 (a missing asset is never answered with HTML)
GET /assets/<hashed>        -> 200, Cache-Control: public, max-age=31536000, immutable
GET /sw.js, /manifest.webmanifest, /icon-192.png, /icon-512.png -> 200, no-cache; manifest is application/manifest+json
GET /api/v1/<unknown>       -> 401 without cookie, 404 with (unchanged); GET /api/<anything else> -> 404 plain, never HTML
POST /                      -> 405
```

```
Phase 05-phone-ui-buttons-pwa — 11 tasks across 3 waves

Wave 1
  t-1  Buttons schema and replace-all store
       files:    internal/store/migrations/0004_buttons.sql, internal/store/buttons.go,
                 internal/store/buttons_test.go
       covers:   c-1
       depends:  —
       desc:     0004_buttons.sql: buttons(id INTEGER PRIMARY KEY, position INTEGER NOT NULL UNIQUE,
                 label TEXT NOT NULL, child_id INTEGER NOT NULL REFERENCES children(id) ON DELETE
                 CASCADE, duration INTEGER NOT NULL CHECK (duration > 0)) — seconds;
                 button_services(button_id REFERENCES buttons(id) ON DELETE CASCADE, service_id TEXT
                 NOT NULL, position INTEGER NOT NULL, PK (button_id, service_id)) — service order
                 matters (locked button_icon: first service's icon). buttons.go: Button{ID, ChildID
                 int64; Label string; Services []string (never nil, stored order); Duration
                 time.Duration}; ListButtons(ctx) ordered by position, folded from one query over
                 buttons then one over button_services, each scanned to completion (single-
                 connection rule); ReplaceButtons(ctx, []Button) ([]Button, error) in one tx: DELETE
                 FROM buttons (cascade clears services), INSERT each with position = index, services
                 deduped preserving first occurrence with their own positions, then return the
                 fresh list with ids; any failure rolls back. sentinel ErrButtonChild for the FK
                 failure (classified from the driver's "FOREIGN KEY constraint failed" text).
       contract: - if order is not persisted, TestButtons_ReplaceOrder fails: Replace([B "TikTok 30m"
                   (ada, [tiktok], 30m), A "YouTube 1h" (ada, [youtube, tiktok], 1h)]) then
                   ListButtons returns labels ["TikTok 30m","YouTube 1h"] with ids ascending and
                   A.Services == ["youtube","tiktok"] in that order (not sorted)
                 - if rows do not survive Close/Open, TestButtons_Persist fails: after Close and Open
                   on the same dir ListButtons returns identical ids, labels, services, durations;
                   schema_migrations lists 0004_buttons.sql exactly once
                 - if Replace is not atomic, TestButtons_ReplaceRollsBack fails: a Replace whose second
                   item names child 999 returns errors.Is(err, ErrButtonChild) and ListButtons still
                   returns the previous two rows unchanged; likewise a Replace whose second item has
                   Duration 0 (CHECK) leaves the previous list intact
                 - if Replace does not clear, TestButtons_ReplaceEmpty fails: Replace(nil) → ListButtons
                   returns []Button{} (non-nil, len 0) and SELECT count(*) FROM button_services == 0
                 - if a deleted child leaves orphan buttons, TestButtons_ChildDeleteCascades fails:
                   two buttons for ada, one for ben; DeleteChild(ada) → ListButtons has only ben's,
                   button_services has only ben's rows
                 - if duplicate services in one item are not folded, TestButtons_DedupeServices fails:
                   Services ["youtube","tiktok","youtube"] read back as ["youtube","tiktok"]; an empty
                   Services slice reads back as []string{} never nil

  t-2  SPA handler, embedded dist, build plumbing
       files:    internal/spa/spa.go, internal/spa/spa_test.go, web/embed.go, web/dist/.gitkeep,
                 .gitignore, Makefile, web/vite.config.ts
       covers:   c-5, c-6 (cache headers)
       depends:  —
       desc:     web/embed.go (package web, module path .../web): `//go:embed all:dist` + Dist() fs.FS
                 (fs.Sub). web/dist/.gitkeep is committed so the directive compiles on a fresh clone:
                 .gitignore changes `web/dist/` to `web/dist/*` + `!web/dist/.gitkeep`; vite build gets
                 `emptyOutDir: false` (vite's emptyDir would delete .gitkeep) and the Makefile
                 build-web target does `rm -rf $(WEB)/dist/assets` first so hashed files never
                 accumulate; `test:` depends on build-web so the binary-level tests in t-11 always see
                 a real dist; lint/fmt exclude only $(WEB)/node_modules and $(WEB)/.stryker-tmp so
                 embed.go is gofmt-checked. spa.go: Handler(fsys fs.FS) http.Handler — GET/HEAD only
                 (405 otherwise); paths under /api/ → 404 (never HTML); a path that names an existing
                 file is served with Cache-Control "public, max-age=31536000, immutable" under
                 /assets/ and "no-cache" elsewhere; a missing path with an extension → 404; any other
                 missing path → index.html 200 no-cache (SPA fallback); .webmanifest is served as
                 application/manifest+json via mime.AddExtensionType. Pure decision function
                 `resolve(fsys, path) (file string, kind kind)` with the table test; Handler is thin.
       contract: - if the fallback decision drifts, TestResolve_Table fails on a fstest.MapFS
                   {index.html, assets/app-abc.js, sw.js, manifest.webmanifest, icon-192.png}:
                   "/" → index.html; "/children" and "/buttons/deep/path" → index.html;
                   "/assets/app-abc.js" → that file, kind immutable; "/assets/gone.js" → 404;
                   "/favicon.ico" → 404; "/api/v2/x" and "/api/" → 404; "/sw.js" → file, kind no-cache
                 - if the headers drift, TestHandler_CacheHeaders fails: GET /assets/app-abc.js has
                   Cache-Control "public, max-age=31536000, immutable"; GET /, GET /children and
                   GET /sw.js have "no-cache"; GET /manifest.webmanifest has Content-Type starting
                   "application/manifest+json"; GET / has Content-Type starting "text/html"
                 - if methods are not restricted, TestHandler_Methods fails: POST / → 405 with Allow
                   containing GET; HEAD / → 200 with empty body and the html Content-Type
                 - if a missing asset is answered with HTML, TestHandler_MissingAssetIs404 fails:
                   GET /assets/old-hash.js → 404 whose Content-Type is not text/html
                 - if the embed directive is broken, `go vet ./...` (make typecheck) fails to compile
                   package web on a clone with only .gitkeep in dist; TestDist_Sub asserts Dist()
                   opens without error and fs.Stat(Dist(), ".gitkeep") succeeds

  t-3  Web manifest, icons, SW registration
       files:    web/public/manifest.webmanifest, web/public/icon-192.png, web/public/icon-512.png,
                 web/index.html, web/src/main.ts, web/src/pwa.test.ts
       covers:   c-6 (locked pwa_scope)
       depends:  —
       desc:     manifest.webmanifest: name "adguard-reward", short_name "Reward", start_url "/",
                 scope "/", display "standalone", background_color/theme_color, icons [{src
                 "/icon-192.png", sizes "192x192", type "image/png"}, {src "/icon-512.png", sizes
                 "512x512", type "image/png", purpose "any maskable"}]. Icons are generated once with
                 a dependency-free node zlib script (solid colour + glyph is fine; the script is not
                 committed). index.html: <title>adguard-reward</title>, <link rel="manifest"
                 href="/manifest.webmanifest">, <meta name="theme-color">, <link
                 rel="apple-touch-icon" href="/icon-192.png">. main.ts (Stryker-disabled bootstrap):
                 `if (import.meta.env.PROD && 'serviceWorker' in navigator)
                 navigator.serviceWorker.register('/sw.js')`. pwa.test.ts runs under node and reads
                 the static files from disk.
       contract: - if a manifest field the installability check needs is lost, PWA "manifest is
                   installable" fails: JSON.parse of public/manifest.webmanifest has display ===
                   'standalone', start_url === '/', scope === '/', non-empty name and short_name,
                   and icons containing sizes '192x192' and '512x512' both type image/png
                 - if an icon file is missing or the wrong size, PWA "icons are real PNGs" fails:
                   for each manifest icon the file exists under public/, its first 8 bytes are the
                   PNG signature and the IHDR width/height equal the declared sizes
                 - if index.html stops linking the manifest, PWA "shell links the manifest" fails:
                   index.html contains rel="manifest" href="/manifest.webmanifest" and a
                   theme-color meta; main.ts contains serviceWorker.register('/sw.js') guarded by
                   'serviceWorker' in navigator

  t-4  Service worker: shell precache, API passthrough
       files:    web/src/sw.ts, web/src/sw.test.ts, web/vite.config.ts
       covers:   c-6
       depends:  —
       desc:     sw.ts (`/// <reference lib="webworker" />`, no imports): SHELL = self.__SHELL__ ??
                 ['/'], CACHE = 'shell-' + self.__VERSION__. install: caches.open(CACHE) →
                 addAll(SHELL), skipWaiting. activate: delete every cache whose name !== CACHE,
                 clients.claim. fetch: return without respondWith for non-GET, cross-origin, and
                 any pathname starting with /api/ or equal to /healthz (the request goes to the
                 network untouched and is never cached); mode 'navigate' → network first, on
                 failure caches.match('/'); anything else → caches.match(request) else fetch
                 (never cache.put at runtime — precache only). The routing decision is a pure
                 exported `classify(request: Request, origin: string): 'bypass'|'navigate'|'shell'`
                 so it is table-testable. vite.config.ts gains a ~30-line inline plugin (file is
                 already Stryker-disabled), apply 'build', generateBundle: shell = ['/', ...emitted
                 'assets/*' as '/assets/…', '/manifest.webmanifest', '/icon-192.png',
                 '/icon-512.png'], version = sha256 of the shell list; source = `self.__SHELL__=…;
                 self.__VERSION__="…";` + transformWithEsbuild(sw.ts) → emitFile 'sw.js'.
       contract: - if /api/ can ever be served or cached by the SW, SW "api requests bypass the
                   worker" fails: with self/caches/fetch stubbed and sw.ts imported, a fetch event for
                   GET http://localhost/api/v1/grants, POST /api/v1/grants, GET /healthz and GET
                   https://other.example/x each leave respondWith uncalled and caches.open/match
                   uncalled; classify returns 'bypass' for all four
                 - if navigation is not network-first with shell fallback, SW "offline navigation
                   serves the shell" fails: mode 'navigate' for /children with fetch resolving 200 →
                   respondWith resolves to that network response and caches.match is not consulted;
                   with fetch rejecting → respondWith resolves to caches.match('/') and that response
                   is the precached index
                 - if shell assets are not cache-first, SW "assets come from the precache" fails:
                   GET /assets/app-abc.js present in the fake cache → respondWith resolves to the
                   cached response and fetch is not called; a shell URL absent from the cache falls
                   through to fetch and is not cache.put afterwards
                 - if install/activate drift, SW "install precaches SHELL and activate prunes" fails:
                   dispatching install with __SHELL__ = ['/', '/assets/a.js'] calls
                   cache.addAll(['/', '/assets/a.js']) on cache 'shell-v1' and skipWaiting; activate
                   with caches ['shell-v0','shell-v1'] deletes only shell-v0 and calls clients.claim
                 - if the build plugin stops emitting, t-11's TestRun_SWPrecacheListIsServed fails
                   (binary-level; see there)

  t-5  Web API client: grants, buttons, route, pure helpers
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/lib/grants.ts,
                 web/src/lib/grants.test.ts
       covers:   c-2, c-3, c-7 (api.ts keys), locked extend_amount, overlap_offer
       depends:  —
       desc:     api.ts: Route += 'buttons' (PATHS.buttons = '/buttons'); interfaces Grant{id,
                 child_id, services, clients, started_at, ends_at}, GrantCreated{id, ends_at,
                 applied, failed}, ButtonInput{label, child_id, services, duration}, Button extends
                 ButtonInput{id}; listGrants(), createGrant(child_id, services, duration) → POST
                 /api/v1/grants, extendGrant(id, duration) → POST …/{id}/extend, endGrant(id) →
                 POST …/{id}/end, listButtons(), putButtons(ButtonInput[]) → PUT /api/v1/buttons
                 {buttons}; ApiError gains optional grantId from a 409 envelope's grant_id
                 (readEnvelope also reads body.grant_id when it is a number). messageFor gains no new
                 fixed lines (409/422 echo the server message). grants.ts (pure): durationOf(g) =
                 (ends_at − started_at) in seconds; remainingMs(ends_at, now); formatRemaining(ms)
                 → "m:ss" under an hour, "h:mm:ss" from an hour, "0:00" at ≤ 0; formatDuration(s)
                 → "30 min", "1 h", "1 h 30 min", "24 h"; planTap(active, childId, services) →
                 {extend: [{id, services}], create: string[]} considering only grants of that child.
                 api.test.ts additionally kills the routed navigate/currentPath mutants.
       contract: - if navigate pushes when already on the path, "navigate to the current path pushes
                   nothing" fails: at '/', vi.spyOn(history,'pushState'); navigate('home') → spy called
                   0 times; navigate('buttons') → called exactly once with third arg '/buttons' and
                   location.pathname === '/buttons'; navigate('buttons') again → still once
                 - if the initial route is not read from the URL, "route reflects the URL at load"
                   fails: for each of '/', '/login', '/children', '/buttons', '/nope':
                   history.replaceState(null,'',p); vi.resetModules(); const m = await import('./api');
                   get(m.route) === 'home'|'login'|'children'|'buttons'|'home' respectively
                 - if any grant/button call drifts, "grant and button endpoints hit the exact URL and
                   method" fails: createGrant(1,['tiktok'],1800) → POST /api/v1/grants with body
                   {child_id:1,services:['tiktok'],duration:1800} and the CSRF header; extendGrant(7,
                   600) → POST /api/v1/grants/7/extend {duration:600}; endGrant(7) → POST
                   /api/v1/grants/7/end with no body and resolves undefined on 204; listGrants() →
                   GET /api/v1/grants returning res.grants; listButtons() → GET /api/v1/buttons
                   returning res.buttons; putButtons([...]) → PUT /api/v1/buttons {buttons:[...]}
                   and returns the response's buttons
                 - if the 409 grant id is dropped, "409 carries grantId" fails: a 409 {error:'conflict',
                   message:'tiktok is already granted by grant 3', grant_id:3} rejects with ApiError
                   whose status 409, code 'conflict', grantId 3; a 409 without grant_id → grantId
                   undefined
                 - if formatting drifts, grants.test "formatRemaining" fails: 1799_000 → '29:59';
                   1000 → '0:01'; 0 and −5000 → '0:00'; 3600_000 → '1:00:00'; 5_425_000 → '1:30:25';
                   "formatDuration": 60 → '1 min', 1800 → '30 min', 3600 → '1 h', 5400 → '1 h 30 min',
                   86400 → '24 h'; "durationOf": started 10:00:00Z ends 11:00:00Z → 3600
                 - if the overlap plan is wrong (locked overlap_offer), grants.test "planTap" fails:
                   active [g1(ada,[youtube]), g2(ada,[tiktok]), g3(ben,[roblox])]; tap(ada,[youtube])
                   → extend [{1,[youtube]}] create []; tap(ada,[youtube,roblox]) → extend
                   [{1,[youtube]}] create [roblox]; tap(ada,[youtube,tiktok,roblox]) → extend
                   [{1,[youtube]},{2,[tiktok]}] create [roblox]; tap(ben,[youtube]) → extend []
                   create [youtube]; tap(ada,[]) → extend [] create []

  t-6  GrantFields: child, services, duration picker
       files:    web/src/lib/GrantFields.svelte, web/src/lib/GrantFields.test.ts
       covers:   c-4, c-8
       depends:  —
       desc:     Shared form fragment used by the ad-hoc form (t-10) and the buttons page (t-9).
                 Props: children: Child[], services: Service[], disabled; bindable childId:
                 number | null, selected: string[], minutes: number (default 60). Renders <select
                 name="child"> (placeholder option when childId is null; auto-selects the only
                 child when exactly one exists), one <input type="checkbox" name="service"
                 value=id> per catalogue service with its iconUrl image and name, <input
                 type="number" name="minutes" min=1 max=1440 step=1>. Exports `valid` via a
                 bindable boolean: childId !== null && selected.length > 0 && minutes is an integer
                 in 1..1440. No submit button of its own.
       contract: - if validity drifts from the server bounds, GrantFields "valid tracks the three
                   fields" fails: with two children nothing selected → valid false; choose Ada →
                   still false; tick tiktok → true; untick → false; tick tiktok and set minutes 0 →
                   false; 1441 → false; 1440 → true; 1 → true; 90.5 → false
                 - if the single-child shortcut is lost, "one child is preselected" fails: children
                   [Ada] → select value '1' and childId 1 on mount; children [Ada, Ben] → select value
                   '' and childId null
                 - if selection order is not preserved (locked button_icon needs the first service),
                   "selected keeps tick order" fails: tick tiktok then youtube → selected
                   ['tiktok','youtube']; untick tiktok → ['youtube']
                 - if the picker ignores disabled, "disabled greys every control" fails: disabled true
                   → select, every checkbox and the minutes input have the disabled attribute

Wave 2
  t-7  Buttons GET/PUT endpoints and validation
       files:    internal/api/buttons.go, internal/api/buttons_test.go, internal/api/api.go,
                 internal/api/login_test.go
       covers:   c-1
       depends:  t-1
       desc:     api.go: ButtonStore interface {ListButtons, ReplaceButtons}, Deps.Buttons, routes
                 GET/PUT /api/v1/buttons behind requireSession. buttons.go: view {id, label,
                 child_id, services, duration(seconds)}; PUT body {buttons: [{label, child_id,
                 services, duration}]} decoded like decodeGrantBody (64 KiB cap, DisallowUnknownFields
                 so a stray "id" is 400, single object, buttons missing/null → 400). Validation,
                 cheapest first, stops at the first bad item with message "buttons[i].field: …":
                 label trimmed 1–64 runes; duration via checkDuration's bounds (reused, but the 422
                 message carries the index); services trimmed/deduped, empty → 422; then each
                 distinct child_id via Children.GetChild (ErrChildNotFound → 422, not 404 — it is a
                 field); then the catalogue once via AdGuard.Services (unreachable → 502, no write)
                 and each id checked. ReplaceButtons → 200 with the returned list; store FK error
                 → 422 (race with a concurrent child delete). GET → {buttons:[…]} never null.
                 login_test.go: harness passes Buttons: st.
       contract: - if either route escapes the gates, TestButtons_Gated fails: GET and PUT without a
                   cookie → 401; PUT with a cookie but no X-Requested-With → 403 and ListButtons is
                   unchanged
                 - if the round trip drifts, TestButtons_PutGet fails: PUT [{"TikTok 30m",ada,
                   ["tiktok"],1800},{"YouTube 1h",ada,["youtube","tiktok"],3600}] → 200 whose
                   buttons have ids > 0 ascending, labels in that order, services ["youtube",
                   "tiktok"] unsorted, duration 3600; GET returns a byte-identical body; PUT [] → 200
                   {"buttons":[]} and GET body is exactly {"buttons":[]}
                 - if validation or its messages drift, TestButtons_Validation fails: duration 59 →
                   422 message containing "buttons[0].duration"; 86401 → 422; 60 and 86400 → 200;
                   child_id 999 → 422 containing "buttons[0].child_id" and no
                   /control/blocked_services/all request was made; services ["nope"] → 422
                   containing "nope" and "buttons[0].services"; services [] and [""] → 422; label
                   "  " → 422 containing "buttons[0].label"; a 65-rune label → 422; a second item bad
                   → message names buttons[1]; {"buttons":[{"label":"x","id":1,…}]} → 400 (unknown
                   field); {"buttons":null} and {} → 400; a 70 KiB body → 400; after every non-200
                   GET still returns the previous list
                 - if the catalogue failure creates rows, TestButtons_CatalogueDown fails:
                   SetStatus("/control/blocked_services/all", 500) → PUT is 502 adguard_unavailable
                   and GET is unchanged
                 - if a deleted child keeps its buttons, TestButtons_ChildDeleted fails: PUT two
                   buttons for ada and one for ben, DELETE /children/{ada} → GET lists only ben's
                   button, same id as before

  t-8  ActiveGrants panel with countdown, extend, end
       files:    web/src/lib/ActiveGrants.svelte, web/src/lib/ActiveGrants.test.ts
       covers:   c-3 (locked extend_amount, unreachable_countdown)
       depends:  t-5
       desc:     Presentational: props grants: Grant[], now: number (ms), unreachable: boolean,
                 childName: (id) => string, serviceName: (id) => string, onExtend: (g) =>
                 Promise<void>, onEnd: (g) => Promise<void>, error: string | null. Renders <section
                 data-active> with heading "Unlocked now"; only grants with ends_at > now, one <li
                 data-grant={id}> each: child name, service names joined ", ", <time
                 data-remaining>{formatRemaining(remainingMs(ends_at, now))}</time>, button
                 "Extend +{formatDuration(durationOf(g))}" and button "End". Empty → <p>Nothing
                 unlocked</p>. unreachable → <span data-badge="unreachable">Can't reach server</span>
                 while the list stays rendered. error → <p role="alert" data-error>. A row's two
                 buttons are disabled while its own onExtend/onEnd promise is pending.
       contract: - if the countdown is not derived from now and ends_at, ActiveGrants "countdown
                   follows now" fails: ends_at = now+1_799_000 → time text '29:59'; re-render with
                   now+1000 → '29:58'; with now ≥ ends_at the row is gone and, if it was the only
                   grant, "Nothing unlocked" is shown
                 - if the extend label is not the grant's own duration (locked extend_amount),
                   "extend label is ends−started" fails: started 10:00:00Z, ends 11:30:00Z → button
                   name 'Extend +1 h 30 min'; clicking it calls onExtend with that grant exactly once
                 - if End is not wired, "end calls onEnd" fails: click 'End' on row 7 → onEnd called
                   with grant 7 once; while onEnd's promise is pending both of row 7's buttons are
                   disabled and row 8's are not; after it resolves they are enabled
                 - if unreachable blanks the panel (locked unreachable_countdown), "unreachable keeps
                   the rows" fails: unreachable true → badge present and every row still rendered
                   with its countdown; unreachable false → no badge
                 - if names are not resolved, "names come from the lookups" fails: a grant for child
                   1 with ['youtube','tiktok'] renders 'Ada' and 'YouTube, TikTok' via the passed
                   functions, never raw ids

  t-9  Buttons settings page, /buttons route, Login/App kills
       files:    web/src/lib/Buttons.svelte, web/src/lib/Buttons.test.ts, web/src/App.svelte,
                 web/src/App.test.ts, web/src/lib/Login.test.ts
       covers:   c-4, c-7 (Login/App keys), locked button_order
       depends:  t-5, t-6
       desc:     Buttons.svelte (props onBack): onMount loads listButtons, listChildren,
                 listServices in parallel. List of stored buttons in array order, each <li
                 data-button={id}> with label, child name, service names, formatDuration, buttons
                 Edit and Delete; no reorder controls (locked). Form: <input name="label"> +
                 <GrantFields> + Save (disabled unless label non-blank and fields valid) + Cancel
                 when editing. Save builds the next list (append, or replace the edited index in
                 place), strips ids, putButtons, replaces the list from the response, clears the
                 form. Delete PUTs the list without that item. Any ApiError → <p role="alert"
                 data-error={code}>{messageFor}</p> and the list is reloaded from the server.
                 App.svelte: import Buttons; `{:else if $route === 'buttons' && username !== null}
                 <Buttons onBack={() => navigate('home')} />`. Tests for the routed Login/App
                 mutants live in Login.test.ts / App.test.ts.
       contract: - if Save does not PUT the whole list in order, Buttons "add appends and PUTs
                   everything" fails: existing [b1]; fill label 'TikTok 30m', child Ada, tick tiktok,
                   minutes 30, Save → exactly one PUT /api/v1/buttons whose body is {buttons:[{label:
                   b1.label, child_id, services, duration}, {label:'TikTok 30m', child_id:1,
                   services:['tiktok'], duration:1800}]} with no "id" keys; the list re-renders from
                   the response (2 rows, the new id from the server)
                 - if edit replaces the wrong slot, "edit keeps position" fails: [b1,b2,b3], Edit b2,
                   change label to 'Renamed', Save → PUT body labels [b1,'Renamed',b3]
                 - if delete is not a PUT of the remainder, "delete PUTs without the item" fails:
                   [b1,b2], Delete b1 → PUT body {buttons:[b2 without id]} and one row remains
                 - if a 422 is swallowed, "422 shows the server message" fails: PUT answers 422
                   {error:'unprocessable', message:'buttons[0].services: unknown service "nope"'} →
                   alert text equals that message, data-error 'unprocessable', and GET /buttons was
                   re-fetched (calls count 2)
                 - if Save is enabled early, "Save waits for a valid form" fails: on mount Save is
                   disabled; label only → disabled; label + child + service → enabled
                 - if the route is missing, App "reload on /buttons stays there" fails: replaceState
                   '/buttons', route.set('buttons'), me() 200 → heading 'Buttons' and pathname
                   '/buttons'; "a 401 at /buttons lands on login" → Sign in button and '/login';
                   Back → 'Signed in as' and '/'
                 - if App's non-401 fallback is lost (App.svelte:17/:21 keys), App "502 on /me from
                   /children lands on /login" fails: replaceState '/children', route.set('children'),
                   setOnUnauthorized(spy), me → 502 → Sign in button rendered, location.pathname ===
                   '/login', get(route) === 'login', spy not called
                 - if Login's initial values or pending state drift (Login.svelte:6/:7/:13/:44 keys),
                   Login "starts empty and locks while signing in" fails: on mount both inputs have
                   value '' and are enabled, button name 'Sign in'; fetch returns a never-settling
                   promise → after click both inputs and the button have the disabled attribute and
                   the button text is 'Signing in…'; resolve the promise with 204 → all enabled,
                   text 'Sign in'

Wave 3
  t-10 Home: buttons, tap flow, overlap offer, ad-hoc form
       files:    web/src/lib/Home.svelte, web/src/lib/Home.test.ts
       covers:   c-2, c-3, c-8, c-7 (Home keys), locked home_layout, no_confirm, overlap_offer,
                 extend_amount, button_icon, button_order, unreachable_countdown
       depends:  t-5, t-6, t-8
       desc:     load(): listChildren, listServices, listButtons, listGrants and childBlocked per
                 child (Promise.all); a failure in the blocked views keeps the existing error
                 branch; a grants poll failure sets unreachable = true without touching grants.
                 setInterval 1 s → now = Date.now(); setInterval 10 s → refreshGrants(); both
                 cleared on destroy. Layout (locked home_layout): nav (Children, Buttons, Log out —
                 sign-out logic untouched) → MigrationBanner → <ActiveGrants> → per child <section
                 data-child>: h2, <div class="buttons"> with that child's buttons in stored order,
                 each <button data-button={id}> showing iconUrl of its first service (when the
                 catalogue has it) + label + formatDuration; then <details data-blocked> summary
                 "Blocked now" wrapping the existing client badges and services list → <details
                 data-adhoc open={buttons.length === 0}> summary "Unlock something else" with
                 <GrantFields> + Unlock button. tap(b): plan = planTap(grants, b.child_id,
                 b.services); if plan.extend is empty → createGrant immediately (no confirm) then
                 refreshGrants; else show <div role="dialog" data-offer> "<services> already
                 unlocked for <child> — extend by <formatDuration(b.duration)>?" with Extend and
                 Cancel; Extend → extendGrant(id, b.duration) for each plan.extend entry, then
                 createGrant for plan.create when non-empty, then refreshGrants. A 409 from
                 createGrant (stale list) → refreshGrants then re-run tap once (which now yields
                 the offer). Result handling: 201 applied=false → <p role="alert"
                 data-error="partial"> "Unlocked, but couldn't reach Kid tablet" (failed joined);
                 ApiError → alert data-error={code} messageFor; network → data-error="network".
                 Ad-hoc Unlock reuses tap() with a synthetic button {child_id, services, duration:
                 minutes*60}. onExtend(g) → extendGrant(g.id, durationOf(g)); onEnd(g) →
                 endGrant(g.id); errors go to the panel's error prop; both refreshGrants after.
       contract: - if buttons are not rendered per child in stored order (locked button_order),
                   Home "renders buttons under their child in order" fails: buttons [{id 2, ada,
                   'TikTok 30m'}, {id 1, ada, 'YouTube 1h'}, {id 3, ben, 'Games 2h'}] → section 1
                   has data-button 2 then 1 with names 'TikTok 30m 30 min' and 'YouTube 1h 1 h',
                   section 2 has only 3; button 1's <img src> is iconUrl of the youtube icon (its
                   first service) and button 2's is tiktok's; a button whose first service is not in
                   the catalogue renders no <img>
                 - if a tap is not immediate (locked no_confirm) or the panel entry waits for the
                   poll, "tap creates the grant and shows the countdown" fails: fake timers, no
                   active grants; click button 'YouTube 1h' → exactly one POST /api/v1/grants
                   {child_id:1, services:['youtube'], duration:3600} with no dialog first; the 201
                   answer + GET /grants now listing it → without advancing timers a <li
                   data-grant> for Ada 'YouTube' with time text '1:00:00' is visible; no alert
                 - if a failed apply is swallowed, "applied=false names the failed clients" fails:
                   POST → 201 {applied:false, failed:['Kid tablet']} → alert with data-error
                   'partial' whose text contains 'Kid tablet'; the grant still appears in the panel;
                   POST → 502 adguard_unavailable → alert "Can't reach AdGuard Home — try again in a
                   moment", data-error 'adguard_unavailable', no panel entry; fetch throws → alert
                   data-error 'network'
                 - if the overlap offer is wrong (locked overlap_offer/extend_amount), "overlapping
                   tap offers an extend and delivers the rest" fails: active g1 (ada, [youtube],
                   started 10:00 ends 11:00) loaded; click 'Video 2h' (ada, [youtube, tiktok], 7200)
                   → zero POSTs and a [role=dialog][data-offer] whose text contains 'YouTube' and
                   '2 h'; Cancel → no POST, dialog gone; click again then Extend → POST
                   /grants/1/extend {duration:7200} and POST /grants {child_id:1, services:
                   ['tiktok'], duration:7200} in that order, then GET /grants; a tap of ['youtube']
                   alone then Extend → only the extend POST
                 - if a server 409 is not turned into the offer, "409 becomes the offer" fails:
                   grants list empty; POST → 409 {error:'conflict', grant_id:5, message:…}; GET
                   /grants now returns g5 (ada, [youtube]) → the dialog appears naming YouTube, no
                   alert, exactly one POST so far; Extend → POST /grants/5/extend {duration:3600}
                 - if the countdown does not tick or expiry does not remove, "countdown ticks and
                   expiry removes" fails: g1 ends_at = now+2000 → time '0:02'; advanceTimersByTime
                   1000 → '0:01'; +1000 → the row is gone with no further GET /grants having fired
                   (the 10 s poll has not elapsed)
                 - if a poll failure blanks the panel (locked unreachable_countdown), "poll failure
                   keeps countdowns" fails: after load, make GET /grants return 502; advance 10 s →
                   GET /grants was called again, [data-badge=unreachable] present, the row and its
                   countdown still rendered and still ticking; restore 200 and advance 10 s → badge
                   gone
                 - if extend/end from the panel do not hit the API with the grant's own duration,
                   "panel extend and end" fails: g1 started 10:00 ends 11:30 → click 'Extend +1 h 30
                   min' → POST /grants/1/extend {duration:5400} then GET /grants; click 'End' → POST
                   /grants/1/end then GET /grants (which no longer lists it) → row gone; End → 502
                   whose message names 'Kid phone' → panel alert containing 'Kid phone' and the row
                   still present after the refresh
                 - if the ad-hoc form is missing or gated on buttons (c-8), "ad-hoc form unlocks
                   without buttons" fails: buttons [] → [data-adhoc] is open and a hint mentions
                   Buttons; choose Ada, tick roblox, minutes 45, click Unlock → POST /grants
                   {child_id:1, services:['roblox'], duration:2700} and the panel shows Ada 'Roblox'
                   '45:00'; with buttons present [data-adhoc] renders closed
                 - if the blocked list leaves the disclosure (locked home_layout), "blocked view sits
                   under a closed disclosure below the buttons" fails: section 1 contains, in DOM
                   order, h2 → .buttons → details[data-blocked] (not open) whose text contains
                   'YouTube' and 'TikTok' from the blocked view and 'unblocked on' for a partial;
                   the ActiveGrants section precedes every section[data-child]
                 - if the sign-out pending state drifts (Home.svelte:8/:30/:36 keys), "log out is
                   enabled, then locked, then enabled" fails: on mount 'Log out' is not disabled;
                   POST /logout returns a never-settling promise → after click it is disabled;
                   resolve 204 with onLogout a no-op spy → not disabled and onLogout called once
                 - if Home no longer reaches the settings page, "Buttons button routes" fails: click
                   'Buttons' → location.pathname '/buttons' and get(route) 'buttons'
                 - the existing Home tests (blocked lists, partial, badges, AdGuard-down error state,
                   refetch on mount, migration banner) keep passing with their selectors moved into
                   details[data-blocked]

  t-11 Wire the SPA into main; binary-level proofs
       files:    cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go
       covers:   c-1, c-4, c-5, c-6
       depends:  t-2, t-3, t-4, t-7
       desc:     main.go: `mux.Handle("/", spa.Handler(web.Dist()))` beside /healthz and /api/v1/
                 (Go 1.22 mux picks the longer pattern, so the API chain and healthz are untouched).
                 main_test.go: helper distFile(t, name) reads web.Dist(); TestRun_SPA* call
                 t.Fatalf("web/dist not built — run make build-web") when index.html is absent
                 (make test builds it first; a skip would be a false pass). Helper putButtons /
                 getButtons over the running server for the restart test.
       contract: - if the SPA is not served with fallback on the same origin, TestRun_SPAFallback
                   fails: GET / → 200, Content-Type text/html, body byte-equal to the embedded
                   index.html and containing '<div id="app">'; GET /children and GET /buttons →
                   200 with the same body; GET /api/v1/nope → 401 without cookie and 404 JSON with
                   one (unchanged); GET /api/v2/nope → 404 whose Content-Type is not text/html; GET
                   /assets/missing.js → 404; healthz still 200 with no cookie
                 - if the PWA files are not reachable from the binary, TestRun_PWAFilesServed fails:
                   GET /manifest.webmanifest → 200 application/manifest+json parsing to display
                   'standalone' and start_url '/'; GET /icon-192.png and /icon-512.png → 200
                   image/png; GET /sw.js → 200 javascript Content-Type with Cache-Control no-cache
                 - if the precache list and the build disagree, TestRun_SWPrecacheListIsServed
                   fails: parse `self.__SHELL__=[...]` out of /sw.js; it contains '/' and at least
                   one '/assets/' entry; every entry answers 200 from the binary, every '/assets/'
                   entry with Cache-Control containing 'immutable'; no entry starts with /api/
                 - if buttons do not survive a restart, TestRun_ButtonsSurviveRestart fails: run,
                   login, POST child, PUT two buttons, stop (exit 0), start again on the same
                   data_dir, fresh login → GET /api/v1/buttons body byte-equal to the PUT's 200 body
                   (ids, order, services order, durations)
                 - if the static handler leaks into the API chain, TestRun_ApiChain (existing) still
                   passes: /me without cookie is 401 JSON no-store, login without CSRF is 403
```

## Coverage

| criterion | tasks |
|---|---|
| c-1 buttons API + persistence + 422s | t-1, t-7, t-11 (restart) |
| c-2 buttons on Home, tap → countdown, inline errors | t-5, t-10 |
| c-3 active-grant panel, extend/end, 409 offer, expiry | t-5, t-8, t-10 |
| c-4 buttons settings page, survives restart | t-6, t-9, t-11 |
| c-5 embedded SPA, fallback, 404 for unknown /api/v1 | t-2, t-11 |
| c-6 manifest, SW shell-only precache, /api never cached | t-2 (headers), t-3, t-4, t-11 |
| c-7 14 routed survivors killed | t-5 (api.ts), t-9 (Login, App), t-10 (Home) |
| c-8 ad-hoc grant form | t-6, t-10 |

Locked decisions pinned by a named test: home_layout (t-10 disclosure/order test),
extend_amount (t-8 label, t-10 panel extend), overlap_offer (t-5 planTap, t-10 offer test),
no_confirm (t-10 tap test: no dialog, one POST), button_icon (t-10 img src), button_order
(t-1 order, t-10 render order, t-9 no reorder controls), pwa_scope (t-3 start_url/scope
'/'), unreachable_countdown (t-8, t-10 poll-failure tests).

## Judgment calls

- PUT /buttons replaces the list wholesale and reassigns ids, rather than upserting by id:
  nothing references a button id, the settings form is the only writer, and replace-all has one
  atomicity test instead of three (create/update/delete-by-diff). Rejected: id-preserving upsert.
- A stray `id` in a PUT item is a 400 (DisallowUnknownFields), not silently ignored: an ignored
  field is an untestable branch. The web client strips ids (ButtonInput vs Button).
- Unknown child in a button is 422 naming `buttons[i].child_id`, not 404: it is a field of an
  item, and the spec text says 422 on unknown child. Rejected: 404 like POST /grants.
- Embedding: `web/embed.go` with `all:dist` plus a committed `web/dist/.gitkeep`, vite
  `emptyOutDir: false` and `rm -rf web/dist/assets` in build-web. Rejected: a build tag or
  fallback FS (the binary would silently ship without a UI); a committed placeholder index.html
  (vite overwrites it and git shows it modified after every build).
- Binary-level SPA tests fatal, not skip, when dist is unbuilt, and `make test` depends on
  build-web. A skip is a green that proves nothing; gremlins runs after `make test` has built.
- Hand-written service worker + ~30-line inline Vite plugin instead of vite-plugin-pwa/workbox:
  the SW is 60 lines whose routing is a pure `classify` with a table test, and the precache list
  is verified end-to-end against the binary (every entry 200). Rejected: workbox — a large
  dependency whose generated code cannot be unit-tested here and whose runtime caching would
  have to be configured off for /api.
- PNG icons (192/512) generated once with a dependency-free node zlib script, not SVG-only:
  Chrome's installability check is only reliably satisfied by PNG; the test parses IHDR so a
  wrong size or a renamed SVG fails.
- ActiveGrants is presentational (receives `now`) and Home owns the two intervals: the panel's
  tests need no fake timers, and Home's timer tests are the only ones that do.
- Overlap is resolved client-side from the loaded list first (planTap) and a server 409 is
  handled as "refresh, then re-plan once": the locked decision allows client-side resolution,
  and the 409 path then shares the same offer UI rather than a second dialog.
- Duration on the pickers is a minutes number input (1–1440), not a preset select: matches the
  API bounds one-to-one and lets a 45-minute grant exist; validity is asserted at both ends.
- Countdown uses the phone's Date.now() against server ends_at with no skew correction: the
  revert is server-side so a skewed countdown is cosmetic; skew correction would need a Date
  header round-trip and its own tests. Noted for a later phase if it bites.
- The three dead survivor keys (api.ts:31/34, App.svelte:21 from the first phase-02/03 runs) get
  behavioural tests anyway (initial route from URL, pathname after non-401 failure) so the
  successors of those mutants in the rewritten files die, rather than relying on the keys
  never reappearing.
- c-7's Login/App kills live in t-9 and the Home kills in t-10 (the tasks that already rewrite
  those test files), not in a separate wave-1 task: a parallel task editing App.test.ts /
  Home.test.ts would conflict, and tests written before the rewrite would target lines that
  no longer exist.
