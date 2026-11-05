# history-overview — panel synthesis

## Scores

Scale 1-5. Repo facts cited below were checked against the working tree, not taken from the drafts.

| Draft | Dimension | Score | Why |
|---|---|---|---|
| risk | Criteria coverage | 5 | All nine covered, and t-2 closes the pre-0009 reused-id hole that c-3's "never" leaves open. |
| risk | Test-contract specificity | 4 | The sharpest edge cases of the three, but its multi-zone tests cannot run under Stryker: per-test `process.env.TZ` is ignored in a worker thread. |
| risk | Granularity | 5 | Every task is 2-4 files in one layer; store options and their endpoint are separate tasks on their own files. |
| risk | Wave correctness | 5 | Every edge is a real dependency; t-10 waits on t-8 for the shared name resolver. |
| mvp | Criteria coverage | 4 | All nine mapped, but the pre-0009 reused-id hole in c-3 is neither fixed nor mentioned. |
| mvp | Test-contract specificity | 3 | Specific and well aimed, but thin on traps, and the Stockholm TZ stub has the same Stryker failure as risk. |
| mvp | Granularity | 2 | t-4 is 6 files (the draft admits it); t-1, t-2 and t-4 each cover 8 of 9 criteria, so a failure localises to almost nothing. |
| mvp | Wave correctness | 5 | Two waves, and every edge is strict. |
| verification | Criteria coverage | 5 | All nine covered, including the backfill; t-10's c-1 claim is a stretch, since retiring a route serves no criterion. |
| verification | Test-contract specificity | 5 | Each contract targets a named wrong implementation, and it alone found the Stryker thread-pool trap. One contract names the wrong test (`TestMigrate_Order`, corrected below). |
| verification | Granularity | 4 | Mostly right, but t-4 folds store options into an already heavy filter task, and t-10 traces to no criterion. |
| verification | Wave correctness | 5 | Every edge is strict; retirement is correctly sequenced last. |

**Skeleton: verification.** Its contracts are the only ones that hold under `dross verify`, and it reuses the existing `web/src/lib/history.ts` instead of adding a module. Risk is structurally near-identical and supplies most of the grafts.

## Merged plan

Phase history-overview — 11 tasks across 4 waves

Pinned wire (every contract below assumes it; see Disagreements for the contested parts):

- `GET /api/v1/history?child=&service=&status=&from=&until=&before=` behind requireSession returns `{ child:{id,name}|null, grants:[<per-child grant shape> + child:{id,name}], next_before:id|null }`. Page `child` is set only under a child filter. Newest first by id DESC, page 50, lists are [] never null.
- `status` is one of ongoing, expired, ended_early, permanent_block, ended_unknown. `service` is an exact service id.
- `from` is inclusive and `until` exclusive, both RFC 3339 instants the client computes. The server knows no time zone.
- 404: unknown, deleted or non-numeric child. 400: bad before, status, from or until; empty service, status, from, until or before; any repeated param. 401: no session.
- `GET /api/v1/history/services` returns `{ services:[{id,name}] }`, sorted by name then id. The child filter's options come from the existing `GET /api/v1/children`.
- `GET /api/v1/children/{id}/history` stays, byte-identical.
- SPA URL: `/history?child=N&service=ID&status=S&from=YYYY-MM-DD&to=YYYY-MM-DD`, fixed param order, local calendar days, never instants.
- Store: `ListHistory(ctx, HistoryFilter{ChildID, ServiceID, Status, From, Until, Before}, limit)` and `ListHistoryServices(ctx)`.

