# history-overview — MVP lens draft

Phase history-overview — 4 tasks across 2 waves

Wire (pinned so t-2 and t-4 can run in parallel):

    GET /api/v1/history[?child=N&service=ID&end=STATUS&from=RFC3339&to=RFC3339&before=GRANT_ID]  (requireSession)
    -> { grants:   [ <existing grant view> + child:{id,name} ],   newest first by id DESC, page 50
         next_before: id|null,
         children: [{id,name}],   live children, for the child filter
         services: [{id,name}] }  every service on a visible grant, for the service filter
    from is inclusive, to is exclusive. Lists are [] never null.
    end is one of ongoing|expired|ended_early|permanent_block|ended_unknown.
    404 unknown/deleted/non-numeric child; 400 bad before/end/from/to or any repeated param; 401 no session.

    SPA URL: /history?child=N&service=ID&end=STATUS&from=YYYY-MM-DD&to=YYYY-MM-DD (each optional, this order).
    The URL holds local calendar days; only the client turns them into instants.

Wave 1
  t-1  Add filtered cross-child history store read
       files:    internal/store/history.go, internal/store/history_test.go
       desc:     Add HistoryFilter{ChildID, ServiceID, End, From, To} and ListHistory(ctx, filter, before, limit), read in one tx.
                 historyGrants joins children on c.id = g.child_id AND g.id > c.history_after_grant_id and takes the filter;
                 ListGrantHistory passes a child-only filter. HistoryGrant gains ChildID/ChildName. The result also carries
                 Children and Services (union of grant_services and grant_service_removals on visible grants, newest snapshot name, else "").
       covers:   c-1, c-2, c-3, c-4, c-5, c-6, c-7, c-9
       contract: if ordering or child naming breaks, TestListHistory_AcrossChildren fails: interleaved grants for two children come back id DESC, each with its own ChildID and ChildName
                 if the cursor skips or repeats, TestListHistory_Paging fails: 120 grants across two children page 50/50/20 with HasMore true/true/false and no id twice, and a grant added between pages does not appear in or shift the older pages
                 if the children join or watermark is dropped, TestListHistory_DeletedAndReusedChild fails: a deleted child's grants are absent, a new child reusing that id lists only its own, and a service only the deleted child's grants carried is absent from Services
                 if the service filter reads only grant_services, TestListHistory_ServiceFilterIncludesTrimmed fails: a grant whose service sits only in grant_service_removals matches that service id, while a grant without it does not
                 if the date bounds are off by one, TestListHistory_DateBounds fails: started_at == From is included, == To is excluded, To-1s is included, and either bound alone works; ends_at and ended_at are ignored
                 if an end predicate ignores status, TestListHistory_EndFilter fails: ('active',NULL) is only ongoing; ('expired',NULL) and ('ended','expired') are only expired; ('ended',NULL) is only ended_unknown; a legacy ended row never matches ended_early or permanent_block
                 if a filter is applied after LIMIT, TestListHistory_FiltersCombineAcrossPages fails: 60 matches for child+service+end among 130 interleaved grants page 50 then 10, with no non-match and HasMore false on page 2
                 if an unknown child falls through to an empty list, TestListHistory_UnknownChild fails: ChildID of a never-created and of a deleted child returns ErrChildNotFound, and ChildID 0 returns all
                 if Services misses deleted custom services or picks a stale name, TestListHistory_Services fails: a deleted custom service with a snapshot is listed once, under its newest snapshot name when two grants differ
                 if the ListGrantHistory rewire changes behaviour, the existing TestListGrantHistory_* and internal/store/setup_test.go assertions fail unchanged

  t-3  Carry history filters in the URL; link overview
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/lib/Home.svelte, web/src/lib/Home.test.ts
       desc:     Extend RouteParams with service, end, from, to. Add a historyFilters store holding those four plus badChild (?child= present but not a valid id),
                 parsed at load and on popstate, set by navigate; historyChild keeps its meaning. Add a History button to the Home nav: navigate('history').
       covers:   c-1, c-8, c-9
       contract: if a filter is dropped from the URL, the navigate test fails: navigate('history', {child: 3, service: 'youtube', end: 'expired', from: '2026-10-01', to: '2026-10-31'}) pushes exactly '/history?child=3&service=youtube&end=expired&from=2026-10-01&to=2026-10-31'
                 if filters leak across routes, the same test fails: a later navigate('home') pushes '/' and clears historyFilters, and navigate('history', {child: 2}) pushes '/history?child=2' with no earlier service left over
                 if load parsing misses a filter or accepts garbage, the module-reload test fails: all four filters parse from the URL; end=bogus, from=2026-13-01 and from=2026-02-31 read null; a service id with & and a space round-trips
                 if popstate does not re-read filters, the popstate test fails: going back from ?service=b to ?service=a sets historyFilters.service to 'a'
                 if garbage collapses into "all children", the routeFor test fails: /history reads historyChild null with badChild false; ?child=abc, 0, -1 and 3.5 read historyChild null with badChild true
                 if the Home link is missing or scoped, the Home test fails: clicking the nav History button lands on /history with historyChild null, and the per-child 'History for Ada' link still lands on /history?child=1

