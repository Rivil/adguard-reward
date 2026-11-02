# Risk-lens plan — 03-children-and-blocked-view

Lens: every task is shaped around a failure mode it alone owns. The graph starts
from what breaks — vacuous folds over zero clients, a 409 that half-applies a
rename, a migration that writes what the parent never saw, a stale home list, a
second tab racing a checkbox — and each of those has exactly one owner below.

Phase 03-children-and-blocked-view — 10 tasks across 3 waves

## Wave 1

  t-1  Children schema, store queries, c-8 Delete test
       files:    internal/store/migrations/0002_children.sql,
                 internal/store/children.go, internal/store/children_test.go,
                 internal/store/sessions_test.go
       covers:   c-1, c-8
       depends_on: []
       description:
         0002_children.sql: children(id INTEGER PRIMARY KEY, name TEXT NOT NULL
         COLLATE NOCASE UNIQUE, created_at INTEGER NOT NULL) and
         child_clients(child_id INTEGER NOT NULL REFERENCES children(id) ON DELETE
         CASCADE, client_name TEXT NOT NULL UNIQUE, PRIMARY KEY(child_id,
         client_name)). children.go: Child{ID int64, Name string, Clients
         []string}; sentinels ErrChildNotFound, ErrNameTaken, and typed
         *ErrClientTaken{Client, ChildID, ChildName}. ListChildren(ctx)
         ([]Child, error) is two queries with the first fully scanned and closed
         before the second (one-connection rule from store.go); GetChild(ctx,
         id); CreateChild(ctx, name, clients) (Child, error); UpdateChild(ctx,
         id, name, clients) (Child, error); DeleteChild(ctx, id) (bool, error).
         Create/Update run in one BeginTx: pre-check name (excluding own id) and
         each client's owner (excluding own id) inside the tx and return the
         typed error with the owner's name, then write; the UNIQUE constraints
         stay as the safety net. clients are trimmed, sorted and deduped before
         the write (same payload twice is idempotent). Clients arrays never come
         back nil. sessions_test.go gains TestSessions_DeleteExecError for c-8.
       test_contract:
         - if the UNIQUE on children.name (or its NOCASE collation) is dropped,
           TestChildren_NameUnique fails: CreateChild("Alice") then
           CreateChild("alice") returns ErrNameTaken and ListChildren has one row
         - if client ownership is not enforced, TestChildren_ClientOneOwner
           fails: child A gets ["Kid phone"], CreateChild("B", ["Kid phone"])
           returns *ErrClientTaken with ChildName "A" and PRAGMA index_list shows
           child_clients.client_name UNIQUE
         - if UpdateChild is not atomic, TestChildren_UpdateRollsBack fails:
           UpdateChild(A.ID, "Renamed", ["<client owned by B>"]) returns
           *ErrClientTaken and GetChild(A.ID) still has name "A" and its
           previous clients
         - if the self-exclusion is missing, TestChildren_UpdateSameName fails:
           UpdateChild(A.ID, "A", A.Clients) succeeds (no ErrNameTaken against
           its own row); UpdateChild(A.ID, "A", ["Kid phone"]) where A already
           owns "Kid phone" succeeds
         - if ON DELETE CASCADE or foreign_keys is lost, TestChildren_DeleteFrees
           fails: DeleteChild(A.ID) returns true, SELECT count(*) FROM
           child_clients WHERE child_id = A.ID is 0, and CreateChild("C",
           A.Clients) then succeeds; DeleteChild(999) returns (false, nil)
         - if the payload is not normalised, TestChildren_ClientsNormalised
           fails: CreateChild("A", [" Kid phone", "Kid phone", "Kid tablet"])
           stores exactly ["Kid phone", "Kid tablet"] in that order
         - if a query is issued while rows are open, TestChildren_ListRace
           fails under go test -race: 10 goroutines calling ListChildren while
           another does 20 CreateChild/DeleteChild cycles complete within 5s
           with no error (a nested query on the single connection deadlocks)
         - if rows do not persist, TestChildren_Persist fails: Create, Close,
           Open on the same dir, ListChildren returns the same id, name and
           clients
         - if an unknown id is not signalled, TestChildren_NotFound fails:
           GetChild(999) and UpdateChild(999, ...) return ErrChildNotFound
         - if the wrap at sessions.go:79 is removed (survivor
           7682485717754728), TestSessions_DeleteExecError fails: after
           s.DB().Exec("DROP TABLE sessions"), s.Delete(ctx, 1) returns an
           error whose text starts "delete session:", whose errors.Unwrap is
           non-nil and whose text contains "no such table"

  t-2  AdGuard MigrateFromGlobal and fake fault injection
       files:    internal/adguard/clients.go, internal/adguard/clients_test.go,
                 internal/adguard/adguardtest/server.go,
                 internal/adguard/adguardtest/server_test.go
       covers:   c-7
       depends_on: []
       description:
         clients.go: var ErrNotOnGlobal; MigrateFromGlobal(ctx, clientName
         string, globalIDs []string) (written []string, err error): under the
         existing rmw mutex re-read /control/clients; ErrClientNotFound if
         absent; ErrNotOnGlobal if use_global_blocked_services is already false
         (nothing written); otherwise POST /control/clients/update with every
         raw field verbatim except blocked_services = sorted+deduped union of the
         fresh read's list and globalIDs, and use_global_blocked_services =
         false. Never touches /control/blocked_services/set. adguardtest gains
         SetUpdateStatus(name string, code int): handleUpdate answers code for
         that client name only (0 clears), so a multi-client write can fail on
         the second client.
       test_contract:
         - if the flag is not flipped or the list not unioned,
           TestMigrateFromGlobal_Writes fails: on "Kid tablet" (fixture: global
           list, blocked_services []) with ["tiktok", "roblox"] the LastUpdate
           body has data.use_global_blocked_services == false,
           data.blocked_services == ["roblox", "tiktok"], and every other key of
           the fixture object (safe_search, tags, blocked_services_schedule,
           upstreams_cache_size 0) byte-identical to the read
         - if the write is not a union, TestMigrateFromGlobal_Union fails:
           MutateClient sets "Kid tablet".blocked_services to ["youtube"], then
           migrating with ["tiktok"] writes ["tiktok", "youtube"] and returns it
         - if an already-migrated client is rewritten,
           TestMigrateFromGlobal_Idempotent fails: "Kid phone" (flag false)
           returns ErrNotOnGlobal and Requests() holds no POST
           /control/clients/update
         - if the global list is touched, TestMigrateFromGlobal_GlobalUntouched
           fails: after a migration Requests() contains no path
           /control/blocked_services/set and GET
           /control/blocked_services/get still returns ["tiktok", "roblox"]
         - if the mutex is bypassed, TestMigrateFromGlobal_Serialised fails:
           Hang("/control/clients/update", 30ms) with 4 concurrent migrations on
           different clients leaves MaxInFlightUpdates() == 1
         - if the fake's per-name fault is wired wrong,
           TestServer_SetUpdateStatus fails: SetUpdateStatus("Kid tablet", 500)
           makes an update to "Kid tablet" 500 and to "Kid phone" 200;
           SetUpdateStatus("Kid tablet", 0) clears it
         - if a vanished client is written blindly,
           TestMigrateFromGlobal_NotFound fails: SetResponse("/control/clients",
           200, {"clients":[]}) then migrating "Kid tablet" returns
           ErrClientNotFound with no update request

  t-3  Pure blocked-state fold and migration plan
       files:    internal/blocked/blocked.go, internal/blocked/blocked_test.go
       covers:   c-4, c-7
       depends_on: []
       description:
         Package blocked, no I/O. Constants StateBlocked/StatePartial/
         StateUnblocked. Input{Clients []adguard.PersistentClient, Global
         []string, Services []adguard.Service, Mapped []string}. Compute(Input)
         View{Clients []ClientState{Name, Missing, UsesGlobal}, Services
         []ServiceState{ID, Name, Icon, State, BlockedOn, UnblockedOn}}. A
         mapped name absent from Clients is Missing and excluded from the fold;
         a UsesGlobal client's blocked set is Global; state is blocked when
         every present client blocks, unblocked when none does, partial
         otherwise; with zero present clients every service is unblocked (no
         vacuous truth). Services follow catalogue order; ids in a client list
         that are not in the catalogue are appended as ServiceState{ID: id,
         Name: id, Icon: ""} sorted by id. Client lists sorted by name; slices
         never nil. MigrationPlan(clients, global, mapped) []Step{Client, Gains,
         Result}: empty when global is empty; one step per mapped, present
         client with UseGlobalBlockedServices true; Gains = global minus the
         client's list, Result = sorted union; a client with Gains empty is still
         a step (its flag must flip).
       test_contract:
         - if the fold is vacuous, TestCompute_NoPresentClients fails: Mapped
           ["Ghost"] over the fixture gives Clients [{Ghost, Missing true}] and
           every service State unblocked, BlockedOn empty
         - if missing clients are folded, TestCompute_MissingExcluded fails:
           Mapped ["Kid phone", "Ghost"] gives youtube blocked (not partial),
           BlockedOn ["Kid phone"], and Ghost only in Clients with Missing true
         - if the global list is not resolved, TestCompute_UsesGlobal fails:
           Mapped ["Kid phone", "Kid tablet"] with Global ["tiktok", "roblox"]
           gives tiktok blocked; youtube partial with BlockedOn ["Kid phone"],
           UnblockedOn ["Kid tablet"]; roblox partial with BlockedOn ["Kid
           tablet"], UnblockedOn ["Kid phone"]; ClientState "Kid tablet" has
           UsesGlobal true
         - if a null list is mishandled, TestCompute_NullList fails: Mapped
           ["Old laptop"] (fixture blocked_services null) gives every service
           unblocked and no panic
         - if unknown ids are dropped, TestCompute_UnknownID fails: a client
           blocking "zzz-new" yields a trailing ServiceState{ID "zzz-new",
           Name "zzz-new", Icon ""} with State blocked
         - if ordering is nondeterministic, TestCompute_Order fails: services
           come back in catalogue order (youtube, tiktok, roblox) and
           BlockedOn/UnblockedOn are sorted by name regardless of Mapped order
         - if the plan targets the wrong clients, TestMigrationPlan_Scope fails:
           Mapped ["Kid tablet", "Kid phone", "Ghost"] gives exactly one step
           (Kid tablet: Gains ["roblox", "tiktok"], Result ["roblox",
           "tiktok"]); Global [] gives no steps; an unmapped global client gives
           no steps
         - if an already-covered client is skipped,
           TestMigrationPlan_EmptyGains fails: a global client whose list
           already holds every global id yields a step with Gains [] and Result
           equal to its list

  t-4  SPA api.ts: children, clients, services, blocked, migration
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts
       covers:   c-5, c-6, c-7
       depends_on: []
       description:
         Method gains 'PUT'. Route gains 'children' (path /children; routeFor and
         navigate map it). Types Child{id, name, clients}, ClientView{name, ids,
         use_global_blocked_services, child: {id, name} | null}, Service{id,
         name, icon}, BlockedView{child, clients: {name, missing,
         uses_global}[], services: {id, name, icon, state, blocked_on,
         unblocked_on}[]}, MigrationPlan{needed, global_blocked_services, plan:
         {client, child, gains, result}[]}. Functions listChildren,
         createChild(name, clients), updateChild(id, name, clients),
         deleteChild(id), listClients, listServices, childBlocked(id),
         migrationPlan(), applyMigration(plan). Exported writable
         migrationDismissed (false) reset to false inside login() on success and
         inside logout() (the "not now" is per login, locked
         migration_offer_ux). messageFor gains case 'conflict' -> e.message.
       test_contract:
         - if PUT is dropped from Method or the path is wrong,
           TestApi_UpdateChild (api.test.ts "updateChild sends PUT") fails:
           updateChild(3, "A", ["x"]) fetches PUT /api/v1/children/3 with the
           CSRF header and body {"name":"A","clients":["x"]}
         - if /children is not a route, "routeFor maps /children" fails:
           navigate('children') sets location.pathname to /children and the
           route store to 'children'; a popstate to / sets 'home'
         - if the dismissal is not reset on login, "login resets
           migrationDismissed" fails: set(true), login() with a 204 leaves
           get(migrationDismissed) false; logout() likewise
         - if a 409 surfaces as 'unknown', "conflict envelope" fails:
           createChild against a 409 {error: conflict, message: m} rejects
           with ApiError.code 'conflict' and messageFor returns m
         - if the 401 hook regresses, the existing "401 calls onUnauthorized"
           test still fails for childBlocked(1) against a 401

## Wave 2

  t-5  Children CRUD endpoints, Deps wiring, restart proof
       files:    internal/api/children.go, internal/api/children_test.go,
                 internal/api/api.go, internal/api/errors.go,
                 cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go
       covers:   c-1
       depends_on: [t-1]
       description:
         errors.go: CodeConflict = "conflict". api.go: Deps gains Children
         (api-local interface over store's ListChildren/GetChild/CreateChild/
         UpdateChild/DeleteChild) and the AdGuard interface widens to Clients,
         Services, MigrateFromGlobal (t-2 exists) so t-8/t-9 only add routes.
         Routes under requireSession: GET /api/v1/children -> {children: []},
         POST -> 201 child, GET/PUT /api/v1/children/{id} -> child, DELETE ->
         204. Body: {name, clients} decoded with DisallowUnknownFields under a
         4 KiB MaxBytesReader; name trimmed, 1..64 chars; clients an array of
         non-empty strings <= 256 chars each, else 400. ErrNameTaken and
         *ErrClientTaken -> 409 conflict with a message naming the owner;
         ErrChildNotFound and a non-numeric id -> 404. main.go wires Children:
         st. main_test.go: TestRun_ChildrenSurviveRestart (same pattern as
         TestRun_SessionSurvivesRestart).
       test_contract:
         - if the 409 mapping is lost, TestChildren_Conflict fails: POST
           {name: "Alice"} twice -> second is 409 {error: conflict}; POST
           {name: "Bob", clients: ["Kid phone"]} when Alice owns it -> 409 with
           message containing "Alice"
         - if validation loosens, TestChildren_BadRequest fails: {name: "  "},
           {name: <65 chars>}, {name: "A", clients: [""]}, {name: "A", extra:
           1}, a 5 KiB body and a non-object body each return 400 and no row
           is created
         - if PUT half-applies, TestChildren_PutAtomic fails: PUT Alice ->
           {name: "Alicia", clients: [<Bob's client>]} is 409 and GET Alice
           still shows "Alice" with her previous clients
         - if a state-changing route escapes the CSRF layer,
           TestChildren_CSRF fails: POST, PUT and DELETE without
           X-Requested-With are 403 and the store is unchanged
         - if any children route is reachable anonymously, TestChildren_Auth
           fails: all five routes without a cookie return 401
         - if ids are guessable to an error, TestChildren_NotFound fails: GET,
           PUT, DELETE on /children/999 and /children/abc return 404 not_found
         - if the round-trip shape drifts, TestChildren_CRUD fails: POST 201
           body {id, name, clients} with clients [] (not null) when omitted;
           GET list is oldest first; DELETE 204 then GET 404
         - if rows do not survive the process, TestRun_ChildrenSurviveRestart
           fails: POST a child, stop, start again on the same data_dir, GET
           /api/v1/children lists it with the same id

  t-6  Home page: per-child blocked list, no stale state
       files:    web/src/lib/Home.svelte, web/src/lib/Home.test.ts,
                 web/src/App.svelte
       covers:   c-6
       depends_on: [t-4]
       description:
         Home on mount: listChildren() then childBlocked(id) for each via
         Promise.allSettled. One card per child: services with state blocked
         or partial (name + <img src="data:image/svg+xml;base64,{icon}">,
         partial marked "partial: <unblocked_on joined>"), clients with
         uses_global shown with a "uses global list" badge, missing clients
         listed as "missing". A rejected childBlocked renders that card as
         role="alert" data-error=<code> with no service names; a rejected
         listChildren renders a page-level alert. No module-level cache: state
         lives in the component. App.svelte adds a "Children" nav link
         (navigate('children')) beside Log out; the 'children' route renders a
         placeholder until t-7 supplies the page.
       test_contract:
         - if the list is not fetched on load, "fetches children then each
           blocked view" fails: mocked fetch sees GET /api/v1/children then GET
           /api/v1/children/1/blocked and /children/2/blocked; both cards show
           their blocked service names and icons
         - if stale state survives, "502 shows an error, not the last list"
           fails: render with 200s (names visible), unmount, render again with
           /children/1/blocked -> 502 adguard_unavailable: the card is
           role="alert" with data-error adguard_unavailable and no service name
           from the first render is in the document
         - if one failure hides every child, "per-card error" fails: child 1
           502, child 2 200 -> child 2's services render alongside child 1's
           alert
         - if unblocked services leak in, "only blocked and partial listed"
           fails: a service with state unblocked is absent; a partial one shows
           the "partial" marker with its unblocked_on names
         - if the global badge is dropped, "uses global list badge" fails: a
           client with uses_global true renders the badge text
         - if the nav link is missing, App.test "Children link routes" fails:
           clicking "Children" sets location.pathname to /children

  t-7  Children settings page
       files:    web/src/lib/Children.svelte, web/src/lib/Children.test.ts,
                 web/src/App.svelte
       covers:   c-5
       depends_on: [t-4]
       description:
         Children.svelte loads listChildren() and listClients() together. Create
         form (name, "Add"). Per child: name input saved on blur/Enter via
         updateChild, "Delete" via deleteChild, and one checkbox per AdGuard
         client: checked when in child.clients; disabled with label "(Bob)"
         when client.child is another child; a name in child.clients that is
         not in the clients list is rendered <s> with a "Remove" button.
         Every change sends the full {name, clients} and then re-reads both
         lists from the server — checkboxes reflect the response, never the
         click. A 409 shows role="alert" with the server message and the
         reload reverts the checkbox. App.svelte renders Children for the
         'children' route (replacing t-6's placeholder) with a "Home" link.
       test_contract:
         - if the disabled rule breaks, "client on another child is disabled"
           fails: clients [{Kid phone, child {2, Bob}}] on Alice's card render a
           disabled checkbox whose label contains "Bob"; Alice's own client is
           enabled and checked
         - if the missing case is not rendered, "missing client struck
           through" fails: Alice.clients ["Ghost"] absent from /clients renders
           <s>Ghost</s> and a Remove button; clicking it sends PUT with clients
           excluding "Ghost"
         - if the page trusts the click, "409 reverts the checkbox" fails:
           ticking a client answers 409 {conflict, "client belongs to Bob"};
           the alert shows that text, a GET /children follows, and the box is
           unchecked again
         - if persistence is local, "reload shows server state" fails: after
           a successful PUT the component re-fetches and renders the server's
           clients, not the pre-PUT array (mock returns a different list and
           that is what renders)
         - if rename or delete skip the API, "rename sends PUT, delete sends
           DELETE" fails: blur on the name input sends PUT /children/1 with
           the new name and existing clients; Delete sends DELETE
           /children/1 and the card disappears after the re-fetch
         - if duplicate names are unhandled, "duplicate name 409" fails: Add
           "Alice" answering 409 shows the alert and no new card

## Wave 3

  t-8  Clients, services and blocked endpoints
       files:    internal/api/clients.go, internal/api/services.go,
                 internal/api/blocked.go, internal/api/adguard_test.go,
                 internal/api/api.go
       covers:   c-2, c-3, c-4
       depends_on: [t-5, t-3]
       description:
         GET /api/v1/clients: AdGuard.Clients() then Children.ListChildren();
         {clients: [{name, ids, use_global_blocked_services, child: {id,
         name} | null}]}. GET /api/v1/services: AdGuard.Services() ->
         {services: [{id, name, icon}]}, no cache. GET
         /api/v1/children/{id}/blocked: GetChild (404), AdGuard.Clients(),
         AdGuard.Services(), blocked.Compute -> {child: {id, name}, clients:
         [{name, missing, uses_global}], services: [{id, name, icon, state,
         blocked_on, unblocked_on}]}. Any AdGuard error (transport, non-2xx,
         ErrBadCredentials, partial Clients failure) -> 502 adguard_unavailable
         and one Warn log line; the store error path is 500. Routes appended
         to api.go.
       test_contract:
         - if a request is served from memory, TestClients_LiveEachCall fails:
           three GET /api/v1/clients leave exactly three GET /control/clients
           in fake.Requests(); MutateClient renaming "Kid phone" between calls
           is visible in the next response
         - if the child join is wrong, TestClients_ChildAssignment fails: with
           Alice owning "Kid phone", that entry has child {id, "Alice"} and
           "Kid tablet" has child null; ids and use_global_blocked_services
           match the fixture
         - if the catalogue is cached or vendored, TestServices_Live fails:
           two GETs yield two /control/blocked_services/all hits;
           SetResponse on that path with a one-entry catalogue changes the next
           response to that single service
         - if a missing client errors, TestBlocked_Missing fails: Alice with
           ["Kid phone", "Ghost"] returns 200 with clients [{Ghost, missing
           true}, {Kid phone, missing false}] and youtube blocked
         - if the 502 mapping is lost, TestBlocked_AdGuardDown fails:
           fake.Close() -> 502 adguard_unavailable on /clients, /services and
           /children/{id}/blocked; SetStatus("/control/blocked_services/get",
           500) -> 502 on /clients and /blocked with no partial body;
           SetAuth(false) -> 502 (never 401 to the parent)
         - if the fold is not re-run per request, TestBlocked_Live fails: GET
           /blocked, MutateClient adds "roblox" to "Kid phone", GET /blocked
           again shows roblox blocked and fake.Requests() has two
           /control/blocked_services/all hits
         - if an unknown child leaks AdGuard calls, TestBlocked_NotFound
           fails: /children/999/blocked is 404 and fake.Requests() gained no
           /control/* entry

  t-9  Migration plan and confirm endpoints
       files:    internal/api/migration.go, internal/api/migration_test.go,
                 internal/api/api.go
       covers:   c-7
       depends_on: [t-5, t-2, t-3]
       description:
         GET /api/v1/migration: Clients() + ListChildren() ->
         blocked.MigrationPlan over every mapped client name -> {needed: bool,
         global_blocked_services: [], plan: [{client, child: {id, name}, gains,
         result}]}; needed is len(plan) > 0. Never writes. POST
         /api/v1/migration body {plan: [{client, result}]} (4 KiB limit,
         DisallowUnknownFields): recompute the plan live; if the set of
         (client -> result) differs from the body -> 409 conflict "the offer
         changed — review it again", nothing written; else
         MigrateFromGlobal(client, global) in plan order; ErrNotOnGlobal is
         skipped (another tab got there first); any other error -> 502 with
         the already-written clients logged; success 200 {applied: [names]}.
       test_contract:
         - if the plan is computed from the wrong set,
           TestMigration_PlanScope fails: children Alice ["Kid tablet"] give
           needed true and plan [{Kid tablet, child Alice, gains [roblox,
           tiktok], result [roblox, tiktok]}]; with no children needed is
           false and plan []; SetResponse("/control/blocked_services/get",
           200, {"ids":[]}) gives needed false even with Kid tablet mapped
         - if GET writes, TestMigration_GetIsReadOnly fails: after GET
           /migration fake.Requests() holds no POST
         - if a stale plan is applied, TestMigration_StalePlan fails: fetch
           the plan, SetResponse changes the global list to ["tiktok"], POST
           the old plan -> 409 conflict and no /control/clients/update request
         - if confirm is not required, TestMigration_ApplyWrites fails: POST
           the current plan -> 200 {applied: ["Kid tablet"]}; LastUpdate has
           use_global_blocked_services false and blocked_services [roblox,
           tiktok]; a following GET /migration has needed false; the fake
           received no /control/blocked_services/set and GET
           /control/blocked_services/get is unchanged
         - if only mapped clients are not respected,
           TestMigration_UnmappedUntouched fails: MutateClient flips "Old
           laptop" to use_global true while unmapped; the plan omits it and
           the apply leaves its flag true
         - if a mid-way failure is hidden, TestMigration_PartialFailure fails:
           two mapped global clients (MutateClient flips "Kid phone"),
           SetUpdateStatus on the second, POST -> 502 adguard_unavailable, the
           first client's flag is false in a re-read, the second's still true,
           and GET /migration now lists only the second
         - if a double submit rewrites, TestMigration_DoubleSubmit fails: two
           sequential POSTs of the same plan -> first 200, second 409 (plan
           now empty) with exactly one /control/clients/update in Requests()

  t-10 Home migration banner
       files:    web/src/lib/MigrationBanner.svelte,
                 web/src/lib/MigrationBanner.test.ts, web/src/lib/Home.svelte
       covers:   c-7
       depends_on: [t-6, t-4]
       description:
         MigrationBanner: on mount migrationPlan(); rendered only when needed
         and !$migrationDismissed. Lists one line per step "<client> (<child>)
         gains <ids>" (or "keeps its list" when gains is empty). "Migrate" ->
         applyMigration(plan) -> on 200 hides and calls onApplied (Home
         re-fetches its blocked views); on 409 re-fetches the plan and shows
         role="alert" "The offer changed — review it again"; on 502 alert with
         the adguard message. "Not now" sets migrationDismissed true. Home
         mounts it above the cards with onApplied wired to its reload.
       test_contract:
         - if the condition is wrong, "hidden when not needed" fails: plan
           {needed: false} renders nothing; {needed: true} renders the banner
           with each client, child and gains id text
         - if anything is written before confirm, "no POST until Migrate"
           fails: after render and 'Not now' the mocked fetch saw only GET
           /api/v1/migration
         - if dismissal is not session-scoped, "not now hides until login"
           fails: click Not now, unmount, render again with needed true ->
           hidden; migrationDismissed.set(false) (what login() does) and
           render -> visible
         - if a stale plan is silently retried, "409 refreshes the offer"
           fails: Migrate answers 409 -> alert text shown, a second GET
           /migration follows and its new gains render
         - if success does not refresh the home list, "apply calls
           onApplied" fails: Migrate answers 200 -> banner gone, onApplied
           called once; Home.test "migration applied re-fetches blocked views"
           sees a second GET /children/1/blocked
         - if the exact plan is not echoed, "POST body is the plan" fails: the
           POST body equals {plan: [{client, result}]} for every step shown

## Coverage

| criterion | tasks |
|---|---|
| c-1 | t-1, t-5 |
| c-2 | t-8 |
| c-3 | t-8 |
| c-4 | t-3, t-8 |
| c-5 | t-4, t-7 |
| c-6 | t-4, t-6 |
| c-7 | t-2, t-3, t-4, t-9, t-10 |
| c-8 | t-1 |

## Judgment calls

- Zero present clients folds to "unblocked", not "blocked": "every client blocks it" is vacuously true over an empty set, and that bug would show a child with only a missing device as fully blocked. t-3 owns it with an explicit test.
- Missing clients are excluded from the fold rather than counted as "differs": counting them would make every service partial for a child with one renamed device and hide real drift (locked dangling_client says report, not error).
- Partial carries both blocked_on and unblocked_on, always populated: "which clients differ" is ambiguous (differ from what?), and phase 04's "unblock the remainder" needs the blocked side while the parent reads the unblocked side. Two arrays cost nothing and remove the ambiguity.
- Unknown service ids (in a client list, not in the catalogue) are appended, not dropped: dropping hides a real block after an AdGuard upgrade; rejected filtering to the catalogue.
- Migration is plan-echo with server recompute (GET returns the plan, POST must echo it, mismatch is 409): rejected "POST applies whatever is current" because the parent would confirm a list they never saw if AdGuard changed between render and click. Rejected client-computed plans because preview and apply must share one function (t-3's MigrationPlan).
- MigrateFromGlobal unions with the fresh read inside the mutex rather than writing the plan's result verbatim: shrinks the read-write window to one locked RMW; the plan's result is a preview, the write is truthful. ErrNotOnGlobal makes a double submit from two tabs a no-op instead of a rewrite.
- Partial migration failure returns 502 and leaves the state honest: no rollback (AdGuard has no transaction) and no retry loop; the locked "condition is the state" decision makes the banner re-offer only the remaining clients, so t-9 tests exactly that.
- Ownership pre-checks run inside the store transaction and name the owner, with the UNIQUE constraints kept as the safety net: the single-connection pool makes the check race-free, and a 409 that can name "Bob" is what c-5 needs; rejected parsing modernc's constraint error text as the primary path.
- children.name is NOCASE unique: "Alice" and "alice" as two children is a parent's typo, not intent. Rejected exact-match since the spec only says "unique" and NOCASE is the stricter reading.
- GET /api/v1/clients stays minimal (name, ids, use_global_blocked_services, child) — rejected adding blocked_services and the global list to it because the banner has its own endpoint and every extra field is a second source of truth for the settings page.
- Home uses the per-child /blocked endpoint (N+1 requests) rather than a bulk endpoint: c-4 mandates the per-child route, and per-card failure isolation (child 2 renders while child 1 shows 502) is a property the risk lens wants; rejected the bulk route as a second code path to keep truthful.
- "Not now" is a module-level Svelte store reset inside login()/logout() (t-4), not component state: component state would resurface the banner on every navigation, and a persisted flag is locked out.
- t-5 owns every Deps/interface change to api.go so t-8 and t-9 only append route lines: two wave-3 tasks still touch api.go, but additive one-line conflicts are trivial versus t-8/t-9 each redefining the AdGuard interface.
- api.ts (t-4) sits in wave 1 against a contract pinned verbatim in t-5/t-8/t-9's descriptions rather than waiting for the backend: the shapes are fixed in this plan, and keeping the SPA in wave 2 would serialise the whole frontend behind the API for no information gain.
