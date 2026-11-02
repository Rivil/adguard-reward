# Plan Review — 04-grants-scheduler-reconciler

Reviewed: 2026-09-21
Plan: 6 tasks across 4 waves

## BLOCKING
(none)

## FLAG
- [antipatterns/files] t-5 validates duration against `grants.MinDuration = 60 s / grants.MaxDuration = 24 h`, described as "exported consts shared by create and extend" in package `grants`. Nobody creates them: t-3 owns `internal/grants/grants.go` and explicitly says "Duration bounds are NOT checked here (API layer does it)" without defining the consts, and t-5's `files` does not include `internal/grants/grants.go`. As written t-5 either fails to compile or has to edit a file outside its declared set.
  Suggestion: add the two consts to t-3's description (package `grants` is the natural owner since the engine stores the duration), or add `internal/grants/grants.go` to t-5's `files`, or make them api-local (`minGrantDuration`/`maxGrantDuration` in `internal/api/grants.go`).

- [test-contract] ApplyTimeout scope is ambiguous and the tests don't pin it. t-3 says "EVERY AdGuard call made under mu runs under an ApplyTimeout-bounded ctx ... so a hung AdGuard can hold mu for at most ApplyTimeout", but Create's apply loop makes one `Clients()` read plus one `RemoveBlockedServices` per stored client. If the executor gives each call its own ApplyTimeout ctx (the literal reading of "every call"), Create on a two-client child with AdGuard hung takes 2 × 4 s + 4 s = 12 s and c-1's "responds 201 within 5 s" is violated. Both TestEngine_CreateDeadline and TestGrants_ApplyDeadline hang a single client, so a per-call implementation passes them.
  Suggestion: state "one ApplyTimeout-bounded ctx per mutating operation, shared by every AdGuard call that operation makes", and make TestEngine_CreateDeadline use two stored clients with both hung (`Hang("/control/clients/update", 8s)` applies to both anyway) and assert wall clock < 1.5 × ApplyTimeout with `Failed` naming both.

- [test-contract] Create's single `Clients()` read (for the global flag) can fail as a whole; the description only defines per-client failure ("a client that errors ... goes into Failed"). Unspecified: does a failed pass read put every stored client into `Failed` with no writes, or does Create skip the flag check and attempt the removes blind? The reconciler converges either way, but the 201 body differs and TestEngine_CreateFailedClients doesn't cover it.
  Suggestion: state the behaviour (recommend: read error → all stored clients in `Failed`, `Applied=false`, zero update POSTs, row active) and add a `SetStatus("/control/clients", 500)` case to TestEngine_CreateFailedClients.

- [granularity] t-5 touches 5 files (`grants.go`, `grants_test.go`, `api.go`, `errors.go`, `login_test.go`). Single layer, and three of the five are additive touches (one const, interface + four routes + one Deps field, harness wiring), so a split would be artificial.
  Suggestion: leave as is; flagged mechanically.

## NOTE
- [coverage] Complete. c-1 → t-1/t-2/t-3/t-5/t-6; c-2 → t-1/t-5; c-3 → t-2/t-3; c-4 → t-4/t-6; c-5 → t-4/t-6; c-6 → t-1/t-3/t-5.

- [locked-decisions] All seven honoured and each is pinned by a named test: overlapping_grants (partial unique index + `*ErrGrantOverlap` → 409 with `grant_id`: TestGrants_OverlapPerChildService, TestGrants_Overlap409); revert_baseline (Add = set-union, no snapshot: TestAddBlockedServices_Union, TestEngine_TimerReverts); partial_apply (row never rolled back, 201 applied=false: TestEngine_PartialApply, TestGrants_PartialApplyBody, TestReconcile_ConvergesPartialApply); client_set (grant_clients frozen, no FK to children: TestGrants_ClientsFrozen); reconciler_cadence (`var reconcileInterval = 60s`, Run(ctx, interval): TestRun_ReconcilerDrift); grant_shape (services[] per grant, overlap per (child, service)); duration_bounds (59/86401 → 422, 60/86400 → 201: TestGrants_Validation, TestGrants_ExtendEnd).

