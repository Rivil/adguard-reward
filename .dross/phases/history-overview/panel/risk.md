# Risk lens draft: history-overview

Phase history-overview — 11 tasks across 4 waves

Wire pins this draft assumes (both sides test the same literal strings, so mocked web tests cannot drift from the server):

- `GET /api/v1/history[?child=&service=&status=&from=&until=&before=]` behind requireSession returns `{ child:{id,name}|null, grants:[<per-child grant shape> + child:{id,name}], next_before:id|null }`. Newest first by id DESC, page 50, lists are [] never null.
- `from` is inclusive and `until` exclusive, both RFC 3339 instants. The client computes them from local days; the server knows no time zone.
- `status` is one of the five wire statuses. `service` is an exact service id.
- 404: unknown, deleted or non-numeric child. 400: bad before, status or date, empty or repeated param, from >= until. 401: no session.
- `GET /api/v1/history/services` returns `{ services:[{id,name}] }`, sorted by name then id.
- `GET /api/v1/children/{id}/history` stays, byte-identical.
- URL: `/history?child=&service=&status=&from=YYYY-MM-DD&to=YYYY-MM-DD`. The URL holds local calendar days, never instants.

Wave 1
  t-1  Add overview store read across children
       files:    internal/store/history.go, internal/store/history_test.go
       desc:     ListHistory(ctx, HistoryFilter{Child, Before}, limit) joins children in SQL (g.id > c.history_after_grant_id), reads limit+1, reuses historyDetails, names each grant's child. Unknown Child is ErrChildNotFound. ListGrantHistory keeps its behaviour.
       covers:   c-1, c-2, c-3, c-9
       owns:     page integrity across children; deleted and reused-id exclusion happening in SQL; per-child regression
       depends:  none
       contract: if deleted children are dropped after LIMIT instead of in SQL, TestListHistory_DeletedChildInterleaved fails: 120 grants alternating Ada/Ben with Ben deleted give a full page of 50 of Ada's with HasMore true, then 10 and HasMore false — a short page or a false end fails
                 if a reused id inherits rows, TestListHistory_ReusedChildID fails: new child Cleo reusing Ada's id (asserted equal) lists only her own grants, with and without Filter.Child, and no grant carries Cleo's name that predates her watermark
                 if has-more or ordering is off, TestListHistory_Paging fails: 51 grants over two children give 50 newest-first plus HasMore, exactly 50 gives HasMore false, and every grant carries its own child's name
                 if the cursor must point at a visible grant, TestListHistory_CursorSurvivesChanges fails: deleting the child that owns the cursor grant between pages still returns the next older grants with no skip; a grant created between pages appears neither in page 2 nor twice
                 if the child filter conflates unknown with empty, TestListHistory_UnknownChild fails: Child=999 returns ErrChildNotFound, a real child with no grants returns an empty page, and an empty install with no Child returns an empty non-nil list
                 if the shared read regresses the per-child path, the existing TestListGrantHistory_* suite fails unedited, including the DeepEqual in TestListGrantHistory_Legacy

  t-2  Backfill watermark for pre-0009 reused ids
       files:    internal/store/migrations/0011_history_watermark_backfill.sql, internal/store/store_test.go
       desc:     For children still at watermark 0, set history_after_grant_id to the highest id of that child_id's grants with started_at < children.created_at. Touches children only, never grants.
       covers:   c-3
       owns:     the legacy hole in "never shows the old child's grants": ids reused before 0009 read watermark 0
       depends:  none
       contract: if pre-0009 reuse still leaks, TestMigrate0011_ReusedBeforeWatermark fails: at 0008, child 1 created_at 5000 with grants 1, 2 started 1000, 2000 and grant 3 started 6000 upgrades to watermark 2, and ListGrantHistory lists only grant 3
                 if real legacy history is hidden, TestMigrate0011_LegacyChildKept fails: the openAt0008 seed keeps watermark 0 and lists grants 3, 2, 1; a grant with started_at == created_at stays visible
                 if the backfill overwrites a set watermark, TestMigrate0011_KeepsSetWatermark fails: a child at 0010 with watermark 7 still reads 7
                 if the migration rewrites grants, TestMigrate0011_GrantsUntouched fails: every grants row is byte-identical before and after, and end_reason stays NULL (locked legacy_end_reason)

  t-3  Add pure history filter parsing and day bounds
       files:    web/src/lib/historyFilters.ts, web/src/lib/historyFilters.test.ts
       desc:     parseFilters(search) gives child, badChild, service, status, from, to. filterSearch(f) gives one canonical query order. dayBounds(from, to) gives local-midnight instants as UTC ISO, with until the midnight after `to`. rangeReversed(f). HistoryFilters and ServiceOption types live here.
       covers:   c-5, c-8, c-9
       owns:     time zone and DST day edges; absent versus invalid child; garbage URL values
       depends:  none
       contract: if TZ switching silently no-ops, every zone test fails its positive control: each zone test first asserts getTimezoneOffset for a known date in that zone, so a worker that ignores process.env.TZ fails rather than passing as UTC
                 if days are added as 24h, the DST test fails: in Europe/Stockholm 2026-03-29..2026-03-29 gives 2026-03-28T23:00:00.000Z to 2026-03-29T22:00:00.000Z (23h) and 2026-10-25..2026-10-25 gives 2026-10-24T22:00:00.000Z to 2026-10-25T23:00:00.000Z (25h)
                 if a day string parses as UTC, the zone test fails: in America/Los_Angeles from=2026-03-01 gives 2026-03-01T08:00:00.000Z, not the previous local day
                 if a skipped midnight breaks, the gap test fails: in America/Havana 2026-03-08 gives from 2026-03-08T05:00:00.000Z
                 if bad dates roll over or overflow, the parse test fails: 2026-02-31, 2026-13-01, 0099-01-01 and 9999-12-31 (whose until would serialise as +010000, unparseable by Go) drop to no date filter
                 if absent and invalid child are conflated, the child test fails: no param gives child null, badChild false; ?child=, abc, 0, -1, 3.5 give child null, badChild true
                 if encoding is lossy, the round-trip test fails: parseFilters(filterSearch(f)) equals f for service 'custom:7' and 'a&b=c +'; an unknown status drops to no status filter

