# Plan Review — history-overview

Reviewed: 2026-11-05
Plan: 11 tasks across 4 waves

## BLOCKING
(none)

## FLAG
- [test-contract] t-1's "thin wrapper" over ListHistory silently changes `GET /api/v1/children/0/history` from 404 to 200 with every child's grants, and no contract catches it. t-1 defines "ChildID 0 returns all". `childID()` (internal/api/children.go:92) only rejects non-numeric ids, so `/children/0/history` reaches `ListGrantHistory(ctx, 0, ...)`, which today fails the children lookup and returns ErrChildNotFound. A wrapper that forwards childID now takes the "no filter" branch. The regression net does not cover this: TestHistory_Errors tests abc, 999 and a deleted child but never 0, and the store tests only try 999. The plan also promises this route stays byte-identical. A filter check written as `> 0` would leak the same way for -1.
  Suggestion: state in t-1 that ListGrantHistory keeps its own child lookup or guards id <= 0, and add a t-1 contract asserting ListGrantHistory(0) and (-1) return ErrChildNotFound.

- [antipatterns] t-1's description and its last contract pull against each other. "Each HistoryGrant gains ChildID and ChildName" plus "thin wrapper" produces populated fields on the per-child read, but TestListGrantHistory_Legacy compares `reflect.DeepEqual(g, want)` against a HistoryGrant with neither field set, and the contract says that test passes unedited. So the wrapper must blank both fields on every grant, or the shared read must skip them. Neither is stated, and the first obvious implementation goes red.
  Suggestion: say in t-1 how the per-child read keeps those fields zero.

- [antipatterns] t-6 never mentions the TypeScript shape change, and no task's file list can absorb it cleanly. The overview needs `HistoryPage.child` nullable and a per-grant `child`. Making `HistoryPage.child` nullable breaks `page.child.name` in History.svelte, which keeps using fetchHistory until t-9. A required `HistoryGrant.child` breaks the typed `grant()` fixtures in history.test.ts and History.panel.test.ts, and both are outside t-6's files (`make typecheck` covers tests: tsconfig.app.json includes src/**/*.ts). Separate overview types avoid both, but `appendPage(shown: HistoryGrant[], ...)` then returns grants without `child`, and fixing that means editing history.ts, which is in neither t-6's nor t-9's files.
  Suggestion: name the types in t-6 (new page and grant types, or an optional `child`) and add history.ts to whichever task needs appendPage generic.

- [test-contract] t-6's navigate contract `navigate('children', {service:'x'})` does not match the param shape used two lines earlier, `navigate('history', {child: 3, filters})`. `service` is not a key of RouteParams under that shape, so the literal call is an excess-property error in `make typecheck`. If cast through, the stray key is ignored whatever navigate does, so the assertion can never fail and the "a query leaks" surface goes untested.
  Suggestion: make it `navigate('children', {filters: {...}})`.

- [test-contract] t-6's badChild definition lets an oversized id through to the unfiltered overview. badChild is "?child present but not a positive whole number", but `childIdOf` also returns null for digits beyond MAX_SAFE_INTEGER. `?child=9007199254740993` is a positive whole number, so read literally it gives historyChild null and badChild false, and the parent sees every child's grants. Today it shows 'No such child', which c-9 requires for an unknown child. The garbage list (?child=, abc, 0, -1, 3.5, 1e2, %203) does not include it.
  Suggestion: define badChild as "?child present and childIdOf returned null" and add an unsafe integer to the garbage list.

- [test-contract] t-3's year guard only protects values read from the URL; values from the filter bar skip it. t-7 emits "YYYY-MM-DD unchanged" and t-6's navigate stores and pushes filters as given, so parseFilters runs only on boot and popstate. Picking to=9999-12-31 sends until=+010000-..., which t-3's own contract calls unparseable by Go, so the server answers 400 and the screen shows an error. Reloading that same URL drops the filter and shows an unfiltered list, so one URL behaves two ways, which is c-8's territory. Related and not verified in a browser here: Chromium appears to fire `change` per segment edit once a date is complete, so typing a year on desktop may emit 0002, 0020, 0202, 2026. If so, each is a pushState and a fetch, and t-7's jsdom test ("one change event emits once") cannot see it.
  Suggestion: state in t-6 that navigate normalises filters through the same validation as parseFilters, with a contract for an out-of-range date passed to navigate.

