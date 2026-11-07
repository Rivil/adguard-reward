# custom-services — risk-lens draft

Lens: every task owns a named failure mode and the test that proves it cannot
happen. The managed region is never edited incrementally: its content is a pure
function of (stored custom services, current child→client mapping, active
grants), rendered and spliced into a fresh read of `user_rules`. Ref-counting
(c-7), restart gaps and manual edits (c-8) then fall out of "render desired
state, splice, skip if equal" instead of being separate code paths that can
disagree.

Wire shape fixed here so the UI can start in wave 1 (t-13) without waiting on
the server:
- custom service id: `custom:<n>` (a colon never appears in AdGuard catalogue ids)
- `GET /api/v1/services` → `{services: [{id, name, icon, custom}]}`; custom rows have `icon: ""`, `custom: true`
- `GET/POST /api/v1/custom-services`, `PUT/DELETE /api/v1/custom-services/{id}` → `{id, name, domains[], applied}`
  (`applied: false` = stored, but the AdGuard region write failed; the reconciler converges it)

```
Phase custom-services — 15 tasks across 4 waves

Wave 1
  t-1  Splice managed region into user_rules
       files:      internal/rules/region.go, internal/rules/region_test.go
       description: Pure Splice(current []string, managed []string) ([]string, error): replaces the
                   lines between `! adguard-reward begin` / `! adguard-reward end` (exclusive) with
                   managed, appends begin+managed+end at the end when no region exists, and returns
                   ErrMalformedRegion (no output) for begin-without-end, end-before-begin or a
                   second begin/end pair.
       covers:     c-9
       depends_on: []
       contract:   - property test: for random user_rules (blank lines, `!`/`#` comments, duplicate
                     lines, trailing spaces, unicode) every line outside the markers is byte-equal and
                     in the same order after Splice; a version that trims, dedupes or sorts fails it
                   - no region present → output is input + [begin, managed..., end] exactly at the
                     tail; inserting at the head or after the first comment fails it
                   - `begin` with no `end` and a doubled `begin` each return ErrMalformedRegion; a
                     version that "repairs" by appending a second region fails it
                   - Splice(Splice(x, m), m) == Splice(x, m) (idempotent, so a no-op pass can be
                     detected by equality)
       status:     pending

  t-2  Validate domains and render region rules
       files:      internal/rules/domain.go, internal/rules/domain_test.go,
                   internal/rules/render.go, internal/rules/render_test.go
       description: NormalizeDomain (lowercase, strip one trailing dot, ASCII LDH labels only, ≤253
                   chars, ≥2 labels) rejecting scheme, path, port, `*`, whitespace/newline, `||`,
                   `^`, `$`, `/regex/`, `@@`, `!`, `#`. Render(Desired) → sorted lines:
                   `||d^$client='a'|'b'` per domain over the union of every mapped child client, and
                   `@@||d^$client=...` per domain over the clients an active grant covers that
                   service on (stored clients, any grant). Client names quoted with `\`, `'`, `"`,
                   `,`, `|` backslash-escaped.
       covers:     c-2, c-6, c-7
       depends_on: []
       contract:   - table test: `http://x.com`, `x.com/a`, `x.com:443`, `*.x.com`, `x.com\n`,
                     ` x.com`, `||x.com^`, `x.com^`, `x.com$important`, `/x\.com/`, `@@x.com`,
                     `x`, `ex ample.com`, `xn--` label over 63 chars each return an error; dropping any
                     one check fails its row
                   - `Example.COM.` normalises to `example.com`
                   - a custom service whose mapped-client set is empty renders NO block line — a
                     version emitting unscoped `||d^` (which would block adults) fails it
                   - client `Frank's "TV", den|x` renders `$client='Frank\'s \"TV\"\, den\|x'`; an
                     unescaped quote/pipe fails it; a client name containing `\n` is dropped from the
                     line (and logged) rather than splitting the rule
                   - two active grants (child A, child B) cover service S on shared client TV;
                     removing A's grant from the input still renders the TV exception; removing both
                     drops it (ref-count by derivation)
                   - a grant of S with stored clients [c1,c2] renders exactly one `@@` line per domain
                     with both clients; an ended/expired grant renders none
                   - Render is deterministic: 100 runs over shuffled map/slice input give identical
                     output (otherwise every reconcile pass would rewrite)
       status:     pending

  t-3  AdGuard user_rules read-modify-write + fake
       files:      internal/adguard/rules.go, internal/adguard/rules_test.go,
                   internal/adguard/adguardtest/server.go,
                   internal/adguard/adguardtest/testdata/filtering_status.json
       description: Client.EditUserRules(ctx, func([]string) ([]string, error)) reads
                   `GET /control/filtering/status` .user_rules, applies the edit and POSTs
                   `/control/filtering/set_rules {"rules":[...]}` only when the result differs —
                   all under a dedicated mutex. Fake serves both endpoints from mutable state with
                   UserRules()/SetUserRules() observers and a hook to mutate rules between a
                   request pair.
       covers:     c-9
       depends_on: []
       contract:   - edit returning the same slice → 0 POSTs to /control/filtering/set_rules
                   - GET failing (fake SetStatus 502) → edit func never called and 0 POSTs
                   - edit func returning an error → 0 POSTs, error returned
                   - `user_rules: null` decodes to [] and an edit appending a region writes
                     `{"rules":[begin,...,end]}` — never `null`
                   - 10 concurrent EditUserRules each appending a distinct line end with all 10
                     lines present (fake MaxInFlight on filtering paths == 1); removing the mutex
                     loses lines and fails it
                   - set_rules body carries every original line byte-for-byte, including blank
                     lines and trailing whitespace
       status:     pending

  t-4  Store custom services with ownership constraints
       files:      internal/store/migrations/0007_custom_services.sql, internal/store/services.go,
                   internal/store/services_test.go
       description: Tables custom_services(id, name, name_key UNIQUE) and
                   custom_service_domains(domain PRIMARY KEY, service_id FK CASCADE).
                   Create/Update(name + whole domain set, one tx)/List/Get/Delete; Delete in one tx
                   answers *ErrServiceInUse{GrantID} while an active grant lists `custom:<id>`,
                   else deletes, strips the id from button_services and deletes buttons left empty.
                   Exports CustomServiceID/ParseCustomID/IsCustomServiceID.
       covers:     c-1, c-3, c-4
       depends_on: []
       contract:   - create "TikTok Web" then "tiktok web" → ErrNameTaken, one row stored
                   - create B with a domain A owns → *ErrDomainTaken{Owner: A} and B not stored;
                     update B to take A's domain → same error and B's old domains intact
                   - update {a,b}→{b,c} leaves exactly {b,c}; an injected failure after the delete
                     step rolls back to {a,b}
                   - delete while an active grant lists the id → *ErrServiceInUse with that grant id;
                     service, domains and buttons untouched. An ended/expired grant does not block
                   - delete strips the id from a [custom:1, youtube] button (keeps youtube, keeps
                     position) and deletes a [custom:1]-only button
                   - close and reopen the db file → service and domains still listed (c-1)
                   - IsCustomServiceID("youtube") false, ("custom:7") true, ("custom:") false
       status:     pending

  t-13 SPA service types, API calls and letter avatar
       files:      web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/lib/ServiceIcon.svelte,
                   web/src/lib/ServiceIcon.test.ts
       description: Service gains `custom: boolean`; CustomService type and
                   list/create/update/deleteCustomService; Route gains 'services' (`/services`).
                   ServiceIcon renders the AdGuard icon when `icon` is non-empty, else a letter
                   avatar from the first grapheme of the name (uppercased, `?` for blank).
       covers:     c-5
       depends_on: []
       contract:   - ServiceIcon with icon "" renders no <img> (an `iconUrl("")` data: URL is a
                     broken image) and shows "T" for "tiktok web"; "  " shows "?"
                   - a name starting with an emoji renders the whole emoji, not half a surrogate pair
                   - createCustomService posts `{name, domains}` with the CSRF header; a 400 surfaces
                     the server's message (naming the bad domain) via messageFor
                   - `/services` path resolves to route 'services' and back
       status:     pending