Wave 2 (t-2 depends t-1; t-4 depends t-3)
  t-2  Serve GET /api/v1/history with filters
       files:    internal/api/history.go, internal/api/api.go, internal/api/history_test.go
       desc:     Add ListHistory to ChildStore, register the route behind requireSession, parse and validate the params. Extract the grant-to-view and name-resolution
                 code from handleHistory so both handlers share it; the overview view embeds historyGrantView and adds child. Resolve option names through the
                 same snapshot, catalogue, bare id chain and sort them by name. The per-child route's wire stays as it is.
       covers:   c-1, c-2, c-3, c-4, c-5, c-6, c-7, c-9
       contract: if the route or child field is wrong, TestHistoryOverview_GoldenKeys fails: page keys are exactly children, grants, next_before, services and grant keys are the per-child set plus child; TestHistory_GoldenKeys still passes unchanged
                 if the session guard is missing, TestHistoryOverview_Errors fails: no cookie is 401
                 if bad input is swallowed, the same test fails: end=bogus, from=yesterday, to=2026-10-01 (no time), before=0 and service=a&service=b are each 400; child=99, a deleted child and child=abc are each 404
                 if the SQL end predicates drift from endStatus, TestHistoryOverview_EndMatchesStatus fails: over grants seeded for every (status, end_reason) pair, each end value returns only grants whose wire status equals it, and the five results together hold every grant exactly once
                 if params are not wired through or paging loses them, TestHistoryOverview_FilteredPaging fails: 51 matches among other grants with child+service+from+to set give next_before on page 1, and page 2 with the same params plus before holds the one remaining match
                 if option names skip the fallback or a catalogue failure errors, TestHistoryOverview_ServiceNames fails: a legacy grant's service with no snapshot shows the catalogue name, shows its bare id with AdGuard down, and the response stays 200
                 if a page leaves out a deleted child's grants only by accident of the query, TestHistoryOverview_DeletedChild fails end to end: DELETE the child, create one reusing the id, and GET /api/v1/history lists only the new child's grants

  t-4  Build filter controls into the History screen
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/lib/History.svelte, web/src/lib/History.panel.test.ts, web/src/App.svelte, web/src/App.test.ts
       desc:     Replace fetchHistory with fetchHistory(query, before?) against /api/v1/history, turning local days into instants (from = local midnight, to = next local midnight
                 via new Date(y, m, d + 1)). History takes child and filters props, shows the child name on each grant, and renders child, service and end selects plus two date
                 inputs that call navigate; options persist across reloads and stay visible on an empty result, which reads 'No grants match these filters' when any filter is set.
                 badChild or a 404 shows 'No such child'. App passes the props, and refresh() no longer re-navigates on the history route, so a reload keeps the whole query.
       covers:   c-1, c-2, c-4, c-5, c-6, c-7, c-8, c-9
       contract: if the client mangles the query, the fetchHistory test fails: {} requests /api/v1/history; a full query plus before 40 sends child, service, end, from, to, before; a 404 rejects with ApiError status 404
                 if days are not the device's or the end day is dropped, the date test fails: with TZ stubbed to Europe/Stockholm, from 2026-03-29 and to 2026-03-29 send the local midnights 23 hours apart (the DST day), which a +86400s bound gets wrong
                 if the grant row loses its child or detail, the panel test fails: each li[data-grant] shows its child's name alongside the existing services, times, status and extensions assertions, with mixed children on one page
                 if load more drops filters, the panel test fails: with service=youtube set, clicking Load more fetches a URL with service=youtube and before=<next_before>, and appended rows keep their order
                 if a stale response lands, the panel test fails: a Load more response for the old filters arriving after a filter change never joins the list
                 if a control does not filter, the panel test fails: changing each of child, service, end, from and to pushes the matching URL and triggers exactly one fetch
                 if options come from the wrong place, the panel test fails: the service select lists page.services including an id no live catalogue has, and the child select lists page.children
                 if the controls vanish with the rows, the panel test fails: a filtered empty page shows 'No grants match these filters' and all controls, and an unfiltered empty page shows 'No grants yet'
                 if the child error paths regress, the panel test fails: a 404 for child=3 and badChild both show 'No such child' with Back, and badChild makes no fetch; a null child with badChild false fetches and titles 'History'
                 if a reload drops filters, the App test fails: starting at /history?child=3&service=youtube&end=expired leaves the URL unchanged after /me, with no pushState, and the page request carries all three
                 if garbage is rewritten on reload, the App test fails: /history?child=abc stays on that URL and shows 'No such child'
                 if back does not restore filters, the App test fails: filter to A, filter to B, fire popstate at A's URL, and the select shows A and the last request carries A

