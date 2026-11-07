# permanent-toggles — risk-lens draft

Lens: failure modes drive the graph. Every task below exists to own a named set of
risks (see the Risk register); each risk has exactly one owning task, and that
task's test_contract is where it is pinned.

Wire conventions this draft assumes (for the judge to merge or replace):
- store: `service_toggles(child_id INTEGER NOT NULL REFERENCES children(id) ON DELETE CASCADE,
  service_id TEXT NOT NULL, state TEXT NOT NULL CHECK (state IN ('blocked','unblocked')),
  updated_at INTEGER NOT NULL, PRIMARY KEY (child_id, service_id))`.
- `Store.SetServiceToggle(ctx, childID, serviceID, state, at) (store.ToggleChange{TrimmedGrantID int64, GrantEnded bool}, error)`;
  `Store.ListServiceToggles(ctx) ([]store.ServiceToggle, error)`.
- `Engine.Toggle(ctx, childID, serviceID, state) (grants.ToggleResult{Applied bool, Failed []string, Clients []string, Grant *{ID, Ended}}, error)`.
- `POST /api/v1/children/{id}/toggles {service_id, state}` -> 200
  `{service_id, state, applied, failed[], shared[{client, others}], grant:{id, ended}|null}`;
  404 unknown child, 400 bad body/state, 422 unknown service id, 502 catalogue unreadable (nothing stored).
- blocked view: each service gains `toggle: "blocked"|"unblocked"|null`; `unblocked_by[]` entries gain
  `permanent: bool` (grant_id 0 when permanent).
- Effective rule per (client, toggled service): OPEN if any current owner has it `unblocked` or any
  active grant covers it there; else BLOCKED if any owner has it `blocked`; else untouched (AdGuard-owned).

