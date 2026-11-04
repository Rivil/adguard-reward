# grant-history — MVP lens draft

```
Phase grant-history — 4 tasks across 3 waves

Wave 1
  t-1  Record end reason, extensions, trimmed services
       files:    internal/store/migrations/0009_grant_history.sql (new),
                 internal/store/grants.go, internal/store/toggles.go,
                 internal/store/custom_services.go, internal/grants/grants.go,
                 internal/store/grants_test.go, internal/store/toggles_test.go,
                 internal/grants/reconcile_test.go
       desc:     One migration: grants.end_reason (NULL for legacy), grant_services.name,
                 grant_services.removed_at/removed_reason, new grant_extensions(grant_id, at,
                 old_ends_at, new_ends_at). SetGrantStatus writes end_reason from `to`
                 (expired→'expired', ended→'ended_early'); trimGrant marks the row
                 active=0 + removed (reason 'permanent_block') instead of DELETE, counts only
                 unremoved rows, and ends with end_reason 'permanent_block'. ExtendGrant gains
                 an `at` arg and inserts a grant_extensions row in the same tx (Engine.Extend
                 passes e.now()). loadGrants and DeleteCustomService's in-use check ignore
                 removed rows so live behaviour is unchanged.
       covers:   c-1, c-2, c-3, c-5
       contract: - extending a grant twice leaves 2 grant_extensions rows (old→new chained:
                   e0→e1, e1→e2, each with its own `at`); dropping the insert fails
                   TestExtendGrant_RecordsEachExtension
                 - End → end_reason 'ended_early'; timer expiry → 'expired'; blocking the
                   last service → status 'ended', end_reason 'permanent_block', ended_at = toggle time
                 - blocking 1 of 2 services keeps its grant_services row with removed_at and
                   reason 'permanent_block', GetGrant.Services omits it, and a new grant for
                   that (child, service) is accepted (live index slot freed)
                 - DeleteCustomService on a custom service that was trimmed off a still-live
                   grant succeeds (no false *ErrServiceInUse)
                 - reopen the store with an overdue active grant, Engine.Start → row is
                   'expired' with end_reason 'expired' (reconcile restart test)
       depends:  —

Wave 2 (depends t-1)
  t-2  Snapshot service names at grant start
       files:    internal/store/grants.go, internal/grants/grants.go,
                 internal/api/grants.go, internal/api/api.go,
                 internal/store/grants_test.go, internal/api/grants_test.go
       desc:     CreateGrant and Engine.Create take a names map[service_id]name and write
                 grant_services.name; handleGrantCreate builds the map from the catalogue it
                 already reads and passes it through the Grants interface.
       covers:   c-4
       contract: - POST /api/v1/grants for a custom service, then rename that service →
                   grant_services.name still holds the old name (fails if the handler stops
                   passing catalogue names or the store ignores them)
                 - built-in id snapshot equals the AdGuard catalogue Name, not the id
       depends:  t-1

  t-3  Paged per-child history endpoint
       files:    internal/store/history.go (new), internal/store/history_test.go (new),
                 internal/api/history.go (new), internal/api/history_test.go (new),
                 internal/api/api.go
       desc:     store.ListGrantHistory(childID, beforeID, limit) returns grants id DESC with
                 every grant_services row (incl. removed), extensions and end fields.
                 GET /api/v1/children/{id}/grants/history?before=<id> returns 50 per page +
                 next_before; how_ended = ongoing | expired | ended_early | permanent_block |
                 ended_unknown (legacy 'ended' with NULL reason); name = snapshot, else
                 current catalogue name, else bare id. Method added to ChildStore.
       covers:   c-1, c-2, c-3, c-4, c-6
       contract: - 55 grants → page 1 has 50 newest-first with next_before = id of 50th;
                   ?before= that id returns 5 and next_before null
                 - DB built with migrationsThrough 0008 holding one 'expired' and one 'ended'
                   grant, reopened with all migrations → history shows them as expired and
                   ended_unknown (never ended_early/permanent_block), with services, started_at
                   and ended_at
                 - NULL-name legacy row shows the current catalogue name; a deleted custom
                   id shows the bare "custom:<n>"; a snapshot name wins over a renamed service
                 - a twice-extended grant returns 2 extensions; a trimmed service is listed
                   with removed_at and reason permanent_block; an active grant is "ongoing"
                 - another child's grants never appear; unknown child id → 404
       depends:  t-1

Wave 3 (depends t-3)
  t-4  History screen with per-child link
       files:    web/src/lib/History.svelte (new), web/src/lib/History.test.ts (new),
                 web/src/lib/api.ts, web/src/App.svelte, web/src/lib/Home.svelte,
                 web/src/lib/Home.test.ts
       desc:     Add 'history' route (/history?child=N, child read from location.search) and
                 listGrantHistory(); History.svelte renders the list with back button and a
                 "Load more" control; Home shows a History link under each child heading.
       covers:   c-4
       contract: - clicking a child's History link on Home pushes /history?child=<id> and
                   renders History for that child; Back returns to /
                 - rows render newest first with service names, start, end, an "Ongoing"
                   label for a live grant, "Ended early" / "Expired" / "Ended by permanent
                   block" / "Ended — reason unknown", each extension, and a removed-service marker
                 - "Load more" calls the API with before=next_before and appends; it is hidden
                   when next_before is null
                 - a reload on /history?child=3 stays on the history route (routeFor maps it)
       depends:  t-3
```

