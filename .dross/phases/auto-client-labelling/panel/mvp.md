# auto-client-labelling — mvp lens

```
Phase auto-client-labelling — 5 tasks across 3 waves

Pinned wire contract (shared by t-3 and t-4, so the UI does not wait on the backend):
  GET  /api/v1/clients        -> {clients: clientView[], unlabelled: [{ip, hostname, source, ids: string[]}]}
                                 unlabelled sorted by hostname; ids = [ip] or [ip, mac] when a DHCP lease maps the ip
  POST /api/v1/clients        {name, ids, child_id: number|null} -> 201 clientView
                                 400 empty name / empty ids, 404 unknown child_id, 409 name exists in AdGuard, 502 AdGuard down
  PUT  /api/v1/clients/{name} {name} -> 200 clientView
                                 400 empty name, 404 unknown client, 409 new name taken, 502 AdGuard down

Wave 1
  t-1  Add AdGuard auto-client, lease, add, rename calls
       files:    internal/adguard/clients.go, internal/adguard/clients_test.go,
                 internal/adguard/adguardtest/server.go, internal/adguard/adguardtest/testdata/dhcp_status.json (new)
       covers:   c-1, c-2, c-6, c-7
       desc:     ClientsResult gains Auto []AutoClient{IP, Hostname, Source} decoded from the same /control/clients
                 doc; DHCPLeases() reads /control/dhcp/status (leases + static_leases -> ip->mac); AddClient(name, ids,
                 blocked *[]string) POSTs /control/clients/add (nil blocked = AdGuard defaults, else own list +
                 use_global_blocked_services=false), returning ErrClientExists when the name is already present under
                 the rmw mutex; RenameClient(old,new) and AdoptBlockedServices(name, ids) both go through updateClient —
                 Adopt writes union(own list, global list if it inherited, ids) and clears the flag. Fake serves
                 clients/add (dup name -> 400), dhcp/status, and lets clients/update change the name.
       contract: - auto_clients fixture entry "printer"/192.168.1.9/ARP comes back in Clients().Auto; dropping the
                   decode fails TestClientsAutoClients
                 - AddClient with an existing name returns ErrClientExists and the fake records zero POSTs to
                   /control/clients/add
                 - AddClient(blocked=nil) sends no blocked_services/use_global_blocked_services keys; with a list it
                   sends that list and use_global_blocked_services=false (asserted on the fake's recorded body)
                 - RenameClient keeps every other raw field byte-identical (compare LastUpdate data minus "name") and
                   the next Clients() read lists the new name, not the old
                 - AdoptBlockedServices on a client blocking [a] with ids [b] writes [a,b] + flag false; on a
                   use_global=true client with global [g] it writes [b,g] — a removed own/global id fails the test
                 - dhcp/status answering 501 makes DHCPLeases return an error (caller decides), not a panic/empty-ok
       depends:  —
       status:   pending

  t-2  Rename client across child mapping and active grants
       files:    internal/store/children.go, internal/store/children_test.go
       covers:   c-6
       desc:     Store.RenameClient(ctx, old, new) in one transaction: child_clients.client_name and grant_clients
                 rows of grants with status='active' move from old to new; ended/expired grants are untouched.
       contract: - after RenameClient("tablet","kid-tablet") GetChild lists "kid-tablet" and not "tablet"
                 - an active grant's Clients becomes ["kid-tablet"]; an ended grant holding "tablet" still holds
                   "tablet" (status filter removed -> test fails)
                 - renaming to a name another child already owns returns *ErrClientTaken and changes no row
                   (transaction rollback asserted by re-reading both children)
       depends:  —
       status:   pending

  t-3  Unlabelled devices list and client rename UI
       files:    web/src/lib/Children.svelte, web/src/lib/Children.test.ts, web/src/lib/api.ts
       covers:   c-4, c-5, c-6
       desc:     api.ts: listClients returns {clients, unlabelled}; add createClient(name, ids, child_id) and
                 renameClient(old, new). Children.svelte: "Unlabelled devices" section, one row per device with name
                 prefilled from hostname, editable ids, optional child select, Save -> createClient then load();
                 empty list shows the "device must connect to the network and appear in AdGuard" note; each known
                 client gets a Rename control -> renameClient then load().
       contract: - mocked GET with unlabelled [{hostname:"ipad", ids:["192.168.1.20","aa:bb:..."]}] renders an input
                   prefilled "ipad" and both ids; saving POSTs {name:"ipad", ids:[...], child_id:null}
                 - picking child 1 then Save POSTs child_id:1, and after the refetch "ipad" is a checked checkbox
                   under child 1 with no window.location reload
                 - unlabelled: [] renders the "must connect to the network and appear in AdGuard" text; the text is
                   absent when one device is listed
                 - a 409 from POST shows the server message and keeps the typed name in the input
                 - Rename "tablet" -> "kid-tablet" PUTs /api/v1/clients/tablet {name:"kid-tablet"} and the child's
                   checkbox list shows the new name after reload
       depends:  —
       status:   pending

Wave 2 (depends t-1, t-2)
  t-4  Clients API: unlabelled list, create, rename
       files:    internal/api/clients.go, internal/api/api.go, internal/api/clients_test.go
       covers:   c-1, c-2, c-3, c-6
       desc:     AdGuard interface gains AddClient/RenameClient/AdoptBlockedServices/DHCPLeases, ChildStore gains
                 RenameClient. GET adds `unlabelled` (auto clients whose IP is no persistent id, MAC from leases,
                 lease read failure tolerated). POST /api/v1/clients and PUT /api/v1/clients/{name} per the pinned
                 contract; POST with child_id checks the child first, computes childBaseline(childID) = union of the
                 child's clients' blocked lists + services of the child's active grants, creates with that list, then
                 appends the client to the child via UpdateChild. Rename writes AdGuard first, then store.RenameClient.
       contract: - fixture auto client 192.168.1.9 is listed in `unlabelled`; once a persistent client carries
                   "192.168.1.9" in ids it is gone (exact-ip filter removed -> test fails); two calls hit
                   /control/clients twice (no memoising)
                 - unlabelled is hostname-sorted and includes a non-DHCP source; a leased ip carries [ip, mac]
                 - POST name "  " -> 400 and zero /control/clients/add requests; existing name -> 409; fake SetStatus
                   502 on /control/clients/add -> 502
                 - POST with child_id while that child has an active grant on "youtube" and a sibling blocking
                   "tiktok": the fake's new client has blocked_services ⊇ [tiktok, youtube] and use_global=false, and
                   GET /children/{id} lists the new client
                 - POST with unknown child_id -> 404 and zero /control/clients/add requests
                 - PUT rename while a grant is active, then End the grant: the fake shows the grant's services
                   re-blocked on the renamed client (revert path follows the rename)
       depends:  t-1, t-2
       status:   pending

Wave 3 (depends t-4)
  t-5  Apply child baseline on client assignment
       files:    internal/api/children.go, internal/api/children_test.go
       covers:   c-7
       desc:     handleChildUpdate reads the child's previous clients, runs UpdateChild, then for each newly added
                 client calls AdoptBlockedServices(childBaseline computed from the previous siblings + active grants);
                 removed clients get no AdGuard write; an AdGuard failure answers 502 naming the client (mapping kept).
       contract: - assigning "laptop" (blocking [steam], use_global=false) to a child whose sibling blocks [tiktok]
                   and whose active grant covers [youtube] leaves laptop with [steam, tiktok, youtube] — steam
                   dropping fails the test
                 - assigning a use_global=true client sets the flag false on the fake
                 - removing a client from the child: CountRequests(POST, /control/clients/update) stays 0
                 - a PUT that 409s on ErrClientTaken issues zero /control/clients/update requests
                 - fake SetUpdateStatus 500 for the added client -> 502 whose message names that client
       depends:  t-4
       status:   pending
```