```
Wave 1

  t-1  Add cross-child history read with visibility  [verification+risk+mvp]
       files:      internal/store/history.go, internal/store/history_test.go
       covers:     c-1, c-2, c-3, c-9
       depends_on: none
       desc:       ListHistory joins children in SQL on child_id AND g.id > history_after_grant_id, reads limit+1, reuses historyDetails. Each HistoryGrant gains ChildID and ChildName. HistoryFilter carries ChildID and Before here. Unknown or deleted ChildID is ErrChildNotFound. ListGrantHistory becomes a thin wrapper with unchanged behaviour.
       contract:   if child names or details are lost, TestListHistory_AllChildrenNewestFirst fails: interleaved Ada/Ben grants come back id DESC, each with ChildID, ChildName, services, removals and extensions equal to the per-child read
                   if the cursor is per-child or the limit is off, TestListHistory_CrossChildPaging fails: 120 grants over 3 children walk 50/50/20 by Before, the union is every id exactly once, HasMore is false on the last page, 51 grants give HasMore true, and exactly 50 give HasMore false
                   if the cursor must point at a visible grant or new grants shift the window, TestListHistory_StableCursorAcrossChildren fails: a grant created between pages appears neither in page 2 nor twice, and deleting the child that owns the cursor grant between pages still returns the next older grants with no skip
                   if deleted children are dropped in Go after LIMIT, TestListHistory_DeletedChildOmitted fails: 60 newest grants on a deleted child (one still active) over 50 visible grants give a full first page of the 50 visible with HasMore false; 120 grants alternating Ada/Ben with Ben deleted give 50 of Ada's with HasMore true, then 10 with HasMore false
                   if the join ignores the watermark, TestListHistory_ReusedIDOmitted fails: delete the highest-id child, create one that gets the same id (asserted), and the old grant is absent both unfiltered and with ChildID set, while the new grant shows the new name and no grant carries that name from before the watermark
                   if the child filter conflates unknown with empty, TestListHistory_ChildFilter fails: unknown and deleted ChildID return ErrChildNotFound; a child with no grants returns an empty page with ChildName set; ChildID 0 returns all; an empty install returns an empty non-nil list
                   if the shared read regresses the per-child path, the existing TestListGrantHistory_* suite and the internal/store/setup_test.go assertions fail unedited, including the DeepEqual in TestListGrantHistory_Legacy

  t-2  Backfill watermarks for pre-0009 reused ids  [risk+verification]
       files:      internal/store/migrations/0011_history_watermark_backfill.sql, internal/store/store_test.go
       covers:     c-3
       depends_on: none
       desc:       For children still at watermark 0, set history_after_grant_id to the highest id of that child_id's grants with started_at < children.created_at. Touches children only, never grants.
       contract:   if a pre-0009 reuse stays exposed, TestMigrate0011_BackfillsReusedID fails: at 0008, child 1 with created_at 5000 and grants 1, 2 started 1000, 2000 and grant 3 started 6000 upgrades to watermark 2 (column read by SQL), and ListGrantHistory lists only grant 3
                   if real legacy history is hidden, TestMigrate0011_LeavesOrdinaryChildren fails: the openAt0008 seed (created_at 100) keeps watermark 0 and lists grants 3, 2, 1; a grant with started_at == created_at stays visible
                   if the update overwrites a real watermark, TestMigrate0011_KeepsExistingWatermark fails: a child at 0010 with watermark 7 still reads 7
                   if the migration rewrites grants, TestMigrate0011_GrantsUntouched fails: every grants row is byte-identical before and after, and end_reason stays NULL (locked legacy_end_reason)
                   if ordering breaks, TestMigrate0011_Order fails: schema_migrations holds 0011 directly after 0010, the shape of TestMigrate0009_Order

  t-3  Add pure filter parse, serialise, day-bounds helpers  [verification+risk]
       files:      web/src/lib/history.ts, web/src/lib/history.test.ts, web/vite.config.ts
       covers:     c-5, c-8
       depends_on: none
       desc:       HistoryFilters type {service, status, from, to}; parseFilters(search), filterParams(f) in fixed order, hasFilters(f), dayBounds(from, to) giving local-midnight instants as UTC ISO, with until the midnight after `to`. Pin process.env.TZ = 'America/Havana' at the top of vite.config.ts so one zone holds in the main process and in Stryker's worker threads.
       contract:   if the pin is dropped or ignored, the zone control test fails: getTimezoneOffset reads 300 for 2026-01-15 and 240 for 2026-07-15, so a run that fell back to the host zone fails instead of passing by accident
                   if a day is computed in UTC or as 24 hours, the dayBounds test fails: 2026-03-08..2026-03-08 gives 2026-03-08T05:00:00.000Z and 2026-03-09T04:00:00.000Z (23-hour day); 2026-11-01..2026-11-01 gives 2026-11-01T04:00:00.000Z and 2026-11-02T05:00:00.000Z (25-hour day)
                   if a skipped or repeated midnight breaks, the same test fails: 2026-03-08 has no local midnight in this zone and 2026-11-01 has two, and the instants above are the required answers
                   if a day string parses as UTC, the zone test fails: from=2026-03-01 gives 2026-03-01T05:00:00.000Z, not 2026-03-01T00:00:00.000Z
                   if an open bound is invented, the dayBounds test fails: only from gives no until, only to gives no from, neither gives {}
                   if bad URL values reach the screen, the parseFilters test fails: status=bogus, from=2026-02-31, from=2026-13-01, to=yesterday and service= all parse to null; 2026-02-31 never rolls over to March
                   if extreme years leak through, the parseFilters test fails: 0099-01-01 (which new Date reads as 1999) and 9999-12-31 (whose until would serialise as +010000, unparseable by Go) drop to no date filter
                   if serialising is lossy or order-dependent, the round-trip test fails: all 16 subsets survive parse(serialise(f)), including services custom:5 and 'a&b=c +', and params come out in one order whatever order they went in

Wave 2 (t-4, t-5 depend t-1; t-6, t-7 depend t-3)

  t-4  Add service, date, status filter predicates  [verification+risk+mvp]
       files:      internal/store/history.go, internal/store/history_test.go
       covers:     c-4, c-5, c-6, c-7
       depends_on: t-1
       desc:       ServiceID as EXISTS over grant_services and grant_service_removals, From/Until as started_at >= From AND < Until, Status as a predicate per wire status mapping to (status, end_reason), all inside the keyset query so has-more counts filtered rows.
       contract:   if the removal log is skipped, TestListHistory_ServiceIncludesTrimmed fails: filtering on a service trimmed off a live two-service grant returns it, and so does a grant trimmed to empty (no grant_services rows left, permanent_block), while a grant without the service does not match
                   if the filter narrows details, the same test fails: the matched grant still lists all its services, the trimmed one marked removed
                   if the filter reads snapshot names, TestListHistory_ServiceFilter fails: a live service, a legacy grant with no name row and a deleted custom:N each match exactly once by id
                   if a JOIN replaces EXISTS, TestListHistory_ServiceNoDuplicates fails: a service seeded in both tables lists its grant once
                   if ids match loosely or unparameterised, TestListHistory_ServiceExactID fails: custom:1 does not return a custom:12 grant, and 'you', '%' and `' OR 1=1 --` match nothing against youtube
                   if the range uses the wrong column or edge, TestListHistory_DateBounds fails: started_at == From is in, From-1 is out, Until-1 is in, Until is out, either bound alone works, and a grant that started before From but ended or was extended into the range is out
                   if a status predicate is wrong, TestListHistory_StatusMatrix fails: each of the 12 cells (3 statuses x end_reason NULL, expired, ended_early, permanent_block) appears under exactly one of the five statuses per an explicit table; ('active',NULL) is only ongoing even when overdue, ('expired',NULL) and ('ended','expired') are only expired, and ('ended',NULL) is only ended_unknown
                   if filters do not AND or arguments misalign in the dynamic SQL, TestListHistory_FiltersCombine fails: for all 32 subsets of child, service, from, until, status over a fixed grid, the result equals a brute-force Go filter and a walk at limit 3 repeats nothing
                   if HasMore counts before filtering, TestListHistory_FilteredPaging fails: 120 grants with 60 matching give 50 + HasMore then 10, no overlap; 50 matches over one older non-match give HasMore false; 51 matches give HasMore true with the 50th id as cursor
                   if a row changing mid-paging breaks the cursor, TestListHistory_StatusChangesBetweenPages fails: under status=ongoing, a page-1 grant that expires before page 2 causes no skip or repeat among the rest
                   if a filter reopens hidden rows, TestListHistory_FiltersKeepVisibility fails: a service filter never returns a deleted or reused-id child's grant

  t-5  Add history service options store read  [risk+verification]
       files:      internal/store/history_services.go, internal/store/history_services_test.go
       covers:     c-3, c-4
       depends_on: t-1
       desc:       ListHistoryServices(ctx) returns one row per service id on any visible grant (live or removed) with the newest grant's snapshot name, "" when none. Uses the same visibility join as t-1.
       contract:   if trimmed-only services vanish, TestListHistoryServices_RemovedOnly fails: a service left only in grant_service_removals is listed
                   if a deleted custom service disappears, TestListHistoryServices_DeletedCustom fails: custom:N is listed with its snapshot name after DeleteCustomService
                   if old snapshots win or ids repeat, TestListHistoryServices_NewestName fails: a service snapshotted 'Roblox' then 'Games' on a later grant appears once as 'Games'
                   if hidden grants leak, TestListHistoryServices_Hidden fails: a service used only by a deleted child, or only by the earlier owner of a reused id, is not listed
                   if legacy rows or an empty install break, TestListHistoryServices_Legacy fails: a legacy service comes back with Name ""; no grants gives a non-nil empty list

  t-6  Carry filters in URL and client  [verification+risk+mvp]
       files:      web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/App.svelte, web/src/App.test.ts
       covers:     c-7, c-8, c-9
       depends_on: t-3
       desc:       historyChild stays as is; a sibling historyFilters store adds the four filters and badChild (?child present but not a positive whole number). navigate and popstate set both. App refresh() navigates only login -> home. New fetchHistoryPage(child, filters, before) and fetchHistoryServices(); old fetchHistory stays until t-11.
       contract:   if the two states collapse, the garbage-list test in api.test.ts fails: /history has historyChild null and badChild false; ?child=, abc, 0, -1, 3.5, 1e2 and %203 each have historyChild null and badChild true
                   if refresh() still rebuilds the URL, the App reload test fails: booting at /history?status=expired&service=custom:5&child=3 keeps the URL byte-for-byte with zero pushState calls, and /history?child=abc is not rewritten to /history
                   if the login bounce regresses, the existing login-route test fails: refresh() on /login still lands on /
                   if PIN unlock loses filters, the App locked test fails: 401 pin_required then unlock leaves the query and historyFilters intact
                   if popstate reads only child, the popstate test fails: going back from /history?status=expired to /history?status=ongoing&from=2026-03-01 updates status and from
                   if a filter is dropped from the URL, the navigate test fails: navigate('history', {child: 3, filters}) pushes exactly /history?child=3&service=youtube&status=expired&from=2026-10-01&to=2026-10-31 once
                   if a query leaks or a store re-notifies, the same test fails: the same call again pushes nothing and does not notify subscribers; navigate('home') pushes / with filters cleared and historyChild null; navigate('history', {child: 2}) pushes /history?child=2 with no earlier service left over; navigate('children', {service:'x'}) carries no query
                   if the client builds a different URL from the server's, the fetchHistoryPage test fails on the literal /api/v1/history?child=3&service=custom%3A5&status=expired&from=2026-03-08T05%3A00%3A00.000Z&until=2026-03-09T04%3A00%3A00.000Z&before=40; no filters requests /api/v1/history with no ?; null lists become []; a 404 rejects with ApiError 404

  t-7  Build history filter bar component  [verification+risk]
       files:      web/src/lib/HistoryFilterBar.svelte, web/src/lib/HistoryFilterBar.test.ts
       covers:     c-4, c-5, c-6, c-9
       depends_on: t-3
       desc:       Presentational bar. Props child, filters, children, services, onChange. Selects for child, service and how it ended; two date inputs; one value each (locked filter_values).
       contract:   if an end status is missing or mislabelled, the status test fails: All plus the five statusLabel options, and picking 'Ended (reason unknown)' emits ended_unknown with other filters unchanged
                   if a control reads All while a filter is active, the unknown-value test fails: filters.service custom:9 absent from services renders as a selected option showing its bare id, including when services is []; child 3 with an empty children list shows selected
                   if labels are used as values, the service test fails: two ids sharing a name are separate options emitting their own ids
                   if a control allows several values, the single-value test fails: no select has multiple (locked filter_values)
                   if changing one control loses another, the merge test fails: picking a status with child and from set emits one onChange that keeps both
                   if dates emit per keystroke or clearing leaks an empty string, the date test fails: input events emit nothing, one change event emits once with YYYY-MM-DD unchanged, and clearing from emits null
                   if the child control resets the rest, the child test fails: choosing All emits child null with filters unchanged
                   if empty options crash the bar, the empty test fails: empty children and services render every control with its All value

