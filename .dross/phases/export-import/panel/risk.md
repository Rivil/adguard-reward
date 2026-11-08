# Panel draft: risk lens

Phase export-import — 10 tasks across 4 waves

Every task owns named risks from the register below. A risk has exactly one owner; a red test
elsewhere is a defect routed back to that owner, not a second test of the same thing.

Wire contract pinned for all tasks (so frontend and backend can run in parallel):

- `GET /api/v1/export` -> 200, the file. `POST /api/v1/import`, body is the file -> 200
  `{missing_clients: string[], rules_applied: bool}`.
- File: `{format_version: 1, children: [{name, clients[], toggles: [{service, state}]}],
  custom_services: [{name, domains[]}], buttons: [{label, child, services: [ref], icon: ref|null, duration}]}`.
- `ref` is `{"id": "youtube"}` or `{"custom": "Minecraft"}`. Custom services are referenced by name.
  The file holds no row ids and no timestamps.
- Status: 400 not JSON, 413 too large, 422 anything incompatible, 409 non-empty install,
  502 AdGuard failure before commit.

```
Wave 1
  t-1  Read setup snapshot in one transaction
       files:    internal/store/setup.go (new), internal/store/setup_test.go (new)
       desc:     store.Setup + store.ServiceRef types; ReadSetup returns children, toggles, custom
                 services and buttons from one tx with custom:<n> translated to names. Toggles sort
                 by (child, service name), not by id string.
       covers:   c-1, c-3
       depends:  -
       status:   pending
       contract: - ReadSetup finishes inside a 5 s test deadline; a call to st.ListChildren or
                   st.ListServiceToggles inside the tx blocks on the single connection and fails it.
                 - Toggles on custom:9 and custom:10 come back in the same order as on a store where
                   those services hold ids 1 and 2 (the string sort puts custom:10 first).
                 - A button_services row, button icon or toggle naming a custom id with no row
                   returns *ErrDanglingRef instead of a snapshot; export never emits an unresolvable file.
                 - A service named "12" while another service holds row id 12 resolves to the right one.

  t-2  Add strict setup file parser
       files:    internal/api/transfer_file.go (new), internal/api/transfer_file_test.go (new)
       desc:     Wire types plus a pure parseSetupFile(bytes): version read first, then strict decode,
                 then field, duplicate and reference checks using the API's existing limits. No I/O.
       covers:   c-4
       depends:  -
       status:   pending
       contract: - {"format_version":2,"new_field":1} names the version, not the unknown field; missing
                   version, "1" as a string and body `null` each name format_version.
                 - `{...}}` is rejected; a dec.More() check passes it (confirmed on this Go toolchain).
                 - Duplicates: children Ada/ada rejected (ASCII fold) but Éa/éa accepted; custom services
                   Éa/éa rejected (strings.ToLower), matching the two store rules, so neither reaches a
                   UNIQUE failure and a 500.
                 - Domains compare after NormalizeDomain: Example.COM in service A and example.com. in
                   service B is rejected naming both; a repeat inside one service is rejected, not deduped.
                 - A domain holding rule text (`x.com^$important`, a regex) is rejected naming it (r-04).
                 - Client names with \r or \n are rejected; the same client under two children passes;
                   two buttons with one label pass; child and service both named Minecraft pass.
                 - Button child "ada" against child "Ada" is rejected (exact match after trim); a ref with
                   both or neither key, custom ref not in the file and a duplicate toggle on one child are
                   each rejected naming the path.
                 - duration 18446745874 is rejected; time.Duration(n)*time.Second wraps it to 30m, inside
                   the bounds (confirmed), so the check compares whole seconds.

  t-3  Add export and raw-body import client
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts
       desc:     exportSetup() through request(); importSetup(text) through a raw-body path that shares
                 request()'s status and 401 handling.
       covers:   c-1
       depends:  -
       status:   pending
       contract: - importSetup sends the file text byte-identical; routing it through request()'s
                   JSON.stringify double-encodes it and the test on the fetch body fails.
                 - Invalid JSON text is still POSTed, so the server names the problem; no client-side parse.
                 - A 401 pin_required on either call sets `locked`, and the CSRF header is present on import.

Wave 2 (t-4 depends t-1; t-5 depends t-1, t-2; t-6 depends t-3)
  t-4  Commit import in one guarded transaction
       files:    internal/store/setup_write.go (new), internal/store/setup_write_test.go (new),
                 internal/store/children.go, internal/store/custom_services.go
       desc:     WriteSetup re-checks emptiness and inserts everything in one tx, resolving custom names
                 to fresh LastInsertId values. Extract the child and service inserts so the
                 history_after_grant_id watermark has one source.
       covers:   c-3, c-4, c-6
       depends:  t-1
       status:   pending
       contract: - A failure on the last toggle insert leaves all of children, child_clients,
                   custom_services, domains, buttons, button_services and service_toggles at zero rows.
                 - Two concurrent WriteSetup calls: exactly one succeeds, the other gets *ErrNotEmpty, not
                   a UNIQUE error; removing the in-tx check turns it into a constraint failure.
                 - *ErrNotEmpty lists each populated kind; a custom-service-only install is refused.
                 - After create-then-delete has advanced AUTOINCREMENT, buttons, icons and toggles land on
                   the new ids; a hard-coded custom:1 assumption fails.
                 - With a deleted child's grant still in `grants`, an imported child reusing that id
                   returns an empty history page; a raw INSERT without the watermark shows the old grant.
                 - The existing children and custom service store suites stay green after the extraction.

  t-5  Serve export endpoint from the snapshot
       files:    internal/api/transfer_export.go (new), internal/api/transfer_export_test.go (new),
                 internal/api/api.go
       desc:     GET /api/v1/export behind the session gate, mapping store.Setup to wire types.
                 Adds the SetupStore dep (nil answers 500) and a test harness exposing the engine.
       covers:   c-1
       depends:  t-1, t-2
       status:   pending
       contract: - The body holds every child with clients and toggles, every button with order, icon,
                   services and duration, and every custom service with domains in parent order.
                 - Export with the fake AdGuard failing is 200 and issues zero /control requests: the
                   backup works when AdGuard is down.
                 - A dangling ref is 500 with no partial body. No session is 401.
                 - An exported file passes parseSetupFile, so the export and import rules cannot disagree.

  t-6  Add export and import to Settings
       files:    web/src/lib/SetupFile.svelte (new), web/src/lib/SetupFile.test.ts (new),
                 web/src/lib/Settings.svelte
       desc:     Own section with its own pending and error state: blob download, file picker, result
                 listing missing clients and lagging rules.
       covers:   c-1
       depends:  t-3
       status:   pending
       contract: - Picking the same file again after a failure re-fires the import (input value cleared);
                   a stale input swallows the retry.
                 - A second pick during an in-flight import issues no second POST.
                 - 200 with missing_clients lists each name; rules_applied false shows the catch-up note.
                 - A 422 or 409 shows the server's message; a 502 says nothing was imported and retry is safe.
                 - An import error does not appear in the PIN section, and the component issues no fetch on
                   mount (Settings.test.ts's mockFetch throws on any unexpected URL).
                 - Export builds a .json blob, clicks a download link and revokes the object URL.

Wave 3 (t-7 depends t-2, t-4, t-5; t-8 depends t-5)
  t-7  Serve import with all-or-nothing ordering
       files:    internal/api/transfer_import.go (new), internal/api/transfer_import_test.go (new),
                 internal/api/api.go
       desc:     POST /api/v1/import in order: bounded read, parse, emptiness 409, catalogue 422,
                 clients read, AdoptClients on the de-duplicated client set, WriteSetup, SyncRules.
       covers:   c-4
       depends:  t-2, t-4, t-5
       status:   pending
       contract: - Each rejection (bad JSON, version, duplicate, dangling ref, unknown built-in, built-in
                   name clash) leaves zero store rows and zero POSTs on the fake.
                 - Unknown built-ins in a button's services, a button icon and a toggle each return 422
                   naming the id (locked unknown_builtin_service).
                 - A custom service named like a live built-in, any case, is 422 before any write.
                 - Each 409 case runs against a seeded install, names what exists, and POSTs nothing to AdGuard.
                 - SetUpdateStatus failing the second client: 502, zero rows, first client keeps its
                   additions; clearing it and retrying is 200 (locked adguard_failure).
                 - A failed catalogue or clients read is 502 with nothing written.
                 - A client AdGuard lacks keeps its mapping and is listed once, even when shared
                   (locked missing_clients). A shared client gets one adoption POST.
                 - A failing set_rules is 200 rules_applied false with rows kept; a malformed region
                   behaves the same and its marker lines are untouched.
                 - limit+1 bytes is 413, not "invalid JSON". No CSRF header is 403. No session is 401.

  t-8  Wire store and prove export secret-free
       files:    cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go
       desc:     Pass the store as Deps.Setup. Boot through run() with an AI key and AdGuard password in
                 config, log in, set a PIN, export, and scan the body.
       covers:   c-2
       depends:  t-5
       status:   pending
       contract: - The body contains none of: raw session cookie, its SHA-256 in hex or base64, raw device
                   cookie, the PIN's Argon2 PHC string, the AdGuard password, the AI key. Each value is read
                   back from its real source first, so a seed that failed cannot pass silently.
                 - The file's key set equals an explicit allowlist at every level, so a new field fails here.
                 - Without the Deps.Setup line in main.go the request is 500 and this test fails (the api
                   harness cannot see that wiring).

Wave 4 (t-9 and t-10 depend t-7)
  t-9  Prove export-import-export round trip
       files:    internal/api/transfer_roundtrip_test.go (new)
       desc:     Two harnesses: seed one with shared clients and mixed built-in and custom buttons, export,
                 import into a second whose ids were pushed forward, export again.
       covers:   c-3
       depends:  t-7
       status:   pending
       contract: - The second export is byte-equal to the first, with source ids non-contiguous and a
                   domain order that is not sorted.
                 - Each button's services keep their mixed order, and each custom ref and icon resolves
                   through GET /custom-services to the same-named service.
                 - A client shared by two children stays shared.

  t-10 Prove imported state converges in AdGuard
       files:    internal/api/transfer_converge_test.go (new)
       desc:     After a real import, drive grants and Engine.Reconcile against the fake and assert on
                 user_rules and blocked_services.
       covers:   c-5, c-6
       depends:  t-7
       status:   pending
       contract: - user_rules seeded with blank lines, trailing spaces, non-ASCII and a near-miss marker
                   comment: every line outside the markers is byte-identical after import, grant and end.
                 - Block lines for an imported service sit inside the markers; a grant on it adds @@ lines
                   under its new id and ending the grant removes them.
                 - A client with use_global_blocked_services=true converges after one Reconcile; the
                   reconciler skips inheriting clients, so a missed adoption fails this.
                 - Two children with opposite built-in toggles each land on their own client, and on a
                   shared client open wins.
                 - A permanent unblock of a service in the global list is gone from the client after
                   Reconcile; a custom unblock gives @@ lines scoped to that child's clients.
                 - Rules that failed at import converge on the next Reconcile with no re-adoption POST.
```

