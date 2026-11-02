# MVP lens — 04-grants-scheduler-reconciler

Phase 04-grants-scheduler-reconciler — 4 tasks across 4 waves

Bias applied: one task per layer, strictly in dependency order, nothing that is not
named by a criterion or a locked decision. No new adguard client method, no new
adguardtest feature, no api-local interface for the engine — the existing fake
server (`SetUpdateStatus`, `MutateClient`, `Requests`, `LastUpdate`) and the
existing `SetBlockedServices` read-modify-write already cover every test the
criteria demand.

Wave 1
  t-1  Add grants schema and store queries
       files:    internal/store/migrations/0003_grants.sql
                 internal/store/grants.go
                 internal/store/grants_test.go
       covers:   c-1, c-2, c-6
       depends_on: []
       description:
         Migration `grants(id PK, child_id INTEGER NOT NULL, services TEXT NOT NULL,
         clients TEXT NOT NULL, status TEXT NOT NULL, started_at INTEGER, ends_at INTEGER)`
         with an index on (status, ends_at); services/clients are sorted JSON arrays,
         status is 'active' | 'expired' | 'ended'. No FK on child_id (locked client_set:
         deleting a child must not strand a revert). Store methods on *Store:
         `CreateGrant(ctx, childID, services, clients, startedAt, endsAt) (Grant, error)`
         (inside inTx: loads the child's active grants, returns `*ErrGrantOverlap{ID}`
         if any shares a service — locked overlapping_grants), `ListActiveGrants(ctx)`,
         `GetActiveGrant(ctx, id)` (ErrGrantNotFound when missing or not active),
         `ExtendGrant(ctx, id, endsAt) (Grant, error)` and `SetGrantStatus(ctx, id, status)
         (bool, error)`, both `WHERE status = 'active'`. Sentinels: `ErrGrantNotFound`,
         `*ErrGrantOverlap`.
       contract:
         - a second CreateGrant for the same child sharing one service id returns
           *ErrGrantOverlap whose ID is the first grant's id; a disjoint service set succeeds
         - after SetGrantStatus(id, "expired") (and likewise "ended") ListActiveGrants no
           longer returns the row and GetActiveGrant returns ErrGrantNotFound
         - ExtendGrant on an ended grant returns ErrGrantNotFound and leaves ends_at unchanged
         - Close then store.Open on the same data_dir lists the same grant with identical
           services[], clients[] and ends_at (row survives, 0003 applied once)