## Coverage

| criterion | tasks |
|---|---|
| c-1 end reason + actual end time | t-1 (write), t-3 (read) |
| c-2 every extension recorded | t-1 (write), t-3 (read) |
| c-3 trimmed service kept, marked removed | t-1 (write), t-3 (read) |
| c-4 per-child history view | t-2 (names), t-3 (API), t-4 (UI) |
| c-5 survives restart; startup pass records expired | t-1 |
| c-6 legacy grants appear | t-3 |

All 6/6 criteria covered. Locked decisions: history_location → t-4; retention (never pruned, 50 + load more) → t-3/t-4; legacy_end_reason → t-1 (legacy end_reason stays NULL, no backfill) + t-3 (ended_unknown); service_names → t-2 (snapshot) + t-3 (fallback); deleted_child_history → t-3 (404 on unknown child, no FK added).

## Judgment calls

- end_reason derived from SetGrantStatus's `to` (ended→ended_early) instead of a new reason parameter: End is the only caller ending via SetGrantStatus and the trim path has its own SQL, so no engine signature churn.
- New end_reason column, not new status values: the CHECK on grants.status would need a table rebuild in SQLite; a nullable column also leaves legacy rows naturally "unknown" without backfill.
- Trim marks the row removed (active=0 + removed_at) instead of moving it to a side table: one table stays the source of a grant's services; the cost is filtering removed rows in loadGrants and DeleteCustomService, which t-1 owns.
- ended_at stays "when the block was actually restored" (restart time for c-5's overdue grants), not ends_at: c-1 asks for the actual end time; how_ended=expired already says it ran its course.
- Names snapshot is a separate task (t-2) from the recording task: folding it into t-1 makes a 7-file, 3-layer task; it is unavoidably a thin store→engine→api param thread, so it stays its own small vertical slice.
- Name passed in from the API's existing catalogue read, not resolved inside the engine: avoids adding Services to the engine's AdGuard interface and a second catalogue call under the engine mutex.
- Cursor paging by grant id (before=), not offset: ids are monotonic with start time, and a new grant arriving mid-browse cannot shift page 2.
- History read added to the existing ChildStore interface, not a new Deps field: *store.Store already fills it, so main.go wiring and test fakes (which embed ChildStore) need no change.
- Unknown/deleted child → 404 from the history endpoint: matches "no longer reachable", no extra code path to keep orphan history visible.
- No dedicated "c-6 migration" task: the legacy guarantee is that 0009 is additive (ALTER/CREATE only); t-3's migrationsThrough-0008 test proves it end to end.
- Legacy-name fallback reads the catalogue only when a page holds an un-snapshotted row; on catalogue failure it degrades to bare ids rather than failing the page (implementation detail inside t-3, no task of its own).