## Coverage

- c-1 → t-1 (cross-child read, child name), t-2 (route, child on the wire), t-3 (Home link), t-4 (screen shows child and detail)
- c-2 → t-1 (cursor over the cross-child set), t-2 (page wire), t-4 (load more)
- c-3 → t-1 (children join and watermark), t-2 (end to end)
- c-4 → t-1 (live plus removed match, service list), t-2 (param, option names), t-4 (service select)
- c-5 → t-1 (bounds on started_at), t-2 (param), t-4 (local day to instant, date inputs)
- c-6 → t-1 (end predicates), t-2 (parity with endStatus), t-4 (end select)
- c-7 → t-1 (filters before LIMIT), t-2 (filtered paging), t-4 (load more keeps filters)
- c-8 → t-3 (URL round-trip, popstate), t-4 (App reload fix, back restores)
- c-9 → t-1 (child filter, ErrChildNotFound), t-2 (404), t-3 (badChild, per-child link keeps working), t-4 (child select, 'No such child')

## Judgment calls

- Filter options ride on the page response (`children`, `services`), rejected `GET /history/services` plus `listChildren`: one fetch keeps the existing load, error and generation handling, with no second failure path or race.
- Keep `/api/v1/children/{id}/history` and `ListGrantHistory`, rejected deleting them: no criterion needs removal, and ~550 lines of existing tests stay as free regression cover. Cost: the web app no longer calls that route, so it is dead until someone removes it. Worth the judge's attention.
- No migration or new index, rejected a `started_at` index: rows are thousands per year (locked retention rationale) and the existing rowid-descending scan is enough.
- Paging stays on the grant-id cursor, rejected a `(started_at, id)` cursor: id order is already "newest first" and nothing deletes grants.
- Client sends `from`/`to` instants and the URL keeps calendar days, rejected server-side days plus a tz param: locked `date_range` follows the device zone, and only the client knows it.
- `?child=abc` stays 'No such child' through a `badChild` flag, rejected treating it as "all children": c-9 says "still shows", and showing every child's grants for a broken link is the worse failure.
- `App.refresh()` skips `navigate` on the history route, rejected passing every filter back through it: it re-pushed the URL from parsed values, which would erase `?child=abc`, and the URL is already right on that path.
- `historyChild` keeps its shape and a sibling `historyFilters` store is added, rejected one merged store: the existing Home, App and api tests keep asserting `historyChild` unchanged.
- End predicates live in store SQL and parity with `endStatus` is enforced by test, rejected filtering in Go after the read: post-filtering breaks the page size and `HasMore`.
- `services` is scoped to visible grants, rejected all grant rows: a service only a deleted child used would offer a filter with no results and expose that child's history.
- No "clear filters" button, no chips, no extra option for a hand-edited `?service=` missing from the list, and no debounce on the date inputs: none is traceable to a criterion.
- t-4 has 6 files, above the 5-file heuristic, accepted over a split: the client swap, screen and `App.test.ts` URL mocks must land together or the tree is red between tasks. It is one layer, three source files with their tests.
- t-1 and t-2 stay split though merging fits the lens: merged they hit 5 files across 2 layers, and wave parallelism with the web tasks is free this way.
- Known limit, not a task: a trim from before migration 0009 left no removal row, so those grants cannot match their trimmed service under c-4. Nothing recorded it.
