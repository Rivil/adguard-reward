# shared-devices — MVP lens draft

Lens: smallest task set that satisfies every criterion. Each task traces to a criterion; nothing speculative.

Where the one-client-one-child rule lives today (all three must go for c-1):
1. Schema: `child_clients.client_name TEXT PRIMARY KEY` (internal/store/migrations/0002_children.sql)
2. Store: `checkConflicts` client loop -> `*ErrClientTaken` (internal/store/children.go), plus single-row `clientOwner` (internal/store/clients.go)
3. API pre-check: `updateConflict` client loop (internal/api/children.go) -> the 409 `client X is already assigned to Y`

Pinned wire contract (t-4/t-5 produce it, t-8/t-9 consume it; the UI never waits on the backend):

```
POST /api/v1/grants 201  {id, ends_at, applied, failed[], shared: [{client, owners: string[]}]}
GET  /api/v1/grants      grants[i] gains shared: [{client, owners: string[]}]
    shared = the grant's stored clients that some OTHER child also owns; owners = those children's names,
    sorted; never null ([] when nothing is shared). The granting child is never in owners.
GET  /api/v1/children/{id}/blocked
    services[i] gains granted_by: [{grant_id, child: {id, name}, clients: string[]}]
    = active grants of OTHER children that hold this service and whose stored clients intersect this
    child's mapped clients; clients = that intersection. Never null.
PUT  /api/v1/children/{id}   unchanged shape; a client another child owns is no longer a 409.
```