Wave 3 (t-8 depends t-4; t-9 depends t-6, t-7)

  t-8  Serve filtered overview endpoint  [verification+risk+mvp]
       files:      internal/api/history.go, internal/api/history_test.go, internal/api/api.go
       covers:     c-1, c-2, c-3, c-4, c-5, c-6, c-7, c-9
       depends_on: t-4
       desc:       Route GET /api/v1/history behind requireSession; ChildStore gains ListHistory. Strict single-value query parsing. The grant view and name resolution are extracted so both history handlers build grants one way; per-grant child is added only on the overview.
       contract:   if grants lose their child or the shape drifts, TestHistoryOverview_GoldenKeys fails: page keys are child, grants, next_before; child is null without a filter and {id,name} with one; grant keys are the per-child set plus child; an unfiltered empty page has "child":null and "grants":[]
                   if the extraction changes the old route, the existing TestHistory_GoldenKeys fails unedited: per-child grants gain no child key
                   if the SQL predicate and endStatus drift, TestHistoryOverview_StatusMatchesWire fails: over all 12 (status, end_reason) cells seeded raw, each grant appears under exactly one ?status= and its returned status equals that value, and the five results together hold every grant exactly once
                   if parsing is lenient, TestHistoryOverview_Errors fails: status=bogus, from=notadate, until=2026-01-01 (no time), before=0, an empty service, status, from, until or before, and each of child, service, status, from, until repeated are 400; an unescaped + offset in from (decoded as a space) is 400, never an unfiltered 200; child=abc, 0, 999 and a deleted child are 404; no session is 401; from after until is 200 with grants []
                   if the cursor drops filters, TestHistoryOverview_FilteredPaging fails: 60 matching among 120 chain through next_before with the same query to 50 + 10 + null, every row matching and none repeated
                   if client and server disagree, TestHistoryOverview_ClientURL fails: the literal URL from t-6 (%3A, .000Z) returns 200 and exactly the expected grant
                   if a trim or reuse is lost on the wire, TestHistoryOverview_EndToEnd fails: through the real store and engine, a block-trimmed service is found by service= with removed_reason set; DELETE a child, create one reusing the id, and only the new child's grants are listed
                   if the catalogue becomes a hard dependency, TestHistoryOverview_CatalogueDown fails: AdGuard Services() returning 500 gives 200 with snapshot names and bare ids; with all snapshots present no catalogue call is made

  t-9  Turn History screen into filtered overview  [verification+risk+mvp]
       files:      web/src/lib/History.svelte, web/src/lib/History.panel.test.ts, web/src/App.svelte, web/src/App.test.ts
       covers:     c-1, c-2, c-5, c-7, c-8, c-9
       depends_on: t-6, t-7
       desc:       Hosts the filter bar, reads listChildren and service options once per mount and independently of the list, shows each grant's child, treats no child as the overview, reloads only when the canonical filter key changes, and pushes filter changes through navigate. badChild and 404 stay 'No such child'.
       contract:   if the overview still needs a child, the panel test fails: child null with badChild false fetches /api/v1/history, heading is plain History, and every row shows its child name beside the existing status, times, services and extensions, with mixed children on one page
                   if 'No such child' is lost, the panel tests fail: badChild shows it with Back, no fetch and no filter bar; a 404 for ?child=99 shows the same
                   if a child link stops selecting, the panel test fails: child 3 fetches ?child=3, heading is 'Cleo — history', and the child select shows Cleo
                   if a control does not filter, the panel test fails: changing each of child, service, status, from and to pushes the matching URL and triggers exactly one fetch
                   if load more drops filters, the panel test fails: with status expired, Load more requests /api/v1/history?status=expired&before=40 and appends deduped
                   if changing a filter keeps the cursor, the panel test fails: the next request has no before and the old rows and Load more are gone
                   if unrelated updates reset paging, the key test fails: re-emitting identical filters makes no fetch and keeps loaded pages
                   if a late response lands, the panel tests fail: first-page and further-page responses, and their errors, for an earlier filter are dropped, old rows never appear, and Load more stays usable for the new filters (mirrors 'after switching child')
                   if states are conflated, the panel test fails: a filtered empty page shows 'No grants match these filters' with the bar still there; an unfiltered one shows 'No grants yet'; a 500 shows retry
                   if options gate the list, the isolation test fails: /api/v1/children or /api/v1/history/services failing with 500 still renders grants and a usable bar; the list failing leaves the bar usable after retry; options are fetched once per mount, not per filter change
                   if options come from the wrong place, the panel test fails: the service select lists the served options including an id no live catalogue has
                   if reload doubles the fetch, the App test fails: booting at /history?child=3&status=expired calls exactly /api/v1/me, one history page, one /api/v1/children and one /api/v1/history/services request, and the status select shows Expired
                   if back does not restore, the App test fails: change status, popstate returns the previous filter with list and control reverted; Back to Home then popstate restores the filtered list

