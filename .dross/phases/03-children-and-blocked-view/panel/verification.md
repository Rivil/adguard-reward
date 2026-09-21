# Verification-lens plan — 03-children-and-blocked-view

Method: for each criterion the ideal test was written first (which surface breaks, what
the test observes), then the smallest task that makes that test satisfiable. Every Go
API test runs the real store and the real `adguardtest` fake through the existing
`newHarness` in `internal/api/login_test.go`; every web test is a jsdom component test
with a stubbed `fetch`, as `App.test.ts` / `Login.test.ts` already do. The state
resolver and the migration-offer computation are pure functions with table tests so
gremlins has a tight target and the locked decisions are pinned by data, not prose.

The wire contract is fixed here so the Go and web halves can be built and tested
against it in parallel (waves 1–2 never wait on each other across the stack):

```
GET  /api/v1/children                 -> 200 {"children":[{"id":1,"name":"Ada","clients":["Kid phone","Kid tablet"]}]}
POST /api/v1/children {name,clients}  -> 201 {"id","name","clients"} | 400 bad_request | 409 conflict
PUT  /api/v1/children/{id}            -> 200 {"id","name","clients"} | 400 | 404 not_found | 409 conflict
DELETE /api/v1/children/{id}          -> 204 | 404 not_found
GET  /api/v1/clients                  -> 200 {"clients":[{"name","ids":[],"use_global_blocked_services":bool,"child":{"id","name"}|null}]}
GET  /api/v1/services                 -> 200 {"services":[{"id","name","icon"}]}          (icon = icon_svg base64 verbatim)
GET  /api/v1/children/{id}/blocked    -> 200 {"child":{"id","name"},
                                             "clients":[{"name","missing":bool,"uses_global":bool}],
                                             "services":[{"id","name","icon","state":"blocked|partial|unblocked","differs":[]}]}
                                         404 not_found | 502 adguard_unavailable
GET  /api/v1/migration                -> 200 {"global":[],"clients":[{"name","child":{"id","name"},"gains":[]}]}   (clients empty => no offer)
POST /api/v1/migration                -> 200 {"migrated":["Kid tablet"]} | 502 adguard_unavailable
```
`differs` on a partial service = the child's present clients on which the service is NOT
blocked (so phase 04's grant unblocks "everything not in differs"). Arrays are never null.

```
Phase 03-children-and-blocked-view — 10 tasks across 4 waves

Wave 1
  t-1  Children schema, store queries, Delete error test
       files:    internal/store/migrations/0002_children.sql, internal/store/children.go,
                 internal/store/children_test.go, internal/store/sessions_test.go
       covers:   c-1, c-8 (locked: client_identity)
       depends:  —
       desc:     0002_children.sql: children(id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE
                 COLLATE NOCASE, created_at INTEGER NOT NULL); child_clients(child_id INTEGER NOT
                 NULL REFERENCES children(id) ON DELETE CASCADE, client_name TEXT NOT NULL UNIQUE)
                 + index on child_id. Only names are stored (client_identity). children.go:
                 Child{ID int64, Name string, Clients []string}; sentinels ErrChildNotFound,
                 ErrDuplicateChild, ErrClientTaken. ListChildren(ctx) ([]Child, error) ordered
                 name NOCASE then id, clients sorted, Clients never nil; GetChild(ctx, id);
                 CreateChild(ctx, name, clients) (Child, error); UpdateChild(ctx, id, name,
                 clients) (Child, error) — full replace of name and client set in one tx (DELETE
                 child_clients WHERE child_id, re-INSERT); DeleteChild(ctx, id) error (ErrChildNotFound
                 when RowsAffected==0). UNIQUE violations are classified by the driver's constraint
                 text: "children.name" -> ErrDuplicateChild, "child_clients.client_name" ->
                 ErrClientTaken; the tx is rolled back so a half-applied update never lands.
                 Query code scans rows to memory before issuing the next query (single-connection
                 rule in store.go). sessions_test.go gains TestSessions_Delete for c-8.
       contract: - if the unique-name constraint or its classification breaks,
                   TestChildren_DuplicateName fails: CreateChild("Ada") then CreateChild("ada")
                   returns errors.Is(err, ErrDuplicateChild) and ListChildren has one row;
                   UpdateChild(ben.ID, "Ada", nil) on a second child also returns ErrDuplicateChild
                   and Ben's row is unchanged afterwards (GetChild still says "Ben")
                 - if a client can be mapped to two children, TestChildren_ClientTaken fails:
                   Ada has ["Kid phone"]; CreateChild("Ben", ["Kid phone","Old laptop"]) returns
                   ErrClientTaken and no "Ben" row exists (the tx rolled back, not just the
                   second insert); UpdateChild(ada.ID, "Ada", ["Kid phone","Kid phone"]) succeeds
                   with Clients == ["Kid phone"] (same-child re-assign and in-body duplicates
                   are not conflicts)
                 - if update is not a full replace, TestChildren_UpdateReplaces fails: Ada
                   ["Kid phone","Kid tablet"] -> UpdateChild(ada.ID, "Ada M", ["Old laptop"])
                   -> GetChild returns Name "Ada M", Clients exactly ["Old laptop"]; a direct
                   SELECT count(*) FROM child_clients WHERE child_id=ada.ID is 1
                 - if cascade or not-found handling breaks, TestChildren_Delete fails:
                   DeleteChild(ada.ID) then GetChild -> ErrChildNotFound, SELECT count(*) FROM
                   child_clients is 0, and "Kid phone" can now be given to Ben; DeleteChild(999)
                   -> ErrChildNotFound
                 - if rows do not persist, TestChildren_SurviveReopen fails: CreateChild x2,
                   Close, Open(same dir), ListChildren returns both with identical ids, names
                   and client lists; schema_migrations lists 0002_children.sql exactly once
                 - if ListChildren nulls or misorders, TestChildren_ListShape fails: empty store
                   returns a non-nil empty slice; "ben","Ada","Cy" come back Ada, ben, Cy; a
                   child with no clients has Clients == []string{} not nil
                 - if Store.Delete stops wrapping or inverts its check (c-8, survivor
                   7682485717754728 at sessions.go:79), TestSessions_Delete fails: Insert two
                   sessions, Delete(id1) -> nil and ListByUser lists only id2; then
                   s.DB().Exec("DROP TABLE sessions") and Delete(id2) -> err != nil with
                   strings.HasPrefix(err.Error(), "delete session: ") and errors.Unwrap(err) != nil

  t-2  adguard.MigrateFromGlobal: list + flag write
       files:    internal/adguard/clients.go, internal/adguard/clients_test.go
       covers:   c-7 (locked: global_list_clients)
       depends:  —
       desc:     Factor SetBlockedServices' locked read-modify-write into
                 updateClient(ctx, name, mutate func(data map[string]json.RawMessage) error)
                 (same rmw mutex, same ErrClientNotFound, same verbatim copy of every other
                 field). SetBlockedServices keeps its signature and behaviour on top of it.
                 New MigrateFromGlobal(ctx, clientName string, ids []string) error: one
                 read-modify-write that sets data["blocked_services"] = sorted, deduplicated,
                 never-null ids AND data["use_global_blocked_services"] = false, touching
                 nothing else. It never calls /control/blocked_services/set (the global list is
                 not this method's business). Existing SetBlockedServices tests must pass
                 unchanged after the refactor.
       contract: - if the flag flip or the list write is lost, TestMigrateFromGlobal_Body fails:
                   MigrateFromGlobal("Kid tablet", ["tiktok","roblox","tiktok"]) makes the fake's
                   LastUpdate() decode to name "Kid tablet" with data.blocked_services ==
                   ["roblox","tiktok"], data.use_global_blocked_services == false, and every
                   other key (safe_search, tags, blocked_services_schedule, upstreams_cache_size,
                   ids, ...) byte-for-byte equal to the fixture object (asAny comparison)
                 - if the global list is touched, TestMigrateFromGlobal_GlobalUntouched fails:
                   the fake's Requests() after the call contain exactly one GET /control/clients
                   and one POST /control/clients/update and nothing under
                   /control/blocked_services/
                 - if a missing client is written anyway, TestMigrateFromGlobal_NotFound fails:
                   MigrateFromGlobal("Nobody", ["tiktok"]) returns errors.Is(err,
                   ErrClientNotFound) and LastUpdate() is nil
                 - if the refactor breaks SetBlockedServices' serialisation, the existing
                   TestSetBlockedServices_Serialised fails: MaxInFlightUpdates() stays 1 under
                   parallel SetBlockedServices + MigrateFromGlobal on the same client (extend the
                   existing concurrency test to mix both writers)

  t-3  Pure blocked-state resolver
       files:    internal/api/blocked.go, internal/api/blocked_test.go
       covers:   c-4 (locked: cross_client_state, global_list_clients, dangling_client)
       depends:  —
       desc:     blocked.go holds only pure code this wave (the handler lands in t-7):
                 blockedView{Child childRef; Clients []blockedClient; Services []blockedService}
                 with json tags matching the wire contract; resolveBlocked(child store.Child,
                 res adguard.ClientsResult, catalogue []adguard.Service) blockedView.
                 For each of child.Clients in order: present iff a persistent client of that
                 name exists; uses_global copied from it; missing clients contribute nothing to
                 state. A present client's effective set = res.GlobalBlockedServices when
                 UseGlobalBlockedServices, else its own BlockedServices. For each catalogue
                 service in catalogue order: blocked when every present client's set contains
                 it, unblocked when none does (and when there are no present clients), partial
                 otherwise with differs = names of present clients whose set lacks it, in
                 child order; differs is []string{} otherwise. Ids blocked on a client but
                 absent from the catalogue are ignored (the catalogue is the view).
       contract: - if the three-way state rule drifts, TestResolveBlocked_Fixture fails: with the
                   embedded fixtures (Kid phone own [youtube,tiktok]; Kid tablet global
                   [tiktok,roblox]; catalogue youtube,tiktok,roblox) and child Ada [Kid phone,
                   Kid tablet]: youtube partial differs ["Kid tablet"]; tiktok blocked differs
                   []; roblox partial differs ["Kid phone"]; services come back in catalogue
                   order; clients [{Kid phone,false,false},{Kid tablet,false,true}]
                 - if global-list clients are read from their own (ignored) list instead of the
                   global one, TestResolveBlocked_GlobalList fails: Kid tablet alone with global
                   [tiktok,roblox] -> tiktok and roblox blocked, youtube unblocked; the same
                   client with global [] -> all three unblocked
                 - if a dangling client becomes an error or skews the state,
                   TestResolveBlocked_Missing fails: Ada [Kid phone, Ghost] -> Clients includes
                   {Ghost, missing:true, uses_global:false}, youtube and tiktok are blocked (not
                   partial), roblox unblocked, and no differs ever names Ghost
                 - if the empty case is wrong, TestResolveBlocked_NoPresentClients fails: a child
                   with [] and a child with only [Ghost] both yield every service unblocked with
                   differs == []string{} (not nil, so JSON is [] not null)
                 - if a null AdGuard list leaks, TestResolveBlocked_NullList fails: Old laptop
                   (blocked_services: null in the fixture) alone -> every service unblocked, no
                   panic

  t-4  Web API client: endpoints, types, settings route
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts
       covers:   c-5, c-6, c-7 (transport half)
       depends:  —
       desc:     Route gains 'settings' (pathname '/settings'); routeFor/navigate map it both
                 ways; Method gains 'PUT'. Types Child, ClientView, Service, ServiceState,
                 BlockedView, MigrationOffer exactly as in the wire contract. Functions:
                 listChildren(), createChild(name, clients), updateChild(id, name, clients),
                 deleteChild(id), listClients(), listServices(), childBlocked(id),
                 migrationOffer(), applyMigration(); iconUrl(icon) returns
                 'data:image/svg+xml;base64,' + icon. messageFor keeps its cases; add
                 'conflict' -> the server's message verbatim (it names the child/client).
       contract: - if a route or method is wrong, api.test.ts 'children endpoints' fails: each
                   function's recorded fetch has the exact URL and method — createChild ->
                   POST /api/v1/children with body {"name":"Ada","clients":["Kid phone"]};
                   updateChild(3, ...) -> PUT /api/v1/children/3; deleteChild(3) -> DELETE
                   /api/v1/children/3 resolving undefined on 204; childBlocked(3) -> GET
                   /api/v1/children/3/blocked; applyMigration -> POST /api/v1/migration with no
                   body; every call carries X-Requested-With
                 - if the settings route is unmapped, 'routing' fails: routeFor via navigate
                   ('settings') pushes '/settings' and route store reads 'settings'; a popstate
                   at '/settings' sets the store to 'settings', at '/x' to 'home'
                 - if the icon prefix drifts, 'iconUrl' fails: iconUrl('PHN2Zy8+') ===
                   'data:image/svg+xml;base64,PHN2Zy8+'
                 - if a 409 loses its message, 'messageFor conflict' fails: ApiError(409,
                   'conflict', 'a child named Ada already exists') -> that exact string

Wave 2 (depends t-1 / t-4)
  t-5  Children CRUD endpoints, ChildStore dep, main wiring
       files:    internal/api/children.go, internal/api/children_test.go, internal/api/api.go,
                 internal/api/errors.go, cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go
       covers:   c-1
       depends:  t-1
       desc:     api.go: ChildStore interface (ListChildren, GetChild, CreateChild, UpdateChild,
                 DeleteChild with store.Child / store sentinels), Deps.Children ChildStore, and
                 routes GET/POST /api/v1/children, PUT/DELETE /api/v1/children/{id}, all behind
                 requireSession. errors.go: CodeConflict = "conflict". children.go: childView
                 {id,name,clients}; body decoded as handleLogin does (16 KiB MaxBytesReader,
                 DisallowUnknownFields, single object); name TrimSpace'd, empty or > 64 runes
                 -> 400 bad_request; clients trimmed, empties dropped, de-duplicated, may be
                 empty. ErrDuplicateChild / ErrClientTaken -> 409 conflict with a message naming
                 the child or client; ErrChildNotFound -> 404 not_found; other store errors ->
                 500 "internal". CRUD never calls AdGuard. main.go passes Children: st.
                 main_test.go gains TestRun_ChildrenSurviveRestart modelled on
                 TestRun_SessionSurvivesRestart.
       contract: - if auth or CSRF gating is missing, TestChildren_Gated fails: GET /children
                   without a cookie is 401; POST with a cookie but no X-Requested-With is 403
                   and the store stays empty
                 - if the CRUD round-trip drifts, TestChildren_CRUD fails: POST {name:"Ada",
                   clients:["Kid phone"]} -> 201 with id>0 and the same name/clients; GET lists
                   exactly it ({"children":[...]}, never null when empty — raw body check on a
                   fresh store is {"children":[]}); PUT {name:"Ada M",clients:[]} -> 200 with
                   clients []; DELETE -> 204 then GET lists nothing and DELETE again -> 404
                   not_found
                 - if conflicts are not 409, TestChildren_Conflicts fails: second POST "Ada" ->
                   409 {error:"conflict"} whose message contains "Ada"; POST "Ben" with
                   ["Kid phone"] (owned by Ada) -> 409 whose message contains "Kid phone"; PUT
                   ben.id with name "Ada" -> 409; the store still has exactly the pre-call rows
                 - if validation is loose, TestChildren_Validation fails: {name:"  "} -> 400,
                   {name:"Ada",clients:["Kid phone"," Kid phone "]} -> 201 with clients ["Kid
                   phone"], a 65-rune name -> 400, an unknown field -> 400, PUT /children/abc
                   -> 404
                 - if CRUD starts depending on AdGuard, TestChildren_NoAdGuard fails: with
                   fake.SetStatus("/control/clients", 500) the whole CRUD sequence still
                   succeeds and fake.Requests() holds no path other than /control/login
                 - if the rows do not survive the process, TestRun_ChildrenSurviveRestart
                   fails: run, login, POST a child, stop with exit 0, run again on the same
                   data_dir and port, GET /children with the same cookie lists the child with
                   the same id and clients

  t-6  Home page: children with blocked services, error state
       files:    web/src/lib/Home.svelte, web/src/lib/Home.test.ts, web/src/App.test.ts
       covers:   c-6 (locked: global_list_clients)
       depends:  t-4
       desc:     Home keeps "Signed in as" and Log out, adds a "Children" button that
                 navigate('settings'). On mount: listChildren(), then childBlocked(id) for every
                 child (Promise.all); every mount re-fetches (no cache, no store). Renders one
                 <section data-child={id}> per child: h2 name; a client list with a "uses
                 global list" badge (data-badge="global") when uses_global and "missing" when
                 missing; a <ul> of services whose state is blocked or partial, each with
                 <img alt="" src={iconUrl(icon)}> and the name, partial ones suffixed
                 "— unblocked on <differs joined>" with data-state. No children -> "No children
                 yet". Any rejected fetch (ApiError or network) -> <p role="alert"
                 data-error={code|'network'}> "Can't reach AdGuard Home — showing nothing
                 rather than a stale list" and NO section renders, even if listChildren had
                 succeeded. Home.test.ts and App.test.ts switch to a URL-keyed fetch stub
                 (path -> Response factory) so extra requests from later components (t-9's
                 banner) cannot reorder a queue; App.test's 'log out' asserts the calls
                 include /api/v1/logout with POST rather than equal a fixed list.
       contract: - if the list is not per child or drops icons, Home.test 'lists blocked
                   services per child' fails: two children (Ada id 1, Ben id 2) with stubbed
                   /blocked answers render two sections; Ada's section lists YouTube and TikTok
                   (blocked) with img src 'data:image/svg+xml;base64,PHN2Zy8+' and omits Roblox
                   (unblocked); Ben's lists nothing and says nothing is blocked
                 - if partial drift is hidden, 'marks partial services' fails: a partial roblox
                   with differs ["Kid phone"] renders with data-state="partial" and text
                   containing "unblocked on Kid phone"
                 - if the global badge is dropped, 'shows the uses-global badge' fails: a client
                   {name:"Kid tablet",uses_global:true} renders an element with
                   data-badge="global" next to "Kid tablet"; a missing client renders "missing"
                 - if a stale list survives an error, 'AdGuard down shows an error state' fails:
                   /children 200 then /children/1/blocked 502 adguard_unavailable -> a
                   role="alert" with data-error="adguard_unavailable", and
                   queryAllByRole('list') is empty; a fetch that throws TypeError -> alert with
                   data-error="network"
                 - if the page caches, 'refetches on every mount' fails: render, unmount,
                   render again -> the stub counts /api/v1/children twice
                 - if navigation breaks, 'Children button goes to settings' fails: clicking
                   "Children" sets location.pathname to '/settings' and get(route) to 'settings'

Wave 3 (depends t-3 + t-5 / t-4 + t-6)
  t-7  Clients, services and blocked-view endpoints
       files:    internal/api/clients.go, internal/api/clients_test.go, internal/api/blocked.go,
                 internal/api/blocked_test.go, internal/api/api.go
       covers:   c-2, c-3, c-4
       depends:  t-3, t-5
       desc:     api.go: AdGuard interface gains Clients(ctx) (adguard.ClientsResult, error)
                 and Services(ctx) ([]adguard.Service, error) (already on *adguard.Client);
                 routes GET /api/v1/clients, GET /api/v1/services, GET
                 /api/v1/children/{id}/blocked behind requireSession. clients.go:
                 handleClients calls Clients() then ListChildren() and answers the wire shape
                 with child = the owning child or null; handleServices maps Services() 1:1.
                 blocked.go gains handleBlocked: GetChild (404 not_found), Clients(), Services(),
                 resolveBlocked -> 200. Any adguard error on these three routes -> 502
                 adguard_unavailable (logged Warn) — including ErrBadCredentials, which is
                 the service credential, not the parent's. Nothing is memoised; every request
                 goes to AdGuard.
       contract: - if /clients stops being live or gains a cache, TestClients_LiveEveryCall
                   fails: two GET /api/v1/clients leave exactly two GET /control/clients (and two
                   GET /control/blocked_services/get) in fake.Requests(); after
                   fake.MutateClient("Kid phone", ids -> ["10.0.0.9"]) the next GET shows the
                   new id
                 - if the child join is wrong, TestClients_ChildAssignment fails: Ada owns
                   ["Kid phone"]; the response has Kid phone.child == {id:ada, name:"Ada"},
                   Kid tablet.child == null (raw body contains "child":null), and
                   use_global_blocked_services true only for Kid tablet; the auto_client
                   "printer" is absent
                 - if /services vendors or caches, TestServices_Live fails: two GET
                   /api/v1/services leave exactly two GET /control/blocked_services/all; the
                   body lists youtube, tiktok, roblox in fixture order with icon "PHN2Zy8+";
                   after fake.SetResponse("/control/blocked_services/all", 200, <one-entry
                   doc>) the very next call returns one service
                 - if the view is wrong end-to-end, TestBlocked_View fails: Ada [Kid phone,
                   Kid tablet, Ghost] -> 200 whose services are youtube partial ["Kid tablet"],
                   tiktok blocked [], roblox partial ["Kid phone"], and clients include
                   {Ghost, missing:true}; the raw body has "differs":[] (never null); each call
                   records one GET /control/clients, one blocked_services/get and one
                   blocked_services/all
                 - if a dangling client errors, TestBlocked_MissingIsNotAnError fails: a child
                   with only ["Ghost"] -> 200 with every service unblocked
                 - if AdGuard failure is not 502, TestBlocked_AdGuardDown fails: with
                   fake.SetStatus("/control/clients", 500) each of /clients, /services (via
                   /control/blocked_services/all) and /children/{id}/blocked answers 502
                   {error:"adguard_unavailable"}; with the fake closed (transport error) the
                   same; fake.SetAuth(false) -> 502 as well, never 401
                 - if ids are not checked, TestBlocked_NotFound fails: /children/999/blocked ->
                   404 not_found and fake.Requests() records no /control/ hit for it

  t-8  Children settings page
       files:    web/src/lib/Settings.svelte, web/src/lib/Settings.test.ts, web/src/App.svelte,
                 web/src/App.test.ts
       covers:   c-5 (locked: dangling_client)
       depends:  t-4, t-6
       desc:     App.svelte routes 'settings' to <Settings onBack={() => navigate('home')} />
                 when signed in, and refresh() keeps the current non-login route on success
                 (navigate($route === 'login' ? 'home' : $route)) so a reload on /settings
                 stays there. Settings.svelte: load() = Promise.all(listChildren(),
                 listClients()); one <form data-child={id}> per child with a name <input
                 aria-label="Name">, a checkbox per AdGuard client (label = client name,
                 checked when in the child's clients; disabled with the text "assigned to
                 <other child>" when client.child is another child), and for each of the
                 child's clients absent from /clients a <s data-missing> entry with a "Remove"
                 button that drops it from the pending list; "Save" -> updateChild(id, name,
                 clients) then load(); "Delete" -> deleteChild(id) then load(). An "Add child"
                 form -> createChild(name, []) then load(). Every mutation re-renders from the
                 server's answer, never from local state. ApiError -> <p role="alert"
                 data-error={code}>messageFor(e)</p>. "Back" -> onBack().
       contract: - if assignment state is wrong, Settings.test 'renders ownership' fails: Ada
                   [Kid phone], Ben []; /clients marks Kid phone child Ada, Kid tablet null ->
                   in Ben's form the "Kid phone" checkbox is disabled and its label text
                   contains "assigned to Ada"; in Ada's form it is enabled and checked; "Kid
                   tablet" is enabled and unchecked in both
                 - if a save sends the wrong body, 'assign by checkbox' fails: ticking "Kid
                   tablet" in Ada's form and clicking Save issues PUT /api/v1/children/1 with
                   body {"name":"Ada","clients":["Kid phone","Kid tablet"]}
                 - if renaming or deleting is wired wrong, 'rename and delete' fails: typing
                   "Ada M" then Save -> PUT body name "Ada M"; clicking Delete on Ben ->
                   DELETE /api/v1/children/2 and, after the stubbed re-fetch omits Ben, no
                   form with data-child="2" remains
                 - if a missing client is pruned or hidden, 'missing client is struck through'
                   fails: Ada [Kid phone, Ghost] with /clients lacking Ghost renders <s
                   data-missing> "Ghost" with a Remove button and no checkbox for Ghost; without
                   clicking Remove, Save sends clients ["Kid phone","Ghost"] (nothing
                   auto-pruned); after Remove, Save sends ["Kid phone"]
                 - if create is broken, 'add child' fails: typing "Cy" into the add form and
                   submitting issues POST /api/v1/children {"name":"Cy","clients":[]} and the
                   form for Cy appears once the stubbed re-fetch lists it
                 - if the page shows optimistic state, 'renders the server after save' fails:
                   the stubbed GET after PUT returns name "Ada Server"; the DOM shows "Ada
                   Server", not what was typed
                 - if 409 is swallowed, 'shows a conflict' fails: PUT answering 409
                   {error:"conflict",message:"client Kid phone is already assigned"} renders
                   role="alert" with that message and data-error="conflict"
                 - if reload bounces, App.test 'reload on /settings stays on settings' fails:
                   history.replaceState to '/settings' before render, /me 200 -> the Settings
                   heading renders and location.pathname is still '/settings'; a 401 at
                   /settings lands on login
                 - if the signed-out guard is bypassed, App.test 'settings needs a session'
                   fails: at '/settings' with /me 401 the Sign in button renders and no
                   /api/v1/children fetch is made

  t-9  Global-list migration banner
       files:    web/src/lib/MigrationBanner.svelte, web/src/lib/MigrationBanner.test.ts,
                 web/src/lib/Home.svelte, web/src/lib/Home.test.ts
       covers:   c-7 (locked: migration_offer_ux)
       depends:  t-4, t-6
       desc:     MigrationBanner.svelte: on mount migrationOffer(); renders nothing when
                 clients is empty or the call fails (the home error state already covers
                 AdGuard down). Otherwise <aside role="status" data-migration> listing, per
                 client, "<name> (<child name>) gains <ids joined>", with "Migrate" and "Not
                 now". Migrate -> applyMigration() then onMigrated() and the banner hides;
                 "Not now" -> a component-local dismissed = true (no storage; a fresh mount —
                 the next login — shows it again). Home.svelte mounts <MigrationBanner
                 onMigrated={load} /> above the children; Home.test.ts stubs /api/v1/migration
                 with an empty offer by default.
       contract: - if the offer text is wrong, MigrationBanner.test 'shows who gains what'
                   fails: offer {global:[tiktok,roblox], clients:[{name:"Kid tablet",
                   child:{id:1,name:"Ada"}, gains:["roblox","tiktok"]}]} renders text
                   containing "Kid tablet (Ada) gains roblox, tiktok"; an empty clients list
                   renders no element with data-migration
                 - if something is written without confirm, 'nothing written until Migrate'
                   fails: after render and a tick the stub has seen only GET /api/v1/migration
                   (no POST); clicking Migrate issues exactly one POST /api/v1/migration and
                   then calls onMigrated once and the banner is gone
                 - if dismissal persists, 'Not now hides for this mount only' fails: clicking
                   "Not now" removes the banner and issues no POST; unmount and re-render with
                   the same offer shows it again; sessionStorage/localStorage are untouched
                   (length 0)
                 - if Home stops hosting it, Home.test 'hosts the migration banner' fails:
                   with a non-empty stubbed offer the home page shows data-migration; with the
                   default empty offer it does not

Wave 4 (depends t-2 + t-7)
  t-10 Migration offer and apply endpoints
       files:    internal/api/migration.go, internal/api/migration_test.go, internal/api/api.go
       covers:   c-7 (locked: global_list_clients, migration_offer_ux)
       depends:  t-2, t-7
       desc:     api.go: AdGuard interface gains MigrateFromGlobal(ctx, name, ids) error;
                 routes GET and POST /api/v1/migration behind requireSession. migration.go:
                 pure migrationOffer(children []store.Child, res adguard.ClientsResult)
                 []migrationClient — empty when res.GlobalBlockedServices is empty; otherwise
                 one entry per mapped persistent client with UseGlobalBlockedServices, in
                 children then client order, gains = sorted global ids not in the client's own
                 list, target = sorted union(own, global). Unmapped clients never appear.
                 handleMigrationOffer: ListChildren + Clients -> {global, clients}.
                 handleMigrationApply: recompute the offer from a fresh Clients() read, call
                 MigrateFromGlobal(name, target) for each entry, 200 {migrated:[names]}; the
                 first adguard error -> 502 adguard_unavailable (names already migrated are
                 simply no longer offered next time). No writes when the offer is empty.
       contract: - if the offer rule drifts, TestMigrationOffer_Table fails: children Ada [Kid
                   phone, Kid tablet], nobody owns Old laptop; fixture global [tiktok,roblox];
                   Old laptop mutated to use_global=true -> offer has exactly one entry {Kid
                   tablet, child Ada, gains [roblox,tiktok]}; with global [] (SetResponse on
                   /control/blocked_services/get) the offer is empty even though Kid tablet
                   still uses the global list; a Kid tablet whose own list is [roblox] gains
                   only [tiktok]
                 - if GET writes anything, TestMigration_OfferIsReadOnly fails: GET
                   /api/v1/migration -> 200 with the entry above and fake.LastUpdate() == nil
                   and no POST in fake.Requests()
                 - if apply writes the wrong clients or touches the global list,
                   TestMigration_Apply fails: POST /api/v1/migration -> 200
                   {migrated:["Kid tablet"]}; fake.Requests() holds exactly one POST
                   /control/clients/update, whose body names Kid tablet with blocked_services
                   [roblox,tiktok] and use_global_blocked_services false; no request to
                   /control/blocked_services/set; the next GET /control/clients from the fake
                   shows Kid tablet migrated and Old laptop (unmapped) still use_global=true;
                   a second GET /api/v1/migration now returns clients []
                 - if apply is not idempotent-safe, TestMigration_ApplyEmpty fails: with no
                   child mapped to a global-list client POST -> 200 {migrated:[]} and no
                   /control/clients/update is recorded
                 - if AdGuard failure is not 502, TestMigration_AdGuardDown fails:
                   fake.SetStatus("/control/clients/update", 500) -> POST answers 502
                   adguard_unavailable; fake.SetStatus("/control/clients", 500) -> GET answers
                   502
                 - if CSRF is skipped, TestMigration_CSRF fails: POST without X-Requested-With
                   is 403 and LastUpdate() is nil
```

## Coverage

| criterion | tasks |
|---|---|
| c-1 | t-1 (schema, uniqueness, reopen), t-5 (endpoints, 409s, process restart) |
| c-2 | t-7 |
| c-3 | t-7 |
| c-4 | t-3 (pure resolver), t-7 (endpoint, missing, 502) |
| c-5 | t-4 (transport), t-8 (page) |
| c-6 | t-4 (transport), t-6 (page) |
| c-7 | t-2 (AdGuard write), t-4 (transport), t-9 (banner), t-10 (offer/apply endpoints) |
| c-8 | t-1 (TestSessions_Delete) |

Locked decisions: cross_client_state -> t-3/t-7; global_list_clients -> t-3, t-6 (badge),
t-10; migration_offer_ux -> t-9; dangling_client -> t-1 (no pruning in store), t-3, t-7,
t-8; client_identity -> t-1 (schema stores names only).

## Judgment calls

- **Wire contract pinned in the plan, not discovered during execution.** Go and web halves
  are tested against the same JSON shapes so t-4/t-6 can run before any Go endpoint
  exists; rejected "web waits for the API" because it would serialise the whole phase.
- **State resolver is a pure function (t-3) tested apart from the handler.** Rejected
  testing the rule only through HTTP: the locked cross_client_state rule has five edge
  cases (global list, empty global, missing, no present clients, null list) and each is a
  one-line table row this way — and a tight gremlins target.
- **`differs` = present clients on which the service is NOT blocked.** Rejected "the
  minority side" (ambiguous at 2 clients) and "clients that block it" (phase 04's grant
  needs the remainder, which is the complement of this list either way). Pinned by the
  fixture test so a later reader cannot silently flip it.