## Coverage

| criterion | tasks |
|-----------|-------|
| c-1 unlabelled devices in GET /clients, live | t-1, t-4 |
| c-2 POST /clients create, 400/409/502 | t-1, t-4 |
| c-3 create + assign in one POST | t-4 |
| c-4 settings page list, name, appears without reload | t-3 |
| c-5 empty-list explanation | t-3 |
| c-6 rename updates mapping + active grants | t-1, t-2, t-4, t-3 (UI entry point) |
| c-7 assign existing client applies baseline, no removals, unassign writes nothing | t-1, t-5 |

7/7 covered.

## Judgment calls

- UI (t-3) in wave 1 against a pinned wire contract; rejected waiting on t-4 — vitest mocks fetch, so the only shared output is the JSON shape, and pinning it here is cheaper than a wave.
- All four new AdGuard calls plus fake support in one task (t-1); rejected one task per call — they share updateClient/rmw and the fake, splitting just multiplies tiny tasks over the same two files.
- Rename UI folded into t-3 instead of an API-only rename; c-6 says "from the app" and rename_scope makes the app own the path, so an endpoint without a control would leave c-6 unreachable by a parent.
- childBaseline lives in t-4 (POST path) and t-5 reuses it, making t-5 wave 3; rejected a shared baseline package/file — one function, two callers, no new abstraction.
- "Siblings" in c-7 means the child's clients *before* the PUT; rejected using the new set, since a newly added client's own list would then count as baseline.
- Store first, AdGuard second on PUT /children (t-5); rejected AdGuard-first so a 409 conflict writes nothing to AdGuard. A later AdGuard failure answers 502 with the mapping kept (same semantics as migration).
- AdoptBlockedServices also folds in the global list when the client inherited it; rejected own-list-only union because clearing use_global would otherwise drop blocks the device had in effect, breaking "nothing it already blocks is removed".
- Duplicate-name 409 is detected app-side by re-reading clients under the rmw mutex (ErrClientExists); rejected parsing AdGuard's 400 body text, which is unversioned prose.
- POST with child_id: AdGuard add first, then UpdateChild; a store failure after the add leaves an unassigned AdGuard client (500). Rejected a compensating delete; it would mean another AdGuard write path for a rare error.
- Unlabelled filter is exact IP-string match against persistent ids, as the criterion words it; rejected CIDR/MAC containment as speculative.
- DHCP lease read failure (AdGuard without DHCP answers 501) degrades to ids=[ip]; rejected failing the whole GET, since the decision only needs the MAC "when AdGuard leased that IP".
- No migration: rename is UPDATE on existing child_clients/grant_clients columns; rejected a client-id indirection table.
