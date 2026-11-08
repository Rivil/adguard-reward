# grant-history: risk-lens draft

Lens: failure modes drive the graph. Each task owns a named set of risks and
proves them with its own tests. Where two tasks touch the same risk, one owns it
and the other only consumes it.

## Pinned contracts (shared by tasks that run in parallel)

Schema (migration 0009, owned by t-1):
- `grants.end_reason TEXT NULL CHECK (end_reason IN ('expired','parent_end','permanent_block'))`. There is no backfill: legacy rows stay NULL.
- `grant_service_names (grant_id → grants ON DELETE CASCADE, service_id, name NOT NULL, PK(grant_id, service_id))`. Written once at create and never touched by a trim.
- `grant_service_removals (grant_id → grants ON DELETE CASCADE, service_id, removed_at INTEGER NOT NULL, reason TEXT NOT NULL CHECK (reason IN ('permanent_block')), PK(grant_id, service_id))`.
- `grant_extensions (id INTEGER PRIMARY KEY, grant_id → grants ON DELETE CASCADE, extended_at, old_ends_at, new_ends_at)`.
- `CREATE INDEX grants_child_history_idx ON grants (child_id, id)`.
- `grant_services` keeps its DELETE-on-trim semantics. Removed services live only in the side table, so `loadGrants` and every live consumer (revert, region sync, drift repair, overlap index, custom-service delete guard, trim's "services left" count) stay unchanged.

Wire shape (owned by t-9 through a golden-key test; t-2 and t-5 build against it):
```
GET /api/v1/children/{id}/history[?before=<grant id>]
200 { "grants": [ {
        "id": 12, "status": "ongoing"|"expired"|"ended_early"|"permanent_block"|"ended_unknown",
        "started_at": RFC3339, "ends_at": RFC3339 (scheduled), "ended_at": RFC3339|null,
        "services":   [ {"id","name","removed_at": RFC3339|null,"removed_reason": "permanent_block"|null} ],
        "extensions": [ {"at","old_ends_at","new_ends_at"} ]          // chronological
      } ],
      "next_before": 12 | null }
```
Grants are listed newest first by id, with a fixed page of 50 and a keyset cursor (`before`).

---

```
Phase grant-history — 10 tasks across 5 waves

Wave 1
  t-1  Add grant-history schema migration 0009
       files:    internal/store/migrations/0009_grant_history.sql, internal/store/store_test.go
       covers:   c-6, c-1, c-2, c-3
       desc:     Add end_reason column, the names/removals/extensions tables and the history index, exactly as pinned. No UPDATE of existing rows.
       contract: - upgrade from 0008 seeded with one expired, one ended and one active legacy grant leaves status, started_at, ends_at, ended_at, grant_services and grant_clients unchanged, and end_reason is NULL on all three (a backfill that writes 'parent_end' to legacy 'ended' rows fails this)
                 - INSERT with end_reason='ended_early' (not in the CHECK set) is rejected; removal reason other than 'permanent_block' is rejected
                 - schema_migrations lists 0009_grant_history.sql directly after 0008_service_toggles.sql
       depends:  —

  t-2  Add history route with child query param
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts
       covers:   c-4
       desc:     Add Route 'history' (/history), a historyChild store parsed from ?child=, navigate(r, params), HistoryGrant types and fetchHistory(childId, before?) per the pinned wire shape.
       contract: - navigate('history', {child: 3}) pushes '/history?child=3' and historyChild === 3; navigate('home') afterwards pushes '/' with no leftover query
                 - routeFor on /history with ?child= missing, 'abc', '0', '-1' or '3.5' resolves to 'home' (never a history route with NaN/0 child)
                 - popstate from /history?child=3 to /history?child=5 updates historyChild to 5 (a stale 3 fails)
                 - fetchHistory(3) requests GET /api/v1/children/3/history with no query; fetchHistory(3, 40) appends ?before=40; a 404 rejects with ApiError status 404
       depends:  —

Wave 2
  t-3  Record permanent-block trims in removal log
       files:    internal/store/toggles.go, internal/store/toggles_test.go
       covers:   c-3, c-1
       desc:     trimGrant inserts a grant_service_removals row in the same tx as the DELETE, and an emptying trim sets end_reason='permanent_block' alongside status/ended_at.
       contract: - blocking one of a grant's two services writes removal (service, at, 'permanent_block'); the grant stays active with end_reason NULL; ListActiveGrants().Services no longer contains it (a "mark removed in grant_services" implementation leaks it back into revert/drift repair and fails this)
                 - blocking the last service writes the removal row and status 'ended', end_reason 'permanent_block', ended_at = at (the trimmed service is not lost from history)
                 - unblock toggle, or block with no live grant covering the service, writes no removal row
                 - after a trim-ended grant, SetGrantStatus(active→expired) returns false and end_reason stays 'permanent_block' (timer-vs-toggle race cannot overwrite the reason)
                 - pre-seeding a conflicting removal row makes SetServiceToggle fail and roll back: no service_toggles row and grant_services still holds the service (all-or-nothing)
       depends:  t-1

  t-4  Record end reasons, extensions, name snapshots
       files:    internal/store/grants.go, internal/store/grants_test.go, internal/grants/grants.go, internal/grants/grants_test.go
       covers:   c-1, c-2, c-4
       desc:     SetGrantStatus writes end_reason from `to` (expired→'expired', ended→'parent_end'). ExtendGrant(ctx,id,newEndsAt,at) reads old ends_at inside its tx and inserts an extension row. CreateGrantNamed(…, names) writes grant_service_names (CreateGrant delegates with nil). Engine gets CreateNamed (Create delegates with nil), Extend passes e.now(), and the Store interface switches to CreateGrantNamed.
       contract: - ExtendGrant twice yields two grant_extensions rows with second.old_ends_at == first.new_ends_at (old read from the row in-tx, not from the caller); ExtendGrant on an expired grant is ErrGrantNotFound and inserts no row
                 - SetGrantStatus active→ended stores 'parent_end' and active→expired stores 'expired'
                 - CreateGrantNamed with an overlap error leaves no grant and no grant_service_names rows; a names key not in services writes no orphan row; CreateGrant (nil names) writes none
                 - engine End where AdGuard's add fails returns *ErrRevertFailed and the row stays active with end_reason NULL (no "ended" is claimed before the block is restored)
                 - engine timer expiry after an Extend re-arm records 'expired' with ended_at = fake clock at revert, not the original ends_at; Extend records extended_at = fake clock
       depends:  t-1

  t-5  Build History screen with paging
       files:    web/src/lib/History.svelte, web/src/lib/History.test.ts, web/src/lib/history.ts, web/src/lib/history.test.ts
       covers:   c-4, c-1, c-2, c-3, c-6
       desc:     Per-child screen with a back button: newest-first grants, each with services (removed ones marked), start, end, end-status label and extensions; a load-more control drives fetchHistory(child, next_before).
       contract: - statusLabel('ended_unknown') renders "Ended (reason unknown)" and never "Ended early"; 'ongoing' renders "Ongoing" with no end time even when ends_at is in the past (an ends_at-vs-now check fails this)
                 - a removed service renders with a "removed <time> · permanent block" marker; a grant with services [] (legacy trim) renders "—" without throwing
                 - two rapid load-more clicks issue exactly one fetch; the appended page is deduped by id; load-more is hidden when next_before is null
                 - a load-more 500 keeps the rows already shown, shows an error, and retry reuses the same before cursor
                 - an initial 404 shows "child not found" with the back button and no list; an initial 500 shows retry, not the "No grants yet" empty state
                 - a twice-extended grant renders two extension entries in chronological order, each old → new
       depends:  t-2

Wave 3
  t-6  Add paged per-child history store read
       files:    internal/store/history.go, internal/store/history_test.go
       covers:   c-4, c-3, c-2, c-6
       desc:     ListGrantHistory(ctx, childID, before, limit) reads grants, live and removed services with snapshot names, and extensions inside one inTx. It reads limit+1 rows to compute has-more and filters out grants started before the child's created_at.
       contract: - 51 grants → first page 50 newest-first with has-more; exactly 50 → has-more false (no empty load-more page)
                 - a grant created between page-1 and page-2 reads appears in neither page 2 nor as a duplicate (keyset, not offset)
                 - deleting the highest-id child and creating a new one (SQLite reuses the id) gives the new child an empty history (the created_at filter guards locked deleted_child_history)
                 - a grant with one trimmed and one live service lists both, the trimmed one with removed_at/reason; a grant ended by its last trim still lists that service
                 - a legacy grant seeded at 0008 and upgraded returns its services with empty snapshot name, its times, status 'ended' and NULL reason; another child's grants never appear
       depends:  t-3, t-4

  t-7  Snapshot catalogue names on grant create
       files:    internal/api/grants.go, internal/api/grants_test.go, internal/api/api.go
       covers:   c-4
       desc:     handleGrantCreate builds names from the catalogue it already read (keyed by trimmed id) and calls Grants.CreateNamed. The Grants interface gains CreateNamed.
       contract: - POST /grants with a built-in and a custom:<n> service stores grant_service_names equal to the catalogue names (built-in display name, custom service name), with no extra AdGuard request
                 - a service id sent with surrounding whitespace still gets its snapshot (lookup keyed by the trimmed id; a raw-id lookup leaves it unnamed)
                 - a 409 overlap or 422 unknown-service create writes no grant_service_names rows
       depends:  t-4

  t-8  Mount history route and Home links
       files:    web/src/App.svelte, web/src/App.test.ts, web/src/lib/Home.svelte, web/src/lib/Home.test.ts
       covers:   c-4
       desc:     App renders History for route 'history' with the parsed child; Home gets a "History" link per listed child.
       contract: - reloading at /history?child=3 keeps the URL /history?child=3 after refresh() and renders History for child 3 (refresh's navigate($route) stripping the query fails this)
                 - with two children, each History link navigates to its own child id (a closure bug pointing both at the last child fails)
                 - History's back button returns to '/' and Home
       depends:  t-5

Wave 4
  t-9  Serve GET child history endpoint
       files:    internal/api/history.go, internal/api/history_test.go, internal/api/api.go
       covers:   c-1, c-2, c-3, c-4, c-6
       desc:     Route GET /api/v1/children/{id}/history behind requireSession. ChildStore gains ListGrantHistory (already wired via `Children: st`). endStatus maps (status, reason) to wire status. Names resolve from the snapshot, then the live catalogue, then the bare id; the catalogue is read only when a snapshot is missing.
       contract: - endStatus table: (ended, NULL) → "ended_unknown", never "ended_early"; (expired, NULL) and (expired,'expired') → "expired"; (ended,'parent_end') → "ended_early"; (ended,'permanent_block') → "permanent_block"; (active, *) → "ongoing"
                 - renaming a custom service after the grant keeps the snapshot name; a legacy grant naming a deleted custom:7 shows name "custom:7"
                 - AdGuard Services() returning 500 still yields 200: snapshot names intact and bare ids for unsnapshotted; with all names snapshotted no catalogue request is made
                 - non-numeric id → 404; unknown or deleted child → 404; ?before=abc, -1 or 0 → 400; no session → 401
                 - golden-key test: the response JSON has exactly the pinned field names (a rename breaks the SPA and fails here), and lists are [] never null
       depends:  t-6, t-7

Wave 5
  t-10 Prove history across restart and downtime
       files:    cmd/adguard-reward/main_test.go
       covers:   c-5, c-1, c-2
       desc:     End-to-end runs through run(): restart survival, overdue-during-downtime expiry, and AdGuard down at startup.
       contract: - a grant ended early after two extensions still shows ended_early with two extensions via GET history after a restart
                 - a grant whose ends_at passes while the app is stopped reads "expired" after restart, with ended_at ≥ restart time (the startup pass, not ends_at)
                 - with AdGuard unreachable at startup the overdue grant reads "ongoing", not "expired", until a later pass reverts it
       depends:  t-9
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 end reason + actual end time | t-3 (permanent_block), t-4 (expired / parent_end, actual ended_at), t-9 (status mapping), t-5 (label), t-10 (end-to-end) |
| c-2 every extension recorded | t-1 (table), t-4 (write, chained old/new), t-6 (read), t-9 (wire), t-5 (render), t-10 (survives restart) |
| c-3 trimmed service stays, marked removed | t-1 (table), t-3 (write, atomic), t-6 (read alongside live), t-9 (wire), t-5 (marker) |
| c-4 per-child history view | t-2 (route/fetch), t-4 + t-7 (name snapshot), t-6 (order/paging), t-9 (endpoint), t-5 (screen), t-8 (reachable from Home) |
| c-5 survives restart; downtime overdue → expired | t-10 (owner); t-4 supplies the reason write it relies on |
| c-6 pre-upgrade grants still shown | t-1 (no backfill, rows intact), t-6 (legacy rows read), t-9 (ended_unknown + name fallback), t-5 (legacy label, empty services) |

All 6 of 6 criteria are covered.

## Judgment calls

- Removed services go in a side table (`grant_service_removals`) instead of soft-deleting rows in `grant_services` with `removed_at`. A soft delete would leak trimmed services into loadGrants and its live consumers: revert, region sync, drift repair, the custom-service delete guard and trim's left-count. That would silently undo permanent blocks.
- Name snapshots get their own table rather than a column on `grant_services`. trimGrant DELETEs from `grant_services`, so a column there would lose the removed service's name.
- `end_reason` is a nullable column with no backfill, rather than new status values or a backfill of 'expired' rows. SQLite cannot alter the status CHECK without a table rebuild, and NULL is the honest "old row" marker that locked legacy_end_reason requires. The API maps (expired, NULL) → expired.
- SetGrantStatus derives the reason from `to` instead of taking an explicit reason parameter. End is the only ended caller and trimGrant writes its own reason, so this keeps the Store interface and its call sites stable. The cost: a future caller of SetGrantStatus(→ended) would be labelled parent_end.
- Additive `CreateGrantNamed`/`CreateNamed` with nil-delegating originals, rather than changing the `CreateGrant`/`Create` signatures. That avoids churn across about 30 test call sites, and the old path doubles as the "no snapshot" legacy path.
- ExtendGrant gains an `at` parameter instead of calling time.Now in the store. Fake-clock engine tests must be able to assert extension times.
- Keyset paging on grant id (`before`) instead of offset. Offset duplicates rows on "load more" when a grant is created between pages, and ids are never reused because grants are never deleted.
- History filters on `started_at >= child.created_at`. The alternative was rebuilding `children` with AUTOINCREMENT, a risky table rebuild with three FK dependents. Without either, SQLite reuses a deleted max child id and a new child would inherit the deleted child's history, which breaks locked deleted_child_history.
- ListGrantHistory goes on the existing ChildStore interface instead of a new Deps field. `Children: st` is already wired in main.go, so a forgotten wiring cannot cause a nil-deref, and fakes embed the interface so they still compile.
- Name fallback reads the catalogue lazily and treats failure softly. The alternative was always reading the catalogue and returning 502 on failure. History is a look-back screen and must not go dark when AdGuard is down.
- End time shown is `ended_at` (when the block was actually restored), not `ends_at`. c-1 says "actual end time", and for c-5 that is the startup revert.
- The UI tasks (t-2, t-5, t-8) start in waves 1–2 against the pinned wire shape rather than waiting for the endpoint. t-9's golden-key test owns the drift risk between them.
- t-9 is sequenced after t-7 even though it needs no data from it. Both edit interfaces in internal/api/api.go, and running them in the same wave risks conflicting edits.
- The engine half of the reason writes is merged into t-4 (store + engine, 4 files, 2 layers) instead of a separate engine task. A separate task would add a wave, and the engine change is a pass-through whose risks (failed revert claims no end, re-armed expiry time) are tested next to the write.