- [antipatterns] t-10 reuses "t-8's resolver" but does not list internal/api/history.go. Today `liveNames(r, h store.GrantHistory)` walks grants, so it cannot take a flat service list, and t-8's description only says name resolution is "extracted so both history handlers build grants one way". That extraction can satisfy t-8 and still not fit t-10, which has no grants to pass and no license to edit the file.
  Suggestion: require in t-8 that the resolver accepts service ids or names rather than a GrantHistory, or add history.go to t-10's files.

- [wave-order] t-5's dependency on t-1 is not backed by anything t-1 produces. ListHistoryServices lives in its own file and reads grant_services, grant_service_removals, grant_service_names, grants and children directly; the test helpers it needs already exist. The stated link is "the same visibility join as t-1", but t-1 does not say it exposes one. Either the fragment is shared and t-1 must name it, or t-5 copies it and could run in wave 1, leaving two copies of the c-3 predicate.
  Suggestion: have t-1 name a shared visibility fragment, or drop the edge and move t-5 to wave 1.

- [coverage] c-4 is narrowed by a comment in plan.toml, not in the spec. c-4 says results "include grants where a permanent block later trimmed that service", unqualified, while the plan header records an accepted gap for trims before migration 0009. The gap is real: 0009 added the removal log and did not backfill it. However, the acceptance comes from two panel drafts (synthesis, "Agreed limit"), not from a spec decision, so `dross verify` will mark c-4 against an unqualified criterion. Criterion coverage itself is complete.
  Suggestion: confirm the gap with the user and record it in the spec, so the limit is visible outside the plan.

## NOTE
- [coverage] All nine criteria appear in at least one task's `covers`. t-9 wires the service and status controls and tests them ("changing each of child, service, status, from and to"), yet it covers c-5 but not c-4 or c-6, so those two have no screen-level task in the verify mapping.
- [coverage] t-2 still carries an open question. The synthesis (Disagreement 5) says it only helps an install that deleted the highest-id child and added another before 0009, and "if none exists, drop t-2". The plan keeps t-2 without recording an answer. The migration itself checks out against existing fixtures: openAt0008 (created_at 100) and the created_at 0 seeds keep watermark 0, including the 'upgrade then create' test in children_test.go.
- [test-contract] t-6's "App refresh() navigates only login -> home" has a side effect no contract mentions. Boot currently normalises the URL on every route, so /nope becomes / and /?child=5 becomes /. After t-6 those stay in the address bar. Harmless, but no test pins either behaviour.
- [test-contract] The wire comment says any repeated param is 400, while t-8's TestHistoryOverview_Errors lists child, service, status, from and until repeated and omits before.
- [forbidden-actions] Checked against project rules r-01 to r-04: no violations. The catalogue stays a runtime /control read and nothing touches user_rules. /Users/rivil/.claude/dross/rules.toml does not exist, so no global rules apply. All commands go through make targets; runtime.mode is native.
- [strengths] The contracts are grounded in the repo. Every existing test the plan cites was found (TestListGrantHistory_Legacy, TestHistory_GoldenKeys, TestMigrate0009_Order, openAt0008, 'after switching child', 'each child has a History link to its own history'), and no two tasks in a wave share a file.
- [strengths] TestHistoryOverview_StatusMatchesWire ties t-4's SQL predicate to the Go endStatus across all 12 cells. That is the right guard for logic that now exists twice, and the schema's CHECKs do allow all 12 cells to be seeded.
- [strengths] The TZ pin has a positive control. If vite.config.ts stops applying under Stryker's threads, the getTimezoneOffset test fails rather than passing by accident on the host zone, and the Havana literals for the skipped and repeated midnights are correct for 2026-03-08 and 2026-11-01.

## Summary
Coverage is complete with no locked-decision or rule conflicts, so nothing blocks, but fix the t-1 child-0 regression and the t-6/t-10 file and type gaps before execution because each one fails or leaks at implementation time.
