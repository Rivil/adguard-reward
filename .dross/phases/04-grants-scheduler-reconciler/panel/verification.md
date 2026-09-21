# Verification-lens plan — 04-grants-scheduler-reconciler

Method: for each criterion the ideal test was written first (which surface breaks, what
the test observes), then the smallest task that makes that test satisfiable. Every Go
API test runs the real store and the real `adguardtest` fake through the existing
`newHarness` in `internal/api/login_test.go`; the timer/reconciler engine is a new
package `internal/grants` tested against the same fake with sub-second grants, so no
test sleeps for a real minute; `cmd/adguard-reward/main_test.go` proves the
startup-pass-before-listen and restart criteria with `run()` and its `onListen` hook,
exactly as `TestRun_SessionSurvivesRestart` and `TestRun_Sweeper` already do.

Backend only (phone UI is phase 05). No `web/` tasks.

The wire contract is fixed here so the API task and the main-wiring task can be written
against it without waiting on each other's prose:

```
POST /api/v1/grants {child_id:int, services:[id...], duration:int seconds 60..86400}
     -> 201 {"id":7,"ends_at":"2026-09-21T14:05:00Z","applied":true,"failed":[]}
        applied=false + failed:["Kid tablet"] when some client writes failed (locked: partial_apply)
     -> 400 bad_request (malformed JSON / unknown field / not an object)
     -> 404 not_found   (unknown child)
     -> 409 conflict    {"error":"conflict","message":"...","grant_id":3}   (locked: overlapping_grants)
     -> 422 unprocessable (unknown service id, services empty, duration outside 60..86400,
                           child has no clients)
     -> 502 adguard_unavailable (catalogue could not be read to validate ids)
GET  /api/v1/grants
     -> 200 {"grants":[{"id","child_id","services":[],"clients":[],"started_at","ends_at"}]}   active only
POST /api/v1/grants/{id}/extend {duration:int seconds 60..86400}
     -> 200 {"id","ends_at"} | 404 not_found (unknown / non-active / non-numeric) | 422 unprocessable
POST /api/v1/grants/{id}/end   (no body)
     -> 204 | 404 not_found (unknown / non-active / non-numeric)
```
Times are RFC3339 UTC on the wire; stored as Unix **milliseconds** (see judgment calls).
Arrays are never null. `services` and `clients` are sorted and deduplicated.

Layering: `store` (rows + status CAS) -> `grants` (engine: apply, timers, revert,
reconcile — owns every AdGuard write in this phase) -> `api` (validation, catalogue check,
envelope) -> `main` (startup pass before Listen, `Run` loop). The engine is the only
thing that transitions a grant's status, so "expired vs ended vs still active" has one
owner and the tests can pin it.

