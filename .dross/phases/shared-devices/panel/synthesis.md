# shared-devices — panel synthesis

Inputs: risk.md, mvp.md, verification.md, checked against the source on branch
`phase/shared-devices`. Claims verified in code before scoring:

- `child_clients.client_name TEXT PRIMARY KEY` (0002_children.sql); `clientOwner` is a single-row
  `QueryRow` used by `RenameClient` and `AddChildClient` (internal/store/clients.go). Once the PK
  is composite, RenameClient's `DELETE FROM child_clients WHERE client_name = ?` branch deletes
  every co-owner's row, and AddChildClient can return a co-owner as `*ErrClientTaken` for a child
  that already holds the client.
- `Engine.Create` takes `clients` as an argument, and `grant_clients` has no FK to `child_clients`
  (0003_grants.sql). So engine revert and adopt tests do not need the schema change.
- `Reconcile` loops over one `ListActiveGrants` snapshot and calls `expireLocked → revert` per
  overdue grant (internal/grants/reconcile.go). A ref-count read from that snapshot would cause
  the both-overdue bug that risk (R5) and verification describe.
- `baselineFrom` only adds services from active grants where `g.ChildID == childID`
  (internal/grants/clients.go), so risk's R9 (a baseline poisoned through a shared sibling) is real.
- `computeOffer` adds every owner's clients to `mapped`, so a shared client would be offered and
  migrated twice (internal/api/migration.go). R14 is real.
- Fixture (internal/adguard/adguardtest/testdata/clients.json): Kid phone has its own list
  `[youtube, tiktok]`; Kid tablet has `use_global_blocked_services: true`, so `Engine.unblock`
  reports it as failed ("migrate it first"). Engine and API contracts that make Kid tablet the
  shared device (risk t-5, t-6, t-8, t-10, t-11, t-16) do not hold against the real fake. Only
  verification noticed this, so its fixture choice is grafted: **Kid phone is the shared device**
  in every merged contract.
- Consumers of `ClientView.child`: Children.svelte:131 and ClientNames.svelte:46. MigrationBanner
  reads `migrationClient.child`, which is a different type and stays as it is.

## Scores

| Draft | Criteria coverage | Test-contract specificity | Granularity | Wave correctness |
|---|---|---|---|---|
| risk | 5/5: 8/8 criteria plus a 25-entry risk register. Only draft with baseline poisoning (R9), revert fail-closed (R6) and an end-to-end task. Misses the AddChildClient shared-self false 409. | 4/5: named tests with exact inputs and outputs, but six engine/API contracts use Kid tablet (inherits global), which is unsound against the real fixture. | 4/5: mostly 2–4 files per task. t-9 spans 5 files (clients, migration, rename) and should split, and it leaves out rename_test.go although a contract needs it. t-15 is a 2-file task split off only to serialise on Home.svelte. | 3/5: t-4 drops `ClientView.child` in wave 1, so `make typecheck` fails on Children.svelte and ClientNames.svelte until t-12 lands in wave 2. t-5 and t-6 list a t-1 dependency they don't need. |
| mvp | 3/5: 8/8 nominal. No reconcile both-overdue case, no revert read-failure, no baseline poisoning, no migration double-offer, no HTTP-level c-3 test. Only draft with the AddChildClient shared-self contract. | 3/5: concrete, but uses "TV" and "Ben phone", which aren't in the fixture. Fewer edge cases per task. | 4/5: no task over 4 files. t-3 (adopt + rename) and t-4 (children PUT + grants shared) each join two unrelated concerns. | 3/5: t-2 and t-3 depend on t-1 on the false premise that "every backend task needs t-1's schema". Otherwise sound. |
| verification | 4/5: 8/8. The existing 409 tests that must be inverted are named contracts. Misses baseline poisoning, AddChildClient shared-self, and degradation when the blocked view's grants read fails. | 5/5: real fixture values throughout, exact expected lists, and named existing tests that must stay green. | 3/5: t-1 (store children + migration + rename) and t-9 (api.ts + Children + ClientNames) are 5 files each and breach the split threshold. | 5/5: revert is correctly in wave 1 (no store dependency). `child` removal is staged so typecheck stays green. t-12 in wave 3 is justified by its adopt contract. Only the wave-2 header "depends t-1..t-4" is broader than the per-task deps. |

**Skeleton: risk.** Verification scores slightly higher in total (17 vs 16). But fixing its two 5-file
tasks reproduces risk's boundaries exactly (store schema / store+engine rename; Children page /
ClientNames). Risk's task boundaries need the fewest structural changes: one split (t-9), one merge
(t-13 + t-15 into one Home task), and one staging fix (`child` removal). Risk also owns failure modes
no other draft names. Grafted from verification: the fixture choice (Kid phone shared), staged
`child` removal, the frontend grouping (tap + ActiveGrants, GrantForm + Buttons, one Home task),
reading ListChildren before the grant is created, and its sharper contracts. Grafted from mvp: the
AddChildClient shared-self contract and the reconcile case where one grant is overdue while
another is still live.

## Merged plan

Fixture convention for every contract: Ada (id 1), Ben (id 2), Cleo (id 3). Kid phone is the shared
device (own list `[youtube, tiktok]`). Kid tablet inherits the global list `[tiktok, roblox]` and is
changed with MutateClient where a test needs an own list.

