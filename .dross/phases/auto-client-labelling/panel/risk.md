# auto-client-labelling — risk-lens draft

Lens: failure modes drive the graph. Each task owns one cluster of risks and the
test that proves it; no risk is tested in two places.

Risk register (what can break, and who owns it):

| # | Risk | Owner |
|---|------|-------|
| R1 | Fake AdGuard accepts what real AdGuard rejects (dup name/id on add, rename onto an existing name), so every downstream test is green against a lie | t-1 |
| R2 | Rename leaves `child_clients` / `grant_clients` half-updated, or collides with a stale name already stored (PK on `child_clients.client_name`, PK `(grant_id, client_name)`) | t-2 |
| R3 | Assign-on-create replaces the child's whole set and clobbers a concurrent PUT | t-2 |
| R4 | "Covered" computed wrong: IP textual mismatch (IPv6 forms), CIDR ids, a device already labelled by MAC re-listed as unlabelled; unstable sort with blank hostnames | t-3 |
| R5 | `/control/clients/add` body omits `use_global_settings: true`, so the new device silently gets filtering/parental/safebrowsing OFF | t-4 |
| R6 | Adopting a `use_global_blocked_services=true` client drops the global services it was effectively blocking ("nothing it already blocks is removed") | t-4 |
| R7 | Rename write drops unknown fields on the client object (raw passthrough) | t-4 |
| R8 | Baseline computed outside the engine mutex races a grant Create/expiry, so a device added mid-grant inherits the temporary unblock and stays open after expiry | t-5 |
| R9 | Rename window: timer/reconciler reverts the *old* name between AdGuard rename and store update; `revert` treats `ErrClientNotFound` as success, grant expires, device stays unblocked | t-6 |
| R10 | Store update fails after the AdGuard rename succeeded; no compensation | t-6 |
| R11 | DHCP leases endpoint fails/unsupported and takes the whole GET /clients down to 502 | t-7 |
| R12 | Validation runs after an AdGuard write (empty name, unknown child) and leaves a half-created device | t-7 |
| R13 | AdGuard written for a PUT that the store then rejects with 409, or a PUT with no added clients needs AdGuard at all | t-8 |
| R14 | Client names with `/`, `%`, spaces break a path-param rename route | t-9 |
| R15 | UI shows "no devices yet" when the read actually failed; double-tap creates twice; failure wipes the typed draft | t-11 |
| R16 | After rename the child's draft still holds the old name and shows it struck-through as "not in AdGuard" | t-12 |

