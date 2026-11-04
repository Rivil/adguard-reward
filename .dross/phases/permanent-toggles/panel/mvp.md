# permanent-toggles — MVP lens (smallest task set)

Wire conventions (fixed here so the SPA task needs no backend output):
- `POST /api/v1/children/{id}/toggles` body `{"service":"<id>","state":"blocked"|"unblocked"}`
  -> 200 `{"service","state","applied":bool,"failed":[client...],"shared":[sharedRef...]}`
  (`shared` = existing `sharedOf(child.Clients, child.ID, children)`; `failed` never null).
  Unknown child 404; bad state / unknown built-in id / unknown `custom:<n>` 400.
- `GET /api/v1/children/{id}/blocked` services gain `"permanent": "blocked"|"unblocked"|null` (this child's
  stored intent); `unblocked_by[]` entries gain `"permanent": bool` (true = another owner's permanent unblock
  on a shared client, `grant_id` 0).
- Store: `service_toggles(child_id FK children ON DELETE CASCADE, service_id TEXT, state CHECK IN
  ('blocked','unblocked'), PRIMARY KEY(child_id, service_id))`. A toggle's clients are the child's
  *current* mapping (not snapshotted). "Open" on a client = any owner of that client has `unblocked`.

```
Phase permanent-toggles — 6 tasks across 4 waves

Wave 1
  t-1  Store toggles, trim grants on block
       files:    internal/store/migrations/0008_service_toggles.sql (new), internal/store/toggles.go (new),
                 internal/store/toggles_test.go (new), internal/store/custom_services_test.go
       desc:     SetToggle(ctx, childID, serviceID, state) upserts intent; for state=blocked the same tx deletes the
                 child's active grant_services row for that service and marks a grant left with none 'ended'
                 (ended_at set), returning {Trimmed, Ended []int64}. ListToggles(ctx) []Toggle{ChildID,ServiceID,State}.
                 Update the "0007 last" assertion in custom_services_test.go to 0008.
       covers:   c-2, c-4
       contract: if intent does not persist, TestToggles_Persist fails: SetToggle(1,"youtube",unblocked), Close,
                 store.Open same dir -> ListToggles == [{1,youtube,unblocked}]; a second SetToggle(…,blocked)
                 leaves exactly one row with state blocked
                 if the CHECK is missing, TestToggles_BadState fails: state "open" -> error, no row
                 if trimming is not atomic/scoped, TestToggles_BlockTrimsGrant fails: active grant 5 {youtube,tiktok}
                 for child 1 -> SetToggle(1,youtube,blocked) returns Trimmed=[5], grant 5 services == [tiktok],
                 status active; child 2's active grant on youtube untouched
                 if an emptied grant stays active, TestToggles_BlockEndsEmptyGrant fails: grant 6 {youtube} ->
                 Ended=[6], GetGrant(6).Status == ended, ListActiveGrants excludes it, and a new CreateGrant(1,
                 youtube) succeeds (partial unique index freed)
                 if the FK is missing, TestToggles_ChildCascade fails: DeleteChild(1) -> ListToggles empty
                 if the migration is out of order, TestCustomServices migration check fails unless 0008 is last

  t-2  Fold permanent intent into blocked view
       files:    internal/blocked/blocked.go, internal/blocked/blocked_test.go
       desc:     Input gains Toggles []Toggle{Child ChildRef, Service, State}. ServiceState gains Permanent (self's
                 intent, "" when none). A custom service is unblocked on a client if a grant covers it OR any owner
                 of that client has it unblocked. Unblock gains Permanent bool; another owner's unblock on a shared
                 client yields {Client, GrantID:0, Child, Permanent:true}.
       covers:   c-5, c-1
       contract: if self intent is not surfaced, TestCompute_PermanentSelf fails: toggle {self,youtube,unblocked}
                 with youtube absent from every client -> State unblocked, Permanent "unblocked"; no toggle ->
                 Permanent ""
                 if custom state ignores permanent unblocks, TestCompute_CustomPermanent fails: custom:1, no grants,
                 toggle {self,custom:1,unblocked} -> unblocked; toggle {self,custom:1,blocked} -> blocked
                 if any-owner-opens is wrong, TestCompute_SharedPermanent fails: tv owned by self+Bo, Bo has custom:1
                 unblocked -> on self's view tv is in Differs and UnblockedBy == [{tv,0,Bo,Permanent:true}];
                 self's own phone still blocks it (state partial)
                 if a non-owner's toggle leaks, TestCompute_ForeignToggle fails: toggle by a child owning none of the
                 mapped clients changes no state and adds no UnblockedBy

  t-3  SPA toggles, permanent rows, block picker
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/lib/Home.svelte, web/src/lib/Home.test.ts
       desc:     api.ts: setToggle(childId, service, state) + ServiceState.permanent + Unblock.permanent (fetcher
                 fills null/false). Home: shown() keeps permanent==='unblocked' rows; blocked/partial rows get an
                 "Unblock" control, unblocked/partial rows a "Block" control; openOn renders "<client> by <child>
                 (permanent)"; a "Block a service…" control at the foot opens a search input over listServices()
                 minus state==='blocked' ids. After a toggle: refetch every open blocked view, refresh() active
                 grants, show failed as a partial note and shared via sharedText.
       covers:   c-1, c-2, c-5
       contract: if the client hits the wrong endpoint, api.test 'setToggle' fails: setToggle(3,'custom:2',
                 'blocked') -> POST /api/v1/children/3/toggles body {"service":"custom:2","state":"blocked"} with
                 the CSRF header; a response without permanent fields reads back permanent null / false
                 if permanently unblocked rows stay hidden, Home.test 'permanent row' fails: view with
                 {youtube, state unblocked, permanent 'unblocked', unblocked_by []} renders a youtube row with a
                 Block button; a plain unblocked service with no permanent and no unblocked_by is still hidden
                 if a row control is wrong, Home.test 'row toggles' fails: blocked row shows only Unblock and
                 clicking it POSTs state 'unblocked'; partial row shows both; unblocked-permanent row shows only Block
                 if the picker includes blocked services or misses custom ones, Home.test 'block picker' fails:
                 catalogue [youtube(blocked), tiktok(unblocked), custom:1 'Portal'(partial)]; typing "port" lists
                 only Portal, youtube never listed; picking it POSTs {service:'custom:1',state:'blocked'}
                 if the outcome is dropped, Home.test 'toggle outcome' fails: response applied=false failed ['tv']
                 -> alert naming tv; shared [{client:'tv',others:[Bo]}] -> notice naming tv and Bo; the blocked view
                 is fetched again and GET /api/v1/grants is re-polled

Wave 2 (depends t-1)
  t-4  Revert skips permanently unblocked services
       files:    internal/grants/shared.go, internal/grants/shared_test.go, internal/grants/grants.go,
                 internal/grants/grants_test.go
       desc:     Store interface gains SetToggle/ListToggles. New pure permanentOpen(toggles, children, client)
                 (sorted ids any current owner has unblocked). revertPlan takes it and drops those ids per client;
                 revert reads toggles + children and fails every client without a write if either read fails.
       covers:   c-3, c-4
       contract: if revert re-blocks a permanent unblock, TestRevertPlan_Permanent fails: grant {youtube,tiktok}
                 on [phone,tv], youtube permanently unblocked for the child -> plan == {phone:[tiktok], tv:[tiktok]}
                 if shared-owner opening is lost, TestRevertPlan_PermanentOtherOwner fails: Bo (co-owner of tv only)
                 has youtube unblocked -> plan phone:[youtube,tiktok], tv:[tiktok]
                 if ref-counting regresses, TestRevertPlan_* existing shared-device cases still pass unchanged
                 with an empty toggle set
                 if End re-blocks it, TestEnd_KeepsPermanentUnblock fails: real store + adguardtest, grant
                 {youtube,tiktok}, SetToggle(child,youtube,unblocked), End -> both clients' blocked_services
                 contain tiktok, not youtube; status ended
                 if timer expiry re-blocks it, TestExpire_KeepsPermanentUnblock fails: same setup, fire the fake
                 timer -> same assertion, status expired
                 if a failed toggles read re-blocks blind, TestRevert_TogglesReadFails fails: wrapped store
                 ListToggles errors -> End returns *ErrRevertFailed, 0 AddBlockedServices POSTs, grant still active

Wave 3 (depends t-4)
  t-5  Engine Toggle op + reconcile convergence
       files:    internal/grants/toggles.go (new), internal/grants/toggles_test.go (new), internal/grants/rules.go,
                 internal/grants/reconcile.go, internal/grants/reconcile_test.go
       desc:     Engine.Toggle(ctx, childID, service, state) under mu: custom id checked as in Create, store
                 SetToggle, stop timers of Ended grants, then write: unblock -> RemoveBlockedServices on every mapped
                 client; block -> AddBlockedServices on each mapped client not held open (heldOpen by another
                 grant or permanentOpen by another owner); custom -> syncRules. Returns Result-like {Applied,
                 Failed}. Engine.Toggles passthrough. desiredRegion adds a Cover per unblocked custom toggle over the
                 child's current clients. Reconcile gains a toggle half converging blocked_services to built-in
                 intent with the same held-open exclusion.
       covers:   c-1, c-2, c-3, c-4
       contract: if unblock does not reach AdGuard, TestToggle_UnblockBuiltin fails: youtube blocked on
                 [phone,tv] -> Toggle(unblocked) returns Applied, both clients lack youtube when it returns
                 if block skips a client, TestToggle_BlockBuiltin fails: youtube absent -> Toggle(blocked) adds it
                 to both clients
                 if a shared client is closed over another owner, TestToggle_BlockSharedHeldOpen fails: Bo has
                 youtube unblocked and co-owns tv -> Toggle(Ann, blocked) adds to phone only; tv untouched
                 if block_during_grant is wrong, TestToggle_BlockDuringGrant fails: active grant {youtube,tiktok} ->
                 Toggle(youtube,blocked): youtube re-added on every client, tiktok still absent, grant active with
                 [tiktok]; grant {youtube} only -> grant ended and its timer stopped (fake AfterFunc Stop called)
                 if unblock_during_grant writes or ends the grant, TestToggle_UnblockDuringGrant fails: grant
                 unchanged, still active, timer armed; after expiry youtube stays open (c-3)
                 if custom toggles miss the region, TestToggle_Custom fails: Toggle(custom:1,unblocked) -> region has
                 @@||a.com^$client='phone'|'tv'; Toggle(custom:1,blocked) -> that @@ line gone, block line kept
                 if partial apply strands intent, TestToggle_PartialApply fails: tv uses the global list -> Applied
                 false, Failed [tv], ListToggles still holds the intent
                 if drift is not converged, TestReconcile_ToggleDrift fails: youtube unblocked toggle, parent re-adds
                 it in AdGuard -> one Reconcile removes it; blocked toggle, parent removes it -> Reconcile re-adds it;
                 an untoggled service the parent changed is left alone (0 writes for it)
                 if reconcile fights a grant, TestReconcile_ToggleVsGrant fails: blocked toggle on youtube + Bo's
                 active grant covering youtube on shared tv -> Reconcile leaves tv open
                 if restart loses it, TestStart_PermanentAcrossRestart fails: grant {youtube,custom:1} plus youtube
                 and custom:1 unblocked toggles, engine closed, ends_at passed, new Engine over the reopened store
                 dir -> Start marks it expired, youtube absent on every client, region still carries the custom:1
                 @@ line, and a second Reconcile writes nothing (RulesWritten 0, Writes 0)

Wave 4 (depends t-1, t-2, t-5)
  t-6  Toggle endpoint + blocked view wiring
       files:    internal/api/toggles.go (new), internal/api/toggles_test.go (new), internal/api/api.go,
                 internal/api/blocked.go, internal/api/blocked_test.go
       desc:     POST /api/v1/children/{id}/toggles per wire conventions (child 404 first, state/ids validated,
                 built-ins against AdGuard's catalogue, ErrUnknownService -> 400). Grants interface gains
                 Toggle/Toggles. handleBlocked passes Toggles (a failed read degrades like grants: custom left out,
                 permanent null, warn) and serialises permanent fields.
       covers:   c-1, c-2, c-5
       contract: if the endpoint does not apply within the request, TestToggle_API_Unblock fails: POST
                 {service:youtube,state:unblocked} -> 200 applied true, failed [], and the fake's phone and tv lack
                 youtube before the response is read (no reconcile tick needed: well under 5 s)
                 if shared devices are not named, TestToggle_API_Shared fails: tv co-owned with Bo -> shared ==
                 [{client:tv,others:[{id:2,name:Bo}]}]
                 if validation is loose, TestToggle_API_Bad fails: state "open" -> 400; service "nope" -> 400;
                 "custom:99" -> 400; child 99 -> 404 with 0 AdGuard requests; missing CSRF header -> 403
                 if partial apply is hidden, TestToggle_API_Partial fails: tv uses global -> 200 applied false,
                 failed [tv]
                 if the view hides permanent state, TestBlocked_Permanent fails: after unblocking youtube the GET
                 /blocked entry is {state:unblocked, permanent:"unblocked"}; Bo's view of shared tv has
                 unblocked_by [{client:tv, grant_id:0, child:{Ann}, permanent:true}]
                 if a toggles read failure 500s, TestBlocked_TogglesUnavailable fails: ListToggles error -> 200,
                 built-ins listed, custom services left out, permanent null
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 permanent unblock (built-in + custom), applied in-request | t-5 (engine), t-6 (endpoint), t-3 (UI control), t-2 (custom state in view) |
| c-2 permanent block (built-in + custom), incl. during grant | t-1 (atomic trim/end), t-5 (engine), t-6 (endpoint), t-3 (row control + picker) |
| c-3 expiry/end keeps permanent unblocks, ref-counting unchanged | t-4 (revertPlan + End/expire), t-5 (unblock_during_grant then expiry) |
| c-4 persists across restart; startup pass + every tick | t-1 (persist), t-4 (startup revert path), t-5 (TestStart_PermanentAcrossRestart, reconcile drift, region every tick) |
| c-5 view lists permanently unblocked with block control | t-2 (fold), t-6 (serialise), t-3 (render) |

5/5 covered.

## Judgment calls

- Trim/end the grant inside SetToggle's store tx, rejected a separate store.TrimGrantService + engine orchestration: one atomic write means a crash can never leave "blocked" intent with the grant still covering it, and it saves a task/file.
- Custom services need no rules.Render change, rejected a new render input: a permanent unblock is just another Cover in desiredRegion, a permanent block is the default block line — Render's union already does any-owner-opens.
- Toggle clients = child's current mapping, rejected snapshotting clients on the toggle row: toggles are standing intent (unlike a grant's fixed client set), so a newly mapped device should follow it; no extra table.
- No cleanup of toggles when a custom service is deleted, rejected stripping them in DeleteCustomService: AUTOINCREMENT ids are never reused and Render/fold ignore ids with no stored service, so a dangling row is inert; no criterion asks for it.
- Block picker lives inside Home.svelte, rejected a new BlockPicker.svelte + test: one consumer, Home.test already stubs fetch per route; saves two files without hurting testability.
- SPA in wave 1 against the wire conventions above, rejected waiting for the API task: the shape is pinned here, so nothing in t-3 strictly needs backend output.
- Blocked fold (t-2) kept separate from the API task (t-6), rejected merging: merged it would be 7 files across domain + api; the fold is pure and runs in wave 1.
- Engine split into t-4 (revert) and t-5 (toggle op + reconcile), rejected one grants task: one task would be 9 files; t-4 owns the Store interface change so t-5 cannot run beside it.
- Toggle endpoint is POST .../toggles with the service id in the body, rejected PUT .../toggles/{service}: keeps `custom:<n>` out of the path segment.
- Toggles reach the API through the engine (Grants.Toggles passthrough), rejected a new Deps field: no main.go change, and the existing embedded-interface fakes (renameOK, failingChildren) keep compiling.
- No grant-history "ended by block" reason: it is deferred to grant-history in the spec.
