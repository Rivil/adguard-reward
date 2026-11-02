# auto-client-labelling — panel synthesis

Inputs: risk.md (12 tasks / 5 waves), mvp.md (5 / 3), verification.md (11 / 5).
Claims checked against the code: `revert` counts `adguard.ErrClientNotFound` as reverted
(internal/grants/grants.go:199). The fake counts only `/control/clients` and
`/control/clients/update` as in-flight rmw requests (adguardtest/server.go `serve`) and has no
`/control/clients/add` or `/control/dhcp/status` route. `grant_clients` PK is
`(grant_id, client_name)` and `child_clients.client_name` is a PK (0002/0003 migrations).
`listClients()` is used only by Children.svelte, not Home, so verification's "keeps array return
for Home" premise is wrong (harmless). `clientView` already serialises `ids`. Mapping a name
AdGuard lacks ("Ghost", struck through) is existing tested behaviour (Children.test.ts:176).
go.mod is go 1.26, so ServeMux wildcards match an escaped `%2F` within one segment.

## Scores

| Draft | Criteria coverage | Test-contract specificity | Granularity | Wave correctness |
|---|---|---|---|---|
| risk | 5/5: 7/7. Every criterion is tested at fake, unit and HTTP layer, including the c-6 timer-revert race. | 5/5: fixture-exact values, a failure mode per line, a `-race` loop, rollback proven byte-identical. | 4/5: tasks are right-sized. t-10 (api.ts only) is borderline small, and t-7 (GET + POST + interface) is the largest. | 5/5: every edge is a real output dependency or a same-block serialisation (`api.go` Grants interface, engine interfaces, Children.svelte). |
| mvp | 4/5: 7/7 on paper. But c-6's "running grant still reverts" is only tested via End, not timer expiry, and c-1 reads the criterion as an exact IP-string match. | 3/5: lines are mutation-tied, but names are not in the fixture ("tablet", "laptop", "ipad"), and there are no race, rollback-on-AdGuard-failure or CSRF checks. | 2/5: under 5 files per task, but t-1 holds the fake plus 5 AdGuard calls, t-4 holds GET + POST + PUT-rename + baseline, and t-3 holds the api client plus two UI features. Each is well over a sitting. | 4/5: putting the UI in wave 1 against a pinned wire contract is correct, and t-5 → t-4 is a real dependency. The draft is shallow because the tasks are fat, not because the dependencies are wrong. |
| verification | 5/5: 7/7, with an "ideal contract" per criterion stated up front and asserted verbatim. | 5/5: real fixture arithmetic (Kid tablet → [roblox, tiktok, youtube]), a mutation named per line, csrf 403, the Ghost no-op. | 4/5: t-8 (engine create + assign + rename) is heavy for one task. t-3 (api.ts only) is borderline small. | 4/5: the `depends:` fields are correct, but the "Wave 3 (depends t-2, t-4, t-5, t-6)" header lists dependencies t-7 does not have. The t-11 → t-10 edge is file-level (`api.go`), which is legitimate. |

**Skeleton: risk.** It ties verification on coverage and specificity and has the cleanest waves.
It is also the only draft that writes the create-time child mapping inside the engine mutex as a
targeted insert. Verification and mvp both do a full-replace `UpdateChild` from the handler,
outside the mutex. Verification supplies most of the grafted contracts. mvp supplies the pinned
wire contract.

## Merged plan

Tasks are renumbered in wave order. The original ids are in the origin tag.

Pinned wire contract [mvp, shapes adjusted to the merged defaults]. It is shared by t-3, t-6, t-8,
t-10 and t-12, so the UI never waits on the backend:
```
GET  /api/v1/clients        -> {clients: clientView[], unlabelled: [{ip, hostname, source, suggested_ids: string[]}]}
                               unlabelled sorted by hostname (case-insensitive, blank last, IP tie-break);
                               suggested_ids = [ip] or [ip, mac] when a DHCP lease maps the ip
POST /api/v1/clients        {name, ids, child_id: number|null|absent} -> 201 clientView
                               400 empty name / empty ids / AdGuard rejected input, 404 unknown child_id,
                               409 name or id exists in AdGuard or name owned by another child's stale mapping,
                               502 AdGuard unreachable
POST /api/v1/clients/rename {from, to} -> 200 clientView
                               400 empty/oversized to, 404 unknown from, 409 to taken, 502 AdGuard down
PUT  /api/v1/children/{id}  unchanged shape; added clients get the child baseline
```

