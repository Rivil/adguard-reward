# Plan Review — custom-services

Reviewed: 2026-11-02
Plan: 17 tasks across 4 waves

## BLOCKING
(none)

All nine criteria are covered, no task contradicts a locked decision, and no task implies a rules.toml violation (runtime is native and no global rules file exists).

## FLAG
- [locked-decisions / antipatterns] t-12 says "AdoptClients, CreateClient (with a child) and RenameClient call syncRules after their store write", but AdoptClients performs no store write. In `internal/api/children.go`, `handleChildCreate` and `handleChildUpdate` call `Grants.AdoptClients` *before* `Children.CreateChild` / `UpdateChild`, so a syncRules inside AdoptClients renders from the old mapping and the newly added device gets no `||d^$client=` line until the next reconcile tick (60s). In the other direction, `handleChildDelete` and removing a client in a PUT never call the engine, so a device that is no longer a child's (an adult's, under block_scope) stays blocked until the tick. No task lists `internal/api/children.go`, and t-12's contract only tests CreateClient and RenameClient. The tick converges this within 60s, so it is not BLOCKING, but the description sends the implementer to the wrong call site and the plan says "follow immediately" when that only holds for 2 of the 5 mapping-change paths.
  Suggestion: Correct t-12's description to say the resync for child create, update and delete must happen after the store write (either in the API handlers or through an engine method they call), add `internal/api/children.go` to t-12's files, and add contract rows for "a client added by PUT /children is blocked before the response" and "a client removed by PUT, or a child deleted, has no block line before the response". Otherwise state explicitly that these paths rely on the tick.

- [wave-order] t-13's `TestReconcile_OverdueCustom` (overdue grant on custom:1 → expired, no @@ left) goes through `expireLocked` → `revert`, which is exactly the path t-11 changes. Before t-11 lands, `revert` calls `AddBlockedServices(name, ["custom:1"])`. Against the fake that either fails, so the grant stays active and the test is red, or it succeeds by writing the custom id into blocked_services, which is the leak t-11 forbids. t-13 sits in wave 3 alongside t-11 with `depends_on = ["t-6"]` only, so the result depends on execution order. t-12's `TestRenameClient_ResyncsRules` ("mid-grant") has the same problem if the grant is created through `Engine.Create` rather than seeded in the store.
  Suggestion: Add t-11 to t-13's depends_on and move t-13 to wave 4 (t-17 already waits on both). Alternatively, move the overdue-custom row into t-11's contract. For t-12, say the grant is seeded directly in the store.

- [antipatterns] t-7's third contract ("a TestRun_ButtonsWired-style boot through run() fails…") needs a test in `cmd/adguard-reward/main_test.go`, but that file is not in t-7's files. It belongs to t-17, two waves later. As written, t-7 either writes outside its file list or ships main.go wiring with no test.
  Suggestion: Add `cmd/adguard-reward/main_test.go` to t-7's files (no wave conflict: t-17 is in wave 4), or move that contract row to t-17.

- [test-contract] c-3 says a parent can edit the service's *name* and domains, but no backend contract tests a name change. t-4's `TestCustomServices_Update` and t-14's `TestCustomServices_Update` exercise domains only, and t-14's `TestCustomServices_Conflicts` only lists POST cases. A PUT renaming to a built-in name ("YouTube"), a PUT renaming to another custom service's name, and a case-only self-rename ("portal" → "Portal") that must *not* 409 against its own `name_key` are all untested.
  Suggestion: Add a PUT-rename row to t-14 (and an Update-name row to t-4) covering the built-in clash, the custom clash and the self case-change.

- [test-contract] No task covers a request whose domains collide after normalisation, e.g. `["a.com", "A.com."]`. t-2 normalises both to `a.com`, and t-4's `UNIQUE(domain)` then fires against the row's own sibling. Depending on how the store maps the constraint error, that comes back as `ErrDomainTaken{Owner: <self>}` (a confusing 409) or as a 500.
  Suggestion: Pick a behaviour (dedupe silently, or 400 naming the duplicate) and pin it in t-14's contract.