```
Phase permanent-toggles — 11 tasks across 4 waves

Wave 1
  t-1  Store toggles with atomic grant trim
       files:    internal/store/migrations/0008_service_toggles.sql (new), internal/store/toggles.go (new),
                 internal/store/toggles_test.go (new), internal/store/custom_services.go,
                 internal/store/custom_services_test.go
       covers:   c-2, c-4
       desc:     service_toggles table; SetServiceToggle upserts and, for state=blocked, in the SAME tx deletes
                 the child's active grant_services row for that service and ends the grant (status ended,
                 ended_at) when no services remain. ListServiceToggles. DeleteCustomService strips toggles
                 naming custom:<id> in its tx.
       contract: - if the trim is not atomic with the toggle, TestSetToggle_TrimAtomic fails: an injected
                   failure after the upsert leaves neither the toggle row nor a trimmed grant (grant still
                   lists the service) — a toggle without the trim would make the reconciler and the grant
                   fight over blocked_services every tick
                 - if a trimmed-to-empty grant stays active, TestSetToggle_EndsEmptyGrant fails: grant on
                   [youtube] + block youtube -> ToggleChange{TrimmedGrantID:g, GrantEnded:true}, GetGrant
                   status "ended", ListActiveGrants empty, and a new grant for (child, youtube) is accepted
                   (partial unique slot freed)
                 - if trimming touches the wrong grant, TestSetToggle_TrimScope fails: grant on [youtube,
                   tiktok] -> block youtube leaves [tiktok] active; another child's active youtube grant and
                   this child's ended youtube grant are untouched; state=unblocked trims nothing
                 - if upsert duplicates, TestSetToggle_Upsert fails: blocked then unblocked for the same pair
                   -> one row, state "unblocked"
                 - if bad input is stored, TestSetToggle_Invalid fails: state "open" -> error, no row; unknown
                   child id -> store.ErrChildNotFound (FK), no row
                 - if toggles outlive their owner, TestToggles_Cascade fails: DeleteChild -> its rows gone;
                   DeleteCustomService(1) -> no row names "custom:1", built-in rows kept
                 - if toggles do not persist, TestToggles_Persist fails: set, Close, store.Open same dir ->
                   identical ListServiceToggles; TestMigrate_Order lists 0008 last

  t-2  Pure intent planner: open-wins and revert-v2
       files:    internal/grants/shared.go, internal/grants/shared_test.go, internal/grants/intent.go (new),
                 internal/grants/intent_test.go (new)
       covers:   c-3
       desc:     permanentOpen(children, unblocked map[childID][]svc) -> client->services opened by ANY
                 current owner. revertPlan gains that map and drops those services per client. togglePlan
                 computes per client {remove, add} from owners' intents + active-grant coverage (open wins),
                 considering only toggled services. Pure, no I/O.
       contract: - if revert ignores permanent unblocks, TestRevertPlan_PermanentOpen fails: grant
                   [youtube,tiktok] on [kid-phone], youtube unblocked for the owner -> plan {kid-phone:[tiktok]}
                 - if the "any owner" rule is wrong, TestRevertPlan_SharedOwner fails: tv owned by A and B,
                   B unblocked youtube, A's grant on youtube covers tv and a-phone -> tv omitted, a-phone
                   [youtube]; with tv owned by nobody now (stored grant client, unmapped) -> tv re-blocked
                 - if ref-counting regresses, TestRevertPlan_* existing table still passes unchanged with an
                   empty permanentOpen map (other grant holding tv open still omits tv)
                 - if block beats open, TestTogglePlan_OpenWins fails: A blocks youtube, B unblocks youtube,
                   shared tv -> tv gets youtube in remove and not in add; A's unshared a-phone gets add
                 - if a block fights a live grant, TestTogglePlan_GrantHolds fails: A blocks youtube, C's
                   active grant covers youtube on shared tv -> tv not in add
                 - if untoggled services are touched, TestTogglePlan_AdGuardOwned fails: no toggle rows ->
                   empty plan for every client regardless of what blocked_services holds

  t-3  Fold permanent toggles into the blocked view
       files:    internal/blocked/blocked.go, internal/blocked/blocked_test.go
       covers:   c-5
       desc:     Input gains Toggles (child id -> service -> state). ServiceState gains Toggle (self's
                 state or ""). Custom fold: open on a client iff a grant covers it OR any owner has it
                 unblocked. UnblockedBy adds {client, child, Permanent:true} for another owner's permanent
                 unblock on a shared client.
       contract: - if a toggled-unblocked service is indistinguishable, TestCompute_ToggleState fails:
                   youtube absent from every client + self toggle unblocked -> State "unblocked", Toggle
                   "unblocked"; never-toggled youtube -> Toggle ""
                 - if a custom permanent unblock reads as blocked, TestCompute_CustomPermanent fails:
                   custom:1, no grants, self unblocked -> State "unblocked"; B's unblock on shared tv only ->
                   State "partial", Differs [tv]
                 - if attribution is lost, TestCompute_PermanentUnblockedBy fails: B unblocked youtube, tv
                   shared -> A's view UnblockedBy [{tv, B, Permanent:true, GrantID:0}]; self's own toggle
                   never appears in UnblockedBy
                 - if nil Toggles breaks the fold, existing TestCompute_* pass unchanged with Toggles nil

  t-4  SPA toggle client and pure view helpers
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/lib/toggles.ts (new),
                 web/src/lib/toggles.test.ts (new)
       covers:   c-1, c-2, c-5
       desc:     api.ts: ServiceState.toggle, Unblock.permanent, toggleService(childId, serviceId, state).
                 toggles.ts: shown(view), openOn(service) (moved from Home), controlFor(service) ->
                 'unblock'|'block'|null, pickerOptions(catalogue, view, query).
       contract: - if the client hits the wrong endpoint, api.test 'toggle service' fails:
                   toggleService(3,'custom:1','blocked') -> POST /api/v1/children/3/toggles
                   {service_id:'custom:1',state:'blocked'} with CSRF header; response failed/shared default []
                 - if toggled-unblocked services stay hidden, toggles.test 'shown' fails: {state:'unblocked',
                   toggle:'unblocked', unblocked_by:[]} is kept; {state:'unblocked', toggle:null,
                   unblocked_by:[]} is dropped
                 - if the wrong control is offered, toggles.test 'controlFor' fails: blocked -> 'unblock',
                   partial -> 'unblock', toggle 'unblocked' -> 'block', unblocked-by-other-only -> null
                 - if the picker offers already-blocked services, toggles.test 'pickerOptions' fails: view
                   blocked=[youtube], partial=[tiktok] over catalogue [youtube,tiktok,netflix,custom:1] ->
                   [tiktok,netflix,custom:1]; query "NET" -> [netflix]; query "zzz" -> []
                 - if permanent attribution reads as a grant, toggles.test 'openOn' fails: {tv, B,
                   permanent:true} -> "tv by B (permanent)"; grant entry text unchanged

Wave 2 (depends t-1, t-2, t-3, t-4)
  t-5  Revert and region honour stored toggles
       files:    internal/grants/grants.go, internal/grants/rules.go, internal/grants/grants_test.go,
                 internal/grants/rules_test.go
       covers:   c-3, c-4
       depends:  t-1, t-2
       desc:     Store interface gains ListServiceToggles/SetServiceToggle. revert reads toggles + children
                 and passes permanentOpen to revertPlan. syncRules reads toggles and adds a Cover over each
                 child's clients for every custom service that child has unblocked.
       contract: - if expiry re-blocks a permanent unblock, TestExpire_KeepsPermanentUnblock fails: grant
                   [youtube,tiktok] on kid-phone+tv, youtube unblocked, Advance past ends_at -> youtube absent
                   on both, tiktok present on both, grant "expired"
                 - if End differs from expiry, TestEnd_KeepsPermanentUnblock fails: same setup through End
                 - if a toggles read failure re-blocks blind, TestRevert_ToggleReadFailure fails: wrapped
                   store failing ListServiceToggles -> 0 /control/clients/update POSTs, *ErrRevertFailed from
                   End, grant still active
                 - if a toggles read failure renders a region without permanent exceptions,
                   TestSyncRules_ToggleReadFailure fails: 0 set_rules POSTs
                 - if the region drops a custom unblock, TestSyncRules_PermanentCover fails: custom:1 unblocked
                   for kid A (kid-phone, tv) -> exactly one @@||a.com^$client='kid-phone'|'tv'; a grant on
                   custom:1 for A expiring leaves that @@ line byte-identical
                 - if restart loses intent, TestStart_RestartKeepsToggles fails: toggle + overdue grant stored,
                   Close engine and store, reopen store, new Engine, Start -> youtube still absent, @@ line
                   present, grant "expired"; a second Reconcile writes 0 set_rules

  t-6  Expose toggle state in the blocked API
       files:    internal/api/blocked.go, internal/api/blocked_test.go, internal/api/api.go,
                 cmd/adguard-reward/main.go
       covers:   c-5
       depends:  t-1, t-3
       desc:     Deps gains Toggles (ListServiceToggles), wired in main. handleBlocked passes toggles to
                 Compute and serves toggle + unblocked_by[].permanent. A failed toggles read leaves custom
                 services out and toggle null (warn), never a false "blocked", never a 500.
       contract: - if toggle state is missing on the wire, TestBlocked_Toggle fails: youtube unblocked for
                   the child -> services[] youtube {state:"unblocked", toggle:"unblocked"}; never-toggled
                   -> "toggle":null (key present)
                 - if a toggles outage misreports, TestBlocked_TogglesDown fails: ListServiceToggles error ->
                   200, no custom entries, every toggle null
                 - if main wiring is missing, a TestRun_*-style boot through run() fails: a stored toggle is
                   absent from GET /children/{id}/blocked

  t-7  Searchable block picker component
       files:    web/src/lib/BlockPicker.svelte (new), web/src/lib/BlockPicker.test.ts (new)
       covers:   c-2
       depends:  t-4
       desc:     Renders a search input over pickerOptions; picking a row emits onpick(service_id) once and
                 closes; empty result shows "No matching service".
       contract: - if already-blocked services leak in, BlockPicker.test 'excludes blocked' fails: view with
                   youtube blocked -> no youtube row rendered
                 - if search is case-sensitive or ignores custom, BlockPicker.test 'search' fails: typing
                   "por" shows custom "School Portal"
                 - if a pick double-fires, BlockPicker.test 'single pick' fails: two rapid clicks -> onpick
                   called once and the picker is closed

Wave 3 (depends t-5, t-7)
  t-8  Engine Toggle: store, trim, write under mutex
       files:    internal/grants/toggle.go (new), internal/grants/toggle_test.go (new)
       covers:   c-1, c-2
       depends:  t-5
       desc:     Toggle under e.mu: custom id must exist (*ErrUnknownService, nothing stored);
                 SetServiceToggle; stop the timer of an ended grant; one ApplyTimeout context; unblock ->
                 RemoveBlockedServices on every child client; block -> AddBlockedServices where togglePlan
                 does not hold it open; syncRules when the service is custom or the trimmed grant had custom
                 ids. Failed clients reported, intent kept.
       contract: - if unblock misses a client, TestToggle_UnblockAll fails: child on kid-phone, tablet, tv
                   (shared) -> youtube absent on all three, Applied true, Clients lists all three
                 - if block_during_grant leaves the grant covering, TestToggle_BlockDuringGrant fails: grant
                   [youtube,tiktok] -> block youtube: youtube present on every grant client immediately,
                   tiktok still absent, grant active with [tiktok], timer still armed
                 - if an emptied grant keeps its timer, TestToggle_BlockEndsGrant fails: grant [youtube] ->
                   Grant{Ended:true}, fakeClock.pending()==0, later Advance issues no update POST
                 - if unblock_during_grant writes or trims, TestToggle_UnblockDuringGrant fails: grant on
                   youtube -> grant unchanged and still active; at expiry youtube stays absent
                 - if a block overrides another owner, TestToggle_BlockSharedHeldOpen fails: B unblocked
                   youtube, A blocks -> tv untouched (absent), a-phone present, tv not in Failed
                 - if partial failure strands intent, TestToggle_PartialApply fails: update 500 on tablet ->
                   Applied false, Failed [tablet], toggle row stored; clients read 502 -> Failed = every
                   client, 0 update POSTs, row stored; uses_global client -> in Failed, global list unwritten
                 - if a custom toggle skips the region, TestToggle_Custom fails: unblock custom:1 -> one
                   @@ line over the child's clients; block custom:1 under a grant -> @@ line gone; malformed
                   region -> Applied false, 0 set_rules POSTs, row stored
                 - if the custom check races delete, TestToggle_UnknownCustom fails: DeleteService(1) then
                   Toggle custom:1 -> *ErrUnknownService, no row
                 - if the mutex is skipped, TestToggle_RaceExpiry fails: 50 runs of Toggle(unblock youtube)
                   concurrent with the grant's expiry -> youtube absent on every client at the end of every run
                 - if re-toggling rewrites, TestToggle_Idempotent fails: second identical unblock -> 0 update
                   POSTs
                 - if the 5 s budget is unbounded, TestToggle_Deadline fails: Hang /control/clients/update
                   10 s, ApplyTimeout 1 s -> returns within 2 s with Applied false

  t-9  Reconciler converges toggles; baseline respects them
       files:    internal/grants/reconcile.go, internal/grants/reconcile_test.go, internal/grants/clients.go,
                 internal/grants/clients_test.go
       covers:   c-1, c-2, c-4
       depends:  t-5
       desc:     Reconcile gains a toggle half after the grant half (re-reading active grants): applies
                 togglePlan per present, non-global client; counted in Stats.Writes/Repaired. A toggles,
                 children or grants read failure skips the half with no writes. baselineFrom drops services
                 an owner of the target device has unblocked and adds the child's blocked toggles.
       contract: - if drift is not undone, TestReconcile_ToggleDrift fails: youtube unblocked, parent
                   re-adds it in AdGuard (MutateClient) -> one pass removes it; tiktok blocked, parent removes
                   it -> one pass re-adds it; a second pass issues 0 writes
                 - if AdGuard-owned services are touched, TestReconcile_UntoggledUntouched fails: no toggle
                   rows, parent changes netflix in AdGuard -> 0 update POSTs
                 - if the reconciler fights a grant, TestReconcile_BlockYieldsToGrant fails: youtube blocked
                   for A, C's live grant covers shared tv -> tv keeps youtube absent across 3 passes
                 - if an overdue grant's services stay open, TestReconcile_OverdueThenBlock fails: overdue
                   grant on tiktok + block toggle on tiktok -> one pass leaves tiktok present
                 - if a read failure converges from partial data, TestReconcile_ToggleReadFailure fails:
                   ListServiceToggles error -> 0 toggle writes, grant half still repairs drift
                 - if a new device misses toggles, TestCreateClient_BaselineToggles fails: child has
                   youtube unblocked and tiktok blocked, sibling blocks youtube -> new client's
                   blocked_services excludes youtube and includes tiktok; same through AdoptClients

  t-10 Home: toggle controls, picker, refresh
       files:    web/src/lib/Home.svelte, web/src/lib/Home.test.ts
       covers:   c-1, c-2, c-5
       depends:  t-4, t-7
       desc:     Each shown row gets controlFor's button; "Block a service…" at the foot opens BlockPicker;
                 a toggle POSTs once (inflight guard), then refetches the child's blocked view and active
                 grants; applied=false shows "Not applied on <names>"; shared shows "Also on <devices>".
       contract: - if the row control is wrong or missing, Home.test 'toggle controls' fails: blocked row has
                   "Unblock", toggled-unblocked row is listed with state "unblocked" and has "Block"
                 - if a double tap sends twice, Home.test 'toggle inflight' fails: two clicks before the
                   POST resolves -> fetch called once for /toggles
                 - if the view goes stale, Home.test 'toggle refresh' fails: after the POST resolves the
                   blocked view and /grants are each fetched again and the row shows the new state
                 - if partial apply is hidden, Home.test 'toggle partial' fails: {applied:false,
                   failed:['tablet']} renders a role=status note naming tablet
                 - if shared devices are not named, Home.test 'toggle shared' fails: shared [{client:'tv',
                   others:[B]}] renders a note naming tv and B
                 - if errors are swallowed, Home.test 'toggle error' fails: 502 -> role=alert, row unchanged

Wave 4 (depends t-6, t-8)
  t-11 Toggle endpoint with validation and shared report
       files:    internal/api/toggles.go (new), internal/api/toggles_test.go (new), internal/api/api.go,
                 cmd/adguard-reward/main_test.go
       covers:   c-1, c-2
       depends:  t-6, t-8
       desc:     POST /api/v1/children/{id}/toggles: decode bounded body; 404 unknown child before any
                 AdGuard call; built-in id must be in the live catalogue (422; catalogue down 502, nothing
                 stored); engine Toggle; response {applied, failed, shared (sharedOf the written clients),
                 grant}. Grants interface gains Toggle.
       contract: - if junk reaches blocked_services, TestToggle_UnknownBuiltin fails: service_id "nope" ->
                   422, no row, 0 update POSTs; "custom:" and "custom:99" -> 422
                 - if bad input is accepted, TestToggle_BadBody fails: state "open", missing service_id, an
                   unknown field, two objects -> 400 each
                 - if the catalogue outage stores intent, TestToggle_CatalogueDown fails: /control/blocked_
                   services/all 502 -> 502 and ListServiceToggles empty
                 - if the shared report is wrong, TestToggle_SharedReport fails: tv also owned by B -> shared
                   [{client:"tv", others:[{id:B,...}]}]; no shared client -> shared []
                 - if partial apply is a server error, TestToggle_PartialIs200 fails: engine Failed [tablet]
                   -> 200 {applied:false, failed:["tablet"]}
                 - if CSRF/session gating is missing, TestToggle_Auth fails: no session -> 401, no CSRF
                   header -> 403, nothing stored
                 - if end-to-end wiring or the 5 s budget breaks, TestRun_ToggleEndToEnd (main_test) fails:
                   boot run(), POST unblock youtube for a child on 2 clients -> response within 5 s and the
                   fake's blocked_services for both lacks youtube; POST block -> both contain it
```

