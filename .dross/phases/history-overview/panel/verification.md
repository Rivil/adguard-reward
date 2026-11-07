# history-overview — verification lens

Each contract below was written first; each task is the smallest change that makes its contracts satisfiable.
Contracts name tests that fail on a specific wrong implementation, most of them traps found in the existing code
(listed under "Traps the contracts exist to catch").

Pinned wire (every contract below assumes it):

- `GET /api/v1/history?child=&service=&status=&from=&to=&before=` behind requireSession ->
  `{ child:{id,name}|null, grants:[{id, child:{id,name}, status, started_at, ends_at, ended_at, services, extensions}], next_before }`.
  Grant shape is the existing one plus `child`. Page `child` is set only under a child filter. Newest first by id DESC, page 50.
- `status` is one of the five wire statuses. `from` is inclusive and `to` exclusive, both RFC 3339 instants the client computes.
- 404: unknown, deleted or non-numeric child. 400: bad before/status/from/to, empty service, any repeated param. 401: no session.
- `GET /api/v1/history/filters` -> `{ children:[{id,name}], services:[{id,name}] }`, services sorted by name.
- SPA URL: `/history?child=N&service=ID&status=S&from=YYYY-MM-DD&to=YYYY-MM-DD`, fixed param order, local calendar days.
- Store: `ListHistory(ctx, HistoryFilter{ChildID, ServiceID, Status, From, To, Before}, limit)` and `ListHistoryServices(ctx)`.

---

Phase history-overview — 10 tasks across 4 waves

Wave 1

  t-1  Add cross-child history read with visibility
       files:    internal/store/history.go, internal/store/history_test.go, internal/store/setup_test.go
       covers:   c-1, c-2, c-3, c-9
       does:     ListHistory joins children on child_id AND g.id > history_after_grant_id; each HistoryGrant gains ChildID, ChildName.
                 HistoryFilter carries ChildID and Before here. ListGrantHistory becomes a thin wrapper (old handler still calls it);
                 mustHistory and setup_test move to ListHistory.
       contract: if child names or details are lost, TestListHistory_AllChildrenNewestFirst fails: interleaved Ada/Ben grants come back
                 id DESC, each with ChildID, ChildName, services, removals and extensions equal to the per-child read
       contract: if the cursor is per-child or the limit is off, TestListHistory_CrossChildPaging fails: 120 grants over 3 children walk
                 50/50/20 by Before, the union is every id exactly once, HasMore is false on the last page, and exactly 50 grants gives HasMore false
       contract: if a grant created between pages shifts the window, TestListHistory_StableCursorAcrossChildren fails: page 2 after a new
                 grant repeats and skips nothing
       contract: if deleted children are dropped in Go after LIMIT, TestListHistory_DeletedChildOmitted fails: 60 newest grants on a deleted
                 child (one still active) over 50 visible grants gives a full first page of the 50 visible, HasMore false
       contract: if the join ignores the watermark, TestListHistory_ReusedIDOmitted fails: delete the highest-id child, create one that gets
                 the same id (asserted), and the old grant is absent both unfiltered and with ChildID set while the new grant shows the new name
       contract: if the child filter loses its 404, TestListHistory_ChildFilter fails: unknown and deleted ChildID return ErrChildNotFound;
                 a child with no grants returns an empty page with ChildName set

  t-2  Backfill watermarks for pre-0009 reused ids
       files:    internal/store/migrations/0011_history_watermark_backfill.sql, internal/store/store_test.go
       covers:   c-3
       does:     Set history_after_grant_id to the highest grant id on that child id with started_at < children.created_at, only where it reads 0.
       contract: if a pre-0009 reuse stays exposed, TestMigrate0011_BackfillsReusedID fails: at 0010, child 1 (created_at T, watermark 0) with
                 grants started T-100 and T+100 reads the older grant's id as its watermark after upgrade (column read by SQL, so no dependency on t-1)
       contract: if ordinary children lose grants, TestMigrate0011_LeavesOrdinaryChildren fails: a grant with started_at == created_at keeps
                 watermark 0, and grants rows stay byte-identical
       contract: if the update overwrites a real watermark, TestMigrate0011_KeepsExistingWatermark fails: a child already at 7 stays 7
       contract: if ordering breaks, TestMigrate_Order fails: 0011 follows 0010

  t-3  Add pure filter parse, serialise, day-bounds helpers
       files:    web/src/lib/history.ts, web/src/lib/history.test.ts, web/vite.config.ts
       covers:   c-5, c-8
       does:     HistoryFilters type {service, status, from, to}; parseFilters(search), filterParams(f) in fixed order, hasFilters(f),
                 dayBounds(from, to). Pin TZ=America/New_York at the top of vite.config.ts.
       contract: if a day is computed in UTC or as 24 hours, the dayBounds test fails: 2026-03-08..2026-03-08 gives 2026-03-08T05:00:00.000Z
                 and 2026-03-09T04:00:00.000Z (23-hour day); 2026-11-01..2026-11-01 gives 2026-11-01T04:00:00.000Z and 2026-11-02T05:00:00.000Z (25-hour day)
       contract: if an open bound is invented, the dayBounds test fails: only from gives no to, only to gives no from, neither gives {}
       contract: if bad URL values reach the screen, the parseFilters test fails: status=bogus, from=2026-02-31, from=2026-13-01, to=yesterday
                 and service= all parse to null; 2026-02-31 never rolls over to March
       contract: if serialising is lossy or order-dependent, the round-trip test fails: all 16 subsets including service custom:5 survive
                 parse(serialise(f)), and params come out in one order whatever order they went in

