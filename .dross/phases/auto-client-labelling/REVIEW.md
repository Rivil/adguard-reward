# Plan Review — auto-client-labelling

Reviewed: 2026-11-01
Plan: 12 tasks across 5 waves

## BLOCKING
(none)

## FLAG
- [locked-decision] The plan matches `new_client_blocked_set` as revised during planning (the global-list fallback for a child's first device). That revision is an uncommitted edit to spec.toml (`git diff` shows the locked `choice`, `why` and c-7 text all changed). Against the committed locked decision, t-7 `TestCreateClient_FirstDevice` and t-11 `TestChildUpdate_FullSwap` contradict the spec: the committed text gives a first device an empty own list.
  Suggestion: get explicit user sign-off on the revised locked decision and commit spec.toml before execution, so the plan conforms to an approved lock rather than one the planner edited.

- [test-contract] A stale mapping held by the *same* child hits the `child_clients.client_name` PRIMARY KEY, and no task specifies or tests that case. `checkConflicts` excludes `selfID` (internal/store/children.go:224), so (a) t-2 `AddChildClient(Ada, "TV")` while Ada already holds a stale "TV" fails the INSERT with a raw constraint error. That happens *after* t-7 has already added TV in AdGuard, which leaves the client unmapped and returns a 500. (b) t-2 `RenameClient("Kid phone", "Ada phone")` while Ada holds a stale "Ada phone" fails the UPDATE. t-9 then compensates, so that rename can never succeed. t-2 specifies dedupe only for `grant_clients`, and t-10 only checks "another child's stale mapping".
  Suggestion: state the intended behaviour (treat it as already mapped, or 409 before any write) in t-2, t-9 and t-10, and add a contract line for each path.

- [test-contract] c-2 requires "AdGuard unreachable 502" on POST. t-10 tests that only through `Hang` on `/control/clients/add` (`TestCreateClient_Timeout`). When AdGuard is actually down, the first call to fail is t-7's pre-check `Clients()` read. t-7 tests that the error is returned (`TestCreateClient_ReadFails`), but no API-level test asserts that this error maps to 502 rather than 500. `TestClients_Unavailable` covers GET only.
  Suggestion: add a t-10 line that forces `/control/clients` to 500 (or closes the fake) during POST and expects 502 with zero adds.

- [test-contract] t-1's last line, "if the enlarged fixture breaks existing expectations, the existing adguard/api/grants suites fail under make test", is the "existing tests verify" pattern. It names no assertion that the new auto_clients rows (192.168.1.50, the leased-MAC IP, the blank hostname) could break.
  Suggestion: name the at-risk existing assertions (for example, any test that counts auto_clients or compares the full `/control/clients` body), or drop the line as a non-contract.

- [test-contract] t-8's `ClientNames.test rename` asserts Children-level DOM ("Ada's 'Ada phone' checkbox is checked", "no [data-missing] element"). That passes only if ClientNames.test mounts Children.svelte, and `Children.test.ts` is not in t-8's files. If the test mounts only ClientNames, the contract cannot be written as stated.
  Suggestion: either state that ClientNames.test mounts Children, or add `web/src/lib/Children.test.ts` to t-8's files and move that assertion there.

- [scope] project.toml `goals.non_goals[0]` says the app "touches only per-client blocked_services, a clearly-marked managed region of user_rules, and reads the query log". This phase creates and renames AdGuard persistent clients and reads `/control/dhcp/status` (c-2, c-6, `new_client_ids`). The plan implements the spec faithfully, so this is a project/spec drift, not a plan defect.
  Suggestion: update the non_goal wording in project.toml when the phase ships, so the stated boundary matches what the app does.

## NOTE
- [strength] The pinned wire contract at the top of plan.toml lets the SPA work (t-3, t-6, t-8) run from wave 1 alongside the backend. Within each wave the tasks' files are disjoint. The shared files (`internal/api/api.go`, `internal/grants/grants.go`, `web/src/lib/Children.svelte`) are serialised through `depends_on` rather than left to collide.
- [strength] The contracts test the *reason* for the locked decisions, not just their shape. `TestCreateClient_SurvivesExpiry` and `TestCreateClient_Race` (-race, 50 iterations) prove that a device added mid-grant keeps the granted service blocked after expiry. `TestRename_GrantExpiryReverts` and `TestRename_TimerSerialised` prove a renamed client still reverts. t-1's `TestFake_AddCountsInFlight` explicitly keeps t-5's serialisation test from being vacuous.
- [strength] Every contract line names its mutation and uses fixture-exact values (Kid tablet → [roblox, tiktok, youtube], 192.168.1.50 owned by "Kid phone"). Rollback is asserted as byte-identical tables or "store unchanged", not "error returned".
- [coverage] All 7 criteria are covered: c-1 (t-1, t-4, t-10), c-2 (t-1, t-5, t-10), c-3 (t-2, t-7, t-10), c-4 (t-3, t-6), c-5 (t-6), c-6 (t-1, t-2, t-3, t-5, t-8, t-9, t-12), c-7 (t-5, t-7, t-11).
- [coverage] t-4 counts a device as "covered" by canonical-IP match, CIDR containment or a leased MAC, which is wider than c-1's literal "IP is not already an id". This is consistent with the locked `unlabelled_listing` ("not covered by a persistent client"). The verifier should map c-1 to `TestUnlabelled_CIDR`, `TestUnlabelled_IPv6Canonical` and `TestUnlabelled_LeasedMAC`, not just the exact-IP case.
- [test-contract] t-11: if `AdoptClients` fails partway through several added clients, the clients already adopted keep their widened own list and `use_global_blocked_services=false`, while the store mapping is untouched. This errs toward more blocking, so it is safe, but it is not stated or tested.
- [test-contract] t-9's compensation covers only a returned store error. A process crash between the AdGuard rename and `Store.RenameClient` leaves the active grant holding the old name. `revert` counts `ErrClientNotFound` as reverted (internal/grants/grants.go:199), so that device would stay unblocked. Swapping the write order would not remove this risk, so it is inherent to two-system writes. Record it as a known residual risk, not a plan fix.
- [wave-order] t-4 and t-5 (wave 2) both write into package `internal/adguard`, and t-11 and t-12 (wave 5) both write into `internal/api`. If the executor runs same-wave tasks concurrently in one working tree, a half-written file from one task breaks the other's `go test` compile. The files are disjoint, so this matters only for concurrent execution.
- [antipattern] t-3 keeps `listClients()` "because Children.svelte calls it", but t-6 switches `Children.load` to `listDevices()`. After t-6, `listClients` has no production caller.

## Summary
The plan is sound and executable, with full criterion coverage, real dependencies and unusually specific contracts. Before starting, fix two things: get the user's sign-off on the planner-edited locked decision, and specify the same-child stale-mapping PK conflict in t-2, t-9 and t-10.