Wave 2 (depends t-1, t-2, t-3, t-4; t-14 depends t-13)
  t-5  Engine-owned region sync and service CRUD
       files:      internal/grants/rules.go, internal/grants/rules_test.go, internal/grants/grants.go
       description: syncRules(ctx, actx, exclude int64) under e.mu: reads custom services, children
                   and active grants (dropping `exclude`), Render → EditUserRules(Splice). Any store
                   read error aborts with no AdGuard call. Engine.CreateService/UpdateService/
                   DeleteService run the store op and syncRules under e.mu, returning
                   {Service, Applied}. Store/AdGuard interfaces extended.
       covers:     c-1, c-3, c-4, c-9
       depends_on: [t-1, t-2, t-3, t-4]
       contract:   - ListActiveGrants (or custom list) failing → 0 set_rules POSTs; a version that
                     renders an empty region on read failure (unblocking every kid) fails it
                   - UpdateService {a.com,b.com}→{b.com,c.com}: region has b and c block lines and no
                     a.com line, under an active grant the @@ lines follow the same set (c-3)
                   - DeleteService with no grant: region has no line containing any of its domains;
                     lines before and after the markers in the fake are byte-identical (c-4, c-9)
                   - DeleteService while a grant is active → ErrServiceInUse and 0 set_rules POSTs
                   - DeleteService holds e.mu across the in-use check: a concurrent Engine.Create
                     for that service either sees the service gone (rejected) or makes Delete 409 —
                     never a grant on a deleted service (race test with -race)
                   - set_rules failing → service stored, Applied=false, error not returned
                   - malformed region in the fake (begin without end) → Applied=false, 0 POSTs,
                     user_rules unchanged
       status:     pending

  t-14 Render custom services in grant form, Home, buttons
       files:      web/src/lib/GrantForm.svelte, web/src/lib/GrantForm.test.ts,
                   web/src/lib/Home.svelte, web/src/lib/Buttons.svelte
       description: Replace `<img src={iconUrl(...)}>` with ServiceIcon in GrantForm, Home (buttons
                   and blocked list) and Buttons (service picker and icon preview fallback); a button
                   whose first service is custom shows the letter avatar. Icon picker keeps offering
                   catalogue services only.
       covers:     c-5
       depends_on: [t-13]
       contract:   - GrantForm with a `custom: true` service in the catalogue lists it as selectable
                     and NOT marked "no longer in catalogue"
                   - Home button whose first service is `custom:3` renders the letter avatar, no
                     broken <img>
                   - Home blocked list shows a custom service with its state badge (blocked/partial)
                   - Buttons icon picker does not offer `custom:*` ids (server would 422 them)
       status:     pending