Pinned wire contract (backend tasks produce it; SPA tasks code against it from wave 1):

```
childRef  = {id, name}
sharedRef = {client: string, others: childRef[]}    // others = current owners minus the grant's child, id ASC
GET  /api/v1/clients           clientView.child -> children: childRef[]  (id ASC, never null)
                               same clientView in POST /clients 201 and POST /clients/rename 200
PUT  /api/v1/children/{id}     shape unchanged; no client-ownership 409 (name-taken 409 stays)
POST /api/v1/children          shape unchanged; a client another child holds is accepted
POST /api/v1/grants 201        {id, ends_at, applied, failed, shared: sharedRef[]}  client ASC, never null
GET  /api/v1/grants            each grant + the same shared, computed from the current mapping
GET  /api/v1/children/{id}/blocked
                               services[] + unblocked_by: [{client, grant_id, child: childRef}]
                               one per (present mapped client, active grant of ANOTHER child) where the
                               grant covers the service on that client and the client does not
                               effectively block it; sorted (client, grant_id); never null
GET/POST /api/v1/migration     shape unchanged; a shared client appears once, child = lowest-id owner
```

```
Phase shared-devices — 17 tasks across 3 waves

Wave 1
  t-1  Rebuild child_clients with composite key  [risk+verification]
       files:    internal/store/migrations/0005_shared_clients.sql (new),
                 internal/store/children.go, internal/store/children_test.go,
                 internal/store/store_test.go
       desc:     Create-copy-drop-rename rebuild to PRIMARY KEY (client_name, child_id), keeping
                 FK ON DELETE CASCADE, the child_id index and an index on client_name, and copying
                 every row. checkConflicts keeps only the name check, so Create/UpdateChild accept a
                 client another child holds. UpdateChild and DeleteChild touch only their own
                 child_id rows.
       covers:   c-1, c-7
       contract: - if the rebuild drops rows, TestMigrate_0005PreservesClients fails: a DB at 0004 with
                   Ada->[Kid phone], Ben->[Kid tablet] holds exactly those rows after Open, and 0005
                   applies after 0004_buttons.sql
                 - if the FK/cascade or child_id index is lost, TestSchema_ChildClientsCascade fails:
                   DELETE FROM children leaves no orphan child_clients rows; the index exists
                 - if the composite key is missing, TestSchema_ChildClientsPK fails: a raw duplicate
                   INSERT (Kid phone, 1) errors with a constraint violation
                 - if the client check survives in UpdateChild, TestUpdateChild_SharedClient fails:
                   Ada holds Kid phone; UpdateChild(Ben, "Ben", [Kid phone]) returns nil and
                   ListChildren shows Kid phone under Ada and Ben
                 - if the client check survives in CreateChild, TestCreateChild_SharedClient fails:
                   CreateChild("Cleo", [Kid phone]) while Ada holds it returns Cleo with [Kid phone]
                 - if UpdateChild deletes by client_name, TestUpdateChild_RemoveKeepsOtherOwner fails:
                   Ben saved without Kid phone leaves Ada holding it
                 - if cascade is by client, TestDeleteChild_KeepsOtherOwner fails: DeleteChild(Ben)
                   leaves Ada holding Kid phone and no row for Ben
                 - existing name-taken (ErrNameTaken) cases stay green unchanged
       depends:  —

  t-2  Pure ref-count helpers for shared clients  [risk; signature per verification's coveredOn]
       files:    internal/grants/shared.go (new), internal/grants/shared_test.go (new)
       desc:     revertPlan(g, active []store.Grant) -> map[client][]service: g.Services minus those
                 covered by another active grant (id != g.ID) whose stored Clients include that
                 client. Clients left with nothing are omitted. heldOpen(active, exclude, client) ->
                 the sorted services any active grant other than `exclude` covers on that client,
                 never nil. Both are keyed on stored grant clients, never on current ownership.
       covers:   c-3, c-4
       contract: - if g counts itself, TestRevertPlan_SelfNotCounted fails: g alone in active -> every
                   client gets all of g's services
                 - if coverage is per-grant not per-client, TestRevertPlan_OtherClient fails: Ben's
                   youtube grant on Kid tablet does not hold youtube open on Kid phone
                 - if coverage is per-grant not per-service, TestRevertPlan_OtherService fails: G1
                   [youtube, tiktok] and G2 [youtube], both on Kid phone -> G1's plan for Kid phone is
                   [tiktok]
                 - if the empty case writes, TestRevertPlan_FullyHeld fails: Kid phone is absent from
                   the map when another grant covers every one of g's services there
                 - if ownership is consulted, TestRevertPlan_StoredClients fails: the plan uses
                   g.Clients verbatim, including a client no child now owns
                 - if heldOpen unions across clients or forgets exclude, TestHeldOpen fails: returns
                   only services of grants whose Clients contain the client, minus `exclude`, sorted,
                   [] not nil
       depends:  —

  t-3  Blocked fold names another child's grant  [risk+verification]
       files:    internal/blocked/blocked.go, internal/blocked/blocked_test.go
       desc:     Input gains Self (child id) and Grants []GrantCover{ID, Child childRef, Services,
                 Clients}. ServiceState gains UnblockedBy []Unblock{Client, GrantID, Child}: the
                 pinned rule (another child's active grant, present mapped client, client does
                 not effectively block the service). Pure, no I/O.
       covers:   c-6
       contract: - if foreign grants are not folded, TestCompute_UnblockedByOther fails: Self=Ben,
                   mapped [Kid phone] not blocking youtube, grant {7, Ada, [youtube], [Kid phone]} ->
                   youtube unblocked, UnblockedBy [{Kid phone, 7, Ada}]
                 - if Self is not excluded, TestCompute_OwnGrantNotNamed fails: same input with
                   Self=Ada -> UnblockedBy []
                 - if drift is ignored, TestCompute_ReblockedNotNamed fails: Kid phone blocks youtube
                   again -> state blocked, UnblockedBy []
                 - if client coverage is not checked, TestCompute_OtherClientGrant fails: Ada's grant
                   stored on Kid tablet only, Ben maps Kid phone -> no entry
                 - if partial loses the attribution, TestCompute_PartialShared fails: Ben maps
                   [Kid phone, Old laptop(own [youtube])] -> partial, Differs [Kid phone], UnblockedBy
                   [Kid phone by Ada]
                 - if a missing client is attributed, TestCompute_MissingClient fails: a grant on a
                   mapped client AdGuard lacks -> no entry
                 - if the slice can be nil, TestCompute_UnblockedByNeverNil fails: no grants -> every
                   UnblockedBy marshals as []
       depends:  —

  t-4  SPA wire types and shared-device helper  [verification+risk+mvp]
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/lib/grants.ts,
                 web/src/lib/grants.test.ts
       desc:     Per the pinned contract: ChildRef/SharedRef. Grant.shared and GrantCreated.shared,
                 ServiceState.unblocked_by, ClientView gains children: ChildRef[]. `child` is kept
                 for now and t-16 removes it, so typecheck stays green. Every new array defaults to
                 [] when absent. grants.ts: pure sharedDevices(childId, children: Child[]) ->
                 SharedRef-like [{client, others: string[]}] and sharedText(refs), the single wording
                 used by the hint, the notice and the row.
       covers:   c-2, c-5, c-6, c-8
       contract: - if defaults are missing, api.test listGrants/createGrant/childBlocked fail: a row,
                   201 or service without shared / unblocked_by yields [] with no TypeError
                 - if children is not defaulted, api.test listDevices fails: a client row without
                   children yields children: []
                 - if self is not excluded, grants.test sharedDevices fails: Ada [Kid phone, Kid
                   tablet], Ben [Kid phone] -> for Ada exactly [{client:"Kid phone", others:["Ben"]}];
                   for Ben [{client:"Kid phone", others:["Ada"]}]; an unshared child -> []
                 - if ordering drifts, grants.test sharedDevicesOrder fails: rows client ASC, others
                   in children-array (id) order across two shuffled inputs
                 - if wording forks, grants.test sharedText fails: [{Kid phone,[Ben, Cleo]}] ->
                   "Kid phone (shared with Ben, Cleo)" and [] -> ""
       depends:  —

Wave 2
  t-5  Ref-counted revert in End, expiry, reconcile  [risk+verification+mvp]
       files:    internal/grants/grants.go, internal/grants/grants_test.go,
                 internal/grants/reconcile.go, internal/grants/reconcile_test.go
       desc:     revert re-reads ListActiveGrants under the mutex on every call and writes
                 AddBlockedServices(client, revertPlan[client]) only. A failed read fails every client:
                 the grant stays active and is retried, never re-blocked blind. Reconcile's overdue
                 loop goes through the same revert, so each expiry sees the statuses the previous one
                 committed.
       covers:   c-4
       contract: - if revert is not ref-counted, TestEngine_SharedExpiryKeepsOther fails: G1 (Ada,
                   youtube, [Kid phone, Old laptop]) and G2 (Ben, youtube, [Kid phone]); advance past
                   G1 -> Old laptop blocks youtube, Kid phone does not, G1 expired; advance past G2 ->
                   Kid phone blocks youtube
                 - if End bypasses it, TestEngine_SharedEnd fails: End(G1) with G2 active -> nil, G1
                   ended, Kid phone still lacks youtube; End(G2) then re-blocks it
                 - if a different service is spared, TestEngine_EndSharedOtherService fails: Ada's
                   grant is tiktok, Ben's is youtube, both on Kid phone; ending Ben's re-blocks youtube
                 - if reconcile uses its snapshot, TestReconcile_BothOverdueShared fails: G1 and G2 both
                   past ends_at, no timer fired -> one Reconcile expires both, Reverted == 2, Kid phone
                   blocks youtube
                 - if reconcile ignores a live owner, TestReconcile_OverdueSharedKeepsOther fails: an
                   overdue Ben grant reverted while Ada's live grant covers Kid phone leaves youtube
                   open there, Reverted == 1
                 - if timers race, TestExpire_SharedTimersSerial fails: both expiry callbacks fired
                   concurrently -> youtube blocked on Kid phone and MaxInFlightUpdates() == 1
                 - if expired/ended grants count as cover, TestEngine_ExpiredGrantNoCover fails: an
                   ended G0 on Kid phone does not keep youtube open when G1 expires
                 - if a fully covered client still gets a POST, TestEngine_CoveredNoWrite fails: G1 on
                   [Kid phone] only, G2 active -> G1's expiry issues 0 /control/clients/update
                 - if the read fails open, TestEnd_ActiveReadFails fails: ListActiveGrants error ->
                   *ErrRevertFailed, zero AdGuard update POSTs, grant still active
                 - if ownership is used, TestExpire_ClientUnassigned fails: Kid phone removed from
                   Ada after her grant; expiry still re-blocks it
                 - TestEngine_TimerReverts and TestReconcile_DriftRepaired stay green unchanged
       depends:  t-2

  t-6  Shared-aware baseline for adopt and create  [risk+verification+mvp]
       files:    internal/grants/clients.go, internal/grants/clients_test.go
       desc:     baselineFrom also unions the services of any active grant whose clients include a
                 sibling (R9). AdoptClients writes, per added client, baseline minus heldOpen(active,
                 0, client): set-union, nothing removed, no write when nothing changes.
                 CreateClient uses the same baseline.
       covers:   c-3
       contract: - if adopt re-blocks, TestAdopt_SharedKeepsOtherGrant fails: Ada's active youtube
                   grant left Kid phone [tiktok]; Ben's sibling Kid tablet own [youtube, roblox];
                   AdoptClients(Ben, [Kid tablet], [Kid phone]) -> Kid phone own list exactly
                   [roblox, tiktok] (union also proven: tiktok survives)
                 - if the subtraction is too broad, TestAdopt_CoverIsPerClient fails: Ada's youtube
                   grant stored on Old laptop only -> adopting Kid phone for Ben writes youtube
                 - if the subtraction is global across added clients, TestAdopt_TwoAdded fails: adding
                   Kid phone (held open by Ada) and Old laptop together -> youtube written to Old
                   laptop, not to Kid phone
                 - if the sibling is poisoned, TestBaseline_SharedSiblingGrant fails: Ben's sibling
                   Kid phone is open for youtube only via Ada's grant; CreateClient("Ben tv", child
                   Ben) sends blocked_services containing youtube
                 - if a no-op writes, TestAdopt_AllCovered fails: baseline ⊆ current ∪ held -> 0
                   /control/clients/update
                 - existing TestAdoptClients_GrantServices stays green (Ben's own grant services still
                   join Ben's baseline)
       depends:  t-2

  t-7  Multi-owner rename in store and engine  [risk+verification+mvp]
       files:    internal/store/clients.go, internal/store/clients_test.go,
                 internal/grants/rename.go, internal/grants/rename_test.go
       desc:     clientOwner -> clientOwners (all owners, id ASC). Store RenameClient moves every
                 owner's row: an owner already holding newName drops its old row, the others are
                 updated, and active grants' stored clients follow. *ErrClientTaken only when a child
                 holds newName but not oldName. AddChildClient checks "this child holds it" before
                 any other owner. checkRename uses owner sets with the same rule, so AdGuard is
                 renamed exactly once.
       covers:   c-7
       contract: - if one row moves, TestRenameClient_Shared fails: Ada+Ben hold Kid phone, each with an
                   active grant on it -> both hold "Family TV", neither holds "Kid phone", both grants
                   store "Family TV"
                 - if the PK collides or the self-stale branch deletes by name,
                   TestRenameClient_SharedSelfStale fails: Ada [Kid phone, TV], Ben [Kid phone];
                   rename Kid phone->TV -> nil, Ada [TV] (one row), Ben [TV]
                 - if merges are silent, TestRenameClient_ForeignStale fails: Cleo holds stale "TV" ->
                   *ErrClientTaken naming Cleo, both tables unchanged
                 - if AddChildClient reads the first owner row, TestAddChildClient_SharedSelf fails:
                   Ada+Ben share Kid phone; AddChildClient(Ben, "Kid phone") -> nil (no-op);
                   TestAddChildClient_Errors (another child's stale mapping refused) stays green
                 - if checkRename keeps one pointer, TestEngineRename_SharedStale fails: the self-stale
                   setup through the engine -> nil and exactly one /control/clients/update
                 - if a shared rename loses an owner, TestEngineRename_Shared fails: Kid phone ->
                   Family TV with one AdGuard update; expiring Ben's grant then re-blocks on "Family TV"
                 - if the outsider check is lost in the engine, TestEngineRename_Outsider fails: zero
                   AdGuard writes and *store.ErrClientTaken
       depends:  t-1

  t-8  Children API: share without 409, remove quietly, migration dedupe  [risk+verification]
       files:    internal/api/children.go, internal/api/children_test.go,
                 internal/api/migration.go, internal/api/migration_test.go
       desc:     updateConflict keeps only the name check. added = in.Clients ∖ cur.Clients still goes
                 to AdoptClients, shared clients included. Removal-only PUTs and DELETE never call
                 AdGuard. computeOffer dedupes mapped names and reports the lowest-id owner. Rewrite
                 the client-taken cases of TestChildren_Conflicts / TestChildUpdate_Conflicts.
       covers:   c-1, c-7
       contract: - if the pre-check survives, TestChildUpdate_ShareNo409 fails: PUT /children/2
                   {Ben, [Kid phone]} while Ada holds it -> 200, GET /children lists Kid phone under both
                 - if POST still conflicts, TestChildren_CreateShared fails: POST /children {Cleo,
                   [Kid phone]} -> 201
                 - if a shared add skips adopt, TestChildUpdate_ShareAdopts fails: that PUT issues
                   exactly one /control/clients/update for Kid phone
                 - if removal writes AdGuard, TestChildUpdate_RemoveSharedNoAdGuard fails: PUT Ben
                   without Kid phone -> 200, zero AdGuard requests, Ada still lists it
                 - if delete writes AdGuard, TestChildDelete_SharedNoAdGuard fails: DELETE /children/2
                   -> 204, zero AdGuard requests, Ada still lists Kid phone
                 - if the name check is lost, TestChildUpdate_NameStill409 fails: PUT Ben {name:"ada"}
                   -> 409 with zero AdGuard requests
                 - if the offer is not deduped, TestMigration_SharedOnce fails: Kid tablet
                   (use_global) held by Ada and Ben -> GET /migration lists it once with child
                   {1, Ada}; POST /migration issues exactly one /control/clients/update for it
       depends:  t-1

  t-9  Clients API: owners list and rename view  [verification+risk]
       files:    internal/api/clients.go, internal/api/clients_test.go,
                 internal/api/rename.go, internal/api/rename_test.go
       desc:     ownerIndex -> map[client][]childRef (id ASC). clientView.child is replaced by
                 children in GET /clients, the POST /clients 201 and the rename read-back.
                 handleClientCreate's stale-mapping 409 fires when any holder differs from child_id.
       covers:   c-1, c-2, c-7
       contract: - if last-writer-wins remains, TestClients_SharedOwners fails: Ada+Ben hold Kid phone
                   -> children [{1,"Ada"},{2,"Ben"}] and the JSON has no "child" key
                 - if the empty case marshals null, TestClients_UnassignedEmpty fails: Old laptop row
                   has "children": []
                 - if the create guard regresses, TestClientCreate_StaleForeign fails: name held by
                   Ben's stale mapping with child_id 1 -> 409 naming Ben, 0 /control/clients/add; with
                   child_id 2 -> 201, children [{2,"Ben"}]
                 - if the rename read-back is single, TestRename_SharedView fails: rename shared Kid
                   phone -> Family TV -> 200 body children lists Ada and Ben
       depends:  t-1

  t-10 Grants API: shared devices on 201 and list  [risk+verification+mvp]
       files:    internal/api/grants.go, internal/api/grants_test.go
       desc:     Pure sharedOf(clients, childID, children) per the pinned rule. handleGrantCreate
                 reads ListChildren before Create (provisional, see Disagreement 4): a failure is a
                 500 with no AdGuard write. The 201 carries shared, and grantView gains shared for
                 GET /grants.
       covers:   c-4, c-5
       contract: - if shared is not computed, TestGrants_CreateShared fails: Ada [Kid phone, Kid
                   tablet], Ben [Kid phone]; POST {child_id:1, services:[youtube]} -> 201 shared
                   exactly [{"client":"Kid phone","others":[{"id":2,"name":"Ben"}]}]
                 - if the grant skips the shared client, the same test fails on the fake: Ben's
                   youtube grant over [Kid phone] removes youtube from Kid phone (c-4 apply side)
                 - if the granter is not excluded or order drifts, TestGrants_SharedThreeOwners fails:
                   Ada, Ben, Cleo hold Kid phone; Ben's grant -> others [{1,Ada},{3,Cleo}]
                 - if overlap is per-client, TestGrantCreate_SecondOwnerSameService fails: Ben's
                   youtube grant while Ada's covers Kid phone -> 201, not 409
                 - if the slice can be nil, TestGrants_UnsharedEmpty fails: an unshared grant's 201 and
                   GET row carry "shared": []
                 - if the list uses creation-time owners, TestGrants_ListSharedLive fails: Ben added to
                   Kid phone after Ada's grant -> GET /grants lists Ben in shared
                 - if the children read is after apply, TestGrants_ChildrenReadFails fails: ListChildren
                   error -> 500 and zero /control/clients/update
                 - TestGrants_ViewShape extended to (id, child_id, services, clients, shared,
                   started_at, ends_at)
       depends:  t-1

  t-11 Blocked API: unblocked_by from active grants  [risk+verification]
       files:    internal/api/blocked.go, internal/api/blocked_test.go
       desc:     After the 404 check, handleBlocked reads engine List grants and ListChildren for
                 names, passes Self and GrantCovers into blocked.Compute, and emits unblocked_by on
                 every service row. A failed grants or children read degrades to [] annotations with
                 a warn, not an error status.
       covers:   c-6
       contract: - if wiring is missing, TestBlocked_SharedUnblockedBy fails: Ada+Ben hold Kid phone,
                   Ada's active youtube grant -> GET /children/2/blocked lists Kid phone in clients and
                   youtube unblocked_by [{client:"Kid phone", grant_id:<G>, child:{id:1,name:"Ada"}}]
                 - if Self is not passed, TestBlocked_OwnGrantUnnamed fails: GET /children/1/blocked ->
                   youtube unblocked_by []
                 - if both owners don't list it, TestBlocked_BothOwnersListShared fails: Ada's and
                   Ben's views both include Kid phone in clients
                 - if a row can omit the key, TestBlocked_UnblockedByShape fails: every service row
                   has "unblocked_by" as an array
                 - if a grants read failure is fatal, TestBlocked_GrantsReadFails fails: 200 with
                   unblocked_by [] everywhere and states unchanged
                 - TestBlocked_NotFound stays green: unknown child is 404 with 0 AdGuard requests
       depends:  t-1, t-3

  t-12 Children page: enable and label shared clients  [risk+verification+mvp]
       files:    web/src/lib/Children.svelte, web/src/lib/Children.test.ts
       desc:     Owners come from ClientView.children (not child). A client owned by other children
                 is never disabled and is labelled "shared with <other owners, joined ', '>". Ticking
                 it and saving sends it in the PUT. A failed share does not stay optimistically ticked.
       covers:   c-2
       contract: - if still disabled, Children.test sharedEnabled fails: Kid phone children [Ada] ->
                   Ben's Kid phone checkbox enabled, label contains "shared with Ada"; Ada's own label
                   does not contain "shared with Ada"
                 - if names are wrong, Children.test sharedThree fails: owned by Ada & Ben -> Cleo's
                   form shows "shared with Ada, Ben"
                 - if ticking doesn't share, Children.test shareByCheckbox fails: tick + Save -> PUT
                   /api/v1/children/2 clients includes "Kid phone"; after reload (children [Ada, Ben])
                   Ada's label reads "shared with Ben"
                 - if the failure is optimistic, Children.test share502 fails: PUT 502 shows role=alert
                   and after reload Kid phone is unchecked in Ben's form
                 - if the old wording lingers, Children.test fails on any "assigned to" text
       depends:  t-4

  t-13 Tap merge and active-grant row name shared devices  [verification+mvp]
       files:    web/src/lib/tap.ts, web/src/lib/tap.test.ts, web/src/lib/ActiveGrants.svelte,
                 web/src/lib/ActiveGrants.panel.test.ts
       desc:     grantFrom copies created.shared onto the merged grant, so applied and partial
                 outcomes carry it. acceptOffer keeps the target's shared. ActiveGrants renders
                 sharedText(grant.shared) per row.
       covers:   c-5
       contract: - if the 201's shared is dropped, tap.test sharedMerged fails: 201 with shared
                   [{Kid phone,[Ben]}] -> merge() receives a grant with that shared before refresh
                   resolves; applied and partial outcomes both carry it
                 - if the extend path loses it, tap.test acceptKeepsShared fails: an accepted offer's
                   merged grant keeps the target's shared
                 - if the row ignores shared, ActiveGrants.panel.test sharedRow fails: that grant's row
                   contains "Kid phone (shared with Ben)"; shared [] renders nothing extra
       depends:  t-4

  t-14 GrantForm passive shared-device hint  [verification+mvp+risk]
       files:    web/src/lib/GrantForm.svelte, web/src/lib/GrantForm.test.ts,
                 web/src/lib/Buttons.test.ts
       desc:     When value.child_id selects a child with shared devices, render a role=note built
                 from sharedDevices/sharedText. It has no control and no effect on `valid`, and it is
                 re-derived when the selection or the children prop changes. The Buttons editor and
                 Home's ad-hoc form inherit it through GrantForm.
       covers:   c-8
       contract: - if the hint is missing or stale, GrantForm.test hintFollowsChild fails: select Ada ->
                   role=note contains "Kid phone (shared with Ben)"; switch to Cleo or no child -> no
                   role=note; a children prop update re-derives it
                 - if the hint gates submit, GrantForm.test hintPassive fails: with the note shown,
                   child + service + minutes make valid true; the note contains no button/checkbox
                 - if Buttons bypasses it, Buttons.test sharedHint fails: editing a button for Ada
                   shows the note and Save issues one PUT /api/v1/buttons
       depends:  t-4

Wave 3
  t-15 Home: pre-tap hint, post-tap notice, blocked attribution  [verification+mvp; risk's t-13/t-15 merged]
       files:    web/src/lib/Home.svelte, web/src/lib/Home.test.ts
       desc:     Each child section with buttons renders one passive role=note from sharedDevices,
                 referenced by each button's aria-describedby (risk). After an applied/partial tap
                 with shared, a role=status line uses sharedText, shown beside the partial alert
                 and not replacing it. The blocked disclosure keeps services with non-empty
                 unblocked_by even when their state is unblocked, rendering "unblocked on <client> by
                 <child>'s grant".
       covers:   c-5, c-6, c-8
       contract: - if the hint is missing/unlinked, Home.test sharedHint fails: Ada (with a button) and
                   Ben both hold Kid phone -> before any tap Ada's button has accessible description
                   containing "Kid phone (shared with Ben)"; Cleo's has none
                 - if a confirm step creeps in, Home.test noConfirm fails: one click on a hinted tile ->
                   exactly one POST /api/v1/grants, window.confirm never called, no role=dialog
                 - if the notice is missing, Home.test sharedNotice fails: 201 with shared
                   [{Kid phone,[Ben]}] -> role=status contains "Kid phone (shared with Ben)"; shared []
                   -> no status line
                 - if partial overwrites it, Home.test partialShared fails: applied:false + shared ->
                   both the "except on" alert and the shared status are present
                 - if the ad-hoc path skips it, Home.test adhocShared fails: ad-hoc form for Ada shows
                   the GrantForm note and its 201 shows the same status line
                 - if the filter hides it, Home.test unblockedBy fails: Ben's view with youtube state
                   'unblocked' and unblocked_by [{Kid phone, 7, Ada}] lists youtube with "unblocked on
                   Kid phone by Ada's grant"; unblocked with unblocked_by [] stays hidden
       depends:  t-4, t-13, t-14

  t-16 ClientNames shows every owner; drop ClientView.child  [risk+verification]
       files:    web/src/lib/ClientNames.svelte, web/src/lib/ClientNames.test.ts,
                 web/src/lib/api.ts, web/src/lib/api.test.ts
       desc:     ClientNames reads children[]. ClientView.child is deleted from api.ts and typed
                 fixtures that still assert `child` move to `children`. This lands only after t-12,
                 so no consumer reads .child when it is removed.
       covers:   c-2, c-7
       contract: - if ClientNames reads .child, ClientNames.test owners fails: Kid phone row reads
                   "Kid phone — Ada, Ben"
                 - if any consumer still reads .child on a ClientView, make typecheck fails
                 - if the rename leaves a stale key, api.test listDevices fails: the parsed ClientView
                   has children and no child
       depends:  t-12

  t-17 End-to-end shared lifecycle through the API  [risk]
       files:    internal/api/shared_test.go (new)
       desc:     One harness test over the real store, engine and fake AdGuard: share Kid phone via
                 PUT, grant Ada then Ben the same service, check the 201 shared and Ben's blocked view,
                 End Ada, End Ben. Plus a share-during-grant case for c-3 over HTTP.
       covers:   c-1, c-3, c-4, c-5, c-6
       contract: - if any layer drops shared clients, TestShared_Lifecycle fails at the first broken
                   step: PUT /children/2 adds Kid phone -> 200 (no 409) -> Ada youtube grant 201 shared
                   names Ben -> Ben youtube grant 201 -> Ben's blocked view attributes youtube on Kid
                   phone to Ada -> End Ada leaves youtube open on Kid phone -> End Ben re-blocks it
                 - if the HTTP path re-blocks a covered service, TestShared_ShareDuringGrant fails:
                   Ada's youtube grant left Kid phone [tiktok]; Ben holds Kid tablet with own
                   [youtube, roblox]; PUT Ben adding Kid phone -> fake Kid phone blocked exactly
                   [roblox, tiktok] until Ada's grant ends
       depends:  t-5, t-6, t-8, t-10, t-11
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 | t-1, t-8, t-9, t-17 |
| c-2 | t-4, t-9, t-12, t-16 |
| c-3 | t-2, t-6, t-17 |
| c-4 | t-2, t-5, t-10, t-17 |
| c-5 | t-4, t-10, t-13, t-15, t-17 |
| c-6 | t-3, t-4, t-11, t-15, t-17 |
| c-7 | t-1, t-7, t-8, t-9, t-16 |
| c-8 | t-4, t-14, t-15 |

8/8 criteria covered.

## Disagreements

1. **Replace `ClientView.child` with `children[]`, or keep the wire as it is.**
   Risk and verification replace it, with no dual field. Verification adds `children` in wave 1 and
   removes `child` only after its consumers have moved. MVP keeps `child`: Children.svelte derives
   owners from the `children` list it already loads, and ClientNames and POST /clients stay
   single-owner ("acceptable, not in scope").
   *Provisional default:* replace, staged (t-4 adds, t-16 removes).
   *Why it matters:* this choice alone creates t-9 and t-16 and changes POST /clients' stale-mapping
   409. Under MVP's choice, `ownerIndex` stays last-writer-wins, so GET /clients and the rename
   read-back report an arbitrary single owner of a shared client. Risk's own version (drop `child`
   in wave 1) breaks `make typecheck` on Children.svelte:131 and ClientNames.svelte:46 for all of
   wave 2.

2. **Where HTTP-level proof of c-3 lives, and whether an end-to-end task exists.**
   Verification puts it in the children-API task (t-12 TestChildUpdate_ShareKeepsOtherGrant) and
   moves that task to wave 3 behind the adopt change. Risk keeps children API in wave 2 (its
   contract only counts one adopt call) and proves c-3 over HTTP in a separate wave-3 e2e task
   (t-16). MVP has no HTTP-level c-3 test: "t-4 proves the PUT path calls it".
   *Provisional default:* risk's structure. t-8 is in wave 2 and t-17 holds the share-during-grant
   test, written with verification's fixture values.
   *Why it matters:* if t-17 is cut as non-essential (MVP lens), nothing above unit level shows that
   PUT → AdoptClients actually subtracts another owner's live grant. If verification's placement is
   chosen instead, t-17 can be dropped and t-8 moves to wave 3.

3. **Where blocked-view attribution is computed, and its shape.**
   Risk and verification extend the pure `blocked.Compute` with an `unblocked_by` entry per client
   and per grant, and suppress drifted (re-blocked) clients. MVP builds `granted_by:
   [{grant_id, child, clients[]}]` in the handler: "attribution is a grants/store join, not an
   AdGuard fold". Its rule is grant-and-client intersection only, with no live-state check.
   *Provisional default:* pure fold, `unblocked_by` (t-3 + t-11).
   *Why it matters:* c-6 says "unblocked *there* by another child's grant". Only the fold sees each
   client's effective set, so MVP's version would claim a service is unblocked on a client the
   parent has since re-blocked. It also changes the SPA type name and shape that t-4 and t-15 code
   against.

4. **Grant create: when to read children for `shared`.**
   Risk reads ListChildren *after* the grant commits. On failure it logs and answers 201 with
   `shared: []`, because "a committed grant answered as 500 invites a duplicate tap". Verification
   reads it *before* Create, so a failure is a 500 with no AdGuard write. MVP doesn't say.
   *Provisional default:* verification's read-before (t-10 TestGrants_ChildrenReadFails).
   Both avoid a 500 after a commit. Read-before also means a 201 always names the shared
   devices (c-5 is never silently degraded), and the read can replace today's GetChild.
   *Why it matters:* the trade-off is availability against notice fidelity. Under risk's version a
   store hiccup gives a successful grant that silently fails to say it unblocked another child's
   device, which is what the locked `shared_notice` decision guards against.

5. **Separate pure ref-count helper task, and whether engine work needs the schema task.**
   Risk splits `revertPlan`/`heldOpen` into a wave-1 pure task (t-2) and has revert (t-5) and adopt
   (t-6) depend on it *and* on t-1. Verification puts `coveredOn` inside the revert task
   (cover.go), in wave 1 with no dependencies, and adopt waits on it. MVP has no helper, and its
   revert and adopt depend on t-1 because "every backend task needs t-1's schema".
   *Provisional default:* risk's separate helper, but with the t-1 dependency dropped from t-5 and
   t-6. The code shows `Engine.Create` takes clients as an argument and `grant_clients` has no FK
   to `child_clients`, so verification is right that the engine doesn't need the schema change.
   *Why it matters:* the set math (self-counting, per-client, per-service) and the
   freshness/concurrency risks fail differently. The split gives each its own failing test and
   lets revert and adopt run in parallel in wave 2. Verification's layout starts revert a wave
   earlier but leaves adopt serialised behind it.

6. **Store rename inside the schema task, or split out with the engine rename.**
   Verification (t-1, 5 files) and MVP (t-1) put RenameClient/AddChildClient with the migration:
   "once the PK changes, clientOwner's single-row QueryRow is wrong straight away, so the store
   suite is only consistent when both land together". Risk splits store and engine rename into t-7.
   *Provisional default:* risk's split (t-1 / t-7), which keeps both tasks under the 5-file
   threshold.
   *Why it matters:* verification's concern is real but narrower than stated. Between t-1 and t-7,
   RenameClient's self-stale branch (`DELETE ... WHERE client_name = ?`) would delete co-owners'
   rows, and AddChildClient can falsely 409 a co-owner. No existing test exercises a shared rename,
   so the suite stays green. The window is only within the phase, so t-7 must not be dropped or
   deferred.

7. **Migration-offer dedupe in scope.**
   Risk (t-9) and verification (t-12) dedupe `computeOffer` and report the lowest-id owner. MVP
   doesn't touch migration ("ClientNames/MigrationBanner keep working; they show one owner").
   *Provisional default:* include, in t-8.
   *Why it matters:* no criterion names it, but `computeOffer` adds every owner's clients to
   `mapped`. Once c-1 lands, a shared global-list client is offered twice and POST /migration
   writes it twice. Leaving it out ships a regression that the phase's own change causes.

8. **Wire shape of `shared`.**
   Risk and verification use `{client, others: [{id, name}]}`. MVP uses `{client, owners: string[]}`.
   *Provisional default:* `others: childRef[]`.
   *Why it matters:* this is a one-time contract choice that t-4, t-10, t-13 and t-15 all code
   against. IDs let the SPA exclude or link children without matching on names. The cost is a
   slightly larger payload.

9. **Home: one task or two.**
   Risk splits blocked-view attribution (t-15) from hint and notice (t-13), and serialises them
   only because both edit Home.svelte. Verification (t-13) and MVP (t-9) use one Home task.
   *Provisional default:* one task (t-15). A 2-file task that exists only to serialise on the same
   file fails the granularity rule.
   *Why it matters:* little, either way it is the last wave. Merging drops one review and commit
   cycle. Splitting would isolate the `shown()` filter change if it regresses.

10. **`shared_with` on blocked-view clients.**
    Only risk adds `clients[].shared_with` to the blocked response (t-3 TestCompute_SharedWith) and
    renders "(shared with …)" on Home. Verification and MVP rely on the shared client appearing in
    `clients`, which it already does once mapped.
    *Provisional default:* left out. c-6 requires the device to be listed and the grant attributed,
    not a shared label.
    *Why it matters:* low. Adding it later costs a field on t-3/t-11 and one render line in t-15.
    Leaving it out means a parent learns the device is shared only from `unblocked_by` or the
    Children page.

11. **Helper location and one wording versus several.**
    Risk adds a new `web/src/lib/shared.ts` with a single `sharedText` used by the hint, the notice
    and the row ("TV (shared with Ben)"), and links the Home hint via aria-describedby. MVP and
    verification put `sharedDevices` in the existing `grants.ts` and write a different string at
    each site ("Kid phone is shared with Ben", "Also unlocked on Kid phone — shared with Ben",
    "also on …").
    *Provisional default:* `grants.ts` (existing file) holds both `sharedDevices` and risk's single
    `sharedText`. Contracts assert that the sharedText output is contained in the hint, notice and
    row, and the surrounding copy is free.
    *Why it matters:* low for behaviour. It decides whether a wording change is one edit or three,
    and whether the frontend contracts pin exact strings.
