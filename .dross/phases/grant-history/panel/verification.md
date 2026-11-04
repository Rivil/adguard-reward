# grant-history — verification-lens draft

Lens: every task below was derived backward from the test contract written for
it first. Where the ideal contract crossed a layer boundary (e.g. "rename a
custom service after the grant, history still shows the old name"), the task
was placed after the layer that makes that contract observable, rather than
settling for a weaker contract that could run earlier.

Wire contract every task builds against (fixed here so web and API can be
drafted in parallel):

```
GET /api/v1/children/{id}/history[?before=<grant id>]      -> 200
{
  "child":  {"id": 3, "name": "Ada"},
  "grants": [                                   // newest first: started_at DESC, id DESC; max 50
    {
      "id": 41,
      "status": "active" | "expired" | "ended",
      "end_reason": null | "expired" | "ended_early" | "permanent_block" | "unknown",
      "started_at": "RFC3339", "ends_at": "RFC3339", "ended_at": "RFC3339" | null,
      "services": [{"id": "youtube", "name": "YouTube",
                    "removed_at": "RFC3339" | null, "removed_reason": "permanent_block" | null}],
      "extensions": [{"at": "RFC3339", "old_ends_at": "RFC3339", "new_ends_at": "RFC3339"}]  // at ASC
    }
  ],
  "next": "<grant id>" | null                   // cursor for "load more"
}
unknown / deleted child -> 404 not_found; non-numeric before -> 400 bad_request
```

Schema (0009_grant_history.sql): `grants.end_reason TEXT NULL CHECK IN
('expired','ended_early','permanent_block')`; `grant_services.name TEXT NULL`,
`grant_services.removed_at INTEGER NULL`, `grant_services.removed_reason TEXT
NULL CHECK IN ('permanent_block')`; new `grant_extensions (id PK, grant_id FK
grants ON DELETE CASCADE, at, old_ends_at, new_ends_at)`; index
`grants_child_history_idx ON grants (child_id, started_at DESC, id DESC)`.
Legacy rows keep NULL everywhere — nothing is backfilled.

---

```
Phase grant-history — 10 tasks across 4 waves

Wave 1
  t-1  Add grant-history schema and removed-row filter
       files:    internal/store/migrations/0009_grant_history.sql, internal/store/store_test.go,
                 internal/store/grants.go, internal/store/grants_test.go
       covers:   c-3, c-6
       contract: - TestMigrate_0009PreservesGrants: a DB built with migrationsThrough(0008) holding one
                   'expired' and one 'ended' grant (services, clients, started/ends/ended_at) reopens with
                   every status, time and service row byte-identical, and end_reason / name / removed_at
                   all NULL — if the migration backfills an end_reason (violating legacy_end_reason) or
                   rebuilds grant_services and drops rows, it fails; 0009 must apply directly after 0008
                 - inserting end_reason='bogus' or removed_reason='bogus' is rejected by the CHECKs
                 - a grant_services row with removed_at set (seeded raw) is absent from GetGrant and
                   ListActiveGrants Services — if loadGrants stops filtering removed rows, the reconciler
                   would strip a permanently blocked service again and this test fails
       depends:  —

  t-2  Add history route, client and outcome labels
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/lib/history.ts,
                 web/src/lib/history.test.ts
       covers:   c-4
       contract: - navigate('history', {child: 3}) pushes '/history?child=3' and sets route 'history';
                   routeFor('/history') is 'history'; historyChild() returns 3 for '?child=3' and null for
                   '?child=x' / missing
                 - fetchHistory(3) GETs /api/v1/children/3/history; fetchHistory(3, '17') appends
                   ?before=17
                 - outcomeLabel: active → 'Ongoing'; expired → 'Expired'; ended+ended_early →
                   'Ended early'; ended+permanent_block → 'Ended by permanent block'; ended+unknown →
                   'Ended (reason unknown)' — a mutation that folds unknown into 'Ended early' fails
                 - endTimeOf(grant) returns ended_at for finished grants and null (rendered "ongoing")
                   for an active one, never the scheduled ends_at
       depends:  —

Wave 2
  t-3  Persist end reasons, extensions and name snapshots
       files:    internal/store/grants.go, internal/store/grants_test.go, internal/grants/grants.go
       covers:   c-1, c-2
       contract: - SetGrantStatus(active→expired, at) leaves end_reason='expired', ended_at=at;
                   (active→ended) leaves 'ended_early' — swapping the derived reasons fails both
                 - ExtendGrant(id, t1, at1) then ExtendGrant(id, t2, at2) leaves exactly two
                   grant_extensions rows (at1, E0→t1), (at2, t1→t2) — an upsert keeping only the latest,
                   or reading old_ends_at after the UPDATE (old==new), fails
                 - ExtendGrant on an ended grant returns ErrGrantNotFound and writes zero extension rows
                   (insert rolls back with the guard)
                 - CreateNamedGrant(..., names{"youtube":"YouTube"}) stores grant_services.name='YouTube';
                   CreateGrant (nil names) stores NULL; a later UpdateCustomService rename leaves the stored
                   name unchanged
                 - engine Extend passes e.now() as `at` (one-line call-site; covered end-to-end in t-7)
       depends:  t-1

  t-4  Keep permanently-blocked services as removed rows
       files:    internal/store/toggles.go, internal/store/toggles_test.go, internal/store/custom_services.go,
                 internal/grants/toggle_test.go
       covers:   c-1, c-3
       contract: - blocking 'tiktok' on a live grant {tiktok, youtube}: the tiktok row survives with
                   active=0, removed_at=at, removed_reason='permanent_block'; the grant stays active and
                   GetGrant.Services == [youtube] — reverting trimGrant to DELETE fails the survival
                   assertion
                 - blocking the last service ends the grant with end_reason='permanent_block', ended_at=at,
                   and that row is marked removed too — if the "left" count includes removed rows the grant
                   never ends and this fails
                 - after a trim, DeleteCustomService of the trimmed custom:N on the still-live grant
                   succeeds, not *ErrServiceInUse (custom_services.go in-use query must skip removed rows)
                 - after trim + unblock toggle, a new grant for the same (child, service) is not an overlap
                 - engine: Toggle(block, tiktok) on a live two-service grant, then Reconcile — the client's
                   blocked_services still contains tiktok (drift repair did not strip it)
       depends:  t-1

  t-5  Add paged per-child history store read
       files:    internal/store/history.go (new), internal/store/history_test.go (new)
       covers:   c-4, c-5, c-6
       contract: - 51 grants for child 1 → ListGrantHistory(1, 0, 50) returns 50, newest first, with
                   Next set; ListGrantHistory(1, Next, 50) returns the remaining 1 with Next=0 — seeded with
                   three grants sharing one started_at across the page boundary, so ordering by started_at
                   alone (dup or gap) fails
                 - child 2's grants never appear in child 1's history
                 - a removed service is listed with RemovedAt/RemovedReason; a live grant has zero EndedAt
                   and EndReason ""; extensions come back at ASC; name NULL reads as Name "" with
                   Snapshot=false (resolution is the API's job)
                 - a legacy row seeded with status 'ended' and end_reason NULL reads EndReason "" (no
                   inference from status)
                 - rows for a child id with no children row are still returned (deleted_child_history keeps
                   rows; reachability is the API's 404)
                 - store Close + Open on the same dir returns an identical history (c-5 survives restart)
       depends:  t-1

  t-6  Build History screen component
       files:    web/src/lib/History.svelte (new), web/src/lib/History.test.ts (new)
       covers:   c-4
       contract: - given a mocked page of [active, expired, ended_early, permanent_block, unknown] grants
                   each row shows its service names, start time, end time ("ongoing" for active) and the
                   t-2 outcome label
                 - a grant with two extensions renders two extension entries with old → new end times
                 - a service with removed_at renders marked "removed — permanent block" with its time,
                   still inside the grant's service list
                 - 'Load more' renders only when next != null; clicking it fetches ?before=<next> and
                   APPENDS (first page still visible) — replacing instead of appending fails
                 - the back button calls onBack; a 404 shows "No such child"; a network error shows an
                   alert, not an empty list; zero grants shows "No grants yet"
       depends:  t-2

Wave 3
  t-7  Record grant outcomes end-to-end in the engine
       files:    internal/grants/grants.go, internal/grants/grants_test.go, internal/grants/reconcile_test.go
       covers:   c-1, c-2, c-5
       contract: - Create → clk +10m → End: history reason ended_early, ended_at == clk.Now() at End
                 - Create → clk.Advance past ends_at (timer fires): reason expired, ended_at == clock at fire
                 - Extend ×2 at different clock times: two extensions whose `at` equal those clock times —
                   passing ends_at or a zero time as `at` fails
                 - restart: create on store dir D, Close engine + store, reopen D with a new engine whose
                   clock is past ends_at, Start → history reason expired, ended_at == restart clock
                 - End that fails the revert (ErrRevertFailed) leaves end_reason NULL and the grant active
                 - CreateNamed(ctx, child, svcs, names, clients, d) stores the names; Create(...) delegates
                   with nil names (existing callers untouched)
       depends:  t-3, t-5

  t-8  Serve per-child grant history endpoint
       files:    internal/api/history.go (new), internal/api/history_test.go (new), internal/api/api.go,
                 cmd/adguard-reward/main.go
       covers:   c-4, c-6
       contract: - real store+engine: grant extended twice then ended → JSON has end_reason 'ended_early',
                   two extensions, ended_at set; a live grant → status 'active', end_reason null,
                   ended_at null
                 - permanent-block trim of one service → that service listed with removed_reason
                   'permanent_block' and removed_at
                 - legacy rows (end_reason NULL): status 'expired' → end_reason 'expired'; status 'ended' →
                   'unknown' — mapping NULL+ended to 'ended_early' fails (locked legacy_end_reason)
                 - legacy service with no snapshot resolves to the current catalogue name ('YouTube'); a
                   deleted custom:9 resolves to the bare id 'custom:9'; with AdGuard's catalogue down the
                   response is still 200 with custom names resolved and built-ins as bare ids (never 502)
                 - 51 grants → 50 + next; following ?before=next returns 1 + next null
                 - deleted child → 404; ?before=abc → 400; no session → 401
                 - main wires Deps.History (main_test / build compiles with the real store)
       depends:  t-3, t-4, t-5

  t-9  Wire History route and per-child link
       files:    web/src/App.svelte, web/src/App.test.ts, web/src/lib/Home.svelte, web/src/lib/Home.test.ts
       covers:   c-4
       contract: - a reload on /history?child=3 renders History for child 3 (stays, does not bounce home)
                 - each child section on Home has a 'History' control; clicking it pushes
                   '/history?child=<that child's id>' — a link wired to the wrong child id fails
                 - History's back returns to '/' (home); /history with no child param shows
                   "No such child" with a back button
       depends:  t-6

Wave 4
  t-10 Snapshot catalogue names when a grant starts
       files:    internal/api/grants.go, internal/api/api.go, internal/api/grants_test.go
       covers:   c-4
       contract: - POST /grants for custom:1 named 'Roblox', then PUT custom-services/1 renaming it 'Games',
                   then GET history → service name 'Roblox' — resolving names at read time fails
                 - built-in: create with the fake catalogue naming 'youtube' 'YouTube', change the fake
                   catalogue name, GET history → still 'YouTube'
                 - DELETE custom:1 after the grant ended → history still shows 'Roblox', not 'custom:1'
                 - the Grants interface's Create is replaced by CreateNamed; the 409 overlap and 422
                   unknown-service paths are unchanged (existing grants_test cases still assert them)
       depends:  t-7, t-8
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 (end status + actual end time) | t-3 (store reasons), t-4 (permanent_block), t-7 (engine end-to-end), t-2/t-6 (labels) |
| c-2 (every extension recorded) | t-3 (extension log), t-7 (clock-accurate `at`), t-8 (wire), t-6 (two entries rendered) |
| c-3 (trimmed service kept, marked removed) | t-1 (removed filter), t-4 (trim keeps row), t-8 (wire), t-6 (render) |
| c-4 (per-child history view) | t-2, t-5, t-6, t-8, t-9, t-10 |
| c-5 (survives restart; overdue → expired at startup) | t-5 (reopen), t-7 (restart + Start) |
| c-6 (pre-upgrade grants appear) | t-1 (migration preserves rows, no backfill), t-5 (legacy read), t-8 (legacy reason + name fallback) |

All 6/6 criteria covered.

## Judgment calls

- **Reason derived from `to` inside SetGrantStatus** (expired→'expired', ended→'ended_early') rather than a new `reason` parameter: keeps the engine's Store interface and every SetGrantStatus call site unchanged; the only other "ended" writer (trimGrant) writes 'permanent_block' itself. Rejected an explicit param because t-3's contract already pins the mapping and the blast radius is ~20 test sites.
- **`end_reason` NULL for legacy rows, mapped at the API** ('expired' / 'unknown') rather than backfilling in the migration: locked legacy_end_reason forbids inventing reasons, and t-1's contract asserts NULL survives so a backfill can't slip in.
- **Trimmed rows kept with `active=0, removed_at, removed_reason`** rather than a separate removed-services table: one table means history and the live set share a PK; the cost is that every reader of grant_services must filter, so t-1 (loadGrants) and t-4 (count, DeleteCustomService in-use check) each carry an explicit contract for the filter — the DeleteCustomService regression was found by writing t-4's contract first.
- **Last service trimmed is also marked removed** (not just the grant ended): uniform rows; rejected leaving it unmarked because the UI would then need a special case to explain why one service has no removal.
- **Actual end time = when the revert landed (`ended_at`)**, including startup-pass expiries (restart clock, not scheduled ends_at): c-1 says "actual end time"; rejected using ends_at for expired grants because a grant overdue during downtime really stayed unblocked until the startup pass.
- **Name snapshot via new `CreateNamedGrant` / `CreateNamed`, with the old names delegating** rather than changing CreateGrant/Create signatures: ~18 store and ~13 engine test call sites stay untouched; API passes the catalogue it already reads in handleGrantCreate, so Create gains no AdGuard call.
- **Names resolved for legacy rows at read time in the API, not snapshotted by the migration**: the migration can't call AdGuard, and the locked decision says legacy rows resolve to the *current* name.
- **Catalogue failure degrades history to bare ids, never 502**: history is a look-back screen; blanking it because AdGuard is down would be worse than showing ids.
- **Keyset cursor `before=<grant id>` on (started_at DESC, id DESC)** rather than offset: no dup/gap when grants are created while paging; t-5's equal-started_at seed is there specifically to kill an order-by-started_at-only mutation.
- **Deleted child → 404** rather than serving its rows: locked deleted_child_history makes it unreachable from UI and the screen needs the child name; rows still exist (t-5 asserts it at store level).
- **t-10 placed in wave 4** rather than folded into t-8: its ideal contract (rename after grant → history keeps old name) is only observable through the history endpoint and engine CreateNamed, so it depends on both; a store-level name check would be a weaker contract that misses the API forgetting to pass names.
- **Web split into pure lib (t-2) → component (t-6) → wiring (t-9)** so outcome mapping is mutation-testable by stryker in isolation and the web track runs in parallel with the whole Go track from wave 1.
