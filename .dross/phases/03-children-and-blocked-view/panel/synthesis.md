# Synthesis — 03-children-and-blocked-view

Cold judge over risk.md, mvp.md, verification.md. Every file path below was checked
against the tree (`internal/`, `cmd/`, `web/src`); new files are marked (new).

## Scores

| draft | criteria coverage | test-contract specificity | granularity | wave correctness |
|---|---|---|---|---|
| risk | 8/8, every locked decision has an owning test; c-7 spread over 5 tasks | Highest edge density (NOCASE, race under -race, per-name fault injection, partial migration, double submit) but the plan-echo/409 migration protocol adds contract lines the spec never asked for | 10 tasks, 3 waves; pure `internal/blocked` package is the best cut in any draft; t-5 spans 3 packages | **Broken twice**: t-6 and t-7 both edit `web/src/App.svelte` in wave 2 (placeholder then replacement), t-8 and t-9 both edit `internal/api/api.go` in wave 3 |
| mvp | 8/8; c-1 restart proof lands last (t-4, wave 3) | Concrete and testable, but no pure fold test, no null-list case, no NOCASE, no "nothing auto-pruned on Save" line | 6 tasks; t-4 (blocked + migration + main wiring, 5 files) and t-6 (Home + banner + routing) are two-commit tasks each | Correct (one `api.go` owner per wave), but `main.go` wiring lands in wave 3 so wave-2 registers children routes over a nil store; only draft to notice `login_test.go` harness needs `Children: st` |
| verification | 8/8 with an explicit locked-decision → test map | Best: wire contract pinned up front, raw-body assertions (`"child":null`, `"differs":[]`), per-call AdGuard hit counts, CRUD-never-calls-AdGuard, CSRF on migration | 10 tasks, 4 waves, every task one surface; App.test.ts conflict avoided by t-8 depending on t-6 | One flaw: t-3 (`internal/api/blocked.go`, wave 1, no deps) takes `store.Child`, a type t-1 creates — it cannot compile until t-1 lands. Otherwise correct; t-10 in wave 4 is the only serialisation cost |

**Skeleton: verification.** It has the sharpest contracts and the only wave graph
without a same-file collision; its one dependency slip is fixed by grafting risk's
`internal/blocked` package (input is `Mapped []string`, no store import), which also
gives gremlins a pure target. Risk's structural extras (fake `SetUpdateStatus`,
partial-failure test, NOCASE, race test, `login_test.go`-free) and mvp's harness line
are grafted below.

## Merged plan