```
Phase auto-client-labelling — 12 tasks across 5 waves

Wave 1
  t-1  Extend fake AdGuard: add, DHCP, rename
       files:    internal/adguard/adguardtest/server.go,
                 internal/adguard/adguardtest/server_test.go,
                 internal/adguard/adguardtest/testdata/clients.json,
                 internal/adguard/adguardtest/testdata/dhcp_status.json (new)
       desc:     Serve POST /control/clients/add (400 on duplicate name or an id held by another
                 client, as AdGuard does), GET /control/dhcp/status from a fixture (dynamic + static
                 leases), and reject a clients/update that renames onto an existing name. Extend the
                 auto_clients fixture with: an IP equal to Kid phone's id, a DHCP-leased IP whose MAC is
                 Kid tablet's id, a blank-hostname entry, a leased unlabelled entry. Add AddedClients()
                 observer.
       covers:   c-1, c-2
       contract: - fake POST /control/clients/add with name "Kid phone" answers 400 and the next GET
                   /control/clients still lists exactly one "Kid phone"
                 - fake add with ids ["192.168.1.50"] (Kid phone's IP) answers 400
                 - fake clients/update {name:"Kid tablet", data:{name:"Kid phone"}} answers 400 and both
                   clients survive unchanged
                 - after a successful add, GET /control/clients lists it and the auto_clients array is
                   still present (clientsJSON re-encode keeps the top-level doc)
                 - SetStatus("/control/dhcp/status", 501) makes the lease read fail without affecting
                   GET /control/clients
       depends:  —

  t-2  Store: atomic client rename + targeted assign
       files:    internal/store/clients.go (new), internal/store/clients_test.go (new)
       desc:     Store.RenameClient(old, new) in one tx: child_clients row renamed, active grants'
                 grant_clients rows renamed (deduped when that grant already holds new); returns
                 *ErrClientTaken when another child row already holds `new` (a stale mapping).
                 Store.AddChildClient(childID, name): single INSERT with the existing conflict check.
       covers:   c-3, c-6
       contract: - rename "Kid phone"→"Ada phone" with an active grant on it: ListChildren and
                   ListActiveGrants both show "Ada phone" and neither shows "Kid phone"
                 - an expired grant's grant_clients keeps "Kid phone" (history untouched)
                 - an active grant holding both "Old tab" (stale) and "Kid phone": renaming Kid phone to
                   "Old tab" leaves one "Old tab" row, no PK error
                 - child B holds stale "Old tab": rename onto "Old tab" returns *ErrClientTaken naming B
                   and BOTH tables are byte-identical to before (rollback proven)
                 - AddChildClient(ada, "New tv") while Ada holds ["Kid phone"] leaves
                   ["Kid phone","New tv"] — never a replace
                 - AddChildClient for a client owned by Ben → *ErrClientTaken; unknown child →
                   ErrChildNotFound
       depends:  —

  t-10 SPA api client: devices, create, rename
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts
       desc:     UnlabelledDevice type {ip, hostname, source, suggested_ids}; listDevices() returning
                 {clients, unlabelled}; createClient(name, ids, childId|null) → POST /api/v1/clients;
                 renameClient(from, to) → POST /api/v1/clients/rename. listClients keeps its shape.
       covers:   c-4, c-6
       contract: - createClient("TV", ["192.168.1.9"], null) sends exactly
                   {"name":"TV","ids":["192.168.1.9"],"child_id":null} with the CSRF header
                 - a response without an `unlabelled` key yields unlabelled: [] (no TypeError)
                 - a 409 from POST /api/v1/clients surfaces the server's message via messageFor, not
                   the generic conflict text
                 - renameClient sends {from, to} in the body, never in the URL (a name containing "/"
                   produces URL exactly /api/v1/clients/rename)
       depends:  —

Wave 2
  t-3  Read auto_clients, leases, compute unlabelled set
       files:    internal/adguard/clients.go, internal/adguard/clients_test.go,
                 internal/adguard/devices.go (new), internal/adguard/devices_test.go (new)
       desc:     Clients() also returns AutoClients from the SAME /control/clients read (readClients
                 keeps its signature; a new readClientsDoc decodes both). Client.Leases(ctx) reads
                 /control/dhcp/status (dynamic + static). Pure Unlabelled(persistent, auto, leases)
                 returns devices not covered, sorted by hostname (blank last, IP tie-break), each with
                 suggested_ids = IP + leased MAC.
       covers:   c-1
       contract: - auto_client 192.168.1.50 (Kid phone's id) is excluded
                 - an id written "2001:db8::0:1" covers auto IP "2001:db8::1" (netip canonical compare)
                 - a persistent CIDR id "192.168.1.0/28" covers 192.168.1.9
                 - an auto IP leased to aa:bb:cc:dd:ee:02 (Kid tablet's MAC id) is excluded
                 - every source is listed (ARP, rDNS, DHCP, WHOIS fixtures all present) — a source
                   filter breaks this
                 - order is ["alpha","printer","zed",""(blank)] across two runs with shuffled input
                 - leased device's suggested_ids == [ip, mac]; unleased == [ip]
                 - Clients() issues exactly one GET /control/clients (CountRequests == 1)
       depends:  t-1

  t-4  AdGuard writes: add, rename, adopt baseline
       files:    internal/adguard/clients_write.go (new), internal/adguard/clients_write_test.go (new)
       desc:     Under the rmw mutex with a fresh read: AddClient(NewClient{Name, IDs, Blocked *[]string})
                 pre-checks name (ErrClientExists) and ids (*ErrIDTaken naming owner), POSTs
                 /control/clients/add with AdGuard-UI defaults; RenameClient(old,new) via updateClient
                 (ErrClientNotFound / ErrClientExists); AdoptBlockedServices(name, ids): blocked =
                 effective set (own, or global when flag set) ∪ ids, flag false, skip when unchanged.
       covers:   c-2, c-6, c-7
       contract: - AddClient with Blocked=nil sends use_global_settings=true AND
                   use_global_blocked_services=true; with Blocked set sends use_global_settings=true,
                   use_global_blocked_services=false, blocked_services sorted/never null
                 - duplicate name → ErrClientExists with ZERO POST /control/clients/add
                 - an id owned by "Kid phone" → *ErrIDTaken{Owner:"Kid phone"}, zero POSTs
                 - RenameClient keeps future_field:42 and every other raw field in the update body
                 - AdoptBlockedServices on Kid tablet (use_global=true, global=[facebook]) with
                   ["youtube"] writes ["facebook","youtube"] and use_global=false
                 - a second identical Adopt issues no POST
                 - concurrent AddClient + AddBlockedServices: MaxInFlightUpdates() == 1
       depends:  t-1

  t-11 Unlabelled devices list on children page
       files:    web/src/lib/UnlabelledDevices.svelte (new), web/src/lib/UnlabelledDevices.test.ts (new),
                 web/src/lib/Children.svelte, web/src/lib/Children.test.ts
       desc:     Children.load uses listDevices; UnlabelledDevices renders each device with name
                 prefilled from hostname, editable ids, optional child picker (default none), submit
                 → createClient then onCreated → Children.load. Empty list shows the
                 connect-to-network sentence.
       covers:   c-4, c-5
       contract: - device "printer" renders a name input with value "printer"; blank hostname renders
                   empty and submit is blocked (required)
                 - editing ids to drop the MAC POSTs ids ["192.168.1.9"] only
                 - after a 201, GET /api/v1/clients is called again and a "TV" checkbox appears under
                   every child; the device leaves the unlabelled list; window.location.reload not called
                 - picking child Ada POSTs child_id 1 and Ada's "TV" checkbox renders checked
                 - two rapid submits issue one POST (pending lock)
                 - a 409 shows the server's message and the typed name/ids are still in the inputs
                 - unlabelled [] shows "…must connect to the network and appear in AdGuard Home before
                   it can be named"; a 502 on GET /clients shows the alert and NOT that sentence
       depends:  t-10

Wave 3 (depends t-2, t-4 / t-11)
  t-5  Engine: baseline under mutex, create + adopt
       files:    internal/grants/clients.go (new), internal/grants/clients_test.go (new),
                 internal/grants/grants.go
       desc:     Extend engine Store/AdGuard interfaces (GetChild, AddChildClient, RenameClient;
                 AddClient, AdoptBlockedServices, RenameClient). Under e.mu with one applyContext:
                 baseline(child) = ∪ siblings' effective blocked sets (present in AdGuard, flag-aware)
                 ∪ services of that child's active grants. Engine.CreateClient(spec, childID*):
                 AddClient with baseline in the add body (no unblocked window), then AddChildClient.
                 Engine.AdoptClients(childID, names): AdoptBlockedServices(baseline) per name.
       covers:   c-3, c-7
       contract: - Ada has active grant [youtube] (siblings unblocked): CreateClient(TV, child=Ada)
                   writes TV with youtube blocked; advancing the clock past ends_at leaves TV blocked
                   and the grant expired with stored clients unchanged
                 - Ben's active grant [tiktok] does NOT enter Ada's baseline
                 - a sibling with use_global=true contributes the global list
                 - -race test: 50 iterations of concurrent Create(grant for Ada) and CreateClient(Ada)
                   always end with TV blocking the granted service
                 - AddChildClient failing (injected store wrapper) after the add: client exists in
                   AdGuard with the baseline list, error returned, child mapping unchanged
                 - AdGuard Clients() read fails → no POST at all, error returned
                 - AdoptClients(ada, nil) issues zero AdGuard requests
       depends:  t-2, t-4

  t-12 Rename a device from the app
       files:    web/src/lib/ClientNames.svelte (new), web/src/lib/ClientNames.test.ts (new),
                 web/src/lib/Children.svelte
       desc:     A "Devices" list of every persistent client with an inline rename (name input +
                 Save); success calls onRenamed → Children.load so drafts rebuild from the server.
       covers:   c-6
       contract: - rename "Kid phone"→"Ada phone": Ada's checkbox "Ada phone" is checked and no
                   [data-missing] element renders
                 - a client assigned to no child still offers rename (rename_scope: any listed client)
                 - blank or unchanged name sends no request
                 - a 409 keeps the typed name and shows the server's message
       depends:  t-11

Wave 4 (depends t-5)
  t-6  Engine: rename under mutex with compensation
       files:    internal/grants/rename.go (new), internal/grants/rename_test.go (new)
       desc:     Engine.RenameClient(old,new) under e.mu: pre-check stale mapping in store, AdGuard
                 RenameClient, Store.RenameClient; on store failure rename back in AdGuard and return
                 an error naming both; same-name is a no-op.
       covers:   c-6
       contract: - active grant [youtube] on Kid phone; rename to "Ada phone"; End → AdGuard
                   BlockedServices("Ada phone") contains youtube (the grant really reverts)
                 - timer expiry fired while RenameClient holds the mutex (fake AfterFunc + blocked
                   AdGuard Hang) runs after it and re-blocks "Ada phone", never logs "client gone"
                 - injected Store.RenameClient error → AdGuard ends with "Kid phone" again (second
                   clients/update observed) and the error is returned
                 - stale-mapping conflict → *store.ErrClientTaken with ZERO clients/update POSTs
                 - rename X→X issues zero requests
       depends:  t-5

  t-7  API: unlabelled listing + POST /api/v1/clients
       files:    internal/api/clients.go, internal/api/clients_test.go, internal/api/api.go
       desc:     GET /clients adds `unlabelled` (one /control/clients read + lease read; lease failure
                 degrades to IP-only ids, logged). POST /clients {name, ids, child_id?}: bounded body,
                 DisallowUnknownFields, trims; 400 / 404 child validated before any AdGuard write;
                 Grants interface gains CreateClient + AdoptClients; route registered.
       covers:   c-1, c-2, c-3
       contract: - GET lists printer with source "ARP", omits 192.168.1.50; two GETs = two
                   /control/clients reads (nothing memoised)
                 - /control/dhcp/status forced 500 → GET still 200, printer suggested_ids == [ip]
                 - /control/clients forced 500 → 502 adguard_unavailable
                 - POST name "  " → 400 and zero AdGuard requests; ids [] → 400
                 - POST "Kid phone" → 409 conflict; Hang on /control/clients/add past timeout → 502
                 - POST child_id 999 → 404 with zero POST /control/clients/add
                 - POST {name:"TV", ids, child_id: Ada} → 201, GET /children shows TV under Ada and
                   GET /clients lists TV with child Ada and no longer lists it as unlabelled
                 - AdGuard 400 on add (malformed id) → 400 bad_request, not 502
       depends:  t-3, t-5

Wave 5 (depends t-6, t-7)
  t-8  PUT children applies baseline to added clients
       files:    internal/api/children.go, internal/api/children_test.go
       desc:     handleChildUpdate: GetChild, added = new ∖ old; conflict pre-check via ListChildren;
                 if added non-empty Grants.AdoptClients BEFORE UpdateChild (fail toward over-block);
                 AdGuard failure → 502 naming the client, store untouched.
       covers:   c-7
       contract: - add Kid tablet to Ada (sibling Kid phone blocks [tiktok,youtube]) → tablet's list ⊇
                   both and use_global=false; services it already blocked are still there
                 - unassign Kid phone → zero POST /control/clients/update
                 - rename-only PUT (same clients) with AdGuard SetAuth(false) → 200 (AdGuard not needed)
                 - add a client owned by Ben → 409 and zero clients/update POSTs
                 - SetUpdateStatus("Kid tablet", 500) → 502 naming Kid tablet and GET /children still
                   shows the old client set
       depends:  t-7

  t-9  API: POST /api/v1/clients/rename
       files:    internal/api/rename.go (new), internal/api/rename_test.go (new), internal/api/api.go
       desc:     Body {from, to} (bounded, trimmed, 1..256 bytes); Grants interface gains RenameClient;
                 maps ErrClientNotFound 404, ErrClientExists / *ErrClientTaken 409, AdGuard 502.
       covers:   c-6
       contract: - rename "Kid phone"→"Ada phone" → 200; GET /children shows "Ada phone"; GET /grants
                   shows the active grant's clients as ["Ada phone"]
                 - a name containing "/" and "%" round-trips (body, not path)
                 - to "" → 400 with zero AdGuard requests; from unknown → 404; to "Kid tablet" → 409
                 - AdGuard unreachable → 502 and store unchanged
       depends:  t-6, t-7
```

