# grant-history: panel synthesis

## Scores

| Draft | Criteria coverage | Test-contract specificity | Granularity | Wave correctness |
|---|---|---|---|---|
| risk | 6/6. Each criterion has one owner and a full write → read → wire → render → restart chain. It is the only draft that proves c-5 through `run()` and the only one that guards locked deleted_child_history against SQLite reusing a child id. | Very high. Most contracts name the mutation they kill: backfill, in-tx old_ends_at, timer-vs-toggle race, all-or-nothing trim, closure bug on per-child links, golden-key wire test. | Good. 10 tasks of 2–4 files. t-4 spans store and engine (2 layers, 4 files), which is at the limit but inside it. | Mostly strict. t-9 → t-7 is justified only by a shared `api.go` (weak). t-6 → t-3/t-4 is defensible because it reads rows the real writers produced. |
| mvp | 6/6 nominally, but thin. c-5 rests on one engine restart test, c-6 sits in one contract, and nothing reaches the UI end to end. | Medium. The contracts are concrete but fewer. There are no failure-path contracts for UI paging or errors, and none for a revert that fails or the timer-vs-toggle race. | Too coarse. t-1 is 8 files across 2 layers. t-4 is 6 files mixing route, component and wiring. t-3 is 5 files across store and API. | Correct for its own graph. t-4 → t-3 is strict only because no wire shape is pinned, so the web track waits for the whole backend. |
| verification | 6/6. Strong c-6 migration proof and an engine-level clock-accurate c-5 restart test. It adds the DeleteCustomService and drift-repair regressions that soft-delete would cause. | Very high. Mutation-named contracts throughout, an equal-started_at page-boundary seed, and rename/delete cross-layer name contracts. | Good. 10 tasks of 3–4 files. Engine work is split across t-3 (Extend call site) and t-7, so `internal/grants/grants.go` is touched in two waves. | Some weak edges. t-7 → t-5 exists only to assert through the history read. t-10 waits for wave 4 to get an end-to-end contract that could live in the endpoint task if snapshotting ran first. |

**Skeleton: risk.** It matches verification on contract sharpness. Its side-table schema leaves the five live `grant_services` consumers untouched; I checked this against `loadGrants`, `trimGrant`, `DeleteCustomService`, the `CreateGrant` overlap check and the `SetGrantStatus` active flip. That protects the core value ("the block comes back by itself, no drift"). It also has the only real end-to-end c-5 proof, plus a code-confirmed child-id-reuse guard: `children.id` is `INTEGER PRIMARY KEY` without AUTOINCREMENT in 0002.

## Merged plan

Pinned contracts (the parallel tasks build against these):