```
Phase shared-devices — 9 tasks across 2 waves

Wave 1
  t-1  Allow multi-owner clients in schema + store
       files:    internal/store/migrations/0005_shared_clients.sql (new), internal/store/children.go,
                 internal/store/clients.go, internal/store/children_test.go, internal/store/clients_test.go
       desc:     Rebuild child_clients with PRIMARY KEY (child_id, client_name) + index on client_name (rows copied);
                 drop the client loop from checkConflicts (name check stays). RenameClient renames every owner's row
                 (an owner already holding newName drops its old row; *ErrClientTaken only when a child that does
                 not own oldName holds newName); AddChildClient checks "this child holds it" before any other owner.
       covers:   c-1, c-7
       contract: if the PK still keys on client_name alone, TestUpdateChild_SharedClient fails: UpdateChild(ben, ["Kid phone"])
                   while Ada holds "Kid phone" returns nil and ListChildren shows "Kid phone" under both Ada and Ben
                 if the migration drops rows, TestMigrate0005_KeepsMappings fails: a DB at 0004 with Ada=[Kid phone, TV]
                   still lists exactly those after Open, and 0005_shared_clients.sql is applied after 0004_buttons.sql
                 if removal deletes by client_name, TestUpdateChild_RemoveSharedKeepsOther fails: Ada and Ben share "TV";
                   UpdateChild(ada, []) leaves Ben holding "TV"
                 if the cascade is lost, TestDeleteChild_SharedKeepsOther fails: DeleteChild(ada) leaves Ben holding "TV"
                 if rename updates only one owner, TestRenameClient_Shared fails: Ada and Ben share "TV";
                   RenameClient("TV","Lounge TV") leaves both holding "Lounge TV" and neither "TV"
                 if the self-stale branch deletes by name, TestRenameClient_SharedSelfStale fails: Ada+Ben share "TV",
                   Ada also holds stale "Den"; RenameClient("TV","Den") leaves Ada=[Den], Ben=[Den]
                 if the stale-foreign rule is dropped, TestRenameClient_Taken (existing) still fails on a non-owner:
                   Cat holds "Den", rename Ada's "TV"->"Den" returns *ErrClientTaken naming Cat, tables unchanged
                 if AddChildClient reads the first owner row, TestAddChildClient_SharedSelf fails: Ada+Ben share "TV";
                   AddChildClient(ben, "TV") returns nil (no-op), not *ErrClientTaken

  t-6  Shared-device helper + grant-form hint
       files:    web/src/lib/grants.ts, web/src/lib/grants.test.ts, web/src/lib/GrantForm.svelte,
                 web/src/lib/GrantForm.test.ts
       desc:     Pure sharedDevices(childId, children) -> [{client, owners: string[]}] (other owners' names, sorted,
                 client order). GrantForm renders a passive note under the child picker for the selected child
                 ("TV is shared with Ben") — no dialog, no change to value/valid. Covers Home ad-hoc and the button editor.
       covers:   c-8
       contract: if the granting child is not excluded, grants.test sharedDevices fails: Ada+Ben share TV ->
                   sharedDevices(ada) == [{client:"TV", owners:["Ben"]}], and a child with no shared client gets []
                 if the hint is missing or names the wrong child, GrantForm.test shared hint fails: selecting Ada shows
                   text naming "TV" and "Ben"; selecting Cat (no shared device) shows no hint element
                 if the hint gates the form, GrantForm.test hint passive fails: with Ada selected and a service ticked,
                   valid is still true and no role=dialog / confirm control exists

  t-7  Children page: enable + label shared clients
       files:    web/src/lib/Children.svelte, web/src/lib/Children.test.ts
       desc:     Derive owners from the loaded children list instead of ClientView.child; a client another child owns
                 stays enabled and reads "shared with <names>"; ticking + Save PUTs it like any other client.
       covers:   c-2
       contract: if the checkbox is still disabled for a foreign owner, Children.test shared client fails: Ben's form
                   shows "Kid phone" (owned by Ada) enabled and its label contains "shared with Ada"
                 if ticking does not share it, Children.test share save fails: ticking "Kid phone" on Ben and Save sends
                   PUT /api/v1/children/2 with clients containing "Kid phone"
                 if names are taken from ClientView.child (single owner), Children.test multi-owner label fails: a client
                   owned by Ada and Ben shows "shared with Ada, Ben" on Cat's form

  t-8  SPA types + active-grant row shared note
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/lib/ActiveGrants.svelte,
                 web/src/lib/ActiveGrants.panel.test.ts
       desc:     Per the pinned contract: Grant.shared, GrantCreated.shared, ServiceState.granted_by (listGrants and
                 childBlocked default a missing field to []). Each active-grant row renders "also on TV (shared with Ben)".
       covers:   c-5
       contract: if a missing field is not defaulted, api.test listGrants fails: a grant without `shared` yields shared: []
                   and childBlocked without `granted_by` yields granted_by: [] on every service, no TypeError
                 if the row ignores shared, ActiveGrants.panel.test shared row fails: a grant with
                   shared [{client:"TV", owners:["Ben"]}] renders a row whose text contains "TV" and "Ben";
                   a grant with shared [] renders no shared note

Wave 2 (t-2..t-5 depend t-1; t-9 depends t-6, t-8)
  t-2  Ref-counted revert on shared clients
       files:    internal/grants/grants.go, internal/grants/grants_test.go, internal/grants/reconcile_test.go
       desc:     revert reads the active grants and, per client, re-adds only g.Services minus services another active
                 grant still holds on that same client; a client left with nothing to re-add gets no write. Used by
                 expiry, End and the reconcile overdue path (all go through revert).
       covers:   c-4
       contract: if Create skips shared clients, TestEngine_CreateUnblocksShared fails: Ben owns [Ben phone, TV] (TV shared
                   with Ada); a netflix grant for Ben removes netflix from both Ben phone and TV
                 if End ignores the other grant, TestEngine_EndSharedKeepsOther fails: Ada and Ben each hold an active
                   netflix grant covering TV; ending Ben's leaves netflix unblocked on TV and re-blocks it on Ben phone
                 if the count is not per client, TestEngine_ExpirySharedLastReblocks fails: after both grants expire in turn,
                   TV blocks netflix again (the second revert re-blocks)
                 if the timer path bypasses the check, TestEngine_TimerSharedKeepsOther fails: firing Ben's timer while
                   Ada's netflix grant is active leaves TV unblocked and marks Ben's grant expired
                 if reconcile revert bypasses it, TestReconcile_OverdueSharedKeepsOther fails: an overdue Ben grant reverted
                   by Reconcile while Ada's live grant covers TV leaves TV unblocked, Reverted == 1
                 if a different service is wrongly spared, TestEngine_EndSharedOtherService fails: Ada's grant is youtube,
                   Ben's is netflix on TV; ending Ben's re-blocks netflix on TV

  t-3  Shared-aware adopt baseline + rename check
       files:    internal/grants/clients.go, internal/grants/clients_test.go, internal/grants/rename.go,
                 internal/grants/rename_test.go
       desc:     AdoptClients subtracts, per added client, services an active grant of another owner holds on that client
                 before the set-union write. checkRename becomes set-based: 409 only when a child that does not own
                 oldName holds newName.
       covers:   c-3, c-7
       contract: if adopt re-blocks another owner's grant, TestAdopt_SharedSkipsOtherGrant fails: Ada's active netflix
                   grant has TV unblocked; AdoptClients(ben, [Ben phone], [TV]) with Ben phone blocking netflix+tiktok
                   leaves TV blocking tiktok but not netflix
                 if adopt removes anything, TestAdopt_SharedUnion fails: TV already blocking roblox keeps roblox after Ben joins
                 if the exclusion is global rather than per client, TestAdopt_SharedOtherClientUnaffected fails: Ada's
                   netflix grant covers only TV; adopting a second added client "Den" for Ben still blocks netflix on Den
                 if checkRename picks one owner, TestEngineRename_SharedStale fails: Ada+Ben share TV, Ada holds stale "Den";
                   RenameClient("TV","Den") issues exactly one /control/clients/update and returns nil
                 if a shared rename loses an owner, TestEngineRename_Shared fails: rename TV->Lounge TV leaves Ada and Ben
                   both listing "Lounge TV", with exactly one AdGuard update

  t-4  Children PUT sharing + grant shared field
       files:    internal/api/children.go, internal/api/children_test.go, internal/api/grants.go,
                 internal/api/grants_test.go
       desc:     Drop the client loop from updateConflict (name check stays). Add sharedOf(grant, children) feeding
                 `shared` on the 201 and on each GET /grants row; invert the existing "taken client" 409 assertions.
       covers:   c-1, c-5, c-7
       contract: if the API pre-check still 409s, TestChildUpdate_SharesClient fails: PUT /children/{ben} with Ada's
                   "Kid phone" answers 200 and GET /children lists "Kid phone" under both
                 if the shared baseline is skipped on the PUT, TestChildUpdate_SharedAdopts fails: that PUT issues one
                   /control/clients/update for "Kid phone" (AdoptClients ran)
                 if removal or delete write AdGuard, TestChildUpdate_SharedRemoveNoWrite fails: PUT removing a shared client
                   from Ada and DELETE /children/{ada} each issue zero /control/clients/update and Ben still lists it
                 if shared is omitted or includes the granter, TestGrantCreate_Shared fails: Ben's grant over [Ben phone, TV]
                   (TV shared with Ada) answers 201 with shared == [{client:"TV", owners:["Ada"]}]; a grant with no shared
                   client answers shared: [] (not null)
                 if the list drops it, TestGrantsList_Shared fails: GET /grants row for that grant carries the same shared

  t-5  Blocked view attributes other child's grant
       files:    internal/api/blocked.go, internal/api/blocked_test.go
       desc:     handleBlocked reads active grants + children and adds granted_by per service per the pinned contract.
       covers:   c-6
       contract: if attribution is missing, TestBlocked_SharedGrantedBy fails: Ada's netflix grant covers TV; Ben's view
                   (Ben owns Ben phone, TV) lists TV in clients and netflix has granted_by
                   [{grant_id, child:{name:"Ada"}, clients:["TV"]}]
                 if the child's own grants are attributed, TestBlocked_OwnGrantNotAttributed fails: Ben's own grant gives
                   granted_by [] in Ben's view
                 if an unshared grant leaks, TestBlocked_UnrelatedGrant fails: Ada's grant on Ada-only "Kid phone" gives
                   granted_by [] in Ben's view

  t-9  Home: button hint, tap notice, blocked attribution
       files:    web/src/lib/Home.svelte, web/src/lib/Home.test.ts, web/src/lib/tap.ts, web/src/lib/tap.test.ts
       desc:     grantFrom copies created.shared onto the merged grant; Home's note line names shared devices after an
                 applied/partial tap; each child's button group shows the sharedDevices hint; the blocked disclosure keeps
                 services with granted_by (even when unblocked) and labels them "unblocked by Ada's grant".
       covers:   c-5, c-6, c-8
       contract: if grantFrom drops shared, tap.test shared merge fails: a 201 with shared [{TV,[Ada]}] yields an
                   applied outcome whose grant.shared equals it
                 if the tap notice is missing, Home.test shared tap notice fails: tapping Ben's button with that 201 shows a
                   note containing "TV" and "Ada"
                 if the button hint is missing or a confirm is added, Home.test shared button hint fails: Ben's button group
                   shows a hint naming "TV" and "Ada" before any tap, and one tap issues exactly one POST /grants with no dialog
                 if granted_by is filtered with unblocked rows, Home.test blocked granted_by fails: a netflix service with
                   state "unblocked" and granted_by [{child:{name:"Ada"}}] is listed and its text contains "Ada"
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 multi-owner PUT succeeds, both list it | t-1, t-4 |
| c-2 children page: enabled + "shared with <names>", tick shares | t-7 |
| c-3 sharing merges baseline, never re-blocks another owner's grant | t-3 (t-4 proves the PUT path calls it) |
| c-4 unblock on every owned client; ref-counted re-block on expiry/End/reconcile | t-2 |
| c-5 201 + tap result + active row name shared devices and other owners | t-4 (201, list), t-8 (row), t-9 (tap result) |
| c-6 blocked view lists shared device, attributes other child's grant | t-5 (server), t-9 (render); t-8 types |
| c-7 rename / remove-from-one / delete-owner keep remaining owners; only rename writes AdGuard | t-1 (store), t-3 (engine rename), t-4 (zero-write removal/delete) |
| c-8 passive pre-tap hint on buttons + grant form, no confirm | t-6 (grant form), t-9 (buttons) |

8/8 criteria covered.

## Judgment calls

- Ref-count computed on demand from `ListActiveGrants` inside `revert` (store rows are the intent); rejected a refcount column/table — the active-grant rows already are the count and a second source would drift.
- Children page derives owners from the `children` list it already loads; rejected widening `ClientView.child` to an array — no criterion needs the wire change and ClientNames/MigrationBanner keep working (they show one owner; acceptable, not in scope).
- Grant `shared` computed server-side for both 201 and GET /grants rows; rejected computing the row client-side — it would mean passing children into ActiveGrants via Home and a second, divergent computation.
- Pre-tap hint uses a pure client-side `sharedDevices(childId, children)` helper; rejected a server preview endpoint — Home and GrantForm already hold Child[] with clients.
- `granted_by` built in internal/api/blocked.go; rejected extending the pure `blocked.Compute` — attribution is a grants/store join, not an AdGuard fold, and keeping it in the handler saves two files.
- Shared-stale rename semantics (owner already holding newName) are fixed in t-1/t-3 because the schema change makes the existing single-owner branch delete a co-owner's row (c-7 data loss); otherwise rename/create-client stale-mapping 409 rules are left unchanged.
- POST /clients (device labelling) is untouched: it creates a client nobody owns in AdGuard, so the stale-mapping 409 keeps its meaning; sharing happens via PUT /children (c-1's surface).
- No new engine method for Create: `handleGrantCreate` already passes `child.Clients`, so once the store allows sharing, c-4's "shared ones included" is test-only (t-2's first contract).
- No separate migration/fixture tasks: the 0005 migration rides with the store code that needs it, and the fake AdGuard already serves the fixtures these tests need.
- Two waves only: every backend task needs t-1's schema (engine and API tests run against the real sqlite store); frontend tasks mock fetch, so t-6/t-7/t-8 run in wave 1 and only t-9 waits on the helper and the types.