Wave 4 (t-10 depends t-5, t-8; t-11 depends t-9)

  t-10 Serve history service options endpoint  [risk+verification]
       files:      internal/api/history_services.go, internal/api/history_services_test.go, internal/api/api.go
       covers:     c-4
       depends_on: t-5, t-8
       desc:       Route GET /api/v1/history/services behind requireSession; ChildStore gains ListHistoryServices. Name resolves snapshot, then live catalogue, then bare id, reusing t-8's resolver. Sorted by name, then id.
       contract:   if option names skip a step, TestHistoryServices_Names fails: snapshot wins, a legacy service with no snapshot shows the live catalogue name, and with AdGuard down it shows its bare id
                   if a catalogue failure fails the options, TestHistoryServices_CatalogueDown fails: AdGuard 500 gives 200 with snapshot names and bare ids for the rest
                   if the catalogue is read needlessly, TestHistoryServices_NoCatalogueRead fails: with every name snapshotted no catalogue request is made
                   if a deleted custom service loses its option, TestHistoryServices_DeletedCustom fails: through the real store, a grant for custom:1 'Roblox', then deleting custom:1, still lists {custom:1, Roblox}, and service=custom:1 on the overview returns that grant
                   if the shape or order drifts, TestHistoryServices_Shape fails: keys are exactly services, id, name; no grants gives "services":[]; two services with equal names sort by id; no session gives 401
                   if the mux route collides, TestHistoryServices_Routing fails: /api/v1/history/services returns options, /api/v1/history returns a page, and /api/v1/history/other is 404

  t-11 Link overview from Home, drop fetchHistory  [verification+risk+mvp]
       files:      web/src/lib/Home.svelte, web/src/lib/Home.test.ts, web/src/lib/api.ts, web/src/lib/api.test.ts
       covers:     c-1, c-9
       depends_on: t-9
       desc:       'History' button in Home's nav row navigating to bare /history; per-child links stay. Removes the now-unused fetchHistory client and its tests. The server route stays.
       contract:   if the link carries a child or sits inside the grid, the Home test fails: the button pushes exactly /history with historyChild null and empty filters, and shows with zero children and when the grid fails to load
                   if filters persist into the link, the Home test fails: after a filtered /history and navigate('home'), the button pushes bare /history
                   if the labels collide, the Home links test fails: 'History' and 'History for Ada' resolve to different buttons
                   if per-child links change, the existing test 'each child has a History link to its own history' fails: still /history?child=2 with no other query
                   if a caller of fetchHistory survives, make typecheck fails on the missing export