- [test-contract] t-2 leaves an open question in its description ("verify whether $ also needs escaping against AdGuard's $client grammar"), and its contract pins only `' " , |`. Neither `$` nor a literal `\` in a client name has a row. Whatever the implementer decides about the open question will be untested, and a `,` or `$` that is quoted wrong lets a parent-chosen client name end or extend the modifier list.
  Suggestion: Settle the `$` and `\` question during planning (AdGuard documents escaping for quotes, commas and pipes only), then add rows to `TestQuoteClient` for both characters.

- [coverage] t-15 derives custom-service state from intent (stored services + grants), not from what is actually in user_rules. After a region write fails (`applied:false`), or while the region is malformed, the child's view says "blocked" when AdGuard is not blocking anything. That is the false "blocked" t-15 says it avoids on read failure. Built-ins in the same view report what AdGuard actually holds, so the two kinds of service now mean different things by "blocked".
  Suggestion: Either read user_rules in the blocked handler and derive state from the region that is actually there, or document in t-15 that custom state reflects intended state and add a contract row for the applied=false case.

- [test-contract] t-11 runs the region sync "BEFORE the status CAS" on End and expiry, but for a mixed grant (`[youtube, custom:1]`) it does not say whether the built-in revert or the region sync runs first. It also does not say what state remains when the second step fails: `TestGrant_CustomWriteFailure` only uses custom-only grants. Separately, a malformed region (a parent deleted the end marker) makes every End and expiry of a custom grant fail closed forever. The @@ exception stays, so the child keeps access indefinitely, and the only signal is an error log on every tick. Nothing in the UI reveals it (`applied:false` appears only on CRUD responses).
  Suggestion: Pin the mixed-grant ordering and the partial state in a t-11 row. Decide whether a persistently malformed region needs a user-visible signal; if it does, record it as deferred rather than leaving it implicit.

## NOTE
- [strengths] The test contracts are unusually sharp. Each one names the test, the concrete input, the exact expected bytes and the mutation it catches (e.g. t-1's substring-marker row, t-2's shared-cover ref-count row, t-6's "an empty region would unblock every kid" read-failure row).
- [strengths] The sequencing for safety is deliberate and correct. t-16 waits for t-11 so a `custom:` id can never reach the unpatched blocked_services path through the API, and t-12 names a concrete existing leak (`baselineFrom` appending `g.Services` of active grants) instead of a generic "check adoption".
- [strengths] The wave graph is tight. Except for the t-13 gap above, every wave-N+1 task depends on a wave-N task, nothing could drop a wave, and no two tasks in the same wave share a file.
- [antipatterns] After t-13 drops the zero-grants early return, every install's first tick will append an empty `! adguard-reward begin` / `! adguard-reward end` pair to user_rules, even if it never creates a custom service. c-9 permits this ("a missing region is appended"), but it is a visible change in every user's AdGuard UI on upgrade, so the author should make that choice knowingly.
- [locked-decisions] `management_ui` says "a 'Services' section of Settings next to Buttons", but the SPA has no Settings page. Buttons is a top-level route reached from Home's header. The plan adds a `/services` route and a "Services" header button next to "Buttons" (t-9, t-10), which is the closest reasonable reading. The decision's rationale ("keeps the phone home screen unchanged") is slightly strained by the extra header button.
- [wave-order] Wave 3 runs t-11, t-12 and t-13 in parallel in the same Go package (`internal/grants`). Their files don't overlap, but they will probably all extend the shared test fixtures (fake AdGuard user_rules seeding, custom-service store setup). Expect merge friction in test helpers if these run as parallel writers.

## Summary
The plan is sound and well-sequenced, with exceptionally specific contracts and full criterion coverage. Before execution, fix t-12's wrong call site for AdoptClients (and the uncovered child-API mapping paths) and t-13's missing dependency on t-11.