## Risk ownership

| Risk | Owner |
|---|---|
| Deadlock: store helper on `st.db` inside a tx on the single connection | t-1 |
| Export skew: four separate reads straddling a delete | t-1 |
| `custom:10` < `custom:9` string sort breaks second-export equality | t-1 |
| Export emits a file import would refuse (dangling ref) | t-1 |
| Newer-version file reported as an unknown field | t-2 |
| Trailing `}` passes `dec.More()` | t-2 |
| Two different case-fold rules; validator mismatch becomes a 500 | t-2 |
| Duplicates hidden by normalisation or silent de-dup | t-2 |
| Crafted file puts rule text into user_rules (r-04, r-02) | t-2 |
| Over-rejecting legal repeats (shared clients, labels) | t-2 |
| Duration overflow wraps into bounds | t-2 |
| Raw file double-encoded by `request()` | t-3 |
| PIN lapse during export or import | t-3 |
| Partial commit | t-4 |
| Emptiness TOCTOU between early check and commit | t-4 |
| "Empty" install with advanced AUTOINCREMENT | t-4 |
| Reused child id inherits old grant history | t-4 |
| Backup unavailable while AdGuard is down | t-5 |
| Same-file retry swallowed by the file input | t-6 |
| Double submit from a phone | t-6 |
| Import error state leaking into PIN UI | t-6 |
| Any AdGuard or store write before validation finishes | t-7 |
| Mid-list adoption failure and retry | t-7 |
| Post-commit rules failure, malformed markers | t-7 |
| Oversized body misreported | t-7 |
| Secret reaches the file | t-8 |
| Handler works in tests, nil in production | t-8 |
| Order and reference loss across id remap, end to end | t-9 |
| Outside-marker bytes changed | t-10 |
| Toggle lands on the wrong child, or never converges | t-10 |

