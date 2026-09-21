# MVP lens — 03-children-and-blocked-view

Bias applied: smallest task set that satisfies every criterion. Six tasks, three
waves. The API JSON contracts are pinned in the task descriptions so the two web
tasks run against them in parallel with the backend (fetch is mocked in every
web test today; nothing in `web/` imports Go output).

```
Phase 03-children-and-blocked-view — 6 tasks across 3 waves

Wave 1
  t-1  Children schema + store, kill Delete survivor
       files:    internal/store/migrations/0002_children.sql,
                 internal/store/children.go,
                 internal/store/children_test.go,
                 internal/store/sessions_test.go
       covers:   c-1, c-8
       depends:  —
       description:
         0002_children.sql: children(id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE)
         and child_clients(client_name TEXT PRIMARY KEY, child_id INTEGER NOT NULL
         REFERENCES children(id) ON DELETE CASCADE) + index on child_id. Only the
         persistent-client NAME is stored (client_identity, locked). children.go:
         Child{ID int64, Name string, Clients []string}; ErrChildNotFound,
         ErrDuplicateName, ErrClientTaken{Client, ChildName string}; ListChildren,
         GetChild(id), CreateChild(name, clients) (Child, error), UpdateChild(id, name,
         clients) (Child, error), DeleteChild(id) (bool, error). Create/Update run in one
         tx: SELECT the name conflict → ErrDuplicateName, SELECT any client mapped to a
         different child → ErrClientTaken (carrying the other child's name for the 409
         message), then write (Update deletes and re-inserts the client set). Clients are
         returned sorted, always non-nil. Every rows cursor is closed before the next
         query (single-connection pool). sessions_test.go gains the c-8 test.
       contract:
         - if the schema drifts, TestSchema_Children fails: PRAGMA table_info lists
           children(id, name) and child_clients(client_name, child_id) with NOT NULL as
           stated; index_list shows UNIQUE on children.name and PRIMARY KEY on
           child_clients.client_name; foreign_key_list(child_clients) shows ON DELETE
           CASCADE to children
         - if name uniqueness is lost, TestChildren_DuplicateName fails: CreateChild("Alice")
           twice returns ErrDuplicateName on the second call and ListChildren has one row
         - if the one-child-per-client rule is lost, TestChildren_ClientTaken fails:
           CreateChild("Bob", ["Kid phone"]) after Alice owns "Kid phone" returns
           ErrClientTaken{Client:"Kid phone", ChildName:"Alice"}; UpdateChild(alice,
           "Alice", ["Kid phone","Kid tablet"]) succeeds (a child's own client is not a
           conflict)
         - if Update is not atomic, TestChildren_UpdateAtomic fails: UpdateChild(bob, "Bob",
           ["Old laptop","Kid phone"]) (second client taken) returns ErrClientTaken and
           GetChild(bob).Clients is unchanged from before the call
         - if cascade is missing, TestChildren_DeleteCascades fails: after DeleteChild(alice)
           == true, CreateChild("Bob", ["Kid phone"]) succeeds and child_clients has no
           row with child_id == alice
         - if rows do not persist, TestChildren_Reopen fails: CreateChild, Close, Open on the
           same dir, ListChildren returns the same id, name and clients
         - if Delete swallows the exec error, TestSessions_DeleteClosed fails: Close() then
           Delete(ctx, 1) returns a non-nil error whose text starts with "delete session:"
           and errors.Unwrap(err) != nil (kills survivor 7682485717754728 at
           internal/store/sessions.go:79)

  t-2  AdGuard write that flips use_global flag
       files:    internal/adguard/clients.go,
                 internal/adguard/clients_test.go
       covers:   c-7
       depends:  —
       description:
         Extract the read-modify-write in SetBlockedServices into updateClient(ctx, name,
         mutate func(data map[string]json.RawMessage)) under c.rmw. Add
         AdoptBlockedServices(ctx, clientName string, ids []string) error: same RMW,
         writes blocked_services = sorted/deduped ids AND use_global_blocked_services =
         false, every other field verbatim. SetBlockedServices keeps its behaviour
         (including the global-list Warn). Nothing touches /control/blocked_services/set.
       contract:
         - if the flag is not flipped or the list not written, TestAdoptBlockedServices_Writes
           fails: AdoptBlockedServices("Kid tablet", ["tiktok","roblox","tiktok"]) makes
           fake.LastUpdate() carry name "Kid tablet", data.use_global_blocked_services ==
           false, data.blocked_services == ["roblox","tiktok"], and data.safe_search /
           data.tags byte-identical to the fixture; a following Clients() read shows
           UseGlobalBlockedServices false for Kid tablet
         - if the shared helper drops fields, TestSetBlockedServices_Preserves fails: the
           update body for "Kid phone" still carries future_field 42 and the unchanged
           use_global_blocked_services false
         - if the mutex is bypassed, TestAdoptBlockedServices_Serialised fails: 10 goroutines
           mixing SetBlockedServices and AdoptBlockedServices with Hang("/control/clients",
           20ms) leave fake.MaxInFlightUpdates() == 1
         - if an unknown name is written, TestAdoptBlockedServices_NotFound fails:
           AdoptBlockedServices("Ghost", nil) returns ErrClientNotFound and fake.Requests()
           has no POST /control/clients/update

  t-5  Children settings page + API bindings
       files:    web/src/lib/api.ts,
                 web/src/lib/api.test.ts,
                 web/src/lib/Children.svelte,
                 web/src/lib/Children.test.ts
       covers:   c-5
       depends:  —
       description:
         api.ts: Method gains 'PUT'; Route gains 'children' (path /children); types Child
         {id, name, clients: string[]}, ClientView {name, ids, use_global_blocked_services,
         child: {id, name} | null}, Service {id, name, icon}, BlockedView, MigrationOffer;
         functions listChildren, createChild, updateChild, deleteChild, clients, services,
         blocked(id), migrationOffer, migrate — paths exactly as in t-3/t-4. Children.svelte
         (props: onBack): on mount fetches GET /children and GET /clients; create form
         (name) → POST; per child: rename input + Save → PUT, Delete → DELETE, one checkbox
         per AdGuard client (checked when in child.clients; disabled with the other child's
         name in the label when client.child is a different child); a name in child.clients
         absent from /clients is rendered inside <s> with a Remove button that unchecks it;
         Save sends PUT {name, clients} and on success re-fetches both lists so the
         rendered state is the server's; ApiError → <p role="alert"> with the server
         message. Routing to the page is wired in t-6.
       contract:
         - if a binding drifts, api.test.ts fails: listChildren → GET /api/v1/children;
           createChild → POST /api/v1/children with body {name, clients}; updateChild(3,..)
           → PUT /api/v1/children/3; deleteChild(3) → DELETE /api/v1/children/3; clients →
           GET /api/v1/clients; services → GET /api/v1/services; blocked(3) → GET
           /api/v1/children/3/blocked; migrationOffer → GET /api/v1/migration; migrate →
           POST /api/v1/migration; each call carries X-Requested-With: adguard-reward;
           routeFor('/children') is 'children' and navigate('children') pushes '/children'
         - if assignment rendering breaks, Children.test.ts "checkboxes" fails: with Alice
           [Kid phone] and Bob [Kid tablet], Alice's row shows "Kid phone" checked, "Kid
           tablet" disabled with label text containing "Bob", "Old laptop" enabled and
           unchecked
         - if missing clients are hidden or auto-pruned, "missing client" fails: Alice [Kid
           phone, Ghost] renders "Ghost" inside an <s> element with a Remove button; no
           PUT is sent until Save; after Remove + Save the PUT body is {name:"Alice",
           clients:["Kid phone"]}
         - if create/rename/delete miss the API, "crud" fails: submitting the create form
           with "Cara" sends POST body {name:"Cara", clients:[]}; changing Alice's name to
           "Ally" + Save sends PUT /api/v1/children/1 with name "Ally"; Delete sends
           DELETE /api/v1/children/1
         - if the page trusts local state, "refetch after save" fails: after a successful
           PUT the mock sees a second GET /api/v1/children and the row shows the name from
           that response, not the typed one
         - if conflicts are silent, "409 shown" fails: a 409 {error:"conflict", message:"a
           child named \"Alice\" already exists"} on POST renders role="alert" with that
           message and the form keeps its value

Wave 2
  t-3  Children CRUD, clients and services endpoints   (depends t-1)
       files:    internal/api/api.go,
                 internal/api/children.go,
                 internal/api/children_test.go,
                 internal/api/clients.go,
                 internal/api/clients_test.go,
                 internal/api/login_test.go   (harness: Children: st, one line)
       covers:   c-1, c-2, c-3
       depends:  t-1
       description:
         api.go: AdGuard interface gains Clients(ctx) (adguard.ClientsResult, error) and
         Services(ctx) ([]adguard.Service, error); Deps gains Children ChildStore (api-local
         interface over the t-1 methods); routes (all behind requireSession): GET/POST
         /api/v1/children, GET/PUT/DELETE /api/v1/children/{id}, GET /api/v1/clients, GET
         /api/v1/services. errors.go-style code CodeConflict = "conflict" (409). children.go:
         body {name, clients[]} (DisallowUnknownFields, 16 KiB cap, name trimmed and
         non-empty else 400, clients deduped); POST → 201 {id, name, clients}; GET list →
         {children: [...]}; PUT → 200 child; DELETE → 204; ErrChildNotFound → 404 not_found;
         ErrDuplicateName / ErrClientTaken → 409 conflict with a message naming the child
         (and client). clients.go: GET /clients calls AdGuard.Clients on every request, joins
         against ListChildren → {clients: [{name, ids, use_global_blocked_services, child:
         {id, name} | null}]}; GET /services calls AdGuard.Services on every request →
         {services: [{id, name, icon}]} with icon echoed as served. Any AdGuard error on
         either → 502 adguard_unavailable. No caching anywhere.
       contract:
         - if CRUD or status codes drift, TestChildren_CRUD fails: POST {name:"Alice",
           clients:["Kid phone"]} → 201 with id > 0; GET /children lists it; PUT /children/{id}
           {name:"Ally", clients:["Kid tablet"]} → 200 and GET /children/{id} reflects both;
           DELETE → 204 then GET /children/{id} → 404 not_found
         - if 409s are lost, TestChildren_Conflicts fails: a second POST "Alice" → 409
           error "conflict" with message containing "Alice"; POST {name:"Bob",
           clients:["Kid phone"]} → 409 with message containing both "Kid phone" and "Alice"
         - if validation is loose, TestChildren_Validation fails: name "" and "   " → 400
           bad_request; unknown JSON field → 400; a 20 KiB body → 400; GET/PUT/DELETE
           /children/abc and /children/999 → 404
         - if the routes bypass auth or CSRF, TestChildren_Guarded fails: every new route
           without a cookie → 401; POST/PUT/DELETE with a cookie but without the header → 403
         - if /clients is cached or the join breaks, TestClients_Live fails: two GET
           /api/v1/clients raise the count of GET /control/clients in fake.Requests() by
           exactly one each; the body has Kid phone, Kid tablet, Old laptop with their ids
           and use_global_blocked_services true only for Kid tablet; after POST Alice [Kid
           phone], Kid phone's child is {id, "Alice"} and the other two are null
         - if /services vendors or caches, TestServices_Live fails: two GETs raise the count
           of GET /control/blocked_services/all by one each; the body is exactly youtube,
           tiktok, roblox in fixture order with icon "PHN2Zy8+"
         - if AdGuard failure is not 502, TestCatalogue_Unavailable fails:
           SetStatus("/control/clients", 500) makes GET /clients 502 adguard_unavailable;
           SetStatus("/control/blocked_services/all", 503) makes GET /services 502

  t-6  Home blocked view, migration banner, routing   (depends t-5)
       files:    web/src/App.svelte,
                 web/src/App.test.ts,
                 web/src/lib/Home.svelte,
                 web/src/lib/Home.test.ts
       covers:   c-5, c-6, c-7
       depends:  t-5
       description:
         App.svelte: route 'children' renders Children (onBack → navigate('home')). Home:
         on mount fetches GET /children, then GET /children/{id}/blocked for each child,
         then GET /migration; renders per child its name, clients (badge "uses global
         list" when uses_global, badge "missing" when missing) and every service whose
         state != unblocked as name + <img src="data:image/svg+xml;base64,{icon}">, with a
         "partial" marker and the differing names; any ApiError during load → <p
         role="alert"> "Can't reach AdGuard Home" and no per-child lists. Banner when
         migration.clients is non-empty: one line per client "name (child): gains id, id";
         "Migrate" → POST /migration then reload everything; "Not now" → $state hidden
         (component state, so it is back after the next login/mount; nothing persisted).
         A "Children" link → navigate('children'). Fetch mocks in Home.test.ts are keyed by
         URL, not call order.
       contract:
         - if routing is missing, App.test.ts "children route" fails: a logged-in render at
           /children shows the Children page heading, and clicking Home's "Children" link
           from / lands on /children
         - if the blocked list is wrong, Home.test.ts "lists blocked" fails: with Alice's
           blocked view (tiktok blocked, youtube partial differs [Kid tablet], roblox
           unblocked) the page shows "TikTok" with an <img> whose src is
           data:image/svg+xml;base64,PHN2Zy8+, shows "YouTube" marked partial naming
           "Kid tablet", and does not show "Roblox"
         - if the locked badges are dropped, "badges" fails: Kid tablet (uses_global) carries
           the text "uses global list"; Ghost (missing) carries "missing"
         - if a stale list is shown on failure, "adguard down" fails: a 502
           adguard_unavailable on /children/1/blocked renders role="alert" and no service
           names, even though /children returned Alice
         - if lists are not re-fetched, "refetch on mount" fails: two successive renders
           each issue their own GET /api/v1/children
         - if the banner logic breaks, "banner" fails: migration {clients:[{name:"Kid
           tablet", child:"Alice", gains:["roblox","tiktok"]}]} shows a banner containing
           "Kid tablet", "Alice", "roblox" and "tiktok"; {clients:[]} shows no banner
         - if writes escape the confirm, "not now" fails: clicking "Not now" hides the
           banner and the mock never sees POST /api/v1/migration; clicking "Migrate" sends
           exactly one POST /api/v1/migration and then a fresh GET /api/v1/migration

Wave 3
  t-4  Blocked view, migration endpoints, main wiring   (depends t-2, t-3)
       files:    internal/api/api.go,
                 internal/api/blocked.go,
                 internal/api/blocked_test.go,
                 cmd/adguard-reward/main.go,
                 cmd/adguard-reward/main_test.go
       covers:   c-1, c-4, c-7
       depends:  t-2, t-3
       description:
         api.go: AdGuard interface gains AdoptBlockedServices; routes GET
         /api/v1/children/{id}/blocked, GET /api/v1/migration, POST /api/v1/migration.
         blocked.go: GET blocked reads GetChild, then AdGuard.Clients and AdGuard.Services
         on every request; each mapped name is looked up in Persistent — absent → missing;
         a present client's blocked set is GlobalBlockedServices when
         UseGlobalBlockedServices else its own list (global_list_clients, locked). Per
         catalogue service, over present clients: blocked when all block it, unblocked when
         none (or none present), partial otherwise with differs = names of present clients
         NOT blocking it. Body {child:{id,name}, clients:[{name, missing, uses_global}],
         services:[{id, name, icon, state, differs[]}]}; 404 unknown child; 502 on any
         AdGuard error. GET /migration: candidates = mapped names present in AdGuard with
         UseGlobalBlockedServices true, only when GlobalBlockedServices is non-empty →
         {global:[ids], clients:[{name, child, gains: global minus own list}]}; nothing is
         written. POST /migration recomputes the candidates and for each calls
         AdoptBlockedServices(name, union(own, global)) → 200 {migrated:[names]}; the global
         list is never written; a failure mid-way → 502 naming the client (already-written
         ones stay written; the next GET simply lists the rest). main.go: Deps.Children: st.
         main_test.go: restart test for c-1 through the real binary.
       contract:
         - if the cross-client fold is wrong, TestBlocked_States fails: Alice [Kid phone, Kid
           tablet] → tiktok blocked with empty differs, youtube partial differs ["Kid
           tablet"], roblox partial differs ["Kid phone"]; clients lists Kid tablet
           uses_global true and Kid phone false; each GET adds exactly one GET
           /control/clients to fake.Requests()
         - if a dangling name is an error or is pruned, TestBlocked_Missing fails: Alice [Kid
           phone, Ghost] → 200, clients has {name:"Ghost", missing:true}, youtube and
           tiktok blocked (computed over Kid phone alone), and GET /children/{id} still
           lists Ghost afterwards
         - if failure is not 502, TestBlocked_Unreachable fails: SetStatus("/control/clients",
           500) → 502 adguard_unavailable; SetStatus("/control/blocked_services/all", 500) →
           502; /children/999/blocked → 404
         - if the offer is wrong or writes early, TestMigration_Offer fails: with Kid tablet
           mapped to Alice, GET /migration → global ["tiktok","roblox"], clients [{name:"Kid
           tablet", child:"Alice", gains:["roblox","tiktok"]}] and fake.Requests() has no
           POST /control/clients/update; with only Kid phone mapped → clients []; with
           SetResponse("/control/blocked_services/get", 200, {"ids":[]}) → clients []
         - if confirm writes the wrong thing, TestMigration_Confirm fails: POST /migration →
           200 {migrated:["Kid tablet"]}; fake.LastUpdate() has name "Kid tablet",
           use_global_blocked_services false, blocked_services ["roblox","tiktok"]; no
           request path in fake.Requests() is /control/blocked_services/set; a second GET
           /migration → clients []; an unmapped client mutated to use_global true (Old
           laptop via MutateClient) is neither offered nor written
         - if the write path ignores AdGuard failure, TestMigration_Unreachable fails:
           SetStatus("/control/clients/update", 500) makes POST /migration 502 with a
           message containing "Kid tablet"
         - if the store is not wired in main, TestRun_ChildrenSurviveRestart fails: against
           the running binary POST /api/v1/children {name:"Alice", clients:["Kid phone"]}
           → 201, stop, start on the same data_dir, GET /api/v1/children → the same id,
           name and clients
```

