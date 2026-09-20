# Plan Review — 03-children-and-blocked-view

Reviewed: 2026-09-20
Plan: 10 tasks across 4 waves

## BLOCKING
(none)

## FLAG
- [granularity] t-5 touches 7 files across three layers (store interface in `internal/api/api.go`, handlers in `internal/api/children.go`, process wiring + restart proof in `cmd/adguard-reward/main.go` / `main_test.go`). Split candidate.
  Suggestion: keep the CRUD + Deps widening together (they are one compile unit), but `TestRun_ChildrenSurviveRestart` and the `main.go` `Children: st` line are a self-contained 2-file follow-on that could ride with t-7 or be its own task, so a failure in the restart harness does not block the CRUD commit.

- [wave-order] t-10 (wave 4) does not consume any output of t-7: `handleMigrationOffer`/`handleMigrationApply` need only `ListChildren` (t-1), `Clients`/`MigrateFromGlobal` (t-2, widened into the interface by t-5) and `blocked.Offer` (t-3). The only reason it cannot sit in wave 3 is that both t-7 and t-10 append route lines to `internal/api/api.go`.
  Suggestion: either accept wave 4 and state "serialised on api.go" in `depends_on` intent, or have t-5 pre-register all six remaining route lines against not-yet-written handlers is not possible in Go — so the honest fix is to leave it and record why. Low priority.

- [correctness] t-1's `ListChildren` is described as "two queries with the first fully scanned and closed before the second". With `SetMaxOpenConns(1)` and no wrapping transaction, a `CreateChild`/`DeleteChild` on another goroutine can land between the two queries, so query 2 can return `child_clients` rows for an id absent from query 1 (or miss rows for one present in it). `TestChildren_ListRace` only asserts "no error", so an implementation that panics on an unknown `child_id` or silently drops it passes/fails on luck.
  Suggestion: either issue one `LEFT JOIN children ⟕ child_clients ORDER BY children.id, client_name` query (no window, no second round-trip, still single-connection safe), or wrap the two reads in a read tx. Add to the ListRace contract that every returned child's `Clients` equals the rows for that id at the moment of the read (or drop the race test and make it a single-goroutine "nested query deadlocks" test, which is what the parenthetical actually describes).

- [locked-decision-fidelity] `migration_offer_ux` says "'not now' hides it for the session and it reappears on next login". t-4's `migrationDismissed` is a module-level Svelte store, so a browser reload (F5) within the same login session re-initialises it to `false` and the banner resurfaces before the next login. t-9's contract explicitly asserts `sessionStorage`/`localStorage` untouched, so this is the author's deliberate reading of "no persisted dismissed flag" — but it is narrower than "for the session".
  Suggestion: no change needed if reload-resurfacing is acceptable (the locked `why` — "nothing to store and nothing to drift" — supports in-memory). Record the consequence in t-9's description so the executor does not "fix" it with `sessionStorage` and break the contract. If it is not acceptable, `sessionStorage` is per-tab and cleared on close, which arguably is not "persisted"; that would be a spec-level call, not a plan-level one.

- [test-contract] t-4 'messageFor conflict': `messageFor` already returns `e.message` on the `default` branch, so adding a `'conflict'` case is a no-op and the contract "if a 409 loses its message, 'messageFor conflict' fails" cannot fail against either the current or the new code — the test discriminates nothing.
  Suggestion: either drop the explicit case and keep the test as a regression pin on the default branch (rename the intent: "if the default branch stops echoing the server message"), or drop the test line. Trivial.

## NOTE
- [coverage] All eight criteria are covered: c-1 (t-1, t-5), c-2 (t-7), c-3 (t-7), c-4 (t-3, t-7), c-5 (t-4, t-8), c-6 (t-4, t-6), c-7 (t-2, t-3, t-4, t-9, t-10), c-8 (t-1).

- [locked-decisions] No conflicts. `cross_client_state` is implemented verbatim in t-3's fold; `global_list_clients` in t-3 (UsesGlobal resolves from `Global`) and t-6 (badge); `migration_offer_ux` in t-4/t-9 (see FLAG above for the reload nuance); `dangling_client` in t-1 (no prune), t-3 (Missing excluded from fold), t-8 (struck-through + Remove); `client_identity` in t-1's schema (names only).

- [forbidden-actions] Nothing in the plan runs pnpm/go directly outside `make`; runtime mode is native so there is no container constraint. r-01 (no vendoring) is honoured by t-7's `/services` passthrough and t-3's "the catalogue is the view" rule. r-02/r-03/r-04 are not in scope of this phase.

- [existing-surface] Every helper the contracts lean on already exists: `Store.DB()` (store.go:105), `foreign_keys(ON)` in the DSN (store.go:85), `adguardtest.MutateClient`/`SetAuth`/`SetResponse`/`Hang`/`MaxInFlightUpdates`/`LastUpdate`, `request()` returning `undefined` on 204 (api.ts:104). No api-package test constructs a stub `AdGuard`, so t-5's interface widening only has to satisfy `*adguard.Client`. Non-numeric `{id}` → 404 matches the existing sessions handler (sessions.go:48-50).

- [intermediate-state] Between t-6 and t-8 the Home "Children" button navigates to a route `App.svelte` does not render, so a signed-in user clicking it lands on the Login view. Harmless within a phase branch, but the t-6 commit message should not claim the button "works".

- [design] t-10's `POST /api/v1/migration` takes no body and recomputes the offer server-side. A client that flips to `use_global_blocked_services=true` between the parent viewing the banner and clicking Migrate is written without having been shown. Single-parent household, low likelihood, and the write is the same one the parent would be offered next — accepted as a conscious trade-off, but worth a line in the description.

- [t-9 silent failure] `MigrationBanner` renders nothing when `migrationOffer()` fails on the grounds that Home's error state "already covers AdGuard down". That holds only because `/migration` and `/children/{id}/blocked` hit the same AdGuard endpoints; if `/migration` alone 502s (e.g. a store error in `ListChildren` after the blocked views cached nothing), the offer vanishes with no signal. Acceptable for v0.1.

- [strength] The wire contract pinned at the top of plan.toml lets t-4 (web API client) sit in wave 1 alongside the Go work and lets t-6/t-8/t-9 test against stubs that match what t-5/t-7/t-10 will actually serve. This is the single biggest reason the plan gets four parallel tasks in wave 1.

- [strength] Test contracts are uniformly of the form "if <specific mutation>, <named test> fails: <inputs> → <exact outputs>". Several pin raw-body facts that JSON decoders would hide (`"differs":[]` never null, `"child":null`, `{"children":[]}` on a fresh store), which is exactly where LLM-written tests usually go soft.

- [strength] Extracting the three-way fold and the offer computation into a pure `internal/blocked` package with no I/O and no store import means the hardest logic (c-4, c-7 scope) is unit-tested in wave 1 with fixture inputs, and t-7/t-10 reduce to plumbing.

## Summary
No blockers; coverage is complete and every locked decision is honoured — the substantive item is t-1's two-query `ListChildren` consistency window under concurrent writes, which a single LEFT JOIN removes, plus a granularity split on t-5 and a reload-resurfacing nuance on the migration dismissal that should be stated rather than discovered.
