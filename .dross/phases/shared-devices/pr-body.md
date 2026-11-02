# shared-devices — Shared devices

## Acceptance criteria

| ID | Criterion | Status | Tests |
|---|---|---|---|
| `c-1` | A persistent client can be assigned to more than one child: PUT /api/v1/children/{id} with a client another child already owns succeeds (no 409) and both children list it | covered | internal/api/children_test.go:TestChildUpdate_ShareNo409, internal/store/children_test.go:TestUpdateChild_SharedClient, internal/api/shared_test.go:TestShared_Lifecycle |
| `c-2` | On the children settings page a client owned by another child is enabled (not disabled) and labelled 'shared with <names>'; ticking it shares the client | covered | web/src/lib/Children.test.ts:sharedEnabled, web/src/lib/Children.test.ts:sharedThree, web/src/lib/Children.test.ts:shareByCheckbox |
| `c-3` | Sharing a client with a further child merges that child's baseline into the client's AdGuard list (set-union, nothing removed), but never re-blocks a service that another owner's active grant has unblocked on that client | covered | internal/grants/clients_test.go:TestAdopt_SharedKeepsOtherGrant, internal/grants/clients_test.go:TestAdopt_TwoAdded, internal/api/shared_test.go:TestShared_ShareDuringGrant, internal/adguard/clients_write_test.go:TestAdopt_Superset |
| `c-4` | A grant unblocks its services on every client the child owns, shared ones included; on expiry, End, or a reconcile revert, a service is re-blocked on a shared client only when no other active grant still covers that service on that client | covered | internal/api/grants_test.go:TestGrants_SharedApplied, internal/grants/grants_test.go:TestEngine_SharedEnd, internal/grants/grants_test.go:TestEngine_SharedExpiryKeepsOther, internal/grants/reconcile_test.go:TestReconcile_OverdueSharedKeepsOther, internal/grants/reconcile_test.go:TestReconcile_BothOverdueShared, internal/grants/shared_test.go:TestRevertPlan* |
| `c-5` | The grant's 201 response and the phone UI (tap result and active-grant row) name every shared device the grant touches and that device's other owners | covered | internal/api/grants_test.go:TestGrants_CreateShared, internal/api/grants_test.go:TestGrants_SharedThreeOwners, web/src/lib/Home.test.ts:sharedNotice, web/src/lib/ActiveGrants.panel.test.ts:sharedRow, web/src/lib/grants.test.ts:sharedText |
| `c-6` | Each owner's blocked view lists the shared device; a service unblocked there by another child's active grant is shown as unblocked by that grant, naming the granting child | covered | internal/api/blocked_test.go:TestBlocked_SharedUnblockedBy, internal/blocked/blocked_test.go:TestCompute_UnblockedByOther, internal/blocked/blocked_test.go:TestCompute_SharedWith, web/src/lib/Home.test.ts:unblockedBy |
| `c-7` | Renaming a shared client, removing it from one child, or deleting one of its owners keeps it assigned to every remaining owner; only the rename writes to AdGuard | covered | internal/store/clients_test.go:TestRenameClient_Shared, internal/grants/rename_test.go:TestEngineRename_Shared, internal/api/children_test.go:TestChildUpdate_RemoveSharedNoAdGuard, internal/api/children_test.go:TestChildDelete_SharedNoAdGuard |
| `c-8` | A button or grant-form selection whose child owns a shared device shows a passive hint naming it and its other owners before tapping; the tap still creates the grant with no confirmation step | covered | web/src/lib/Home.test.ts:sharedHint, web/src/lib/Home.test.ts:noConfirm, web/src/lib/GrantForm.test.ts:hintFollowsChild, web/src/lib/GrantForm.test.ts:hintPassive, web/src/lib/Buttons.test.ts:sharedHint |

## Decisions locked

- **shared_device_rule** 🔒 — Ref-counted unlock: any owner's grant unblocks the service on a shared device; it is re-blocked only when no active grant (of any owner) still covers that service on that device *(Keeps the common reward ('Netflix on the family TV') possible without a revert from one child's grant cutting short another's)*
- **shared_notice** 🔒 — Passive pre-tap hint on buttons and the grant form plus the post-tap notice; no confirmation dialog *(Satisfies 'never silently unblocks for another child' while keeping a reward to one tap (core value))*
- **grant_coverage_basis** 🔒 — A grant covers a service on the clients stored on the grant at creation (renames followed), not on the child's current ownership; a grant whose child is later unassigned from or deleted off a shared client still holds the service open there until it expires or is ended *(Matches how revert already targets a grant's stored clients; resolves how shared_device_rule's 'active grant (of any owner)' is read without a store lookup on every revert (plan review 2026-11-02); confirmed by the user and locked 2026-11-02)*