Wave 2

  t-4  Add store filters and service options
       files:    internal/store/history.go, internal/store/history_test.go
       depends:  t-1
       covers:   c-4, c-5, c-6, c-7
       does:     ServiceID as EXISTS over grant_services and grant_service_removals, From/To on started_at, Status as a predicate per wire status,
                 all inside the paged query. ListHistoryServices: distinct ids across visible grants with their newest snapshot name.
       contract: if the removal log is skipped, TestListHistory_ServiceIncludesTrimmed fails: filtering on a service trimmed off a live two-service
                 grant returns it, and so does a grant trimmed to empty (no grant_services rows left, permanent_block)
       contract: if the filter narrows details, the same test fails: the matched grant still lists all its services, the trimmed one marked removed
       contract: if a JOIN replaces EXISTS, TestListHistory_ServiceNoDuplicates fails: a service seeded in both tables lists its grant once
       contract: if ids match loosely, TestListHistory_ServiceExactID fails: custom:1 does not return a custom:12 grant
       contract: if the range uses the wrong column or edge, TestListHistory_DateBounds fails: started_at == From included, To-1s included,
                 To excluded, and a grant that started before From but ended inside the range is excluded
       contract: if a status predicate is wrong, TestListHistory_StatusMatrix fails: each of the 12 cells (3 statuses x end_reason NULL, expired,
                 ended_early, permanent_block) appears under exactly one of the five statuses, per an explicit table
       contract: if filters do not AND, TestListHistory_FiltersCombine fails: for all 32 subsets of child, service, from, to, status over a fixed
                 grid, the result equals a brute-force Go filter and a walk at limit 3 repeats nothing
       contract: if HasMore counts before filtering, TestListHistory_FilteredPaging fails: 50 matches among non-matches over one older non-match
                 gives HasMore false; 51 matches gives HasMore true with the 50th id as cursor
       contract: if a filter reopens hidden rows, TestListHistory_FiltersKeepVisibility fails: a service filter never returns a deleted or
                 reused-id child's grant
       contract: if options are wrong, TestListHistoryServices fails: a deleted custom service stays listed with its snapshot name; a trimmed-only
                 service is listed; one id with two snapshots appears once with the newest grant's name; no snapshot gives Name ""; a service
                 only on hidden grants is not listed

  t-5  Carry filters in URL and client
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/App.svelte, web/src/App.test.ts
       depends:  t-3
       covers:   c-7, c-8, c-9
       does:     historyChild stays as is; a sibling historyFilters store adds the four filters and badChild (?child present but not a positive whole
                 number). navigate and popstate set both. App refresh() navigates only login -> home. New fetchHistoryPage(child, filters, before)
                 and fetchHistoryFilters(); old fetchHistory stays until t-9.
       contract: if the two states collapse, the garbage-list test in api.test.ts fails: /history has badChild false; every other garbage URL
                 (?child=, abc, 0, -1, 3.5, 1e2, %203) has historyChild null and badChild true
       contract: if refresh() still rebuilds the URL, the App reload test fails: booting at /history?status=expired&service=custom:5&child=3 keeps
                 the URL byte-for-byte with zero pushState calls, and /history?child=abc is not rewritten to /history
       contract: if PIN unlock loses filters, the App locked test fails: 401 pin_required then unlock leaves the query and historyFilters intact
       contract: if popstate reads only child, the popstate test fails: going from /history?status=expired to /history?status=ongoing updates historyFilters
       contract: if a query leaks or repeats, the navigate test fails: navigate('history', {child: 3, filters}) pushes the canonical URL once, the same
                 call again pushes nothing and does not notify subscribers, and navigate('home') pushes / with filters cleared
       contract: if the client builds a different URL from the server's, the fetchHistoryPage test fails on the literal
                 /api/v1/history?child=3&service=custom%3A5&status=expired&from=2026-03-08T05%3A00%3A00.000Z&to=2026-03-09T04%3A00%3A00.000Z&before=40;
                 no filters requests /api/v1/history with no ?; null lists become []; a 404 rejects with ApiError 404

  t-6  Build history filter bar component
       files:    web/src/lib/HistoryFilterBar.svelte, web/src/lib/HistoryFilterBar.test.ts
       depends:  t-3
       covers:   c-4, c-5, c-6, c-9
       does:     Props child, filters, children, services, onChange. Selects for child, service and how it ended; two date inputs.
       contract: if an end status is missing or mislabelled, the status test fails: All plus the five statusLabel options, and picking
                 'Ended (reason unknown)' emits ended_unknown with other filters unchanged
       contract: if a control reads All while a filter is active, the unknown-service test fails: filters.service custom:9 absent from services
                 renders as a selected option showing its bare id, including when services is []
       contract: if labels are used as values, the service test fails: two ids sharing a name are separate options emitting their own ids
       contract: if a control allows several values, the single-value test fails: no select has multiple (locked filter_values)
       contract: if clearing leaks an empty string, the date test fails: clearing from emits null, and setting to emits YYYY-MM-DD unchanged
       contract: if the child control resets the rest, the child test fails: choosing All emits child null with filters unchanged