Wave 2 (t-4, t-5 depend t-1; t-6, t-7 depend t-3)
  t-4  Add service, date, status filter predicates
       files:    internal/store/history.go, internal/store/history_test.go
       desc:     HistoryFilter gains Service, Status, From, Until as SQL predicates inside the keyset query. Service is an EXISTS over grant_services and grant_service_removals; dates are started_at >= From AND < Until; Status maps wire values to (status, end_reason).
       covers:   c-4, c-5, c-6, c-7
       owns:     trimmed services found once; half-open date edge; NULL reason handling; predicate and argument composition; has-more computed on filtered rows
       depends:  t-1
       contract: if the filter reads only live services or snapshot names, TestListHistory_ServiceFilter fails: a live service, a partial trim, a last-service trim, a legacy grant with no name row and a deleted custom:N each match exactly once
                 if matching is not exact and parameterised, TestListHistory_ServiceExact fails: 'you', '%' and `' OR 1=1 --` match nothing against youtube
                 if the date edges drift, TestListHistory_DateEdges fails: started_at == From is in, From-1 is out, Until-1 is in, Until is out; a grant extended into the range but started before it is out
                 if NULL reason is mishandled, TestListHistory_StatusFilter fails: each of the five values returns only its rows, 'ended_unknown' returns a legacy ended grant, and an overdue active grant is 'ongoing' not 'expired'
                 if predicates AND wrongly or arguments misalign in the dynamic SQL, TestListHistory_FiltersCombine fails: a grid of grants where removing any one of child, service, status, from, until adds exactly one known extra grant
                 if paging is computed before filtering, TestListHistory_FilteredPaging fails: 120 grants with 60 matching give 50 + HasMore then 10, no overlap; exactly 50 matching with older non-matching rows gives HasMore false
                 if a row changing mid-paging breaks the cursor, TestListHistory_StatusChangesBetweenPages fails: under status=ongoing, a page-1 grant that expires before page 2 causes no skip or repeat among the rest

  t-5  Add history service options store read
       files:    internal/store/history_services.go, internal/store/history_services_test.go
       desc:     ListHistoryServices(ctx) returns one row per service id on any visible grant (live or removed) with the newest grant's snapshot name, "" when none. Uses the same visibility join as t-1.
       covers:   c-3, c-4
       owns:     option list completeness (trimmed-only, deleted custom) and no leak from hidden grants
       depends:  t-1
       contract: if trimmed-only services vanish, TestListHistoryServices_RemovedOnly fails: a service left only in grant_service_removals is listed
                 if a deleted custom service disappears, TestListHistoryServices_DeletedCustom fails: custom:N is listed with its snapshot name after DeleteCustomService
                 if old snapshots win or ids repeat, TestListHistoryServices_NewestName fails: a service snapshotted 'Roblox' then 'Games' on a later grant appears once as 'Games'
                 if hidden grants leak, TestListHistoryServices_Hidden fails: a service used only by a deleted child, or only by the earlier owner of a reused id, is not listed
                 if legacy rows or an empty install break, TestListHistoryServices_Legacy fails: a legacy service comes back with name ""; no grants gives a non-nil empty list

  t-6  Carry filters through routing, reload, client
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/App.svelte, web/src/App.test.ts
       desc:     navigate('history', filters) pushes filterSearch; a historyFilters store sits beside historyChild, set at the same sites and on popstate. App.refresh stops rebuilding /history's URL. Adds fetchHistoryOverview(filters, before) and fetchHistoryServices.
       covers:   c-8, c-9
       owns:     refresh() stripping or rewriting the query; extra back-stack entries; filters leaking across routes; client URL shape
       depends:  t-3
       contract: if refresh rewrites the URL, the App reload test fails: loading /history?service=custom:7&child=3&status=expired keeps that exact string (order and encoding) and history.length is unchanged after refresh()
                 if an invalid child is normalised away, the App reload test fails: /history?child=abc stays /history?child=abc with badChild true, not /history
                 if PIN unlock drops filters, the unlock test fails: a pin_required on a filtered URL, then unlock, leaves the URL and historyFilters unchanged
                 if the login bounce regresses, the existing login-route test fails: refresh() on /login still lands on /
                 if popstate re-parses only the child, the popstate test fails: going back from ?status=expired to ?status=ongoing&from=2026-03-01 updates status and from
                 if filters leak, the navigate test fails: navigate('home') after a filtered /history pushes '/', historyFilters is empty and historyChild null; navigate('children', {service:'x'}) carries no query
                 if the client URL drifts from the server, the fetch test fails: with TZ pinned, fetchHistoryOverview for child 3, custom:7, expired, 2026-03-01..2026-03-02, before 40 requests the literal string t-8 tests; no filters requests /api/v1/history; [] defaults apply; a 404 rejects with ApiError status 404

  t-7  Build history filter bar component
       files:    web/src/lib/HistoryFilterBar.svelte, web/src/lib/HistoryFilterBar.test.ts
       desc:     Presentational bar with child, service and status selects and from/to date inputs, one value each (locked filter_values). Props are filters, children, services; emits onChange(next filters).
       covers:   c-4, c-5, c-6, c-9
       owns:     selected value missing from options; one history entry per change; rendering with empty options
       depends:  t-3
       contract: if a selected value outside the options renders blank, the missing-option test fails: service custom:99 with no matching option still shows selected, labelled by its bare id; child 3 with an empty children list shows selected
                 if changing one control loses another, the merge test fails: picking a status with child and from set emits one onChange that keeps both
                 if dates emit per keystroke, the date test fails: input events emit nothing, one change event emits once, and clearing emits from ''
                 if status options drift, the options test fails: the status select lists exactly the five statuses with statusLabel text plus an 'All' value, and 'Ended (reason unknown)' is present
                 if empty options crash the bar, the empty test fails: empty children and services render every control with its 'All' value