```
Phase 04-grants-scheduler-reconciler — 4 tasks across 4 waves

Wave 1
  t-1  Grants schema, store queries, status CAS
       files:    internal/store/migrations/0003_grants.sql, internal/store/grants.go,
                 internal/store/grants_test.go
       covers:   c-1, c-2 (locked: overlapping_grants, client_set, grant_shape)
       depends:  —
       desc:     0003_grants.sql: grants(id INTEGER PRIMARY KEY, child_id INTEGER NOT NULL — no
                 FK, see judgment calls —, status TEXT NOT NULL CHECK(status IN
                 ('active','expired','ended')), services TEXT NOT NULL (JSON array), clients TEXT
                 NOT NULL (JSON array), started_at INTEGER NOT NULL, ends_at INTEGER NOT NULL,
                 created_at INTEGER NOT NULL; all times Unix ms) + INDEX grants_status_idx
                 (status, ends_at). grants.go: Grant{ID, ChildID int64; Services, Clients
                 []string; Status string; StartedAt, EndsAt time.Time}; consts StatusActive/
                 StatusExpired/StatusEnded; sentinels ErrGrantNotFound and typed
                 *ErrGrantOverlap{GrantID int64, Service string}. CreateGrant(ctx, childID,
                 services, clients, startedAt, endsAt) (Grant, error): one tx (st.inTx) that
                 SELECTs the child's active grants, scans them to memory (single-connection rule
                 in store.go), returns *ErrGrantOverlap naming the first colliding (grant,
                 service), else INSERTs with services/clients normalised via the existing
                 normaliseClients (trim, drop empties, sort, dedup; never nil).
                 ListActiveGrants(ctx) ([]Grant, error) id ASC, status='active' only.
                 GetGrant(ctx, id) any status or ErrGrantNotFound. UpdateGrantEndsAt(ctx, id,
                 endsAt) (Grant, error): UPDATE ... WHERE id=? AND status='active', RowsAffected
                 0 -> ErrGrantNotFound. SetGrantStatus(ctx, id, from, to string) (bool, error):
                 compare-and-set UPDATE ... WHERE id=? AND status=from, reports whether a row
                 flipped. wrapGrantErr passes the sentinels through like wrapChildErr.
       contract: - if the row or the migration does not persist, TestGrants_CreatePersist fails:
                   CreateGrant(ada.ID, ["youtube","tiktok","youtube"], [" Kid phone","Kid phone"],
                   now, now+1h) returns id>0, Status "active", Services ["tiktok","youtube"],
                   Clients ["Kid phone"]; Close, Open on the same dir, ListActiveGrants returns
                   that one grant with identical id/child/services/clients and StartedAt/EndsAt
                   equal to the millisecond; schema_migrations lists 0003_grants.sql exactly once
                 - if one-active-per-(child,service) is not enforced, TestGrants_Overlap fails:
                   with an active grant g1 (ada, ["youtube"]), CreateGrant(ada, ["tiktok",
                   "youtube"], ...) returns errors.As *ErrGrantOverlap with GrantID g1.ID and
                   Service "youtube" and ListActiveGrants still has exactly one row (the tx wrote
                   nothing); CreateGrant(ben, ["youtube"]) succeeds (overlap is per child);
                   after SetGrantStatus(g1.ID, "active", "expired") CreateGrant(ada, ["youtube"])
                   succeeds (only active rows collide)
                 - if listing leaks non-active rows, TestGrants_ActiveOnly fails: three rows set
                   to active / expired / ended -> ListActiveGrants returns only the active id;
                   GetGrant returns each of the three with its status; an empty table returns a
                   non-nil empty slice
                 - if the CAS is not a compare-and-set, TestGrants_StatusCAS fails:
                   SetGrantStatus(id, "active", "expired") -> (true, nil); the same call again ->
                   (false, nil); SetGrantStatus(id, "active", "ended") -> (false, nil) and
                   GetGrant still says "expired"; SetGrantStatus(999, "active", "ended") ->
                   (false, nil)
                 - if extend can touch a non-active row, TestGrants_UpdateEndsAt fails:
                   UpdateGrantEndsAt(activeID, t) returns Grant with EndsAt == t (ms) and GetGrant
                   agrees; UpdateGrantEndsAt(endedID, t) and UpdateGrantEndsAt(999, t) ->
                   ErrGrantNotFound and the ended row's ends_at is unchanged
                 - if the grant is tied to the child's lifetime, TestGrants_SurviveChildDelete
                   fails: DeleteChild(ada.ID) -> (true, nil) and ListActiveGrants still returns
                   ada's active grant with Clients ["Kid phone"] (client_set: revert must still
                   target the stored list after the child is gone)
                 - if the JSON columns are hand-parsed loosely, TestGrants_EmptyLists fails:
                   CreateGrant with clients [] stores and reads back Clients == []string{} (not
                   nil) and services [] likewise; the raw column text is "[]"

Wave 2 (depends t-1)
  t-2  Grant engine: apply, timers, revert, reconcile
       files:    internal/grants/grants.go, internal/grants/grants_test.go,
                 internal/adguard/adguardtest/server.go, internal/adguard/adguardtest/server_test.go
       covers:   c-3, c-5, and the apply/revert mechanics of c-1, c-4, c-6
                 (locked: revert_baseline, partial_apply, client_set, reconciler_cadence)
       depends:  t-1
       desc:     Package grants. Interfaces GrantStore (CreateGrant, ListActiveGrants, GetGrant,
                 UpdateGrantEndsAt, SetGrantStatus over store.Grant) and AdGuard (Clients(ctx),
                 SetBlockedServices(ctx, name, ids)) — both satisfied by *store.Store and
                 *adguard.Client. New(store, adguard, log) *Engine; Close() stops every timer.
                 Result{Grant store.Grant; Applied bool; Failed []string}.
                 Create(ctx, child store.Child, services []string, d time.Duration) (Result,
                 error): ends_at = now+d, CreateGrant with clients = child.Clients (client_set),
                 then for each client in order: read live via Clients() once, desired = own
                 blocked_services minus services; a client whose own list already lacks every
                 granted id is skipped (no write); a client with use_global_blocked_services
                 true, an ErrClientNotFound, or any write error goes into Failed; Applied =
                 len(Failed)==0. The row is committed before any AdGuard write and never rolled
                 back on write failure (partial_apply). A timer (time.AfterFunc) is armed for
                 ends_at. Bounds are NOT checked here (the API does that) so tests use
                 millisecond grants.
                 expire(id): under the engine mutex, GetGrant; if status active and now >=
                 ends_at -> revert(grant) then SetGrantStatus(active->expired) only if every
                 covered client was re-blocked (ErrClientNotFound counts as done — nothing to
                 re-block); any other write error leaves it active and logs, the reconciler
                 retries. If ends_at moved (extend) the timer is re-armed instead.
                 revert(grant): read live once; for each stored client, desired = own list ∪
                 services (set-union, sorted, revert_baseline); write only when desired differs
                 from live; never touches use_global_blocked_services.
                 Extend(ctx, id, d) (store.Grant, error): UpdateGrantEndsAt(ends_at+d) (only
                 active -> else ErrGrantNotFound), then timer.Reset to the new instant.
                 End(ctx, id) (store.Grant, error): stop timer, revert, SetGrantStatus
                 (active->ended) under the same rule as expire; non-active/unknown ->
                 ErrGrantNotFound before any AdGuard call.
                 ListActive(ctx) passes through.
                 Reconcile(ctx) error: exactly one Clients() read per pass; for each active grant:
                 past ends_at -> revert + expire (missed timer); still live -> for each stored
                 client, if live own list ∩ services ≠ ∅ write live minus services (parent
                 re-blocked in AdGuard's UI; also converges a partial apply), and arm a timer if
                 none exists for that id (a fresh process after restart). Missing clients are
                 skipped with a log line. A pass that finds nothing to do issues no
                 /control/clients/update.
                 Run(ctx, interval): ticker loop calling Reconcile until ctx is done, logging
                 errors, never exiting on one.
                 adguardtest gains BlockedServices(name string) ([]string, bool) — the stored
                 client's current own list, decoded, nil-safe — and CountRequests(method, path
                 string) int, so every test below asserts on AdGuard state and write counts
                 rather than on log lines.
       contract: - if Create does not remove the ids, TestEngine_CreateUnblocks fails: child
                   {"Kid phone"}, services ["youtube"], 1h -> Result.Applied true, Failed [],
                   fake.BlockedServices("Kid phone") == ["tiktok"] and the LastUpdate() body
                   carries future_field 42 and use_global_blocked_services false unchanged (the
                   existing RMW is reused, not a fresh object)
                 - if a client that already lacks the id is written anyway,
                   TestEngine_CreateNoOpWrite fails: services ["roblox"] (not on Kid phone) ->
                   CountRequests("POST","/control/clients/update") == 0 and Applied true
                 - if a global-list or missing client is silently counted as applied,
                   TestEngine_CreateFailedClients fails: child {"Kid tablet"} (use_global true)
                   -> Applied false, Failed ["Kid tablet"], zero update POSTs, and the grant row
                   is active in the store; child {"Ghost"} -> Failed ["Ghost"], row active
                 - if a write failure rolls the row back or hides the failed name,
                   TestEngine_PartialApply fails: Kid tablet mutated to use_global false with
                   own ["youtube"]; SetUpdateStatus("Kid tablet", 500); child {"Kid phone","Kid
                   tablet"} -> err nil, Applied false, Failed ["Kid tablet"], Kid phone ==
                   ["tiktok"], Kid tablet still ["youtube"], GetGrant status active; clear the
                   fault, Reconcile -> Kid tablet == [] and exactly one update POST in that pass
                 - if the timer does not fire or does not set-union, TestEngine_TimerReverts
                   fails: Create(Kid phone, ["youtube"], 150ms); MutateClient adds "roblox" to
                   Kid phone mid-grant; within 2s fake.BlockedServices("Kid phone") ==
                   ["roblox","tiktok","youtube"] and GetGrant status "expired"
                 - if revert writes when nothing changes, TestEngine_RevertIdempotent fails: a
                   store-seeded grant past ends_at whose services are already all present on
                   Kid phone -> Reconcile marks it expired with zero update POSTs and exactly
                   one GET /control/clients
                 - if a failed revert flips the status, TestEngine_RevertFailureStaysActive
                   fails: Create(150ms) then SetUpdateStatus("Kid phone", 502); at 1s the status
                   is still "active" and Kid phone still lacks youtube; clear the fault,
                   Reconcile -> youtube back and status "expired"
                 - if drift is not corrected from a live read, TestEngine_ReconcileDrift fails:
                   Create(1h); MutateClient re-adds "youtube" to Kid phone; Reconcile -> Kid
                   phone == ["tiktok"]; a second Reconcile issues 0 POSTs and exactly 1 GET
                   /control/clients (one read per pass, no per-grant re-reads)
                 - if a missed expiry is not caught, TestEngine_ReconcileMissedExpiry fails: a
                   store-seeded active grant with ends_at 1s in the past and Kid phone mutated
                   to ["tiktok"] -> Reconcile -> Kid phone == ["tiktok","youtube"], status
                   "expired"
                 - if the pass does not arm timers for pre-existing rows,
                   TestEngine_ReconcileArmsTimer fails: a store-seeded active grant with ends_at
                   now+200ms on a fresh Engine (no Create called) -> after one Reconcile, within
                   2s the block is restored and status is "expired" with no further Reconcile
                 - if Run ignores the interval or ctx, TestEngine_Run fails: Run(ctx, 20ms) with
                   drift injected corrects it within 2s; cancelling ctx makes Run return within
                   1s
                 - if extend does not move the timer, TestEngine_Extend fails: Create(200ms);
                   Extend(+800ms) returns EndsAt == old+800ms; at 500ms status still "active"
                   and youtube still absent; by 3s status "expired" and youtube present;
                   Extend on an expired id and on 999 -> store.ErrGrantNotFound
                 - if End does not revert now or leaves the timer armed, TestEngine_End fails:
                   Create(300ms); End -> youtube present immediately (before returning), status
                   "ended"; at 1s the status is still "ended" (the old timer did not flip it to
                   "expired"); End again and End(999) -> store.ErrGrantNotFound with zero
                   further update POSTs
                 - if two writers race on one client, TestEngine_Serialised fails: Hang
                   ("/control/clients/update", 30ms) with Create/End/Reconcile from 8 goroutines
                   keeps fake.MaxInFlightUpdates() == 1 under -race
                 - if the fake helpers misreport, TestFake_BlockedServices fails:
                   BlockedServices("Kid phone") == ["youtube","tiktok"] from the fixture,
                   reflects MutateClient, ("Old laptop") == [] for the null fixture, ("Nobody")
                   -> ok false; TestFake_CountRequests counts only exact method+path matches

Wave 3 (depends t-2)
  t-3  Grants endpoints, Deps.Grants, harness wiring
       files:    internal/api/grants.go, internal/api/grants_test.go, internal/api/api.go,
                 internal/api/errors.go, internal/api/login_test.go
       covers:   c-1, c-2, c-6 (locked: overlapping_grants, partial_apply, duration_bounds,
                 grant_shape)
       depends:  t-2
       desc:     errors.go: CodeUnprocessable = "unprocessable". api.go: GrantEngine interface
                 (Create, Extend, End, ListActive over grants.Result / store.Grant / store
                 sentinels), Deps.Grants GrantEngine, routes GET+POST /api/v1/grants, POST
                 /api/v1/grants/{id}/extend, POST /api/v1/grants/{id}/end, all behind
                 requireSession. grants.go: consts minDuration=60s, maxDuration=24h, var
                 applyTimeout = 5*time.Second (a var like main's sweepInterval so the deadline
                 test runs in ms). grantRequest{child_id, services, duration int seconds}
                 decoded like decodeChild (16 KiB, DisallowUnknownFields, single object) -> 400;
                 duration outside 60..86400 or services empty -> 422. handleGrantCreate:
                 GetChild -> 404 not_found; child with no clients -> 422; Services() -> 502 on
                 failure, any id absent from the catalogue -> 422 naming it; then Grants.Create
                 under context.WithTimeout(applyTimeout) — clients not written before the
                 deadline are simply in failed; *store.ErrGrantOverlap -> 409 {"error":
                 "conflict","message":...,"grant_id":N}; success -> 201 {id, ends_at, applied,
                 failed}. handleGrantsList -> {"grants":[grantView...]} with started_at/ends_at
                 RFC3339 UTC and arrays never null. handleGrantExtend: grantID like childID
                 (non-numeric -> 404), duration bounds -> 422, ErrGrantNotFound -> 404, else 200
                 {id, ends_at}. handleGrantEnd: 204 / 404. login_test.go newHarness builds
                 grants.New(st, client, log), Close on cleanup, passes Grants: eng.
       contract: - if auth or CSRF gating is missing, TestGrants_Gated fails: all four routes
                   without a cookie -> 401; the three POSTs with a cookie but no X-Requested-With
                   -> 403, and the store has no grant row and the fake saw no update POST
                 - if the create round-trip drifts, TestGrants_Create fails: POST {child_id:
                   ada, services:["youtube"], duration:1800} -> 201 with id>0, ends_at parsing as
                   RFC3339 within 5s of now+30m, applied true, failed []; fake.BlockedServices
                   ("Kid phone") == ["tiktok"]; GET /grants -> {"grants":[{id, child_id ada,
                   services ["youtube"], clients ["Kid phone"], started_at, ends_at}]} with
                   ends_at byte-equal to the 201's
                 - if validation is loose, TestGrants_CreateValidation fails: child_id 999 ->
                   404 not_found before any /control call; services ["zzz"] -> 422 unprocessable
                   whose message contains "zzz"; services [] -> 422; duration 59 -> 422; 86401 ->
                   422; 60 and 86400 -> 201 (inclusive bounds, on different children so no
                   overlap); a child with clients [] -> 422; malformed JSON, {extra:1}, a 20 KiB
                   body, a non-object -> 400; fake.SetStatus("/control/blocked_services/all",
                   500) -> 502 adguard_unavailable; after every non-201 the store has no new row
                   and no update POST was made
                 - if overlap is not 409 with the id, TestGrants_Overlap fails: second POST
                   {ada, ["tiktok","youtube"]} while ada's youtube grant is active -> 409
                   {"error":"conflict","grant_id":<first id>} and GET /grants still lists one;
                   the same services for ben -> 201
                 - if a partial apply is hidden, TestGrants_PartialApply fails: Kid tablet
                   mutated off the global list with own ["youtube"], SetUpdateStatus("Kid
                   tablet", 500), POST for a child {"Kid phone","Kid tablet"} -> 201 applied
                   false failed ["Kid tablet"], the grant is listed, Kid phone == ["tiktok"]
                 - if the 5 s bound is not enforced, TestGrants_ApplyDeadline fails: applyTimeout
                   = 100ms and fake.Hang("/control/clients/update", 2s) -> POST returns 201
                   applied false failed ["Kid phone"] in under 1s (wall clock measured)
                 - if the list is not active-only, TestGrants_ListActiveOnly fails: a fresh store
                   returns the raw body {"grants":[]}; after two creates then POST /end on one,
                   GET lists only the other; after store.SetGrantStatus(other, active->expired)
                   GET returns {"grants":[]}
                 - if extend does not push by the duration, TestGrants_Extend fails: POST
                   /grants/{id}/extend {duration:600} -> 200 {id, ends_at == previous ends_at +
                   600s exactly}; GET /grants reflects it; {duration:59} and {duration:86401} ->
                   422 with ends_at unchanged; /grants/abc/extend, /grants/999/extend and extend
                   on an ended grant -> 404 not_found
                 - if end does not revert or does not close the grant, TestGrants_End fails:
                   POST /grants/{id}/end -> 204, fake.BlockedServices("Kid phone") ==
                   ["tiktok","youtube"] before the response is read, GET /grants no longer lists
                   it, a second /end -> 404, /grants/abc/end and /grants/999/end -> 404
                 - if the view nulls, TestGrants_ViewShape fails: the raw GET body contains
                   "services":["youtube"] and "clients":["Kid phone"] and never the substring
                   ":null"

Wave 4 (depends t-3)
  t-4  Main wiring: startup pass, Run loop, restart proofs
       files:    cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go
       covers:   c-1 (restart), c-3, c-4, c-5 (locked: reconciler_cadence)
       depends:  t-3
       desc:     main.go: var reconcileInterval = 60 * time.Second beside sweepInterval;
                 after store.Open build eng := grants.New(st, client, log) (defer Close), pass
                 Grants: eng in api.Deps; call eng.Reconcile(ctx) BEFORE net.Listen — a failed
                 pass (AdGuard unreachable) is logged, not fatal (startup_probe precedent), the
                 next tick retries; go eng.Run(ctx, reconcileInterval) beside the sweeper.
                 main_test.go: helpers seedGrant(t, dataDir, clients, services, endsAt) that
                 opens the store, creates a child and an active grant, closes; and a
                 startWith(t, args, env, onListen func(addr string)) variant of start so a test
                 can observe fake state at the instant the listener opens.
       contract: - if the pass runs after (or concurrently with) the listener,
                   TestRun_StartupReconcileBeforeListen fails: seedGrant(["Kid phone"],
                   ["youtube"], ends_at 1h in the past) with Kid phone mutated to ["tiktok"];
                   inside onListen, before any HTTP request, fake.BlockedServices("Kid phone")
                   already == ["tiktok","youtube"]; afterwards GET /api/v1/grants (logged in)
                   returns {"grants":[]}
                 - if a still-live grant is not re-armed on startup, TestRun_TimerFromStartup
                   fails: seedGrant with ends_at now+300ms and Kid phone at ["tiktok"]; start;
                   within 3s Kid phone == ["tiktok","youtube"] with reconcileInterval left at
                   its 60s default (so only the timer could have done it)
                 - if the row does not survive the process, TestRun_GrantSurvivesRestart fails:
                   start, login, POST /grants {ada, ["youtube"], 3600} -> 201 id; cancel and wait
                   for exit 0; start again on the same data_dir; GET /grants with the same
                   cookie lists the same id, services and clients and the same ends_at string
                 - if the restart pass does not revert a grant that expired while down,
                   TestRun_RestartAfterEndsAt fails: start, login, POST a 60s grant (Kid phone
                   -> ["tiktok"]), stop; st.DB().Exec("UPDATE grants SET ends_at = ?", now-1s)
                   to simulate the clock passing ends_at while the process was down; start again;
                   inside onListen Kid phone == ["tiktok","youtube"], and GET /grants returns
                   {"grants":[]}
                 - if the loop interval is not the injected var, TestRun_ReconcileInterval
                   fails: reconcileInterval = 30ms (restored in Cleanup); start; POST a 1h
                   grant; fake.MutateClient re-adds "youtube" to Kid phone; within 2s Kid phone
                   == ["tiktok"] again; with reconcileInterval at 60s the same drift is still
                   present after 500ms
                 - if the startup pass becomes fatal, TestRun_StartupReconcileAdGuardDown fails:
                   fake.SetStatus("/control/clients", 500) with a seeded past-due grant -> run
                   still reaches onListen, exit code is not 1, stderr contains "reconcile" and
                   the grant is still active in the store (retry is the reconciler's job)
                 - if a secret leaks through the new log lines, the existing
                   TestRun_NoSecretsInLogs still fails: stderr after a startup pass with a
                   seeded grant contains neither svcPass nor aiKey
```

