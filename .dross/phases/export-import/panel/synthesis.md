# Panel synthesis: export-import

## Scores

Scale 1-5. Claims in the drafts were checked against `internal/`, `cmd/adguard-reward` and `web/src/lib`.

| Draft | Dimension | Score | Why |
|---|---|---|---|
| risk | Criteria coverage | 5 | All six criteria and four locked decisions have owners; a risk register gives each hazard one task. |
| risk | Test-contract specificity | 5 | Sharpest of the three. Most lines name the mutant they kill: `{...}}` past `dec.More()`, the duration wrap, concurrent import. |
| risk | Granularity | 3 | No task breaks the file or layer limits, but t-3 is a thin slice of the UI, and t-8's only production change is a wiring line that a different design removes. |
| risk | Wave correctness | 4 | Mostly strict. t-7 depends on t-5 only for shared wiring and the harness, not for export behaviour. |
| mvp | Criteria coverage | 3 | All criteria are mapped, but the history watermark is missing (no contract, `children.go` not in files), and its AI-key line cannot work from `internal/api`. |
| mvp | Test-contract specificity | 3 | The lines are concrete but sit in one undifferentiated block. It says "trailing data" without the `}` case, leaves "over-limit body rejected" unspecified and has no duration overflow edge. |
| mvp | Granularity | 2 | t-3 meets the file limit, but it carries all six criteria, every locked decision and about 30 contract lines. The draft itself says to split it if review size matters. |
| mvp | Wave correctness | 5 | Two waves, and every dependency is strict. |
| verification | Criteria coverage | 5 | Each criterion and locked decision maps to named deciding tests. |
| verification | Test-contract specificity | 4 | Named tests with failure modes. Two mutation claims do not hold up (see Notes), and a few edges are softer than risk's. |
| verification | Granularity | 5 | Six tasks, each at most 4 files and 2 layers. Each has one clear failure surface. |
| verification | Wave correctness | 4 | Every dependency is real. The wave 2-3-4 chain is serial, but the risk draft's critical path is also four waves deep. |

**Skeleton: verification.** It has the only task graph that needs no restructuring, and its t-5/t-6 cut at the AdGuard boundary gives each task contracts of its own. Most grafts are contract lines from risk, whose tests are stronger but whose ten-task structure is heavier than the work needs.

## Merged plan

Contract lines without a tag are verification's. Tagged lines were grafted or reworded from the draft named.