Wave 3 (t-8 depends t-4; t-9 depends t-6, t-7)
  t-8  Serve filtered overview endpoint
       files:    internal/api/history.go, internal/api/history_test.go, internal/api/api.go
       desc:     Route GET /api/v1/history behind requireSession; ChildStore gains ListHistory. Strict single-value query parsing. The grant view and name resolution are extracted so both history handlers build grants one way; per-grant child is added only on the overview.
       covers:   c-1, c-2, c-3, c-4, c-5, c-6, c-7, c-9
       owns:     input validation; SQL status predicate versus Go endStatus drift; catalogue stays optional; wire shape; per-child wire unchanged
       depends:  t-4
       contract: if the status predicate drifts from endStatus, TestHistoryOverview_StatusMatchesWire fails: all 12 (status, end_reason) combinations seeded raw each appear under exactly one status filter, the one their wire status field shows
                 if bad input is ignored, TestHistoryOverview_Errors fails: status=bogus, from=abc, from >= until, an empty param, a repeated service or status, and before=0 give 400; an unescaped + offset in from (decoded as a space) gives 400, never an unfiltered 200; child=abc, 999 or a deleted child give 404; no session gives 401
                 if the cursor loses the filters, TestHistoryOverview_FilteredPaging fails: 60 matching among 120 chain through next_before with the same query to 50 + 10 + null, every row matching
                 if client and server disagree, TestHistoryOverview_ClientURL fails: the literal URL from t-6 returns 200 and exactly the expected grant
                 if a trim or reuse is lost on the wire, TestHistoryOverview_EndToEnd fails: through the real store and engine, a block-trimmed service is found by service= with removed_reason set, a deleted child's grants are absent, and a reused id shows only the new child's
                 if the catalogue becomes a hard dependency, TestHistoryOverview_CatalogueDown fails: AdGuard Services() returning 500 gives 200 with snapshot names and bare ids; with all snapshots present no catalogue call is made
                 if the shape drifts, TestHistoryOverview_GoldenKeys fails: page keys are child, grants, next_before; child is null without a filter and {id,name} with one; grant keys are the per-child set plus child; lists are []
                 if the extraction changes the old route, the existing TestHistory_GoldenKeys fails unedited: per-child grants gain no child key

  t-9  Make History the filtered overview screen
       files:    web/src/lib/History.svelte, web/src/lib/History.panel.test.ts, web/src/App.svelte, web/src/App.test.ts
       desc:     History takes filters, fetches the overview, names each grant's child, hosts the bar, reads children and service options independently, and navigates on bar changes. Reloads only when the canonical filter key changes. /history with no child is now the overview; badChild and 404 stay "No such child".
       covers:   c-1, c-2, c-5, c-7, c-8, c-9
       owns:     stale responses after a filter change; load more keeping its filters; empty versus no-match versus not-found; option failure isolation; DOM-level reload and back
       depends:  t-6, t-7
       contract: if a late response lands, the race test fails: changing a filter while page 1 or a load more is in flight drops the old response, old rows never appear, and Load more stays usable for the new filters
                 if load more leaves the filter, the paging test fails: the second request carries the same filters plus before=<next_before>, rows append deduped, and a filter change resets the list and cursor
                 if unrelated updates reset paging, the key test fails: re-emitting identical filters makes no fetch and keeps loaded pages
                 if c-9 regresses, the not-found test fails: badChild shows "No such child" with no fetch, a 404 shows it with no list, and no child shows the overview with no "No such child"
                 if states are conflated, the states test fails: no filters and zero grants shows "No grants yet", filters and zero grants shows "No grants match these filters" with the bar still usable, a 500 shows retry
                 if option reads gate the list, the isolation test fails: children or services failing with 500 still renders grants and the bar; the list failing still leaves the bar usable after retry
                 if a reversed range hits the server, the range test fails: from after to shows an inline message and makes no fetch
                 if child names are missing, the rows test fails: each row shows g.child.name, and the heading reads "<name> — history" with a child filter and "History" without
                 if back or reload loses filters in the DOM, the App flow test fails: set a status, tap Back to Home, browser back restores the status in the bar and refetches it; a remount at the same URL shows the same selection

