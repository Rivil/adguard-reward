# Planner draft — LENS: verification

Method: for each criterion I wrote the test that would prove it first (the
"ideal contract"), then cut the smallest task that makes that test passable.
Where the existing suite asserts the opposite of the new behaviour (the 409
tests), the task that flips the behaviour owns rewriting that test, so the
inversion is a named contract, not collateral breakage.

Fixture facts used below are the real ones in
`internal/adguard/adguardtest/testdata/clients.json`: Kid phone = own list
`[youtube, tiktok]`, ids `aa:bb:cc:dd:ee:01, 192.168.1.50`; Kid tablet inherits
global; global = `[tiktok, roblox]`; Old laptop = `null` own list. Children in
contracts: Ada (id 1), Ben (id 2), Cleo (id 3). **Kid phone plays the shared
device** throughout, so no fake-AdGuard task is needed.

### Where the one-client-one-child rule lives (found before planning)

- Schema: `child_clients.client_name TEXT PRIMARY KEY`
  (`internal/store/migrations/0002_children.sql`) — the hard constraint.
- Store: `checkConflicts` client loop (`internal/store/children.go`) returns
  `*ErrClientTaken` for CreateChild / UpdateChild; `clientOwner`
  (`internal/store/clients.go`) is a single-row `QueryRow` used by RenameClient
  and AddChildClient.
- API: `updateConflict` (`internal/api/children.go`) pre-checks the same rule
  and is what actually produces the PUT 409 before AdGuard is touched;
  `writeStoreError` maps it.
- Single-owner consumers that silently go wrong once sharing exists:
  `ownerIndex` (`internal/api/clients.go`, used by `handleClients`,
  `handleClientCreate`, `renamedView`) maps name → one child; `computeOffer`
  (`internal/api/migration.go`) would list a shared global-list client twice
  and migrate it twice; `Engine.checkRename` (`internal/grants/rename.go`)
  keeps only the last owner; `clientView.child` and the SPA's
  `ClientView.child` are singular; `Children.svelte` disables a foreign-owned
  checkbox.
- Revert: `Engine.revert` (`internal/grants/grants.go`) re-adds every service
  on every stored client unconditionally — the thing c-4 must ref-count.
  `Engine.AdoptClients` (`internal/grants/clients.go`) adds the full baseline —
  the thing c-3 must trim.

### Ideal contracts per criterion (the design input)

- **c-1** Ada holds Kid phone. `PUT /children/2 {name:"Ben", clients:["Kid phone"]}`
  → 200; `GET /children` lists Kid phone under Ada *and* Ben; `GET /clients`
  row for Kid phone has `children: [{1,Ada},{2,Ben}]`. A DB created at 0004
  keeps every mapping after 0005.
- **c-2** Children page, clients `[{name:"Kid phone", children:[Ada]}]`: Ben's
  Kid phone checkbox is enabled and its label reads "shared with Ada"; tick +
  Save PUTs `/children/2` with `"Kid phone"` in `clients`.
- **c-3** Ada's active grant on youtube left Kid phone at `[tiktok]`. Ben holds
  Kid tablet with own list `[youtube, roblox]`. PUT Ben adding Kid phone →
  Kid phone own list is exactly `[roblox, tiktok]`: roblox merged in, tiktok
  kept, youtube *not* re-added.
- **c-4** Grants G1 (Ada, youtube, clients [Kid phone, Kid tablet]) and G2 (Ben,
  youtube, [Kid phone]). G1 expires → Kid tablet blocks youtube, Kid phone
  still does not. G2 expires → Kid phone blocks youtube. Same for End, and for
  a reconcile pass in which *both* are overdue (Kid phone must end blocked).
- **c-5** `POST /grants` for Ada → 201 carries
  `shared: [{"client":"Kid phone","others":[{"id":2,"name":"Ben"}]}]`;
  `GET /grants` rows carry the same; the phone shows "Also unlocked on Kid
  phone — shared with Ben" after the tap and in the active-grant row.
