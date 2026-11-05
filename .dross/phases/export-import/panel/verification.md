# export-import: verification lens

Each criterion's ideal test was written first; each task is the smallest change
that lets those tests pass. The strongest oracle available decided the design:
c-3 is one `bytes.Equal` on two exports, so the file carries no ids and no
timestamps.

```
Phase export-import — 6 tasks across 4 waves

Wave 1
  t-1  Add store setup snapshot and atomic restore
       files:    internal/store/setup.go (new), internal/store/setup_test.go (new),
                 internal/store/children.go, internal/store/buttons.go
       desc:     ExportSetup reads one name-keyed store.Setup in a single tx; ImportSetup
                 writes it in one tx, resolving names to fresh ids; EnsureEmpty and
                 *ErrNotEmpty. CreateChild's insert and ReplaceButtons' row insert become
                 shared helpers, so the history watermark exists in one place.
       covers:   c-1, c-3, c-4, c-6
       depends:  none
       status:   pending
       contract: - TestSetup_RoundTrip: source ids are gapped (a service created and deleted
                   first, so A=custom:2, B=custom:3; the target gets 1 and 2). Export, import
                   into a second store, export: DeepEqual. If ids pass through unmapped, the
                   target button resolves to the wrong service or dangles and this fails.
                 - TestSetup_IconRemap: a button icon naming a custom service that is NOT in
                   the button's services resolves to the same-named target service. Fails if
                   only Services is remapped.
                 - TestSetup_ToggleOrder: toggles on custom:9 (A) and custom:10 (B) list the
                   same before and after import. ListServiceToggles' string order puts
                   custom:10 first, so an unsorted export fails.
                 - TestSetup_NotEmpty: a custom service alone, and a child with a button and
                   a toggle, each make ImportSetup return *ErrNotEmpty naming every kind
                   present, with row counts unchanged.
                 - TestSetup_Rollback: a Setup whose last service repeats a domain trips the
                   UNIQUE constraint; ExportSetup is then empty. Fails if any insert runs
                   outside the tx.
                 - TestSetup_HistoryWatermark: delete a child that has grants, import a child
                   that reuses the id; ListGrantHistory is empty. Fails if import inserts
                   children without history_after_grant_id.
                 - TestSetup_DanglingRef: a button seeded with custom:99 makes ExportSetup
                   return an error, not a file its own import would refuse.

  t-2  Parse and validate the export file
       files:    internal/api/setup_file.go (new), internal/api/setup_file_test.go (new)
       desc:     Wire types plus pure parseSetupFile([]byte) returning a file or a problem
                 (400 vs 422, message naming the path), and builtinRefs(file) listing every
                 built-in id used. No store, no AdGuard, no HTTP.
       covers:   c-4, c-6
       depends:  none
       status:   pending
       contract: - Invalid JSON, trailing data and an unknown field are 400-class.
                 - {"format":"adguard-reward-export","version":2,"future":[]} names version 2,
                   not "future". Fails if the strict decode runs before the version peek.
                 - Wrong or absent format is rejected as not an adguard-reward export.
                 - Children "Ada"/"ada" are duplicates; "Éa"/"éa" are NOT (NOCASE folds ASCII
                   only). Custom services "Éa"/"éa" ARE duplicates (nameKey is ToLower). One
                   shared fold breaks one of the two.
                 - "Example.COM" and "example.com." collide in one service and across two; the
                   message names the domain. Fails if decodeCustomService's silent dedupe is
                   copied.
                 - Domain "||evil.com^$important" is rejected naming
                   custom_services[0].domains[0] (r-04): the file is untrusted input.
                 - buttons[i].child, buttons[i].services, buttons[i].icon, toggles[i].child and
                   toggles[i].service each reject a target missing from the file, naming the
                   path.
                 - The same client under two children passes (shared device).
                 - A repeated (child, service) toggle and state "open" are rejected.
                 - Label, duration (grants.MinDuration/MaxDuration edges), empty services,
                   name lengths and the 50-domain cap reject at the same bounds as the
                   existing handlers.
                 - builtinRefs includes an id used only as an icon or only in a toggle, and
                   never a custom: ref.

  t-3  Add export and import to Settings
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/lib/Settings.svelte,
                 web/src/lib/Settings.test.ts
       desc:     exportSetup and importSetup in api.ts, a raw-string body path through
                 request(), and a Backup section in Settings with its own error and note state.
                 Built against the wire shape below with mocked fetch.
       covers:   c-1, c-4
       depends:  none
       status:   pending
       contract: - Clicking Export GETs /api/v1/export and saves one Blob named
                   adguard-reward-export.json whose text parses back to the response.
                 - A 401 pin_required on export sets `locked`. Fails if export becomes a bare
                   <a href>, which bypasses request().
                 - Choosing a file containing `{not json` POSTs exactly those bytes with the
                   CSRF header. Fails if the client parses or re-encodes, which would hide the
                   server's named problem.
                 - A 422 message renders in role=alert inside the Backup section and not in the
                   PIN section. Fails if the shared `error` state is reused.
                 - A 200 with missing_clients lists each name; an empty list shows no warning.

Wave 2 (depends t-1, t-2)
  t-4  Serve the export download
       files:    internal/api/export.go (new), internal/api/export_test.go (new),
                 internal/api/api.go, cmd/adguard-reward/main_test.go
       desc:     GET /api/v1/export behind requireSession with attachment disposition, mapping
                 store.Setup to the wire type. ChildStore gains ExportSetup, ImportSetup and
                 EnsureEmpty.
       covers:   c-1, c-2, c-6
       depends:  t-1, t-2
       status:   pending
       contract: - TestExport_Contents: a seeded install compares against a golden body covering
                   a shared client, button order, icon, services, duration in seconds, domain
                   order, and built-in and custom toggles. Any dropped or renamed field fails.
                 - TestExport_Download: Content-Disposition is attachment with the filename;
                   anonymous is 401.
                 - TestExport_NoSecrets: a live session cookie plus InsertDevice with sentinel
                   token and PIN hashes; none appears in the raw body, in raw or hex form.
                 - TestExport_KeyAllowlist: decoded keys at every level equal the wire shape
                   exactly, so a future field fails here until someone chooses to export it.
                 - TestRun_ExportNoSecrets: full run() with config carrying svcPass and aiKey;
                   neither appears in the body, with the child's name as positive control.
                   This sits at cmd level because internal/api never sees config, so a test
                   there could not seed these values.

Wave 3 (depends t-4)
  t-5  Gate import and commit the store
       files:    internal/api/import.go (new), internal/api/import_test.go (new),
                 internal/api/api.go
       desc:     POST /api/v1/import: bounded read, parseSetupFile, EnsureEmpty, one built-in
                 catalogue read and builtinRefs check, built-in name clash check, ImportSetup,
                 200 with counts. Makes no AdGuard write yet.
       covers:   c-3, c-4
       depends:  t-2, t-4
       status:   pending
       contract: - TestImport_RoundTrip: gapped source harness, export, import into a fresh
                   harness, export again: bytes.Equal, and each button's custom ref lists the
                   same-named service via /custom-services.
                 - TestImport_RejectedWhole, table: invalid JSON 400; unknown version, duplicate
                   name, duplicate domain, dangling child, dangling service 422. Every row
                   asserts the named problem, an empty ExportSetup, no AdGuard POST and an
                   unchanged UserRules.
                 - TestImport_UnknownBuiltin: an id absent from the catalogue used as a button
                   service, an icon and a toggle each gives 422 naming the id, no rows.
                 - TestImport_BuiltinNameClash: a custom service named like a built-in gives 422.
                 - TestImport_NonEmpty: an existing child gives 409 naming it, with the file
                   valid. A ChildStore wrapper passing EnsureEmpty but returning *ErrNotEmpty from
                   ImportSetup also gives 409, covering the in-tx race backstop.
                 - TestImport_CatalogueDown: the catalogue read failing gives 502, no rows.
                 - TestImport_BodyBounds: a valid 100 KiB file imports, which fails if
                   decodeGrantBody's 16 KiB limit is reused; a body over 1 MiB is 400, no rows.
                 - TestImport_Gated: anonymous is 401, missing CSRF header is 403.

Wave 4 (depends t-5)
  t-6  Apply AdGuard effects around the commit
       files:    internal/api/import.go, internal/api/import_adguard_test.go (new)
       desc:     Before commit: one Clients read for missing_clients and one
                 AdoptClients(0, nil, union of file clients). After commit: resyncRules. Test
                 file builds its own harness exposing the engine, as newCustomHarness does.
       covers:   c-5, c-6
       depends:  t-5
       status:   pending
       contract: - TestImport_AdGuardFailure: SetUpdateStatus("Kid tablet", 500) gives 502 naming
                   it, ExportSetup empty, Kid phone keeps its addition; clearing the status and
                   retrying gives 200. Fails if adoption moves after the commit.
                 - TestImport_ClientsReadDown: a failed clients read gives 502, no rows.
                 - TestImport_MissingClient: "Gone phone" imports with 200, mapping stored,
                   missing_clients ["Gone phone"], no /control/clients/add call.
                 - TestImport_SharedClientAdoptedOnce: a client under two children gets one
                   update. Fails on per-child adoption.
                 - TestImport_RulesOutsideMarkersUntouched: UserRules seeded with a comment, odd
                   whitespace and non-ASCII lines before and after the markers. Post-import
                   those lines are identical in value and position, and the region holds
                   ||domain^$client=... for the imported clients.
                 - TestImport_CustomServiceGrants: a grant on an imported service adds its @@
                   exception and ending it removes that, outside lines still identical.
                 - TestImport_RulesWriteFails: set_rules 500 still gives 200 with rows stored;
                   one eng.Reconcile puts the region in place.
                 - TestImport_TogglesConverge: built-in unblocked, built-in blocked and custom
                   unblocked toggles on the second child, ids differing from the source. After
                   one eng.Reconcile, BlockedServices and the @@ lines match on that child's
                   clients only. Kid tablet starts on use_global_blocked_services, so skipped
                   adoption fails here: the reconciler skips clients on the global list.
```

## Coverage

| Criterion | Tasks | Deciding tests |
|---|---|---|
| c-1 | t-1, t-3, t-4 | TestExport_Contents, TestExport_Download, Settings export test |
| c-2 | t-4 | TestExport_NoSecrets, TestExport_KeyAllowlist, TestRun_ExportNoSecrets |
| c-3 | t-1, t-5 | TestSetup_RoundTrip, TestSetup_IconRemap, TestSetup_ToggleOrder, TestImport_RoundTrip |
| c-4 | t-1, t-2, t-3, t-5 | setup_file table, TestSetup_Rollback, TestImport_RejectedWhole, Settings 422 test |
| c-5 | t-6 | TestImport_RulesOutsideMarkersUntouched, TestImport_CustomServiceGrants, TestImport_RulesWriteFails |
| c-6 | t-1, t-2, t-4, t-6 | TestSetup_RoundTrip, TestExport_Contents, TestImport_TogglesConverge |

| Locked decision | Task | Deciding test |
|---|---|---|
| non_empty_target | t-1, t-5 | TestSetup_NotEmpty, TestImport_NonEmpty |
| missing_clients | t-6 | TestImport_MissingClient |
| adguard_failure | t-6 | TestImport_AdGuardFailure, TestImport_RulesWriteFails |
| unknown_builtin_service | t-2, t-5 | builtinRefs test, TestImport_UnknownBuiltin |

## Judgment calls

- **File references:** chose names (`"custom:<name>"`, children by name) over local `custom:<n>` ids with a remap table, because the c-3 oracle becomes `bytes.Equal` with no id-normalising test helper that could hide a bug.
- **Timestamps:** left out `exported_at` and toggle `updated_at`, because either breaks byte equality and no criterion needs them.
- **Remap location:** chose the store (name-keyed `store.Setup`) over the API, so the round trip is testable against real SQL without AdGuard.
- **Wire type:** kept separate api structs over JSON tags on store types, matching `childView` and `buttonView`; the cost is two small mappers.
- **Engine:** composed `AdoptClients`, `ImportSetup` and `resyncRules` in the handler, as `handleChildCreate` does, rather than adding `Engine.Import` under the mutex. No contract needs the lock, and the in-tx emptiness re-check covers the race.
- **Adoption:** one `AdoptClients(0, nil, union)` over one call per child. On an empty install every baseline is the global list, and shared clients get one write.
- **Built-in toggles:** left to the next reconcile tick (up to 60 s) over calling Reconcile from the handler, per c-6's wording. A blocked toggle can lag that long after import; custom toggles apply at once through `resyncRules`.
- **Custom name equal to a built-in:** 422, not the 409 the create path uses. The spec does not name this case; 409 stays reserved for non_empty_target, and the file is incompatible with this AdGuard.
- **Duplicate domains:** rejected over deduped as `decodeCustomService` does, because c-4 names duplicates as a rejection.
- **Version check:** peek at format and version before the strict decode, so a newer file reports its version rather than its first unknown field.
- **Dangling ref on export:** return an error over dropping the reference. A backup that silently loses a button is worse, and the state is unreachable through the API.
- **Store access:** extended `ChildStore` over a new `Deps.Setup` field, following `ListServiceToggles` and `ListGrantHistory`. That keeps `main.go` and every harness unchanged and t-4 at 4 files.
- **Frontend wave:** wave 1 against a pinned wire shape over wave 4. The risk is SPA and server drifting apart; TestExport_KeyAllowlist and the exact-body tests in t-3 both pin the shape below.
- **SPA transport:** through `request()` over a plain download link, so `pin_required` locks the screen; the raw body keeps malformed files reaching the server.
- **t-5/t-6 split:** cut at the AdGuard boundary over one import task, so t-6's contracts exist only because of t-6's lines. t-5 alone already satisfies c-3 and c-4.
- **Body limit:** 1 MiB, about 80 services at the maximum 50 domains of 253 characters, over reusing the 16 KiB and 32 KiB handler bounds.

## Wire shape the contracts assume

```
GET /api/v1/export  -> 200, Content-Disposition: attachment; filename="adguard-reward-export.json"
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

POST /api/v1/import  (raw file body)
-> 200 {"children": 1, "custom_services": 1, "buttons": 1, "toggles": 1, "missing_clients": []}
-> 400 bad_request | 409 conflict | 422 unprocessable | 502 adguard_unavailable  ({error, message})
```

Toggles sort by (child name, service ref). Children, services and buttons keep
stored order, which import reproduces.