## Coverage

| criterion | tasks |
|---|---|
| c-1 | t-1 (schema, store, reopen), t-3 (CRUD + 409s), t-4 (main wiring, restart through the binary) |
| c-2 | t-3 |
| c-3 | t-3 |
| c-4 | t-4 |
| c-5 | t-5 (page), t-6 (route + link) |
| c-6 | t-6 |
| c-7 | t-2 (flag-flipping write), t-4 (offer + confirm endpoints), t-6 (banner) |
| c-8 | t-1 |

## Judgment calls

- Conflicts detected by SELECT inside the tx, not by parsing SQLite constraint errors: the 409 must name the other child, and the driver's "UNIQUE constraint failed: ..." text carries no child name. Rejected: catch-and-map constraint codes.
- `AdoptBlockedServices` as a separate wave-1 task (t-2) rather than folded into t-4: t-4 already sits at five files and two layers, and the api-local AdGuard interface cannot name a method `*adguard.Client` lacks without breaking `main`'s compile. Rejected: generalising `SetBlockedServices` with an options struct — its callers (phase 04) want the plain signature.
- One `api.go` owner per wave: t-3 edits it in wave 2, t-4 in wave 3. The routes table is one function by design ("kept in one place"), so parallel edits would collide. That is the only reason t-4 is wave 3; it costs nothing since t-6 fills wave 2 on the web side.
- Migration offer is a server endpoint (`GET /api/v1/migration`) rather than computed in the SPA from `/clients`: c-7's "exactly which clients gain which ids" needs the global list, which c-2's `/clients` body does not carry, and having one place compute the candidate set means GET and POST cannot disagree. Rejected: extending `/clients` with the global list.
- `differs` on a partial service = present clients NOT blocking it. Either side is derivable from the response's `clients` array; "not blocking" is the drift from the parental-control norm and is what phase 04's grant closes. Rejected: a `blocked_by` list.
- State folds over present clients only; zero present clients → all unblocked, not all blocked. Vacuous "blocked" would show a child with only missing devices as fully blocked, which is the stale picture the phase is trying to avoid.
- "Not now" is plain component `$state`: it resets on remount, which happens on every login (and on reload, stricter than the locked decision requires but nothing to drift). Rejected: sessionStorage — one more thing to clear on logout.
- `main.go` gets `Children: st` in t-4, not t-3, to keep t-3 at one layer; between t-3 and t-4 the children handlers are registered but the store is nil at runtime. Accepted because no test outside the api harness reaches them until t-4 lands and adds the binary-level restart test.
- No `SetResponse`/fake changes: every c-2/c-4/c-7 case is reachable with the existing fixture (Kid tablet is already `use_global_blocked_services: true` against a non-empty global list) plus `MutateClient`, `SetStatus` and `SetResponse`.
- Web tasks in waves 1–2 alongside the backend: every existing web test stubs `fetch`, so the contract pinned in t-3/t-4's descriptions is all t-5/t-6 need. End-to-end agreement is the verify step's job, not a wave dependency.
