# Risk-lens plan — 04-grants-scheduler-reconciler

Lens: every task is shaped around the ways a timed unblock gets stranded or
double-applied. The graph starts from what breaks — two POSTs racing for the
same (child, service), a revert that half-succeeds, a timer that fires at an
ends_at that /extend already moved, a reconciler pass that re-unblocks a grant
/end is reverting at the same instant, a restart that forgets every timer, a
POST that hangs on a slow AdGuard, a client the parent deleted in AdGuard mid-
grant — and each of those has exactly one owner below. The unifying rule: every
AdGuard write on behalf of a grant goes through one engine mutex, and every
status transition is a compare-and-set in the store, so there is never a second
decider.

Phase 04-grants-scheduler-reconciler — 6 tasks across 4 waves

## Wave 1

  t-1  Grants schema and CAS store queries
       files:    internal/store/migrations/0003_grants.sql,
                 internal/store/grants.go, internal/store/grants_test.go
       covers:   c-1, c-2, c-6
       depends_on: []
       description:
         0003_grants.sql: grants(id INTEGER PRIMARY KEY, child_id INTEGER NOT
         NULL, status TEXT NOT NULL CHECK (status IN ('active','expired',
         'ended')), started_at INTEGER NOT NULL, ends_at INTEGER NOT NULL,
         ended_at INTEGER) — deliberately NO foreign key to children: a grant
         must outlive its child so the revert still runs (locked: client_set);
         grant_services(grant_id INTEGER NOT NULL REFERENCES grants(id) ON
         DELETE CASCADE, child_id INTEGER NOT NULL, service_id TEXT NOT NULL,
         active INTEGER NOT NULL, PRIMARY KEY (grant_id, service_id)) with
         CREATE UNIQUE INDEX grant_services_live_idx ON grant_services
         (child_id, service_id) WHERE active = 1 as the overlap safety net;
         grant_clients(grant_id INTEGER NOT NULL REFERENCES grants(id) ON
         DELETE CASCADE, client_name TEXT NOT NULL, PRIMARY KEY (grant_id,
         client_name)); index grants(status, ends_at). grants.go: Grant{ID,
         ChildID int64, Status string, Services, Clients []string (sorted,
         never nil), StartedAt, EndsAt time.Time}; consts StatusActive/
         StatusExpired/StatusEnded; sentinels ErrGrantNotFound and typed
         *ErrGrantOverlap{ExistingID int64, ServiceID string}.
         CreateGrant(ctx, childID, services, clients []string, startedAt,
         endsAt time.Time) (Grant, error): one inTx — SELECT the live index
         for each (child, service) → *ErrGrantOverlap naming the first
         existing grant, INSERT grants + grant_services(active=1) +
         grant_clients; services and clients normalised like
         normaliseClients. ListActiveGrants(ctx) ([]Grant, error) id ASC,
         status='active' only, folded from one JOIN over grant_services and
         one over grant_clients scanned to completion before the next query
         (single-connection rule in store.go). GetGrant(ctx, id).
         ExtendGrant(ctx, id, newEndsAt) (Grant, error): UPDATE ... WHERE id=?
         AND status='active', RowsAffected 0 → ErrGrantNotFound.
         SetGrantStatus(ctx, id, from, to string, at time.Time) (bool, error):
         one tx that UPDATEs grants WHERE id=? AND status=from and, when a
         row changed, UPDATEs grant_services SET active=0 for that grant;
         returns false when nothing matched — this CAS is the only way a
         grant leaves 'active', so the timer, /end and the reconciler can
         never both "win".
       test_contract:
         - if the overlap check or the partial unique index is dropped,
           TestGrants_OverlapPerChildService fails: CreateGrant(ada, ["youtube",
           "tiktok"], ...) then CreateGrant(ada, ["tiktok"], ...) returns
           *ErrGrantOverlap with ExistingID == first id and ServiceID
           "tiktok" and the grants table still has one row; CreateGrant(ben,
           ["tiktok"], ...) succeeds (a different child is not an overlap);
           PRAGMA index_list('grant_services') shows grant_services_live_idx
           partial and unique
         - if a finished grant still blocks new ones, TestGrants_OverlapClears
           fails: after SetGrantStatus(id, active, expired) → (true, nil),
           CreateGrant(ada, ["tiktok"], ...) succeeds; the same after
           active→ended
         - if the CAS is not conditional, TestGrants_StatusCAS fails:
           SetGrantStatus(id, active, expired) → true; a second
           SetGrantStatus(id, active, ended) → false and GetGrant still says
           expired; SetGrantStatus(999, active, expired) → (false, nil), not
           an error
         - if ListActiveGrants leaks non-active rows or misorders,
           TestGrants_ListActiveOnly fails: three grants, mark one expired and
           one ended → exactly the remaining one comes back with its full
           Services and Clients lists; an empty store returns a non-nil empty
           slice; ids ASC when several are active
         - if ExtendGrant touches non-active rows, TestGrants_ExtendActiveOnly
           fails: ExtendGrant(activeID, t+30m) returns the grant with EndsAt
           == t+30m; ExtendGrant(endedID, ...) → ErrGrantNotFound and its
           ends_at is unchanged; ExtendGrant(999, ...) → ErrGrantNotFound
         - if the client list is derived instead of stored, TestGrants_
           ClientsFrozen fails: CreateGrant with clients ["Kid phone","Kid
           tablet"] then DeleteChild(ada) (no FK, so it succeeds) → GetGrant
           still returns both client names and child_id == ada's old id
         - if the insert is not atomic, TestGrants_CreateRollsBack fails: a
           CreateGrant whose second service overlaps leaves no grants,
           grant_services or grant_clients rows for the attempted grant
         - if a nested query is issued while rows are open, TestGrants_
           ListRace fails under -race: 10 goroutines calling ListActiveGrants
           while another creates/expires grants for 20 cycles complete within
           5 s with no deadlock and every returned grant carries either the
           full pre- or post-cycle service list, never a partial one
         - if rows do not persist, TestGrants_Persist fails: CreateGrant,
           Close, Open on the same dir, ListActiveGrants returns it with
           identical id, EndsAt (second precision) and lists, and
           schema_migrations lists 0003_grants.sql exactly once

  t-2  AdGuard set-difference/union writes and fake helpers
       files:    internal/adguard/clients.go, internal/adguard/clients_test.go,
                 internal/adguard/adguardtest/server.go,
                 internal/adguard/adguardtest/server_test.go
       covers:   c-1, c-3
       depends_on: []
       description:
         clients.go: RemoveBlockedServices(ctx, clientName string, ids
         []string) error and AddBlockedServices(ctx, clientName string, ids
         []string) error, both on updateClient so the list is re-read under
         c.rmw and edited in place: Remove = current list minus ids, Add =
         sorted union(current, ids), written as [] never null; every other
         field goes back verbatim; a client on the global list still gets the
         warn line SetBlockedServices already emits. Neither ever uses
         SetBlockedServices' blind replace, so a parent's concurrent edit to
         an unrelated id survives. adguardtest gains BlockedServices(name)
         []string (the stored list, decoded; panics on unknown name),
         RemoveClient(name) (drops the client so a later GET /control/clients
         omits it and /update answers 400 for it — "parent deleted the
         device in AdGuard"), and UpdateCount() int (POSTs to
         /control/clients/update so far), so engine tests can assert "no
         writes" without scanning Requests().
       test_contract:
         - if Add is not a set-union, TestAddBlockedServices_Union fails:
           MutateClient("Kid phone") sets blocked_services ["youtube",
           "roblox"] (the parent added roblox after our read); AddBlocked-
           Services("Kid phone", ["tiktok","youtube"]) leaves the fake's
           BlockedServices("Kid phone") == ["roblox","tiktok","youtube"] and
           the update body's future_field is still 42
         - if Add is not idempotent, TestAddBlockedServices_Idempotent fails:
           calling it twice with the same ids yields the same list and the
           second body equals the first byte-for-byte
         - if Remove is not a set-difference, TestRemoveBlockedServices_Diff
           fails: "Kid phone" (fixture ["youtube","tiktok"]) after
           RemoveBlockedServices(["tiktok","nonexistent"]) has ["youtube"];
           removing from "Old laptop" (fixture null) writes [] not null and
           does not error
         - if a missing client is written anyway, TestRemoveAdd_NotFound
           fails: both calls against "Nobody" return errors.Is(err,
           ErrClientNotFound) and UpdateCount() is 0
         - if the two writers escape the mutex, TestSetBlockedServices_
           Serialised (extended) fails: Hang("/control/clients/update", 30ms)
           with 10 goroutines mixing Add/Remove/SetBlockedServices leaves
           MaxInFlightUpdates() == 1
         - if the fake helpers lie, TestFake_GrantHelpers fails:
           RemoveClient("Kid tablet") makes GET /control/clients return two
           clients and POST /update for "Kid tablet" answer 400;
           BlockedServices("Kid phone") reflects a MutateClient edit;
           UpdateCount() increments once per POST /control/clients/update
           and not on GETs

## Wave 2 (depends t-1, t-2)

  t-3  Grant engine: create, timer expiry, extend, end
       files:    internal/grants/grants.go, internal/grants/grants_test.go
       covers:   c-1, c-3, c-6
       depends_on: [t-1, t-2]
       description:
         Package grants owns every AdGuard write made for a grant. Interfaces
         Store (CreateGrant, ListActiveGrants, GetGrant, ExtendGrant,
         SetGrantStatus — satisfied by *store.Store) and AdGuard (Clients,
         RemoveBlockedServices, AddBlockedServices — satisfied by
         *adguard.Client). Options{Now func() time.Time, AfterFunc func(d
         time.Duration, f func()) Timer, ApplyTimeout time.Duration (default
         4 s), Log}; Timer is interface{ Stop() bool }; defaults time.Now and
         time.AfterFunc. Engine{mu sync.Mutex; timers map[int64]Timer}. mu is
         held for the whole read-decide-write of every operation below, so
         two deciders never interleave. Create(ctx, childID, services,
         clients []string, d time.Duration) (Result{Grant, Applied bool,
         Failed []string}, error): store.CreateGrant (overlap →
         *store.ErrGrantOverlap passed through), then under a ctx bounded by
         ApplyTimeout RemoveBlockedServices on each stored client in order;
         a client that errors (incl. ErrClientNotFound and deadline) is
         appended to Failed and the loop continues; the grant is never rolled
         back (locked: partial_apply); arm timers[id] = AfterFunc(EndsAt-
         Now(), func(){ e.expire(id) }). expire(id): under mu, GetGrant; if
         not active or Now() < EndsAt (extended meanwhile) → re-arm and
         return; revert(grant): AddBlockedServices on every stored client;
         ErrClientNotFound counts as reverted (nothing left to restore on a
         deleted device); any other failure → leave active, log warn, no
         status change (the reconciler owns the retry); all ok →
         SetGrantStatus(active→expired), delete timers[id]. Extend(ctx, id,
         d) (Grant, error): under mu, ExtendGrant(EndsAt+d) (ErrGrantNotFound
         passed through), Stop the old timer, arm a new one. End(ctx, id)
         error: under mu, GetGrant, not active → ErrGrantNotFound; revert; if
         any client failed → return *ErrRevertFailed{Clients} and leave the
         grant active with its timer; else SetGrantStatus(active→ended), stop
         timer. ArmAll(grants) arms a timer per active grant (used by t-4's
         startup pass). Tests use a fake clock whose Advance(d) fires due
         timers synchronously on the calling goroutine.
       test_contract:
         - if Create does not unblock, TestEngine_CreateUnblocks fails: child
           clients ["Kid phone","Kid tablet"], services ["tiktok"] → fake
           BlockedServices("Kid phone") == ["youtube"] and "Kid tablet" == [],
           Result.Applied true, Failed == []string{}, Grant.EndsAt == t0+d
         - if partial failure rolls back or aborts, TestEngine_PartialApply
           fails: SetUpdateStatus("Kid tablet", 500) → Create returns nil
           error, Applied false, Failed ["Kid tablet"], "Kid phone" is
           unblocked, and ListActiveGrants holds the grant
         - if the apply step is unbounded, TestEngine_CreateDeadline fails:
           Hang("/control/clients/update", 8s) with ApplyTimeout 1 s → Create
           returns within 2 s (wall clock), Applied false, Failed names the
           hung client, grant row exists
         - if the timer does not revert, TestEngine_TimerReverts fails:
           Advance(d) fires the timer; fake lists show "tiktok" back on both
           clients; GetGrant.Status == expired; a second revert of the same
           grant (call expire directly) issues zero further POST
           /control/clients/update (UpdateCount unchanged) and the lists are
           identical (set-union idempotence)
         - if a failed revert marks expired, TestEngine_RevertFailureStaysActive
           fails: SetUpdateStatus("Kid phone", 500), Advance(d) → status still
           active, "Kid tablet" re-blocked, "Kid phone" not, the log carries
           a warn naming "Kid phone"; clearing the fault and calling
           expire(id) again re-blocks "Kid phone" and marks expired
         - if a deleted device wedges the grant, TestEngine_MissingClientExpires
           fails: fake.RemoveClient("Kid tablet") after Create, Advance(d) →
           "Kid phone" re-blocked and the grant is expired (not stuck active)
         - if /extend does not reschedule, TestEngine_ExtendReschedules fails:
           Extend(id, 10m) → EndsAt == t0+d+10m; Advance(d) (old deadline)
           → status active, UpdateCount unchanged (the stale timer re-armed
           instead of reverting); Advance(10m) → expired and re-blocked;
           Extend on an expired id → ErrGrantNotFound
         - if /end and the timer both decide, TestEngine_EndVsTimerRace fails
           under -race: Hang("/control/clients/update", 20ms); End(id) on
           one goroutine while Advance(d) fires the timer on another → after
           both return the grant is exactly one of ended/expired, both
           clients list "tiktok" once, and no goroutine returned a
           "not active" error for a grant it had already reverted
         - if End reports success on a failed revert, TestEngine_EndRevert-
           Failure fails: SetUpdateStatus("Kid phone", 500) → End returns
           *ErrRevertFailed with Clients ["Kid phone"], the grant is still
           active, "Kid tablet" is re-blocked; End(id) on an ended grant →
           ErrGrantNotFound

## Wave 3 (depends t-3)

  t-4  Reconciler pass, drift repair, startup pass
       files:    internal/grants/reconcile.go, internal/grants/reconcile_test.go
       covers:   c-4, c-5
       depends_on: [t-3]
       description:
         Reconcile(ctx) (Stats{Grants, Reverted, Repaired, Writes int}, error)
         on Engine, under mu: ListActiveGrants; if empty return without
         touching AdGuard. Otherwise ONE Clients() read for the pass (never
         one per grant); for each grant in id order: Now() >= EndsAt →
         revert+CAS exactly as expire does; else for each stored client
         present in the read, ids := granted ∩ client.BlockedServices, and if
         non-empty RemoveBlockedServices(client, ids) (one write per
         drifted client, none otherwise); a client absent from the read is
         skipped with a debug line; a client whose UseGlobalBlockedServices
         is set is compared against its own list only (the global list is
         never written, phase-03 lock); any grant with no armed timer gets
         one (post-restart). A Clients() error aborts the pass with no
         status change and no writes. Start(ctx) error: one Reconcile plus
         ArmAll, bounded by ctx; errors are returned for main to log, never
         fatal. Run(ctx, interval): ticker loop calling Reconcile, logging
         errors, exiting on ctx.Done; passes never overlap (mu).
       test_contract:
         - if drift is not repaired, TestReconcile_DriftRepaired fails: grant
           ["tiktok"] on ["Kid phone","Kid tablet"]; MutateClient("Kid phone")
           puts "tiktok" back (parent re-blocked in UI); Reconcile → "Kid
           phone" list is ["youtube"], Stats.Repaired 1, UpdateCount rose by
           exactly 1 (Kid tablet untouched), exactly one GET /control/clients
           in the pass
         - if a quiet pass still writes, TestReconcile_NoopNoWrites fails: an
           active, undrifted grant → Reconcile issues zero POST
           /control/clients/update and exactly one GET /control/clients; no
           active grants → zero requests of any kind
         - if the missed-timer path is absent, TestReconcile_RevertsOverdue
           fails: an engine built with AfterFunc that never fires; Advance
           past EndsAt; Reconcile → both clients re-blocked, status expired,
           Stats.Reverted 1
         - if AdGuard being down changes state, TestReconcile_AdGuardDown
           fails: SetStatus("/control/clients", 500) on an overdue grant →
           Reconcile returns an error, status still active, UpdateCount
           unchanged; clearing the fault and calling Reconcile again reverts
           and expires it
         - if the reconciler can re-unblock a grant /end is reverting,
           TestReconcile_EndRace fails under -race: Hang("/control/clients",
           30ms); Reconcile on one goroutine and End(id) on another → after
           both return, the grant is ended and "tiktok" is on both clients'
           lists (the pass either ran before End and found nothing, or after
           and saw no active grant — never in between)
         - if the startup pass forgets timers, TestReconcile_StartArmsTimers
           fails: rows created via the store directly (no engine), one
           overdue and one live; Start → the overdue one is reverted+expired,
           the live one is re-verified (a drifted id removed) and Advance to
           its EndsAt fires a revert with no reconciler call
         - if the deleted-device case wedges the pass, TestReconcile_Missing-
           Client fails: RemoveClient("Kid tablet"); an overdue grant on both
           → "Kid phone" re-blocked, status expired, no error returned
         - if passes overlap or the loop leaks, TestReconcile_RunLoop fails:
           Run with interval 20 ms and Hang("/control/clients", 50 ms) for
           300 ms → MaxInFlightUpdates() stays ≤ 1 and the fake never sees
           two concurrent GET /control/clients from the reconciler; cancelling
           ctx returns Run within 100 ms

  t-5  Grants HTTP endpoints, validation, Deps wiring
       files:    internal/api/grants.go, internal/api/grants_test.go,
                 internal/api/api.go, internal/api/errors.go,
                 internal/api/login_test.go
       covers:   c-1, c-2, c-6
       depends_on: [t-3]
       description:
         errors.go: CodeUnprocessable = "unprocessable" (422). api.go: Grants
         interface (Create, List, Extend, End over *grants.Engine's
         signatures), Deps.Grants, routes GET/POST /api/v1/grants, POST
         /api/v1/grants/{id}/extend, POST /api/v1/grants/{id}/end, all behind
         requireSession. grants.go: body {child_id int64, services []string,
         duration int (seconds)} decoded like decodeChild (16 KiB, no unknown
         fields, single object → 400); duration outside [60, 86400] → 422;
         services trimmed/deduped, empty after normalisation → 422; child
         looked up via Children.GetChild first (404 not_found before any
         AdGuard call); ids validated against AdGuard.Services (unknown id →
         422 naming it; catalogue unreachable → 502); Create → 201
         {id, child_id, services, clients, started_at, ends_at (RFC3339
         UTC), applied, failed}; *store.ErrGrantOverlap → 409 {error:
         "conflict", message, grant_id}. GET → {grants: [grantView...]} from
         List. extend: body {duration} same bounds → 200 grantView;
         ErrGrantNotFound and non-numeric id → 404. end: 204;
         ErrGrantNotFound → 404; *grants.ErrRevertFailed → 502
         adguard_unavailable naming the clients (grant stays active).
         login_test.go: newHarness builds a *grants.Engine on the harness
         store and fake with Now: c.Now and passes Grants: eng.
       test_contract:
         - if any route escapes auth/CSRF, TestGrants_Gated fails: all four
           routes without a cookie → 401; the three POSTs with a cookie but no
           X-Requested-With → 403 and the grants table is empty afterwards
         - if the contract drifts, TestGrants_CreateAndList fails: POST
           {child_id: ada, services: ["tiktok"], duration: 1800} → 201 with
           id > 0, clients == ada's clients, applied true, failed [], ends_at
           == started_at + 30m; fake BlockedServices("Kid phone") lacks
           "tiktok"; GET /api/v1/grants lists exactly it with the same
           fields; after SetGrantStatus(id, active, expired) via the store,
           GET returns {"grants":[]} (raw body)
         - if validation is loose, TestGrants_Validation fails: duration 59 →
           422, 86401 → 422, 60 and 86400 → 201; services ["nope"] → 422
           whose message contains "nope"; services [] and [""] → 422;
           {child_id: 999,...} → 404 and fake.Requests() contains no
           /control/blocked_services/all for it (child checked first);
           unknown field / non-object / 20 KiB body → 400; no grant row is
           created by any 4xx
         - if overlap is not surfaced with the id, TestGrants_Overlap409
           fails: second POST for ada ["tiktok","roblox"] → 409 with body
           .error "conflict" and .grant_id == first id; a POST for ben
           ["tiktok"] → 201
         - if partial apply is hidden, TestGrants_PartialApplyBody fails:
           SetUpdateStatus("Kid tablet", 500) → 201 with applied false and
           failed ["Kid tablet"], and GET lists the grant
         - if the catalogue outage is misreported, TestGrants_CatalogueDown
           fails: SetStatus("/control/blocked_services/all", 500) → POST is
           502 adguard_unavailable and no row is created
         - if extend/end 404s are wrong, TestGrants_ExtendEnd fails: extend
           {duration: 600} → 200 with ends_at pushed by 10 m from the previous
           ends_at (not from now); extend {duration: 30} → 422; end → 204 and
           BlockedServices("Kid phone") lists "tiktok" again; extend and end
           on that id → 404; /grants/abc/end and /grants/999/extend → 404
         - if a failed end is reported as done, TestGrants_EndRevertFails
           fails: SetUpdateStatus("Kid phone", 500) → POST end is 502 whose
           message contains "Kid phone", GET still lists the grant active

## Wave 4 (depends t-4, t-5)

  t-6  Wire engine into main; restart and drift proofs
       files:    cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go
       covers:   c-1, c-4, c-5
       depends_on: [t-4, t-5]
       description:
         main.go: var reconcileInterval = 60 * time.Second (test-overridable
         like sweepInterval); after store.Open build eng := grants.New(st,
         client, grants.Options{Log: log}); eng.Start(ctx) runs BEFORE
         net.Listen — its error is logged ("startup reconcile", err) and
         never fatal, bounded by a 30 s context so an unreachable AdGuard
         cannot hold the listener closed; pass Grants: eng in api.Deps; go
         eng.Run(ctx, reconcileInterval) beside the prober and sweeper.
         main_test.go: TestRun_GrantRestoredAfterRestart modelled on
         TestRun_ChildrenSurviveRestart — run, login, POST child + grant,
         stop (exit 0), open the store on the same data_dir and UPDATE
         grants SET ends_at = now-60 (time passes while the process is
         down), start again on the same port with the fake's lists still
         showing the unblock; TestRun_StartupPassBeforeListen;
         TestRun_ReconcilerDrift with reconcileInterval = 50 ms;
         TestRun_StartupAdGuardDown.
       test_contract:
         - if the startup pass is missing or ordered after Listen,
           TestRun_GrantRestoredAfterRestart fails: on the second run, by the
           time onListen fires the fake's BlockedServices("Kid phone")
           already contains "tiktok" (asserted inside the onListen callback,
           before any HTTP call) and GET /api/v1/grants with the old cookie
           returns {"grants":[]}
         - if a live grant loses its timer across restart, TestRun_
           LiveGrantRearmed fails: restart with ends_at rewound to now+2 s;
           within 5 s of listening the fake shows "tiktok" re-blocked with
           no reconciler tick having run (reconcileInterval = 1 h in this
           test)
         - if the reconciler is not wired or its interval is not the var,
           TestRun_ReconcilerDrift fails: reconcileInterval = 50 ms; after
           POST grant, MutateClient("Kid phone") re-adds "tiktok"; within 2 s
           the fake shows it removed again; with no drift the fake sees zero
           POST /control/clients/update over 500 ms
         - if AdGuard being down at boot blocks startup, TestRun_
           StartupAdGuardDown fails: fake.SetStatus("/control/clients", 500)
           with an overdue grant in the DB → start() returns a listening addr
           within 10 s, stderr contains "startup reconcile", the grant is
           still active; SetResponse(..., nil) clears it and with
           reconcileInterval = 50 ms the block is restored within 2 s
         - if the row does not survive the process, the first half of
           TestRun_GrantRestoredAfterRestart fails: GET /api/v1/grants after
           a restart with an un-rewound ends_at lists the grant with the
           same id, services and clients

## Coverage

| criterion | tasks |
|---|---|
| c-1 | t-1 (row, overlap, persist), t-2 (unblock write), t-3 (Create applies, deadline), t-5 (201/404/422/409 contract), t-6 (survives restart end-to-end) |
| c-2 | t-1 (ListActiveGrants excludes expired/ended), t-5 (GET shape) |
| c-3 | t-2 (set-union idempotent), t-3 (timer revert, failed write stays active) |
| c-4 | t-4 (Start: overdue reverted, live re-verified, timers armed), t-6 (before-listen ordering, restart proof) |
| c-5 | t-4 (single read per pass, drift repair, missed timer, no-op = no writes, no overlap), t-6 (60 s var wiring, drift end-to-end) |
| c-6 | t-1 (Extend CAS on active only), t-3 (reschedule, end revert, end-vs-timer race), t-5 (404s, 422 bounds, 502 on failed end) |

Every criterion has one owning test surface per failure mode; where two tasks
list the same criterion they own different failure modes of it (store vs
engine vs HTTP vs process), never the same one.

## Judgment calls

- **One engine mutex around read-decide-write, not just the adguard rmw lock.**
  Chose: Engine.mu held across Create/expire/Extend/End/Reconcile. Rejected:
  per-call locking only (adguard.Client.rmw). Why: rmw serialises single
  writes, but the reconciler-vs-/end race (t-4's TestReconcile_EndRace) is a
  decision race — the pass reads "active", /end reverts, the pass re-unblocks.
  Only a lock over the decision closes it.
- **Status changes are a store-level CAS (SetGrantStatus from→to).** Rejected:
  plain UPDATE status. Why: timer and /end can both revert the same grant;
  set-union makes the double write harmless, the CAS makes the double mark
  impossible and gives a single audit answer (ended vs expired).
- **No foreign key from grants to children.** Rejected: FK with CASCADE (drops
  the grant → stranded unblock, the exact core-value failure) and FK RESTRICT
  (silently changes phase-03's DELETE /children into a 500). Why: locked
  client_set already says the revert targets the stored client list, so a
  grant needs nothing from its child after creation.
- **Overlap enforced in the tx with a partial UNIQUE index as safety net.**
  Chose: SELECT-in-tx (the single-connection pool serialises) + `WHERE active
  = 1` index maintained by the CAS. Rejected: index only (loses the typed
  error naming the existing id the 409 body must carry) or SELECT only (no
  net if a future writer bypasses the store).
- **A deleted AdGuard client counts as reverted.** Rejected: leave the grant
  active forever (reconciler logs every 60 s, grant never expires, the
  child/service pair is locked against new grants by the overlap rule). Why:
  nothing is stranded on a device that no longer exists; the grant must still
  free the (child, service) slot.
- **Failed /end → 502 and the grant stays active.** Rejected: mark ended and
  add an "unreverted" flag the reconciler sweeps. Why: mirrors c-3's rule for
  the timer (failed write leaves it active), keeps "active" the only state
  the reconciler has to reason about, and the parent gets an honest error
  rather than a silent queue.
- **ApplyTimeout lives in the engine (default 4 s), not the handler.** Why:
  c-1's 5 s bound is about not hanging the phone on a slow AdGuard; the
  engine owns the loop over clients, so it owns cutting it short and
  reporting the unreached clients as failed (locked: partial_apply).
- **Reconciler does one Clients() read per pass, and none when no grant is
  active.** Rejected: read per grant. Why: c-5's "no writes on a quiet pass"
  is the criterion; a per-grant read would also multiply AdGuard load by the
  number of grants every minute for nothing.
- **duration is an integer of seconds; times are RFC3339 UTC.** Rejected: Go
  duration strings ("30m"). Why: unambiguous for the phone client in phase
  05 and trivially bounded (60..86400) without a parser.
- **Restart test rewinds ends_at in the SQLite file between runs.** Rejected:
  a test-only clock override in main. Why: the minimum duration is 60 s so no
  real grant can expire inside the test; editing the row is exactly "time
  passed while the process was down" and exercises the real startup pass
  with no production hook.
- **Extend has per-call bounds only, no cap on total length.** Why: the spec
  bounds "duration" for POST and extend; a total cap is neither locked nor a
  criterion, and inventing one would surprise phase 05's extend offer.