## Coverage

| criterion | tasks | how the acceptance is observed |
|---|---|---|
| c-1 | t-1, t-2, t-3, t-4 | t-1 row + overlap; t-2 unblock on every mapped client / partial apply; t-3 201 {id, ends_at} within applyTimeout, 404/409/422; t-4 TestRun_GrantSurvivesRestart |
| c-2 | t-1, t-3 | t-1 ListActiveGrants active-only; t-3 TestGrants_ListActiveOnly / ViewShape shape {id, child_id, services, clients, started_at, ends_at} |
| c-3 | t-2, t-4 | t-2 TimerReverts (set-union), RevertIdempotent (no write), RevertFailureStaysActive; t-4 TimerFromStartup within 3s |
| c-4 | t-2, t-4 | t-2 ReconcileMissedExpiry / ReconcileArmsTimer; t-4 StartupReconcileBeforeListen, RestartAfterEndsAt, StartupReconcileAdGuardDown |
| c-5 | t-2, t-4 | t-2 ReconcileDrift (one live read, zero writes when clean), Run; t-4 ReconcileInterval (injected var) |
| c-6 | t-2, t-3 | t-2 Extend (timer moved), End (immediate revert, timer cancelled); t-3 TestGrants_Extend / TestGrants_End incl. 404 for unknown / non-active |