- **c-6** `GET /children/2/blocked` while G1 is active: `clients` includes Kid
  phone, the youtube row has
  `unblocked_by: [{client:"Kid phone", grant_id:G1, child:{1,Ada}}]`; Home
  renders "unblocked on Kid phone by Ada's grant" even when youtube's state is
  `unblocked` (today's filter would hide it).
- **c-7** Ada+Ben hold Kid phone. Rename → "Family TV": exactly one
  `/control/clients/update`, both children hold "Family TV". PUT Ben without
  Kid phone → Ada still holds it, 0 AdGuard writes. DELETE Ben → Ada still
  holds it, 0 AdGuard writes.
- **c-8** Home, Ada has a button and owns shared Kid phone: before any tap the
  section shows "Kid phone is shared with Ben"; tapping issues exactly one
  `POST /api/v1/grants` and no `role=dialog` appears. GrantForm with Ada
  selected shows the same note; selecting an unshared child shows none.

### Pinned wire contract (shared by backend and SPA tasks — the UI never waits on the backend)

```
GET  /api/v1/clients        clientView.child (single) is REPLACED by
                            children: [{id, name}]   id ASC, never null ([] = unassigned)
                            (same clientView in POST /clients 201 and POST /clients/rename 200)
PUT  /api/v1/children/{id}  unchanged shape; a client another child holds is no longer 409
                            (name-taken 409 stays)
POST /api/v1/grants 201     {id, ends_at, applied, failed[],
                             shared: [{client, others: [{id, name}]}]}
                            shared = the grant's stored clients that ≥1 child other than the
                            granting child currently holds; sorted by client; others id ASC;
                            never null
GET  /api/v1/grants         each grant row + the same `shared`, computed from the current mapping
GET  /api/v1/children/{id}/blocked
                            services[] + unblocked_by: [{client, grant_id, child: {id, name}}]
                            one entry per (present mapped client, active grant of ANOTHER child)
                            where that grant covers the service on that client and the client
                            does not effectively block it; sorted (client, grant_id); never null
GET  /api/v1/migration      a shared client appears once; child = its lowest-id owner
```

---

```
Phase shared-devices — 13 tasks across 3 waves

Wave 1
  t-1  Store: shared client ownership + migration 0005
       files:    internal/store/migrations/0005_shared_clients.sql (new),
                 internal/store/children.go, internal/store/children_test.go,
                 internal/store/clients.go, internal/store/clients_test.go
       desc:     Rebuild child_clients with PRIMARY KEY (child_id, client_name)
                 (create-copy-drop-rename, index on client_name and child_id,
                 FK CASCADE kept); drop the client loop from checkConflicts;
                 clientOwner → clientOwners (all owners, id ASC). RenameClient
                 moves every owner's row (collapsing where an owner already
                 holds newName), refuses only a newName held by a non-owner.
       covers:   c-1, c-7
       contract: - if the client_name PK survives, TestUpdateChild_SharedClient
                   fails: Ada holds Kid phone; UpdateChild(Ben,"Ben",[Kid phone])
                   returns nil and ListChildren shows Kid phone under Ada and Ben
                 - if checkConflicts still checks clients, TestCreateChild_SharedClient
                   fails: CreateChild("Cleo",[Kid phone]) while Ada holds it
                   returns Cleo with [Kid phone], not *ErrClientTaken
                 - if the rebuild drops rows, TestMigration0005_KeepsMappings
                   fails: a DB migrated to 0004 with Ada→Kid phone, Ben→Kid tablet,
                   reopened at 0005, lists exactly those mappings
                 - if the composite key is missing, TestChildClients_CompositePK
                   fails: a raw second INSERT (1,"Kid phone") errors with a
                   constraint violation
                 - if the CASCADE is lost in the rebuild,
                   TestDeleteChild_SharedSurvives fails: DeleteChild(Ben) leaves
                   Ada holding Kid phone and no child_clients row for Ben
                 - if UpdateChild deletes by client name, TestUpdateChild_RemoveShared
                   fails: Ben saved without Kid phone leaves Ada holding it
                 - if RenameClient renames only one owner's row,
                   TestRenameClient_Shared fails: Ada+Ben hold Kid phone, each with
                   an active grant on it → both hold "Family TV", neither holds
                   "Kid phone", both active grants store "Family TV"
                 - if collapse is per-table not per-owner, TestRenameClient_SharedSelfStale
                   fails: Ada [Kid phone, TV], Ben [Kid phone]; rename Kid phone→TV
                   → Ada [TV] (one row), Ben [TV], no PK error
                 - if a non-owner's stale name is merged silently,
                   TestRenameClient_ForeignStale fails: Cleo holds stale "TV";
                   rename Kid phone (Ada, Ben)→TV returns *ErrClientTaken naming
                   Cleo and both tables are unchanged
                 - existing TestChildren name-taken (ErrNameTaken) and
                   TestAddChildClient_Errors stay green unchanged (AddChildClient
                   still refuses another child's stale mapping — POST /clients
                   creates a new device, it does not share one)
       depends:  —
       status:   pending

  t-2  Blocked fold names another child's grant
       files:    internal/blocked/blocked.go, internal/blocked/blocked_test.go
       desc:     Input gains Self (child id) and Grants []GrantCover{ID, ChildID,
                 ChildName, Services, Clients}; ServiceState gains UnblockedBy
                 []Unblock{Client, GrantID, ChildID, ChildName} per the pinned
                 rule. Pure, no I/O.
       covers:   c-6
       contract: - if foreign grants are not folded, TestCompute_UnblockedByOther
                   fails: Self=Ben, mapped [Kid phone] not blocking youtube, grant
                   {ID 7, Ada, [youtube], [Kid phone]} → youtube state unblocked,
                   UnblockedBy [{Kid phone, 7, 1, "Ada"}]
                 - if Self is not excluded, TestCompute_OwnGrantNotNamed fails:
                   same input with Self=Ada → UnblockedBy is []
                 - if drift is ignored, TestCompute_ReblockedNotNamed fails: Kid
                   phone blocks youtube again (parent re-blocked) → state blocked,
                   UnblockedBy []
                 - if client coverage is not checked, TestCompute_OtherClientGrant
                   fails: Ada's grant stored on Kid tablet only, Ben maps Kid phone
                   → no entry
                 - if partial loses the attribution, TestCompute_PartialShared
                   fails: Ben maps [Kid phone, Old laptop(own [youtube])] →
                   partial, Differs [Kid phone], UnblockedBy [Kid phone by Ada]
                 - if a missing client is attributed, TestCompute_MissingClient
                   fails: grant on a mapped client AdGuard lacks → no entry
                 - if the slice can be nil, TestCompute_UnblockedByNeverNil fails:
                   no grants → every UnblockedBy marshals as []
       depends:  —
       status:   pending

  t-3  Engine: ref-counted revert on shared clients
       files:    internal/grants/cover.go (new), internal/grants/grants.go,
                 internal/grants/grants_test.go, internal/grants/reconcile_test.go
       desc:     cover.go: coveredOn(active []store.Grant, exclude int64, client)
                 → set of services. revert reads ListActiveGrants fresh at each
                 call and, per stored client, re-adds only g.Services minus what
                 another active grant covers on that client; a client whose
                 services are all covered gets no write. Used by timer expiry,
                 End and reconcile (all go through revert). A failed active-grant
                 read fails the revert (grant stays active), never re-blocks blind.
       covers:   c-4
       contract: - if revert is not ref-counted, TestEngine_SharedExpiryKeepsOther
                   fails: G1 (Ada, youtube, [Kid phone, Kid tablet*]) and G2 (Ben,
                   youtube, [Kid phone]); advance past G1 → Kid tablet blocks
                   youtube, Kid phone does not, G1 expired; advance past G2 → Kid
                   phone blocks youtube
                 - if cover is per-grant not per-service, TestEngine_SharedPerService
                   fails: G1 [youtube, tiktok], G2 [youtube], both on Kid phone; G1
                   expires → Kid phone blocks tiktok, not youtube
                 - if cover spans clients, TestEngine_CoverIsPerClient fails: G2
                   covers youtube on Kid phone only; G1's expiry still re-blocks
                   youtube on Kid tablet
                 - if End bypasses the cover, TestEngine_SharedEnd fails: End(G1)
                   with G2 active returns nil, G1 ended, Kid phone still lacks
                   youtube; End(G2) then re-blocks it
                 - if the cover is snapshotted once per pass,
                   TestReconcile_BothOverdueShared fails: G1 and G2 both past
                   ends_at with no timer fired → after one Reconcile both are
                   expired, Reverted == 2 and Kid phone blocks youtube (a stale
                   snapshot leaves it unblocked forever)
                 - if expired/ended grants count as cover,
                   TestEngine_ExpiredGrantNoCover fails: an ended G0 on Kid phone
                   does not keep youtube open when G1 expires
                 - if a fully covered client still gets a POST,
                   TestEngine_CoveredNoWrite fails: G1 on [Kid phone] only, G2
                   active → G1's expiry issues 0 /control/clients/update for Kid
                   phone
                 - TestEngine_TimerReverts and TestReconcile_DriftRepaired stay
                   green unchanged (single-owner behaviour preserved)
       depends:  —
       status:   pending

  t-4  SPA api: shared wire types + sharedDevices
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts,
                 web/src/lib/grants.ts, web/src/lib/grants.test.ts
       desc:     Per the pinned contract: SharedDevice {client, others: ChildRef[]};
                 Grant.shared and GrantCreated.shared; ServiceState.unblocked_by;
                 ClientView gains children: ChildRef[] (child kept for now, t-9
                 removes it). Every new array defaulted to [] when absent. Pure
                 sharedDevices(childId, children: Child[]) → [{client, others:
                 string[]}] in grants.ts.
       covers:   c-2, c-5, c-6, c-8
       contract: - if a missing key is not defaulted, api.test listGrants/createGrant
                   fail: a row or 201 without `shared` yields shared: [] (no
                   undefined reaching a template)
                 - if unblocked_by is not defaulted, api.test childBlocked fails:
                   a service row without it yields unblocked_by: []
                 - if children is not defaulted, api.test listDevices fails: a
                   client row without `children` yields children: []
                 - if self is not excluded, grants.test sharedDevices fails: Ada
                   [Kid phone, Kid tablet], Ben [Kid phone] → for Ada exactly
                   [{client:"Kid phone", others:["Ben"]}]; for Ben
                   [{client:"Kid phone", others:["Ada"]}]
                 - if ordering drifts, grants.test sharedDevicesOrder fails: three
                   owners list others in children-array order, rows sorted by
                   client; an unshared child → []
       depends:  —
       status:   pending

Wave 2 (depends t-1, t-2, t-3, t-4)
  t-5  Engine: adopt skips covered, multi-owner rename
       files:    internal/grants/clients.go, internal/grants/clients_test.go,
                 internal/grants/rename.go, internal/grants/rename_test.go
       desc:     AdoptClients writes, per added client, baseline minus
                 coveredOn(active, 0, client) — services another owner's active
                 grant unblocked on that exact client; still set-union, nothing
                 removed. checkRename: refuse only when a child holding newName
                 does not also hold oldName.
       covers:   c-3, c-7
       contract: - if the subtraction is missing, TestAdoptClients_SkipsCovered
                   fails: Ada's active grant (youtube, [Kid phone]) left Kid phone
                   [tiktok]; Ben's sibling Kid tablet own [youtube, roblox];
                   AdoptClients(Ben, [Kid tablet], [Kid phone]) → Kid phone own
                   list exactly [roblox, tiktok]
                 - if union became replace, the same test fails on tiktok (only
                   Kid phone had it; it must survive)
                 - if subtraction is too broad, TestAdoptClients_CoverIsPerClient
                   fails: Ada's youtube grant stored on Old laptop only → adopting
                   Kid phone for Ben writes youtube
                 - existing TestAdoptClients_GrantServices stays green: Ben's own
                   active grant services still join Ben's baseline (that grant
                   never touched the added client)
                 - if a fully covered adopt still writes, TestAdoptClients_AllCovered
                   fails: baseline ⊆ current ∪ covered → 0 /control/clients/update
                 - if checkRename keeps one owner, TestRename_Shared fails: Ada+Ben
                   hold Kid phone; RenameClient(Kid phone, Family TV) → exactly one
                   POST /control/clients/update, both children hold Family TV, and
                   expiring Ben's grant re-blocks on "Family TV"
                 - if a non-owner's stale name is allowed, TestRename_ForeignStale
                   fails: Cleo holds stale "TV" → *store.ErrClientTaken naming Cleo,
                   0 AdGuard writes; TestRename_SelfStale stays green
       depends:  t-1, t-3
       status:   pending

  t-6  Clients API: owners list, rename view
       files:    internal/api/clients.go, internal/api/clients_test.go,
                 internal/api/rename.go, internal/api/rename_test.go
       desc:     ownerIndex → ownersIndex (name → []childRef, id ASC); clientView
                 `child` replaced by `children`. handleClientCreate's stale-mapping
                 409 now fires when any holder differs from child_id. renamedView
                 lists every owner.
       covers:   c-1, c-7
       contract: - if the index keeps one owner, TestClients_SharedOwners fails:
                   Ada+Ben hold Kid phone → its row has children
                   [{1,"Ada"},{2,"Ben"}] and the JSON has no "child" key
                 - if the empty case marshals null, TestClients_UnassignedEmpty
                   fails: Old laptop row has "children": []
                 - if the create pre-check regresses, TestClientCreate_StaleForeign
                   fails: name held by Ben's stale mapping with child_id 1 → 409
                   naming Ben and 0 /control/clients/add; with child_id 2 → 201,
                   children [{2,"Ben"}]
                 - if the rename read-back keeps one owner, TestRename_SharedView
                   fails: rename shared Kid phone→Family TV → 200 body children
                   lists Ada and Ben
       depends:  t-1
       status:   pending

  t-7  Grants API: shared devices on 201 and list
       files:    internal/api/grants.go, internal/api/grants_test.go
       desc:     Pure sharedOf(clients, childID, children) per the pinned rule;
                 handleGrantCreate reads ListChildren before Create (a failure is
                 500 with no AdGuard write) and adds `shared` to the 201;
                 grantView gains `shared` for GET /grants.
       covers:   c-4, c-5
       contract: - if shared is not computed, TestGrants_CreateShared fails: Ada
                   [Kid phone, Kid tablet], Ben [Kid phone]; POST {child_id:1,
                   services:[youtube]} → 201 shared exactly
                   [{"client":"Kid phone","others":[{"id":2,"name":"Ben"}]}]
                 - if the grant skips the shared client, the same test fails on
                   the fake: Kid phone's blocked_services no longer contains
                   youtube after the 201 (c-4 apply side)
                 - if the granting child is not excluded or order drifts,
                   TestGrants_SharedThreeOwners fails: Ada, Ben, Cleo hold Kid
                   phone; Ben's grant → others [{1,Ada},{3,Cleo}]
                 - if the slice can be nil, TestGrants_UnsharedEmpty fails: an
                   unshared grant's 201 and GET row carry "shared": []
                 - if the list uses creation-time owners, TestGrants_ListSharedLive
                   fails: Ben added to Kid phone after Ada's grant → GET /grants
                   row for that grant lists Ben in shared
                 - TestGrants_ViewShape is extended to the new key set (id,
                   child_id, services, clients, shared, started_at, ends_at)
       depends:  t-1
       status:   pending

  t-8  Blocked API: unblocked_by from active grants
       files:    internal/api/blocked.go, internal/api/blocked_test.go
       desc:     handleBlocked also reads Grants.List and ListChildren (for
                 names) after the 404 check, passes Self and GrantCovers into
                 blocked.Compute, and emits unblocked_by on every service row.
       covers:   c-6
       contract: - if the fold is not wired, TestBlocked_SharedUnblockedBy fails:
                   Ada+Ben hold Kid phone, Ada's active grant on youtube →
                   GET /children/2/blocked lists Kid phone in clients and the
                   youtube row has unblocked_by [{client:"Kid phone",
                   grant_id:<G>, child:{id:1,name:"Ada"}}]
                 - if Self is not passed, TestBlocked_OwnGrantUnnamed fails:
                   GET /children/1/blocked → youtube unblocked_by []
                 - if a row can omit the key, TestBlocked_UnblockedByShape fails:
                   every service row has "unblocked_by" as an array
                 - TestBlocked_NotFound stays green: unknown child is 404 with 0
                   AdGuard requests (grants are read after the child lookup)
       depends:  t-1, t-2
       status:   pending

  t-9  Children page: shared label, enabled checkbox
       files:    web/src/lib/api.ts, web/src/lib/Children.svelte,
                 web/src/lib/Children.test.ts, web/src/lib/ClientNames.svelte,
                 web/src/lib/ClientNames.test.ts
       desc:     Drop ClientView.child; Children.svelte never disables a
                 foreign-owned client and labels it "shared with <other owners,
                 joined ', '>"; ClientNames shows every owner. Test fixtures move
                 to `children`.
       covers:   c-2
       contract: - if the checkbox stays disabled, Children.test sharedEnabled
                   fails: Kid phone children [Ada] → Ben's Kid phone checkbox is
                   enabled and its label contains "shared with Ada"; Ada's own
                   label does not contain "shared with Ada"
                 - if ticking is not wired, Children.test shareByCheckbox fails:
                   tick Kid phone in Ben's form + Save → PUT /api/v1/children/2
                   body clients includes "Kid phone"; after the reload (children
                   [Ada, Ben]) Ada's label reads "shared with Ben"
                 - if names are not joined, Children.test sharedThree fails: Cleo's
                   form shows "shared with Ada, Ben"
                 - if the old wording lingers, Children.test fails on any
                   "assigned to" text
                 - if ClientNames keeps one owner, ClientNames.test owners fails:
                   Kid phone row reads "Ada, Ben"
                 - make typecheck fails if any consumer still reads .child on a
                   ClientView
       depends:  t-4
       status:   pending

  t-10 Tap and active-grant row name shared devices
       files:    web/src/lib/tap.ts, web/src/lib/tap.test.ts,
                 web/src/lib/ActiveGrants.svelte,
                 web/src/lib/ActiveGrants.panel.test.ts
       desc:     grantFrom copies created.shared onto the merged grant (applied
                 and partial outcomes carry it); acceptOffer keeps the target's
                 shared. ActiveGrants renders, per row with shared, "also on
                 <client> (shared with <names>)".
       covers:   c-5
       contract: - if the 201's shared is dropped, tap.test sharedMerged fails:
                   a 201 with shared [{Kid phone, [Ben]}] → merge() receives a
                   grant with that shared and Outcome applied.grant.shared equals
                   it; the partial outcome carries it too
                 - if the extend path loses it, tap.test acceptKeepsShared fails:
                   an accepted offer's merged grant keeps the target's shared
                 - if the row ignores shared, ActiveGrants.panel.test sharedRow
                   fails: that grant's row text contains "Kid phone" and "Ben";
                   an unshared row contains neither
       depends:  t-4
       status:   pending

  t-11 GrantForm passive shared-device hint
       files:    web/src/lib/GrantForm.svelte, web/src/lib/GrantForm.test.ts,
                 web/src/lib/Buttons.test.ts
       desc:     When value.child_id selects a child with shared devices, render a
                 role=note line "<client> is shared with <names>" per device via
                 sharedDevices. No control, no effect on `valid`. Buttons page
                 inherits it through GrantForm.
       covers:   c-8
       contract: - if the hint is missing, GrantForm.test sharedHint fails: Ada
                   [Kid phone], Ben [Kid phone]; select Ada → role=note reads
                   "Kid phone is shared with Ben"
                 - if it leaks, GrantForm.test noHint fails: select an unshared
                   child, or no child → no role=note
                 - if the hint gates submit, GrantForm.test hintPassive fails:
                   with the note shown, child + service + minutes make valid true
                   with no further interaction, and the note contains no button
                   or checkbox
                 - if Buttons bypasses GrantForm's hint, Buttons.test sharedHint
                   fails: editing a button for Ada shows the note and Save issues
                   one PUT /api/v1/buttons
       depends:  t-4
       status:   pending

Wave 3 (depends t-5, t-10, t-11)
  t-12 Children API: PUT shares, migration offer dedup
       files:    internal/api/children.go, internal/api/children_test.go,
                 internal/api/migration.go, internal/api/migration_test.go
       desc:     updateConflict checks names only; a shared add flows through
                 AdoptClients (now cover-aware). computeOffer dedups mapped names
                 and reports the lowest-id owner. Rewrite the client-taken cases
                 of TestChildren_Conflicts / TestChildUpdate_Conflicts.
       covers:   c-1, c-3, c-7
       contract: - if updateConflict still checks clients, TestChildUpdate_Share
                   fails: Ada holds Kid phone; PUT /children/2 {Ben,[Kid phone]}
                   → 200 (not 409) and GET /children lists Kid phone under both
                 - if POST still conflicts, TestChildren_CreateShared fails: POST
                   /children {Cleo,[Kid phone]} → 201
                 - name-taken still 409: TestChildren_Conflicts keeps its
                   duplicate-name case
                 - if the HTTP path re-blocks a covered service,
                   TestChildUpdate_ShareKeepsOtherGrant fails: POST /grants Ada
                   youtube (Kid phone → [tiktok]); Ben holds Kid tablet with own
                   [youtube, roblox]; PUT Ben adding Kid phone → fake Kid phone
                   blocked exactly [roblox, tiktok]
                 - if removal touches AdGuard or other owners,
                   TestChildUpdate_UnshareOffline fails: PUT Ben without Kid phone
                   → 200, Ada still lists it, 0 POST /control/clients/update
                 - if delete cascades too far, TestChildDelete_SharedSurvives
                   fails: DELETE /children/2 → 204, Ada still lists Kid phone,
                   0 AdGuard writes
                 - if the offer is not deduped, TestMigration_SharedOnce fails:
                   Kid tablet (use_global) held by Ada and Ben → GET /migration
                   lists it once with child {1,Ada}; POST /migration issues
                   exactly one /control/clients/update for Kid tablet
       depends:  t-1, t-5
       status:   pending

  t-13 Home: pre-tap hint, post-tap notice, unblocked-by
       files:    web/src/lib/Home.svelte, web/src/lib/Home.test.ts
       desc:     Per child section with buttons: passive role=note from
                 sharedDevices. After applied/partial with shared, a role=status
                 line "Also unlocked on <client> — shared with <names>" (beside
                 the partial alert, not replacing it). Blocked disclosure shows
                 services with non-empty unblocked_by even when unblocked, as
                 "unblocked on <client> by <child>'s grant".
       covers:   c-5, c-6, c-8
       contract: - if the hint is missing, Home.test sharedHint fails: Ada (with a
                   button) and Ben both hold Kid phone → before any tap Ada's
                   section has role=note "Kid phone is shared with Ben"
                 - if a confirm step creeps in, Home.test noConfirm fails: one
                   click on the tile → exactly one POST /api/v1/grants and no
                   role=dialog element
                 - if the notice is missing, Home.test sharedNotice fails: 201
                   with shared [{Kid phone, [Ben]}] → role=status reads "Also
                   unlocked on Kid phone — shared with Ben"; a 201 with shared []
                   renders no status line
                 - if partial overwrites the notice, Home.test partialShared fails:
                   applied:false + shared → both the "except on" alert and the
                   shared status are present
                 - if the ad-hoc path skips it, Home.test adhocShared fails: the
                   ad-hoc form for Ada shows the GrantForm note and its 201 shows
                   the same status line
                 - if the filter hides it, Home.test unblockedBy fails: Ben's
                   view with youtube state 'unblocked' and unblocked_by
                   [{Kid phone, 7, Ada}] lists youtube with "unblocked on Kid
                   phone by Ada's grant"; an unblocked service with
                   unblocked_by [] stays hidden
       depends:  t-4, t-10, t-11
       status:   pending
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 | t-1 (schema + store), t-6 (GET /clients owners), t-12 (PUT 200, both listed) |
| c-2 | t-4 (ClientView.children), t-9 (enabled checkbox, "shared with" label) |
| c-3 | t-5 (AdoptClients cover subtraction), t-12 (end-to-end over PUT) |
| c-4 | t-3 (ref-counted revert on expiry / End / reconcile), t-7 (apply on the shared client over HTTP) |
| c-5 | t-4 (types), t-7 (201 + GET shared), t-10 (tap merge + active-grant row), t-13 (tap notice) |
| c-6 | t-2 (pure fold), t-8 (blocked API), t-4 (types), t-13 (rendering) |
| c-7 | t-1 (store rename/remove/delete), t-5 (engine rename, one AdGuard write), t-6 (rename view), t-12 (remove/delete offline over HTTP) |
| c-8 | t-4 (sharedDevices), t-11 (GrantForm + Buttons hint), t-13 (Home buttons hint, no confirm) |

All 8 of 8 criteria covered.

## Judgment calls

- Ref-count computed at revert time from a fresh `ListActiveGrants` per grant; rejected a stored refcount column / per-pass snapshot, because the store rows already are the truth and a per-pass snapshot leaves the device unblocked forever when two overdue grants revert in one reconcile pass (that's what TestReconcile_BothOverdueShared catches).
- Cover keyed on (stored client, service), excluding only the grant being reverted; rejected per-child cover, because the locked rule is "no active grant (of any owner) still covers that service on that device".
- A failed active-grant read fails the revert (grant stays active for the reconciler); rejected re-blocking blind on error, because that's exactly the "one child's revert cuts short another's" outcome the locked decision forbids.
- c-3 subtraction uses only grants whose stored clients include the added client; rejected subtracting all active-grant services, because that would leave the joining child's baseline open on services no grant touched on that device (TestAdoptClients_CoverIsPerClient).
- `shared` / `unblocked_by` are computed at read time from the current mapping; rejected storing owners on the grant row, because owners change (c-7) and a stored snapshot would need a migration and name the wrong people after an unshare.
- `clientView.child` is replaced by `children[]`, with no dual field; rejected keeping `child` as "first owner", because a stale singular field is how today's single-owner consumers would keep silently mis-reporting.
- t-4 adds `ClientView.children` and t-9 removes `child`; rejected doing both in t-4, because removing it in wave 1 breaks `make typecheck` for Children/ClientNames before their task runs.
- Rename onto a name held by a non-owner child stays 409 (store, engine, API); rejected merging it into a share, because that would silently give the device to a child nobody ticked it for.
- POST /clients stale-foreign 409 and AddChildClient's refusal stay as they are; rejected loosening them, because creating a new device is not sharing an existing one and the spec only asks for sharing through PUT.
- The migration offer dedups shared clients and names the lowest-id owner; rejected switching migrationClient to an owners array, because it's a regression fix (double listing, double write) and not in this phase's criteria.
- The pre-tap hint is computed client-side from `listChildren` (`sharedDevices`); rejected a new endpoint or a field on GET /children, because the SPA already holds every child's client list.
- `unblocked_by` only names another child's grant on a client that effectively doesn't block the service; rejected naming own grants or drifted (re-blocked) clients, because the criterion says "another child's" and "unblocked there".
- Home shows a service with non-empty `unblocked_by` even when its state is `unblocked`; rejected keeping the current non-unblocked filter, because a child whose only device is the shared one would never see the attribution.
- POST /grants reads ListChildren before `Create`; rejected reading it after apply, because a failure then would 500 on a grant that already unblocked AdGuard.
- No fake-AdGuard task: the existing fixture with Kid phone as the shared device and MutateClient for Kid tablet's own list covers every contract; rejected adding a "Family TV" fixture, because more fixture rows would put the existing count-based fixture tests at risk for no extra coverage.
- t-1 keeps rename and migration in one 5-file store task; rejected splitting them, because once the PK changes, `clientOwner`'s single-row QueryRow is wrong straight away, so the store suite is only consistent when both land together.