Wave 3 (depends t-5; t-15 depends t-13, t-14)
  t-6  Apply grant lifecycle to custom services
       files:      internal/grants/grants.go, internal/grants/grants_test.go
       description: Create/revert/End/expireLocked partition services: built-in ids keep the
                   blocked_services path, custom ids never reach it. Create syncs the region with
                   the new grant included; End and expiry sync with the grant excluded BEFORE the
                   status CAS — a failed region write leaves the grant active (ErrRevertFailed on
                   End) for the reconciler. Extend writes nothing.
       covers:     c-6, c-7, c-9
       depends_on: [t-5]
       contract:   - grant of [custom:1] on [c1,c2]: 0 POSTs to /control/clients/update on create,
                     end and expiry; any `custom:` id in a clients/update body fails the test
                   - grant of [youtube, custom:1]: youtube removed from blocked_services AND one
                     `@@||d^$client='c1'|'c2'` per domain in the region (c-6)
                   - End: exceptions gone; expiry via fake clock: exceptions gone (c-7)
                   - shared TV: grants for child A and child B both cover custom:1 on TV; ending A
                     keeps TV in the @@ line; ending B removes it
                   - set_rules failing during End → *ErrRevertFailed, grant still active, @@ lines
                     still present (never "ended but still unblocked")
                   - Extend issues 0 set_rules POSTs and the region is byte-identical
                   - every op leaves the lines outside the markers byte-identical (c-9)
       status:     pending

  t-7  Resync region on client rename and adoption
       files:      internal/grants/clients.go, internal/grants/rename.go,
                   internal/grants/clients_test.go, internal/grants/rename_test.go
       description: AdoptClients, CreateClient (with child) and RenameClient call syncRules after
                   their store write; baselineFrom filters custom ids out of the set handed to
                   AdoptBlockedServices / NewClient.Blocked.
       covers:     c-6, c-8
       depends_on: [t-5]
       contract:   - rename client "Tab" → "Tablet" mid-grant: region has no `'Tab'` and the block
                     and @@ lines name `'Tablet'` immediately (no wait for the tick); the stale name
                     would silently stop matching and leave the child unblocked
                   - a device added to a child via CreateClient is in the custom block lines
                     before the call returns
                   - AdoptClients while the child has a [custom:1] grant: the clients/update body
                     contains no `custom:` id
                   - rename where AdGuard rename succeeds but store fails (existing rollback path):
                     region not rewritten
       status:     pending

  t-8  Converge region in every reconcile pass
       files:      internal/grants/reconcile.go, internal/grants/reconcile_test.go
       description: Reconcile syncs the region after the overdue-grant loop on every pass —
                   including when there are no active grants and when the clients read fails.
                   Stats gains RulesWritten; Run logs a pass that rewrote the region at info and a
                   malformed region at error.
       covers:     c-8, c-9
       depends_on: [t-5]
       contract:   - zero active grants + one custom service + empty user_rules → one pass appends
                     the region; the current `len(grants)==0` early return would skip it and fails
                   - parent hand-edits a line inside the markers (fake SetUserRules) → next pass
                     restores the rendered lines; a line added outside the markers survives
                   - converged state → second pass issues 0 set_rules POSTs (no 60 s churn)
                   - /control/clients failing still lets the region converge
                   - grant overdue at startup (restart gap): one Start() pass removes its @@ lines
                     and marks it expired
                   - malformed region → error logged, 0 POSTs, pass continues grant repair
       status:     pending

  t-9  Custom services CRUD API and merged catalogue
       files:      internal/api/custom_services.go, internal/api/custom_services_test.go,
                   internal/api/api.go, internal/api/clients.go
       description: Routes GET/POST /api/v1/custom-services, PUT/DELETE /custom-services/{id}
                   behind the session gate. Validation cheapest-first: body bound, name 1–64 runes,
                   1–50 domains each through NormalizeDomain (400 naming the domain), then one
                   catalogue read for case-insensitive built-in name clash (409), then the engine.
                   Store conflicts → 409; in-use delete → 409 naming the grant. handleServices
                   appends custom services (`custom: true`, `icon: ""`). Deps gains CustomServices.
       covers:     c-1, c-2, c-3, c-4, c-5
       depends_on: [t-5]
       contract:   - POST with domains ["ok.com", "https://x.com"] → 400 naming `https://x.com`,
                     then GET lists nothing and the fake saw 0 set_rules POSTs (c-2 "nothing stored")
                   - POST name "YouTube" when the catalogue has "YouTube" → 409; catalogue read
                     failing → 502 and nothing stored (fail closed, not "skip the check")
                   - POST name "Games" twice (different case) → 409; domain owned by another custom
                     service → 409 naming it
                   - DELETE while a grant is active → 409 whose message contains the grant id; the
                     service still in GET
                   - set_rules failing on POST → 201 with `applied: false`
                   - GET /services lists catalogue entries then `{id:"custom:1", icon:"", custom:true}`
                   - unauthenticated POST → 401, missing CSRF header → 403 (route registered inside
                     the existing chain)
       status:     pending

  t-15 Services section in settings
       files:      web/src/lib/Services.svelte, web/src/lib/Services.test.ts, web/src/App.svelte,
                   web/src/lib/Home.svelte
       description: `/services` page listing custom services (letter avatar, name, domains) with
                   create/edit (name + one-domain-per-line textarea) and inline Delete? Confirm/
                   Cancel; Home nav gets "Services" next to "Buttons". Re-render from server
                   responses; `applied: false` shows "saved — AdGuard will catch up".
       covers:     c-1, c-3, c-4
       depends_on: [t-13, t-14]
       contract:   - server 400 naming a domain shows that message and keeps the textarea draft
                   - DELETE answered 409 shows the server message naming the grant and the row
                     stays listed
                   - edit save replaces the row from the response (not the local draft)
                   - `applied: false` response shows the catch-up note
                   - App renders Services for route 'services'; Home has a Services nav button
       status:     pending

