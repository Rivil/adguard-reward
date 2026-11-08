# permanent-toggles — panel synthesis

## Scores

Scale 1-5. Code facts checked against the tree are cited where they decide a score.

| Draft | Dimension | Score | Note |
|---|---|---|---|
| risk | criteria coverage | 4 | 5/5 covered, plus the only fail-closed region read and the only new-device baseline fix. But `controlFor(partial) -> 'unblock'` leaves no Block control on a partial row, which c-2 ("block … unblocked or partial") implies. Also has no built-in-only guard on the planner. |
| risk | test-contract specificity | 5 | Every contract names a test, a fixture and an observable (POST counts, timer pending, byte-identical @@ line), plus race, idempotence and deadline tests that neither other draft has together. |
| risk | granularity | 4 | t-1 is 5 files (Go edit to `DeleteCustomService`). t-8 packs 11 contracts into 2 files, but it is one concern. Every other task is ≤4 files in one layer. |
| risk | wave correctness | 5 | Every dependency is real. The pure planner (t-2) runs in wave 1, so revert, Toggle and Reconcile all share it. |
| mvp | criteria coverage | 3 | Nominally 5/5. Missing: fail-closed region read, deadline test, mutex race, and the trimmed grant's unmapped clients. Rejects custom-delete cleanup outright. |
| mvp | test-contract specificity | 3 | The contracts are concrete, but there are fewer of them. t-5's bundle mixes Toggle, region and reconcile, so one red test does not point at one surface. |
| mvp | granularity | 2 | t-5 is 5 files over 3 concerns (op + region + reconcile). t-6 is 5 files (endpoint + view wiring). t-3 is client + UI + picker. All break the 5+-file split rule. |
| mvp | wave correctness | 5 | Minimal and correct. Pinning the wire lets the SPA run in wave 1. |
| verification | criteria coverage | 5 | 5/5, with each locked decision mapped to a proving contract. Only draft with `BlockOldDevice` (trimmed grant's stored clients) and `BuiltinOnly` (no custom id in blocked_services). |
| verification | test-contract specificity | 4 | Very specific, but two contracts are wrong. (1) The `TestToggles_BlockTrimsGrant` fixture has active grants 7 and 8 both covering youtube for child 1, which `grant_services_live_idx` (0003_grants.sql:26, unique on (child_id, service_id) WHERE active=1) makes impossible to build. (2) 404 for an unknown service contradicts the existing 422 convention (internal/api/grants.go:245,259). |
| verification | granularity | 5 | Every task is ≤4 files, one layer each. The custom-delete trigger keeps the store task at 4 files. |
| verification | wave correctness | 3 | t-9 `TestReconcile_ConvergesToggle` drives `Engine.Toggle` (t-8) yet sits beside t-8 in wave 3. As written it cannot pass in isolation. |

**Skeleton: risk.** It is the only draft whose wave graph is both correct and parallel. Its pure planner in wave 1 is reused by revert, Toggle and Reconcile, and its contracts are the sharpest. Grafted onto it: verification's granularity fix (SQL trigger), its trimmed-client and built-in-only contracts, and its view and SPA contracts; mvp's foreign-toggle and partial-row-both-controls contracts.

## Merged plan

Pinned wire (provisional defaults; see Disagreements 1, 2 and 8):
- Store: `service_toggles(child_id FK children ON DELETE CASCADE, service_id TEXT, state CHECK IN ('blocked','unblocked'), updated_at, PK(child_id, service_id))`, plus an `AFTER DELETE ON custom_services` trigger that drops `'custom:' || OLD.id`.
  - `SetServiceToggle(ctx, childID, serviceID, state, at) (ToggleChange{Trimmed *store.Grant /*pre-trim row*/, Ended bool}, error)`. At most one grant can be trimmed, because of `grant_services_live_idx`.
  - `ListServiceToggles(ctx)` returns rows ordered by (child_id, service_id).
- Engine: `Toggle(ctx, childID, serviceID, state) (ToggleResult{Applied, Failed []string, Clients []string, Grant *{ID, Ended}}, error)`.
- HTTP: `POST /api/v1/children/{id}/toggles {service_id, state}` returns 200 `{service_id, state, applied, failed[], shared[{client, others}], grant:{id, ended}|null}`.
  - Errors: 404 unknown child, 400 bad body/state, 422 unknown service, 502 catalogue unreadable (nothing stored).
- Blocked view: each service gains `toggle: "blocked"|"unblocked"|null`; `unblocked_by[]` entries gain `permanent: bool` (with `grant_id` 0).
- Precedence per (client, toggled built-in), used everywhere:
  1. OPEN if any current owner has it `unblocked` or any active grant covers it there.
  2. Else BLOCK if any owner has it `blocked`.
  3. Else AdGuard-owned (no write).

```
Phase permanent-toggles — 11 tasks across 4 waves

Wave 1
  t-1  Store toggles with atomic grant trim  [risk+verification+mvp]
       files:    internal/store/migrations/0008_service_toggles.sql (new), internal/store/toggles.go (new),
                 internal/store/toggles_test.go (new), internal/store/custom_services_test.go
       covers:   c-2, c-4
       depends:  (none)
       desc:     service_toggles table + custom-delete trigger. SetServiceToggle upserts; for state=blocked the SAME
                 tx deletes the child's active grant_services row for that service and ends a grant left with none
                 (status ended, ended_at=at), returning the pre-trim grant. ListServiceToggles lists all.
       contract: - if trim is not atomic with the upsert, TestSetToggle_TrimAtomic fails: an injected failure after
                   the upsert leaves neither the toggle row nor a trimmed grant; state "maybe" -> error, no row,
                   grant still [tiktok,youtube]
                 - if an emptied grant stays active, TestSetToggle_EndsEmptyGrant fails: grant {youtube} + block
                   youtube -> Ended true, GetGrant status "ended", ListActiveGrants empty, CreateGrant(child,
                   [youtube]) then succeeds instead of *ErrGrantOverlap
                 - if trimming is mis-scoped, TestSetToggle_TrimScope fails: grant {tiktok,youtube} -> block youtube
                   leaves [tiktok] active with ends_at unchanged and Trimmed carrying the pre-trim stored clients;
                   child 2's active youtube grant and child 1's expired youtube grant untouched; state=unblocked
                   trims nothing (Trimmed nil, Ended false)
                 - if set is not an upsert, TestSetToggle_Upsert fails: blocked then unblocked -> one row, unblocked
                 - if bad input persists, TestSetToggle_Invalid fails: unknown child 99 -> ErrChildNotFound, no row
                 - if toggles dangle, TestToggles_Cascade fails: DeleteChild(1) drops only child 1's rows;
                   DeleteCustomService(1) drops custom:1 rows, keeps custom:10 (no prefix LIKE) and built-ins
                 - if intent does not persist, TestToggles_Persist fails: set, Close, store.Open same dir ->
                   identical ListServiceToggles; the custom_services_test migration-order check fails unless
                   0008_service_toggles.sql is last

  t-2  Pure intent planner: open-wins and revert-v2  [risk+verification]
       files:    internal/grants/shared.go, internal/grants/shared_test.go, internal/grants/intent.go (new),
                 internal/grants/intent_test.go (new)
       covers:   c-3
       depends:  (none)
       desc:     permanentOpen(children, toggles) -> client -> services any CURRENT owner has unblocked. revertPlan
                 takes it and drops those per client. togglePlan(children, toggles, active) -> per client {remove,
                 add} under the pinned precedence, built-in toggled services only. Pure, no I/O.
       contract: - if revert ignores permanent unblocks, TestRevertPlan_PermanentOpen fails: grant [youtube,tiktok]
                   on [kid-phone], youtube unblocked for the owner -> {kid-phone:[tiktok]}
                 - if "any owner" is read from the grant, TestRevertPlan_SharedOwner fails: tv owned by A+B, B
                   unblocked youtube, A's grant covers tv+a-phone -> tv omitted, a-phone [youtube]; B unmapped
                   from tv -> tv re-blocked
                 - if ref-counting regresses, existing TestRevertPlan_* pass unchanged with an empty map
                 - if block beats open, TestTogglePlan_OpenWins fails: A blocks, B unblocks youtube, shared tv ->
                   tv remove ∋ youtube, add ∌ youtube; a-phone add ∋ youtube
                 - if a block fights a live grant, TestTogglePlan_GrantHolds fails: C's active grant covers youtube
                   on shared tv, A blocks -> tv add ∌ youtube
                 - if custom ids reach blocked_services, TestTogglePlan_BuiltinOnly fails: custom:1 toggles appear
                   in no remove/add list
                 - if untoggled services are touched, TestTogglePlan_AdGuardOwned fails: no toggle rows -> empty
                   plan for every client

  t-3  Fold permanent toggles into the blocked view  [risk+mvp+verification]
       files:    internal/blocked/blocked.go, internal/blocked/blocked_test.go
       covers:   c-5
       depends:  (none)
       desc:     Input gains Toggles. ServiceState gains Toggle (self's intent or ""). Custom service open on a
                 present client iff a grant covers it OR an owner of that client has it unblocked. UnblockedBy adds
                 {client, child, GrantID:0, Permanent:true} for another owner's permanent unblock on a shared client.
       contract: - if self intent is lost, TestCompute_ToggleState fails: self unblocked youtube (absent everywhere)
                   -> State "unblocked", Toggle "unblocked"; self blocked tiktok -> Toggle "blocked"; untoggled -> ""
                 - if a custom permanent reads wrong, TestCompute_CustomPermanent fails: custom:1, no grants, self
                   unblocked -> unblocked; self blocked -> blocked; only B's unblock on shared tv -> partial, Differs [tv]
                 - if attribution is lost, TestCompute_PermanentUnblockedBy fails: B unblocked youtube, tv shared ->
                   A's UnblockedBy [{tv, B, GrantID:0, Permanent:true}]; self's own toggle never in UnblockedBy
                 - if ordering drifts, TestCompute_PermanentAndGrant fails: B's toggle + B's grant 5 on tv -> two
                   entries, permanent (id 0) first, then grant 5
                 - if a non-owner's toggle leaks, TestCompute_ForeignToggle fails: toggle by a child owning none of
                   the mapped clients changes no state and adds no UnblockedBy
                 - if nil Toggles changes today's output, TestCompute_NoToggles fails: existing fixtures with Toggles
                   nil produce byte-identical Views

  t-4  SPA toggle client and pure view helpers  [risk+verification]
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/lib/toggles.ts (new),
                 web/src/lib/toggles.test.ts (new)
       covers:   c-1, c-2, c-5
       depends:  (none)
       desc:     api.ts: toggleService(childId, serviceId, state), ToggleResult (arrays default [], grant null),
                 ServiceState.toggle, Unblock.permanent, childBlocked normalises missing fields. toggles.ts: shown,
                 openOn (moved from Home), controlFor -> ('unblock'|'block')[], pickerOptions(catalogue, view, query).
       contract: - if the client hits the wrong endpoint, api.test 'toggle service' fails: toggleService(3,'custom:1',
                   'blocked') -> POST /api/v1/children/3/toggles {service_id:'custom:1',state:'blocked'} with CSRF
                 - if result fields can be undefined, api.test 'toggle result defaults' fails: body without failed/
                   shared/grant -> [] / [] / null; blocked view without toggle/permanent -> null / false
                 - if errors lose detail, api.test 'toggle errors' fails: 422 -> ApiError code unprocessable
                 - if toggled-unblocked rows stay hidden, toggles.test 'shown' fails: {unblocked, toggle 'unblocked',
                   unblocked_by []} kept; {unblocked, toggle null, unblocked_by []} dropped
                 - if the wrong controls are offered, toggles.test 'controlFor' fails: blocked -> ['unblock'];
                   partial -> ['unblock','block']; toggle 'unblocked' -> ['block']; unblocked-by-other-only -> []
                 - if the picker offers blocked services, toggles.test 'pickerOptions' fails: blocked [youtube],
                   partial [tiktok] over [youtube,tiktok,netflix,custom:1] -> [tiktok,netflix,custom:1]; "NET" ->
                   [netflix]; "zzz" -> []
                 - if permanent attribution reads as a grant, toggles.test 'openOn' fails: {tv, B, permanent} ->
                   "tv by B (permanent)"; grant entries unchanged

Wave 2
  t-5  Revert and region honour stored toggles  [risk+mvp+verification]
       files:    internal/grants/grants.go, internal/grants/rules.go, internal/grants/grants_test.go,
                 internal/grants/rules_test.go
       covers:   c-3, c-4
       depends:  t-1, t-2
       desc:     Store interface gains SetServiceToggle/ListServiceToggles. revert reads toggles + children
                 (fail-closed) and passes permanentOpen to revertPlan. syncRules reads toggles (fail-closed) and
                 desiredRegion adds a Cover over the child's current clients per unblocked custom toggle.
       contract: - if expiry re-blocks a permanent unblock, TestExpire_KeepsPermanentUnblock fails: grant
                   [youtube,tiktok] on kid-phone+tv, youtube unblocked, Advance past ends_at -> youtube absent on
                   both, tiktok present on both, status expired
                 - if End differs, TestEnd_KeepsPermanentUnblock fails: same through End, status ended
                 - if a co-owner's unblock is ignored end-to-end, TestRevert_CoOwnerUnblock fails: A's grant on
                   youtube over [a-phone,tv], B (co-owner of tv) unblocked youtube -> End adds on a-phone, no Add for tv
                 - if a toggles read failure re-blocks blind, TestRevert_ToggleReadFailure fails: ListServiceToggles
                   error -> *ErrRevertFailed, 0 /control/clients/update POSTs, grant still active
                 - if a toggles read failure drops permanent exceptions, TestSyncRules_ToggleReadFailure fails: 0
                   set_rules POSTs
                 - if the region drops a custom unblock, TestSyncRules_PermanentCover fails: custom:1 unblocked for A
                   (kid-phone, tv) -> one @@||a.com^$client='kid-phone'|'tv'; A's custom:1 grant expiring leaves it
                   byte-identical
                 - if a tick loses it, TestSyncRules_ToggleSurvivesEdit fails: SetUserRules removes the @@ line inside
                   the markers -> next Reconcile restores it
                 - if restart loses intent, TestStart_RestartKeepsToggles fails: toggles on youtube + custom:1 and an
                   overdue grant on both, reopen store, new Engine, Start -> grant expired, youtube absent, @@ line
                   present; a second Reconcile writes 0 (Writes 0, RulesWritten 0)
                 - if ref-counting regresses, existing TestEngine_SharedExpiryKeepsOther passes unmodified

  t-6  Expose toggle state in the blocked API  [verification+risk]
       files:    internal/api/api.go, internal/api/blocked.go, internal/api/blocked_test.go
       covers:   c-5
       depends:  t-1, t-3
       desc:     ChildStore gains ListServiceToggles (main already wires the store as Children). handleBlocked
                 passes toggles to Compute and serves toggle (null when "") and unblocked_by[].permanent. A failed
                 toggles read leaves custom services out with a warn, toggle null, never a 500.
       contract: - if toggle state is not served, TestBlocked_Toggle fails: child unblocked youtube -> {state
                   "unblocked", toggle "unblocked"}; untoggled -> "toggle":null with the key present
                 - if other owners' unblocks are hidden, TestBlocked_PermanentUnblockedBy fails: Bob unblocked
                   custom:1, tv shared -> Ann's custom:1 partial, unblocked_by [{client:"tv", grant_id:0,
                   child:{id,name:"Bob"}, permanent:true}]
                 - if an outage lies or 500s, TestBlocked_TogglesDown fails: ListServiceToggles error -> 200,
                   built-ins with toggle null, no custom:* entries

  t-7  Searchable block picker component  [risk+verification]
       files:    web/src/lib/BlockPicker.svelte (new), web/src/lib/BlockPicker.test.ts (new)
       covers:   c-2
       depends:  t-4
       desc:     "Block a service…" button opens a search input over pickerOptions; picking calls onpick once,
                 disables options while pending, closes after it resolves; empty result shows a no-match message.
       contract: - if blocked services leak in, BlockPicker.test 'excludes blocked' fails: youtube blocked -> no
                   youtube row; options exactly the unblocked/partial set
                 - if search is case-sensitive or misses custom, BlockPicker.test 'search' fails: "TIK" -> only
                   TikTok; "por" -> custom "School Portal"; "zzz" -> no-match message
                 - if a pick double-fires, BlockPicker.test 'pick once' fails: two rapid clicks -> onpick once,
                   options disabled while pending, closed after resolve
                 - if custom rows render a broken image, BlockPicker.test 'custom avatar' fails: Portal shows "P", no <img>

Wave 3
  t-8  Engine Toggle: store, trim, write under mutex  [risk+verification+mvp]
       files:    internal/grants/toggle.go (new), internal/grants/toggle_test.go (new)
       covers:   c-1, c-2, c-3
       depends:  t-5
       desc:     Toggle under e.mu with one ApplyTimeout context: custom id must exist (*ErrUnknownService, nothing
                 stored); SetServiceToggle; stop an Ended grant's timer. Built-in: unblock -> Remove on every mapped
                 client (adguard skips unchanged lists); block -> Add per togglePlan over mapped ∪ trimmed grant's
                 stored clients. Custom or trimmed-custom -> syncRules. Missing/global/AdGuard error -> Failed.
       contract: - if unblock misses a client, TestToggle_UnblockAll fails: child on kid-phone, tablet, tv (shared) ->
                   youtube absent on all three, Applied true, Clients all three
                 - if unblock_during_grant writes or trims, TestToggle_UnblockDuringGrant fails: grant on youtube
                   over all the child's clients -> 0 /control/clients/update POSTs, services/ends_at unchanged, timer
                   armed; at expiry youtube stays absent
                 - if block_during_grant leaves it covered, TestToggle_BlockDuringGrant fails: grant [youtube,tiktok]
                   -> youtube present on every grant client in-call, tiktok absent, grant active [tiktok], timer armed;
                   expiry re-adds only tiktok
                 - if an emptied grant keeps its timer, TestToggle_BlockEndsGrant fails: grant [youtube] ->
                   Grant{Ended:true}, fakeClock.pending()==0, later Advance issues no update POST
                 - if a trimmed grant's old device stays open, TestToggle_BlockOldDevice fails: grant stored on
                   old-tablet (since unmapped) -> youtube added on old-tablet too
                 - if a block overrides a co-owner, TestToggle_BlockSharedHeldOpen fails: B unblocked youtube, A
                   blocks -> a-phone present, tv untouched and not in Failed
                 - if partial failure strands intent, TestToggle_PartialApply fails: SetUpdateStatus(tablet,500) ->
                   Applied false, Failed [tablet], row stored, nil error; clients read 502 -> Failed every client, 0
                   update POSTs; use_global client -> in Failed, global list unwritten
                 - if a custom toggle skips the region, TestToggle_Custom fails: unblock custom:1 -> one @@ line over
                   the child's clients, block line unchanged; block under a {custom:1} grant -> @@ gone, grant ended;
                   set_rules 500 or malformed region -> Applied false, Failed = child's clients, row stored, nil error
                 - if the custom check races delete, TestToggle_UnknownCustom fails: DeleteService(1) then Toggle
                   custom:1 (and custom:99) -> *ErrUnknownService, no row
                 - if the mutex is skipped, TestToggle_RaceExpiry fails: 50 runs of unblock youtube concurrent with
                   expiry -> youtube absent on every client every run
                 - if re-toggling rewrites, TestToggle_Idempotent fails: second identical unblock -> 0 update POSTs
                 - if the 5 s budget is unbounded, TestToggle_Deadline fails: Hang /control/clients/update 10 s with
                   default ApplyTimeout (4 s) -> returns within 4.5 s, Applied false

  t-9  Reconciler converges toggles; baseline respects them  [risk+verification]
       files:    internal/grants/reconcile.go, internal/grants/reconcile_test.go, internal/grants/clients.go,
                 internal/grants/clients_test.go
       covers:   c-1, c-2, c-4
       depends:  t-5
       desc:     Toggle half after the grant half (re-reading active grants) applies togglePlan per present,
                 non-global client, counted in Stats.Writes/Repaired. Any toggles/children/grants/clients read
                 failure skips the half. baselineFrom drops services an owner unblocked, adds the child's blocks.
       contract: - if drift is not undone, TestReconcile_ToggleDrift fails: youtube unblocked, MutateClient re-adds
                   it -> one pass removes it; tiktok blocked, parent removes it -> one pass re-adds it
                 - if a converged pass writes, TestReconcile_ToggleNoop fails: second pass -> 0 POSTs
                 - if AdGuard-owned services are touched, TestReconcile_UntoggledUntouched fails: no toggle rows,
                   parent changes netflix -> 0 update POSTs
                 - if the reconciler fights a grant, TestReconcile_BlockYieldsToGrant fails: A blocked youtube, C's
                   live grant covers shared tv -> tv keeps youtube absent across 3 passes
                 - if an overdue grant's services stay open, TestReconcile_OverdueThenBlock fails: overdue grant on
                   tiktok + block toggle on tiktok -> one pass leaves tiktok present
                 - if partial apply never converges, TestReconcile_ConvergesToggle fails: toggle row seeded via
                   store.SetServiceToggle with kid-tablet still carrying youtube, fault cleared -> one pass removes it
                   (store-seeded so this task does not need t-8)
                 - if a read failure converges from partial data, TestReconcile_ToggleReadFailure fails:
                   ListServiceToggles error -> 0 toggle writes, grant half and region half still run
                 - if a new device ignores toggles, TestCreateClient_BaselineToggles fails: youtube unblocked,
                   tiktok blocked, sibling still blocks youtube -> new client excludes youtube, includes tiktok;
                   same through AdoptClients

  t-10 Home: toggle controls, picker, refresh  [risk+verification+mvp]
       files:    web/src/lib/Home.svelte, web/src/lib/Home.test.ts
       covers:   c-1, c-2, c-5
       depends:  t-4, t-7
       desc:     Rows render controlFor's buttons; BlockPicker at the foot of the child's view. A toggle POSTs once
                 (inflight guard), then refetches that child's blocked view and active grants; applied=false ->
                 alert naming failed clients; shared -> notice naming devices and owners.
       contract: - if row controls are wrong, Home.test 'toggle controls' fails: blocked row only Unblock (click ->
                   POST state 'unblocked'); partial row both; toggled-unblocked row listed "unblocked" with only Block
                 - if today's hiding regresses, Home.test 'untoggled unblocked hidden' fails: unblocked, toggle null,
                   unblocked_by [] -> not listed
                 - if a toggle is not reversible, Home.test 'reverse' fails: Unblock youtube -> refetched row still
                   listed with Block; Block -> POST {state:'blocked'} and the refetched row shows Unblock
                 - if a double tap sends twice, Home.test 'toggle inflight' fails: two clicks before resolve -> one
                   /toggles fetch
                 - if the view goes stale, Home.test 'toggle refresh' fails: after resolve the blocked view and
                   GET /api/v1/grants are each fetched again
                 - if partial apply / shared / errors are hidden, Home.test 'toggle outcome' fails: {applied:false,
                   failed:['tablet']} -> role=alert naming tablet; shared [{tv,[B]}] -> status naming tv and B; 502 ->
                   role=alert, row unchanged
                 - if the picker is not wired, Home.test 'picker blocks' fails: open "Block a service…", pick TikTok
                   -> POST {service_id:'tiktok',state:'blocked'} then blocked view refetched
                 - if attribution reads as a grant, Home.test 'permanent attribution' fails: {tv, Bob, permanent}
                   renders "tv by Bob (permanent)", not "Bob's grant"

Wave 4
  t-11 Toggle endpoint with validation and shared report  [risk+verification+mvp]
       files:    internal/api/toggles.go (new), internal/api/toggles_test.go (new), internal/api/api.go,
                 cmd/adguard-reward/main_test.go
       covers:   c-1, c-2
       depends:  t-6, t-8
       desc:     POST /api/v1/children/{id}/toggles: bounded strict body; 404 unknown child before any AdGuard call;
                 built-in id must be in the live catalogue (422; catalogue down 502, nothing stored); engine Toggle;
                 200 {applied, failed, shared (sharedOf), grant}. Grants interface gains Toggle.
       contract: - if junk reaches blocked_services, TestToggle_UnknownService fails: "nope", "custom:", "custom:99"
                   -> 422, no row, 0 update POSTs
                 - if bad input is accepted, TestToggle_BadBody fails: state "open", missing service_id, unknown
                   field, two objects -> 400 each, no row
                 - if the unknown child touches AdGuard, TestToggle_UnknownChild fails: child 99 -> 404, 0 AdGuard requests
                 - if a catalogue outage stores intent, TestToggle_CatalogueDown fails: blocked_services/all 502 ->
                   502, ListServiceToggles empty
                 - if the shared report is wrong, TestToggle_SharedReport fails: tv co-owned by B -> shared
                   [{client:"tv", others:[{id:B,...}]}], present even when applied=false; none shared -> []
                 - if partial apply becomes an error, TestToggle_PartialIs200 fails: Failed [tablet] -> 200
                   {applied:false, failed:["tablet"]}
                 - if trims are not reported, TestToggle_GrantReport fails: block youtube under grant 7 {youtube} ->
                   grant {id:7, ended:true}; no grant involved -> grant null
                 - if CSRF/session gating is missing, TestToggle_Auth fails: no session -> 401, no CSRF -> 403, no row
                 - if wiring or the 5 s budget breaks, TestRun_ToggleEndToEnd fails: boot run(), POST unblock youtube
                   for a child on 2 clients -> response within 5 s and both lack youtube in the fake; POST block ->
                   both contain it
```

Coverage: c-1 t-4, t-8, t-9, t-10, t-11 · c-2 t-1, t-4, t-7, t-8, t-9, t-10, t-11 · c-3 t-2, t-5, t-8 · c-4 t-1, t-5, t-9 · c-5 t-3, t-4, t-6, t-10. 5/5.

Grafts not tied to a divergence:
- t-9 `ConvergesToggle` is re-seeded through the store. This repairs verification's wave error, so t-8 and t-9 stay parallel.
- t-8 `UnblockDuringGrant` asserts 0 POSTs while still issuing Remove on every client (risk's race argument). Both hold because `RemoveBlockedServices` skips an unchanged list (internal/adguard/clients.go:210, `slices.Equal`). So risk and verification did not actually diverge here.
- Verification's t-1 fixture (two active youtube grants for one child) is replaced by a single-grant fixture, because `grant_services_live_idx` forbids the original.

## Disagreements

1. **Toggle endpoint shape and unknown-service status.**
   - risk: `POST …/toggles {service_id, state}`, unknown service 422.
   - mvp: the same POST with field `service`, unknown service 400.
   - verification: `PUT …/services/{service}` with a path-escaped `custom%3A1`, unknown service 404.
   - Default: risk's POST + 422. It is 2-of-3 on the route, it keeps `custom:<n>` out of a path segment, and 422 matches the existing grant endpoint (internal/api/grants.go:245,259).
   - Why it matters: t-4 (SPA) and t-11 (API) run in different waves against the pinned wire. If the default is flipped, both tasks change together, or the SPA ships calling a route that 404s.

2. **How the response reports trimmed grants.**
   - risk: `grant:{id, ended}|null`.
   - verification: `trimmed_grants[]`, `ended_grants[]`.
   - mvp: nothing; the SPA always re-polls grants.
   - Default: risk's single object. `grant_services_live_idx` allows at most one active grant per (child, service), so the arrays describe a state that cannot exist, and verification's t-1 fixture built on it cannot be constructed.
   - Why it matters: the deferred grant-history phase records "ended by permanent block" from this field. Arrays would also invite tests against an impossible store state.

3. **Controls on a partial row.**
   - risk: `controlFor(partial) -> 'unblock'` only.
   - mvp and verification: partial rows get both Unblock and Block.
   - Default: both. c-2 says a parent can block a service that is "unblocked or partial". Under risk's rule, a partial service could only be blocked through the picker.
   - Why it matters: it is user-visible, and decides whether c-2 holds from the row itself.

4. **Which clients a block writes.**
   - verification: the child's mapped clients ∪ the trimmed grant's stored clients (`TestToggle_BlockOldDevice`).
   - mvp: mapped clients only.
   - risk: says "every grant client" in one test but "child clients" in its description. Ambiguous.
   - Default: verification.
   - Why it matters: trimming removes the grant's revert coverage, and the toggle reconciler works from current owners. A device unmapped from the child mid-grant would otherwise stay open for good: a silent permanent unblock that no criterion allows.

5. **Toggle cleanup when a custom service is deleted.**
   - risk: Go edit in `DeleteCustomService` (5th file in t-1).
   - verification: an `AFTER DELETE` trigger in 0008.
   - mvp: no cleanup. Ids are AUTOINCREMENT (confirmed in 0007_custom_services.sql) and never reused, so a dangling row is inert.
   - Default: the trigger. It is cleanup without the 5-file task, and the custom:1-vs-custom:10 contract pins it.
   - Why it matters: mvp's argument holds today. The dangling rows would surface in the deferred export-import phase, which serialises toggle state.

6. **New and adopted devices honouring toggles (`baselineFrom`).**
   - risk: includes it in t-9.
   - verification: explicitly rejects it; the next 60 s reconcile tick converges the device.
   - mvp: silent.
   - Default: include it (risk), as it costs two files inside t-9. Locked `drift_enforcement` says every pass converges, but between adoption and the next tick a new device blocks a permanently unblocked service.
   - Why it matters: it is up to 60 s of user-visible wrong state on every new device. If the judge of plan.toml prefers verification's scope, drop `clients.go`, `clients_test.go` and `TestCreateClient_BaselineToggles` from t-9; nothing else depends on them.

7. **Decomposition of the engine, API and SPA work.**
   - mvp: 6 tasks. Toggle + region + reconcile in one 5-file task; endpoint + view wiring in one 5-file task; client + Home + picker in one task.
   - risk: 11 tasks. verification: 10 tasks.
   - Default: risk/verification-style splits. mvp's t-5 and t-6 break the 5+-file rule, and one red test in a 3-concern task does not point at a surface.
   - Why it matters: the split adds tasks and review cycles in pair mode. mvp's merge has fewer handoffs but larger diffs per gate.

8. **How the blocked view reads toggles.**
   - risk: new `Deps.Toggles`, wired in `cmd/adguard-reward/main.go`, with a boot test.
   - verification: `ChildStore` gains `ListServiceToggles`. main already passes the store as `Children` (main.go:133), and `failingChildren` embeds `ChildStore`, so fakes still compile.
   - mvp: passthrough on the `Grants` engine interface.
   - Default: verification.
   - Why it matters: under risk's route, forgetting the main.go wiring makes production views silently lack toggle state while harness tests pass. The ChildStore route cannot be mis-wired. risk's end-to-end boot test is kept in t-11 as the backstop.
