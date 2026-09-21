# MVP-lens plan — 05-phone-ui-buttons-pwa

Lens: smallest task set that satisfies every criterion. No task exists that
does not trace to a criterion; adjacent work is merged wherever it stays
inside one layer and under ~5 files.

Existing surface this plan leans on (verified by reading the files):
`grants` API is complete (`internal/api/grants.go`: POST/GET, extend, end, 409
`{error:"conflict", grant_id}`, 201 `{id, ends_at, applied, failed}`;
`grants.MinDuration/MaxDuration` exported); `store.Store` has `DB()`,
`foreign_keys=ON`, migrations applied by name order; `api.Deps` takes
api-local interfaces; `login_test.go:newHarness` builds the api on a real store
+ `adguardtest` fake; `main_test.go` has `startWith`, `loginOK`, `call`,
restart helpers; `web/src/lib/api.ts` has `request()`, `messageFor`, routing
(`Route`, `PATHS`, `navigate`, `route`); `Children.svelte` is the form idiom
(drafts, `run()`, checkbox toggles); every web test is jsdom with a URL-keyed
`mockFetch`. `web/dist` is gitignored (root + web/.gitignore) and built by
`make build-web`; the Dockerfile copies it in before `go build`.

Stryker survivors as they exist in the current tree (the spec's line numbers
are phase-02 numbering; `web/reports/mutation/mutation.json` from the phase-03
run gives the live ones, Login.svelte's come from the phase-02 run):

| spec ref | current mutant | how it dies |
|---|---|---|
| api.ts:31/34 | `routeFor`: `r !== 'home' && …` → `true`; `'home'` → `''` | both are **equivalent** (the `r !== 'home'` guard is redundant: `PATHS.home === '/'` only matches `/`). Rewrite `routeFor` as a `find` with no guard — the mutants cease to exist; the surviving `?? 'home'` fallback is pinned by the existing `/x → home` test |
| api.ts:43/44 | `navigate`: `typeof history !== 'undefined' && currentPath() !== path` → whole `true`, `\|\|`, left `true`, `'undefined'` → `''`, right `true`; `pushState(null, '', path)` `''` → string | drop the `typeof history/location/window` guards (every test is jsdom, the SPA never runs outside a browser) so the left-operand/`'undefined'` mutants no longer exist; a new test spies `history.pushState` and asserts navigating to the current route pushes nothing (kills whole-`true`, `\|\|`, right-`true`); the pushState title literal is ignored by every browser — `// Stryker disable next-line StringLiteral: pushState ignores its title argument` with that reason |
| Home.svelte:6/9/15 | `pending = $state(false)` initial; `pending = true` in `signOut`; `pending = false` in `finally` | Home is rewritten this phase; its test asserts Log out is enabled on render, disabled while logout is in flight (deferred fetch), enabled again after it settles with `onLogout` a spy |
| Login.svelte:6/7 | `$state('')` initial username/password | assert both inputs have value `''` on render |
| Login.svelte:13/44 | `pending = true` in `submit`; `'Signing in…'` literal | deferred login fetch: assert inputs + button disabled and button text `Signing in…` while pending, `Sign in` and enabled after |
| App.svelte:15 | `refresh()` catch block → `{}` | 502 on `/me` while at `/children` → `location.pathname` must be `/login` (empty catch leaves it at `/children`) |
| App.svelte:21 | `$route === 'children' && username !== null` right operand → `true` | signed out on `/login`, then popstate to `/children` → Sign in still shown, no `Children` heading, fetch calls stay `['/api/v1/me']` |

Scope caveat: `dross verify` mutates the phase diff. Login.svelte must be in
the diff for its four routed mutants to be re-scored, so t-4 makes a real
phone-UI change there (`autocapitalize="none" spellcheck="false"` on the
username input — phone keyboards capitalise the first letter of a username).

---

```
Phase 05-phone-ui-buttons-pwa — 10 tasks across 3 waves

Wave 1
  t-1  Buttons migration and store replace/list
       files:    internal/store/migrations/0004_buttons.sql,
                 internal/store/buttons.go, internal/store/buttons_test.go
       covers:   c-1
       depends:  —
       description:
         0004_buttons.sql: buttons(id INTEGER PRIMARY KEY, position INTEGER NOT NULL,
         label TEXT NOT NULL, child_id INTEGER NOT NULL REFERENCES children(id) ON
         DELETE CASCADE, services TEXT NOT NULL /* JSON array, parent's order */,
         duration INTEGER NOT NULL /* seconds */), index buttons(position).
         buttons.go: Button{ID int64; Label string; ChildID int64; Services []string
         (never nil, order preserved — the first id picks the icon, locked
         button_icon); Duration time.Duration}. ListButtons(ctx) position ASC.
         ReplaceButtons(ctx, in []Button) ([]Button, error): one tx — DELETE rows
         whose id is not among the given non-zero ids, then per item i an UPSERT
         (INSERT … VALUES (NULLIF(?,0), i, …) ON CONFLICT(id) DO UPDATE SET position,
         label, child_id, services, duration) so a given id is kept and 0 gets a
         new one; services trimmed/deduped preserving order; returns ListButtons
         from inside the tx. A FK failure rolls the whole replace back.
       contract:
         - if replace does not persist order or the services order gets sorted,
           TestButtons_ReplaceAndList fails: Replace([{Label "YouTube 1h", ada,
           ["youtube","tiktok"], 1h}, {Label "Roblox", ben, ["roblox"], 30m}]) returns
           two distinct ids > 0 in that order with Services exactly ["youtube","tiktok"]
           (not sorted) and Duration 1h; ListButtons equals the returned slice
         - if a given id is not kept or an omitted one is not deleted,
           TestButtons_ReplaceKeepsIds fails: second Replace([{ID first, Label "YT"},
           {ID 0, Label "New", …}]) → first keeps its id with label "YT", the Roblox
           row is gone, the new one has a third distinct id; list order is [first, new]
         - if rows do not survive reopen or the migration is not recorded,
           TestButtons_Persist fails: Close, Open the same dir → ListButtons identical
           (ids, labels, services, durations); schema_migrations lists
           0004_buttons.sql exactly once, after 0003_grants.sql
         - if the FK is missing, TestButtons_ChildCascade fails: DeleteChild(ada) →
           ListButtons holds only ben's button
         - if the replace is not atomic, TestButtons_UnknownChildRollsBack fails:
           Replace with child_id 999 in the second item → error, ListButtons still
           returns the previous two rows unchanged
         - if empties read back as nil, TestButtons_Empty fails: fresh store →
           []Button{} non-nil; Replace([]) → [] and a later List is [] (rows deleted);
           a button with Services [" youtube","youtube",""] stores ["youtube"]

  t-2  Embed web/dist and serve the SPA
       files:    web/embed.go, web/dist/.gitkeep, web/.gitignore, .gitignore,
                 internal/spa/spa.go, internal/spa/spa_test.go
       covers:   c-5, c-6
       depends:  —
       description:
         web/embed.go: package web, `//go:embed all:dist`, Dist() fs.FS = fs.Sub(…,
         "dist"). dist/.gitkeep is committed so the embed compiles on a fresh clone
         (.gitignore rules become `dist/*` + `!dist/.gitkeep`, root likewise for
         web/dist). spa.go: Handler(fsys fs.FS) http.Handler — path.Clean, reject
         "..", GET/HEAD only; a regular file present in fsys is served with
         http.ServeFileFS: /assets/* gets Cache-Control "public, max-age=31536000,
         immutable", everything else "no-cache"; init registers
         mime.AddExtensionType(".webmanifest", "application/manifest+json"); any
         other path (including directories) serves index.html with 200 + no-cache
         (client routes); no index.html in fsys → 503 "frontend not built".
       contract:
         - if the SPA fallback is lost, TestSPA_Fallback fails: MapFS{index.html,
           assets/app-1a2b.js, manifest.webmanifest, sw.js}: GET /buttons and
           /children → 200, body == index.html, Content-Type text/html,
           Cache-Control no-cache; GET / → same
         - if hashed assets are not immutable or the shell is cacheable,
           TestSPA_CacheHeaders fails: /assets/app-1a2b.js → 200 with its body and
           Cache-Control "public, max-age=31536000, immutable"; /sw.js and
           /index.html → Cache-Control no-cache
         - if the manifest type is wrong, TestSPA_Manifest fails:
           /manifest.webmanifest → Content-Type starts with application/manifest+json
         - if a directory lists or traversal escapes, TestSPA_NoListing fails:
           /assets/ → index.html body (not a listing); /../embed.go and
           /assets/../index.html → 200 index.html or 400, never a file outside fsys
         - if a missing build is masked, TestSPA_NotBuilt fails: MapFS{} → / → 503
           whose body mentions "frontend"; MapFS{.gitkeep} likewise
         - if the embed breaks, `go vet ./...` fails to compile package web on a
           checkout with only dist/.gitkeep (TestDist_Compiles imports web.Dist() and
           asserts fs.Stat(".gitkeep") succeeds)

  t-3  Web manifest, service worker, install shell
       files:    web/public/manifest.webmanifest, web/public/sw.js,
                 web/public/icon-192.png, web/public/icon-512.png,
                 web/index.html, web/src/main.ts, web/src/pwa.test.ts
       covers:   c-6
       depends:  —
       description:
         manifest: name "adguard-reward", short_name "Reward", start_url "/",
         scope "/", display "standalone", background/theme colours, icons 192 and
         512 PNG (rendered from public/favicon.svg, committed). sw.js (plain JS, no
         build step, outside Stryker's src glob): SHELL = ['/', '/manifest.webmanifest',
         '/favicon.svg', '/icon-192.png', '/icon-512.png']; install → cache.addAll(SHELL)
         + skipWaiting; activate → drop other cache names + clients.claim; fetch →
         return without respondWith for non-GET and for pathnames starting /api/ or
         /healthz (they always hit the network); navigation requests network-first
         with cached '/' as fallback; /assets/* cache-first then runtime-put (hashed,
         immutable); anything else straight to network. index.html: <link
         rel="manifest">, <meta name="theme-color">, apple-touch-icon, title
         "adguard-reward". main.ts: `if (import.meta.env.PROD && 'serviceWorker' in
         navigator) navigator.serviceWorker.register('/sw.js')` (dev server stays
         uncached). pwa.test.ts runs node-env with self/caches/fetch stubbed and
         imports ../public/sw.js.
       contract:
         - if the manifest stops satisfying installability, 'manifest is installable'
           fails: JSON parses; name and short_name non-empty; start_url "/"; scope
           "/"; display "standalone"; icons include sizes "192x192" and "512x512"
           with type image/png; each icon src exists under web/public and its PNG
           IHDR width/height (bytes 16–23) equal the declared size
         - if the shell does not reference the manifest, 'index links the manifest'
           fails: web/index.html contains rel="manifest" href="/manifest.webmanifest"
           and a theme-color meta
         - if /api/ is ever intercepted, 'sw never handles /api' fails: the fetch
           handler called with a GET /api/v1/grants event and a POST /api/v1/grants
           event never calls respondWith and never touches caches; /healthz likewise
         - if the app shell is not precached, 'sw precaches the shell only' fails:
           the install handler's waitUntil promise calls cache.addAll with a list
           containing '/' and '/manifest.webmanifest' and no entry starting /api or
           /assets
         - if navigation falls back wrongly, 'sw navigation is network-first' fails:
           a mode "navigate" event → respondWith resolves to fetch's response when
           fetch succeeds; when fetch rejects it resolves to caches.match('/')
         - if hashed assets are refetched every time, 'sw caches /assets' fails:
           two events for /assets/app-1a2b.js → fetch called once, the second answered
           from cache.put's stored response
         - manual (not automatable): Chrome on Android shows "Add to Home screen"
           for the served binary and the installed app opens standalone at /

  t-4  API client for grants/buttons; kill routing and Login survivors
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts,
                 web/src/lib/Login.svelte, web/src/lib/Login.test.ts
       covers:   c-2, c-3, c-4, c-7, c-8
       depends:  —
       description:
         api.ts: types Button{id,label,child_id,services,duration(seconds)},
         ButtonInput (id optional), Grant{id,child_id,services,clients,started_at,
         ends_at}, GrantCreated{id,ends_at,applied,failed}; listButtons(),
         putButtons(list) → Button[] (PUT {buttons}), listGrants(),
         createGrant(childId, services, duration) → GrantCreated, extendGrant(id,
         duration) → {id, ends_at}, endGrant(id) → void (204). ApiError gains
         `readonly grantId?: number` read from a numeric envelope grant_id (409).
         Route 'buttons' → '/buttons'. routeFor becomes
         `(Object.keys(PATHS) as Route[]).find((r) => PATHS[r] === pathname) ?? 'home'`;
         navigate/currentPath/popstate lose their typeof guards; the pushState line
         carries `// Stryker disable next-line StringLiteral: pushState ignores its
         title argument`. Login.svelte: username input gains autocapitalize="none"
         spellcheck="false" (phone keyboards). Login.test.ts gains the idle/in-flight
         assertions listed below.
       contract:
         - if any new endpoint hits the wrong URL, method or body, 'grants and
           buttons endpoints' fails: listButtons → GET /api/v1/buttons; putButtons →
           PUT /api/v1/buttons with body {buttons:[…]} and the CSRF header;
           listGrants → GET /api/v1/grants returning res.grants; createGrant(1,
           ['youtube'], 3600) → POST /api/v1/grants body {child_id:1,
           services:['youtube'], duration:3600}; extendGrant(7, 600) → POST
           /api/v1/grants/7/extend {duration:600}; endGrant(7) → POST
           /api/v1/grants/7/end and resolves undefined on 204
         - if the 409 loses its grant id, '409 carries grantId' fails: a 409
           {error:'conflict', message:'…', grant_id: 7} rejects with ApiError whose
           code is 'conflict' and grantId === 7; a 409 without grant_id has grantId
           undefined; a non-numeric grant_id is ignored
         - if navigate pushes when already on the route, 'navigate does not push
           the current path' fails: a vi.spyOn(history, 'pushState') sees 0 calls for
           navigate('home') at '/', 1 for navigate('children'), still 1 after a second
           navigate('children'), and location.pathname is '/children'
         - if the route table drifts, 'maps the buttons route both ways' fails:
           navigate('buttons') → pathname '/buttons' and route 'buttons'; replaceState
           '/buttons' + popstate → 'buttons'; '/x' → 'home'; '/' → 'home'
         - if the login form starts dirty, 'fields start empty and idle' fails:
           on render both inputs have value '' and the username input has
           autocapitalize="none"; the submit button is enabled with text 'Sign in'
         - if the in-flight lock is lost, 'locks the form while signing in' fails:
           with fetch returning a promise resolved manually, after clicking Sign in
           both inputs and the button are disabled and the button reads 'Signing in…';
           after resolving with 204 the button reads 'Sign in' and is enabled

Wave 2
  t-5  GET/PUT /api/v1/buttons with validation
       files:    internal/api/buttons.go, internal/api/buttons_test.go,
                 internal/api/api.go, internal/api/login_test.go
       covers:   c-1, c-4
       depends:  t-1
       description:
         api.go: ButtonStore interface (ListButtons, ReplaceButtons), Deps.Buttons,
         routes GET and PUT /api/v1/buttons behind requireSession. buttons.go:
         buttonView{id, label, child_id, services (never null), duration seconds};
         GET → {buttons:[…]}. PUT body {buttons:[{id?, label, child_id, services,
         duration}]} decoded like decodeGrantBody (64 KiB, no unknown fields, single
         object → 400); per item in order: label trimmed 1..64 runes else 400;
         duration via checkDuration → 422; services trimmed/deduped preserving
         order, empty → 422; child_id must appear in one Children.ListChildren read
         → 422 "button N: unknown child M"; service ids checked against one
         AdGuard.Services read (skipped when the list is empty) → 422 naming the id,
         catalogue error → 502; then ReplaceButtons → 200 {buttons} from the store.
         login_test.go: newHarness passes Buttons: h.st.
       contract:
         - if either route escapes the gates, TestButtons_Gated fails: GET and PUT
           without a cookie → 401; PUT with a cookie but no X-Requested-With → 403 and
           the store still lists zero buttons
         - if the round trip drifts, TestButtons_RoundTrip fails: PUT two buttons
           (ada ["youtube","tiktok"] 3600, ben ["roblox"] 1800) → 200 with ids > 0 in
           order, services order preserved, duration 3600/1800; GET body byte-equal
           to the PUT response; PUT the GET body back → identical ids; PUT {buttons:
           []} → 200 {"buttons":[]} and GET agrees
         - if validation order or codes drift, TestButtons_Validation fails:
           child_id 999 → 422 whose message contains "999"; services ["nope"] → 422
           containing "nope"; duration 59 and 86401 → 422; 60 and 86400 → 200;
           services [] and [""] → 422; label "" and a 65-rune label → 400; unknown
           field, a bare array and a 100 KiB body → 400; after every non-200 the GET
           list is unchanged
         - if an unreadable catalogue writes anyway, TestButtons_CatalogueDown fails:
           SetStatus("/control/blocked_services/all", 500) → 502 adguard_unavailable
           and the list is unchanged; PUT [] with the catalogue down → 200 (no
           catalogue read needed)
         - if arrays serialise as null, TestButtons_ViewShape fails: raw GET body
           contains "services":["youtube","tiktok"] and never the substring :null;
           duration is an integer, not a string

  t-6  ActiveGrants panel: countdown, extend, end
       files:    web/src/lib/ActiveGrants.svelte, web/src/lib/ActiveGrants.test.ts
       covers:   c-3
       depends:  t-4
       description:
         Props {grants: Grant[], childName(id), serviceName(id), unreachable:
         boolean, onChanged(): void}. A $effect runs setInterval(1000) updating
         `now`; shown = grants with ends_at > now, sorted by ends_at; each row
         [data-grant=id]: child name, service names, .countdown formatted m:ss or
         h:mm:ss from ends_at − now, Extend and End buttons. Extend posts
         extendGrant(id, (ends_at − started_at) seconds) (locked extend_amount);
         End posts endGrant(id); both then onChanged(); failures show one inline
         alert (messageFor) and leave the row. unreachable → [data-badge=
         unreachable] "can't reach server" while countdowns keep ticking (locked
         unreachable_countdown). Empty → "Nothing is unlocked".
       contract:
         - if the countdown does not tick from ends_at, 'counts down each second'
           fails: fake timers on Date + setInterval only; a grant ending 90 s from
           now reads "1:30"; advance 1 s → "1:29"; a 2 h grant reads "2:00:00"
         - if an expired grant lingers, 'drops a grant at zero' fails: advance past
           ends_at → the row is gone with no fetch made; a second grant still shows
         - if extend uses the wrong amount, 'extend adds the grant's own duration'
           fails: a grant with started_at 12:00 and ends_at 13:00 → Extend posts
           /api/v1/grants/<id>/extend {duration: 3600}; a 30 m grant posts 1800;
           onChanged called once after the 200
         - if end is not wired, 'end posts and refreshes' fails: End → POST
           /api/v1/grants/<id>/end, onChanged called once; a 502 with message
           "could not re-block Kid phone …" → alert text contains "Kid phone", the
           row stays, onChanged not called
         - if a failed poll blanks the panel, 'unreachable badge keeps the countdown'
           fails: unreachable=true → badge visible, rows still present and advancing
           1 s changes the countdown

  t-7  GrantForm: child, services, duration picker
       files:    web/src/lib/GrantForm.svelte, web/src/lib/GrantForm.test.ts
       covers:   c-4, c-8
       depends:  t-4
       description:
         Props {children: Child[], services: Service[], initial?: {label, child_id,
         services, duration}, showLabel: boolean, submitText: string, busy: boolean,
         onSubmit(values: {label, child_id, services: string[], duration: seconds})}.
         Child <select> (first child preselected), service checkboxes with icons
         (iconUrl), duration <input type="number" inputmode="numeric" min=1
         max=1440> in minutes (default 60), label <input> only when showLabel.
         Submit disabled while busy, until one service is checked, when duration is
         outside 1..1440, or when showLabel and the label is blank. Services are
         emitted in catalogue order. No children → "Add a child first" and no form.
       contract:
         - if the one-or-more rule is lost, 'needs a service before submit' fails:
           submit disabled with none checked, enabled after checking YouTube,
           disabled again after unchecking
         - if values are mangled, 'emits child, services and seconds' fails: pick
           Ben, check TikTok then YouTube, duration 90 → onSubmit({label:'',
           child_id:2, services:['youtube','tiktok'] (catalogue order), duration:5400})
         - if the bounds are not enforced client-side, 'rejects out-of-range
           minutes' fails: 0 and 1441 disable submit; 1 and 1440 enable it
         - if showLabel is ignored, 'label field only when asked' fails: showLabel
           false → no Label input; true → present, submit disabled until non-blank,
           label included in onSubmit
         - if initial values are not honoured, 'edit prefills' fails: initial
           {label 'YT', child_id 2, services ['tiktok'], duration 1800} → label 'YT',
           select value '2', only TikTok checked, minutes 30
         - if an empty roster renders a form, 'no children' fails: children [] →
           "Add a child first", no select

Wave 3
  t-8  Home: buttons, tap flow, 409 offer, ad-hoc form
       files:    web/src/lib/Home.svelte, web/src/lib/Home.test.ts
       covers:   c-2, c-3, c-7, c-8
       depends:  t-6, t-7
       description:
         load() fetches children, buttons, grants, services and per-child blocked
         views in parallel (blocked failure keeps the phase-03 semantics: blank +
         alert). Layout (locked home_layout): nav (Children, Buttons, Log out),
         MigrationBanner, <ActiveGrants>, then per child: h2, its buttons in stored
         order as <button data-button=id> with the first service's icon (iconUrl),
         label and duration, then <details> "Blocked services" holding the existing
         client/service list; after all children a <details> "Unblock something
         else" with <GrantForm showLabel=false submitText="Unblock">. unlock(childId,
         services, duration) — shared by taps and the form, no confirm (locked
         no_confirm): pending; createGrant; 201 applied=false → alert "Unlocked, but
         AdGuard did not accept it on: <failed>"; then await listGrants() so the
         panel shows the new row within one round trip; ApiError 409 → offer state
         {childId, services, duration, existing: active grants for that child whose
         services overlap} rendered as "<names> already unlocked for <child> —
         extend by <duration>?" with Extend/Cancel; Extend → extendGrant(each
         existing.id, duration), then createGrant(childId, services − ∪existing
         .services, duration) when non-empty (locked overlap_offer), then reload;
         other errors → messageFor. A 15 s setInterval polls listGrants: failure
         sets unreachable=true and keeps the last list, success clears it.
         signOut unchanged.
       contract:
         - if buttons render out of order or with the wrong icon, 'renders buttons
           per child in stored order' fails: buttons [id 2 ada youtube+tiktok, id 1
           ada roblox, id 3 ben tiktok] → section 1 lists data-button 2 then 1, section
           2 lists 3; button 2's img src is iconUrl(youtube icon); button text holds
           the label and "1 h"
         - if a tap needs confirmation or the panel does not refresh, 'tap unlocks
           and shows the countdown' fails: click button 2 → exactly one POST
           /api/v1/grants with body {child_id:1, services:['youtube','tiktok'],
           duration:3600} and no confirm dialog; the grants route then returns the new
           grant and [data-grant=<id>] with a .countdown appears without advancing
           timers beyond one tick
         - if failures are swallowed, 'failed tap shows an inline error' fails:
           POST → 502 adguard_unavailable → alert "Can't reach AdGuard Home …"; POST →
           201 {applied:false, failed:['Kid tablet']} → alert text contains "Kid
           tablet" and the grant still appears in the panel
         - if the 409 is not turned into an extend offer, '409 offers extend and
           creates the remainder' fails: active grant 7 (ada, ['youtube']); tap button
           2 → POST returns 409 {grant_id:7}; offer text contains "YouTube"; click
           Extend → POST /api/v1/grants/7/extend {duration:3600} then POST
           /api/v1/grants {child_id:1, services:['tiktok'], duration:3600}; Cancel
           makes no request
         - if the poll failure blanks the panel, 'unreachable keeps countdowns'
           fails: first grants poll ok, later ones throw TypeError; advance 15 s →
           [data-badge=unreachable] shown, [data-grant] still present, countdown
           still changes on the next second
         - if the blocked view leaves the disclosure, 'blocked list under a
           disclosure' fails: each section has a <details> whose summary is "Blocked
           services" and whose contents are the phase-03 client badges and service
           rows (existing assertions retained inside it)
         - if the ad-hoc form diverges from a tap, 'ad-hoc form unlocks like a tap'
           fails: with buttons [] open "Unblock something else", pick Ada, check
           TikTok, 30 minutes, Unblock → POST /api/v1/grants {child_id:1,
           services:['tiktok'], duration:1800} and the panel row appears
         - if the sign-out lock regresses (Home.svelte:6/9/15 survivors), 'log out
           locks while in flight' fails: Log out enabled on render; with the logout
           fetch deferred it is disabled after the click; after resolving 204 it is
           enabled again and onLogout was called once
         - if navigation is lost, 'Buttons nav routes' fails: clicking "Buttons"
           sets route 'buttons' and pathname '/buttons'

  t-9  Buttons settings page and App routes
       files:    web/src/lib/Buttons.svelte, web/src/lib/Buttons.test.ts,
                 web/src/App.svelte, web/src/App.test.ts
       covers:   c-4, c-7
       depends:  t-7
       description:
         Buttons.svelte: props {onBack}; loads listButtons, listChildren,
         listServices; lists each button ([data-button=id]: label, child name,
         service names, minutes) with Edit (swaps the row for <GrantForm showLabel
         initial=… submitText="Save">) and Delete; an "Add button" <GrantForm
         showLabel submitText="Add"> at the bottom. Every change builds the full
         list (existing items keep their ids, order unchanged, new item appended —
         locked button_order) and calls putButtons; the page re-renders from the
         response; failures show messageFor (a 422 echoes the server message).
         App.svelte: route 'buttons' && username → <Buttons onBack={() =>
         navigate('home')}>. App.test.ts adds the two survivor kills.
       contract:
         - if add does not PUT the whole ordered list, 'add PUTs the whole list'
           fails: existing [id 1, id 2]; fill Add (label 'TikTok 30', Ben, TikTok, 30)
           → PUT body buttons is [{id:1…},{id:2…},{label:'TikTok 30', child_id:2,
           services:['tiktok'], duration:1800}] with no id on the new one; the page
           then shows the id the server assigned
         - if edit drops the id, 'edit keeps the id' fails: Edit button 2, rename
           to 'YT', Save → PUT body item 2 carries id 2 and label 'YT', item 1 unchanged
         - if delete PUTs the wrong list, 'delete PUTs without it' fails: Delete
           button 1 → PUT body is [{id:2…}] and the row for 1 is gone after the
           response
         - if server errors are swallowed, '422 shows the server message' fails:
           PUT → 422 {message:'button 1: unknown service "nope"'} → alert text is that
           message and the list still shows the pre-PUT rows
         - if the route is not wired, 'reload on /buttons stays there' fails:
           signed in at /buttons → heading 'Buttons', pathname '/buttons'; Back →
           'Signed in as mum' at '/'
         - if refresh's catch block is emptied (App.svelte:15), '502 on /me at
           /children lands on login' fails: 502 from /me while at /children → Sign in
           button and location.pathname === '/login'
         - if the children route stops requiring a user (App.svelte:21), 'back to
           /children while signed out stays on login' fails: 401 on /me, then
           pushState '/children' + popstate → Sign in still shown, no 'Children'
           heading, fetch calls remain exactly ['/api/v1/me']

  t-10 Wire SPA, buttons store into main; binary proofs
       files:    cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go,
                 Makefile
       covers:   c-1, c-4, c-5, c-6
       depends:  t-2, t-5
       description:
         main.go: mux.Handle("/api/", apiHandler) replaces the /api/v1/ mount so
         any /api/* path answers from the API chain (its own 404) rather than the
         SPA; mux.Handle("/", spa.Handler(web.Dist())); Buttons: st in api.Deps.
         Makefile: test-go depends on build-web (the binary test needs a real
         dist); lint/fmt exclusions narrow from ./web/* to ./web/node_modules/* so
         web/embed.go is formatted and vetted. main_test.go: TestRun_SPA fails
         (not skips) with "run make build-web" when web/dist/index.html is absent.
       contract:
         - if the SPA is not served from the binary, TestRun_SPA fails: GET / →
           200 text/html whose body contains `<div id="app">`; GET /buttons → same
           body (client route); GET /assets/<first file listed in dist/assets> →
           200 with Cache-Control immutable; GET /manifest.webmanifest → 200
           application/manifest+json; GET /sw.js → 200 Cache-Control no-cache
         - if API misses fall through to the SPA, TestRun_SPA_ApiNotFound fails:
           GET /api/v1/nope with the session cookie → 404 JSON {error:"not_found"};
           without a cookie → 401; GET /api/v2/x → 404 and its body is not index.html
         - if /healthz is shadowed, the existing TestRun_Healthz fails
         - if buttons do not survive a restart end-to-end,
           TestRun_ButtonsSurviveRestart fails: run, login, POST child, PUT two
           buttons, stop (exit 0), start on the same data_dir, GET /api/v1/buttons
           with a fresh login → byte-equal to the earlier GET
         - if the buttons dep is unwired, TestRun_ButtonsWired fails: PUT with
           child_id 999 → 422 (validation reached the store-backed children read)
```

## Coverage

| criterion | tasks |
|---|---|
| c-1 buttons stored, ordered, survive restart, 422s | t-1, t-5, t-10 |
| c-2 Home buttons, tap → countdown ≤ 5 s, inline errors | t-4, t-8 |
| c-3 active panel, ticking countdown, extend/end, 409 → extend, leaves on expiry | t-4, t-6, t-8 |
| c-4 buttons settings page, PUT, render after restart | t-4, t-5, t-7, t-9, t-10 |
| c-5 embedded SPA, fallback, /api 404 against the binary | t-2, t-10 |
| c-6 manifest, SW shell-only, /api never cached, installable | t-2, t-3, t-10 |
| c-7 14 routed survivors killed | t-4 (api.ts, Login), t-8 (Home), t-9 (App) |
| c-8 ad-hoc grant form on Home | t-4, t-7, t-8 |

## Judgment calls

- **Services as a JSON text column, not a `button_services` table.** Order matters (first service = icon, locked `button_icon`) so a join table would need its own position column and fold logic; one column, one migration. Rejected the phase-04 style two-table layout.
- **PUT keeps ids via UPSERT, deletes the rest.** Rejected delete-all-and-reinsert (ids churn on every save, so the settings page and Home keys thrash) and a per-button CRUD API (four routes for a list the spec defines as one PUT).
- **Equivalent api.ts mutants are removed by simplification, not "killed".** `r !== 'home'` and the `typeof history/location/window` guards are redundant in a browser-only SPA whose every test is jsdom; deleting them makes the mutants disappear. The one truly equivalent survivor (pushState's ignored title string) gets a reasoned `Stryker disable next-line`. Rejected contorting tests to "cover" behaviour that has no observable effect.
- **Login.svelte gets a real change (`autocapitalize="none"`) to enter the mutation scope.** `dross verify` mutates the diff; an untouched file is never re-scored, so its four routed survivors could not be reported killed. Rejected a whitespace-only touch.
- **Go embed lives in `web/embed.go` with a committed `dist/.gitkeep`.** `//go:embed` cannot reach a parent directory, so the package must sit beside dist; the `.gitkeep` keeps a fresh clone compiling. Rejected committing built assets (dirty tree after every build) and a runtime `--web-dir` flag (speculative).
- **`TestRun_SPA` fails, not skips, without a built dist; `make test-go` gains `build-web`.** A skipping test would silently drop c-5's binary proof. The Dockerfile already builds web before Go, so nothing new is required there.
- **Hand-written `sw.js` in `public/`, no vite-plugin-pwa.** The criterion is shell-only precache + /api pass-through; ~40 lines of plain JS is testable by importing it with stubbed `self`/`caches`, adds no dependency, and stays outside Stryker's `src/**` glob. Cost: hashed `/assets/*` are runtime-cached rather than precached, so first offline load needs one prior online visit — acceptable for M1.
- **Home owns polling and the tap/409 flow; ActiveGrants and GrantForm are dumb components.** One `unlock()` shared by buttons and the ad-hoc form is what makes c-8 "the same behaviour as a button" true by construction. Rejected a separate `grantflow.ts` module (one more file and test for logic used by exactly one page).
- **Duration is entered in minutes via a numeric input, not a preset picker.** The picker is nicer on a phone, but the criterion only requires a duration inside 1 min–24 h; presets are M2 button-editor polish.
- **The blocked view stays inside Home under `<details>` rather than moving to its own component.** Locked layout puts it beneath each child's buttons; jsdom queries see `<details>` content regardless of open state, so the phase-03 assertions survive with one wrapper change.
- **`/api/` (not `/api/v1/`) is mounted on the API chain.** One-line change guarantees no `/api/*` path ever receives index.html, which is what "same origin as /api" plus "never cached" needs; `/api/v2/x` now 404s from the API mux instead of returning HTML.
- **Poll interval 15 s, not 5 s.** c-2's "within 5 s" is met by re-fetching grants immediately after the POST; the poll only catches expiry/ends made elsewhere and the local countdown already drops rows at zero.