## Risk register (each risk -> exactly one owning task)

| Risk | Owner |
|---|---|
| Toggle stored but grant not trimmed (grant vs. block intent flap) | t-1 |
| Emptied grant left active / unique slot not freed | t-1 |
| Toggle rows dangle after child or custom-service delete | t-1 |
| Invalid state / unknown child persisted | t-1 |
| "Any owner opens" computed wrong on shared clients | t-2 |
| Revert re-blocks a permanently unblocked service (pure plan) | t-2 |
| Revert uses stored instead of current ownership for permanents | t-2 |
| Block intent overrides another owner's unblock or a live grant (pure plan) | t-2 |
| Untoggled services touched (breaks AdGuard ownership) | t-2 |
| Custom permanent unblock folded as "blocked" in view | t-3 |
| Other owner's permanent unblock unattributed | t-3 |
| Toggles read failure -> blind re-block at revert | t-5 |
| Toggles read failure -> region without permanent exceptions | t-5 |
| Restart loses intent / startup pass re-blocks | t-5 |
| Toggles outage turns view into 500 or false "blocked" | t-6 |
| Picker offers already-blocked; picker double-fires | t-7 |
| Toggle vs. expiry race (mutex) | t-8 |
| Partial AdGuard failure loses intent or reports success | t-8 |
| Toggle exceeds the 5 s budget on a hung AdGuard | t-8 |
| Custom toggle vs. concurrent custom delete | t-8 |
| block_during_grant leaves timer armed / other services re-blocked | t-8 |
| Manual AdGuard edit drifts a toggled service | t-9 |
| Reconciler fights a live grant on a block-intent service | t-9 |
| New/adopted device ignores toggles (baseline) | t-9 |
| Stale view / double POST / hidden partial-apply in UI | t-10 |
| Unknown or malformed service id reaches blocked_services | t-11 |
| Catalogue outage stores unvalidated intent | t-11 |
| Endpoint wiring / auth / CSRF missing | t-11 |