## Efficacy

- Mutation score: **93.5%** (869 killed / 60 survived)
- Criteria coverage: **8/8** covered, 0 uncovered
- Verdict: **PASS**

## Findings

- **NOTE**: skipped internal/store/migrations/0005_shared_clients.sql: no mutation adapter for .sql
- **NOTE**: skipped web/package.json: no mutation adapter for .json
- **NOTE**: mutation scope is degraded (source: git+changes) — stryker: AST unavailable for web/src/lib/ActiveGrants.svelte (no parser for .svelte); mutating the whole file; stryker: AST unavailable for web/src/lib/Children.svelte (no parser for .svelte); mutating the whole file; stryker: AST unavailable for web/src/lib/ClientNames.svelte (no parser for .svelte); mutating the whole file; stryker: AST unavailable for web/src/lib/GrantForm.svelte (no parser for .svelte); mutating the whole file; stryker: AST unavailable for web/src/lib/Home.svelte (no parser for .svelte); mutating the whole file
- **NOTE**: 12 in-diff stryker survivors accepted as equivalent or unreachable (component-side ?? [] fallbacks that api.ts already normalises; guards whose forced value yields the same output); 2 gremlins survivors at internal/api/children.go:286-287 killed by quick 0.1.8.2 (TestChildUpdate_AdoptStoreReadFails); 3 stale store/clients.go acceptances retired
- **NOTE**: Stryker concurrency lowered to 1 (quick 0.1.8.1) after the vite deps-cache ENOTEMPTY race failed the first TS leg at concurrency 2
- **NOTE**: combination gaps, not criterion surfaces: no API test grants across two shared devices (c-5); rename of a shared client under two active grants untested (c-4/c-7); expiry and reconcile ref-counting pinned at engine level only, End also over HTTP (c-4)

## Reviewer findings

- `t-1` — pass after 1 round
  - **FLAG** quality `c-7` (round 1): internal/store/clients.go clientOwner still assumes a client has one owner (QueryRow, arbitrary row). Now that 0005 allows several owners, RenameClient's 'same owner' branch runs DELETE FROM child_clients WHERE client_name = oldName, which drops the old name for every owner, not just that child; AddChildClient can return *ErrClientTaken for a child that already holds a shared client. Presumably a sibling task's job (c-7 rename), but these paths are now reachable with shared data. — _non-blocking, left; fixed by t-7_
  - **NOTE** quality `c-1` (round 1): PUT /children/{id} still runs updateConflict in internal/api/children.go, which returns *store.ErrClientTaken for a client another child holds, so the API-level PUT half of c-1 still returns 409. This task's record claims only the store-level UpdateChild and the POST path, so this is left to a sibling task. — _non-blocking, left; fixed by t-8_
  - **NOTE** quality (round 1): The 'index on client_name' is the composite primary key's autoindex, which leads with client_name, rather than a separate index; TestSchema_ChildClientsPK pins that column order. — _non-blocking, left_
  - **NOTE** quality (round 1): TestChildren_UpdateRollsBack now fails on the name check, which happens before any write, so it shows a rejected update leaves the child unchanged rather than exercising a mid-transaction rollback. The old client-check version had the same limitation. — _non-blocking, left_
- `t-2` — pass after 1 round
  - **NOTE** quality `c-4` (round 1): TestRevertPlan_StoredClients is close to vacuous: revertPlan takes no ownership input, so the function signature already enforces grant_coverage_basis. The test only confirms g.Clients is used as given, which is fine but adds little protection. — _non-blocking, left_
  - **NOTE** quality (round 1): heldOpen uses exclude=0 to mean 'count every grant', which relies on grant ids starting at 1. This is documented in a comment and is safe as long as that holds. — _non-blocking, left_
- `t-3` — pass after 1 round
  - **NOTE** quality (round 1): TestCompute_UnblockedByOrder goes beyond the contract and pins the (client, grant id) sort order across several clients and grants — _non-blocking, left_
  - **NOTE** quality (round 1): ChildRef and Unblock have JSON tags while ClientState and ServiceState still have none; the casing that reaches the API will depend on how the handler serializes these types — _non-blocking, left_
