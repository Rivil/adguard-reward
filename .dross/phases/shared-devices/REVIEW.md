# Plan Review — shared-devices

Reviewed: 2026-11-02
Plan: 17 tasks across 3 waves

## BLOCKING
(none)

## FLAG
- [antipattern] The SPA helper's shape conflicts with the pinned wire type. The plan header pins `sharedRef = {client, others: childRef[]}`, and t-4 types `Grant.shared` / `GrantCreated.shared` as that `SharedRef`. But t-4's own contract has `sharedDevices` return `others` as bare strings (`[{client:"Kid phone", others:["Ben"]}]`), and its `sharedText` example passes names (`[{Kid phone, [Ben, Cleo]}]`). `sharedText` is then fed from both sources: wire `grant.shared` in t-13's ActiveGrants row, and `sharedDevices` output in t-14 and t-15. If the helper is written against strings, the active-grant row renders `[object Object]` or fails typecheck. If it is written against `ChildRef`, t-4's test fixtures are wrong.
  Suggestion: in t-4, make `sharedDevices` return `SharedRef[]` (`others: ChildRef[]`) and make `sharedText` take `SharedRef[]`. Write both test fixtures with `{id, name}` so the hint, the notice and the row all go through one typed path.
- [coverage] c-3 says sharing a client with a further child merges that child's baseline. t-1 opens a second sharing path: POST /api/v1/children with a client another child holds now returns 201 (`TestChildren_CreateShared`). But `handleChildCreate` (internal/api/children.go:143) calls `CreateChild` directly and never calls `AdoptClients`, and no task changes that. The new child's baseline (AdGuard's global list, since it has no siblings) is never merged onto the shared client, so c-3 holds only for PUT. The SPA always creates with `createChild(name, [])` (Children.svelte:104), so the gap is API-only.
  Suggestion: either state in t-1 or t-8 that a shared client added at POST-create is deliberately not adopted (matching today's no-adopt POST), or route POST-with-clients through `AdoptClients` and give that a contract.
- [locked-decision] `grant_coverage_basis` resolves how the locked `shared_device_rule` is read: coverage follows stored clients, so an unassigned or deleted owner's grant still holds a service open. It is recorded with `locked = false`, yet `TestRevertPlan_StoredClients` (t-2) and `TestExpire_ClientUnassigned` (t-5) hard-code it, and so does the plan header ("never on current ownership"). If the user reverses it, both tests and t-2's helper signatures change. The decision's `why` cites this review process, not a user sign-off.
  Suggestion: get the user's explicit confirmation and set `locked = true` before t-2 and t-5 execute.
- [test-contract] t-10 and t-11 both add a `ListChildren` read to a GET path, and their failure semantics are uneven:
  - t-11's description says a failed *children* read degrades to `shared_with: []` with a warning, but only the grants-read failure has a contract (`TestBlocked_GrantsReadFails`). A mutation that turns the children-read failure into a 500 survives.
  - t-10 adds a `ListChildren` read to GET /grants (to compute `shared` "from the current mapping") but never says whether that read failing is a 500 or a degrade. No test pins it either. That endpoint is the phone's polled active-grants list, so the choice decides whether a store hiccup blanks the countdowns.

  Suggestion: add one contract to t-11 for the children-read failure. In t-10, state the GET /grants behaviour when `ListChildren` fails and add one contract for it.

## NOTE
- [coverage] All 8 criteria are covered:

  | Criterion | Covered by |
  | --- | --- |
  | c-1 | t-1, t-8, t-9, t-17 |
  | c-2 | t-4, t-9, t-12, t-16 |
  | c-3 | t-2, t-6, t-17 |
  | c-4 | t-2, t-5, t-10, t-17 |
  | c-5 | t-4, t-10, t-13, t-15, t-17 |
  | c-6 | t-3, t-4, t-11, t-15, t-17 |
  | c-7 | t-1, t-7, t-8, t-9, t-16 |
  | c-8 | t-4, t-14, t-15 |

  No two tasks in the same wave share a file. Every referenced file exists or is marked New (0005_shared_clients.sql, grants/shared.go, grants/shared_test.go, api/shared_test.go). Every `depends_on` edge needs its predecessor's output, so no task can drop a wave.
- [forbidden-actions] Nothing in r-01..r-04 is implicated. Every check runs through `make` targets, which fits `runtime.mode = "native"`. There is no global ~/.claude/dross/rules.toml.
- [granularity] t-1 sits exactly at the 5-file threshold and spans migration, store and API test. That is justified: the API test file is there only so that flipping the POST "taken client" case keeps wave 1 green. Splitting it would bring back the red-wave problem.
- [wave-order] This corrects the previous review's "without crashing" note. Once t-9 drops `child` from the /clients JSON, Children.svelte:131 (`c.child !== null && c.child.id ...`) throws a TypeError, because `undefined !== null` is true. That lasts until t-12 rewrites the line. ClientNames.svelte only hides owners (`{#if c.child}`). Both tasks are in wave 2 and the phase ships as one PR, so this only affects intermediate commits. In pair mode, don't open the children page in `make dev` between t-9 and t-12.
- [wave-order] t-9's `TestRename_SharedView` goes through the engine and store rename that t-7 rewrites, but t-9 depends only on t-1. After t-1 alone, a plain shared rename already works (`UPDATE child_clients ... WHERE client_name = ?` moves every owner row), so the coupling is soft.
- [antipattern] In package `api`, three wave-2 tasks each derive owners per client in parallel: t-9's `ownerIndex` becomes `map[client][]childRef`, t-10 adds `sharedOf`, and t-11 builds `Owners` for `blocked.Input`. Watch for duplicate helpers or name collisions when they land on one branch.
- [strength] Every finding from the previous review was addressed concretely:
  - t-1 now owns the POST case in `TestChildren_Conflicts`.
  - t-8 names `TestChildren_PutAtomic` and gives it a failure trigger that survives the 409 removal (an adopt failure on the fake).
  - t-5 adds `TestReconcile_WritesCountsActual`.
  - The stored-clients reading is written down as `grant_coverage_basis`.
- [strength] The ref-count rule is isolated in pure helpers (t-2). Each contract names the specific wrong implementation it catches: self-counted, per-grant instead of per-client, per-grant instead of per-service, ownership consulted. t-5 goes after the hard failure modes: fail-closed on a failed read, a stale reconcile snapshot when two grants are both overdue, concurrent timers via `MaxInFlightUpdates`, and a covered client getting zero writes.
- [strength] The locked `shared_notice` decision is enforced with negative tests: `Home.test noConfirm` checks for exactly one POST, no `confirm()` and no dialog, and `GrantForm.test hintPassive` checks that the note has no control and does not affect `valid`. The pinned wire contract lets the SPA wave-1 task run in parallel with the backend.

## Summary
The plan is sound and can proceed. Before executing, fix the SharedRef `others` type mismatch in t-4 and lock (or get the user to confirm) `grant_coverage_basis`. The POST-create baseline gap and the unspecified `ListChildren` failure paths are smaller fixes worth making in the same edit.