Wave 4 (depends t-6, t-9 and friends)
  t-10 Accept custom ids in grant and button APIs
       files:      internal/api/grants.go, internal/api/buttons.go, internal/api/grants_test.go,
                   internal/api/buttons_test.go
       description: Service-id validation for POST /grants and PUT /buttons accepts ids from the
                   custom store as well as the catalogue (custom list read before the catalogue);
                   button `icon` stays catalogue-only.
       covers:     c-5, c-6
       depends_on: [t-6, t-9]
       contract:   - POST /grants {services:["custom:1"]} → 201 and the region holds the @@ lines
                     (end-to-end through the real engine + fake)
                   - POST /grants {services:["custom:99"]} (deleted/unknown) → 422; a version
                     accepting any `custom:` prefix fails it
                   - PUT /buttons with services ["custom:1"] → 200; icon "custom:1" → 422
                     `buttons[0].icon`
                   - grant for custom:1 then DELETE /custom-services/1 → 409; after End → 200 and
                     the button holding only custom:1 is gone from GET /buttons
       status:     pending

  t-11 Fold custom services into child blocked view
       files:      internal/blocked/blocked.go, internal/blocked/blocked_test.go,
                   internal/api/blocked.go, internal/api/blocked_test.go
       description: blocked.Input gains Custom []CustomService; each gets a ServiceState computed
                   from desired state: per present mapped client, unblocked iff an active grant
                   covers it there, else blocked; partial/differs/unblocked_by as for built-ins.
                   handleBlocked reads the custom list; a failed read degrades to built-ins + warn.
       covers:     c-5
       depends_on: [t-9]
       contract:   - child with clients [c1,c2], no grant → custom service `blocked`
                   - own grant on [c1,c2] → `unblocked`; another child's grant on shared c2 only →
                     `partial`, differs [c2], unblocked_by names that child's grant
                   - a missing (AdGuard-unknown) client does not count toward partial
                   - custom list read failing → 200 with built-ins only (no 500)
                   - custom entries carry `icon: ""` and `custom: true`
       status:     pending

  t-12 Wire custom services into startup and restart
       files:      cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go
       description: Pass the store as api.Deps.CustomServices; startup reconcile already runs the
                   region sync. End-to-end restart tests against adguardtest.
       covers:     c-1, c-8, c-9
       depends_on: [t-8, t-9]
       contract:   - TestRun_CustomServiceSurvivesRestart: create via API, stop, start on the same
                     data_dir → GET /custom-services lists it (c-1)
                   - TestRun_RegionRestoredAtStartup: wipe the region in the fake while stopped,
                     start → region present before the first /healthz answers (c-8)
                   - user_rules lines outside the markers seeded before the first start are
                     byte-identical after create + restart + reconcile tick (c-9)
       status:     pending
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 create + persists | t-4 (schema/reopen), t-5 (engine create), t-9 (API), t-15 (UI), t-12 (restart e2e) |
| c-2 non-hostname → 400, nothing stored | t-2 (validator table), t-9 (400 + nothing stored + 0 writes) |
| c-3 edit name/domains, region follows | t-4 (atomic domain swap), t-5 (region exactly new set), t-9 (API), t-15 (UI) |
| c-4 delete, no rules left | t-4 (409 / button strip), t-5 (region clean, in-use race), t-9 (API), t-15 (UI) |
| c-5 appears in grant form / buttons / blocked view | t-13 (types + avatar), t-14 (form/Home/buttons), t-9 (merged /services), t-10 (ids accepted), t-11 (blocked view) |
| c-6 grant writes `@@` per domain | t-2 (rendering), t-6 (create path), t-7 (rename/adopt keep it targeted), t-10 (API e2e) |
| c-7 end/expiry remove, ref-counted on shared | t-2 (ref-count by derivation), t-6 (end/expiry, shared TV, fail-closed) |
| c-8 reconciler converges at startup + tick | t-8 (pass), t-7 (immediate resync on mapping change), t-12 (startup e2e) |
| c-9 outside-region lines byte-identical; missing region appended | t-1 (splice property), t-3 (RMW carries every line), t-5, t-6, t-8, t-12 (per-operation assertions) |