Wave 3

  t-7  Serve history overview and filter options
       files:    internal/api/history.go, internal/api/history_test.go, internal/api/api.go, cmd/adguard-reward/main_test.go
       depends:  t-4
       covers:   c-1, c-2, c-3, c-4, c-5, c-6, c-7, c-9
       does:     GET /api/v1/history and /api/v1/history/filters; shared page renderer; ListHistory and ListHistoryServices on ChildStore. historyPath and
                 the main_test getHistory move to the new route. Old route still registered (the screen uses it until t-8).
       contract: if grants lose their child, TestHistory_GoldenKeys fails: grant keys gain child with keys id, name; an unfiltered page body has "child":null and "grants":[]
       contract: if the SQL predicate and endStatus drift, TestHistory_StatusFilterMatchesEndStatus fails: over all 12 (status, end_reason) cells, each grant
                 appears under exactly one ?status= and its returned status equals that value
       contract: if parsing is lenient, TestHistory_QueryErrors fails: status=bogus, from=notadate, to=2026-01-01, before=0 and service= are 400; each of
                 child, service, status, from, to repeated is 400; child=abc, 0, 999 and a deleted child are 404; both routes are 401 with no cookie
       contract: if the cursor drops filters, TestHistory_FilteredPaging fails: walking next_before under status and service stays inside the matches and repeats nothing
       contract: if the client's encoding is rejected, the main_test e2e fails: the real server answers the literal URL from t-5 (%3A, .000Z) with 200 and the expected grant
       contract: if option names skip a step, TestHistoryFilters_Names fails: snapshot, then live catalogue, then bare id; catalogue down is 200 with ids;
                 the catalogue is not read when every service has a snapshot
       contract: if options leak, TestHistoryFilters_Lists fails: a deleted child is not in children; services sort by name; lists are [] never null

  t-8  Turn History screen into filtered overview
       files:    web/src/lib/History.svelte, web/src/lib/History.panel.test.ts, web/src/App.svelte, web/src/App.test.ts
       depends:  t-5, t-6
       covers:   c-1, c-2, c-7, c-8, c-9
       does:     Hosts the filter bar, loads options once, shows each grant's child, treats no child as the overview, reloads when child or filters change,
                 and pushes filter changes through navigate.
       contract: if the overview still needs a child, the panel test fails: child null with badChild false fetches /api/v1/history, heading is plain History,
                 and every row shows its child name beside the existing status, times, services and extensions
       contract: if 'No such child' is lost, the panel tests fail: badChild shows it with Back, no fetch and no filter bar; a 404 for ?child=99 shows the same
       contract: if a child link stops selecting, the panel test fails: child 3 fetches ?child=3, heading is 'Cleo — history', and the child select shows Cleo
       contract: if load more drops filters, the panel test fails: with status expired, Load more requests /api/v1/history?status=expired&before=40 and appends deduped
       contract: if changing a filter keeps the cursor, the panel test fails: the next request has no before and the old rows and Load more are gone
       contract: if a late response lands, the panel tests fail: first-page and further-page responses, and their errors, for an earlier filter are dropped
                 and do not re-enable Load more (mirrors 'after switching child')
       contract: if an empty filter strands the parent, the panel test fails: a filtered empty page shows 'No grants match these filters' with the bar still there;
                 an unfiltered one shows 'No grants yet'
       contract: if options gate the list, the panel test fails: /history/filters failing still renders grants and a usable bar; options are fetched once per mount, not per filter change
       contract: if reload doubles the fetch, the App test fails: booting at /history?child=3&status=expired calls exactly /api/v1/me, one history page and one options
                 request, and the status select shows Expired
       contract: if back does not restore, the App test fails: change status, popstate returns the previous filter with list and control reverted; Back to Home then popstate
                 restores the filtered list

