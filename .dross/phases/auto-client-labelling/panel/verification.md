# Planner draft — LENS: verification

Method: for each criterion I wrote the test that would prove it first (the
"ideal contract"), then cut the smallest task that makes that test passable.
Fixture numbers below are the real ones in
`internal/adguard/adguardtest/testdata/` (Kid phone = `[youtube, tiktok]`, own
list, ids `aa:bb:cc:dd:ee:01, 192.168.1.50`; Kid tablet inherits global; global
= `[tiktok, roblox]`; Old laptop = `null` own list, ids `192.168.1.77`).

### Ideal contracts per criterion (the design input)

- **c-1** `GET /api/v1/clients` → `unlabelled` holds every auto_client whose IP is
  not a persistent id. The fixture gets an auto_client at `192.168.1.50` (Kid
  phone's id). It must be absent while ARP / rDNS / DHCP-sourced ones are present,
  ordered by hostname case-insensitively. A leased IP's row carries `ids: [ip, mac]`,
  an unleased one `ids: [ip]`. Each GET hits `/control/clients` again.
- **c-2** POST `{name:"  "}` → 400 with 0 AdGuard writes; `{name:"Kid phone"}` →
  409 with 0 `/control/clients/add`; `/control/clients` forced 502 or hung → 502;
  success → 201 and the next GET lists the client as persistent.
- **c-3** POST `{name, ids, child_id: Ada}` → 201; `GET /children/{Ada}` includes
  the name; the fake's new client has `use_global_blocked_services=false`.
- **c-4** On the Children page, submitting an unlabelled row leads to a checkbox
  with that name in every child form, with no `location.reload`.
- **c-5** An empty `unlabelled` renders the fixed sentence; a non-empty one does not.
- **c-6** Grant on Ada for youtube → rename Kid phone → "Ada phone" → the expiry
  timer fires → the fake's "Ada phone" blocks youtube again, and no update POST
  names "Kid phone".
- **c-7** PUT Ada adding Kid tablet (inherits global) while Kid phone is a
  sibling → Kid tablet's own list = `[roblox, tiktok, youtube]` with the flag
  false. PUT that only removes a client → 0 `/control/clients/update`.

---

```
Phase auto-client-labelling — 11 tasks across 5 waves

Wave 1
  t-1  Extend fake AdGuard for device adoption
       files:    internal/adguard/adguardtest/server.go,
                 internal/adguard/adguardtest/server_test.go,
                 internal/adguard/adguardtest/testdata/clients.json,
                 internal/adguard/adguardtest/testdata/dhcp_status.json (new)
       desc:     auto_clients fixture rows (one at 192.168.1.50 = Kid phone's id,
                 one at a Kid-tablet-MAC-leased IP, uncovered ARP/rDNS/DHCP rows
                 with mixed-case hostnames); GET /control/dhcp/status serving
                 leases + static_leases; POST /control/clients/add storing the
                 client (400 on a duplicate name); clients/update answering 400
                 when data.name collides with another client; SetAutoClients /
                 SetLeases knobs and a ClientField(name, key) observer.
       covers:   c-1, c-2, c-6 (test substrate)
       contract: - after POST /control/clients/add {name:"Printer"}, the next
                   GET /control/clients lists "Printer" under clients — if add
                   does not persist, Fake_AddPersists fails
                 - a second add with name "Kid phone" answers 400 and the client
                   count does not change
                 - clients/update {name:"Kid phone", data.name:"Kid tablet"}
                   answers 400 and both clients are unchanged
                 - SetAutoClients(nil) makes the next GET serve "auto_clients": []
                   (not null); GET /control/dhcp/status returns the fixture leases
                 - every existing adguard/api/grants test stays green with the
                   enlarged fixture (the added auto_clients are invisible to the
                   current readClients)
       depends:  —
       status:   pending

  t-2  Store RenameClient across mappings and active grants
       files:    internal/store/children.go, internal/store/children_test.go
       desc:     RenameClient(ctx, old, new) in one tx: renames the child_clients
                 row and the grant_clients rows of *active* grants only; a new
                 name already in child_clients → *ErrClientTaken; an unmapped old
                 name is a no-op.
       covers:   c-6
       contract: - Ada owns "Kid phone" → RenameClient("Kid phone","Ada phone") →
                   GetChild(Ada).Clients == ["Ada phone"]; if the child_clients
                   UPDATE is dropped, TestRenameClient_Mapping fails
                 - one active and one expired grant both store "Kid phone" →
                   after rename the active grant's Clients has "Ada phone" and
                   the expired one still has "Kid phone" (WHERE status='active'
                   guard)
                 - Bob owns stale "Ghost" → RenameClient("Kid phone","Ghost")
                   returns *ErrClientTaken naming Bob, and Ada still owns
                   "Kid phone" (tx rolled back, active grant unchanged)
                 - RenameClient on a name nobody owns returns nil and ListChildren
                   is byte-identical before/after
       depends:  —
       status:   pending

  t-3  SPA api functions for devices and rename
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts
       desc:     UnlabelledDevice type {ip, hostname, source, ids}; listDevices()
                 → {clients, unlabelled} (missing unlabelled → []);
                 createClient(name, ids, childId|null); renameClient(old, new).
                 listClients() keeps its array return for Home.
       covers:   c-4, c-6
       contract: - createClient("Printer", ["192.168.1.9"], 1) sends POST
                   /api/v1/clients with body {name, ids, child_id:1} plus the
                   CSRF header; with childId null the body has no child_id key
                 - renameClient("Kid phone","Ada phone") sends PUT to
                   /api/v1/clients/Kid%20phone — if encodeURIComponent is dropped
                   the URL assertion fails
                 - listDevices() on a body without "unlabelled" returns
                   unlabelled: [] (not undefined)
                 - a 409 from createClient surfaces the server's message via
                   messageFor (it does not use the generic text)
       depends:  —
       status:   pending

Wave 2 (depends t-1, t-3)
  t-4  Read auto clients and DHCP leases
       files:    internal/adguard/clients.go, internal/adguard/clients_test.go,
                 internal/adguard/dhcp.go (new), internal/adguard/dhcp_test.go (new)
       desc:     ClientsResult gains AutoClients []AutoClient{IP, Hostname,
                 Source}, decoded from the same GET /control/clients (no extra
                 call); Client.DHCPLeases(ctx) → ip→mac from leases +
                 static_leases.
       covers:   c-1
       contract: - Clients() returns the fixture auto_clients with source
                   verbatim ("ARP", "rDNS", ...) while still issuing exactly one
                   GET /control/clients — a second GET fails the count assertion
                 - auto_clients null or missing → AutoClients is [] (not nil)
                 - DHCPLeases maps a static_leases entry as well as a dynamic one;
                   if static_leases is ignored, TestDHCPLeases_Static fails
                 - /control/dhcp/status forced 500 → DHCPLeases returns a
                   *StatusError (the caller decides whether to degrade)
       depends:  t-1
       status:   pending

  t-5  AdGuard writes: add, rename, adopt
       files:    internal/adguard/client_admin.go (new),
                 internal/adguard/client_admin_test.go (new)
       desc:     AddClient(ctx, NewClient{Name, IDs, Blocked *[]string}) under the
                 rmw mutex with a name/id pre-check (ErrClientExists,
                 *ErrIDTaken); RenameClient(old, new) via updateClient setting
                 data.name; AdoptBlockedServices(name, add, inherited) = union
                 onto the effective list (inherited when the live flag is true,
                 own list otherwise), flag forced false, write skipped when nothing
                 changes.
       covers:   c-2, c-6, c-7
       contract: - AddClient("Kid phone") returns ErrClientExists and the fake
                   records 0 POST /control/clients/add
                 - AddClient with id "192.168.1.50" returns *ErrIDTaken naming
                   "Kid phone", 0 adds
                 - AddClient with Blocked=nil posts use_global_settings=true and
                   use_global_blocked_services=true (AdGuard defaults); with
                   Blocked=&["tiktok"] it posts blocked_services ["tiktok"] and
                   use_global_blocked_services=false
                 - RenameClient("Kid phone","Ada phone"): the update body has
                   name "Kid phone", data.name "Ada phone", and data.future_field
                   == 42 (verbatim carry-over); a target name that exists →
                   ErrClientExists with 0 updates; an unknown old name →
                   ErrClientNotFound
                 - AdoptBlockedServices("Kid tablet", ["youtube"],
                   ["tiktok","roblox"]) → the fake's own list is
                   [roblox,tiktok,youtube] with the flag false. If the inherited
                   branch is dropped, roblox is missing and the test fails
                 - AdoptBlockedServices("Kid phone", ["tiktok"], …) on an
                   own-list client already blocking tiktok with the flag false →
                   0 update POSTs. A union→replace mutation drops youtube and
                   fails the superset assertion
       depends:  t-1
       status:   pending

  t-6  Unlabelled devices section on Children page
       files:    web/src/lib/UnlabelledDevices.svelte (new),
                 web/src/lib/UnlabelledDevices.test.ts (new),
                 web/src/lib/Children.svelte, web/src/lib/Children.test.ts
       desc:     a component that lists `unlabelled` in server order, with a name
                 input prefilled with the hostname, an ids input prefilled with
                 ids.join(", "), an optional child <select> and a Save that calls
                 createClient and then onCreated. Children loads via listDevices
                 and calls load() from onCreated. The empty state shows the fixed
                 "connect to the network" sentence.
       covers:   c-4, c-5
       contract: - rows render in the array order given; the name input value ==
                   hostname and the ids input value == "192.168.1.70,
                   aa:bb:cc:dd:ee:70"
                 - editing ids to "192.168.1.70" then saving sends ids
                   ["192.168.1.70"] (the edited value, not the prefill)
                 - picking "Ben" sends child_id 2; leaving "No child" sends no
                   child_id
                 - after the 201 the mock server state includes the client; the
                   Children test then finds a checkbox labelled with the new name
                   in every child form, checked in Ben's, with
                   window.location.reload never called. If onCreated→load is not
                   wired, the checkbox lookup throws
                 - a 409 shows the server message in role=alert and the row keeps
                   the typed name
                 - unlabelled: [] → the text "A device has to connect to your
                   network and show up in AdGuard Home before you can name it"
                   is visible; with one row it is absent (a test for each branch)
       depends:  t-3
       status:   pending

Wave 3 (depends t-2, t-4, t-5, t-6)
  t-7  List unlabelled devices in GET /clients
       files:    internal/api/clients.go, internal/api/clients_test.go,
                 internal/api/api.go
       desc:     the response gains `unlabelled: [{ip, hostname, source, ids}]`.
                 An auto_client is dropped when its IP is a persistent id, or its
                 leased MAC is one. Rows are sorted by lowercased hostname with IP
                 as the tiebreak and empty hostnames last. ids = [ip, mac] when
                 leased. A DHCP read failure degrades to IP-only ids, while a
                 /control/clients failure is still 502. DHCPLeases is added to the
                 api.AdGuard interface.
       covers:   c-1
       contract: - the fixture auto_client at 192.168.1.50 is absent from
                   unlabelled. If the covered-by-IP filter is removed,
                   TestClients_Unlabelled_CoveredHidden fails
                 - the auto_client whose leased MAC is Kid tablet's
                   aa:bb:cc:dd:ee:02 is absent
                 - ARP, rDNS and DHCP-sourced rows are all present (no source
                   filter)
                 - order is alpha-laptop, printer, Zed-TV; a case-sensitive sort
                   puts Zed-TV first and fails
                 - the leased row has ids [ip, mac] and the unleased printer has
                   ["192.168.1.9"]
                 - fake.SetAutoClients adding a row between two GETs → the second
                   GET lists it (read live)
                 - /control/dhcp/status forced 500 → still 200 with IP-only ids;
                   /control/clients forced 500 → 502
       depends:  t-4
       status:   pending

  t-8  Engine: adopt, create and rename clients
       files:    internal/grants/grants.go, internal/grants/clients.go (new),
                 internal/grants/clients_test.go (new)
       desc:     all three run under e.mu with one applyContext.
                 Baseline(child, siblings) = union of each sibling's effective
                 list (global when it inherits) and the services of the child's
                 active grants. CreateClient(name, ids, childID, siblings) writes
                 AddClient with Blocked=&baseline when it has a child and nil when
                 it does not. AssignClients(childID, siblings, added) runs
                 AdoptBlockedServices per added client and skips ErrClientNotFound.
                 RenameClient(old, new) does the AdGuard rename then
                 store.RenameClient, and renames back in AdGuard when the store
                 fails. The AdGuard and Store interfaces are extended.
       covers:   c-3, c-6, c-7
       contract: - an active grant on Ada for youtube (Kid phone now [tiktok]) →
                   CreateClient("Printer", …, Ada, ["Kid phone"]) gives the fake's
                   Printer [tiktok, youtube] with the flag false. If grant
                   services are left out of Baseline, youtube is missing
                 - advance the clock past that grant's ends_at → Printer is still
                   [tiktok, youtube] and no update POST names "Printer" (it is not
                   among the grant's stored clients)
                 - CreateClient with childID 0 → the fake's Printer has
                   use_global_blocked_services=true
                 - AssignClients(Ada, ["Kid phone"], ["Kid tablet"]) → Kid tablet
                   own list [roblox, tiktok, youtube] with the flag false
                   (sibling's own list ∪ its inherited global)
                 - AssignClients with added = ["Ghost"] (unknown to AdGuard) →
                   nil error, 0 update POSTs
                 - grant on Ada for youtube over "Kid phone" → RenameClient(
                   "Kid phone","Ada phone") → advance past ends_at → the fake's
                   "Ada phone" blocks youtube, updatesFor() never contains
                   "Kid phone", and the grant is expired. If the store rename is
                   skipped, the revert targets the old name and the test fails
                 - Bob owns stale "Ghost" → RenameClient("Kid phone","Ghost")
                   returns *ErrClientTaken and the fake still has a client named
                   "Kid phone" (compensating rename)
                 - TestEngine_Serialised-style: a concurrent Create and
                   CreateClient never exceed MaxInFlightUpdates()==1
       depends:  t-2, t-5
       status:   pending

  t-9  Rename control for listed clients
       files:    web/src/lib/Children.svelte, web/src/lib/Children.test.ts
       desc:     a "Devices" list of every persistent client, each with a Rename
                 action (inline input + Save) that calls renameClient and then
                 load(). It is offered for unassigned clients too.
       covers:   c-6
       contract: - renaming "Kid phone" to "Ada phone" sends PUT
                   /api/v1/clients/Kid%20phone {name:"Ada phone"}; after the
                   reload Ada's form has "Ada phone" checked and no struck-through
                   "Kid phone" row
                 - an unassigned client (child null) also shows Rename
                   (rename_scope). If the control is gated on c.child, the lookup
                   fails
                 - a 409 shows the server's message and the client list still
                   shows "Kid phone"
                 - an empty rename input disables Save, and no fetch is made
       depends:  t-6, t-3
       status:   pending

Wave 4 (depends t-7, t-8)
  t-10 POST create and PUT rename client endpoints
       files:    internal/api/client_admin.go (new),
                 internal/api/client_admin_test.go (new), internal/api/api.go
       desc:     POST /api/v1/clients {name, ids, child_id?}: the name is trimmed
                 and 1–256 bytes, ids trimmed, deduped and non-empty, else 400. A
                 child_id is looked up (404) and checked against store ownership
                 (409) before any AdGuard write. Engine.CreateClient runs, then
                 store.UpdateChild(child, clients+name), and the answer is 201
                 clientView. PUT /api/v1/clients/{name} {name} → Engine.
                 RenameClient. ErrClientExists/*ErrIDTaken/*ErrClientTaken → 409,
                 ErrClientNotFound → 404, other AdGuard errors → 502. Routes and
                 Grants interface methods are added.
       covers:   c-2, c-3, c-6
       contract: - POST {name:"   ", ids:["10.0.0.5"]} → 400 and the fake records
                   0 requests after login. If the trim is dropped, the
                   whitespace name reaches AdGuard and the count fails
                 - POST {name:"Kid phone", …} → 409; POST with ids ["192.168.1.50"]
                   → 409 naming "Kid phone"; 0 /control/clients/add in both
                 - fake.SetStatus("/control/clients", 502) → POST answers 502
                   with code adguard_unavailable; fake.Hang beyond ApplyTimeout →
                   502, not a hang
                 - POST {name:"Printer", ids, child_id: Ada} → 201, GET
                   /children/{Ada} clients contains "Printer", and the fake's
                   Printer has use_global_blocked_services=false
                 - POST with child_id 999 → 404 and 0 adds; POST naming a client
                   that Bob's store row already owns (stale "Ghost") with
                   child_id Ada → 409 and 0 adds
                 - POST without child_id → 201 and no child lists it
                 - PUT /clients/Kid%20phone {name:"Ada phone"} → 200, Ada's
                   clients == ["Ada phone"], and GET /grants shows the active
                   grant's clients renamed
                 - PUT renaming an unassigned "Old laptop" → 200 (rename_scope)
                 - PUT {name:""} → 400; PUT to a name that exists → 409; PUT
                   /clients/Nope → 404; AdGuard down → 502 with the store mapping
                   unchanged
                 - no X-Requested-With → 403 on both routes (they sit behind the
                   csrf chain)
       depends:  t-7, t-8
       status:   pending

Wave 5 (depends t-10)
  t-11 Apply child baseline on client assignment
       files:    internal/api/children.go, internal/api/children_test.go
       desc:     handleChildUpdate decodes first. It loads the child (404) and
                 pre-checks the name and the client owners against ListChildren
                 (409) before any AdGuard write. With added = new − old and
                 siblings = old ∩ new, it calls Engine.AssignClients when added is
                 non-empty (an AdGuard failure → 502 with the store untouched),
                 then UpdateChild. Removals never call AdGuard.
       covers:   c-7
       contract: - Ada has Kid phone, an active grant on youtube exists, and the
                   PUT adds "Old laptop" → 200. The fake's Old laptop is
                   [tiktok, youtube] with the flag false
                 - the PUT adds "Kid tablet" (inherits global [tiktok, roblox]) →
                   own list [roblox, tiktok, youtube] with the flag false, so
                   nothing it effectively blocked is removed
                 - a client with a pre-existing own entry ("steam", seeded via
                   MutateClient) keeps "steam" after the assignment
                 - a PUT that only drops "Kid phone" → 200 and 0 POST
                   /control/clients/update. If the diff is computed backwards,
                   the count fails
                 - a PUT that only renames the child → 0 update POSTs
                 - a PUT adding a client Ben owns → 409 and 0 update POSTs; a PUT
                   renaming Ada to "ben" while adding a client → 409 and 0 update
                   POSTs
                 - fake.SetUpdateStatus("Old laptop", 500) → 502, and
                   GET /children/{Ada} does not contain "Old laptop" (AdGuard
                   first, store second)
                 - adding a name AdGuard doesn't know ("Ghost") → 200, stored,
                   0 update POSTs (existing struck-through behaviour preserved)
       depends:  t-10, t-8
       status:   pending
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 unlabelled listing | t-1, t-4, t-7 |
| c-2 POST create / 400 / 409 / 502 | t-1, t-5, t-10 |
| c-3 create + assign in one action | t-8, t-10 |
| c-4 Unlabelled devices UI, appears without reload | t-3, t-6 |
| c-5 empty-state sentence | t-6 |
| c-6 rename updates mapping + active grants | t-1, t-2, t-3, t-5, t-8, t-9, t-10 |
| c-7 baseline on PUT /children assign | t-5, t-8, t-11 |

All 7 criteria are covered. Each criterion's ideal contract above is asserted
verbatim in at least one task: c-1 in t-7, c-2/c-3 in t-10, c-4/c-5 in t-6,
c-6 end-to-end in t-8 (timer revert) and t-10 (HTTP), c-7 in t-11.

## Judgment calls

- **Baseline, create and rename live in `grants.Engine` under `e.mu`.** I rejected
  computing the baseline in the API handler. Grant Create/revert read and write the
  same clients, so a race would let a new device inherit a mid-flight unblock or
  let a revert hit the pre-rename name. Only the engine mutex makes the c-6 timer
  contract deterministic.
- **"Already blocks" means the effective list.** A client inheriting global unions
  from the global list, not its stale own list. I rejected unioning the own list:
  the fixture's Kid tablet would lose `roblox` when its flag flips to false, which
  contradicts "nothing it already blocks is removed".
- **"Covered" also hides an auto_client whose DHCP-leased MAC is a persistent id.**
  I rejected the strictly-IP rule. A MAC-only client (Kid tablet) would show as
  unlabelled, and saving it collides on the id in AdGuard. This is a superset of
  c-1's IP rule and fits the locked "not covered by a persistent client". The judge
  can strip it: it is one contract line in t-7.
- **Name and id collisions are pre-checked in the app under the rmw mutex
  (ErrClientExists / ErrIDTaken → 409).** I rejected parsing AdGuard's 400 body.
  The pre-check makes 409 deterministic against the fake and the real server.
- **An unassigned new client is posted with `use_global_settings=true,
  use_global_blocked_services=true`.** I rejected a bare `{name, ids}`. AdGuard
  zero-values missing booleans, which would create a device with filtering off.
  That is not "AdGuard's defaults".
- **PUT /children writes AdGuard first and the store second, with every 409
  pre-checked before any write.** I rejected store-first. A failed AdGuard write
  after a store commit leaves no diff for a retry, so the baseline would never land.
  AdGuard-first plus a 502 keeps the PUT retryable.
- **Rename writes AdGuard then the store, and renames back in AdGuard on a store
  error.** I rejected store-first: AdGuard's own collision check must run before any
  local change. Only a stale store mapping can fail after AdGuard succeeds, and the
  t-2/t-8 "Ghost" contract tests that path directly.
- **Rename touches only active grants' `grant_clients`.** I rejected renaming all
  rows. The spec scopes it to active grants, and expired rows are history.
- **A DHCP lease-table failure degrades to IP-only ids instead of a 502.** The
  lease table only fills in a suggestion, and the ids field can be edited anyway.
  The listing must not go down because DHCP is off or errors.
- **Wave 5 for t-11.** t-10 and t-11 both edit `api.go`'s Grants interface. I
  rejected running them in parallel to save a wave, because two parallel writers on
  one interface block means a merge conflict. The dependency is real but file-level.
- **The fake gets its own wave-1 task (t-1).** I rejected folding the fake changes
  into each consumer. Four downstream tasks assert against `/control/clients/add`,
  `/dhcp/status` and the auto_clients fixture. One owner keeps the fixture
  consistent, and its own self-tests catch a fake that silently accepts duplicates.
- **The UI split: a new `UnlabelledDevices.svelte` (t-6), with rename in
  `Children.svelte` (t-9).** I rejected putting everything in Children.svelte. The
  c-4/c-5 contracts can then be unit-tested in isolation, and only the "appears
  among checkboxes" contract needs the full page.