All 9 criteria covered.

## Judgment calls

- Region is re-rendered whole from state on every change, not patched per operation — rejected incremental add/remove of rule lines because two code paths (grant ops vs reconciler) would drift and ref-counting would need its own bookkeeping.
- Custom ids are `custom:<n>` — rejected bare integers or slugs because both can collide with AdGuard catalogue ids, and a collision would route a custom id into `blocked_services`.
- Custom ids are filtered out of every `blocked_services` write (t-6 create/revert, t-7 adoption) as their own tested risk — rejected relying on AdGuard ignoring unknown ids; unverified, and a 400 would fail the whole client write.
- Malformed markers (begin without end, duplicate pair) → refuse to write and report, never repair — rejected auto-repair because guessing where a parent's region ends can delete their own rules (r-02).
- Any store read failure during sync → no AdGuard write — rejected "render what we have", which on a failed grant read renders an empty exception set (re-block early) or on a failed service read an empty region (unblock everything).
- End/expiry write the region BEFORE the status CAS, and a failed write leaves the grant active — rejected CAS-then-sync because a failed sync would leave a grant "ended" while the child stays unblocked until the next tick; this matches the built-in revert semantics.
- Store is intent, AdGuard write failure is `applied: false` + reconciler — rejected rolling back the store on a region-write failure, consistent with locked partial_apply for grants.
- Built-in name clash check fails closed (502) when the catalogue is unreachable — rejected skipping the check, which would let a duplicate "YouTube" in permanently.
- Delete runs under the engine mutex with the in-use check in the same store tx — rejected an API-level pre-check, which races a concurrent grant create for the same service.
- Blocked view derives custom state from stored intent (grants + mapping), not by parsing the live region — rejected a second parser over text a parent may have hand-edited; drift is corrected by the reconciler within one tick.
- Rename/adoption trigger an immediate resync (t-7) instead of waiting for the 60 s tick — a renamed `$client` silently stops matching, so the child would be unblocked for up to a minute; removals are left to the tick because stale rules only over-block (safe direction).
- Non-ASCII domains are rejected (enter punycode) — rejected silent IDNA conversion, which adds a dependency and a normalisation surface for little gain.
- Lost update with a parent editing user_rules in AdGuard's UI between our GET and POST cannot be closed (AdGuard has no CAS); mitigated by re-reading immediately before write and skipping equal writes. Noted, not tasked.
- Clients with per-client `filtering_enabled: false` ignore user_rules, so a custom block silently does not apply there — outside the criteria; flagged for the judge, not tasked (would be a blocked-view warning in a later phase).
- UI types/avatar (t-13) start in wave 1 against the wire shape fixed above rather than waiting for t-9 — t-9's contract pins the same shape, so drift fails a test on both sides.
- t-10 (API accepts custom ids in grants) waits on t-6 — landing it first would let `custom:` ids reach `blocked_services` through the unpatched engine.
