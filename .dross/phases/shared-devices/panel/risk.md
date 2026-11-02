# shared-devices — risk-lens draft

Lens: failure modes drive the graph. Each task owns one cluster of risks and the
test that proves it; no risk is tested in two places.

Where the one-client-one-child rule lives today (all of it must go, nothing else):

- schema: `child_clients.client_name TEXT PRIMARY KEY` (internal/store/migrations/0002_children.sql)
- store: `checkConflicts` client loop (CreateChild/UpdateChild) and `clientOwner` single-row
  `QueryRow` (RenameClient) in internal/store/children.go / clients.go
- api: `updateConflict` client loop (internal/api/children.go) is what answers the 409
  before AdGuard is touched; `ownerIndex` is last-writer-wins (internal/api/clients.go)
- engine: `checkRename` keeps one `oldOwner`/`newOwner` pointer (internal/grants/rename.go)
- UI: `disabled={pending || other !== null}` + "assigned to" (web/src/lib/Children.svelte)

Risk register (what can break, and who owns it):

| # | Risk | Owner |
|---|------|-------|
| R1 | Table-rebuild migration of `child_clients` loses rows, drops the `ON DELETE CASCADE` FK or the `child_id` index, or leaves the old single-column PK in place | t-1 |
| R2 | Removing a client from one child (PUT replace) or deleting one owner wipes the other owners' rows (DELETE keyed on client_name instead of child_id) | t-1 |
| R3 | Ref-count set math wrong: the reverting grant counts itself as "another grant", a grant on a *different* client or *different* service holds the block open, or an empty per-client set still produces a write | t-2 |
| R4 | Another owner's live grant on a shared client is cut short by End / timer expiry / reconcile revert (the locked shared_device_rule) | t-5 |
| R5 | Reconcile computes "still covered" from the pass-start snapshot: two overdue grants on the same shared client each see the other as active, both skip, and the service is never re-blocked | t-5 |
| R6 | The active-grants read inside revert fails and revert fails *open* (re-blocks everything, cutting another child's grant) or fails *silently* (marks expired without re-blocking) | t-5 |
| R7 | Ref-count keyed on current ownership instead of the grant's stored clients: a client removed from the child after the grant still has to be re-blocked by that grant | t-2 |
| R8 | Sharing a client into child B re-blocks a service another owner's active grant has open on it (adopt writes B's baseline wholesale) | t-6 |
| R9 | Baseline poisoned through a shared sibling: sibling S is open for netflix only because child A's grant covers it, so a *new* device of B never gets netflix and stays open after A's grant ends | t-6 |
| R10 | Rename of a shared client hits the composite PK when one owner holds a stale mapping of the new name, or moves only one owner's row (single-row `clientOwner`) | t-7 |
| R11 | Engine `checkRename` keeps a single owner pointer, so a client owned by A and B where A also holds a stale new-name is falsely rejected — or a new-name held by an unrelated child C is silently merged in | t-7 |
| R12 | PUT still 409s via the API pre-check (`updateConflict`) even after the store allows sharing; or removal-only / DELETE starts calling AdGuard | t-8 |
| R13 | `ownerIndex` is last-writer-wins: GET /clients and the rename read-back show one owner of a shared client, so the UI cannot say "shared with" | t-9 |
| R14 | Migration offer folds the mapped list without dedupe: a shared global-list client is offered twice and `MigrateFromGlobal` POSTed twice | t-9 |
| R15 | A stale mapping of another child still lets POST /clients create-and-assign a new AdGuard client into a silent share (prior locked 409) | t-9 |
| R16 | Grant 201 / list `shared` wrong: includes the granting child, lists unshared clients, or a ListChildren failure *after* the grant committed turns a created grant into a 500 | t-10 |
| R17 | Per-child overlap index (`grant_services_live_idx`) is mistaken for per-client: B's grant for a service A already has open on the shared TV must still be 201, not 409 | t-10 |
| R18 | Blocked-view attribution wrong: own grant listed as "another child's", a grant covering a client this child doesn't own leaks in, or a drifted (re-blocked) service is still claimed unblocked | t-3 |
| R19 | Blocked view 500s when the grants read fails although the AdGuard-derived states are fine | t-11 |
| R20 | Frontend wire drift: `child` → `children` rename leaves a consumer reading `c.child` (undefined), or a missing `shared` / `unblocked_by` key throws | t-4 |
| R21 | Children page: shared client still disabled, the label names the viewing child itself, or a failed share (502 from adopt) leaves the box optimistically ticked | t-12 |
| R22 | Tap merges the 201 without `shared`, so the post-tap notice / active-row is blank until the next poll; or the hint grows a confirm step and a tap needs two clicks (locked shared_notice) | t-13 |
| R23 | GrantForm hint stale after the child selection changes; active-grant row drops the shared devices | t-14 |
| R24 | Home blocked disclosure filters `state === 'unblocked'` out, so a service open only on the shared device (by another child's grant) is never shown | t-15 |
| R25 | Units pass but layers don't compose: API passes the child's clients (shared included) to the engine, engine ref-counts over the real store, view reflects it | t-16 |

Pinned wire contract (t-4 and the UI tasks code against this; the backend tasks must produce it):

```
childRef = {id, name}
GET  /api/v1/clients        clientView.child  (single)  -> clientView.children: childRef[]  (id ASC, never null)
POST /api/v1/clients 201, POST /api/v1/clients/rename 200: same clientView shape
sharedRef = {client: string, others: childRef[]}   // others: current owners minus the grant's/view's child, id ASC
POST /api/v1/grants 201     {id, ends_at, applied, failed, shared: sharedRef[]}   // client ASC, only clients with >=1 other owner
GET  /api/v1/grants         grantView gains shared: sharedRef[]
GET  /api/v1/children/{id}/blocked
     clients[]  gains shared_with: childRef[]   (other owners)
     services[] gains unblocked_by: [{client, grant_id, child: childRef}]  (never null; other children's active
                grants covering this service on a mapped client that does not block it live)
GET/POST /api/v1/migration  shape unchanged; each client once, child = lowest-id owner
PUT  /api/v1/children/{id}  shape unchanged; no client-ownership 409
```

```
Phase shared-devices — 16 tasks across 3 waves

Wave 1
  t-1  Rebuild child_clients with composite key
       files:    internal/store/migrations/0005_shared_clients.sql (new),
                 internal/store/children.go, internal/store/children_test.go,
                 internal/store/store_test.go
       desc:     Migration rebuilds child_clients with PRIMARY KEY (client_name, child_id), same FK
                 (ON DELETE CASCADE) and child_id index, copying every row. checkConflicts keeps only
                 the name check; Create/UpdateChild accept a client another child holds; UpdateChild
                 and DeleteChild touch only their own child_id rows.
       covers:   c-1, c-7
       contract: - if the rebuild drops rows, TestMigrate_0005PreservesClients fails: a DB migrated to 0004
                   with Ada->[Kid phone], Ben->[Kid tablet] holds exactly those rows after 0005
                 - if the FK/cascade is lost in the rebuild, TestSchema_ChildClientsCascade fails:
                   DELETE FROM children leaves no orphan child_clients rows; the child_id index exists
                 - if the client check survives, TestUpdateChild_SharedClient fails: Ben PUT [Kid tablet]
                   while Ada holds it returns no *ErrClientTaken and ListChildren shows it under both
                 - if UpdateChild deletes by client_name, TestUpdateChild_RemoveKeepsOtherOwner fails:
                   Ada drops Kid tablet; Ben still holds it
                 - if cascade is by client, TestDeleteChild_KeepsOtherOwner fails: delete Ada; Ben still
                   holds Kid tablet
                 - if the composite key is missing, TestSchema_ChildClientsPK fails: a raw duplicate
                   INSERT (Kid tablet, Ada) errors with a constraint violation
       depends:  —

  t-2  Pure ref-count helpers for shared clients
       files:    internal/grants/shared.go (new), internal/grants/shared_test.go (new)
       desc:     revertPlan(g, active []store.Grant) -> map[client][]service: g's services minus those
                 covered by another active grant (id != g.ID) whose stored Clients include that client;
                 clients left with nothing are omitted. heldOpen(active, client) -> services any active
                 grant covers on client. Both keyed on stored grant clients, never ownership.
       covers:   c-3, c-4
       contract: - if g counts itself, TestRevertPlan_SelfNotCounted fails: g alone in active -> every
                   client gets all of g's services
                 - if coverage is per-grant not per-client, TestRevertPlan_OtherClient fails: B's netflix
                   grant on Ben phone does not hold netflix open on TV
                 - if coverage is per-grant not per-service, TestRevertPlan_OtherService fails: B's
                   youtube grant on TV leaves netflix in TV's plan
                 - if the empty case writes, TestRevertPlan_FullyHeld omits TV from the map entirely when
                   B's grant covers every one of g's services on TV
                 - if ownership is consulted, TestRevertPlan_StoredClients fails: plan uses g.Clients
                   verbatim, including a client no child now owns
                 - if heldOpen unions across clients, TestHeldOpen fails: heldOpen(active, "TV") returns
                   only services of grants whose Clients contain TV, sorted, never nil
       depends:  —

  t-3  Blocked fold: shared_with and unblocked_by
       files:    internal/blocked/blocked.go, internal/blocked/blocked_test.go
       desc:     Input gains ChildID, Owners map[client][]Ref and Grants []GrantRef{ID, Child Ref,
                 Services, Clients}. ClientState gains SharedWith (other owners); ServiceState gains
                 UnblockedBy: other children's active grants covering the service on a present mapped
                 client whose effective set does not contain it.
       covers:   c-6
       contract: - if attribution is missing, TestCompute_UnblockedByOtherChild fails: Ben's view, Ada's
                   grant 7 covers netflix on TV, TV doesn't block it -> netflix UnblockedBy
                   [{TV, 7, Ada}]
                 - if own grants leak in, TestCompute_OwnGrantNotAttributed fails: Ben's own grant on TV
                   yields UnblockedBy []
                 - if drift is ignored, TestCompute_DriftedNotAttributed fails: Ada's grant covers netflix
                   on TV but TV blocks it live -> UnblockedBy [] and state blocked
                 - if foreign clients leak, TestCompute_UnmappedClient fails: Ada's grant on Ada phone
                   never appears in Ben's view
                 - if SharedWith includes self or nil, TestCompute_SharedWith fails: TV owned by Ada, Ben
                   -> Ben's TV SharedWith [Ada]; unshared Ben phone SharedWith [] (not nil)
       depends:  —

  t-4  SPA wire types and shared-device helper
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/lib/shared.ts (new),
                 web/src/lib/shared.test.ts (new)
       desc:     Per the pinned contract: ClientView.children replaces child; Grant/GrantCreated gain
                 shared; BlockedClient.shared_with; ServiceState.unblocked_by — all defaulted to [] when
                 absent. shared.ts: sharedDevices(children, childId) -> SharedRef[] and sharedText(refs)
                 (the one wording used by hint, notice and row).
       covers:   c-2, c-5, c-6, c-8
       contract: - if defaults are missing, api.test listGrants/createGrant/childBlocked fail: a response
                   without shared / unblocked_by / shared_with yields [] with no TypeError
                 - if the rename is half done, api.test listClients fails: {children:[{id:1,name:"Ada"}]}
                   maps to children, and the type has no `child` key (tsc catches stale readers)
                 - if sharedDevices includes the child itself, shared.test fails: Ada & Ben own TV ->
                   sharedDevices(…, Ada) = [{client:"TV", others:[Ben]}]; an unshared client is absent
                 - if ordering is unstable, shared.test order fails: clients ASC, others id ASC across two
                   shuffled inputs
                 - if wording forks, shared.test sharedText fails: [{TV,[Ben,Cy]}] -> "TV (shared with Ben,
                   Cy)" and [] -> ""
       depends:  —

Wave 2 (depends t-1..t-4)
  t-5  Ref-counted revert in End, expiry, reconcile
       files:    internal/grants/grants.go, internal/grants/grants_test.go,
                 internal/grants/reconcile.go, internal/grants/reconcile_test.go
       desc:     revert re-reads ListActiveGrants under the mutex on every call and writes
                 AddBlockedServices(client, revertPlan[client]) only; a failed read fails every client
                 (grant stays active, retried). Reconcile's overdue loop goes through the same revert, so
                 each expiry sees the statuses the previous one committed.
       covers:   c-4
       contract: - if End ignores other owners, TestEnd_SharedHeldOpen fails: Ada & Ben grants for youtube
                   both on Kid tablet; End(Ada) re-blocks youtube on Kid phone, zero update POSTs for Kid
                   tablet, and Kid tablet still lacks youtube
                 - if the last grant doesn't re-block, TestEnd_SharedLastOut fails: then End(Ben) -> Kid
                   tablet blocks youtube again
                 - if reconcile uses its snapshot, TestReconcile_TwoOverdueShared fails: both grants past
                   ends_at in one pass -> both expired AND Kid tablet blocks youtube after the pass
                 - if timers race, TestExpire_SharedTimersSerial fails: firing both expiry callbacks
                   concurrently ends with youtube blocked on Kid tablet and MaxInFlightUpdates() == 1
                 - if the read fails open, TestEnd_ActiveReadFails fails: store ListActiveGrants error ->
                   *ErrRevertFailed, zero AdGuard update POSTs, grant still active
                 - if ownership is used, TestExpire_ClientUnassigned fails: Kid tablet removed from Ada
                   after her grant; expiry still re-blocks it
       depends:  t-1, t-2

  t-6  Shared-aware baseline for adopt and create
       files:    internal/grants/clients.go, internal/grants/clients_test.go
       desc:     baselineFrom also unions the services of any active grant whose clients include a
                 sibling. AdoptClients writes, per added client, baseline minus heldOpen(client); set-union,
                 nothing removed; no write when nothing changes. CreateClient uses the same baseline.
       covers:   c-3
       contract: - if adopt re-blocks, TestAdopt_SharedKeepsOtherGrant fails: Ada's youtube grant open on
                   Kid tablet; adopting Kid tablet into Ben (siblings block youtube, tiktok) writes tiktok
                   but not youtube, and Ada's grant still reverts it later
                 - if union becomes replace, TestAdopt_SharedUnion fails: Kid tablet keeps every service it
                   blocked before the share
                 - if the sibling is poisoned, TestBaseline_SharedSiblingGrant fails: Ben's sibling Kid
                   tablet is open for youtube only via Ada's grant; CreateClient("Ben tv", child Ben)
                   sends blocked_services containing youtube
                 - if the subtraction is global, TestAdopt_TwoAdded fails: adding TV (held open by Ada) and
                   Ben tv together -> youtube written to Ben tv, not to TV
                 - if a no-op writes, TestAdopt_SharedNoChange fails: share where baseline ⊆ current ->
                   zero update POSTs
       depends:  t-1, t-2

  t-7  Multi-owner rename in store and engine
       files:    internal/store/clients.go, internal/store/clients_test.go,
                 internal/grants/rename.go, internal/grants/rename_test.go
       desc:     Store RenameClient moves every owner's row: owners that already hold newName drop the old
                 row, others are updated; *ErrClientTaken only when a child holds newName but not oldName.
                 checkRename uses owner sets with the same rule. AdGuard is renamed exactly once.
       covers:   c-7
       contract: - if one row moves, TestRenameClient_SharedAllOwners fails: TV owned by Ada & Ben renamed
                   to "Family TV" -> both hold "Family TV", neither holds "TV"
                 - if the PK collides, TestRenameClient_SharedStaleOneOwner fails: Ada also holds stale
                   "Family TV" -> nil error, Ada has one "Family TV" row, Ben has one
                 - if merges are silent, TestRenameClient_SharedTakenByOutsider fails: Cy holds stale
                   "Family TV" -> *ErrClientTaken naming Cy, both tables unchanged
                 - if checkRename keeps one pointer, TestEngineRename_SharedStale fails: same setup as the
                   stale case through the engine -> nil, and the fake saw exactly one clients/update
                 - if the outsider check is lost in the engine, TestEngineRename_Outsider fails: zero
                   AdGuard writes and *store.ErrClientTaken
       depends:  t-1

  t-8  Children API: share without 409, remove quietly
       files:    internal/api/children.go, internal/api/children_test.go
       desc:     updateConflict keeps only the name check; added = in.Clients ∖ cur.Clients still go to
                 AdoptClients (shared ones included). Removal-only PUTs and DELETE never call AdGuard.
       covers:   c-1, c-7
       contract: - if the pre-check survives, TestChildUpdate_ShareNo409 fails: PUT Ben {clients:[Kid
                   tablet]} while Ada holds it -> 200, and GET /children lists Kid tablet under both
                 - if a shared add skips adopt, TestChildUpdate_ShareAdopts fails: that PUT issues exactly
                   one clients/update for Kid tablet
                 - if removal writes AdGuard, TestChildUpdate_RemoveSharedNoAdGuard fails: Ada PUT without
                   Kid tablet -> zero AdGuard requests, Ben still lists it
                 - if delete writes AdGuard, TestChildDelete_SharedNoAdGuard fails: DELETE Ada -> 204,
                   zero AdGuard requests, Ben still lists Kid tablet
                 - if the name check is lost, TestChildUpdate_NameStill409 fails: PUT Ben {name:"ada"} ->
                   409 with zero AdGuard requests
       depends:  t-1

  t-9  Clients view, migration, rename: multi-owner
       files:    internal/api/clients.go, internal/api/clients_test.go, internal/api/migration.go,
                 internal/api/migration_test.go, internal/api/rename.go
       desc:     ownerIndex -> map[client][]childRef (id ASC); clientView.children replaces child in GET
                 /clients, POST /clients and rename read-back. POST /clients keeps its 409 when any child
                 other than child_id holds the name. Migration dedupes mapped names; child = lowest-id owner.
       covers:   c-2, c-7
       contract: - if last-writer-wins remains, TestClients_SharedOwners fails: Kid tablet owned by Ada &
                   Ben -> children [{1,Ada},{2,Ben}]; unowned client -> [] not null
                 - if the stale guard is lost, TestCreateClient_StaleOtherOwner409 fails: Ben's stale
                   "TV", POST {name:"TV", child_id: Ada} -> 409 with zero clients/add
                 - if migration doesn't dedupe, TestMigration_SharedOnce fails: shared global-list client
                   appears once in GET and POST issues one update for it
                 - if rename read-back is single, TestRename_SharedReadBack fails: 200 body children lists
                   both owners
       depends:  t-1

  t-10 Grant responses name shared devices
       files:    internal/api/grants.go, internal/api/grants_test.go
       desc:     sharedOf(grant, children) builds sharedRef[] from the grant's stored clients and current
                 owners minus grant.child_id. Added to the 201 and every grantView. A ListChildren
                 failure after the grant committed logs and answers shared [] (never 500).
       covers:   c-4, c-5
       contract: - if shared clients aren't passed, TestGrantCreate_SharedClientsIncluded fails: Ada owns
                   Kid phone + shared Kid tablet -> the stored grant's clients are both, and youtube is
                   removed from both in the fake
                 - if shared is wrong, TestGrantCreate_SharedBody fails: 201 shared ==
                   [{client:"Kid tablet", others:[{2,"Ben"}]}]; Kid phone absent; Ada absent from others
                 - if overlap is per-client, TestGrantCreate_SecondOwnerSameService fails: Ben's youtube
                   grant while Ada's covers Kid tablet -> 201, not 409
                 - if the read-back 500s, TestGrantCreate_SharedReadFails fails: children read error after
                   create -> 201 with shared [] and the grant listed
                 - if the list omits it, TestGrantsList_Shared fails: GET /grants entry carries the same
                   shared; a grant whose child was deleted lists every current owner as others
       depends:  t-1

  t-11 Blocked API: shared devices and attribution
       files:    internal/api/blocked.go, internal/api/blocked_test.go
       desc:     handleBlocked passes ListChildren owners and engine List grants (with child names) to
                 blocked.Compute and emits shared_with / unblocked_by. A grants or children read failure
                 degrades to [] annotations with a warn, not an error status.
       covers:   c-6
       contract: - if wiring is missing, TestBlocked_SharedAttribution fails: Ada's youtube grant on shared
                   Kid tablet -> Ben's view has Kid tablet with shared_with [Ada] and youtube unblocked_by
                   [{client:"Kid tablet", grant_id, child:{1,"Ada"}}]
                 - if both owners don't list it, TestBlocked_BothOwnersListShared fails: Ada's and Ben's
                   views both include Kid tablet in clients
                 - if a grants read failure is fatal, TestBlocked_GrantsReadFails fails: 200 with
                   unblocked_by [] everywhere and states unchanged
       depends:  t-1, t-3

  t-12 Children page: enable and label shared clients
       files:    web/src/lib/Children.svelte, web/src/lib/Children.test.ts,
                 web/src/lib/ClientNames.svelte, web/src/lib/ClientNames.test.ts
       desc:     A client owned by other children is enabled and labelled "shared with <names>" (other
                 owners only); ticking + Save sends it in the PUT. ClientNames shows every owner.
       covers:   c-2
       contract: - if still disabled, Children.test sharedEnabled fails: Kid tablet owned by Ben is an
                   enabled checkbox in Ada's form with text "shared with Ben"
                 - if self is named, Children.test sharedLabelExcludesSelf fails: owned by Ada & Ben ->
                   Ada's form says "shared with Ben", Cy's says "shared with Ada, Ben"
                 - if ticking doesn't share, Children.test shareSave fails: tick + Save PUTs clients
                   including "Kid tablet"; after reload it is checked in both Ada's and Ben's forms
                 - if the failure is optimistic, Children.test share502 fails: PUT 502 shows role=alert and
                   after reload Kid tablet is unchecked in Ada's form
                 - if ClientNames reads .child, ClientNames.test owners fails: row reads "Kid tablet — Ada,
                   Ben"
       depends:  t-4

  t-13 Home: pre-tap hint and post-tap notice
       files:    web/src/lib/tap.ts, web/src/lib/tap.test.ts, web/src/lib/Home.svelte,
                 web/src/lib/Home.test.ts
       desc:     grantFrom carries created.shared into the merged grant; applied/partial outcomes with
                 shared set a child note via sharedText. Each child section with sharedDevices renders one
                 passive hint element referenced by every one of its buttons' aria-describedby.
       covers:   c-5, c-8
       contract: - if merge drops it, tap.test mergeShared fails: 201 {shared:[TV/Ben]} -> merged grant has
                   that shared before refresh resolves
                 - if the notice is silent, Home.test appliedSharedNote fails: applied tap with shared ->
                   note "Unlocked — also on TV (shared with Ben)"; shared [] -> no note
                 - if partial loses it, Home.test partialSharedNote fails: failed [Kid phone] + shared TV ->
                   note names both
                 - if the hint is missing/unlinked, Home.test buttonHint fails: Ada's button has accessible
                   description "TV (shared with Ben)"; Cy's (no shared) has none
                 - if a confirm step creeps in, Home.test hintNoConfirm fails: one click on a hinted button
                   -> exactly one POST /grants, window.confirm never called, no role=dialog rendered
       depends:  t-4

  t-14 GrantForm hint and active-grant shared row
       files:    web/src/lib/GrantForm.svelte, web/src/lib/GrantForm.test.ts,
                 web/src/lib/ActiveGrants.svelte, web/src/lib/ActiveGrants.panel.test.ts
       desc:     GrantForm derives sharedDevices from the selected child and renders a passive hint (Home
                 adhoc form and Buttons editor). ActiveGrants rows render grant.shared via sharedText.
       covers:   c-5, c-8
       contract: - if the hint is stale, GrantForm.test hintFollowsChild fails: select Ada -> "TV (shared
                   with Ben)"; switch to Cy -> no hint; children prop update re-derives it
                 - if the form blocks, GrantForm.test hintPassive fails: valid stays true and no extra
                   control is added when the hint is shown
                 - if the row drops it, ActiveGrants.panel.test sharedRow fails: grant with shared TV/Ben
                   renders "TV (shared with Ben)" in its row; shared [] renders nothing extra
       depends:  t-4

Wave 3
  t-15 Home blocked view: show attribution
       files:    web/src/lib/Home.svelte, web/src/lib/Home.test.ts
       desc:     Blocked disclosure keeps services whose unblocked_by is non-empty even when state is
                 unblocked, rendering "unblocked on <client> by <child>'s grant"; clients with shared_with
                 are marked "(shared with …)".
       covers:   c-6
       contract: - if shown() still filters, Home.test unblockedByShown fails: youtube state unblocked with
                   unblocked_by [{Kid tablet, 7, Ada}] is rendered in Ben's disclosure
                 - if the child isn't named, Home.test attributionText fails: text "unblocked on Kid tablet
                   by Ada's grant"
                 - if plain unblocked leaks, Home.test plainUnblockedHidden fails: unblocked with
                   unblocked_by [] stays hidden
       depends:  t-4, t-13 (same file: serialised)

  t-16 End-to-end shared lifecycle through the API
       files:    internal/api/shared_test.go (new)
       desc:     One harness test over real store, engine and fake AdGuard: share Kid tablet via PUT, grant
                 Ada then Ben the same service, check 201 shared and Ben's blocked view, End Ada, End Ben.
       covers:   c-1, c-3, c-4, c-6
       contract: - if any layer drops shared clients, TestShared_Lifecycle fails at the first broken step:
                   PUT 200 (no 409) -> Ada grant 201 names Ben -> Ben grant 201 -> End Ada leaves youtube
                   open on Kid tablet and blocked on Kid phone -> End Ben re-blocks Kid tablet
                 - if adopt ignores a live grant, TestShared_ShareDuringGrant fails: Ada's grant open on
                   Kid tablet, then PUT shares it to Ben -> youtube still absent from Kid tablet's
                   blocked_services until Ada's grant ends
       depends:  t-5, t-6, t-8, t-10, t-11
```

## Coverage

| Criterion | Tasks |
|-----------|-------|
| c-1 | t-1, t-8, t-16 |
| c-2 | t-4, t-9, t-12 |
| c-3 | t-2, t-6, t-16 |
| c-4 | t-2, t-5, t-10, t-16 |
| c-5 | t-4, t-10, t-13, t-14 |
| c-6 | t-3, t-4, t-11, t-15, t-16 |
| c-7 | t-1, t-7, t-8, t-9 |
| c-8 | t-4, t-13, t-14 |

8/8 criteria covered.

## Judgment calls

- Composite PK `(client_name, child_id)` via table rebuild, over a separate `client_shares` table: one source of ownership keeps every existing `JOIN child_clients` query correct, and the rebuild risk is boxed into t-1's migration test.
- Ref-count derived live from `grant_clients` of active grants, over a persisted counter column: a counter drifts on any failed write, while the derived set comes from the same rows revert already trusts (and that rename keeps current).
- Revert fails closed (grant stays active, retried) when the active-grants read fails, over re-blocking everything: re-blocking would break the locked shared_device_rule on a transient store error. Reconcile retries anyway.
- Revert re-reads active grants on each call, over sharing the reconcile pass's snapshot: the snapshot is R5's root cause (two overdue grants hold each other open forever).
- Baseline adds services of *any* active grant covering a sibling, then subtracts heldOpen per added client, over only subtracting: subtraction alone still lets R9 leak a new device open via a shared sibling.
- Pure helpers (t-2) split from engine wiring (t-5, t-6): the set math and the concurrency/read-freshness risks fail differently and each needs its own failing test.
- Rename onto a name held only by an unrelated child stays `*ErrClientTaken`, over merging into a share: a stale mapping must not silently grant a child a device. Same reason POST /clients keeps its stale-owner 409.
- Sharing also allowed on POST /children (store-level), over PUT-only: one ownership rule in the store; the API create path never adopted baselines before and still doesn't.
- `clientView.child` replaced by `children[]` (breaking), over keeping `child` plus a new field: a lingering single-owner field is exactly the last-writer-wins bug (R13); tsc flags every stale reader.
- Migration offer keeps its single `child` (lowest-id owner) and only dedupes, over a `children[]` there: no criterion asks for it and MigrationBanner stays untouched.
- `shared` computed server-side for grants (c-5 requires the 201 to carry it), but the pre-tap hint computed client-side from the children list already loaded: no new endpoint for a passive hint; one `sharedText` keeps the wording identical.
- `shared` and blocked attribution reflect *current* owners, over owners at grant time: no schema for history, and the parent needs to know who shares the device now.
- Read-back failures after a committed write (grant 201 shared, blocked annotations) degrade to `[]` with a warn, over an error status: a committed grant answered as 500 invites a duplicate tap.
- Home hint once per child section linked via aria-describedby to each button, over a line under every button: the hint stays readable, and each button still carries it accessibly.
- t-15 serialised after t-13 only because both edit Home.svelte; t-16 kept as a separate wiring owner so the unit tasks don't each re-test the full stack.