- [wave-order] Tight. t-3 needs both wave-1 outputs (store queries and the RMW writers); t-4 and t-5 both need the concrete `*grants.Engine` (t-5's harness builds a real engine and asserts fake state, so an interface stub would not do); t-6 needs Start/Run from t-4 and `Deps.Grants` from t-5. No task can drop a wave.

- [files] Every referenced pre-existing symbol was verified in the tree: `normaliseClients`, `DeleteChild`, `DB()`, `updateClient` + `c.rmw`, `ErrClientNotFound`, `UseGlobalBlockedServices`, fake `Hang`/`SetStatus`/`SetUpdateStatus`/`MutateClient`/`LastUpdate`/`MaxInFlightUpdates`, `TestSetBlockedServices_Serialised`, `Manager.RunSweeper`, `decodeChild`, `CodeConflict`/`CodeNotFound`/`CodeAdGuardUnavailable`, `api.AdGuard.Services`, `requireSession`, the X-Requested-With gate in `internal/api/middleware.go`, `sweepInterval` + its Cleanup-restore pattern in `TestRun_Sweeper`, `svcPass`/`aiKey`, `TestRun_NoSecretsInLogs`, and `startup_probe`. `internal/grants/` exists as an empty directory. The store opens with `foreign_keys(ON)`, so t-1's `ON DELETE CASCADE` is live, not decorative.

- [description] t-1's ListActiveGrants is "one query over grant_services then one over grant_clients, each scanned to completion". With `SetMaxOpenConns(1)` and no enclosing tx, another goroutine's SetGrantStatus tx can take the connection between the grants query and the grant_services query; if the services query filters on `active = 1` the grant then reads back with an empty service list. TestGrants_ListRace is written to catch exactly this, so it is a hint for the executor: run the reads inside one read tx, or join on `grants.status` rather than `grant_services.active`.

- [description] Use-global client asymmetry. Create skips a `UseGlobalBlockedServices` client (into `Failed`, no write) and Reconcile never repairs it, but expire/End's revert does `AddBlockedServices` on every stored client, so a use-global client gets a POST to its own (ignored) list on every revert. Harmless, but several t-3 contracts assert on Kid tablet ("Kid tablet re-blocked" in TestEngine_RevertFailureStaysActive, TestEngine_EndRevertFailure) while the fixture ships Kid tablet with `use_global_blocked_services: true`. The executor must mutate it off the global list in those tests too, or the "re-blocked" assertion is testing a write AdGuard ignores.

- [description] Extend + queued timer. If expire(id) is already blocked on mu when Extend runs, Extend stops the old Timer (too late — the callback is live), arms a new one in `timers[id]`, then the queued expire sees `Now() < EndsAt` and re-arms, overwriting `timers[id]` and orphaning Extend's timer. The orphan fires later and is harmless (expire is CAS-guarded and idempotent), but it is a leaked timer until then and `Close()` cannot stop it. TestEngine_ExtendReschedules asserts the visible outcome only.

- [description] Boot with a hung (not erroring) AdGuard: t-6's 30 s bound on Start means `/healthz` and the listener are unavailable for up to 30 s. Inherent to c-4's "before the HTTP listener accepts"; worth a one-line comment beside the 30 s literal in main.go. TestRun_StartupAdGuardDown uses a 500 so it does not exercise this path.

- [strengths] Every contract is "if X is lost, TestY fails: <exact observable>" with request accounting (GET/POST counts per operation) — mutation-resistant and executable as written, and the GET/POST budgets double as a performance contract for c-1's 5 s bound.

- [strengths] The concurrency model is decided up front rather than deferred: one engine mutex across read-decide-write, an ApplyTimeout bound so a hung AdGuard cannot wedge it, List outside the mutex, the CAS as the single exit from `active`, and `-race` tests in t-1, t-3 and t-4 (ListRace, EndVsTimerRace, Serialised, EndRace, RunLoop). This is the hard part of the phase and it is designed, not hand-waved.

- [strengths] The no-op skip lives in the adguard layer (t-2), so c-5's "a pass that finds nothing to do issues no writes" and c-3's idempotent revert fall out of one mechanism instead of being re-implemented per caller; TestAddBlockedServices_Idempotent pins it at the source.

## Summary
No blockers: coverage is complete, all seven locked decisions are honoured and test-pinned, and the wave graph is tight — fix the unowned `grants.MinDuration`/`MaxDuration` consts before execution and tighten the ApplyTimeout wording/test so per-call timeouts cannot slip past c-1's 5 s bound.