Wave 2 (depends t-1)
  t-2  Grants engine: apply, timers, reconciler
       files:    internal/grants/grants.go
                 internal/grants/grants_test.go
       covers:   c-1, c-3, c-4, c-5, c-6
       depends_on: [t-1]
       description:
         `grants.Manager` = store + an `AdGuard` interface {Clients, SetBlockedServices}
         + logger + `time.AfterFunc` timers keyed by grant id. `Create(ctx, childID,
         services, duration) (Result, error)`: GetChild (ErrChildNotFound passes through),
         CreateGrant with the child's current clients as the stored list, then one
         Clients() read and, per stored client, SetBlockedServices(current minus granted);
         per-client failures are collected into Result.Failed with Applied=false, the grant
         stays active (locked partial_apply); schedules the expiry timer. `Extend(ctx, id,
         duration)`: ExtendGrant + stop/re-arm timer. `End(ctx, id)`: revert then
         SetGrantStatus ended. `Reconcile(ctx) error`: one pass — ListActiveGrants; if
         empty return with no AdGuard call; else one Clients() read, then per grant:
         past ends_at → revert (set-union over stored clients, write only clients whose
         list actually changes) + mark expired, stop timer; live → for each stored client
         present in AdGuard whose blocked_services still contains a granted id, write
         current minus granted; arm a timer for any live grant without one. `Run(ctx,
         interval)` ticks Reconcile like auth.RunSweeper. The timer callback is the same
         revert; a failed write logs and leaves status active. Missing clients (not in
         AdGuard) are logged and skipped. Duration bounds are NOT checked here (see t-3)
         so tests can use millisecond durations.
       contract:
         - Create for a child with clients [Kid phone, Kid tablet] and services [youtube]
           makes the fake's GET /control/clients show neither client blocking youtube, with
           exactly two POST /control/clients/update recorded
         - a 50 ms grant: within 1 s the fake shows youtube back in both clients'
           blocked_services and GetActiveGrant returns ErrGrantNotFound (status expired);
           calling the revert again records zero additional /control/clients/update (idempotent)
         - SetUpdateStatus("Kid phone", 500) before expiry → after the timer fires the grant
           is still active; clearing the fault and calling Reconcile restores the block and
           marks it expired (failed write leaves active, reconciler retries)
         - MutateClient re-adding youtube to Kid phone mid-grant → Reconcile writes one update
           removing it; an immediately following Reconcile with nothing to do adds zero
           /control/clients/update to fake.Requests()
         - a grant inserted through the store with ends_at in the past and no timer is
           reverted and marked expired by a single Reconcile (the "timer missed" path)
         - SetUpdateStatus("Kid tablet", 500) during Create → Result.Applied == false,
           Result.Failed == ["Kid tablet"], the grant is active and Kid phone is unblocked;
           clearing the fault and calling Reconcile unblocks Kid tablet
         - Extend on a 50 ms grant to 500 ms: at 200 ms the block is still lifted and the
           grant active; End reverts immediately and GetActiveGrant returns ErrGrantNotFound

Wave 3 (depends t-2)
  t-3  Grants HTTP endpoints
       files:    internal/api/grants.go
                 internal/api/grants_test.go
                 internal/api/api.go
                 internal/api/errors.go
       covers:   c-1, c-2, c-6
       depends_on: [t-2]
       description:
         `Deps.Grants *grants.Manager`; routes POST/GET /api/v1/grants, POST
         /api/v1/grants/{id}/extend, POST /api/v1/grants/{id}/end, all behind
         requireSession. Body {child_id, services[], duration} with duration an integer
         number of seconds; decoded like decodeChild (bounded, no unknown fields). 422
         (`CodeUnprocessable = "unprocessable"`, added to errors.go) for an empty or
         unknown service id (checked against AdGuard.Services(), 502 if that read fails)
         or duration outside [60, 86400] s (locked duration_bounds; the bounds live here
         as `grants.MinDuration`/`MaxDuration` consts referenced from the handler). 404
         for store.ErrChildNotFound, 409 `{error: conflict, message, grant_id}` for
         *ErrGrantOverlap. 201 `{id, ends_at, applied, failed_clients[]}` (ends_at RFC3339).
         GET → `{grants: [{id, child_id, services, clients, started_at, ends_at}]}`.
         extend → 200 `{id, ends_at}`; end → 204; both 404 on ErrGrantNotFound or a
         non-numeric id. The harness constructs a real grants.Manager over its store and
         fake, as newHarness already does for the other Deps.
       contract:
         - POST with child_id 999 → 404; services ["not_a_service"] → 422; duration 59 and
           86401 → 422; duration 60 → 201 whose ends_at is within 2 s of now+60 s
         - POST overlapping an active grant on the same (child, service) → 409 whose
           grant_id equals the first grant's id; the same service for a different child → 201
         - GET /api/v1/grants lists the created grant with clients[] equal to the child's
           clients at creation; after POST .../end it is absent and a second .../end → 404
         - POST .../extend {duration: 60} → ends_at in the response is exactly 60 s later
           than GET reported before; extend on an unknown id → 404
         - every grants route without a session cookie → 401 (same table-driven gate as
           TestChildren_Gated)