- `t-4` — pass after 1 round
  - **FLAG** quality `c-5` (round 1): The new array fields (ClientView.children, Grant.shared, GrantCreated.shared, BlockedClient.shared_with, ServiceState.unblocked_by) are declared optional, even though every fetcher fills them. Code that reads them later will need `?? []` or `!`, which weakens the 'never undefined' guarantee at the type level. Separate wire types from the returned types, or make the fields required, so they are typed as always present. — _non-blocking, left_
  - **FLAG** quality (round 1): web/src/lib/activeGrants.test.ts is changed (adds `shared: []` to its grant fixture) but is not in the task's file list. The change is harmless; the field is optional, so the edit is not even needed for type-checking. — _non-blocking, left_
  - **NOTE** quality `c-8` (round 1): sharedDevices sorts client names with the default Array.prototype.sort (UTF-16 code-unit order). That matches a byte-order sort on the server for ASCII names but may differ for non-ASCII names; acceptable for a 'client ASC' contract. — _non-blocking, left_
- `t-5` — pass after 1 round
  - **NOTE** quality (round 1): Reconcile now does one ListActiveGrants per overdue grant (N+1 store reads in a pass with many overdue grants); acceptable for correctness since each revert must see the statuses the previous one committed — _non-blocking, left_
  - **NOTE** quality `c-4` (round 1): TestEngine_ExpiredGrantNoCover mostly exercises the store's active-status filter rather than revert logic, since ListActiveGrants already excludes ended rows; still a valid guard against a future snapshot or all-grants read — _non-blocking, left_
- `t-6` — pass after 1 round
  - **NOTE** quality `c-3` (round 1): TestBaseline_SharedSiblingGrant checks the fake's stored list for Ben tv instead of the add request body; for a fresh client the two are the same, so the contract line is still enforced — _non-blocking, left_
  - **NOTE** quality (round 1): without() relies on heldOpen returning a sorted, de-duplicated slice (it does, in shared.go); a future change to heldOpen's ordering would quietly break the binary search — _non-blocking, left_