Wire contract (verification, adopted verbatim; the Go and web halves test against it
independently so waves 1–2 never wait across the stack):

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
POST /api/v1/migration  (no body)     -> 200 {"migrated":["Kid tablet"]} | 502 adguard_unavailable
```
`differs` on a partial service = the child's present clients on which the service is
NOT blocked. Arrays are never null. Every error uses the existing envelope
`{error, message}`; `CodeConflict = "conflict"` is new.

Phase 03-children-and-blocked-view — 10 tasks across 4 waves

### Wave 1

  t-1  Children schema, store queries, c-8 Delete error test   [verification+risk+mvp]
       files:    internal/store/migrations/0002_children.sql (new),
                 internal/store/children.go (new), internal/store/children_test.go (new),
                 internal/store/sessions_test.go
       covers:   c-1, c-8   (locked: client_identity, dangling_client — no pruning in store)
       depends_on: []
       description:
         0002_children.sql: children(id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE
         COLLATE NOCASE, created_at INTEGER NOT NULL); child_clients(client_name TEXT
         PRIMARY KEY, child_id INTEGER NOT NULL REFERENCES children(id) ON DELETE CASCADE)
         + index on child_id. Only persistent-client names are stored. children.go:
         Child{ID int64, Name string, Clients []string}; sentinels ErrChildNotFound,
         ErrNameTaken, and typed *ErrClientTaken{Client, ChildID, ChildName}. ListChildren(ctx)
         ([]Child, error) ordered id ASC, two queries with the first fully scanned and closed
         before the second (single-connection rule in store.go); GetChild(ctx, id);
         CreateChild(ctx, name, clients) (Child, error); UpdateChild(ctx, id, name, clients)
         (Child, error) — full replace of name and client set (DELETE child_clients WHERE
         child_id, re-INSERT); DeleteChild(ctx, id) (bool, error) like DeleteByUser.
         Create/Update run in one BeginTx: SELECT the name conflict (excluding own id) →
         ErrNameTaken, SELECT each client's owner (excluding own id) → *ErrClientTaken naming
         the owner, then write; the UNIQUE/PK constraints stay as the safety net and any
         constraint error still rolls the tx back. Clients are trimmed, empties dropped,
         sorted, deduped before the write; returned sorted and never nil. sessions_test.go
         gains TestSessions_DeleteExecError for c-8.
       test_contract:
         - if the UNIQUE on children.name or its NOCASE collation is dropped,
           TestChildren_NameUnique fails: CreateChild("Ada") then CreateChild("ada") returns
           errors.Is(err, ErrNameTaken) and ListChildren has one row; UpdateChild(ben.ID,
           "Ada", nil) also returns ErrNameTaken and GetChild(ben.ID) still says "Ben"
         - if a client can be mapped to two children, TestChildren_ClientOneOwner fails: Ada
           has ["Kid phone"]; CreateChild("Ben", ["Kid phone","Old laptop"]) returns
           *ErrClientTaken with ChildName "Ada" and Client "Kid phone", and no "Ben" row
           exists (the tx rolled back, not just the second insert); PRAGMA index_list shows
           child_clients.client_name unique
         - if UpdateChild is not atomic, TestChildren_UpdateRollsBack fails: UpdateChild(ada.ID,
           "Renamed", ["<client owned by Ben>"]) returns *ErrClientTaken and GetChild(ada.ID)
           still has name "Ada" and her previous clients
         - if the self-exclusion is missing, TestChildren_UpdateSameName fails: UpdateChild(ada.ID,
           "Ada", ada.Clients) succeeds; UpdateChild(ada.ID, "Ada", ["Kid phone","Kid phone"])
           where Ada already owns "Kid phone" succeeds with Clients == ["Kid phone"]
         - if update is not a full replace, TestChildren_UpdateReplaces fails: Ada ["Kid phone",
           "Kid tablet"] → UpdateChild(ada.ID, "Ada M", ["Old laptop"]) → GetChild returns Name
           "Ada M", Clients exactly ["Old laptop"]; SELECT count(*) FROM child_clients WHERE
           child_id=ada.ID is 1
         - if ON DELETE CASCADE or foreign_keys is lost, TestChildren_DeleteFrees fails:
           DeleteChild(ada.ID) returns (true, nil), SELECT count(*) FROM child_clients WHERE
           child_id=ada.ID is 0, GetChild(ada.ID) → ErrChildNotFound, and CreateChild("Cy",
           ["Kid phone"]) then succeeds; DeleteChild(999) returns (false, nil)
         - if the payload is not normalised, TestChildren_ClientsNormalised fails:
           CreateChild("A", [" Kid phone", "Kid phone", "", "Kid tablet"]) stores exactly
           ["Kid phone", "Kid tablet"] in that order
         - if ListChildren nulls or misorders, TestChildren_ListShape fails: an empty store
           returns a non-nil empty slice; children created in order "ben","Ada","Cy" come
           back in that order; a child with no clients has Clients == []string{} not nil
         - if a query is issued while rows are open, TestChildren_ListRace fails under go test
           -race: 10 goroutines calling ListChildren while another does 20 CreateChild/
           DeleteChild cycles complete within 5s with no error (a nested query on the single
           connection deadlocks)
         - if rows do not persist, TestChildren_Persist fails: CreateChild x2, Close, Open on
           the same dir, ListChildren returns both with identical ids, names and client
           lists; schema_migrations lists 0002_children.sql exactly once
         - if an unknown id is not signalled, TestChildren_NotFound fails: GetChild(999) and
           UpdateChild(999, ...) return ErrChildNotFound
         - if the wrap at sessions.go:79 is removed (survivor 7682485717754728),
           TestSessions_DeleteExecError fails: Insert two sessions, Delete(id1) → nil and
           ListByUser lists only id2; then s.DB().Exec("DROP TABLE sessions") and Delete(id2)
           → err != nil with strings.HasPrefix(err.Error(), "delete session: "),
           errors.Unwrap(err) != nil and the text containing "no such table"

  t-2  adguard.MigrateFromGlobal + fake per-client update fault   [verification+risk; mvp]
       files:    internal/adguard/clients.go, internal/adguard/clients_test.go,
                 internal/adguard/adguardtest/server.go,
                 internal/adguard/adguardtest/server_test.go
       covers:   c-7   (locked: global_list_clients)
       depends_on: []
       description:
         Factor SetBlockedServices' locked read-modify-write into updateClient(ctx, name,
         mutate func(data map[string]json.RawMessage) error) (same c.rmw mutex, same
         ErrClientNotFound, same verbatim copy of every other field). SetBlockedServices
         keeps its signature and behaviour on top of it; its existing tests pass unchanged.
         New MigrateFromGlobal(ctx, clientName string, ids []string) error: one RMW that sets
         data["blocked_services"] = sorted, deduplicated, never-null ids AND
         data["use_global_blocked_services"] = false, touching nothing else. It never calls
         /control/blocked_services/set. adguardtest gains SetUpdateStatus(name string, code
         int): handleUpdate answers code for that client name only (0 clears), so a
         multi-client write can fail on the second client.
       test_contract:
         - if the flag flip or the list write is lost, TestMigrateFromGlobal_Body fails:
           MigrateFromGlobal("Kid tablet", ["tiktok","roblox","tiktok"]) makes the fake's
           LastUpdate() decode to name "Kid tablet" with data.blocked_services ==
           ["roblox","tiktok"], data.use_global_blocked_services == false, and every other
           key (safe_search, tags, blocked_services_schedule, upstreams_cache_size 0, ids,
           ...) byte-for-byte equal to the fixture object
         - if the shared helper drops fields, the existing TestSetBlockedServices_Preserves
           still fails: the update body for "Kid phone" carries future_field 42 and the
           unchanged use_global_blocked_services false
         - if the global list is touched, TestMigrateFromGlobal_GlobalUntouched fails: the
           fake's Requests() after the call contain exactly one GET /control/clients and one
           POST /control/clients/update and nothing under /control/blocked_services/; GET
           /control/blocked_services/get still returns ["tiktok","roblox"]
         - if a missing client is written anyway, TestMigrateFromGlobal_NotFound fails:
           MigrateFromGlobal("Nobody", ["tiktok"]) returns errors.Is(err, ErrClientNotFound)
           and LastUpdate() is nil; SetResponse("/control/clients", 200, {"clients":[]})
           then migrating "Kid tablet" likewise
         - if the refactor breaks serialisation, TestSetBlockedServices_Serialised (extended
           to mix both writers) fails: Hang("/control/clients/update", 30ms) with 10
           goroutines mixing SetBlockedServices and MigrateFromGlobal leaves
           MaxInFlightUpdates() == 1
         - if the fake's per-name fault is wired wrong, TestFake_SetUpdateStatus fails:
           SetUpdateStatus("Kid tablet", 500) makes an update to "Kid tablet" 500 and to
           "Kid phone" 200; SetUpdateStatus("Kid tablet", 0) clears it

  t-3  Pure blocked-state fold and migration offer   [risk; verification]
       files:    internal/blocked/blocked.go (new), internal/blocked/blocked_test.go (new)
       covers:   c-4, c-7   (locked: cross_client_state, global_list_clients, dangling_client)
       depends_on: []
       description:
         Package blocked, no I/O and no store import (so it compiles in wave 1). Constants
         StateBlocked/StatePartial/StateUnblocked. Input{Clients []adguard.PersistentClient,
         Global []string, Services []adguard.Service, Mapped []string}. Compute(Input)
         View{Clients []ClientState{Name, Missing, UsesGlobal}, Services []ServiceState{ID,
         Name, Icon, State, Differs}}. For each Mapped name in order: present iff a
         persistent client of that name exists; Missing otherwise and excluded from the
         fold; a UsesGlobal client's effective set is Global, else its own BlockedServices
         (nil-safe). For each catalogue service in catalogue order: blocked when every
         present client's set contains it, unblocked when none does (and when there are no
         present clients — no vacuous truth), partial otherwise with Differs = names of
         present clients whose set lacks it, sorted by name; Differs is []string{} otherwise.
         Ids blocked on a client but absent from the catalogue are ignored (the catalogue is
         the view). Offer(clients, global, mapped) []Step{Client, Gains, Target}: empty when
         global is empty; one step per mapped, present client with UseGlobalBlockedServices
         true, in mapped order; Gains = sorted global ids not in the client's own list,
         Target = sorted union(own, global); a client with Gains empty is still a step (its
         flag must flip). Slices never nil.
       test_contract:
         - if the three-way rule drifts, TestCompute_Fixture fails: Mapped ["Kid phone","Kid
           tablet"] over the embedded fixtures (Kid phone own [youtube,tiktok]; Kid tablet
           global [tiktok,roblox]; catalogue youtube,tiktok,roblox): youtube partial Differs
           ["Kid tablet"]; tiktok blocked Differs []; roblox partial Differs ["Kid phone"];
           services in catalogue order; Clients [{Kid phone,false,false},{Kid tablet,false,true}]
         - if global-list clients are read from their own (ignored) list, TestCompute_UsesGlobal
           fails: Kid tablet alone with Global [tiktok,roblox] → tiktok and roblox blocked,
           youtube unblocked; the same client with Global [] → all three unblocked
         - if missing clients are folded, TestCompute_MissingExcluded fails: Mapped ["Kid
           phone","Ghost"] gives youtube and tiktok blocked (not partial), roblox unblocked,
           Ghost only in Clients with Missing true, and no Differs ever names Ghost
         - if the fold is vacuous, TestCompute_NoPresentClients fails: Mapped [] and Mapped
           ["Ghost"] both yield every service unblocked with Differs == []string{} (not nil)
         - if a null AdGuard list leaks, TestCompute_NullList fails: Mapped ["Old laptop"]
           (fixture blocked_services null) gives every service unblocked and no panic
         - if ordering is nondeterministic, TestCompute_Order fails: services come back in
           catalogue order regardless of client list order, Differs sorted by name
           regardless of Mapped order
         - if unknown ids leak into the view, TestCompute_UnknownID fails: a client blocking
           "zzz-new" yields exactly the three catalogue services and nothing else
         - if the offer targets the wrong clients, TestOffer_Scope fails: Mapped ["Kid tablet",
           "Kid phone","Ghost"] with Global [tiktok,roblox] gives exactly one step (Kid
           tablet: Gains ["roblox","tiktok"], Target ["roblox","tiktok"]); Global [] gives no
           steps; an unmapped global client gives no steps
         - if the target drops the client's own list, TestOffer_Union fails: a global client
           whose own list is ["youtube"] with Global ["tiktok"] yields Gains ["tiktok"],
           Target ["tiktok","youtube"]; one whose list already holds every global id yields
           a step with Gains [] and Target equal to its list

  t-4  Web API client: endpoints, types, children route, dismissal store   [verification+risk+mvp]
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts
       covers:   c-5, c-6, c-7 (transport half)   (locked: migration_offer_ux)
       depends_on: []
       description:
         Method gains 'PUT'. Route gains 'children' (pathname '/children'); routeFor/navigate
         map it both ways. Types Child, ClientView, Service, ServiceState, BlockedView,
         MigrationOffer exactly as in the wire contract. Functions: listChildren(),
         createChild(name, clients), updateChild(id, name, clients), deleteChild(id),
         listClients(), listServices(), childBlocked(id), migrationOffer(), applyMigration();
         iconUrl(icon) returns 'data:image/svg+xml;base64,' + icon. messageFor keeps its
         cases; adds 'conflict' → the server's message verbatim (it names the child/client).
         Exported writable store migrationDismissed (false), reset to false inside login()
         on success and inside logout() — the "not now" is per login, nothing persisted.
       test_contract:
         - if a route or method is wrong, api.test.ts 'children endpoints' fails: each
           function's recorded fetch has the exact URL and method — createChild → POST
           /api/v1/children with body {"name":"Ada","clients":["Kid phone"]}; updateChild(3,
           "A", ["x"]) → PUT /api/v1/children/3 with body {"name":"A","clients":["x"]};
           deleteChild(3) → DELETE /api/v1/children/3 resolving undefined on 204;
           listClients → GET /api/v1/clients; listServices → GET /api/v1/services;
           childBlocked(3) → GET /api/v1/children/3/blocked; migrationOffer → GET
           /api/v1/migration; applyMigration → POST /api/v1/migration with no body; every
           call carries X-Requested-With: adguard-reward
         - if the children route is unmapped, 'routing' fails: navigate('children') pushes
           '/children' and the route store reads 'children'; a popstate at '/children' sets
           the store to 'children', at '/x' to 'home'
         - if the icon prefix drifts, 'iconUrl' fails: iconUrl('PHN2Zy8+') ===
           'data:image/svg+xml;base64,PHN2Zy8+'
         - if a 409 loses its message, 'messageFor conflict' fails: ApiError(409, 'conflict',
           'a child named Ada already exists') → that exact string
         - if the dismissal is not reset on login, 'login resets migrationDismissed' fails:
           migrationDismissed.set(true), login() with a 204 leaves get(migrationDismissed)
           false; logout() likewise
         - if the 401 hook regresses, the existing '401 calls onUnauthorized' test still
           fails for childBlocked(1) against a 401

### Wave 2

  t-5  Children CRUD endpoints, Deps/interface widening, main wiring, restart proof   [verification+risk; mvp]
       files:    internal/api/children.go (new), internal/api/children_test.go (new),
                 internal/api/api.go, internal/api/errors.go, internal/api/login_test.go,
                 cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go
       covers:   c-1
       depends_on: [t-1, t-2]
       description:
         errors.go: CodeConflict = "conflict". api.go: ChildStore interface (ListChildren,
         GetChild, CreateChild, UpdateChild, DeleteChild over store.Child / store sentinels),
         Deps.Children ChildStore, and the AdGuard interface widened once here to Clients(ctx)
         (adguard.ClientsResult, error), Services(ctx) ([]adguard.Service, error) and
         MigrateFromGlobal(ctx, name, ids) error (all on *adguard.Client after t-2), so t-7
         and t-10 only append route lines. Routes GET/POST /api/v1/children, GET/PUT/DELETE
         /api/v1/children/{id}, all behind requireSession. children.go: childView
         {id,name,clients}; body decoded as handleLogin does (16 KiB MaxBytesReader,
         DisallowUnknownFields, single object); name TrimSpace'd, empty or > 64 runes → 400
         bad_request; clients an array of strings, trimmed, empties dropped, deduplicated,
         each ≤ 256 chars, may be empty. ErrNameTaken / *ErrClientTaken → 409 conflict with a
         message naming the child (and client); ErrChildNotFound and a non-numeric id → 404
         not_found; other store errors → 500. CRUD never calls AdGuard. login_test.go's
         newHarness passes Children: st. main.go passes Children: st. main_test.go gains
         TestRun_ChildrenSurviveRestart modelled on TestRun_SessionSurvivesRestart.
       test_contract:
         - if auth or CSRF gating is missing, TestChildren_Gated fails: all five routes
           without a cookie → 401; POST, PUT and DELETE with a cookie but no X-Requested-With
           → 403 and the store stays unchanged
         - if the CRUD round-trip drifts, TestChildren_CRUD fails: POST {name:"Ada",
           clients:["Kid phone"]} → 201 with id>0 and the same name/clients; POST {name:"Ben"}
           (clients omitted) → 201 with clients [] not null; GET lists both oldest first
           ({"children":[...]}; raw body on a fresh store is {"children":[]}); PUT
           /children/{ada} {name:"Ada M",clients:[]} → 200 with clients [] and GET
           /children/{ada} reflects both; DELETE → 204 then GET /children/{ada} → 404
           not_found and DELETE again → 404
         - if conflicts are not 409, TestChildren_Conflicts fails: second POST "Ada" → 409
           {error:"conflict"} whose message contains "Ada"; POST "Cy" with ["Kid phone"]
           (owned by Ada) → 409 whose message contains both "Kid phone" and "Ada"; PUT ben
           with name "Ada" → 409; the store still has exactly the pre-call rows
         - if PUT half-applies, TestChildren_PutAtomic fails: PUT Ada → {name:"Alicia",
           clients:[<Ben's client>]} is 409 and GET Ada still shows "Ada" with her previous
           clients
         - if validation is loose, TestChildren_Validation fails: {name:"  "}, a 65-rune
           name, {name:"A",clients:[""]} treated as [] (201, clients []), {name:"A",extra:1}
           → 400, a 20 KiB body → 400, a non-object body → 400, and no row is created by any
           400; {name:"Ada",clients:["Kid phone"," Kid phone "]} → 201 with clients ["Kid
           phone"]; GET/PUT/DELETE /children/abc and /children/999 → 404 not_found
         - if CRUD starts depending on AdGuard, TestChildren_NoAdGuard fails: with
           fake.SetStatus("/control/clients", 500) the whole CRUD sequence still succeeds and
           fake.Requests() holds no path other than /control/login
         - if rows do not survive the process, TestRun_ChildrenSurviveRestart fails: run,
           login, POST a child, stop with exit 0, run again on the same data_dir and port,
           GET /api/v1/children with the same cookie lists the child with the same id, name
           and clients

  t-6  Home page: per-child blocked list, error state, URL-keyed fetch stub   [verification; mvp+risk]
       files:    web/src/lib/Home.svelte, web/src/lib/Home.test.ts, web/src/App.test.ts
       covers:   c-6   (locked: global_list_clients)
       depends_on: [t-4]
       description:
         Home keeps "Signed in as" and Log out, adds a "Children" button that
         navigate('children') (App.svelte's route for it lands in t-8). On mount:
         listChildren(), then childBlocked(id) for every child (Promise.all); every mount
         re-fetches (no cache, no module store). Renders one <section data-child={id}> per
         child: h2 name; a client list with a "uses global list" badge (data-badge="global")
         when uses_global and "missing" when missing; a <ul> of services whose state is
         blocked or partial, each with <img alt="" src={iconUrl(icon)}> and the name, partial
         ones suffixed "— unblocked on <differs joined>" with data-state="partial". No
         children → "No children yet". Any rejected fetch (ApiError or network) → <p
         role="alert" data-error={code|'network'}> "Can't reach AdGuard Home — showing nothing
         rather than a stale list" and NO section renders, even if listChildren had
         succeeded. Home.test.ts and App.test.ts switch to a URL-keyed fetch stub (path →
         Response factory) so extra requests from later components (t-9's banner) cannot
         reorder a queue; App.test's 'log out' asserts the calls include POST /api/v1/logout
         rather than equal a fixed list.
       test_contract:
         - if the list is not per child or drops icons, Home.test 'lists blocked services per
           child' fails: two children (Ada id 1, Ben id 2) with stubbed /blocked answers
           render two sections; Ada's lists YouTube and TikTok (blocked) with img src
           'data:image/svg+xml;base64,PHN2Zy8+' and omits Roblox (unblocked); Ben's lists
           nothing and says nothing is blocked
         - if partial drift is hidden, 'marks partial services' fails: a partial roblox with
           differs ["Kid phone"] renders with data-state="partial" and text containing
           "unblocked on Kid phone"
         - if the global badge is dropped, 'shows the uses-global badge' fails: a client
           {name:"Kid tablet",uses_global:true} renders an element with data-badge="global"
           next to "Kid tablet"; a missing client renders "missing"
         - if a stale list survives an error, 'AdGuard down shows an error state' fails:
           render with 200s (names visible), unmount, render again with /children 200 then
           /children/1/blocked 502 adguard_unavailable → a role="alert" with
           data-error="adguard_unavailable", queryAllByRole('list') is empty and no service
           name from the first render is in the document; a fetch that throws TypeError →
           alert with data-error="network"
         - if the page caches, 'refetches on every mount' fails: render, unmount, render
           again → the stub counts /api/v1/children twice
         - if navigation breaks, 'Children button routes' fails: clicking "Children" sets
           location.pathname to '/children' and get(route) to 'children'

### Wave 3

  t-7  Clients, services and blocked-view endpoints   [verification+risk; mvp]
       files:    internal/api/clients.go (new), internal/api/clients_test.go (new),
                 internal/api/blocked.go (new), internal/api/blocked_test.go (new),
                 internal/api/api.go
       covers:   c-2, c-3, c-4
       depends_on: [t-3, t-5]
       description:
         api.go: routes GET /api/v1/clients, GET /api/v1/services, GET
         /api/v1/children/{id}/blocked behind requireSession (interface already widened in
         t-5). clients.go: handleClients calls Clients() then ListChildren() and answers the
         wire shape with child = the owning child or null (auto_clients never appear);
         handleServices maps Services() 1:1 with icon echoed as served. blocked.go:
         handleBlocked: GetChild (404 not_found, no AdGuard call), Clients(), Services(),
         blocked.Compute → 200 in the wire shape. Any adguard error on these three routes →
         502 adguard_unavailable with one Warn line — including ErrBadCredentials, which is
         the service credential, not the parent's; a store error → 500. Nothing is memoised;
         every request goes to AdGuard.
       test_contract:
         - if /clients stops being live or gains a cache, TestClients_LiveEveryCall fails:
           three GET /api/v1/clients leave exactly three GET /control/clients (and three GET
           /control/blocked_services/get) in fake.Requests(); after fake.MutateClient("Kid
           phone", ids → ["10.0.0.9"]) the next GET shows the new id
         - if the child join is wrong, TestClients_ChildAssignment fails: Ada owns ["Kid
           phone"]; the response has Kid phone.child == {id:ada, name:"Ada"}, Kid tablet.child
           == null (raw body contains "child":null), use_global_blocked_services true only
           for Kid tablet, ids matching the fixture, and the auto_client "printer" absent
         - if /services vendors or caches, TestServices_Live fails: two GET /api/v1/services
           leave exactly two GET /control/blocked_services/all; the body lists youtube,
           tiktok, roblox in fixture order with icon "PHN2Zy8+"; after
           fake.SetResponse("/control/blocked_services/all", 200, <one-entry doc>) the very
           next call returns one service
         - if the view is wrong end-to-end, TestBlocked_View fails: Ada [Kid phone, Kid
           tablet, Ghost] → 200 whose services are youtube partial ["Kid tablet"], tiktok
           blocked [], roblox partial ["Kid phone"]; clients include {Ghost, missing:true}
           and Kid tablet uses_global true; the raw body has "differs":[] (never null); each
           call records one GET /control/clients, one blocked_services/get and one
           blocked_services/all
         - if the fold is not re-run per request, TestBlocked_Live fails: GET /blocked,
           MutateClient adds "roblox" to "Kid phone", GET /blocked again shows roblox blocked
         - if a dangling client errors or is pruned, TestBlocked_MissingIsNotAnError fails: a
           child with only ["Ghost"] → 200 with every service unblocked, and GET
           /children/{id} still lists Ghost afterwards
         - if AdGuard failure is not 502, TestBlocked_AdGuardDown fails: with
           fake.SetStatus("/control/clients", 500) /clients and /children/{id}/blocked answer
           502 {error:"adguard_unavailable"}; SetStatus("/control/blocked_services/all", 503)
           makes /services and /blocked 502; SetStatus("/control/blocked_services/get", 500)
           makes /clients and /blocked 502 with no partial body; with the fake closed
           (transport error) the same; fake.SetAuth(false) → 502 as well, never 401
         - if ids are not checked, TestBlocked_NotFound fails: /children/999/blocked → 404
           not_found and fake.Requests() records no /control/ hit for it

  t-8  Children settings page + route   [verification+mvp; risk]
       files:    web/src/lib/Children.svelte (new), web/src/lib/Children.test.ts (new),
                 web/src/App.svelte, web/src/App.test.ts
       covers:   c-5   (locked: dangling_client)
       depends_on: [t-4, t-6]
       description:
         App.svelte routes 'children' to <Children onBack={() => navigate('home')} /> when
         signed in, and refresh() keeps the current non-login route on success
         (navigate($route === 'login' ? 'home' : $route)) so a reload on /children stays
         there. Children.svelte: load() = Promise.all(listChildren(), listClients()); one
         <form data-child={id}> per child with a name <input aria-label="Name">, a checkbox
         per AdGuard client (label = client name, checked when in the child's clients;
         disabled with the text "assigned to <other child>" when client.child is another
         child), and for each of the child's clients absent from /clients a <s data-missing>
         entry with a "Remove" button that drops it from the pending list; "Save" →
         updateChild(id, name, clients) then load(); "Delete" → deleteChild(id) then load().
         An "Add child" form → createChild(name, []) then load(). Every mutation re-renders
         from the server's answer, never from local state. ApiError → <p role="alert"
         data-error={code}>messageFor(e)</p> and the reload reverts any unsaved checkbox.
         "Back" → onBack().
       test_contract:
         - if assignment state is wrong, Children.test 'renders ownership' fails: Ada [Kid
           phone], Ben []; /clients marks Kid phone child Ada, Kid tablet null → in Ben's
           form the "Kid phone" checkbox is disabled and its label text contains "assigned
           to Ada"; in Ada's form it is enabled and checked; "Kid tablet" is enabled and
           unchecked in both
         - if a save sends the wrong body, 'assign by checkbox' fails: ticking "Kid tablet"
           in Ada's form and clicking Save issues PUT /api/v1/children/1 with body
           {"name":"Ada","clients":["Kid phone","Kid tablet"]}; no PUT is sent before Save
         - if renaming or deleting is wired wrong, 'rename and delete' fails: typing "Ada M"
           then Save → PUT body name "Ada M" with the existing clients; clicking Delete on
           Ben → DELETE /api/v1/children/2 and, after the stubbed re-fetch omits Ben, no
           form with data-child="2" remains
         - if a missing client is pruned or hidden, 'missing client is struck through'
           fails: Ada [Kid phone, Ghost] with /clients lacking Ghost renders <s data-missing>
           "Ghost" with a Remove button and no checkbox for Ghost; without clicking Remove,
           Save sends clients ["Kid phone","Ghost"] (nothing auto-pruned); after Remove, Save
           sends ["Kid phone"]
         - if create is broken, 'add child' fails: typing "Cy" into the add form and
           submitting issues POST /api/v1/children {"name":"Cy","clients":[]} and the form
           for Cy appears once the stubbed re-fetch lists it
         - if the page shows optimistic state, 'renders the server after save' fails: the
           stubbed GET after PUT returns name "Ada Server"; the DOM shows "Ada Server", not
           what was typed
         - if 409 is swallowed, 'shows a conflict' fails: PUT answering 409
           {error:"conflict",message:"client Kid phone is already assigned to Ben"} renders
           role="alert" with that message and data-error="conflict", a GET /children
           follows, and the ticked box is unchecked again; Add "Ada" answering 409 shows the
           alert, keeps the typed value and adds no form
         - if reload bounces, App.test 'reload on /children stays there' fails:
           history.replaceState to '/children' before render, /me 200 → the Children heading
           renders and location.pathname is still '/children'; a 401 at /children lands on
           login
         - if the signed-out guard is bypassed, App.test 'children needs a session' fails: at
           '/children' with /me 401 the Sign in button renders and no /api/v1/children fetch
           is made

  t-9  Global-list migration banner   [verification; risk+mvp]
       files:    web/src/lib/MigrationBanner.svelte (new), web/src/lib/MigrationBanner.test.ts (new),
                 web/src/lib/Home.svelte, web/src/lib/Home.test.ts
       covers:   c-7   (locked: migration_offer_ux)
       depends_on: [t-4, t-6]
       description:
         MigrationBanner.svelte: on mount migrationOffer(); renders nothing when clients is
         empty, when the call fails (the home error state already covers AdGuard down), or
         when $migrationDismissed is true. Otherwise <aside role="status" data-migration>
         listing, per client, "<name> (<child name>) gains <ids joined>" (or "keeps its
         list" when gains is empty), with "Migrate" and "Not now". Migrate → applyMigration()
         then onMigrated() and the banner hides; a 502 → role="alert" with messageFor. "Not
         now" → migrationDismissed.set(true) (module store from t-4, reset by login/logout;
         nothing in storage). Home.svelte mounts <MigrationBanner onMigrated={load} /> above
         the children; Home.test.ts stubs /api/v1/migration with an empty offer by default.
       test_contract:
         - if the offer text is wrong, MigrationBanner.test 'shows who gains what' fails:
           offer {global:[tiktok,roblox], clients:[{name:"Kid tablet", child:{id:1,
           name:"Ada"}, gains:["roblox","tiktok"]}]} renders text containing "Kid tablet
           (Ada) gains roblox, tiktok"; an empty clients list renders no element with
           data-migration
         - if something is written without confirm, 'nothing written until Migrate' fails:
           after render and a tick the stub has seen only GET /api/v1/migration (no POST);
           clicking Migrate issues exactly one POST /api/v1/migration and then calls
           onMigrated once and the banner is gone
         - if dismissal is wrong-scoped, 'Not now hides until next login' fails: clicking
           "Not now" removes the banner and issues no POST; unmount and re-render with the
           same offer → still hidden (navigating away and back does not resurface it);
           migrationDismissed.set(false) (what login() does) and render → visible;
           sessionStorage/localStorage are untouched (length 0)
         - if apply failure is silent, 'migrate 502 shows an alert' fails: Migrate answering
           502 adguard_unavailable renders role="alert" and the banner stays
         - if Home stops hosting it, Home.test 'hosts the migration banner' fails: with a
           non-empty stubbed offer the home page shows data-migration; with the default
           empty offer it does not; Home.test 'migration applied re-fetches blocked views'
           sees a second GET /children/1/blocked after Migrate answers 200

### Wave 4

  t-10 Migration offer and apply endpoints   [verification+mvp; risk]
       files:    internal/api/migration.go (new), internal/api/migration_test.go (new),
                 internal/api/api.go
       covers:   c-7   (locked: global_list_clients, migration_offer_ux)
       depends_on: [t-2, t-3, t-7]
       description:
         api.go: routes GET and POST /api/v1/migration behind requireSession.
         migration.go: handleMigrationOffer: ListChildren + Clients → blocked.Offer over
         every mapped client name → {global, clients:[{name, child:{id,name}, gains}]}. Never
         writes. handleMigrationApply (no body): recompute the offer from a fresh Clients()
         read, call MigrateFromGlobal(name, step.Target) for each step in order, 200
         {migrated:[names]}; the first adguard error → 502 adguard_unavailable with a message
         naming the client (already-written clients stay written; the next GET simply lists
         the rest). No writes when the offer is empty. The global list is never written.
       test_contract:
         - if the offer rule drifts, TestMigration_OfferScope fails: children Ada [Kid phone,
           Kid tablet], nobody owns Old laptop; fixture global [tiktok,roblox]; Old laptop
           mutated to use_global=true → offer has exactly one entry {Kid tablet, child
           {ada,"Ada"}, gains [roblox,tiktok]}; with no children → clients []; with global
           [] (SetResponse on /control/blocked_services/get) the offer is empty even though
           Kid tablet still uses the global list; a Kid tablet whose own list is [roblox]
           gains only [tiktok]
         - if GET writes anything, TestMigration_OfferIsReadOnly fails: GET /api/v1/migration
           → 200 with the entry above and fake.LastUpdate() == nil and no POST in
           fake.Requests()
         - if apply writes the wrong clients or touches the global list, TestMigration_Apply
           fails: POST /api/v1/migration → 200 {migrated:["Kid tablet"]}; fake.Requests()
           holds exactly one POST /control/clients/update, whose body names Kid tablet with
           blocked_services [roblox,tiktok] and use_global_blocked_services false; no request
           to /control/blocked_services/set and GET /control/blocked_services/get is
           unchanged; the fake's next GET /control/clients shows Kid tablet migrated and Old
           laptop (unmapped) still use_global=true; a second GET /api/v1/migration now
           returns clients []
         - if apply is not idempotent-safe, TestMigration_ApplyEmpty fails: with no child
           mapped to a global-list client POST → 200 {migrated:[]} and no
           /control/clients/update is recorded; two sequential POSTs of a one-client offer
           leave exactly one /control/clients/update in Requests()
         - if a mid-way failure is hidden, TestMigration_PartialFailure fails: two mapped
           global clients (MutateClient flips "Kid phone" to use_global true),
           SetUpdateStatus on the second, POST → 502 adguard_unavailable whose message names
           the failed client, the first client's flag is false in a re-read, the second's
           still true, and GET /migration now lists only the second
         - if AdGuard failure is not 502, TestMigration_AdGuardDown fails:
           fake.SetStatus("/control/clients/update", 500) → POST answers 502
           adguard_unavailable; fake.SetStatus("/control/clients", 500) → GET answers 502
         - if CSRF is skipped, TestMigration_CSRF fails: POST without X-Requested-With is 403
           and LastUpdate() is nil; GET and POST without a cookie → 401

### Coverage

| criterion | tasks |
|---|---|
| c-1 | t-1 (schema, uniqueness, reopen), t-5 (endpoints, 409s, process restart) |
| c-2 | t-7 |
| c-3 | t-7 |
| c-4 | t-3 (pure fold), t-7 (endpoint, missing, 502) |
| c-5 | t-4 (transport), t-8 (page + route) |
| c-6 | t-4 (transport), t-6 (page) |
| c-7 | t-2 (AdGuard write + fake fault), t-3 (pure offer), t-4 (transport + dismissal store), t-9 (banner), t-10 (offer/apply endpoints) |
| c-8 | t-1 (TestSessions_DeleteExecError) |

Locked decisions: cross_client_state → t-3/t-7; global_list_clients → t-3, t-6 (badge),
t-10; migration_offer_ux → t-4 (store), t-9; dangling_client → t-1 (no pruning), t-3, t-7,
t-8; client_identity → t-1.

## Disagreements

1. **Migration apply protocol: recompute-on-POST vs plan-echo with 409.**
   Risk: GET returns a plan, POST must echo `{plan:[{client,result}]}`, the server
   recomputes and answers 409 "the offer changed" on mismatch, `ErrNotOnGlobal` makes a
   double submit a no-op, and the banner re-fetches on 409. MVP and verification: POST has
   no body, recomputes from a fresh read and applies whatever is currently on the global
   list. **Default: recompute-on-POST** (2 of 3; smaller request surface; every client the
   recompute finds is one that genuinely is on the global list, so the write is always the
   right write even if the shown list was a few seconds stale). Why it matters: risk's
   point is real — a parent could confirm a list that changed between render and click.
   If the user wants the stricter protocol, t-3 `Step` gains `Result`, t-4/t-9 echo it,
   t-10 gains the 409 branch and `TestMigration_StalePlan`/`_DoubleSubmit`, and t-2 gains
   `ErrNotOnGlobal` — all specified in risk.md. Related sub-choice folded in: the union
   with the client's own list is computed in the handler from the GET-time read (mvp,
   verification) rather than inside the mutex from a fresh read (risk); the window is one
   request.

2. **"Not now" scope: module store reset on login/logout vs component-local state.**
   Risk: exported writable `migrationDismissed` in api.ts, reset in `login()`/`logout()`.
   MVP and verification: plain `$state` in the banner, so any remount (including
   home → children → home) re-shows it. **Default: module store** (risk — the minority),
   because the locked decision says "hides it for the session and it reappears on next
   login", and this phase ships a settings page the parent will bounce to and from; a
   banner that reappears on every return from settings is not hidden for the session.
   Nothing is persisted either way. Why it matters: it is the difference between
   satisfying and merely approximating a locked decision; cost is three lines in t-4.

3. **Wave count: 3 (risk, mvp) vs 4 (verification).**
   Risk reaches 3 waves by letting t-8/t-9 both append to `api.go` in wave 3 and by
   having t-6/t-7 both edit `App.svelte` in wave 2; mvp reaches 3 by folding migration
   endpoints + blocked view + main wiring into one task. **Default: 4 waves** — one
   `api.go` owner per wave, and t-8 (Children page) waits on t-6 for `App.test.ts`.
   Why it matters: in `--solo` parallel execution a same-file edit is a merge conflict,
   not a one-line nuisance; the critical path is the same length on the web side either
   way (t-4 → t-6 → t-8/t-9), so wave 4 costs only the backend migration endpoints.

4. **Where the pure fold lives: `internal/blocked` (risk) vs `internal/api/blocked.go` (verification); mvp has no pure fold.**
   **Default: `internal/blocked`** taking `Mapped []string`. Verification's version takes
   `store.Child` and sits in wave 1 with no dependency on t-1, which cannot compile; the
   separate package has no store import, so it truly is wave-1 independent and is a tight
   gremlins target. Why it matters: a wave-1 task that does not build blocks nothing
   visibly but fails on execution.

5. **Partial-service payload: `differs` (mvp, verification) vs `blocked_on` + `unblocked_on` (risk).**
   **Default: `differs` = present clients NOT blocking the service** (2 of 3). Risk argues
   "differ from what?" is ambiguous and two arrays remove it; the counter is that the
   complement is derivable from the response's `clients` array, and the spec's
   `cross_client_state` says "partial carries the list of clients that differ" — one
   list. Why it matters: phase 04's grant reads this field; the meaning is pinned by
   `TestCompute_Fixture` so it cannot silently flip later.

6. **Unknown service ids in a client list: ignore (verification; mvp implicitly) vs append as `{ID: id, Name: id, Icon: ""}` (risk).**
   **Default: ignore** — the catalogue from `/control/blocked_services/all` is the view,
   and c-4 is defined "per service" over that catalogue. Risk's concern (a real block
   hidden after an AdGuard upgrade) is valid but the mismatch window is an AdGuard
   version skew, not app state. Why it matters: `TestCompute_UnknownID` asserts one
   behaviour or the other; flipping later means changing the wire shape phase 04 reads.

7. **Home error state on a partial failure: hide everything (mvp, verification) vs per-card isolation (risk).**
   Risk renders child 2's services alongside child 1's alert. **Default: hide everything**
   (2 of 3): a half-rendered page reads as "Ben is unblocked", which is the stale-list lie
   c-6 forbids; against a single AdGuard instance a per-child partial failure is a race,
   not a steady state. Why it matters: the test `'AdGuard down shows an error state'`
   asserts `queryAllByRole('list')` is empty, which per-card rendering would fail.

8. **Uniqueness detection: SELECT pre-check inside the tx (risk, mvp) vs classify the driver's constraint text (verification).**
   **Default: pre-check inside the tx** naming the owner, with UNIQUE/PK as the safety
   net (2 of 3). The 409 must name the other child and the constraint text carries no
   child name; the single-connection pool makes the pre-check race-free. Why it matters:
   verification's `TestChildren_ClientOneOwner` rollback assertion holds either way, but
   the error-text path would need a second query to produce the name.

9. **Route and component naming: `'children'` / `/children` / `Children.svelte` (risk, mvp) vs `'settings'` / `/settings` / `Settings.svelte` (verification).**
   **Default: `children`** (2 of 3, and c-5 calls it "a children settings page" — the
   path names the thing managed). Cosmetic, but every t-4/t-6/t-8 contract line hard-codes
   it, so it is decided here rather than at execution.

Minor merges made without a divergence entry (2-of-3 or existing-idiom calls): `DeleteChild`
returns `(bool, error)` to match `DeleteByUser`; `ListChildren` orders by id ASC so a rename
does not reorder forms under the parent's finger; sentinel names `ErrNameTaken` /
`*ErrClientTaken`; `MigrateFromGlobal` over mvp's `AdoptBlockedServices`; 16 KiB body cap
to match `handleLogin`; rename via explicit Save rather than blur; c-8 test uses `DROP TABLE
sessions` (proves the exec-error path, not the closed-pool path); `login_test.go` added to
t-5's files because `newHarness` constructs `Deps` and will not compile without
`Children: st` (only mvp noticed).