## Coverage

- c-1 -> t-1, t-3, t-5, t-6
- c-2 -> t-8
- c-3 -> t-1, t-4, t-9
- c-4 -> t-2, t-4, t-7
- c-5 -> t-10
- c-6 -> t-4, t-10

Locked decisions: non_empty_target -> t-4 (race backstop), t-7 (409 before AdGuard). missing_clients -> t-7.
adguard_failure -> t-7, t-10 (reconciler retry). unknown_builtin_service -> t-7.

## Judgment calls

- Custom references by name in typed refs (`{"custom": "X"}`); rejected a `custom:<name>` string, because a service named "12" reads as a row id, and separate built-in and custom arrays, because button order sets the icon.
- Toggles nested under their child; rejected a top-level list with child names, because nesting makes "toggle on an unknown child" unrepresentable.
- No ids or timestamps in the file; c-3 allows id differences, but none lets t-9 assert byte equality, a stricter and simpler check. `exported_at` would break it.
- One `store.Setup` owning id-to-name translation in both directions; rejected translating in the API from the four list calls, which cannot share a snapshot and spreads the remap.
- Parser in package api; rejected a new `internal/transfer` package, because it would need the unexported limits duplicated or moved, which invites validator drift.
- Emptiness checked twice, early and in the tx; rejected tx-only, because the early check keeps AdGuard untouched on 409, and early-only, because child create is not under the engine mutex.
- AdoptClients reused unchanged with childID 0, and missing clients taken from a prior Clients read; rejected a new engine method. The list is advisory and a client vanishing between reads is already skipped.
- SyncRules only after commit, leaving built-in toggles to the next tick (up to 60 s); rejected calling Reconcile from the handler, because it swallows ErrMalformedRegion, which would misreport rules_applied, and it widens the request to grant expiry. spec c-6 says the reconciler converges.
- Built-in name clash in the file is 422, not the CRUD 409; 409 is reserved by non_empty_target, and this is an incompatible file. The spec does not list it, so it needs a look.
- Strict over lenient: repeated client, service or domain inside one record is rejected rather than de-duped; export cannot produce them, and silent mutation hides a hand-edit error.
- Export built client-side from `request()` JSON into a blob; rejected an `<a href>` to the endpoint, because a lapsed PIN session would save the 401 JSON as the file. No Content-Disposition needed.
- Active grants left out of the emptiness check; non_empty_target lists four kinds and is locked. An orphan live grant on a reused child id already behaves this way under CreateChild. Reported, not fixed.
- Test-only tasks t-9 and t-10 stay separate from t-7; rejected folding them in, because they have different failure surfaces, can run in parallel, and t-7 is already the densest task.
- Frontend starts in wave 1 against the pinned contract; accepted that nothing mechanically ties TS types to the Go response. It is two fields, and a shared fixture costs more than it saves.

Outside this phase: `handleButtonsReplace` and `checkDuration` carry the same duration overflow t-2 guards against. `dec.More()` has the same trailing-bracket gap in every existing decoder. Neither is fixed here. Blob download inside an iOS standalone PWA cannot be tested in jsdom and needs a manual check at verify.
