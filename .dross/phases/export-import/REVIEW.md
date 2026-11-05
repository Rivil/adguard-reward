# Plan Review — export-import

Reviewed: 2026-11-05
Plan: 6 tasks across 4 waves

## BLOCKING
(none)

## FLAG
- [test-contract] The contracts do not stop t-6 placing adoption ahead of the catalogue check. TestImport_UnknownBuiltin only asserts "422 naming the id, no rows", and TestImport_BuiltinNameClash says "before any write", but both are written in t-5, when the handler makes no AdGuard write at all. TestImport_RejectedWhole does assert no AdGuard POST, but its rows all fail in parseSetupFile, before either check. TestImport_NonEmptyNoAdoption pins adoption after the emptiness check only, and t-6's description says just "before commit". An AdoptClients call placed after EnsureEmpty but before the catalogue read passes every listed test, yet it writes to AdGuard on a file that c-4 and locked unknown_builtin_service say must be rejected with no AdGuard change.
  Suggestion: In t-6, assert zero /control/clients/update requests for an unknown built-in and for a built-in name clash, using a file that maps a client AdGuard knows. Otherwise the fixture adopts nothing and the assertion cannot fail.

- [test-contract] t-2 has no case for absent or null top-level lists. DisallowUnknownFields rejects extra keys, not missing ones, so `{"format":"adguard-reward-export","version":1}` parses as an empty setup and returns 200 with nothing imported. A truncated or hand-stripped backup then reports success on an install that stays empty, which is the kind of malformed file c-4 covers. The reverse case is also unpinned: TestExport_Contents is seeded, so nothing checks that an empty install exports `[]` rather than `null`.
  Suggestion: Decide whether the four lists are required and add that to t-2's contract. If they are, add a t-4 case that an empty install's export parses back, so the export cannot produce a file its own import refuses.

- [test-contract] t-6's TestImport_RulesOutsideMarkersUntouched seeds lines "before and after the markers", but a real fresh install has no markers: syncRules writes nothing while there is nothing to manage. The fresh-install restore that c-5 describes therefore takes Splice's no-marker append branch, not the replace branch this test exercises. Splice's own unit tests make the risk low, but no import-level contract states this path.
  Suggestion: Run the same assertion with UserRules seeded and no markers, checking the parent's lines stay in place and the region is appended after them.

- [test-contract] t-3 and t-6 disagree about the 502 message. t-6 requires the 502 to name the failed client (via writeAdoptError), but messageFor in web/src/lib/api.ts replaces every adguard_unavailable message with "Can't reach AdGuard Home — try again in a moment". t-3 separately requires text saying nothing was imported and a retry is safe, which messageFor cannot produce. t-3 must therefore bypass messageFor, and it can satisfy its mocked test with a hard-coded string, so the client name tested in t-6 never reaches the parent. The existing Settings run() helper goes through messageFor, so copying it fails the contract.
  Suggestion: Say in t-3 whether the Backup section shows the server's 502 message or fixed text, and assert it. If the client name matters, the mock should return a named-client message and the test should expect that name on screen.

- [test-contract] t-4's TestRun_ExportNoSecrets lists svcPass and aiKey but not svcUser. c-2 says "AdGuard credentials", and config carries both a username and a password. TestExport_KeyAllowlist blocks a new wire field carrying it, but not the username appearing inside an allowed field's value, and c-2 asks for a value-level assertion on each seeded secret.
  Suggestion: Add the service username to the list. The constant is the short string "svc", so give this test a distinctive value or the assertion may match unrelated text.

- [test-contract] t-6 says "union of file clients" without giving an order. AdoptClients writes in slice order and stops at the first failure, and TestImport_AdGuardFailure needs Kid phone written before Kid tablet fails. The natural way to dedupe is a map, which makes that test flaky and makes the order of missing_clients unstable in the response.
  Suggestion: State the order in t-6 (first occurrence in the file, or sorted) and assert the missing_clients order with two missing names.

- [antipattern] The iOS standalone PWA download check exists only as a comment in plan.toml's header, and no task or contract carries it. project.toml names phone-based parents as the audience, so this is c-1's main path, and jsdom cannot test it. D-10 dropped Content-Disposition and t-3's lock test rules out a bare `<a href>`, so the plan has no fallback if the Blob download fails there. The failure would surface at verify, after all four waves, against a wave-1 task.
  Suggestion: Make it an explicit verify step with a named device and expected result, and run it as soon as t-4 lands. Record now what happens if it fails, so that isn't decided at verify.

## NOTE
- [strengths] The test contracts are specific and the ones I checked are accurate. Each line leads with the defect it catches, and the claims hold against the code: `dec.More()` returns false on a trailing `}`; 18446745874 wraps to about 1800 s inside the bounds, and the existing button handler's post-conversion check would accept it; child names fold ASCII only (NOCASE) while custom service names use ToLower.
- [strengths] The t-5/t-6 cut at the AdGuard boundary fits the code. reconcileToggles and reconcileGrants both skip clients on the global list, so adoption before commit is required for c-6, as TestImport_TogglesConverge states. Calling Grants.SyncRules directly is also correct, because resyncRules discards the error rules_applied needs.
- [strengths] The plan's assumptions about the code hold, so no file is missing from a task. Every ChildStore test double embeds the interface and main.go passes the store, so t-4's widening needs no other changes. adguardtest already has what the contracts use: SetUpdateStatus (0 clears it), SetResponse with a nil body to clear, and UserRules. The existing children and buttons suites do pin the watermark and NULL icon behaviour t-1's extraction could break.
- [adoption-budget] Import is the first path that adopts every client in one AdoptClients call, under one 4 s ApplyTimeout. Each inheriting client costs at least a global-list read and an update POST, so a large household on slow hardware could hit the deadline. The failure is safe: a 502, no rows, and a retry skips clients already adopted. No contract covers it, and none is needed unless it shows up in practice.
- [dangling-ref] TestSetup_DanglingRef guards a state normal use cannot produce: trigger service_toggles_custom_delete removes toggles and DeleteCustomService strips buttons and icons. An export 500 therefore means corrupted data, not a state a parent can reach and get stuck in.
- [clean-checks] All six criteria are covered, and no task contradicts a locked decision. No task reaches the granularity limits, and all files exist or are created by an earlier task. Each wave dependency is real, including t-5 on t-4 for the ChildStore widening and the round trip through export. No task breaks r-01 to r-04. ~/.claude/dross/rules.toml does not exist, so only project rules applied.

## Summary
The plan can proceed with no blocking issues: coverage and locked decisions hold, and the seven flags are contract gaps, the most important being t-6 adoption order against c-4 and the untested iOS download path.
