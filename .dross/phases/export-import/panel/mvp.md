# export-import — mvp lens

```
Phase export-import — 3 tasks across 2 waves

Wave 1
  t-1  Add store setup export and atomic import
       files:    internal/store/setup.go (new), internal/store/setup_test.go (new),
                 internal/store/buttons.go, internal/store/toggles.go
       desc:     Portable store.Setup (names, no ids; custom refs as "custom:<name>").
                 ExportSetup reads it in one tx, SetupCounts reports what exists, ImportSetup
                 re-checks empty and writes everything in one tx, remapping refs to new ids.
                 Extract tx-level insertButtons/loadToggles; the public methods deadlock in a tx.
       covers:   c-1, c-3, c-6
       contract: - button services, button icon and toggle naming custom:10 export as custom:<name>;
                   a dangling custom:<n> fails ExportSetup instead of writing the raw id
                 - import into a store whose AUTOINCREMENT was advanced (service created then
                   deleted): button_services, buttons.icon and service_toggles all hold the NEW
                   custom:<id>; a hard-coded 1..n mapping fails this
                 - source services with ids 2 and 10 (string order inverts numeric): export ->
                   import -> export is DeepEqual, so unsorted toggles fail it
                 - button order and file-order children survive; toggle lands on the named child
                   when two children exist
                 - ImportSetup with one existing child returns *ErrNotEmpty and adds no rows
                 - an unresolvable ref on the LAST toggle returns an error and SetupCounts is all
                   zero: a per-table commit fails the rollback check
                 - ExportSetup and ImportSetup finish under a 2 s ctx (nested st.db use on the
                   single connection hangs and fails)
       depends:  none
       status:   pending

  t-2  Add Settings export download and import upload
       files:    web/src/lib/api.ts, web/src/lib/Settings.svelte, web/src/lib/Settings.test.ts
       desc:     New "Backup" section with its own error/note state. Download: request() GET
                 /api/v1/export, Blob, <a download>. Import: file input, JSON.parse, POST
                 /api/v1/import, show the server's message or the missing_clients list.
       covers:   c-1, c-3, c-4
       contract: - Download setup issues GET /api/v1/export and the Blob given to the stubbed
                   URL.createObjectURL parses to the mocked body; anchor download is
                   adguard-reward-export.json
                 - a failed export shows role=alert and createObjectURL is never called
                 - choosing a file POSTs exactly its JSON to /api/v1/import
                 - a 422 and a 409 each show the server's message verbatim in role=alert
                 - a non-JSON file shows role=alert with writes(calls) empty
                 - 200 with missing_clients ["Kid tablet"] shows that name in role=status;
                   with [] it shows success and no missing-client text
                 - existing PIN tests still pass: no alert/status renders until Backup is used
       depends:  none (pinned to the wire contract below; tests mock fetch)
       status:   pending

Wave 2 (depends t-1)
  t-3  Add export and import endpoints
       files:    internal/api/setup.go (new), internal/api/setup_test.go (new),
                 internal/api/api.go, internal/api/login_test.go
       desc:     Add ExportSetup/ImportSetup/SetupCounts to ChildStore; add `eng` to the harness.
                 POST order: size-bounded read, version probe, strict decode, pure validation,
                 409, Services+Clients reads, catalogue checks, AdoptClients(0, nil, union),
                 ImportSetup, resyncRules, 200. No internal/grants change.
       covers:   c-1, c-2, c-3, c-4, c-5, c-6
       contract: c-1
                 - seeded install: GET /api/v1/export holds every child's clients, buttons in
                   order with icon/services/duration, services with domains; no session is 401
                 c-2
                 - seeded session (raw cookie and stored hash), device token, stored PIN hash,
                   fakePass and an AI key sentinel set via the AI_API_KEY env: none appears in
                   the body
                 - JSON keys at every level equal the wire contract's exact set, so a new field
                   fails the test
                 c-3
                 - two harnesses, target AUTOINCREMENT advanced: export A, import B, export B is
                   byte-equal; B's button services and custom icon resolve via the store to the
                   same-named service
                 - a valid file over 16 KiB imports; decodeGrantBody's limit fails this
                 c-4 (each row: status, message contains the offender, zero POSTs on the fake,
                 SetupCounts all zero)
                 - invalid JSON; trailing data; unknown field
                 - version missing; version 2 with a new field says "version", not generic JSON
                 - duplicate child names differing in ASCII case; duplicate service names
                   differing in case; "Example.com" + "example.com." across two services
                 - duplicate (child, service) toggle; invalid toggle state
                 - button or toggle naming a child not in the file; button or icon or toggle
                   naming custom:<name> not in the file
                 - unknown built-in id in button services, button icon and toggle: 422 naming it
                 - custom service name equal to a built-in name: 422 naming it
                 - domain "||x^$important": 422, so filter syntax cannot reach user_rules
                 - over-limit body rejected
                 non_empty_target
                 - seeded child, then seeded custom service: 409 naming each, and zero POSTs (a
                   409 checked after adoption fails this)
                 missing_clients
                 - a client AdGuard lacks: 200, listed in missing_clients, mapping stored
                 adguard_failure
                 - Clients or Services read failing: 502, no writes, no rows
                 - SetUpdateStatus on the second client: 502, zero rows, first client keeps its
                   additions, retry after clearing is 200 (not 409)
                 - set_rules failing after commit: 200, rows stored, eng.Reconcile writes region
                 c-5
                 - SetUserRules with lines both sides of the markers: after import those lines
                   are byte-identical and ||domain^$client=... is inside the markers
                 - a grant on the imported service adds @@||domain^ inside the markers; ending
                   it removes the line
                 c-6
                 - unblocked built-in on Ada and blocked built-in on Ben: after eng.Reconcile each
                   child's client matches and the other child's client is untouched
                 - unblocked custom toggle: exception line for that child's client only
       depends:  t-1
       status:   pending
```