- **Schema 0009 (t-1):** `grants.end_reason TEXT NULL CHECK IN ('expired','ended_early','permanent_block')`, with no backfill. Plus `grant_service_names(grant_id FK CASCADE, service_id, name NOT NULL, PK(grant_id, service_id))`, `grant_service_removals(grant_id FK CASCADE, service_id, removed_at NOT NULL, reason CHECK IN ('permanent_block'), PK(grant_id, service_id))`, `grant_extensions(id PK, grant_id FK CASCADE, extended_at, old_ends_at, new_ends_at)` and `grants_child_history_idx ON grants(child_id, id)`. `grant_services` keeps its DELETE-on-trim semantics. *(Stored vocabulary is `ended_early`, not risk's `parent_end`; see D-3.)*
- **Wire:** `GET /api/v1/children/{id}/history[?before=<grant id>]` → `{ "child": {"id","name"}, "grants": [ {id, status: ongoing|expired|ended_early|permanent_block|ended_unknown, started_at, ends_at (scheduled), ended_at|null, services: [{id, name, removed_at|null, removed_reason|null}], extensions: [{at, old_ends_at, new_ends_at}] (chronological)} ], "next_before": id|null }`. Results are newest first by id with a fixed page of 50. *(The `child` object comes from verification; see D-4.)*

```
Phase grant-history — 10 tasks across 5 waves

Wave 1
  t-1  Add grant-history schema migration 0009                                   [risk+verification]
       files:    internal/store/migrations/0009_grant_history.sql, internal/store/store_test.go
       covers:   c-6, c-1, c-2, c-3
       depends:  —
       desc:     Add the end_reason column, the names/removals/extensions tables and the history index
                 exactly as pinned. No UPDATE of existing rows.
       contract: - Upgrading from 0008 (migrationsThrough 0008) with one expired, one ended and one active
                   legacy grant leaves status, started_at, ends_at, ended_at, grant_services and
                   grant_clients byte-identical, with end_reason NULL on all three. A backfill that writes
                   any reason to legacy rows fails this. [risk+verification]
                 - INSERT with end_reason='bogus' is rejected, and so is a removal reason other than
                   'permanent_block'. [risk+verification]
                 - schema_migrations lists 0009_grant_history.sql directly after 0008_service_toggles.sql. [risk]

  t-2  Add history route, child param and client                                 [risk+verification]
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts
       covers:   c-4
       depends:  —
       desc:     Add Route 'history' (/history), a historyChild store parsed from ?child=, navigate(r, params),
                 HistoryPage/HistoryGrant types per the pinned wire shape (including `child`), and
                 fetchHistory(childId, before?).
       contract: - navigate('history', {child: 3}) pushes '/history?child=3' and historyChild === 3. A later
                   navigate('home') pushes '/' with no query left over. [risk]
                 - routeFor on /history with ?child= missing, 'abc', '0', '-1' or '3.5' resolves to 'home',
                   never to a history route with a NaN or 0 child. [risk] (verification's alternative: see D-7)
                 - popstate from /history?child=3 to /history?child=5 updates historyChild to 5. A stale 3
                   fails. [risk]
                 - fetchHistory(3) requests GET /api/v1/children/3/history with no query, and
                   fetchHistory(3, 40) appends ?before=40. A 404 rejects with ApiError status 404.
                   [risk+verification]

Wave 2 (t-3, t-4 depend t-1; t-5 depends t-2)
  t-3  Record permanent-block trims in removal log                                [risk+mvp+verification]
       files:    internal/store/toggles.go, internal/store/toggles_test.go, internal/grants/toggle_test.go
       covers:   c-3, c-1
       depends:  t-1
       desc:     trimGrant inserts a grant_service_removals row in the same tx as the DELETE. A trim that
                 empties the grant sets end_reason='permanent_block' along with status and ended_at.
       contract: - Blocking one of a grant's two services writes the removal (service, at, 'permanent_block').
                   The grant stays active with end_reason NULL, and ListActiveGrants().Services no longer
                   contains the service. [risk]
                 - Blocking the last service writes the removal row and sets status 'ended',
                   end_reason 'permanent_block' and ended_at = at. [risk+mvp+verification]
                 - An unblock toggle, or a block with no live grant covering the service, writes no removal
                   row. [risk]
                 - After a trim-ended grant, SetGrantStatus(active→expired) returns false and end_reason stays
                   'permanent_block'. [risk]
                 - Pre-seeding a conflicting removal row makes SetServiceToggle fail and roll back: no
                   service_toggles row is written, and grant_services still holds the service. [risk]
                 - After trim + unblock, a new grant for the same (child, service) is accepted, not treated
                   as an overlap. DeleteCustomService of a custom:N trimmed off a still-live grant succeeds
                   rather than returning *ErrServiceInUse. [mvp+verification]
                 - Engine: Toggle(block, tiktok) on a live two-service grant, then Reconcile, leaves tiktok in
                   the client's blocked_services (drift repair did not strip it). [verification]

  t-4  Record end reasons, extensions, name snapshots                             [risk+verification]
       files:    internal/store/grants.go, internal/store/grants_test.go, internal/grants/grants.go,
                 internal/grants/grants_test.go
       covers:   c-1, c-2, c-4
       depends:  t-1
       desc:     SetGrantStatus derives end_reason from `to` (expired→'expired', ended→'ended_early').
                 ExtendGrant(ctx, id, newEndsAt, at) reads the old ends_at inside its tx and inserts an
                 extension row. CreateGrantNamed(…, names) writes grant_service_names; CreateGrant delegates
                 with nil. Engine gains CreateNamed (Create delegates), Extend passes e.now(), and the Store
                 interface switches to CreateGrantNamed.
       contract: - Calling ExtendGrant twice yields two rows with second.old_ends_at == first.new_ends_at. An
                   upsert, or reading old after the UPDATE (old == new), fails. ExtendGrant on an expired grant
                   returns ErrGrantNotFound and inserts no row. [risk+verification]
                 - SetGrantStatus active→ended stores 'ended_early', and active→expired stores 'expired' with
                   ended_at = at. [risk+verification]
                 - CreateGrantNamed with an overlap error leaves no grant and no name rows. A names key not in
                   services writes no orphan row. CreateGrant with nil names writes none. A later
                   UpdateCustomService rename leaves the stored name unchanged. [risk+verification]
                 - Engine End where AdGuard's add fails returns *ErrRevertFailed, and the row stays active with
                   end_reason NULL. [risk+verification]
                 - Engine: Create → clk+10m → End gives ended_at == clk.Now() at End. A timer expiry after an
                   Extend re-arm records 'expired' with ended_at = fake clock at revert, not the original
                   ends_at. Extend ×2 at different clock times records extended_at equal to those times.
                   [risk+verification]

  t-5  Build History screen with paging                                          [risk+verification]
       files:    web/src/lib/History.svelte, web/src/lib/History.test.ts, web/src/lib/history.ts,
                 web/src/lib/history.test.ts
       covers:   c-4, c-1, c-2, c-3, c-6
       depends:  t-2
       desc:     Per-child screen with the child's name and a back button. It lists grants newest first, each
                 with services (removed ones marked), start, end, an end-status label and extensions. A
                 load-more control drives fetchHistory(child, next_before). Pure label and end-time helpers
                 live in history.ts.
       contract: - statusLabel('ended_unknown') renders "Ended (reason unknown)", never "Ended early".
                   'ongoing' renders "Ongoing" with no end time even when ends_at is in the past.
                   endTimeOf(grant) returns ended_at, never the scheduled ends_at. [risk+verification]
                 - A removed service renders with a "removed <time> · permanent block" marker inside the
                   grant's list. A grant with services [] (legacy trim) renders "—" without throwing. [risk]
                 - Two rapid load-more clicks issue exactly one fetch. The appended page is deduped by id and
                   the first page stays visible (append, not replace). Load-more is hidden when next_before is
                   null. [risk+verification]
                 - A load-more 500 keeps the rows already shown, shows an error, and retry reuses the same
                   before cursor. [risk]
                 - An initial 404 shows "child not found" with the back button and no list. An initial 500
                   shows retry, not the empty state. Zero grants shows "No grants yet". [risk+verification]
                 - A twice-extended grant renders two extension entries in chronological order, each old → new.
                   [risk+verification]

Wave 3
  t-6  Add paged per-child history store read                                    [risk+verification]
       files:    internal/store/history.go, internal/store/history_test.go
       covers:   c-4, c-3, c-2, c-6
       depends:  t-3, t-4
       desc:     ListGrantHistory(ctx, childID, before, limit) reads grants, live and removed services with
                 snapshot names, extensions and the child row inside one inTx. It reads limit+1 rows to work
                 out has-more, and filters out grants started before the child's created_at.
       contract: - 51 grants give a first page of 50 newest-first with has-more. Exactly 50 gives has-more
                   false. [risk+verification]
                 - A grant created between the page-1 and page-2 reads appears neither in page 2 nor as a
                   duplicate. [risk]
                 - Deleting the highest-id child and creating a new one (SQLite reuses the id) gives the new
                   child an empty history. [risk] (contradicts verification's store contract; see D-5)
                 - A grant with one trimmed and one live service lists both, the trimmed one with removed_at
                   and reason. A grant ended by its last trim still lists that service. Extensions come back
                   in ascending order. [risk+verification]
                 - A legacy grant seeded at 0008 and upgraded returns its services with an empty snapshot name,
                   its times, status 'ended' and a NULL reason (no inference from status). Another child's
                   grants never appear. [risk+verification]
                 - Store Close + Open on the same dir returns an identical history. [verification]

  t-7  Snapshot catalogue names on grant create                                  [risk+mvp+verification]
       files:    internal/api/grants.go, internal/api/grants_test.go, internal/api/api.go
       covers:   c-4
       depends:  t-4
       desc:     handleGrantCreate builds names from the catalogue it already reads (keyed by trimmed id)
                 and calls Grants.CreateNamed. The Grants interface swaps Create for CreateNamed.
       contract: - POST /grants with a built-in and a custom:<n> service stores names equal to the catalogue
                   names, with no extra AdGuard request. [risk+mvp]
                 - A service id sent with surrounding whitespace still gets its snapshot, because the lookup
                   is keyed by the trimmed id. [risk]
                 - A 409 overlap or 422 unknown-service create writes no name rows, and the existing
                   409/422 cases still pass. [risk+verification]

  t-8  Mount history route and Home links                                        [risk+verification]
       files:    web/src/App.svelte, web/src/App.test.ts, web/src/lib/Home.svelte, web/src/lib/Home.test.ts
       covers:   c-4
       depends:  t-5
       desc:     App renders History for route 'history' with the parsed child. Home gets a "History" link
                 for each listed child.
       contract: - Reloading at /history?child=3 keeps the URL /history?child=3 after refresh() and renders
                   History for child 3. [risk+verification]
                 - With two children, each History link navigates to its own child id; a closure bug pointing
                   both at the last child fails. [risk+verification]
                 - History's back button returns to '/' and Home. [risk+mvp+verification]

Wave 4
  t-9  Serve GET child history endpoint                                          [risk+verification+mvp]
       files:    internal/api/history.go, internal/api/history_test.go, internal/api/api.go
       covers:   c-1, c-2, c-3, c-4, c-6
       depends:  t-6, t-7
       desc:     Route GET /api/v1/children/{id}/history behind requireSession. ChildStore gains
                 ListGrantHistory (already wired through `Children: st`). endStatus maps (status, reason) to
                 the wire status. Names resolve from the snapshot, then the live catalogue, then the bare id;
                 the catalogue is read only when a snapshot is missing.
       contract: - endStatus table: (ended, NULL) → "ended_unknown", never "ended_early". (expired, NULL) and
                   (expired, 'expired') → "expired". (ended, 'ended_early') → "ended_early".
                   (ended, 'permanent_block') → "permanent_block". (active, *) → "ongoing". [risk+verification]
                 - End to end through the real store and engine: POST a grant for custom:1 'Roblox', rename it
                   to 'Games', GET history → 'Roblox'. DELETE custom:1 after the grant ends → still 'Roblox'.
                   Change the fake catalogue's built-in name → the snapshot still wins. A legacy grant naming a
                   deleted custom:7 shows "custom:7". [verification, moved here from its t-10; see D-10]
                 - AdGuard Services() returning 500 still gives 200, with snapshot names intact and bare ids
                   for unsnapshotted services. With all names snapshotted, no catalogue request is made.
                   [risk+verification]
                 - A real-store grant extended twice and then ended returns ended_early, two extensions and
                   ended_at set. A trimmed service is listed with removed_reason. [verification]
                 - 51 grants give 50 + next_before, and following it returns 1 + null. [mvp+verification]
                 - A non-numeric id, or an unknown or deleted child, → 404. ?before=abc, -1 or 0 → 400.
                   No session → 401. [risk+verification]
                 - Golden-key test: the response JSON has exactly the pinned field names, and lists are [],
                   never null. [risk]

Wave 5
  t-10 Prove history across restart and downtime                                 [risk]
       files:    cmd/adguard-reward/main_test.go
       covers:   c-5, c-1, c-2
       depends:  t-9
       desc:     End-to-end runs through run(), reusing the rewindGrant/startWith helpers: restart survival,
                 expiry of a grant that went overdue during downtime, and AdGuard down at startup.
       contract: - A grant ended early after two extensions still shows ended_early with two extensions via
                   GET history after a restart.
                 - A grant whose ends_at passes while the app is stopped reads "expired" after restart, with
                   ended_at ≥ restart time (set by the startup pass, not by ends_at).
                 - With AdGuard unreachable at startup, the overdue grant reads "ongoing", not "expired",
                   until a later pass reverts it.
```

Coverage: c-1 → t-1, t-3, t-4, t-5, t-9, t-10. c-2 → t-1, t-4, t-5, t-6, t-9, t-10. c-3 → t-1, t-3, t-5, t-6, t-9. c-4 → t-2, t-4, t-5, t-6, t-7, t-8, t-9. c-5 → t-10 (t-6 adds reopen at store level). c-6 → t-1, t-5, t-6, t-9. All 6 of 6 are covered.

Wave edges changed from the skeleton: t-9 → t-7 was a weak shared-`api.go` edge in risk. It is now strict, because t-9 carries verification's POST→rename→GET contract, which needs t-7's snapshotting.

## Disagreements

**D-1. Where trimmed services live**
- **risk:** a side table, `grant_service_removals`, with `grant_services` keeping DELETE-on-trim.
- **mvp and verification:** soft-delete in `grant_services` (`active=0, removed_at, removed_reason`), filtering removed rows in `loadGrants`, trim's left-count and `DeleteCustomService`.
- **Default:** side table (minority, 1 of 3).
- **Why it matters:** the code check confirms soft-delete needs a correct filter in three live paths (`grants.go:206`, `toggles.go:95`, `custom_services.go:199`). Missing any one of them silently re-unblocks a permanently blocked service or blocks custom-service deletion; that is the revert/no-drift core value. Verification wrote contracts for all three, so soft-delete is viable if a human prefers one-table simplicity. Choosing it rewrites t-1 and t-3 and adds a `loadGrants` filter contract (verification t-1).

**D-2. Name snapshot storage**
- **risk:** a `grant_service_names` table.
- **mvp and verification:** a `grant_services.name` column.
- **Default:** table, coupled to D-1.
- **Why it matters:** under DELETE-on-trim, a column would lose the trimmed service's name. If D-1 flips to soft-delete, the column becomes the simpler choice.

**D-3. Stored end_reason vocabulary**
- **risk:** `'parent_end'`, mapped to wire `ended_early`.
- **mvp and verification:** store `'ended_early'` directly.
- **Default:** `'ended_early'` (majority, which removes a translation layer).
- **Why it matters:** cosmetic for behaviour, but it fixes the CHECK set. Risk t-1's "INSERT 'ended_early' is rejected" contract was rewritten to use 'bogus'.

**D-4. Wire shape and path**
- **risk:** one collapsed `status` (ongoing / expired / ended_early / permanent_block / ended_unknown) and `next_before`.
- **verification:** separate `status` (active / expired / ended) + `end_reason` (… / unknown), a top-level `child {id, name}` and `next`.
- **mvp:** a `how_ended` field at `/children/{id}/grants/history`.
- **Default:** risk's collapsed status, path and cursor name, plus verification's `child` object so the screen can title itself without a second fetch.
- **Why it matters:** this is the contract t-2/t-5 build against in parallel with the Go track. The golden-key test in t-9 enforces whichever shape is chosen.

**D-5. Child-id reuse after delete**
- **risk:** filter `started_at >= child.created_at`, because `children.id` lacks AUTOINCREMENT (confirmed in 0002) and a new child can inherit a deleted max-id child's grants.
- **verification:** the store returns rows even when no children row exists, with the API 404 as the only guard.
- **mvp:** relies on the 404.
- **Default:** risk's filter. Verification's "rows for a missing child id are returned" store contract is dropped.
- **Why it matters:** without the filter, locked deleted_child_history is broken in the id-reuse case (a new child sees another child's past). The filter costs a join to `children` in the read.

**D-6. Page ordering key**
- **risk and mvp:** `id DESC`.
- **verification:** `started_at DESC, id DESC`, with an equal-started_at boundary seed.
- **Default:** id only. Ids are monotonic with `started_at = now` at create, so the keyset is a single column.
- **Why it matters:** if grants could ever be created with a back-dated start, the two orders diverge and verification's tiebreak contract becomes necessary.

**D-7. Invalid or missing `?child=`**
- **risk:** routeFor resolves to `home`.
- **verification:** stay on History and show "No such child" with a back button.
- **Default:** risk (route home).
- **Why it matters:** this is UX only. Verification's choice keeps one error surface (the same as a 404 child), while risk's avoids rendering a screen with no child.

**D-8. Where c-5 is proven**
- **risk:** end to end through `run()` in `main_test.go` (t-10), including AdGuard-down-at-startup.
- **verification:** an engine-level fake-clock restart with `Start` (its t-7).
- **mvp:** a reconcile restart test in its t-1.
- **Default:** risk's t-10, with verification's store reopen grafted into t-6. No separate engine-level restart test was added, because putting one in t-4 would push it to 5 files.
- **Why it matters:** t-10 is slower and sits at wave 5, but it is the only proof that the startup pass ordering (before net.Listen) records the reason. If wave depth matters more, verification's engine test could stand in for most of it.

**D-9. Engine changes: merged or separate**
- **risk:** the engine pass-through (CreateNamed, Extend's `at`, reason on End/expiry) is merged into store task t-4 (4 files, 2 layers).
- **verification:** a separate engine task (t-7, wave 3).
- **mvp:** folds store, engine and migration into one 8-file task.
- **Default:** merged (risk).
- **Why it matters:** splitting adds a wave and touches `internal/grants/grants.go` in two tasks. Merging puts t-4 at the granularity ceiling.

**D-10. Order of name snapshotting vs. the endpoint**
- **risk:** snapshot task t-7 runs before the endpoint.
- **verification:** snapshotting is last (wave 4), after the endpoint, so its rename/delete contract is observable through GET history.
- **mvp:** wave 2, beside the endpoint.
- **Default:** risk's order, with verification's end-to-end rename/delete contract moved into endpoint task t-9.
- **Why it matters:** this keeps verification's stronger contract without the extra trailing wave. The cost is that t-9 now strictly depends on t-7.

**D-11. Granularity**
- **mvp:** 4 tasks.
- **risk and verification:** 10 tasks.
- **Default:** 10.
- **Why it matters:** mvp's t-1 (8 files), t-3 (5 files, 2 layers) and t-4 (6 files) exceed the granularity guidance. Mvp also serialises the whole web track behind the backend, because no wire shape is pinned.

**D-12. How the history read is wired into the API**
- **risk and mvp:** add `ListGrantHistory` to the existing `ChildStore` interface (already `Children: st` in main.go:133).
- **verification:** a new `Deps.History` field wired in `cmd/adguard-reward/main.go`.
- **Default:** `ChildStore`, with no main.go change.
- **Why it matters:** a new Deps field can be forgotten in wiring and nil-deref at runtime. Verification guarded against that with a main_test contract, which the default makes unnecessary.
