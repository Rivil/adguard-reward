# Plan Review — permanent-toggles

Reviewed: 2026-11-03
Plan: 11 tasks across 4 waves

## BLOCKING
- [file-scope / build break] t-2 changes `revertPlan`'s signature ("revertPlan takes it", i.e. gains the permanentOpen map), but its only caller, `internal/grants/grants.go:295` (`plan := revertPlan(g, active)`), belongs to t-5 in wave 2. As soon as t-2 lands, package `grants` stops compiling, so t-2's own scoped tests (`shared_test.go`, `intent_test.go`) can't run and its test gate fails. A writer limited to t-2's files can't fix that. The same mismatch makes t-2's third contract contradict itself: "existing TestRevertPlan_* cases pass unchanged with an empty permanentOpen map" can't hold if the five existing `revertPlan(g, …)` calls in `shared_test.go` have to gain an argument.
  Suggestion: either add `internal/grants/grants.go` to t-2's files (a one-line call-site change passing nil), or keep `revertPlan(g, active)` as a wrapper over a new permanentOpen-aware variant that t-5 switches to. Then reword the contract to say the existing cases are edited only to pass an empty map.

## FLAG
- [wave-order] t-2's `permanentOpen(children, toggles)` and `togglePlan(children, toggles, active)` take toggle rows. The obvious type is the store row type (`store.ServiceToggle` or similar) from t-1's `internal/store/toggles.go`, which t-1 creates in the same wave, and t-2 has no `depends_on`. Run in parallel, t-2 has nothing to compile against. (t-3 avoids this because package `blocked` imports only `adguard` and defines its own types.)
  Suggestion: pin the toggle row type in the wire-conventions header and either have t-2 define a grants-local type, or add `depends_on = ["t-1"]` and move t-2 to wave 2.

- [antipattern: file-scope] t-4 says `openOn` is "moved from Home", but `openOn` lives in `web/src/lib/Home.svelte:220`, which t-4 doesn't list. So t-4 can only copy it, and two copies coexist until t-10. t-10's description doesn't say Home switches to `toggles.ts`'s `openOn`/`shown`/`controlFor`, so t-10 could leave the inline copy and its old hide-unblocked filter in place.
  Suggestion: say "copied" in t-4, and state explicitly in t-10 that Home drops its local `openOn` and filter in favour of `toggles.ts`.

- [antipattern: interface mismatch] t-8 says a block does "Add per togglePlan over mapped ∪ trimmed grant's stored clients", but t-2's `togglePlan(children, toggles, active)` takes no client set. Under the pinned precedence, an unowned device like `old-tablet` (in TestToggle_BlockOldDevice) has no owner with a toggle, so it is "AdGuard-owned (no write)" and `togglePlan` produces nothing for it. t-8 has to special-case the trimmed grant's clients outside `togglePlan` (that is really revert semantics for the trimmed service) or change `intent.go`, which isn't in t-8's files.
  Suggestion: say in t-8 how the trimmed grant's off-owner clients get written (e.g. `revertPlan` on the pre-trim grant restricted to the trimmed service, unioned with `togglePlan` for mapped clients), or give `togglePlan` a client-set parameter in t-2.

- [wave-order: parallel conflict] t-8 (`toggle.go`) and t-9 (`reconcile.go`) both run in wave 3 in package `grants`, and both apply `togglePlan` output to live clients: skip missing or `use_global` clients, Remove/Add, record failures. No task owns that shared "apply plan to one client" helper. Run in parallel, they'll either define clashing symbols or diverge on `use_global`/missing-client handling.
  Suggestion: give the helper to one task (t-5 or t-2) or have t-8 depend on t-9, and name it in the wire conventions.

- [test-contract] The 5 s budget in c-1/c-2 is only gated on the engine side. t-11's handler reads the live catalogue first (`a.catalogue(r.Context())`), which is bounded only by the AdGuard client's 10 s `http.Client` timeout. Then comes the engine's 4 s ApplyTimeout, plus any wait on `e.mu` behind a reconcile pass. TestToggle_Deadline (t-8) covers only the engine part, and TestRun_ToggleEndToEnd's "within 5 s" runs against an instant fake, so it can't catch a slow catalogue.
  Suggestion: add a t-11 contract that hangs `blocked_services/all` (and/or holds the engine mutex) and asserts the response returns within 5 s, or bound the catalogue read with its own timeout.

- [granularity: one task that should be two] t-8 carries 12 contracts covering three surfaces with different dependencies: the built-in path (`togglePlan`, Remove/Add, `use_global`), the custom path (`syncRules`/region, malformed region, delete race), and the grant trim/timer/deadline/race behaviour. A failure in any one blocks the whole atomic commit, and the diff will be the largest in the phase.
  Suggestion: consider splitting the custom-service path (TestToggle_Custom, TestToggle_UnknownCustom) into its own task after the built-in Toggle task.

## NOTE
- [coverage] All five criteria are covered (c-1: t-4/t-8/t-9/t-10/t-11; c-2: t-1/t-4/t-7/t-8/t-9/t-10/t-11; c-3: t-2/t-5/t-8; c-4: t-1/t-5/t-9; c-5: t-3/t-4/t-6/t-10). No task contradicts a locked decision. No rule violations: built-ins are checked against the live catalogue (r-01), and custom changes go only through `desiredRegion`/`syncRules` → `EditUserRules` under the mutex (r-02).
- [design] t-1 trims a grant by deleting its `grant_services` row. Once that commits, the grant no longer records that it covered the trimmed service, so the deferred grant-history work ("ended by permanent block") can't reconstruct partial trims, only full ends. If grant-history will want that, a soft marker (e.g. `active = 0` plus a trim reason) would keep the data.
- [design] `use_global` clients end up in Failed (t-8), and the reconciler's toggle half skips them (t-9). The toggle_partial_apply promise that "the reconciler converges them" therefore holds only after the client is migrated off the global list. This matches the phase-04 grant precedent (`reconcile.go:113`), so it is consistent, but it is a case where c-1/c-2's "every mapped client" isn't met within 5 s.
- [design] There is an asymmetry for devices that leave a child. A block writes the trimmed grant's old devices (TestToggle_BlockOldDevice), but a permanent unblock leaves a device open forever once it is unmapped, because it becomes AdGuard-owned and nothing re-blocks it. The spec doesn't address this. Worth knowing before export-import or a later phase relies on toggle state alone.
- [strength] The wire-conventions header pins store signatures, the engine result shape, HTTP status codes, open-wins precedence and the shape of the blocked view before any task starts. That is what lets four wave-1 tasks across store, grants, blocked and web proceed in parallel without guessing.
- [strength] The test contracts are failure-framed and concrete: named fixtures, exact expected lists, POST counts. They also deliberately cover fail-closed read failures (TestRevert_ToggleReadFailure, TestSyncRules_ToggleReadFailure, TestReconcile_ToggleReadFailure), which is the case that would otherwise unblock every child.
- [strength] The plan pays attention to dependencies: t-1 includes `custom_services_test.go` because its "0007 last" migration-order assertion would otherwise break, and t-9 seeds toggle rows via the store specifically to avoid depending on t-8.

## Summary
The plan is well specified and covers every criterion with no locked-decision conflicts, but t-2's `revertPlan` signature change breaks package `grants` until wave 2 and must be fixed. Several cross-task seams (the toggle row type, the `togglePlan` client set, the shared apply helper) need pinning before parallel execution.