Wave 4

  t-9  Link overview from Home, drop fetchHistory
       files:    web/src/lib/Home.svelte, web/src/lib/Home.test.ts, web/src/lib/api.ts, web/src/lib/api.test.ts
       depends:  t-8
       covers:   c-1, c-9
       does:     'History' button in Home's nav row navigating to bare /history. Remove fetchHistory and its tests.
       contract: if the link carries a child or sits inside the grid, the Home test fails: the button pushes /history with historyChild null and empty filters,
                 and shows with zero children and when the grid fails to load
       contract: if per-child links change, the existing test 'each child has a History link to its own history' fails: still /history?child=2
       contract: if a caller of fetchHistory survives, make typecheck fails on the missing export

  t-10 Retire per-child history route
       files:    internal/api/api.go, internal/api/history.go, internal/api/history_test.go, internal/store/history.go
       depends:  t-7, t-8
       covers:   c-1
       does:     Unregister GET /api/v1/children/{id}/history, delete its handler and ListGrantHistory, leaving one history read path.
       contract: if the route survives, TestHistory_PerChildRouteGone fails: GET /api/v1/children/{id}/history is 404 with a session and 401 without
       contract: if ?child=N behaviour regressed in the swap, TestHistory_ChildFilter fails: /api/v1/history?child=N returns that child's grants with page child set

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 overview, naming child, Home link | t-1, t-7, t-8, t-9, t-10 |
| c-2 newest 50, load more | t-1, t-7, t-8 |
| c-3 deleted and reused ids | t-1, t-2, t-7 (t-4 keeps it under filters) |
| c-4 service filter incl. trimmed | t-4, t-6, t-7 |
| c-5 date range | t-3, t-4, t-6, t-7 |
| c-6 how it ended | t-4, t-6, t-7 |
| c-7 combine, load more in filter | t-4, t-5, t-7, t-8 |
| c-8 reload and back | t-3, t-5, t-8 |
| c-9 child filter, No such child | t-1, t-5, t-6, t-7, t-8, t-9 |