```
Phase export-import — 6 tasks across 4 waves

Wave 1
  t-1  Add store setup snapshot and atomic restore  [verification+risk+mvp]
       files:    internal/store/setup.go (new), internal/store/setup_test.go (new),
                 internal/store/children.go, internal/store/buttons.go
       desc:     ExportSetup reads one name-keyed store.Setup in a single tx; ImportSetup
                 re-checks emptiness and writes it in one tx, resolving names to fresh ids;
                 EnsureEmpty and *ErrNotEmpty. CreateChild's insert and ReplaceButtons' row
                 insert become shared helpers, so the history watermark exists in one place.
                 Toggles are read by a query inside the tx and sorted by names, not through
                 ListServiceToggles.
       covers:   c-1, c-3, c-4, c-6
       contract: - TestSetup_RoundTrip: source ids are gapped (a service created and deleted
                   first, so A=custom:2, B=custom:3; the target gets 1 and 2). Export, import
                   into a second store, export: DeepEqual. Ids passed through unmapped resolve
                   to the wrong service or dangle, and this fails.
                 - TestSetup_IconRemap: a button icon naming a custom service NOT in the
                   button's services resolves to the same-named target service. Fails if only
                   Services is remapped.
                 - TestSetup_ToggleOrder: toggles on custom:9 (A) and custom:10 (B) list the
                   same before and after import. ListServiceToggles' string order puts
                   custom:10 first, so an unsorted export fails.
                 - TestSetup_NameLikeID: a service named "12" while another service holds row
                   id 12 resolves to the named service, not the row.  (risk)
                 - TestSetup_NotEmpty: a custom service alone, and a child with a button and a
                   toggle, each return *ErrNotEmpty naming every kind present, row counts
                   unchanged.
                 - TestSetup_ConcurrentImport: two parallel ImportSetup calls: exactly one
                   succeeds and the other gets *ErrNotEmpty. Removing the in-tx check turns it
                   into a UNIQUE failure.  (risk)
                 - TestSetup_Rollback: an invalid state on the LAST toggle trips the CHECK
                   constraint; children, child_clients, custom_services, domains, buttons,
                   button_services and service_toggles are all at zero rows. Fails if any
                   insert commits outside the tx.  (risk+mvp)
                 - TestSetup_HistoryWatermark: delete a child that has grants, import a child
                   that reuses the id; ListGrantHistory is empty. Fails if import inserts
                   children without history_after_grant_id.
                 - TestSetup_DanglingRef: a button service, a button icon and a toggle each
                   seeded with custom:99 make ExportSetup return an error, not a file its own
                   import would refuse.  (verification+risk)
                 - TestSetup_NoDeadlock: ExportSetup and ImportSetup finish under a 5 s
                   deadline; a nested st.db call on the single connection hangs and fails.
                   (risk+mvp)
                 - The existing children and buttons store suites stay green after the helper
                   extraction.  (risk)

  t-2  Parse and validate the export file  [verification+risk]
       files:    internal/api/setup_file.go (new), internal/api/setup_file_test.go (new)
       desc:     Wire types plus pure parseSetupFile([]byte) returning a file or a problem
                 (400 vs 422, message naming the path), and builtinRefs(file) listing every
                 built-in id used. No store, no AdGuard, no HTTP.
       covers:   c-4, c-6
       contract: - Invalid JSON and an unknown field are 400-class. `{...}}` is rejected: the
                   dec.More() check every existing decoder uses passes it.  (verification+risk)
                 - {"format":"adguard-reward-export","version":2,"future":[]} names version 2,
                   not "future". Fails if the strict decode runs before the version peek.
                 - Missing version, "1" as a string and body `null` each name the version.
                   (risk)
                 - Wrong or absent format is rejected as not an adguard-reward export.
                 - Children "Ada"/"ada" are duplicates; "Éa"/"éa" are NOT (NOCASE folds ASCII
                   only). Custom services "Éa"/"éa" ARE duplicates (nameKey is ToLower). One
                   shared fold breaks one of the two.
                 - "Example.COM" and "example.com." collide in one service and across two; the
                   message names the domain. Fails if decodeCustomService's silent dedupe is
                   copied.
                 - "||evil.com^$important" and a regex domain are rejected naming
                   custom_services[0].domains[0] (r-04): the file is untrusted input.
                 - buttons[i].child, buttons[i].services, buttons[i].icon, toggles[i].child and
                   toggles[i].service each reject a target missing from the file, naming the
                   path. Button child "ada" against child "Ada" is rejected (exact match after
                   trim).  (verification+risk)
                 - The same client under two children passes; two buttons with one label pass;
                   a child and a service both named Minecraft pass.  (verification+risk)
                 - A repeated (child, service) toggle, state "open", and a repeated client or
                   service inside one record are rejected, not deduped.  (verification+risk)
                 - Label, duration (grants.MinDuration/MaxDuration edges), empty services, name
                   lengths, the 256-byte client name and the 50-domain cap reject at the same
                   bounds as the existing handlers.
                 - duration 18446745874 is rejected: time.Duration(n)*time.Second wraps it to
                   30m, inside the bounds, so the check compares whole seconds.  (risk)
                 - builtinRefs includes an id used only as an icon or only in a toggle, and
                   never a custom: ref, including "custom:12" and "custom:Roblox Extras".
                   (verification+risk)

  t-3  Add export and import to Settings  [verification+risk+mvp]
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/lib/Settings.svelte,
                 web/src/lib/Settings.test.ts
       desc:     exportSetup and importSetup in api.ts, a raw-string body path that shares
                 request()'s status and 401 handling, and a Backup section in Settings with its
                 own pending, error and note state. Built against the wire shape below with
                 mocked fetch.
       covers:   c-1, c-3, c-4
       contract: - Clicking Export GETs /api/v1/export and saves one Blob named
                   adguard-reward-export.json whose text parses back to the response; the
                   object URL is revoked.  (verification+risk)
                 - A failed export shows role=alert and createObjectURL is never called.  (mvp)
                 - A 401 pin_required on export or import sets `locked`. Fails if export becomes
                   a bare <a href>, which bypasses request().  (verification+risk)
                 - Choosing a file containing `{not json` POSTs exactly those bytes with the
                   CSRF header. Fails if the client parses or re-encodes, or if the body goes
                   through request()'s JSON.stringify.  (verification+risk)
                 - Picking the same file again after a failure re-fires the import (input value
                   cleared).  (risk)
                 - A second pick during an in-flight import issues no second POST.  (risk)
                 - A 422 and a 409 show the server's message in role=alert inside the Backup
                   section and not in the PIN section; a 502 says nothing was imported and a
                   retry is safe.  (verification+risk+mvp)
                 - A 200 with missing_clients lists each name; an empty list shows no warning;
                   rules_applied false shows the catch-up note.  (verification+risk)
                 - The Backup section issues no fetch on mount, so the existing PIN tests pass
                   against Settings.test.ts's mockFetch, which throws on unexpected URLs.
                   (risk+mvp)

Wave 2 (depends t-1, t-2)
  t-4  Serve the export download  [verification+risk+mvp]
       files:    internal/api/export.go (new), internal/api/export_test.go (new),
                 internal/api/api.go, cmd/adguard-reward/main_test.go
       desc:     GET /api/v1/export behind requireSession, mapping store.Setup to the wire
                 type. ChildStore gains ExportSetup, ImportSetup and EnsureEmpty.
       covers:   c-1, c-2, c-6
       contract: - TestExport_Contents: a seeded install compares against a golden body covering
                   a shared client, button order, icon, services, duration in seconds, domain
                   order, and built-in and custom toggles. Any dropped or renamed field fails.
                 - TestExport_Gated: anonymous is 401.
                 - TestExport_AdGuardDown: with the fake failing, export is 200 and issues zero
                   /control requests after login: the backup works when AdGuard is down.  (risk)
                 - TestExport_DanglingRef: a dangling ref is 500 with no partial body.  (risk)
                 - TestExport_ParsesBack: an exported file passes parseSetupFile, so the export
                   and import rules cannot disagree.  (risk)
                 - TestExport_NoSecrets: a live session cookie plus InsertDevice with sentinel
                   token and PIN hashes; none appears in the raw body, in raw or hex form.
                 - TestExport_KeyAllowlist: decoded keys at every level equal the wire shape
                   exactly, so a future field fails here until someone chooses to export it.
                 - TestRun_ExportNoSecrets: full run() with config carrying svcPass and aiKey;
                   log in, set a PIN, export. The body holds none of: raw session cookie, its
                   SHA-256 in hex or base64, raw device cookie, the PIN's Argon2 PHC string,
                   svcPass, aiKey. Each value is read back from its real source first, so a seed
                   that failed cannot pass silently; the child's name is the positive control.
                   This sits at cmd level because internal/api never sees config.
                   (verification+risk)

Wave 3 (depends t-2, t-4)
  t-5  Gate import and commit the store  [verification+risk+mvp]
       files:    internal/api/import.go (new), internal/api/import_test.go (new),
                 internal/api/api.go
       desc:     POST /api/v1/import: bounded read, parseSetupFile, EnsureEmpty, one built-in
                 catalogue read and builtinRefs check, built-in name clash check, ImportSetup,
                 200. Makes no AdGuard write yet; t-6 fills the response's two fields.
       covers:   c-3, c-4
       contract: - TestImport_RoundTrip: gapped source harness with shared clients and mixed
                   built-in and custom buttons, export, import into a fresh harness whose ids
                   were pushed forward, export again: bytes.Equal, domain order unsorted. Each
                   button's custom ref and icon lists the same-named service via
                   /custom-services; a client shared by two children stays shared.
                   (verification+risk)
                 - TestImport_RejectedWhole, table: invalid JSON 400; unknown version, duplicate
                   name, duplicate domain, dangling child, dangling service 422. Every row
                   asserts the named problem, an empty ExportSetup, no AdGuard POST and an
                   unchanged UserRules.
                 - TestImport_UnknownBuiltin: an id absent from the catalogue used as a button
                   service, an icon and a toggle each gives 422 naming the id, no rows.
                 - TestImport_BuiltinNameClash: a custom service named like a built-in, any
                   case, gives 422 before any write.  (verification+risk)
                 - TestImport_NonEmpty: a seeded child, and separately a seeded custom service,
                   each give 409 naming what exists, with the file valid. A ChildStore wrapper
                   passing EnsureEmpty but returning *ErrNotEmpty from ImportSetup also gives
                   409, covering the in-tx race backstop.  (verification+mvp)
                 - TestImport_CatalogueDown: the catalogue read failing gives 502, no rows.
                 - TestImport_BodyBounds: a valid 100 KiB file imports, which fails if
                   decodeGrantBody's 16 KiB limit is reused; limit+1 bytes is 400 with a size
                   message, not "invalid JSON", and no rows.  (verification+risk)
                 - TestImport_Gated: anonymous is 401, missing CSRF header is 403.

Wave 4 (depends t-5)
  t-6  Apply AdGuard effects around the commit  [verification+risk+mvp]
       files:    internal/api/import.go, internal/api/import_adguard_test.go (new)
       desc:     Before commit: one Clients read for missing_clients and one
                 AdoptClients(0, nil, union of file clients). After commit: Grants.SyncRules
                 called directly, because resyncRules returns nothing and rules_applied needs
                 the error. Test file builds its own harness exposing the engine.
       covers:   c-5, c-6
       contract: - TestImport_NonEmptyNoAdoption: a 409 on a seeded install POSTs nothing to
                   AdGuard. Fails if adoption runs before the emptiness check; t-5's zero-POST
                   assertions only start to bite here.  (risk+mvp)
                 - TestImport_AdGuardFailure: SetUpdateStatus("Kid tablet", 500) gives 502
                   naming it, ExportSetup empty, Kid phone keeps its addition; clearing the
                   status and retrying gives 200, not 409. Fails if adoption moves after the
                   commit.
                 - TestImport_ClientsReadDown: a failed clients read gives 502, no rows.
                 - TestImport_MissingClient: "Gone phone" imports with 200, mapping stored,
                   missing_clients ["Gone phone"], listed once even when two children share it,
                   no /control/clients/add call.  (verification+risk)
                 - TestImport_SharedClientAdopted: a client under two children ends with
                   use_global_blocked_services false after one /control/clients/update.
                   (reworded; see Notes)
                 - TestImport_RulesOutsideMarkersUntouched: UserRules seeded with a comment,
                   blank lines, trailing spaces, non-ASCII and a near-miss marker comment before
                   and after the markers. Post-import those lines are identical in value and
                   position, and the region holds ||domain^$client=... for the imported clients.
                   (verification+risk)
                 - TestImport_CustomServiceGrants: a grant on an imported service adds its @@
                   exception under the new id and ending it removes that, outside lines still
                   identical.  (verification+risk)
                 - TestImport_RulesWriteFails: set_rules 500 gives 200, rules_applied false, rows
                   stored; one eng.Reconcile puts the region in place with no re-adoption POST.
                   (verification+risk)
                 - TestImport_MalformedRegion: a malformed region gives 200, rules_applied false,
                   and its marker lines are untouched.  (risk)
                 - TestImport_TogglesConverge: built-in unblocked, built-in blocked and custom
                   unblocked toggles on the second child, ids differing from the source. After
                   one eng.Reconcile, BlockedServices and the @@ lines match on that child's
                   clients only. Kid tablet starts on use_global_blocked_services, so skipped
                   adoption fails here: the reconciler skips clients on the global list.
                 - TestImport_ToggleEdges: a permanent unblock of a service in the global list
                   is gone from the client after Reconcile; two children with opposite built-in
                   toggles on a shared client leave it open.  (risk)
```

### Wire shape

Wave 1 builds the frontend against this shape. It is verification's, changed by the provisional defaults below.

```
GET /api/v1/export -> 200
{
  "format": "adguard-reward-export",
  "version": 1,
  "children":        [{"name": "Ada", "clients": ["Kid phone"]}],
  "custom_services": [{"name": "Roblox Extras", "domains": ["rbxcdn.com"]}],
  "buttons":         [{"label": "Games", "child": "Ada",
                       "services": ["youtube", "custom:Roblox Extras"],
                       "icon": "custom:Roblox Extras", "duration": 1800}],
  "toggles":         [{"child": "Ada", "service": "custom:Roblox Extras", "state": "unblocked"}]
}

POST /api/v1/import  (raw file body, 8 MiB limit)
-> 200 {"missing_clients": [], "rules_applied": true}
-> 400 bad_request | 409 conflict | 422 unprocessable | 502 adguard_unavailable  ({error, message})
```

The file holds no row ids and no timestamps. Toggles sort by (child name, service ref). Children, services and buttons keep stored order, which import reproduces.

### Coverage

| Criterion | Tasks |
|---|---|
| c-1 | t-1, t-3, t-4 |
| c-2 | t-4 |
| c-3 | t-1, t-3, t-5 |
| c-4 | t-1, t-2, t-3, t-5 |
| c-5 | t-6 |
| c-6 | t-1, t-2, t-4, t-6 |

| Locked decision | Tasks |
|---|---|
| non_empty_target | t-1, t-5, t-6 |
| missing_clients | t-6 |
| adguard_failure | t-6 |
| unknown_builtin_service | t-2, t-5 |

### Notes

Draft claims checked against source:

- **Confirmed by running them:** `{"a":1}}` decodes with `dec.More()` false, and `time.Duration(18446745874)*time.Second` is 30m0.29s, inside the bounds.
- **Confirmed by reading:**
  - `DeleteCustomService` strips button refs and icons, and trigger `service_toggles_custom_delete` removes toggles. A dangling ref is therefore unreachable through the API, and tests must seed it directly.
  - Every `ChildStore` test fake embeds the interface.
  - `harness` does not expose the engine.
  - `custom:10` sorts before `custom:9`.
  - Children have no AUTOINCREMENT, so ids are reused and the history watermark matters.
- **Wrong in verification:** TestImport_SharedClientAdoptedOnce claimed to fail on per-child adoption. It does not: `AdoptBlockedServices` skips the write when the client no longer inherits and already holds the list, so a second call on an empty install sends no update. Risk's "one adoption POST" line has the same gap. The merged line keeps the assertion and drops the mutation claim.
- **Weak in verification:** TestSetup_Rollback tripped on a service domain, which fails before buttons and toggles are written. Risk's last-toggle failure replaces it. The `CHECK (state IN ...)` constraint makes that failure reachable.
- **Wrong in mvp:** the c-2 line seeds an AI key through `AI_API_KEY`. The real variable is `ADGUARD_REWARD_AI_API_KEY`, and `internal/api` never reads config, so the seed does nothing there.
- **Unneeded in risk:** t-4's `custom_services.go` edit. `nameKey` and `insertDomains` are already package-level, so the service insert has no watermark-style invariant to protect.

All three drafts agree on the points below, but the spec names none of them, so they need a human look:

- A custom service named like a built-in returns 422. The existing create path returns 409.
- Built-in toggles wait for the next reconcile tick, up to 60 s after import.
- Active grants are not part of the emptiness check.

Risk also flags a manual check at verify: blob download inside an iOS standalone PWA cannot be tested in jsdom.

## Disagreements

**1. How to decompose the backend**
- Diverged: the number of tasks and where the import work is cut.
- risk: ten tasks, one import handler task (t-7), then separate test-only tasks for round trip (t-9) and convergence (t-10).
- mvp: one API task for both endpoints and every criterion.
- verification: export, import-and-store, then AdGuard effects, cut at the AdGuard boundary.
- Default: verification's cut, with risk's t-9 and t-10 contracts folded into t-5 and t-6.
- Why it matters: risk's shape allows more parallel work and separates failure surfaces. Verification's keeps each test with the code it proves. If t-6 proves too dense in review, risk's t-10 is the natural split.

**2. One store task or a read/write split**
- Diverged: risk splits snapshot read (t-1) from guarded write (t-4). mvp and verification use one store task. File lists also differ: mvp edits `toggles.go`, risk edits `custom_services.go`, and verification edits neither.
- Default: one task at 4 files, with the toggle read as a query inside `setup.go`, because export needs its own sort and no `updated_at`.
- Why it matters: risk's split shortens no critical path, since both shapes run four waves. It would give two smaller reviews. Adding `toggles.go` to the single task would reach the 5-file limit and force the split.

**3. How the API reaches the store**
- Diverged: risk adds `Deps.Setup` with its own interface, a nil-answers-500 path and a `main.go` line. mvp and verification widen `ChildStore`.
- Default: widen `ChildStore`.
- Why it matters: every harness and `main.go` already pass the store as `Children`, and the test fakes embed the interface, so the change needs no wiring. That removes the "works in tests, nil in production" risk rather than testing for it. The cost is that `ChildStore` grows further from its documented purpose of serving children handlers.

**4. Where the c-2 secret proof lives**
- Diverged: risk gives it a task of its own (t-8) at cmd level. verification puts it in the export task, at both api and cmd level. mvp keeps it at api level only.
- Default: both levels inside t-4, using risk's stronger seed-and-read-back list.
- Why it matters: only a cmd-level test can seed the AdGuard password and AI key, as c-2 requires. With no `main.go` change (disagreement 3), a separate t-8 would be one test in one file. The cost is that t-4 spans the api and cmd test layers.

**5. How custom services are referenced in the file**
- Diverged: risk uses typed refs (`{"id":"youtube"}`, `{"custom":"Minecraft"}`). mvp and verification use `custom:<name>` strings.
- Default: `custom:<name>` strings, with risk's service-named-"12" case added to t-1 and t-2 as contracts.
- Why it matters: the format is hard to change once users hold backups. Strings keep every field a string. But `custom:12` also matches `IsCustomServiceID`, so code that reuses the row-id helpers will misread a name as an id, or `custom:Foo` as a built-in. Typed refs make that bug unrepresentable, at the cost of a union type in every ref field.

**6. Where toggles sit in the file**
- Diverged: risk nests toggles under their child. mvp and verification use a top-level list with child names.
- Default: top-level list, which keeps the skeleton's `toggles[i].child` contracts.
- Why it matters: nesting makes a toggle on an unknown child unrepresentable and removes one validation path. The top-level list keeps all cross-references in one shape. This is also fixed once files exist.

**7. How the file identifies itself**
- Diverged: risk uses `format_version`, mvp uses `version`, and verification uses `format` plus `version`.
- Default: `format` plus `version`.
- Why it matters: c-4 requires the response to name the problem. A format marker can say "not an adguard-reward export" for an unrelated JSON file, where the other two can only report a missing version.

**8. The import success body**
- Diverged: risk returns `{missing_clients, rules_applied}`, mvp returns `{missing_clients}`, and verification adds four row counts.
- Default: risk's. The field follows the `applied` flag on `customServiceView`. Counts are dropped because no criterion needs them.
- Why it matters: without the flag, the parent sees success while c-5's rules have not landed. With it, the handler must call `SyncRules` itself, because `resyncRules` returns nothing.

**9. The body limit and its status**
- Diverged: verification sets 1 MiB and 400, mvp sets 8 MiB and an unstated rejection, and risk sets an unstated limit and 413.
- Default: 8 MiB and 400 with a size message, keeping risk's point that it must not read as "invalid JSON".
- Why it matters: every existing handler answers 400 "request body too large", and `Code*` has no 413 entry. Nothing caps the number of custom services, so 1 MiB (about 80 full services) could produce an export that its own import refuses, which breaks c-3. A larger limit costs only memory on an authenticated route.

**10. Content-Disposition on export**
- Diverged: verification sends and tests `attachment`. risk and mvp drop it.
- Default: drop it.
- Why it matters: all three download through `fetch` and a Blob, so the SPA never reads the header. It would only help someone calling the endpoint directly, and it adds a test for an unused path.

**11. Frontend task and component shape**
- Diverged: risk has two tasks (client in wave 1, Settings in wave 2) and a new `SetupFile.svelte`. mvp and verification keep a section inside `Settings.svelte`; verification uses one 4-file task.
- Default: verification's single wave 1 task, with risk's behavioural contracts.
- Why it matters: `Settings.svelte` is 195 lines, so a section fits. Risk's component isolates state and tests better, and merging its two tasks would hit the 5-file limit. Choosing it means keeping two tasks.

**12. How the SPA sends the file**
- Diverged: risk and verification POST the raw text. mvp runs `JSON.parse` and posts the result, reporting invalid JSON locally.
- Default: raw text.
- Why it matters: re-encoding hides trailing data and duplicate keys from the server, so c-4's named rejections become unreachable from the UI. It also gives two error paths for one problem. The cost is a small raw-body path beside `request()`.

**13. Rejecting newlines in client names**
- Diverged: risk rejects client names containing `\r` or `\n`. mvp and verification do not.
- Default: not grafted.
- Why it matters: `QuoteClient` already refuses such names at render and `Render` skips them, so they cannot reach `user_rules`. `decodeChild` accepts them, so rejecting them would make a stored mapping export a file its own import refuses, which breaks c-3. If the rule is wanted, `decodeChild` should change too, and that is outside this phase.