- `t-7` — pass after 1 round
  - **NOTE** quality `c-7` (round 1): With several outsiders holding newName, store.RenameClient names the lowest child id while checkRename names the first in ListChildren order; if those orders differ the two layers could name different children (only the engine's error reaches the caller, since checkRename rejects first) — _non-blocking, left_
  - **NOTE** quality `c-7` (round 1): TestRenameClient_SharedSelfStale would fail on the old delete that removed the old name from every owner, and TestAddChildClient_SharedSelf would fail on the old first-owner-row read, so both tests guard the regressions their test_contract lines describe — _non-blocking, left_
- `t-8` — pass after 1 round
  - **FLAG** quality (round 1): Undeclared files: blocked_test.go, clients_test.go and grants_test.go are not in the task's files list. The edits are a mechanical switch from createOK to seedChild, needed because POST now writes the client's blocked-services list to AdGuard. — _non-blocking, left_
  - **NOTE** quality (round 1): In handleChildCreate, a name race lost at CreateChild (store ErrNameTaken backstop) happens after AdoptClients has already written the baseline to AdGuard, so the client's list can grow with no child created. The description accepts this as the race backstop, and the write only adds services. — _non-blocking, left_
  - **NOTE** quality (round 1): Many existing children and migration tests now seed children straight into the store, so only the new create tests and TestChildren_NoAdGuard (now client-less) still exercise POST. — _non-blocking, left_
  - **NOTE** quality (round 1): adguardSince leaves out /control/login when counting 'zero AdGuard requests'. That is harmless, since a login only happens alongside another request. — _non-blocking, left_
- `t-9` — pass after 1 round
  - **FLAG** quality `c-2` (round 1): The API response now uses children[] instead of child, but the web frontend still reads child (web/src/lib/api.ts:186 type, Children.svelte:131, ClientNames.svelte:46, api.test.ts:561). Until the frontend is updated, the settings and client-name views will misread GET /clients and the rename response. — _non-blocking, left; fixed by t-16_
  - **NOTE** quality (round 1): TestClientCreate_StaleShared also covers a stale name held by two children, so any holder other than child_id conflicts, not just the last one listed. — _non-blocking, left_
  - **NOTE** quality (round 1): TestRename_OwnersReadFails calls handleClientRename directly with stub dependencies and skips the session middleware. That is fine for checking the response, but it does not test routing. — _non-blocking, left_
- `t-10` — pass after 1 round
  - **NOTE** quality (round 1): TestGrants_ChildrenReadFails and TestGrants_ListChildrenReadFails call the handlers directly through newGrantsDirect, so the session and CSRF middleware are not covered on those paths; the comment on the helper says so. — _non-blocking, left_
  - **NOTE** quality (round 1): No test checks the warning logged on the degraded GET /grants path. The test_contract does not require one, and the code does log it. — _non-blocking, left_
- `t-11` — pass after 1 round
  - **NOTE** quality (round 1): sharedInputs drops grant covers as well when the children read fails, even though the deleted-granter path shows covers could still be built with empty names; this matches the test_contract (unblocked_by [] everywhere on a ListChildren error) and the doc comment explains it — _non-blocking, left_
  - **NOTE** quality (round 1): blockedVia also checks that the degraded body contains no ':null', which goes beyond the contract and is a useful guard on the never-null shape — _non-blocking, left_
- `t-12` — pass after 1 round
  - **NOTE** quality (round 1): share502 asserts the extra GET /api/v1/children reload before checking the box is unticked, so it would fail on an optimistic tick that survives a failed PUT — _non-blocking, left_
- `t-13` — pass after 1 round
  - **NOTE** quality (round 1): acceptOffer needed no code change: the existing { ...target, ends_at } spread already carries shared, so acceptKeepsShared is a regression guard rather than a test of new code — _non-blocking, left_
  - **NOTE** quality (round 1): the 409 fallback stub grant in runTap has no shared field; it is only an offer target and is never rendered as a row, so this is harmless — _non-blocking, left_
- `t-14` — pass after 1 round
  - **NOTE** quality `c-8` (round 1): hintPassive asserts valid is true with the note shown but never compares against the same inputs without the note; it is enough here only because the hint is outside isValid by construction — _non-blocking, left_
  - **NOTE** quality (round 1): the data-shared-hint attribute on the note is not used by any test or selector — _non-blocking, left_
- `t-15` — pass after 1 round
  - **NOTE** quality `c-5` (round 1): tap() resets sharedNotes[childId] to '' before the run, so a double tap that comes back 'ignored' clears the previous tap's shared notice. notes[childId] was already reset the same way, so this follows the existing pattern. — _non-blocking, left_
  - **NOTE** quality `c-5` (round 1): When acceptOffer only extends, it returns the listed target grant, and that grant carries its stored shared list. An extend can therefore show 'Also unblocked on …'. The text is accurate but goes beyond the 201-only notice the record describes. — _non-blocking, left_
  - **NOTE** quality `c-6` (round 1): openOn removes attributed clients from the partial service's differs list, so a shared client appears once with its granter. unblockedBy checks this for the partial TikTok case. — _non-blocking, left_
- `t-16` — pass after 1 round
  - **FLAG** quality (round 1): web/src/lib/Children.test.ts is edited but not in the task's files list; the edit (dropping child from the typed client() fixture) is what the description asks for, but the file is undeclared — _non-blocking, left_
  - **NOTE** quality (round 1): web/src/lib/UnlabelledDevices.test.ts:38 still mocks a ClientView response with a stale `child: null` key; it is untyped so typecheck passes and the description only covers typed fixtures, but it no longer matches the wire shape — _non-blocking, left_
  - **NOTE** quality (round 1): the listDevices no-child assertion relies on the input fixtures having no child; clientView() spreads the server payload, so a server that still sent child would pass it through. The Go ClientView no longer emits it, so this is fine today — _non-blocking, left_
- `t-17` — pass after 1 round
  - **NOTE** quality `c-1` (round 1): TestShared_Lifecycle does not check that both children list Kid phone after the PUT; this task's test_contract claims only the 200 (no 409) for c-1, so this is not a spec gap — _non-blocking, left_
  - **NOTE** quality `c-3` (round 1): TestShared_ShareDuringGrant's final assertion only checks that youtube is back on Kid phone, not the exact list; that is enough for the contract's 'until Ada's grant ends' clause — _non-blocking, left_

---
_Generated by `dross ship`. Edit before push if you want this body changed._