## Traps the contracts exist to catch

- `App.svelte` refresh() calls `navigate($route, { child: $historyChild })` on every boot, so filters and `?child=abc` are wiped on reload. Fixing only `navigate` leaves c-8 broken (t-5).
- `historyChild` is null for both missing and invalid, and History shows 'No such child' for null. Missing must now mean overview (t-5, t-8).
- `endStatus` maps ended + end_reason 'expired' to expired and ignores end_reason on active and expired rows. A naive `status = ?` predicate disagrees (t-4, t-7).
- A trimmed-to-empty grant has no grant_services rows, so a service filter on that table alone misses it (t-4).
- Stryker's vitest runner forces `pool: 'threads'`, where a per-test `process.env.TZ` does not take effect. The zone is pinned at the top of vite.config.ts so the DST contracts hold under `dross verify` too (t-3).
- Moving a store from a number to an object means every `set` notifies, which would double-fetch on boot (t-5 keeps subscribers quiet; t-8 counts fetches).

## Judgment calls

- Retire the per-child API route (t-10) rather than keep it: a route with no client needs its own tests or goes stale. Cost: a tab left open across a deploy shows 'No such child' until reload.
- Sequence that retirement last: removing it in t-7 would break the app between commits, since the screen moves in t-8.
- Separate `/history/filters` endpoint rather than options inside each page: keeps the page shape stable, and a failed options read cannot take the list down.
- Options list covers all visible grants, not rows on hidden children and not the current filter: an option that can only return nothing is misleading, and a shrinking list makes filters hard to undo.
- URL holds local calendar days, API takes instants: the server has no device zone (locked date_range), and a date in the URL reads the same after reload.
- pushState per filter change rather than replaceState: back steps through filters, and it reuses the existing navigate and popstate path.
- Add migration 0011 rather than accept the prior phase's "predating children read 0": c-3 says never, and that gap fails a test for a child that reused an id before 0009. The judge can drop t-2 alone; nothing depends on it.
- Keep `historyChild` and add a sibling store rather than one object store: Home.test and most of App.test stay untouched as a regression net. Cost: two stores set together.
- Bad filter values in the URL are ignored by the SPA but rejected 400 by the API: a mangled link should still open, and the SPA never sends one.
- from after to returns an empty list rather than a validation error: it falls out of the query and the empty state already explains it.
- Newest first stays id DESC rather than started_at: it matches the cursor, which is what makes no-skip and no-repeat provable.
- No new index: EXISTS uses the (grant_id, service_id) primary keys and row counts are thousands.
- Status predicate lives in the store and parity is tested at the API rather than moving endStatus: no code move, and the test sees both sides.
- Filter bar is its own component in wave 2 rather than inline in History.svelte: its contracts need no backend and History.svelte is already the busiest file.
- Rows name their child even under a child filter: one render path, and c-1's "each naming its child" needs no exception.
- Accepted gap: a service trimmed before 0009 left no removal row, so c-4 cannot find those grants. Nothing recorded it.