Wave 4 (depends t-3)
  t-4  Wire engine into main; restart test
       files:    cmd/adguard-reward/main.go
                 cmd/adguard-reward/main_test.go
       covers:   c-4, c-5
       depends_on: [t-3]
       description:
         `var reconcileInterval = 60 * time.Second` beside sweepInterval. main builds
         `grants.New(st, client, log)`, passes it as Deps.Grants, runs one
         `mgr.Reconcile(ctx)` synchronously before `net.Listen` (a failure is logged, not
         fatal — same stance as the startup probe; the loop retries), then
         `go mgr.Run(ctx, reconcileInterval)` next to the sweeper.
       contract:
         - TestRun_GrantSurvivesRestart: start, login, create child "Ada" with "Kid phone",
           POST a 60 s grant for youtube (fake shows Kid phone without youtube), stop the
           run, rewrite that row's ends_at to now-1 via st.DB(), start again on the same
           data_dir: by the time onListen fires the fake's Kid phone lists youtube again and
           the first GET /api/v1/grants returns an empty list (block restored before the
           listener accepts)
         - TestRun_ReconcilerLoop: reconcileInterval = 50 ms; with the server running, a grant
           created through the API has its ends_at rewritten to the past via st.DB(); within
           2 s and without restart the fake shows youtube re-blocked (the 60 s loop is wired)
         - main_test's existing no-secrets and healthz tests still pass with the extra
           startup pass (no regression in boot ordering)

## Coverage

| criterion | tasks |
|---|---|
| c-1 | t-1, t-2, t-3 |
| c-2 | t-1, t-3 |
| c-3 | t-2 |
| c-4 | t-2, t-4 |
| c-5 | t-2, t-4 |
| c-6 | t-1, t-2, t-3 |

## Judgment calls

- services/clients as sorted JSON TEXT columns, not grant_services/grant_clients tables: one query and one scan per read; the overlap check runs in Go inside the create transaction on the single-connection store, so a partial unique index buys nothing. Rejected the two-table join because two LEFT JOINs cross-multiply and force two queries.
- No FK from grants.child_id to children: with `foreign_keys(ON)` a plain REFERENCES would block deleting a child that ever had a grant, and ON DELETE CASCADE would strand an active unblock — contrary to locked client_set. The stored clients list is the revert target, so the child row is not needed after creation.
- Duration bounds enforced only in the handler (t-3), with the consts exported from `grants` so extend and create share them: the engine accepts any duration, which is what lets t-2's timer tests run in milliseconds without a clock abstraction.
- `duration` on the wire is an integer number of seconds: the spec does not fix a format; seconds fit the locked bounds as plain integers (60..86400) and need no parser.
- `Deps.Grants` is the concrete `*grants.Manager`, not an api-local interface: the api harness already builds a real store and real fake AdGuard, so an interface plus a fake would be speculative structure with no test using it.
- `End` on an AdGuard write failure returns the error (handler → 502) and leaves the grant active, rather than marking it ended with the block still lifted; the spec only says "reverts immediately", and c-3's "failed write leaves active" is the closest stated rule.
- No new adguard client method: reconcile reads `Clients()` once per pass and writes only changed clients through the existing `SetBlockedServices`, whose own locked read-modify-write preserves every other client field. Rejected a `ModifyBlockedServices(name, fn)` because the extra atomicity it buys is not named by any criterion.
- Startup reconcile failure is non-fatal: an unreachable AdGuard at boot already is not fatal (startup probe decision), and the 60 s loop retries.
- The restart and loop tests move a grant into the past by rewriting `ends_at` through `st.DB()`, not via a clock hook threaded through main and the handler: the same data, one fewer injection point, and the "stopped mid-grant" sequence stays literal (real POST, real stop).
- A stored client no longer present in AdGuard is skipped and logged during reconcile/revert, never treated as a failure: otherwise a renamed client would keep the grant active forever.
- t-4 stays a separate task and wave rather than folding into t-3: together they touch five files across api and cmd, and the restart test is the only test that exercises "before the HTTP listener accepts", which deserves its own verification step.
