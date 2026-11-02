# Plan Review — shared-devices

Reviewed: 2026-11-02
Plan: 17 tasks across 3 waves

## BLOCKING
(none)

## FLAG
- [test-contract] t-8 now routes POST /children with clients through `AdoptClients` *before* `CreateChild`, but the POST path has no pre-check for a taken name. Today `handleChildCreate` (internal/api/children.go:143) relies only on the store's in-transaction `checkConflicts`. So `POST {name:"ada", clients:[Kid phone]}` would write Kid phone's baseline to AdGuard (merging in the global list and clearing `use_global_blocked_services`, which cannot be undone) and only then return 409 from the store. PUT has the opposite, documented guarantee ("The 404 and every 409 are decided before AdGuard is touched", pinned by `TestChildUpdate_NameStill409`). t-8 has no POST counterpart, and `TestChildCreate_AdoptFailNoChild` covers only the reverse order (adopt fails, so no child is created).
  Suggestion: in t-8, state that POST checks the name before adopting (for example `updateConflict(children, 0, in)`, which after this phase checks the name only). Add a contract: POST `{name:"ada", clients:[Kid phone]}` while Ada exists returns 409 with zero /control/clients/update.
- [test-contract] t-3 and t-11 do not cover a granting child that has been deleted. The locked `grant_coverage_basis` makes this state reachable: a deleted owner's grant "still holds the service open there until it expires". `GrantCover.Child` is a `childRef`, and t-11 builds it from `ListChildren`, which no longer has that child. c-6 requires the view to name the granting child, and t-15 renders "unblocked on <client> by <child>'s grant". Neither task says whether such a grant is skipped, which leaves youtube unblocked with `unblocked_by []` so Home hides it, or emitted with an empty or placeholder name. Neither has a contract for the case.
  Suggestion: in t-3/t-11, choose the behaviour for a grant whose `child_id` is not in `ListChildren`. Add one contract for it, e.g. `TestBlocked_DeletedGranter`: Ada deleted while her youtube grant on Kid phone is active, then check Ben's view.
- [test-contract] Only half of the locked `grant_coverage_basis` is tested at engine level. `TestExpire_ClientUnassigned` (t-5) covers the *reverting* grant: its own stored clients are re-blocked after its child is unassigned. Nothing covers the *holding* grant. Example: Ada is removed from (or deleted off) Kid phone while her youtube grant is still active; Ben's youtube grant on Kid phone then expires, and youtube must stay open. t-2's `revertPlan` is pure and takes `active []store.Grant`, so a t-5 `revert` that filters `active` by current ownership before calling it would pass every listed contract.
  Suggestion: add a t-5 contract for that sequence: Ada unassigned from Kid phone while G_Ada is active, then expire G_Ben. Kid phone must still lack youtube.
- [test-contract] t-9 changes the rename read-back (`renamedView`, internal/api/rename.go:71) to emit `children`. The existing degraded path sets the owner only inside the `ListChildren` success branch and leaves the field zero-valued on failure. With a slice, that marshals as `"children": null`, which breaks the pinned "id ASC, never null". `TestClients_UnassignedEmpty` pins `[]` for GET /clients only, and `TestRename_SharedView` covers only the success path. The SPA's `renameClient` returns the body without the `[]` default that t-4 adds to `listDevices`.
  Suggestion: add a t-9 contract: with `ListChildren` failing after a successful rename, the response is 200 with `"children": []`.

## NOTE
- [coverage] All 8 criteria are covered:

  | Criterion | Covered by |
  | --- | --- |
  | c-1 | t-1, t-8, t-9, t-17 |
  | c-2 | t-4, t-9, t-12, t-16 |
  | c-3 | t-2, t-6, t-8, t-17 |
  | c-4 | t-2, t-5, t-10, t-17 |
  | c-5 | t-4, t-10, t-13, t-15, t-17 |
  | c-6 | t-3, t-4, t-11, t-15, t-17 |
  | c-7 | t-1, t-7, t-8, t-9, t-16 |
  | c-8 | t-4, t-14, t-15 |

  No two tasks in the same wave share a file. The only files that don't exist yet are the ones marked New: 0005_shared_clients.sql, grants/shared.go, grants/shared_test.go and api/shared_test.go. All three locked decisions are respected: no task adds a confirm step, and every coverage rule is keyed on stored grant clients.
- [forbidden-actions] Nothing in r-01..r-04 is touched: no user_rules writes, no vendored AdGuard code, no credential handling. Every check runs through `make`, which fits `runtime.mode = "native"`. There is no global ~/.claude/dross/rules.toml.
- [wave-order] Carried over from the last review and still true: t-9 removes `child` from the /clients JSON in wave 2. Until t-12 lands, `Children.svelte:131` (`c.child !== null && c.child.id ...`) throws, because `undefined !== null`. The header says "`child` is ... removed from the SPA type only in t-16", but the SPA type keeping `child` does not keep the wire field. This only affects intermediate commits.
- [wave-order] t-6's `TestBaseline_SharedSiblingGrant` needs Ben to hold Kid phone while Ada's grant covers it, which in practice is t-1's composite key. t-6 lists `depends_on = ["t-2"]` only. That is harmless because t-1 is in wave 1, but the dependency edge is missing.
- [antipattern] t-8's POST-adopt change also hits clients nobody else owns. `POST /children {Cy, [New TV]}` now converts New TV from the global list to its own list, where it used to make no AdGuard request. That matches PUT, and the SPA always creates with `[]`, so this is API-only. It is still a behaviour change beyond c-3's "sharing" wording and is worth one line in the PR description. t-8 now spans PUT sharing, removal and delete, POST adopt, and migration dedupe across 9 contracts. It is under the file threshold, but it is the heaviest wave-2 task.
- [strength] Every finding from the previous review was fixed concretely:
  - t-4's helpers are typed on `SharedRef`/`ChildRef`.
  - POST-create sharing goes through adopt, with success and failure contracts.
  - `grant_coverage_basis` is locked with the user's confirmation.
  - Both `ListChildren` failure paths (GET /grants, the blocked view) degrade to `[]`, each with a named test.
- [strength] The ref-count rule lives in pure helpers (t-2). Each helper contract names the wrong implementation it catches. t-5 goes after the hard engine failure modes: fail-closed on a failed active-grant read, a stale reconcile snapshot with two overdue grants, concurrent timers (`MaxInFlightUpdates() == 1`), zero writes to a fully covered client, and `Writes` counting actual writes.
- [strength] The pinned wire contract in the plan header lets the SPA wave-1 task (t-4) run in parallel with the backend. The locked `shared_notice` is enforced by negative tests: `Home.test noConfirm` checks for exactly one POST, no `confirm()` and no dialog, and `GrantForm.test hintPassive` checks that the note has no controls and no effect on `valid`.

## Summary
The plan is sound and can proceed. The four flags are each a missing contract or ordering statement, not a design flaw. The POST name-check ordering in t-8 matters most, because getting it wrong causes an AdGuard write that cannot be undone on a request that then fails with 409.