Wire contract (fixed here so t-2 and t-3 agree without a dependency):

```
GET  /api/v1/export -> 200
  {"version":1,
   "children":[{"name","clients":[...]}],
   "custom_services":[{"name","domains":[...]}],
   "buttons":[{"label","child":"<child name>","services":["youtube","custom:<name>"],
               "icon":"","duration":<seconds>}],
   "toggles":[{"child":"<child name>","service":"youtube|custom:<name>","state":"blocked|unblocked"}]}
  toggles sorted by (child, service); array order is otherwise meaningful.

POST /api/v1/import (same body) -> 200 {"missing_clients":[...]}, never null
  400 invalid JSON or shape | 422 incompatible content | 409 non-empty | 502 AdGuard
  errors use the existing {error, message} envelope; message names the problem.
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 | t-1 (read), t-2 (Settings download), t-3 (endpoint) |
| c-2 | t-3 |
| c-3 | t-1 (remap, ordering), t-2 (Settings upload), t-3 (round trip) |
| c-4 | t-2 (shows the message), t-3 (validation, atomic rejection) |
| c-5 | t-3 |
| c-6 | t-1 (toggle remap), t-3 (reconciler convergence) |

Locked decisions: `non_empty_target`, `missing_clients`, `adguard_failure` and `unknown_builtin_service` each have a contract line under t-3.

## Judgment calls

- One API task for both endpoints, not separate export and import tasks: 4 files in one layer, and the import tests need export anyway. It is the largest task here; split it if the judge weighs review size over task count.
- UI in wave 1 against a pinned wire contract, not wave 3: the tests mock fetch, so it needs no backend output.
- Refs as `custom:<name>` strings, not file-local ids or a tagged object: no second lookup table, the fields stay strings, and a second export is byte-equal.
- ID remapping in the store, not the API: only the transaction knows the new ids.
- Sort toggles by (child, service) names on export, not store order: `ListServiceToggles` sorts `custom:10` before `custom:2`, and renumbering would break c-3.
- Widen `ChildStore`, not a new `Deps.Setup`: no `main.go` or harness wiring, it already holds non-child reads, and its test fakes embed the interface.
- Emptiness via `SetupCounts`, not `deps.CustomServices`: that field is nil in the test harness, so the check would silently pass.
- Empty check twice, in the handler and the store tx: the first keeps a non-empty install's clients from being adopted, the second closes the race. A lost race leaves client additions, the same outcome `adguard_failure` accepts.
- Reuse `AdoptClients` and `SyncRules`, no engine import method: the handler mirrors child create, which is what `adguard_failure` locks. Commit runs outside the engine mutex, also as child create does.
- No immediate `Reconcile` after import: c-6 says the reconciler converges, so built-in toggles land on the next tick. Custom toggles land at once via `SyncRules`.
- Validate with the existing constants and `rules.NormalizeDomain` inline, not by extracting helpers from `buttons.go` and `children.go`: less churn, at the cost that a later change to those rules must be made in both places.
- Reject a custom name equal to a built-in as 422: no criterion names it, but the local path refuses it with 409 and c-5 requires imported services to behave like local ones.
- Version probe before the strict decode: `DisallowUnknownFields` alone would report a v2 file as a generic shape error, and c-4 requires the response to name the problem.
- Own body limit (8 MiB), not `decodeGrantBody`: its 16 KiB limit rejects real exports.
- `fetch` + Blob download, not `<a href>`: an expired session or PIN lock would save the error JSON as a backup file. This drops `Content-Disposition`, which nothing else needs.
- `JSON.parse` in the SPA, not a raw-body path through `request()`: leaves the transport alone. Invalid JSON from the UI is reported locally, and t-3 covers the server rejection.
- Backup section inside `Settings.svelte`, not a new component: three files, not five.
- `missing_clients` as the only success field, no row counts: no criterion needs counts.
- The AI key has no store row or API path, so the c-2 sentinel can only be seeded via env. That check is close to vacuous today; the exact-key test is what actually guards the file.
- No grant history, docs or `ARCHITECTURE.md` task: history is deferred in the spec and the others trace to no criterion.