Wave 4 (t-10 depends t-5, t-8; t-11 depends t-9)
  t-10 Serve history service options endpoint
       files:    internal/api/history_services.go, internal/api/history_services_test.go, internal/api/api.go
       desc:     Route GET /api/v1/history/services behind requireSession; ChildStore gains ListHistoryServices. Name resolves snapshot, then live catalogue, then bare id, reusing t-8's resolver. Sorted by name, then id.
       covers:   c-4
       owns:     an unavailable catalogue never empties the service filter; stable option order
       depends:  t-5, t-8
       contract: if a catalogue failure fails the options, TestHistoryServices_CatalogueDown fails: AdGuard 500 gives 200 with snapshot names and bare ids for the rest
                 if the catalogue is read needlessly, TestHistoryServices_NoCatalogueRead fails: with every name snapshotted no catalogue request is made
                 if a deleted custom service loses its option, TestHistoryServices_DeletedCustom fails: through the real store, a grant for custom:1 'Roblox', then deleting custom:1, still lists {custom:1, Roblox}, and service=custom:1 on the overview returns that grant
                 if the shape or order drifts, TestHistoryServices_Shape fails: keys are exactly services, id, name; no grants gives "services":[]; two services with equal names sort by id; no session gives 401
                 if the mux route collides, TestHistoryServices_Routing fails: /api/v1/history/services returns options, /api/v1/history returns a page, and /api/v1/history/other is 404

  t-11 Link overview from Home, drop old client
       files:    web/src/lib/Home.svelte, web/src/lib/Home.test.ts, web/src/lib/api.ts, web/src/lib/api.test.ts
       desc:     Home nav gains History (navigate('history'), no params); per-child links stay. Removes the now-unused fetchHistory client and its tests. The server route stays for open tabs on an older bundle.
       covers:   c-1, c-9
       owns:     Home entry points opening the right filter state
       depends:  t-9
       contract: if the overview link carries a child, the Home nav test fails: tapping History pushes exactly /history, and historyChild is null
                 if the labels collide, the Home links test fails: 'History' and 'History for Ada' resolve to different buttons, and the per-child link still pushes /history?child=2 with no other query
                 if filters persist into a link, the Home nav test fails: after a filtered /history and navigate('home'), the overview link pushes bare /history
                 if the client is left behind, `make typecheck` fails: api.test.ts no longer exercises fetchHistory and no module imports it, so a stale History.svelte call is a compile error

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 overview on Home, every child, full detail | t-1, t-8, t-9, t-11 |
| c-2 newest 50, load more without skip or repeat | t-1, t-8, t-9 |
| c-3 deleted and reused-id exclusion | t-1, t-2, t-5, t-8 |
| c-4 service filter, trimmed included | t-4, t-5, t-7, t-8, t-10 |
| c-5 date range | t-3, t-4, t-7, t-8, t-9 |
| c-6 end filter | t-4, t-7, t-8 |
| c-7 filters combine, load more stays inside | t-4, t-8, t-9 |
| c-8 survive reload and back | t-3, t-6, t-9 |
| c-9 child filter, Home link, No such child | t-1, t-3, t-6, t-7, t-8, t-9, t-11 |