All six criteria covered; every locked decision is pinned by at least one contract
(overlapping_grants: t-1 Overlap, t-3 Overlap; revert_baseline: t-2 TimerReverts;
partial_apply: t-2/t-3 PartialApply; client_set: t-1 SurviveChildDelete + t-2 revert
reads grant.Clients; reconciler_cadence: t-4 ReconcileInterval; grant_shape: t-1
CreatePersist multi-service; duration_bounds: t-3 CreateValidation / Extend).

## Judgment calls

- **New `internal/grants` package owns every AdGuard write, not the API.** Rejected:
  handlers calling SetBlockedServices directly with a separate scheduler package. The timer,
  the reconciler, End and Create all need the same revert/apply code and the same
  status transitions; one owner means "expired vs ended vs still active" is tested once.
- **Duration is an integer number of seconds** (`duration: 1800`), not a Go duration
  string or ISO 8601. Rejected: "30m" strings — parsing ambiguity across the phone UI and
  Go; the 422 bounds are trivially integer comparisons and the test can hit 59/60/86400/86401.
- **Store times as Unix milliseconds, not seconds like sessions.** Rejected: seconds. Every
  engine and restart test uses sub-second grants (150–800 ms); second-granularity storage
  would truncate ends_at below now and make the extend/timer tests either flaky or 2–3 s
  each. The API still renders RFC3339.