```
Phase auto-client-labelling — 12 tasks across 5 waves

Wave 1
  t-1  Extend fake AdGuard: add, DHCP, rename  [risk t-1 + verification t-1]
       files:    internal/adguard/adguardtest/server.go,
                 internal/adguard/adguardtest/server_test.go,
                 internal/adguard/adguardtest/testdata/clients.json,
                 internal/adguard/adguardtest/testdata/dhcp_status.json (new)
       covers:   c-1, c-2, c-6 (test substrate)
       desc:     Serve POST /control/clients/add (400 on duplicate name or an id another client holds),
                 GET /control/dhcp/status from a fixture (leases + static_leases), and a 400 when
                 clients/update renames onto an existing name. Extend auto_clients with: 192.168.1.50
                 (Kid phone's id), an IP leased to aa:bb:cc:dd:ee:02 (Kid tablet's MAC), a blank-hostname
                 row, mixed-case ARP/rDNS/DHCP/WHOIS rows. Add SetAutoClients/SetLeases knobs and an
                 AddedClients() observer, and count /control/clients/add in the in-flight rmw set.
       contract: - add {name:"Kid phone"} answers 400 and the next GET /control/clients still lists exactly
                   one "Kid phone"
                 - add with ids ["192.168.1.50"] answers 400 and the client count is unchanged
                 - clients/update {name:"Kid tablet", data:{name:"Kid phone"}} answers 400 and both
                   clients are unchanged
                 - after a successful add of "Printer", GET /control/clients lists it and auto_clients is
                   still present (the clientsJSON re-encode keeps the top-level doc). If add does not
                   persist, Fake_AddPersists fails
                 - SetAutoClients(nil) serves "auto_clients": [] (not null)
                 - GET /control/dhcp/status returns the fixture leases. SetStatus("/control/dhcp/status",
                   501) fails only that read, and GET /control/clients still answers 200
                 - two overlapping add requests raise MaxInFlightUpdates() to 2 (proves add is counted,
                   so t-5's serialisation contract is not vacuous)
                 - every existing adguard/api/grants test stays green with the enlarged fixture
       depends:  —

  t-2  Store: atomic client rename + targeted assign  [risk t-2 + mvp t-2 + verification t-2]
       files:    internal/store/clients.go (new), internal/store/clients_test.go (new)
       covers:   c-3, c-6
       desc:     RenameClient(old, new) in one tx: the child_clients row is renamed, and grant_clients rows
                 of status='active' grants are renamed (deduped when that grant already holds new).
                 *ErrClientTaken when another child holds `new`. An unmapped old name is a no-op.
                 AddChildClient(childID, name) is a single INSERT with the existing conflict check.
       contract: - rename "Kid phone"→"Ada phone" with an active grant on it: ListChildren and
                   ListActiveGrants both show "Ada phone" and neither shows "Kid phone". If the
                   child_clients UPDATE is dropped, TestRenameClient_Mapping fails
                 - an expired grant's grant_clients keeps "Kid phone" (removing the status filter fails
                   this)
                 - an active grant holding both stale "Old tab" and "Kid phone": renaming Kid phone to
                   "Old tab" leaves one "Old tab" row and raises no PK error
                 - child B holds stale "Old tab": renaming onto it returns *ErrClientTaken naming B, and
                   both tables are byte-identical to before (rollback proven)
                 - RenameClient on a name nobody owns returns nil and ListChildren is unchanged
                 - AddChildClient(ada, "New tv") while Ada holds ["Kid phone"] leaves ["Kid phone",
                   "New tv"], never a replace
                 - AddChildClient for a client Ben owns → *ErrClientTaken. Unknown child →
                   ErrChildNotFound
       depends:  —

  t-3  SPA api client: devices, create, rename  [risk t-10 + verification t-3]
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts
       covers:   c-4, c-6
       desc:     UnlabelledDevice {ip, hostname, source, suggested_ids}; listDevices() → {clients,
                 unlabelled}; createClient(name, ids, childId|null); renameClient(from, to) → POST
                 /api/v1/clients/rename. listClients() keeps its array return (only Children.svelte
                 calls it).
       contract: - createClient("TV", ["192.168.1.9"], null) sends exactly {"name":"TV","ids":
                   ["192.168.1.9"],"child_id":null} with the CSRF header. With childId 1, child_id is 1
                 - a response without an `unlabelled` key yields unlabelled: [] (no TypeError)
                 - a 409 from POST /api/v1/clients surfaces the server's message via messageFor, not the
                   generic conflict text
                 - renameClient("a/b%c","x") sends {from,to} in the body, and the URL is exactly
                   /api/v1/clients/rename
       depends:  —

Wave 2
  t-4  Read auto_clients, leases, compute unlabelled set  [risk t-3 + verification t-4]
       files:    internal/adguard/clients.go, internal/adguard/clients_test.go,
                 internal/adguard/devices.go (new), internal/adguard/devices_test.go (new)
       covers:   c-1
       desc:     Clients() also returns AutoClients from the same /control/clients read. Leases(ctx)
                 reads /control/dhcp/status (dynamic + static). The pure Unlabelled(persistent, auto,
                 leases) returns uncovered devices sorted by lowercased hostname (blank last, IP
                 tie-break), each with suggested_ids = IP + leased MAC.
       contract: - auto_client 192.168.1.50 (Kid phone's id) is excluded
                 - an id written "2001:db8::0:1" covers auto IP "2001:db8::1" (netip canonical compare)
                 - a persistent CIDR id "192.168.1.0/28" covers 192.168.1.9
                 - an auto IP leased to aa:bb:cc:dd:ee:02 (Kid tablet's MAC id) is excluded
                 - ARP, rDNS, DHCP and WHOIS rows are all listed (a source filter breaks this)
                 - order is alpha, printer, Zed-TV, then the blank hostname, across two shuffled runs. A
                   case-sensitive sort puts Zed-TV first and fails
                 - a leased device has suggested_ids == [ip, mac], an unleased one [ip]
                 - Clients() issues exactly one GET /control/clients (CountRequests == 1)
                 - auto_clients null or missing → AutoClients is [] (not nil)
                 - a static_leases entry is mapped as well as a dynamic one (TestLeases_Static)
                 - /control/dhcp/status forced 500 → Leases returns *StatusError (the caller degrades)
       depends:  t-1

  t-5  AdGuard writes: add, rename, adopt baseline  [risk t-4 + verification t-5 + mvp t-1]
       files:    internal/adguard/clients_write.go (new), internal/adguard/clients_write_test.go (new)
       covers:   c-2, c-6, c-7
       desc:     All three run under the rmw mutex with a fresh read.
                 AddClient(NewClient{Name, IDs, Blocked *[]string}) pre-checks the name (ErrClientExists)
                 and ids (*ErrIDTaken) before posting. RenameClient(old, new) goes through updateClient.
                 AdoptBlockedServices(name, ids) writes effective set ∪ ids, sets the flag false, and
                 skips the write when nothing changes.
       contract: - Blocked=nil sends use_global_settings=true and use_global_blocked_services=true.
                   Blocked=&["tiktok"] sends use_global_settings=true, use_global_blocked_services=false,
                   blocked_services sorted and never null
                 - duplicate name → ErrClientExists with zero POST /control/clients/add
                 - id "192.168.1.50" → *ErrIDTaken{Owner:"Kid phone"} with zero adds
                 - RenameClient("Kid phone","Ada phone"): the update body has name "Kid phone",
                   data.name "Ada phone", data.future_field == 42, and every other raw field is
                   byte-identical. The next Clients() lists "Ada phone", not "Kid phone"
                 - RenameClient to an existing name → ErrClientExists with 0 updates. Unknown old →
                   ErrClientNotFound
                 - Adopt on Kid tablet (use_global=true, global [tiktok, roblox]) with ["youtube"] writes
                   [roblox, tiktok, youtube] with the flag false. Dropping the inherited branch loses
                   roblox and fails
                 - Adopt on Kid phone with ["tiktok"] (already blocked, flag false) → 0 update POSTs. A
                   union→replace mutation drops youtube and fails the superset check
                 - concurrent AddClient + AddBlockedServices → MaxInFlightUpdates() == 1
       depends:  t-1

  t-6  Unlabelled devices list on children page  [risk t-11 + verification t-6 + mvp t-3]
       files:    web/src/lib/UnlabelledDevices.svelte (new), web/src/lib/UnlabelledDevices.test.ts (new),
                 web/src/lib/Children.svelte, web/src/lib/Children.test.ts
       covers:   c-4, c-5
       desc:     Children.load uses listDevices. UnlabelledDevices renders rows in server order, with the
                 name prefilled from the hostname, editable ids (joined ", "), an optional child picker
                 (default "No child") and a Save button. Save → createClient → onCreated → Children.load.
                 An empty list shows the connect-to-network sentence.
       contract: - device "printer" renders a name input "printer" and ids "192.168.1.70,
                   aa:bb:cc:dd:ee:70". A blank hostname renders empty and blocks submit
                 - rows render in array order given (the UI does not re-sort)
                 - editing ids to drop the MAC POSTs ids ["192.168.1.70"] only
                 - after a 201, GET /api/v1/clients is called again and a "TV" checkbox appears in every
                   child form. The device leaves the unlabelled list and window.location.reload is never
                   called. If onCreated→load is unwired, the checkbox lookup throws
                 - picking Ada POSTs child_id 1, and Ada's "TV" checkbox renders checked
                 - two rapid submits issue one POST (pending lock)
                 - a 409 shows the server's message in role=alert, and the typed name and ids are still in
                   the inputs
                 - unlabelled [] shows "…must connect to the network and appear in AdGuard Home before it
                   can be named". With one row the sentence is absent. A 502 on GET /clients shows the
                   alert and not the sentence
       depends:  t-3

Wave 3
  t-7  Engine: baseline under mutex, create + adopt  [risk t-5 + verification t-8]
       files:    internal/grants/clients.go (new), internal/grants/clients_test.go (new),
                 internal/grants/grants.go
       covers:   c-3, c-7
       desc:     Extend the engine Store/AdGuard interfaces (GetChild, AddChildClient, RenameClient;
                 AddClient, AdoptBlockedServices, RenameClient). Under e.mu with one applyContext:
                 baseline = ∪ siblings' effective sets ∪ services of the child's active grants.
                 CreateClient puts the baseline in the add body, then calls AddChildClient.
                 AdoptClients(childID, siblings, added) adopts per name and skips ErrClientNotFound.
       contract: - Ada has an active grant on youtube (Kid phone now [tiktok]). CreateClient(TV, child=Ada)
                   gives the fake's TV [tiktok, youtube] with the flag false. If grant services are left
                   out of the baseline, youtube is missing
                 - after the clock passes that grant's ends_at, TV is still [tiktok, youtube], no update
                   POST names "TV", and the grant is expired with its stored clients unchanged
                 - Ben's active grant [tiktok] does not enter Ada's baseline
                 - a sibling with use_global=true contributes the global list
                 - CreateClient with no child → the fake's client has use_global_blocked_services=true
                 - AdoptClients(Ada, ["Kid phone"], ["Kid tablet"]) → Kid tablet [roblox, tiktok,
                   youtube] with the flag false
                 - AdoptClients with added ["Ghost"] (unknown to AdGuard) → nil error and 0 update POSTs
                 - AdoptClients(ada, _, nil) issues zero AdGuard requests
                 - -race, 50 iterations: concurrent Create(grant for Ada) and CreateClient(TV, Ada). After
                   End, TV blocks the granted service in every iteration, and MaxInFlightUpdates()
                   stays 1
                 - AddChildClient failing (injected store wrapper) after the add: the client exists in
                   AdGuard with the baseline list, the error is returned, the child mapping is unchanged
                 - a failed AdGuard Clients() read → no add POST, error returned
       depends:  t-2, t-5

  t-8  Rename a device from the app  [risk t-12 + verification t-9 + mvp t-3]
       files:    web/src/lib/ClientNames.svelte (new), web/src/lib/ClientNames.test.ts (new),
                 web/src/lib/Children.svelte
       covers:   c-6
       desc:     A "Devices" list of every persistent client with an inline rename (input + Save).
                 Success → onRenamed → Children.load, so drafts rebuild from the server.
       contract: - rename "Kid phone"→"Ada phone" sends POST /api/v1/clients/rename {from:"Kid phone",
                   to:"Ada phone"}. After reload Ada's "Ada phone" checkbox is checked and no
                   [data-missing] element renders
                 - an unassigned client (child null) also offers rename (rename_scope). Gating on
                   c.child fails the lookup
                 - a blank or unchanged name disables Save and no fetch is made
                 - a 409 keeps the typed name, shows the server's message, and the list still shows
                   "Kid phone"
       depends:  t-6

Wave 4
  t-9  Engine: rename under mutex with compensation  [risk t-6 + verification t-8]
       files:    internal/grants/rename.go (new), internal/grants/rename_test.go (new)
       covers:   c-6
       desc:     Engine.RenameClient(old, new) runs under e.mu. It pre-checks for a stale store mapping,
                 runs the AdGuard RenameClient, then Store.RenameClient. On a store failure it renames
                 back in AdGuard and returns an error naming both. Same name is a no-op.
       contract: - grant on Ada for youtube over "Kid phone" → rename to "Ada phone" → clock past ends_at
                   → the fake's "Ada phone" blocks youtube, no clients/update body names "Kid phone", and
                   the grant is expired. Skipping the store rename makes revert hit the old name and fail
                 - the same set-up with End instead of expiry → BlockedServices("Ada phone") contains
                   youtube
                 - a timer expiry fired while RenameClient holds the mutex (fake AfterFunc + Hang) runs
                   after it, re-blocks "Ada phone", and never logs "client gone"
                 - an injected Store.RenameClient error → AdGuard ends with "Kid phone" again (a second
                   clients/update is observed) and the error is returned
                 - a stale-mapping conflict ("Ghost" owned by Bob) → *store.ErrClientTaken with zero
                   clients/update POSTs
                 - rename X→X issues zero requests
       depends:  t-7

  t-10 API: unlabelled listing + POST /api/v1/clients  [risk t-7 + verification t-7/t-10 + mvp t-4]
       files:    internal/api/clients.go, internal/api/clients_test.go, internal/api/api.go
       covers:   c-1, c-2, c-3
       desc:     GET /clients adds `unlabelled` (one /control/clients read plus a lease read; a lease
                 failure degrades to IP-only and is logged). POST /clients {name, ids, child_id?}: bounded
                 body, DisallowUnknownFields, name trimmed to 1–256 bytes, ids trimmed, deduped and
                 non-empty. The child is checked (404) and stale ownership is checked (409) before any
                 AdGuard write. The AdGuard and Grants interfaces gain Leases, CreateClient and
                 AdoptClients, and the route is registered.
       contract: - GET lists printer with source "ARP" and omits 192.168.1.50. Two GETs make two
                   /control/clients reads. SetAutoClients adding a row between the GETs → the second GET
                   lists it
                 - /control/dhcp/status forced 500 → GET still 200, printer suggested_ids ==
                   ["192.168.1.9"]
                 - /control/clients forced 500 → 502 adguard_unavailable
                 - POST name "  " → 400 and zero AdGuard requests after login. If the trim is dropped the
                   count fails. ids [] → 400
                 - POST "Kid phone" → 409. POST ids ["192.168.1.50"] → 409 naming "Kid phone". Zero adds in
                   both
                 - Hang on /control/clients/add past ApplyTimeout → 502, not a hang
                 - POST child_id 999 → 404 with zero adds. POST "Ghost" (stale, owned by Bob) with
                   child_id Ada → 409 with zero adds
                 - POST {name:"TV", ids, child_id: Ada} → 201. GET /children/{Ada} shows TV, and GET
                   /clients lists TV with child Ada and not as unlabelled. The fake's TV has
                   use_global_blocked_services=false
                 - POST without child_id → 201 and no child lists it
                 - AdGuard 400 on add (malformed edited id) → 400 bad_request, not 502
                 - no X-Requested-With → 403 (csrf chain)
       depends:  t-4, t-7

Wave 5
  t-11 PUT children applies baseline to added clients  [risk t-8 + verification t-11 + mvp t-5]
       files:    internal/api/children.go, internal/api/children_test.go
       covers:   c-7
       desc:     handleChildUpdate: decode, GetChild (404), and pre-check name and owners via
                 ListChildren (409) before any AdGuard write. added = new ∖ old, siblings = old ∩ new.
                 When added is non-empty, Grants.AdoptClients runs before UpdateChild. An AdGuard failure
                 → 502 naming the client, store untouched. Removals never call AdGuard.
       contract: - Ada has Kid phone and an active grant on youtube. The PUT adds "Old laptop" → 200 and the
                   fake's Old laptop is [tiktok, youtube] with the flag false
                 - the PUT adds Kid tablet (inherits [tiktok, roblox]) → own list [roblox, tiktok,
                   youtube] with the flag false
                 - a client with a pre-existing own "steam" (seeded via MutateClient) keeps steam
                 - a PUT that only drops Kid phone → 0 POST /control/clients/update. A backwards diff fails
                   the count
                 - a rename-only PUT with SetAuth(false) → 200 (AdGuard is not needed)
                 - a PUT adding a client Ben owns → 409 and 0 update POSTs. A PUT renaming Ada to "ben"
                   while adding a client → 409 and 0 update POSTs
                 - SetUpdateStatus("Old laptop", 500) → 502 naming Old laptop, and GET /children/{Ada}
                   still shows the old set
                 - adding "Ghost" (unknown to AdGuard) → 200, stored, 0 update POSTs (the existing
                   struck-through behaviour is kept)
       depends:  t-10

  t-12 API: POST /api/v1/clients/rename  [risk t-9 + verification t-10 + mvp t-4]
       files:    internal/api/rename.go (new), internal/api/rename_test.go (new), internal/api/api.go
       covers:   c-6
       desc:     Body {from, to}: bounded, trimmed, 1–256 bytes. The Grants interface gains RenameClient.
                 Errors map as: ErrClientNotFound → 404, ErrClientExists or *ErrClientTaken → 409, other
                 AdGuard errors → 502.
       contract: - rename "Kid phone"→"Ada phone" → 200. GET /children shows "Ada phone", and GET /grants
                   shows the active grant's clients as ["Ada phone"]
                 - renaming the unassigned "Old laptop" → 200 (rename_scope)
                 - a name containing "/" and "%" round-trips (body, not path)
                 - to "" → 400 with zero AdGuard requests. Unknown from → 404. to "Kid tablet" → 409
                 - AdGuard unreachable → 502, store mapping unchanged
                 - no X-Requested-With → 403
       depends:  t-9, t-10
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 unlabelled listing, read live | t-1, t-4, t-10 |
| c-2 POST create, 400/409/502 | t-1, t-5, t-10 |
| c-3 create + assign in one POST | t-2, t-7, t-10 |
| c-4 Unlabelled devices UI, appears without reload | t-3, t-6 |
| c-5 empty-state sentence | t-6 |
| c-6 rename updates mapping + active grants | t-1, t-2, t-3, t-5, t-8, t-9, t-12 |
| c-7 assign existing client adds baseline, unassign writes nothing | t-5, t-7, t-11 |

All 7 criteria are covered.

## Disagreements

1. **Where create, adopt and rename orchestration lives.** risk and verification put it in
   `grants.Engine` under `e.mu`. mvp puts it in the API handlers (childBaseline in clients.go,
   rename as AdGuard then store). Default: the engine (t-7, t-9). This matters because `revert`
   treats `ErrClientNotFound` as success (grants.go:199). A timer expiry that lands between
   mvp's AdGuard rename and its store rename expires the grant while the device stays unblocked.
   That is the exact failure c-6 forbids, and mvp's End-only contract cannot catch it.

2. **Rename failure after AdGuard succeeds.** risk pre-checks the stale mapping and also
   compensates. verification compensates only. mvp does neither. Default: risk (t-9). Without
   compensation, a store failure leaves AdGuard holding a name the grants do not reference, and
   the same not-found revert path applies.

3. **PUT /children write order.** risk and verification write AdGuard first after a full 409
   pre-check, then the store. mvp writes the store first, then AdGuard, and returns 502 with the
   mapping kept. Default: AdGuard-first (t-11). With store-first, a failed adopt leaves the
   device mapped to the child at its old, looser list, and a retry sees no diff, so the baseline
   never lands. mvp's argument (a 409 writes nothing to AdGuard) is met by the pre-check.

4. **How the create-time child mapping is written.** risk uses a targeted `AddChildClient`
   INSERT inside the engine mutex. verification and mvp call the full-replace `UpdateChild(child,
   clients+name)` from the handler after the AdGuard add. Default: risk (t-2, t-7). A full
   replace computed from a pre-read can clobber a concurrent PUT /children (R3). It is also the
   only way the -race contract in t-7 can hold the mapping steady.

5. **Body of an unassigned new client's `/control/clients/add`.** risk and verification send
   `use_global_settings=true, use_global_blocked_services=true`. mvp sends neither key. Default:
   explicit flags (t-5). If AdGuard zero-values missing booleans, mvp's body creates a device
   with filtering and global blocks off. Two lenses assert this, but no draft cites AdGuard's
   source. Confirm it against a real instance before t-5 is reviewed.

6. **What counts as "covered" (c-1 vs the locked unlabelled_listing).** mvp uses an exact
   IP-string match, the criterion's literal wording. verification uses IP plus a leased MAC that
   is a persistent id. risk uses canonical IP, CIDR containment and the leased MAC. Default: risk
   (t-4). A MAC-only client re-listed by IP invites a duplicate-id add that AdGuard rejects. The
   CIDR and IPv6 lines are the most speculative: removing them costs two contract lines.

7. **Concern about a LOCKED decision (new_client_blocked_set), raised by risk and not
   resolved.** A child's first device, with no siblings and no active grants, gets an empty own
   list with `use_global_blocked_services=false`. It blocks nothing, not even the global list,
   and is less restricted than an unassigned device. risk accepted this as residual risk under
   the lock. mvp and verification did not mention it. Revisit the spec if the global list should
   seed the baseline.

8. **Sibling set for the c-7 baseline.** mvp uses the child's clients before the PUT. risk's
   engine reads the stored child before UpdateChild, which is the same as mvp. verification uses
   old ∩ new, so clients being removed in the same PUT do not contribute. Default: old ∩ new
   (t-7 `AdoptClients(childID, siblings, added)`). The difference only shows when one PUT both
   removes and adds, and then mvp's reading over-blocks with the removed device's list.

9. **Rename route shape.** risk uses `POST /api/v1/clients/rename {from,to}`. mvp and
   verification use `PUT /api/v1/clients/{name}` with `encodeURIComponent`. Default: risk (t-3,
   t-12) despite being the minority. risk's stated reason (R14, a name containing `/`) is weaker
   than claimed: Go 1.26 ServeMux matches `%2F` inside one wildcard segment. The body form still
   avoids any path normalisation by the phase-06 tailnet HTTPS proxy, which no draft tested.
   Switching costs one contract line in t-3 and t-12.

10. **Granularity and structure.** mvp has 5 fat tasks: the fake folded into adguard, api.ts
    folded into the UI, GET + POST + rename in one API task. risk has 12 and verification 11:
    a separate fake task, a separate api.ts task, the unlabelled filter as a pure adguard
    function (risk) versus in the API handler (verification), and rename UI as its own component
    (risk) versus inline (verification). Default: risk's 12, plus mvp's pinned wire contract so
    wave-1/2 UI work is independent. mvp's t-1 and t-4 each fail the "more than one sitting" test.

11. **AdGuard 400 on add.** risk maps it to 400 (AdGuard was reachable and rejected the input).
    verification maps all other AdGuard errors to 502. mvp is silent. Default: 400 (t-10). This
    decides whether a parent who mistypes an edited id sees "fix your input" or "AdGuard is
    down".

12. **Wire field names.** For unlabelled rows, risk uses `suggested_ids` while mvp and
    verification use `ids` (matching clientView). For no child, risk sends `child_id: null` while
    verification omits the key. Default: `suggested_ids`, with the server accepting both null and
    absent `child_id`. Cosmetic, but pin it before t-3 and t-10 run in parallel waves.