## Judgment calls

- Backfill for pre-0009 reused ids (t-2): chose a one-off clock comparison, rejected leaving the hole. c-3 says "never", and those children read watermark 0. Failure direction is hiding a legacy grant under clock skew, not leaking one; the judge can drop t-2 if no pre-0009 install exists (the repo has no release tags).
- Deleted-child exclusion: chose a SQL join, rejected filtering in Go. Post-LIMIT filtering gives short pages and false ends, which is c-2's failure.
- Keyset order: chose id DESC with started_at used only as a filter, rejected ordering by started_at. Ids are a stable total order because grants are never deleted, and a clock step cannot reorder or skip.
- Date wire format: chose client-computed instants, rejected sending days plus a zone. Locked date_range uses the device zone, and instants keep DST logic in one tested place.
- Dates in the URL: chose local days, rejected instants, so a reload after travel still means the same calendar days.
- Bad filter values: server 400s, client drops them on parse. Rejected server-side tolerance because a silent unfiltered 200 looks like a correct result. An invalid child is the exception: it stays "No such child" per c-9.
- `/history` with no child: flips the grant-history behaviour from "No such child" to the overview, as locked screen_structure requires. badChild keeps `?child=abc` on "No such child"; rejected treating it as no filter.
- Per-child endpoint: chose to keep it unchanged, rejected deleting it. An open tab on an older bundle would read a 404 as "No such child". Cost is two routes, held together by the extracted grant view and the unedited golden test.
- Status drift: chose a black-box test over all 12 (status, end_reason) rows, rejected moving endStatus into the store. The SQL predicate mirrors the mapping either way, so only a test across both catches drift.
- Service options scope: chose all visible history, not narrowed by the child filter. Locked service_filter says every service in history, and narrowing would strand a selected value on child change. Hidden grants are excluded because their option could only ever return nothing.
- Custom service id reuse: no task. Migration 0007 uses AUTOINCREMENT, so custom:N is never reused and id matching is safe.
- Grant id reuse: no task. Nothing deletes grants and ImportSetup goes through insertChild, so the watermark holds.
- Service worker: no task. decide() already bypasses all of /api/, and /history is a shell navigation.
- New index: none. The volume is thousands of rows a year (locked retention) and the keyset scan walks the primary key; rejected adding schema for a problem not yet seen.
- Scroll and pages after reload: filters survive, loaded pages do not. c-8 names filters only.
- Splitting store work into t-1 and t-4: chose two tasks on one file in sequence, rejected one task, so visibility and paging are proven before dynamic predicates can mask a failure.
