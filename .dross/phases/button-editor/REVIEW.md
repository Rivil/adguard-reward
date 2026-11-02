# Plan Review — button-editor

Reviewed: 2026-11-02
Plan: 6 tasks across 2 waves

## BLOCKING
(none)

## FLAG
- [wave-order] t-6 is wave 2 but `depends_on = ["t-5"]`, which is also wave 2. The plan schema (dross plan.md: "depends_on — Task ids in lower waves") requires dependencies to sit in a lower wave, and every earlier phase's plan follows that. The dependency is real: Home reads `Button`, which `extends ButtonInput` in web/src/lib/api.ts, and t-5 is the task that adds `icon` to `ButtonInput`. So t-6 cannot type-check before t-5 lands.
  Suggestion: move t-6 to wave 3. The other fix, giving the api.ts type change to t-6, would put two wave-2 tasks on the same file.

- [wave-order] t-5's `depends_on` lists only t-4, but its contract 'reorder and delete keep icons' needs t-2's Down control and t-3's delete Confirm. Wave 2 placement orders it correctly by accident. The dependency record is still incomplete.
  Suggestion: add t-2 and t-3 to t-5's `depends_on`.

- [wave-order] t-2 and t-3 are both wave 1, which marks them as parallelisable. Both edit the same per-row `.actions` block in web/src/lib/Buttons.svelte, and both edit web/src/lib/Buttons.test.ts. Run in parallel or in separate worktrees, they conflict on the same lines. Run serially, the second task works on code the first just rewrote.
  Suggestion: either accept that they run serially and make t-3 depend on t-2 in wave 2, or note in the plan that wave 1 is serial for these two.

- [test-contract] No contract covers reordering or deleting while the edit form is open. Buttons.svelte tracks the edited row as a full-list index (`editing = i`), and `save()` writes `next[editing] = draft()`. If you tap Edit on one button and then Up/Down on it or on its neighbour (t-2), or Delete on an earlier row (t-3), that index points at a different button, and Save overwrites the wrong one. Ids can't be used to recover it, because every PUT returns fresh ids. Delete already has this latent bug. t-2 makes it easy to hit, since reorder controls stay live while editing (they are disabled only on `pending`).
  Suggestion: give t-2 (and t-3) a contract that pins the behaviour. Either reorder/delete are disabled while `editing !== null`, or after a reorder/delete Save still replaces the button that was being edited (check this by label in the PUT body).

- [test-contract] t-3's description says "only one row can be confirming at a time", but no contract checks it. Nothing defines what happens to an open confirmation when you tap Up/Down on another row, which shifts the indices. The confirming row is presumably tracked by index, which has the same weakness as `editing`.
  Suggestion: add a clause to the t-3 contract. Opening Delete on a second row closes the first row's confirmation, and an open confirmation either blocks reorder or survives it on the same button.

- [test-contract] t-3's 'delete PUTs without the item' contract does not name a fixture. The existing test uses `[b1, b2]` and deletes b1, which is first in both list order and grouped display order. So it cannot catch the grouped-display → full-list index mapping that t-2 introduces.
  Suggestion: have the contract use the interleaved `[b1 (Ada), b2 (Ben), b3 (Ada)]` fixture and delete b2 or b3. Then a display-index/list-index mix-up sends the wrong list.

- [test-contract] t-5 covers the catalogue-outage case (select disabled, stored icon sent as-is) but not the other locked `icon_fallback` case: the catalogue is up and the stored icon id is no longer in it. In that state the `<select>` has no matching `<option>`. Nothing pins what it shows or what Save sends: the stale id, which the server will 422, or a silent `""`, which drops your choice and which the decision's "why" forbids.
  Suggestion: add a t-5 contract for "icon 'gone' with the catalogue up". Pin what the picker shows and what the PUT body carries.

## NOTE
- [coverage] All three criteria are covered (c-1: t-2; c-2: t-1, t-4, t-5, t-6; c-3: t-3). The "Home renders in the new order after a server restart" half of c-1 has no new contract. It is already guarded by internal/store/buttons_test.go `TestButtons_OrderPersists` and Home.test.ts 'renders buttons per child in stored order'. Verify should map c-1 to those two tests as well as to t-2's.

- [locked-decisions] No conflicts. `icon_source` → t-4 validates against the catalogue and t-5 lists every catalogue service. `reorder_scope` / `reorder_save` → t-2. `icon_fallback` → t-5 (outage) and t-6 (Home fallback). One consequence of `icon_source` + `icon_fallback`: once a stored icon id leaves the catalogue, every whole-list PUT returns 422, including reorder and delete of unrelated buttons, until you clear that icon. Unknown services already behave this way, so this is consistent and not a plan defect. It is worth knowing when writing the t-5 stale-icon contract above.

- [churn] t-2's grouping changes display order for the existing `[b1, b2, b3]` fixture to `[b1, b3, b2]`. So 'edit keeps position and converts units' (asserts `rows()[1]` contains 'Renamed') will need updating in t-2. t-3 rewrites 'delete PUTs without the item'. Both changes are expected, not regressions.

- [strengths] t-5 → t-4 ordering is correctly motivated. `decodeGrantBody` uses `DisallowUnknownFields`, so a client that sends `icon` before the API accepts it would get a 400 on every PUT. t-5's 'reorder and delete keep icons' contract targets the most likely regression: `toInput` silently dropping `icon`.

- [strengths] t-4's contracts pin the existing catalogue-read budget: exactly one `/control/blocked_services/all` hit with icon and services set, and zero on `PUT []` with the catalogue down. That keeps icon validation from quietly adding an AdGuard round-trip.

- [strengths] t-1's contracts check storage structurally: the raw column is NULL for "", the icon survives Close/Open, and pre-0006 rows survive the migration intact. t-2's `[Ada1, Ben1, Ada2]` swap case exercises the cross-child skip that `reorder_scope` exists for.

## Summary
Coverage is complete and the plan respects every locked decision and rule. Fix two things before execution: t-6 needs to move to wave 3, and the index-based edit/confirm state needs a contract for reorder and delete. Neither is blocking.