- **`services` and `clients` are JSON text columns, not child tables.** Rejected: a
  normalised grant_services with a partial unique index for the overlap. The overlap check
  runs as a SELECT-then-INSERT in one tx, exactly like `checkConflicts` in children.go, and
  it can return the existing grant id, which a constraint error cannot. One table, one query
  for the list, no nested reads on the single connection.
- **No foreign key from grants.child_id to children.** Rejected: ON DELETE CASCADE (deletes
  the grant -> stranded unblock, the exact failure the project exists to prevent) and ON
  DELETE RESTRICT (a parent cannot delete a child until its grants lapse). The grant carries
  its own client list (locked client_set), so it keeps reverting after the child is gone;
  TestGrants_SurviveChildDelete pins it.
- **Bounds (1 min–24 h) are enforced only in the API layer.** The engine accepts any
  duration so its tests run in milliseconds; a second copy of the bounds in the engine
  would only ever be exercised by a test that waits a minute.
- **A client on AdGuard's global list, or missing from AdGuard, is reported in `failed`
  rather than silently counted as applied.** Rejected: flipping use_global_blocked_services
  inside the grant (that is the phase-03 migration, which is user-consented) and rejecting
  the whole POST (partial_apply says the grant row is the intent). The reconciler never
  writes for such a client because its own list already lacks the granted ids, so the
  "no-op pass issues no writes" contract still holds.
- **A revert that hits ErrClientNotFound counts the client as done; any other write error
  keeps the grant active.** A client that no longer exists has nothing to re-block, and
  keeping the grant active forever would pin the reconciler on it every minute.
- **The apply deadline is a package var (`applyTimeout = 5 s`) in api, mirroring
  `sweepInterval` in main.** Rejected: a Deps field. The test needs to lower it to 100 ms
  and Hang the fake; a var is the established pattern for exactly this.
- **Restart-after-ends_at test time-travels the row (`UPDATE grants SET ends_at`)
  between the two runs** rather than adding a config knob for the minimum duration.
  Rejected: waiting 60 s or making duration bounds injectable in main. The UPDATE is the
  literal "clock passed ends_at while the process was down" and touches no production code.
- **The startup pass is non-fatal when AdGuard is unreachable.** Rejected: exit 1. The
  probe already tolerates an unreachable AdGuard at boot (startup_probe); a fatal pass would
  make the app unbootable in exactly the window it is needed, and Run retries in 60 s.
- **`POST /end` returns 204**, not the grant view; there is nothing for the phone to render
  after an end except the refreshed list, and it keeps the 404 branch the only body-bearing
  path.