```

### Coverage

| Criterion | Tasks |
|---|---|
| c-1 overview on Home, every child, full detail | t-1, t-8, t-9, t-11 |
| c-2 newest 50, load more without skip or repeat | t-1, t-8, t-9 |
| c-3 deleted and reused-id exclusion | t-1, t-2, t-5, t-8 |
| c-4 service filter, trimmed included | t-4, t-5, t-7, t-8, t-10 |
| c-5 date range | t-3, t-4, t-7, t-8, t-9 |
| c-6 how it ended | t-4, t-7, t-8 |
| c-7 filters combine, load more stays inside | t-4, t-6, t-8, t-9 |
| c-8 survive reload and back | t-3, t-6, t-9 |
| c-9 child filter, Home link, No such child | t-1, t-6, t-7, t-8, t-9, t-11 |

### Corrections to draft contracts

Each of these changes what a draft wrote, because the repo contradicted it or a disagreement default required it. No task was added.

- `TestMigrate_Order` (verification t-2) runs against a stub filesystem and cannot see 0011. Replaced with `TestMigrate0011_Order`, modelled on `TestMigrate0009_Order`.
- Risk's and mvp's Stockholm and Los Angeles dates, and risk's Havana case, are restated in the single pinned zone (Disagreement 7).
- Verification's `to` becomes `until` on the wire, including in its literal URL (Disagreement 11).
- Verification's `TestHistoryFilters_Lists` is dropped: `GET /api/v1/children` already exists and excludes deleted children (Disagreement 2).
- Verification's moves of `mustHistory`, `setup_test.go`, `historyPath` and `main_test.go` to the new read are dropped, so those suites stay unedited as a regression net (Disagreement 4). Its real-server literal-URL test collapses into risk's `TestHistoryOverview_ClientURL` in `internal/api`.

## Disagreements

Thirteen divergences. Entries 1-5 change the shape of the plan, and 6-13 are smaller.

**1. Overall task size**
- Diverged: 4 tasks against 10 and 11, including whether visibility and filters are one store task.
- Lenses: mvp puts everything in one store task and one screen task, and accepts 6 files on t-4 because the client swap and screen "must land together or the tree is red". Risk and verification split them, so paging is proven before dynamic predicates can mask a failure.
- Default: the finer split. Keeping old `fetchHistory` until t-11 removes mvp's red-tree concern, so its reason for 6 files no longer applies.
- Why it matters: mvp's t-1, t-2 and t-4 each cover 8 of 9 criteria. A failure there points at almost nothing, and the 6-file task breaks the granularity rule. The cost of splitting is more commits and review cycles.

**2. How filter options reach the screen** (three-way)
- Diverged: the transport for the child and service option lists.
- Lenses: mvp embeds `children` and `services` in every page response, for one fetch and one failure path. Verification adds `GET /history/filters` returning both. Risk adds `GET /history/services` and reads children from the existing list.
- Default: risk. `GET /api/v1/children` and `listChildren()` already exist and are store-only, so verification's `children` half duplicates a route.
- Why it matters: this sets the page's wire shape, the failure isolation (a failed option read must not take down the list) and the boot request count (`/me`, page, children, services: four requests, against two for mvp). Mvp's single fetch is simpler, but it recomputes options on every page and has no options to show on a 404.

**3. Options as their own tasks, or folded in**
- Diverged: where the options store read and endpoint sit.
- Lenses: risk gives them separate tasks on separate files (t-5, t-10). Verification folds them into its filter task (t-4) and its API task (t-7). Mvp folds them into t-1 and t-2.
- Default: risk's split, with verification's contracts merged in.
- Why it matters: the store read then runs in parallel with the filter predicates, and neither task is overloaded. The cost is that t-10 lands in wave 4 because it needs t-8's name resolver. That does not lengthen the critical path, since t-11 is also wave 4.

**4. Retiring `GET /api/v1/children/{id}/history`**
- Diverged: whether a task exists for this at all.
- Lenses: verification t-10 retires the route and `ListGrantHistory`, because "a route with no client needs its own tests or goes stale". Risk keeps it for tabs open on an older bundle. Mvp keeps it for about 550 lines of free regression cover, but flags it as dead code worth the judge's attention.
- Default: keep the route and drop verification's t-10. It traces to no criterion, and its c-1 claim does not hold, since c-1 needs nothing removed.
- Why it matters: keeping it leaves a route with no web client, plus two handlers held together by the extracted view and the unedited golden test. Retiring it gives one read path, but rewrites those suites and shows 'No such child' to a tab left open across a deploy. Reversing this restores verification's t-10 and its test moves in t-1 and t-8.

**5. Migration 0011 backfill**
- Diverged: a task two planners need and one omits.
- Lenses: risk and verification add it because c-3 says "never" and ids reused before 0009 read watermark 0. Mvp plans no migration and never mentions the hole. Both risk and verification say t-2 can be dropped alone.
- Default: keep t-2. Nothing depends on it, and the repo confirms the leak is possible: `grants.child_id` has no foreign key, so grants outlive their child, and 0009 left existing children at 0.
- Why it matters: it rests on a clock comparison, so clock skew could hide a legacy grant, though it cannot leak one. It only helps an install that deleted the highest-id child and added another before 0009. The repo has no tags, so the code cannot show whether such an install exists. You can answer that; if none exists, drop t-2.

**6. When the Home link lands**
- Diverged: wave 1 against the last wave.
- Lenses: mvp puts it in wave 1 with the URL work. Risk and verification put it last, combined with removing `fetchHistory`.
- Default: last wave. Alone it is a two-file task under 10 minutes, and its empty-filters assertion needs t-6's store. Removing `fetchHistory` strictly needs t-9, so the combined task sits after t-9.
- Why it matters: read strictly, the link alone could land earlier. Until t-9, though, bare `/history` still shows 'No such child', so an earlier link goes nowhere useful on the branch.

**7. Time zone test mechanism**
- Diverged: how DST tests get their zone.
- Lenses: risk switches `process.env.TZ` per test across three zones, with a positive control. Mvp stubs Stockholm. Verification pins New York at the top of `vite.config.ts`.
- Default: verification's pin, keeping risk's positive control, but pinned to `America/Havana`. This zone is a judge's combination and appears in no draft as the pin.
- Why it matters: Stryker's vitest runner forces `pool: 'threads'`. I confirmed on this Node that setting `TZ` inside a worker has no effect, while a worker inherits a zone set on the main thread. Risk's positive control would fail every zone test under `dross verify`. Havana gives the same 2026 instants as New York, so verification's literals are unchanged, and it also has risk's skipped midnight, which New York lacks. The web suite passes 469/469 with `TZ` set to either zone, run via the environment rather than the config pin. The trade-off is that the pin affects every web test, and only one zone is exercised.

**8. Where filter parsing lives**
- Diverged: the module, and which layer owns badChild.
- Lenses: risk adds `historyFilters.ts`, with child and badChild parsed there. Verification extends the existing `history.ts` and leaves child parsing in `api.ts`. Mvp keeps all parsing in `api.ts`.
- Default: verification. `history.ts` is already the home for pure history helpers and imports only types from `api.ts`, so no runtime cycle appears. `childFor` already lives in `api.ts`.
- Why it matters: child validity stays separate from the four filters, so it is tested in t-6 rather than t-3. Risk's version would have put all URL parsing in one place.

**9. Filter bar as its own task**
- Diverged: a task two planners need and one rejects.
- Lenses: risk and verification build a separate `HistoryFilterBar.svelte` in wave 2. Mvp inlines the controls in `History.svelte`.
- Default: separate component. Its contracts need no backend, it runs alongside t-6, and `History.svelte` is already the busiest file.
- Why it matters: it adds one file and a props contract. In return, control behaviour is tested without mocking the fetch layer.

**10. Reversed date range**
- Diverged: from after to.
- Lenses: risk returns 400 on the server and shows an inline message with no fetch. Verification returns an empty list and lets the empty state explain it. Mvp does not address it.
- Default: verification. Risk's `rangeReversed`, its range test and its server 400 are dropped, and t-8 asserts a 200 with `[]`.
- Why it matters: no criterion decides this. The default is less code, and 'No grants match these filters' stays true, but a parent gets no hint that the dates are swapped. If you want the hint, restore risk's contracts in t-3, t-8 and t-9.

**11. API param names**
- Diverged: `status` or `end`, and `to` or `until`.
- Lenses: mvp uses `end`; risk and verification use `status`. Mvp and verification use `to` on the wire; risk uses `until`.
- Default: `status`, matching the wire field and `HistoryStatus`. `until` follows the minority, deliberately.
- Why it matters: the SPA URL's `to` is an inclusive calendar day, while the API's bound is an exclusive instant. Sharing one name across those two meanings invites exactly the off-by-one c-5 must avoid. The name appears in literal tests on both sides, so changing it later touches t-3, t-4, t-6 and t-8.

**12. Selected value missing from options**
- Diverged: a contract one planner rejects.
- Lenses: risk and verification render it as a selected option labelled by its bare id. Mvp rejects any extra option for a hand-edited `?service=`, as not traceable to a criterion.
- Default: render it. Options can also be missing because the options read failed, not only because a URL was hand-edited.
- Why it matters: if the options read fails while a filter is active, an unrendered control reads All over a filtered list. That shows the wrong state after a reload, which is c-8's territory.

**13. Date control emission and clearing**
- Diverged: when dates emit, and what clearing emits.
- Lenses: risk emits on change, not input, and clears to `''`. Verification clears to `null` and does not address keystrokes. Mvp rejects a debounce.
- Default: change events only, with no debounce, clearing to `null`.
- Why it matters: per-keystroke emission would add history entries and fetches, which harms c-8's back button. `null` matches what `parseFilters` returns, so "unset" has one representation.

### Agreed limit, not a divergence

A service trimmed before migration 0009 left no removal row, so c-4 cannot find those grants. Mvp and verification both record this as an accepted gap, and risk is silent. No task addresses it, because the data was never recorded.