## Coverage

| Criterion | Tasks |
|-----------|-------|
| c-1 unlabelled listing, live | t-1, t-3, t-7 |
| c-2 POST create, 400/409/502 | t-1, t-4, t-7 |
| c-3 create + assign in one action | t-2, t-5, t-7 |
| c-4 unlabelled list UI, appears without reload | t-10, t-11 |
| c-5 empty-state sentence | t-11 |
| c-6 rename updates mapping + active grants | t-2, t-4, t-6, t-9, t-10, t-12 |
| c-7 assign existing client adds baseline, unassign writes nothing | t-4, t-5, t-8 |

7/7 covered.

## Judgment calls

- Baseline and rename run under the grants engine mutex (new engine methods), not in the API — rejected API-level orchestration because R8/R9 are races against the timer and reconciler, which only the engine mutex excludes.
- New-client baseline goes in the `/control/clients/add` body itself; rejected add-then-adopt because it opens a window where the device is unblocked mid-grant.
- PUT children writes AdGuard before the store (with a conflict pre-check); rejected store-first because a store-committed assignment with a failed AdGuard write leaves the device open, while the reverse failure only over-blocks.
- Rename compensates by renaming back in AdGuard on store failure; rejected "store first then AdGuard" because a stored name AdGuard lacks makes `revert` treat the client as gone and expire the grant.
- Rename onto a name held by another child's stale mapping is 409; rejected silent merge because it would hand a device to a different child.
- Rename endpoint is `POST /api/v1/clients/rename {from,to}`; rejected `PUT /clients/{name}` because client names may contain `/` and `%` (R14).
- Only active grants' `grant_clients` are renamed; rejected rewriting history since c-6 names active grants and history rows can collide on the PK.
- "Covered" = canonical IP match, CIDR containment, or leased MAC is a persistent id; rejected literal string IP match because the locked unlabelled_listing says "not covered", and a MAC-labelled device re-listed would invite a duplicate-id add.
- "Siblings' blocked services" and "already blocks" use the effective set (global list when the flag is set); rejected own-list-only because flipping the flag false would silently drop globally blocked services.
- A failed DHCP lease read degrades to IP-only suggested ids; rejected 502 because DHCP is often disabled or unsupported and c-1 shouldn't depend on it.
- AdGuard 400 on add maps to our 400; rejected 502 because AdGuard was reachable and rejected the input (e.g. a malformed edited id). Duplicate name/id are pre-checked under the rmw lock and answered 409.
- Accepted per the locked new_client_blocked_set: a child's first device (no siblings, no grants) gets an empty own list with use_global=false. Flagged here as a residual risk, not overridden.
- Rename reloads the whole page state, discarding unsaved checkbox drafts; rejected patching drafts in place because a reload from the server is the existing pattern and the only one that removes the stale-name risk (R16).
- t-5 owns all engine interface extensions and t-7 owns the api.go additions, so t-6/t-8/t-9 sit a wave later; rejected parallel edits to the same interface blocks, which would conflict.