## Coverage

- c-1 (permanent unblock within 5 s): t-4, t-8, t-9, t-10, t-11
- c-2 (permanent block within 5 s, picker): t-1, t-4, t-7, t-8, t-9, t-10, t-11
- c-3 (expiry/end keeps permanent unblocks, ref-count unchanged): t-2, t-5
- c-4 (persistence across restart, region every tick): t-1, t-5, t-9
- c-5 (view lists toggled-unblocked with a block control): t-3, t-4, t-6, t-10

5/5 criteria covered.

## Judgment calls

- Grant trim and toggle upsert in one store transaction (t-1); rejected trimming in the engine after the upsert — a crash between them leaves block intent and a live grant that the reconciler would flip every tick.
- Trim deletes the grant_services row; rejected `active=0` on a live grant — it breaks the "active mirrors grants.status" invariant and loadGrants would still list the service. History of trims is deferred to grant-history anyway.
- Pure intent planner (t-2) split from the engine so the open-wins / revert-v2 rules are table-tested once and reused by revert, Toggle and Reconcile; rejected encoding the rule three times inline.
- Unblock toggle always issues Remove on every child client (no-op where already open) instead of "record only" during a grant; the write is a no-op under the grant (honours unblock_during_grant) but closes the toggle-vs-expiry race even if ordering slips, and covers partial clients.
- Revert uses CURRENT owners of each stored grant client for the permanent check (per revert_baseline_v2 "an owner of that client"), while grant coverage keeps stored clients; rejected snapshotting owners on the grant.
- Any toggles/children/grants read failure fails closed: no re-block at revert, no region write, no toggle half in reconcile; rejected degrading to "no toggles" — that direction re-blocks a permanently unblocked service.
- A missing or global-list client counts as Failed for a toggle (mirrors grant Create) rather than silently skipped, so applied=false is honest.
- Built-in id validated against the live catalogue in the API with 502-on-outage and nothing stored; rejected storing unvalidated ids for the reconciler, which would push junk ids into blocked_services forever.
- Baseline fix for new/adopted devices included (t-9) although no criterion names it: without it a new device blocks a permanently unblocked service until the next 60 s tick, a drift the locked drift_enforcement decision forbids.
- Endpoint is POST /children/{id}/toggles with a state body; rejected two verb routes (/block, /unblock) — one handler, one validation path, one test surface.