- **No present clients => unblocked.** Both "every client blocks" and "none does" are
  vacuously true; chose unblocked because nothing is reachable-blocked. Pinned by test.
- **CRUD never consults AdGuard.** A child can be created and edited while AdGuard is down;
  unknown client names become "missing" in the view, which is exactly the dangling_client
  decision. Rejected validating names on POST/PUT — it would make the settings page
  unusable during an outage and add a second source of truth for "exists".
- **Uniqueness is DB constraints classified by error text, inside one tx.** Rejected
  SELECT-then-INSERT pre-checks as the source of truth: the single-connection pool makes
  them race-free, but the constraint is what actually guarantees the invariant, and the
  test asserts the rollback (no half-applied child) either way.
- **Child names unique NOCASE.** The spec says "unique"; "Ada" and "ada" as two children
  is a foot-gun on a phone keyboard. Pinned by TestChildren_DuplicateName.
- **Migration is its own GET/POST pair, not a field on /clients.** "Nothing is written
  without the confirm" is one assertion on LastUpdate()==nil after GET; folding the offer
  into /clients would tie the banner to the clients page and blur that test.
- **Migration target list = union(own list, global).** A global-list client's own list is
  ignored by AdGuard today, but dropping it on migration would silently lose data the
  parent once entered. "gains" shows only the global ids that are new.
- **Apply recomputes from a fresh read instead of trusting a client-sent list.** Rejected
  POSTing the shown offer back: it reintroduces a stale-write window and a bigger request
  surface for no gain; the response says exactly what was migrated.
- **"Not now" is component state, no storage.** The locked decision forbids a persisted
  flag; sessionStorage would survive logout+login in the same tab and violate "reappears
  on next login". A reload also re-shows it, which is stricter than required and cheaper
  than clearing storage on logout.
- **t-10 sits in wave 4 only because it edits api.go**, which t-7 edits in wave 3. Rejected
  folding it into t-7 (seven files, three surfaces) and rejected stub routes in t-5.
- **t-8 (Settings) waits for t-6 (Home)** only to avoid two wave-2 tasks editing
  App.test.ts; t-6 converts the App tests to a URL-keyed stub that later tasks extend.
- **Blocked-services error on Home hides everything, not just the failed child.** The
  criterion forbids a stale list; a half-rendered page with one child's real data and
  another's error would read as "Ben is unblocked", which is the exact lie to avoid.
