# Synthesis — 04-grants-scheduler-reconciler

Judge read the spec, project/rules, all three drafts, and the existing source
(`internal/api/api.go`, `internal/adguard/clients.go`, `adguardtest/server.go` +
`testdata/clients.json`, `internal/store/{store,children}.go` + migrations,
`cmd/adguard-reward/main{,_test}.go`, `internal/api/login_test.go`). Facts that
shaped the scoring:

- `api.Deps` is built from api-local interfaces ("so the package compiles against
  fakes"); `Children: st` is the precedent.
- The api harness already owns a `clock{Now,Advance}` and passes `Now: c.Now`
  into `auth.New`; `auth.Options.Now` and `RunSweeper(ctx, interval)` exist.
- `store` is single-connection; `children` uses a normalised `child_clients`
  table, not a JSON column; sessions store Unix **seconds**.
- `adguard.SetBlockedServices` is a locked read-modify-write, but the *list* it
  writes is whatever the caller computed from an earlier `Clients()` read — an
  unrelated id a parent adds between those two calls is lost.
- The fixture's "Kid tablet" has `use_global_blocked_services: true` and an empty
  own list; "Old laptop" has `blocked_services: null`.
- `main_test.go` has `start`/`stop`/`onListen`, `TestRun_Sweeper` overrides
  `sweepInterval`, and `TestRun_ChildrenSurviveRestart` is the restart template.

## Scores

Scale 1–5 per dimension.

| draft | criteria coverage | test-contract specificity | granularity | wave correctness |
|---|---|---|---|---|
| risk | 5 — every criterion mapped to a failure mode per layer; adds the deadline, race, deleted-device and AdGuard-down cases the spec implies | 4 — very precise (CAS truths, UpdateCount deltas, `-race` tests) but two contracts assume "Kid tablet" is a per-client-list device when the fixture puts it on the global list | 5 — 6 tasks; adguard primitives and the reconciler are their own commits; nothing bundles unrelated files | 5 — W1 {t-1,t-2} and W3 {t-4,t-5} are genuinely parallel; every depends_on is minimal |
| mvp | 3 — all six ticked, but c-1's "within 5 s" has no owner, no CSRF gate test, no AdGuard-down-at-boot test, no live-grant re-arm-after-restart test | 3 — good store/API contracts; engine contracts rely on 50 ms real timers with no clock control, and "exactly two POST" for Kid phone + Kid tablet writes a global-list client without noticing | 3 — 4 tasks; the engine task carries create+timer+extend+end+reconcile+run in one commit | 4 — strictly linear; correct, but no parallelism where it was available |
| verification | 5 — coverage table names the observing test per criterion and pins every locked decision to a contract | 5 — the sharpest: mutates Kid tablet off the global list first, byte-equal `ends_at`, `":null"` never in a body, `NoSecretsInLogs` regression, wall-clock deadline test, `seedGrant`/`startWith` helpers | 3 — 4 tasks; t-2 has 16 contracts plus fake-server changes in one commit | 4 — linear; the adguardtest helpers sit in wave 2 though they depend on nothing |

**Skeleton: risk.** It is the only draft whose structure closes the two real
concurrency holes (the lost-update window in `SetBlockedServices`, and the
reconciler-vs-`/end` decision race), it has the best granularity and the only
parallel waves, and its contracts are nearly as sharp as verification's.
Verification's contracts are grafted wherever they are more precise or catch a
fixture detail; mvp contributes the shared `MinDuration`/`MaxDuration` consts
and serves mainly as the dissenting voice recorded under Disagreements.

## Merged plan

Phase 04-grants-scheduler-reconciler — 6 tasks across 4 waves.

Wire contract (verification's block, adopted verbatim except where a
disagreement below says otherwise):

```
POST /api/v1/grants {child_id:int, services:[id...], duration:int seconds 60..86400}
     -> 201 {"id","ends_at","applied":bool,"failed":[client names]}
     -> 400 bad_request | 404 not_found (child) | 409 conflict {..., "grant_id":N}
     -> 422 unprocessable (unknown id, services empty, duration out of bounds, child has no clients)
     -> 502 adguard_unavailable (catalogue unreadable)
GET  /api/v1/grants                     -> 200 {"grants":[{id, child_id, services[], clients[], started_at, ends_at}]}
POST /api/v1/grants/{id}/extend {duration} -> 200 {"id","ends_at"} | 404 | 422
POST /api/v1/grants/{id}/end            -> 204 | 404 | 502 adguard_unavailable (revert failed, grant stays active)
```
Times RFC3339 UTC on the wire; arrays never null; `services`/`clients` sorted and
deduplicated.

### Wave 1

**t-1 — Grants schema and CAS store queries** `[risk+verification+mvp]`
- files: `internal/store/migrations/0003_grants.sql`, `internal/store/grants.go`, `internal/store/grants_test.go`
- covers: c-1, c-2, c-6
- depends_on: []
- description: `0003_grants.sql` — `grants(id INTEGER PRIMARY KEY, child_id INTEGER NOT NULL, status TEXT NOT NULL CHECK (status IN ('active','expired','ended')), started_at INTEGER NOT NULL, ends_at INTEGER NOT NULL, ended_at INTEGER)`, times Unix seconds UTC like sessions (see D3); deliberately no FK to `children` (all three drafts agree: a grant must outlive its child, locked `client_set`). `grant_services(grant_id REFERENCES grants(id) ON DELETE CASCADE, child_id, service_id, active INTEGER NOT NULL, PK (grant_id, service_id))` with partial `UNIQUE INDEX grant_services_live_idx (child_id, service_id) WHERE active = 1`; `grant_clients(grant_id ... CASCADE, client_name, PK (grant_id, client_name))`; index `grants(status, ends_at)` (see D2). `grants.go`: `Grant{ID, ChildID int64; Status string; Services, Clients []string (sorted, never nil); StartedAt, EndsAt time.Time}`, consts `StatusActive/StatusExpired/StatusEnded`, sentinels `ErrGrantNotFound` and typed `*ErrGrantOverlap{ExistingID int64, ServiceID string}`. `CreateGrant(ctx, childID, services, clients, startedAt, endsAt)` in one `inTx`: SELECT the live index per (child, service) → typed overlap error naming the first collision, else INSERT all three tables; lists normalised via `normaliseClients`. `ListActiveGrants(ctx)` id ASC, active only, folded from one query over grant_services then one over grant_clients, each scanned to completion (single-connection rule). `GetGrant(ctx, id)` any status. `ExtendGrant(ctx, id, newEndsAt) (Grant, error)` `WHERE status='active'`, 0 rows → `ErrGrantNotFound`. `SetGrantStatus(ctx, id, from, to string, at time.Time) (bool, error)`: one tx, `UPDATE grants ... WHERE id=? AND status=from`, and when a row changed `UPDATE grant_services SET active=0`; this CAS is the only exit from `active`. `wrapGrantErr` passes sentinels through like `wrapChildErr`.
- test_contract:
  - `[risk]` TestGrants_OverlapPerChildService: CreateGrant(ada, ["youtube","tiktok"]) then CreateGrant(ada, ["tiktok"]) → `*ErrGrantOverlap{ExistingID: first, ServiceID: "tiktok"}` and one grants row; CreateGrant(ben, ["tiktok"]) succeeds; `PRAGMA index_list('grant_services')` shows `grant_services_live_idx` partial and unique
  - `[risk+verification]` TestGrants_OverlapClears: after SetGrantStatus(id, active, expired) → (true, nil), CreateGrant(ada, ["tiktok"]) succeeds; likewise after active→ended
  - `[risk+verification]` TestGrants_StatusCAS: (active→expired) → true; second call → false; (active→ended) → false and GetGrant still expired; SetGrantStatus(999, …) → (false, nil), not an error
  - `[risk+verification]` TestGrants_ListActiveOnly: three grants, one expired, one ended → exactly the active one with full Services/Clients; empty store → non-nil empty slice; ids ASC
  - `[risk+verification]` TestGrants_ExtendActiveOnly: ExtendGrant(activeID, t+30m).EndsAt == t+30m and GetGrant agrees; ExtendGrant(endedID) and (999) → ErrGrantNotFound, ended row's ends_at unchanged
  - `[risk+verification]` TestGrants_ClientsFrozen: CreateGrant with clients ["Kid phone","Kid tablet"] then DeleteChild(ada) → (true, nil); GetGrant/ListActiveGrants still return both names and ada's old child_id
  - `[risk]` TestGrants_CreateRollsBack: a CreateGrant whose second service overlaps leaves no grants, grant_services or grant_clients rows for the attempt
  - `[verification]` TestGrants_CreatePersist: CreateGrant(ada, ["youtube","tiktok","youtube"], [" Kid phone","Kid phone"]) → Services ["tiktok","youtube"], Clients ["Kid phone"]; Close, Open same dir, ListActiveGrants returns it with identical id/lists and StartedAt/EndsAt equal at second precision; `schema_migrations` lists `0003_grants.sql` exactly once
  - `[verification]` TestGrants_EmptyLists: services [] / clients [] read back as `[]string{}`, never nil
  - `[risk]` TestGrants_ListRace under `-race`: 10 goroutines calling ListActiveGrants while another creates/expires for 20 cycles complete within 5 s; every returned grant carries a complete service list, never a partial one

**t-2 — AdGuard set-difference/union writes and fake helpers** `[risk; fake helpers also verification]`
- files: `internal/adguard/clients.go`, `internal/adguard/clients_test.go`, `internal/adguard/adguardtest/server.go`, `internal/adguard/adguardtest/server_test.go`
- covers: c-1, c-3
- depends_on: []
- description: `RemoveBlockedServices(ctx, clientName, ids)` and `AddBlockedServices(ctx, clientName, ids)` on `updateClient`, so the current list is re-read under `c.rmw` and edited in place inside the mutate closure: Remove = current minus ids; Add = sorted union(current, ids); written as `[]` never null; every other field verbatim; the existing global-list warn line is kept. Neither path uses the caller-supplied replace list, so a parent's concurrent edit to an unrelated id survives (see D1). `adguardtest` gains `BlockedServices(name) ([]string, bool)` (stored own list, decoded, nil-safe, ok=false for unknown), `RemoveClient(name)` (later GET omits it; POST /update for it answers 400 — "parent deleted the device"), and `CountRequests(method, path) int` (exact-match counter, so "zero writes" and "exactly one GET /control/clients" assert without scanning `Requests()`).
- test_contract:
  - `[risk]` TestAddBlockedServices_Union: MutateClient("Kid phone") → ["youtube","roblox"]; Add(["tiktok","youtube"]) → fake list ["roblox","tiktok","youtube"]; update body still carries `future_field: 42`
  - `[risk]` TestAddBlockedServices_Idempotent: twice with the same ids → same list, second body byte-equal to the first
  - `[risk]` TestRemoveBlockedServices_Diff: "Kid phone" after Remove(["tiktok","nonexistent"]) → ["youtube"]; Remove on "Old laptop" (null fixture) writes `[]` and does not error
  - `[risk]` TestRemoveAdd_NotFound: both against "Nobody" → `errors.Is(err, ErrClientNotFound)`, CountRequests("POST","/control/clients/update") == 0
  - `[risk]` TestSetBlockedServices_Serialised extended: Hang("/control/clients/update", 30ms) with 10 goroutines mixing Add/Remove/SetBlockedServices → MaxInFlightUpdates() == 1
  - `[risk+verification]` TestFake_GrantHelpers: RemoveClient("Kid tablet") → GET lists two clients, POST /update for it → 400; BlockedServices("Kid phone") == ["youtube","tiktok"] from the fixture and reflects MutateClient; ("Old laptop") == [] ok; ("Nobody") ok=false; CountRequests counts only exact method+path matches and not GETs as POSTs

### Wave 2 (depends t-1, t-2)

**t-3 — Grant engine: create, timer expiry, extend, end** `[risk; contracts grafted from verification]`
- files: `internal/grants/grants.go`, `internal/grants/grants_test.go`
- covers: c-1, c-3, c-6
- depends_on: [t-1, t-2]
- description: Package `grants` owns every AdGuard write made for a grant and is the only thing that transitions status. Interfaces `Store` (CreateGrant, ListActiveGrants, GetGrant, ExtendGrant, SetGrantStatus — satisfied by `*store.Store`) and `AdGuard` (Clients, RemoveBlockedServices, AddBlockedServices — satisfied by `*adguard.Client`). `Options{Now func() time.Time, AfterFunc func(time.Duration, func()) Timer, ApplyTimeout time.Duration (default 4 s), Log}`; `Timer interface{ Stop() bool }`; defaults `time.Now`/`time.AfterFunc` (see D3, D6). `Engine{mu sync.Mutex; timers map[int64]Timer}`; `mu` is held across the whole read-decide-write of every operation. `Create(ctx, childID, services, clients []string, d) (Result{Grant, Applied bool, Failed []string}, error)`: `store.CreateGrant` (overlap passes through), then under an `ApplyTimeout`-bounded ctx `RemoveBlockedServices` per stored client in order; a client that errors (incl. `ErrClientNotFound` and deadline), or whose live `UseGlobalBlockedServices` is true (no write issued, see D5), goes into `Failed`; the row is never rolled back (locked `partial_apply`); arm `timers[id]`. `expire(id)`: under mu, GetGrant; not active or `Now() < EndsAt` (extended meanwhile) → re-arm and return; else `revert`: AddBlockedServices on every stored client, `ErrClientNotFound` counts as reverted, any other failure → leave active, log warn, no status change; all ok → `SetGrantStatus(active→expired)`, drop timer. `Extend(ctx, id, d)`: under mu, `ExtendGrant(EndsAt+d)`, stop old timer, arm new. `End(ctx, id) error`: under mu, GetGrant, not active → `ErrGrantNotFound`; revert; any client failed → `*ErrRevertFailed{Clients}` and the grant stays active with its timer; else `SetGrantStatus(active→ended)`, stop timer. `Close()` stops every timer. Bounds are NOT checked here (all three drafts agree — API layer does it) so tests use short virtual durations. Tests use a fake clock whose `Advance(d)` fires due timers synchronously on the calling goroutine.
- test_contract:
  - `[risk+verification]` TestEngine_CreateUnblocks: Kid tablet first mutated off the global list with own ["youtube"]; clients ["Kid phone","Kid tablet"], services ["youtube"] → BlockedServices("Kid phone") == ["tiktok"], ("Kid tablet") == [], Applied true, Failed == `[]string{}`, Grant.EndsAt == t0+d; LastUpdate() still carries `future_field 42` (the RMW is reused)
  - `[verification]` TestEngine_CreateNoOpWrite: services ["roblox"] (absent from Kid phone) → CountRequests("POST","/control/clients/update") == 0 and Applied true
  - `[verification]` TestEngine_CreateFailedClients: clients ["Kid tablet"] untouched (use_global true) → Applied false, Failed ["Kid tablet"], zero update POSTs, row active; clients ["Ghost"] → Failed ["Ghost"], row active
  - `[risk+verification]` TestEngine_PartialApply: SetUpdateStatus("Kid tablet", 500) → err nil, Applied false, Failed ["Kid tablet"], Kid phone unblocked, ListActiveGrants holds the grant; clear the fault, Reconcile (t-4) later converges it
  - `[risk]` TestEngine_CreateDeadline: Hang("/control/clients/update", 8s) with ApplyTimeout 1 s → Create returns within 2 s wall clock, Applied false, Failed names the hung client, row exists
  - `[risk+verification]` TestEngine_TimerReverts: MutateClient adds "roblox" to Kid phone mid-grant; Advance(d) → both clients list the granted id again (Kid phone == ["roblox","tiktok","youtube"]), status expired; calling expire(id) again issues zero further update POSTs and the lists are identical (set-union idempotence)
  - `[risk+verification]` TestEngine_RevertFailureStaysActive: SetUpdateStatus("Kid phone", 500), Advance(d) → still active, Kid tablet re-blocked, Kid phone not, warn log names "Kid phone"; clear fault, expire(id) again → Kid phone re-blocked, expired
  - `[risk]` TestEngine_MissingClientExpires: RemoveClient("Kid tablet") after Create, Advance(d) → Kid phone re-blocked, grant expired (not stuck active)
  - `[risk+verification]` TestEngine_ExtendReschedules: Extend(id, 10m) → EndsAt == t0+d+10m; Advance(d) → still active, update count unchanged (stale timer re-armed, not reverted); Advance(10m) → expired and re-blocked; Extend on an expired id and on 999 → ErrGrantNotFound
  - `[risk]` TestEngine_EndVsTimerRace under `-race`: Hang("/control/clients/update", 20ms); End(id) on one goroutine while Advance(d) fires the timer on another → grant is exactly one of ended/expired, both clients list the id once, neither goroutine errored "not active" for a grant it had itself reverted
  - `[risk+verification]` TestEngine_EndRevertFailure: SetUpdateStatus("Kid phone", 500) → End returns `*ErrRevertFailed{Clients: ["Kid phone"]}`, grant still active, Kid tablet re-blocked; End on an ended grant and End(999) → ErrGrantNotFound with zero further update POSTs; after a successful End, Advance past the old EndsAt leaves status "ended" (the old timer did not flip it to expired)
  - `[verification]` TestEngine_Serialised under `-race`: Hang("/control/clients/update", 30ms) with Create/End from 8 goroutines keeps MaxInFlightUpdates() == 1

### Wave 3 (depends t-3)

**t-4 — Reconciler pass, drift repair, startup pass** `[risk; contracts grafted from verification]`
- files: `internal/grants/reconcile.go`, `internal/grants/reconcile_test.go`
- covers: c-4, c-5
- depends_on: [t-3]
- description: `Reconcile(ctx) (Stats{Grants, Reverted, Repaired, Writes int}, error)` on Engine, under mu: ListActiveGrants; empty → return without touching AdGuard. Else ONE `Clients()` read for the pass; per grant in id order: `Now() >= EndsAt` → revert+CAS exactly as `expire`; else per stored client present in the read, `ids := granted ∩ client.BlockedServices`, non-empty → `RemoveBlockedServices(client, ids)` (one write per drifted client, none otherwise); absent client → skipped with a debug line; a `UseGlobalBlockedServices` client is compared against its own list only (the global list is never written, phase-03 lock); any active grant with no armed timer gets one (post-restart). A `Clients()` error aborts the pass with no status change and no writes. `Start(ctx) error`: one Reconcile bounded by ctx; errors returned for main to log, never fatal. `Run(ctx, interval)`: ticker loop like `auth.RunSweeper`, logs errors, exits on ctx.Done; passes never overlap (mu).
- test_contract:
  - `[risk+verification]` TestReconcile_DriftRepaired: grant ["tiktok"] on ["Kid phone","Kid tablet"]; MutateClient puts "tiktok" back on Kid phone → Reconcile → Kid phone == ["youtube"], Stats.Repaired 1, exactly one update POST (Kid tablet untouched), exactly one GET /control/clients in the pass
  - `[risk+verification]` TestReconcile_NoopNoWrites: an active undrifted grant → zero update POSTs and exactly one GET /control/clients; no active grants → zero requests of any kind
  - `[risk+verification]` TestReconcile_RevertsOverdue: engine built with an AfterFunc that never fires; Advance past EndsAt; Reconcile → both clients re-blocked, status expired, Stats.Reverted 1
  - `[verification]` TestReconcile_RevertIdempotent: a store-seeded overdue grant whose services are already all present → Reconcile marks it expired with zero update POSTs and exactly one GET
  - `[verification]` TestReconcile_ConvergesPartialApply: after t-3's partial-apply scenario, clear the fault → Reconcile writes exactly one update and Kid tablet is unblocked
  - `[risk]` TestReconcile_AdGuardDown: SetStatus("/control/clients", 500) on an overdue grant → Reconcile returns an error, status still active, update count unchanged; clearing the fault and re-running reverts and expires it
  - `[risk]` TestReconcile_EndRace under `-race`: Hang("/control/clients", 30ms); Reconcile on one goroutine, End(id) on another → grant is ended and "tiktok" is on both clients' lists (the pass ran wholly before or wholly after End, never in between)
  - `[risk+verification]` TestReconcile_StartArmsTimers: rows created via the store directly (no engine), one overdue and one live with a drifted id; Start → the overdue one reverted+expired, the live one repaired, and Advance to its EndsAt fires a revert with no further Reconcile
  - `[risk]` TestReconcile_MissingClient: RemoveClient("Kid tablet"); an overdue grant on both → Kid phone re-blocked, status expired, no error returned
  - `[risk+verification]` TestReconcile_RunLoop: Run with interval 20 ms and Hang("/control/clients", 50 ms) for 300 ms → MaxInFlightUpdates() ≤ 1 and never two concurrent GET /control/clients; cancelling ctx returns Run within 100 ms

**t-5 — Grants HTTP endpoints, validation, Deps wiring** `[risk+verification+mvp]`
- files: `internal/api/grants.go`, `internal/api/grants_test.go`, `internal/api/api.go`, `internal/api/errors.go`, `internal/api/login_test.go`
- covers: c-1, c-2, c-6
- depends_on: [t-3]
- description: `errors.go`: `CodeUnprocessable = "unprocessable"`. `api.go`: api-local `Grants` interface (Create, List, Extend, End over `*grants.Engine`'s signatures — see D7), `Deps.Grants`, routes `GET/POST /api/v1/grants`, `POST /api/v1/grants/{id}/extend`, `POST /api/v1/grants/{id}/end`, all behind `requireSession`. `grants.go`: body `{child_id int64, services []string, duration int (seconds)}` decoded like `decodeChild` (16 KiB, no unknown fields, single object → 400); bounds `grants.MinDuration = 60 s` / `grants.MaxDuration = 24 h` exported consts shared by create and extend `[mvp]`; duration outside → 422; services trimmed/deduped, empty → 422; child via `Children.GetChild` first (404 before any AdGuard call); child with no clients → 422 `[verification]`; ids validated against `AdGuard.Services` (unknown → 422 naming it; catalogue unreachable → 502). `Create` → 201 `{id, ends_at, applied, failed}`; `*store.ErrGrantOverlap` → 409 `{error:"conflict", message, grant_id}`. GET → `{grants:[grantView...]}` with RFC3339 UTC times and never-null arrays. extend: `{duration}` same bounds → 200 `{id, ends_at}`; `ErrGrantNotFound` and non-numeric id → 404. end: 204; not found → 404; `*grants.ErrRevertFailed` → 502 `adguard_unavailable` naming the clients (grant stays active). `login_test.go`: `newHarness` builds a `*grants.Engine` on the harness store and fake with `Now: c.Now`, `Close` on cleanup, and passes `Grants: eng`.
- test_contract:
  - `[risk+verification]` TestGrants_Gated: all four routes without a cookie → 401; the three POSTs with a cookie but no `X-Requested-With` → 403, grants table empty afterwards and the fake saw no update POST
  - `[risk+verification]` TestGrants_CreateAndList: POST {ada, ["tiktok"], 1800} → 201 with id > 0, applied true, failed [], ends_at parsing as RFC3339 == started_at+30m; BlockedServices("Kid phone") lacks "tiktok"; GET lists exactly it with clients == ada's clients at creation and ends_at byte-equal to the 201's; after `SetGrantStatus(id, active, expired)` via the store, raw GET body is `{"grants":[]}`
  - `[risk+verification]` TestGrants_Validation: duration 59 → 422, 86401 → 422, 60 and 86400 → 201 (different children); services ["nope"] → 422 whose message contains "nope"; services [] and [""] → 422; a child with clients [] → 422; {child_id: 999} → 404 with no `/control/blocked_services/all` request; unknown field / non-object / 20 KiB body → 400; after every non-201 the store has no new row and no update POST was made
  - `[risk+verification]` TestGrants_Overlap409: second POST for ada ["tiktok","roblox"] → 409 with `.error "conflict"` and `.grant_id` == first id, GET still lists one; ben ["tiktok"] → 201
  - `[risk+verification]` TestGrants_PartialApplyBody: Kid tablet mutated off the global list with own ["youtube"], SetUpdateStatus("Kid tablet", 500) → 201 applied false failed ["Kid tablet"], GET lists it, Kid phone unblocked
  - `[verification]` TestGrants_ApplyDeadline: engine ApplyTimeout 100 ms in the harness and Hang("/control/clients/update", 2 s) → POST returns 201 applied false failed ["Kid phone"] in under 1 s wall clock
  - `[risk]` TestGrants_CatalogueDown: SetStatus("/control/blocked_services/all", 500) → 502 adguard_unavailable, no row created
  - `[risk+verification]` TestGrants_ExtendEnd: extend {600} → 200 with ends_at == previous ends_at + 10 m exactly (not from now) and GET reflects it; extend {30} and {86401} → 422 with ends_at unchanged; end → 204 and BlockedServices("Kid phone") lists the id again before the response is read; extend and end on that id → 404; `/grants/abc/end`, `/grants/999/extend` → 404
  - `[risk]` TestGrants_EndRevertFails: SetUpdateStatus("Kid phone", 500) → POST end is 502 whose message contains "Kid phone", GET still lists the grant
  - `[verification]` TestGrants_ViewShape: raw GET body contains `"services":["tiktok"]` and `"clients":["Kid phone"]` and never the substring `:null`

### Wave 4 (depends t-4, t-5)

**t-6 — Wire engine into main; restart and drift proofs** `[risk+verification+mvp]`
- files: `cmd/adguard-reward/main.go`, `cmd/adguard-reward/main_test.go`
- covers: c-1, c-4, c-5
- depends_on: [t-4, t-5]
- description: `main.go`: `var reconcileInterval = 60 * time.Second` beside `sweepInterval`; after `store.Open` build `eng := grants.New(st, client, grants.Options{Log: log})` (defer Close); `eng.Start(ctx)` runs BEFORE `net.Listen`, bounded by a 30 s context so an unreachable AdGuard cannot hold the listener closed `[risk]`; its error is logged ("startup reconcile", err) and never fatal (startup_probe precedent); `Grants: eng` in `api.Deps`; `go eng.Run(ctx, reconcileInterval)` beside the prober and sweeper. `main_test.go`: helpers `seedGrant(t, dataDir, clients, services, endsAt)` (opens the store, creates a child and an active grant, closes) and a `startWith(t, args, env, onListen)` variant so a test can observe fake state at the instant the listener opens `[verification]`. Restart tests rewind `ends_at` through `st.DB()` between runs — all three drafts chose this over a clock knob in main.
- test_contract:
  - `[risk+verification+mvp]` TestRun_GrantRestoredAfterRestart: run, login, POST child + 60 s grant (Kid phone loses the id), stop (exit 0), `UPDATE grants SET ends_at = now-60`, start again on the same data_dir: inside the onListen callback, before any HTTP call, BlockedServices("Kid phone") already contains the id; GET /api/v1/grants with the old cookie → `{"grants":[]}`
  - `[risk+verification+mvp]` TestRun_GrantSurvivesRestart: restart with an un-rewound ends_at → GET lists the grant with the same id, services, clients and the same ends_at string
  - `[risk+verification]` TestRun_LiveGrantRearmed: restart with ends_at rewound to now+2 s and reconcileInterval = 1 h; within 5 s of listening the fake shows the id re-blocked (only the timer could have done it)
  - `[risk+verification+mvp]` TestRun_ReconcilerDrift: reconcileInterval = 50 ms (restored in Cleanup); after POST grant, MutateClient re-adds the id on Kid phone; within 2 s it is removed again; with no drift the fake sees zero update POSTs over 500 ms; with reconcileInterval at 60 s the same drift is still present after 500 ms
  - `[risk+verification]` TestRun_StartupAdGuardDown: SetStatus("/control/clients", 500) with a seeded overdue grant → run still reaches onListen within 10 s, exit code is not 1, stderr contains "startup reconcile", grant still active; `SetResponse(..., nil)` clears it and with reconcileInterval = 50 ms the block is restored within 2 s
  - `[verification]` TestRun_NoSecretsInLogs still passes after a startup pass with a seeded grant (stderr contains neither svcPass nor aiKey); existing healthz test unchanged (no boot-ordering regression `[mvp]`)

### Coverage

| criterion | tasks (failure mode owned) |
|---|---|
| c-1 | t-1 row/overlap/persist · t-2 unblock write · t-3 Create applies, partial, deadline · t-5 201/404/409/422 contract · t-6 survives restart |
| c-2 | t-1 active-only list · t-5 GET shape, never-null |
| c-3 | t-2 set-union idempotent · t-3 timer revert, failed write stays active · t-6 live grant re-armed |
| c-4 | t-4 Start: overdue reverted, live re-verified, timers armed · t-6 before-listen ordering, AdGuard down at boot |
| c-5 | t-4 one read per pass, drift repair, missed timer, quiet pass = no writes, no overlap · t-6 60 s var wiring, drift end-to-end |
| c-6 | t-1 Extend CAS active-only · t-3 reschedule, end revert, end-vs-timer race · t-5 404s, 422 bounds, 502 on failed end |

## Disagreements

**D1 — AdGuard write primitive: new set-diff/union methods vs reuse `SetBlockedServices`.**
Risk adds `RemoveBlockedServices`/`AddBlockedServices` whose list arithmetic runs *inside* `updateClient`'s locked re-read. MVP and verification reuse `SetBlockedServices` with a list the engine computed from its own earlier `Clients()` read; mvp explicitly rejects a new method as "atomicity not named by any criterion". Provisional default: **risk** (t-2). Why it matters: with the existing method there is a window between the engine's read and the write in which a parent's edit to an unrelated id on the same client is silently overwritten — a drift *caused* by the app, which is the project's stated core-value failure. c-3's "set-union, idempotent" is literally what `Add` does. Cost is one extra wave-1 task that runs in parallel with t-1, so the critical path is unchanged. If the executor flips this, t-3/t-4 contracts that assert `LastUpdate()` preserves `future_field` still hold; only TestAddBlockedServices_Union's "roblox survives" assertion becomes unprovable.

**D2 — Schema: normalised `grant_services`/`grant_clients` + partial unique index vs JSON text columns.**
Risk: three tables, `WHERE active = 1` unique index as a DB-level overlap safety net, the CAS also flips `active=0`. MVP and verification: one `grants` table with `services`/`clients` as sorted JSON arrays, overlap checked by SELECT-then-INSERT in the tx (like `checkConflicts` in children.go). Provisional default: **risk**, because the existing store already uses a child table (`child_clients`) rather than a JSON column, and the index is the only guard that survives a future writer bypassing the store. Why it matters: it is 2-vs-1 among planners, the JSON design is genuinely simpler (one query per list, no `active` bookkeeping in the CAS), and the typed 409 error is achievable either way. Flipping it changes t-1 only: drop the PRAGMA assertion, replace with verification's "raw column text is `[]`" check.

**D3 — Timer/clock abstraction and stored time precision (coupled).**
Risk injects `Options{Now, AfterFunc}` and tests with a fake clock whose `Advance` fires timers synchronously, storing Unix seconds like sessions. MVP and verification use real `time.AfterFunc` with 50–800 ms grants and polling; verification therefore stores Unix **milliseconds** because seconds would truncate a sub-second `ends_at` below now. Provisional default: **risk** — deterministic engine tests, no sleeps, and it mirrors the api harness's existing `clock{Now,Advance}` and `auth.Options.Now`; storage stays seconds, consistent with `sessions`. Why it matters: it decides whether the engine's ~12 contracts are race-free-by-construction or timing-sensitive, and it fixes the `ends_at` column unit for phase 05. The main_test restart proofs still use real time; risk's timings (rewind to now+2 s, "within 5 s") are compatible with seconds — verification's `now+300ms` seed would not be, and is not adopted.

**D4 — Engine and reconciler as two tasks (risk) vs one (mvp, verification).**
Provisional default: **risk** — t-3 (create/expire/extend/end) then t-4 (reconcile/start/run) in the same package. Why it matters: a single engine task carries 16–20 contracts and both files in one commit; the split also lets t-5 (HTTP) run in parallel with t-4, which the linear drafts could not. The dependency is real (Reconcile reuses `revert` and the CAS), so the order cannot be reversed.

**D5 — A client on AdGuard's global blocked-services list during Create.**
Verification: no write, client reported in `failed`, grant stays active (the reconciler never writes for it either, so the "quiet pass issues no writes" contract survives). Risk: Create writes every stored client and only Reconcile is told to compare a global-list client against its own list. MVP: unaddressed — its "exactly two POSTs" contract would write to Kid tablet while it is still on the global list, which `SetBlockedServices`' own warn says AdGuard may ignore. Provisional default: **verification**. Why it matters: the fixture's "Kid tablet" is `use_global_blocked_services: true`, so every contract that expects Kid tablet to be unblocked must first `MutateClient` it off the global list (grafted into t-3/t-5), and phase 05 must render `applied=false` for such a device instead of assuming the tap worked. Alternative considered by verification and rejected: 422 (contradicts locked `partial_apply`).

**D6 — Where the 5 s apply bound lives.**
Risk: `Options.ApplyTimeout` in the engine (default 4 s). Verification: package var `applyTimeout = 5 s` in `api`, mirroring `sweepInterval`. MVP: no bound at all — c-1's "within 5 s" has no owner. Provisional default: **risk** (engine owns the per-client loop, so it owns cutting it short and naming the unreached clients as failed), with verification's HTTP-level TestGrants_ApplyDeadline kept by constructing the harness engine with a 100 ms timeout. Why it matters: a bound in the handler alone would cancel the ctx mid-loop and leave the engine unable to say *which* clients were skipped.

**D7 — `Deps.Grants` as an api-local interface (risk, verification) vs the concrete `*grants.Manager` (mvp).**
Provisional default: **interface** — `api.go` states "interfaces are api-local so the package compiles against fakes" and every other Dep follows it. MVP's point that no test currently needs a fake engine is true; the choice costs five lines and keeps the package idiom uniform.

**D8 — Response bodies beyond the spec's minimum.**
Risk returns the full grant view (plus `applied`/`failed`) on 201 and on extend; mvp and verification return `{id, ends_at, applied, failed}` and `{id, ends_at}`; mvp names the array `failed_clients`. Provisional default: **minimal bodies, key `failed`** (2-of-3, and the spec's own wording is `{id, ends_at}`). Why it matters only slightly: phase 05 will GET the list after a create anyway; whichever shape ships becomes the wire contract the SPA keys on.
