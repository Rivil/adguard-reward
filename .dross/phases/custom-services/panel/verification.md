# Panel draft — verification lens

Every task below was derived backward: first the test that would prove the criterion, then the smallest change that makes that test satisfiable. The architecture follows from those contracts. The managed region is a **pure function of intent**: `Render(custom services, mapped child clients, active-grant covers)`. It is spliced into the live `user_rules` by one locked read-modify-write. With that shape, c-6/c-7/c-8 become table tests over a pure function plus a small number of engine-level tests against `adguardtest`. Ref-counting (c-7) falls out of rendering the union of active covers, so it needs no separate bookkeeping to test.

Wire conventions the contracts assume. They are fixed here so wave-1 frontend and backend tasks agree:
- A custom service's catalogue id is `custom:<n>` (a colon never appears in AdGuard ids). The resource is `{id: n, service_id: "custom:n", name, domains[]}` at `/api/v1/custom-services[/{n}]`.
- `GET /api/v1/services` entries gain `custom: bool`. Custom entries carry `icon: ""`.
- AdGuard calls: `GET /control/filtering/status` → `user_rules[]`; `POST /control/filtering/set_rules` `{"rules":[...]}`.
- Region lines, in order: `! adguard-reward begin`, then all block rules `||d^$client='a'|'b'` sorted by (service id, domain), then all exceptions `@@||d^$client=...` sorted the same way, then `! adguard-reward end`.

```
Phase custom-services — 16 tasks across 5 waves

Wave 1
  t-1  Add domain validation and region renderer
       files:    internal/rules/domain.go, internal/rules/domain_test.go,
                 internal/rules/render.go, internal/rules/render_test.go
       description: Pure package. NormaliseDomain(s) lowercases and returns an
                 error for anything but a plain multi-label ASCII hostname.
                 QuoteClient(name) single-quotes and backslash-escapes
                 \ ' " , | $. Render(services, childClients, covers) returns
                 the deterministic region lines (markers included).
       covers:   c-2, c-6, c-7
       contract: - NormaliseDomain table: "https://a.com", "a.com/x", "a.com:443",
                   "*.a.com", "a .com", "a.com\n", "||a.com^", "a.com^",
                   "a.com$important", "/a\.com/", "com" (single label), "",
                   "ä.com" each return an error. "Example.COM" returns "example.com".
                   If any rejection branch is dropped, its row fails.
                 - QuoteClient(`Ann's "tab", |x`) == `'Ann\'s \"tab\"\, \|x'`.
                   An unescaped quote or pipe fails the exact-string assert.
                 - Render, 1 service [a.com,b.com], child clients [kid-phone,tv],
                   no covers → exactly 4 lines:
                   begin, `||a.com^$client='kid-phone'|'tv'`,
                   `||b.com^$client='kid-phone'|'tv'`, end. An adult client never
                   appears because it is not in childClients.
                 - Render with cover {custom:1, [kid-phone,tv]} adds exactly one
                   `@@||a.com^$client='kid-phone'|'tv'` per domain (c-6: one
                   exception per domain).
                 - Render with covers A{custom:1,[a-phone,tv]} and
                   B{custom:1,[b-phone,tv]}, then with only B: the a.com exception
                   is `'b-phone'|'tv'`. 'tv' survives because B still covers it
                   (c-7 ref-count) and 'a-phone' is gone.
                 - Zero child clients → no block line rendered (never an empty
                   `$client=`). Permuting the input slices gives byte-identical
                   output (determinism is what makes the no-op skip work).
       depends_on: []
       status:   pending

  t-2  Add marker-region splice
       files:    internal/rules/region.go, internal/rules/region_test.go
       description: Splice(current, region []string) ([]string, error) replaces
                 the lines from `! adguard-reward begin` through
                 `! adguard-reward end` with region and appends region when no
                 marker exists. A lone begin, a lone end, end-before-begin or a
                 duplicate pair returns ErrMalformedRegion.
       covers:   c-9
       contract: - Prefix/suffix property table (blank lines, duplicate lines,
                   trailing spaces, `! other` comments, a line containing
                   "adguard-reward begin" as a substring, CRLF-free unicode):
                   out[:len(prefix)] == prefix and out[len(out)-len(suffix):] ==
                   suffix, compared with reflect.DeepEqual. Any trim or reorder
                   outside the markers fails.
                 - No markers → result == current ++ region. A region that is
                   prepended or inserted mid-list fails.
                 - Splice(Splice(x, r), r) == Splice(x, r) (idempotent).
                 - The four malformed shapes each return ErrMalformedRegion with
                   a nil slice. A guessed repair fails.
       depends_on: []
       status:   pending

  t-3  Add AdGuard user_rules read and locked edit
       files:    internal/adguard/rules.go, internal/adguard/rules_test.go,
                 internal/adguard/adguardtest/server.go
       description: Client.UserRules(ctx) reads GET /control/filtering/status.
                 Client.EditUserRules(ctx, fn) re-reads under a dedicated mutex,
                 applies fn and POSTs set_rules {"rules":[...]} only when the
                 list changed (never null). The fake serves both endpoints, plus
                 SetUserRules/UserRules knobs and a set_rules fault knob.
       covers:   c-8, c-9
       contract: - EditUserRules with fn returning the same lines → 0
                   CountRequests("POST","/control/filtering/set_rules").
                 - The fake receives a set_rules body that json-decodes to exactly
                   {"rules":[...]} with the edited list (an empty list is [],
                   never null).
                 - 20 concurrent EditUserRules each appending one line →
                   the fake ends with all 20 lines. A missing mutex loses
                   updates and fails the count.
                 - fn error or a GET failure → no POST and the error is returned.
                 - A set_rules 500 from the fault knob → *StatusError.
       depends_on: []
       status:   pending

  t-4  Add custom services store and migration
       files:    internal/store/migrations/0007_custom_services.sql,
                 internal/store/custom_services.go,
                 internal/store/custom_services_test.go
       description: Tables custom_services (id AUTOINCREMENT, name, name_key
                 UNIQUE) and custom_service_domains (service_id FK cascade,
                 position, domain UNIQUE). Adds Create/Update/Get/List/Delete,
                 CustomServiceID(n)/IsCustomID. Delete runs in one tx: an active
                 grant covering the service gives *ErrServiceInUse{GrantID};
                 otherwise the id is stripped from button_services, emptied
                 buttons are deleted, matching button icons are nulled, and the
                 row is deleted.
       covers:   c-1, c-3, c-4
       contract: - Create then close + store.Open on the same dir → List
                   returns the same name and domains in order (c-1 persistence).
                 - Create "School" when "school" exists → *ErrCustomNameTaken.
                   Create with a domain owned by service 1 →
                   *ErrDomainTaken{Domain, Owner:"…"}. Either way List is
                   unchanged.
                 - Update [a,b]→[b,c] → Get returns [b,c]. An Update that hits a
                   domain conflict leaves the old [a,b] intact (tx rollback).
                 - Delete with active grant 7 on custom:1 → *ErrServiceInUse
                   {GrantID:7}, and the service and buttons are untouched. With
                   that grant ended → button [custom:1,youtube] becomes
                   [youtube], button [custom:1] is gone, and icon "custom:1"
                   reads back "".
                 - TestMigrate_Order lists 0007 last.
       depends_on: []
       status:   pending

  t-5  Add custom-services SPA client and route
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts
       description: Service gains optional `custom`. Adds the CustomService type
                 and list/create/update/deleteCustomService. Adds Route
                 'services' ↔ '/services'.
       covers:   c-1, c-5
       contract: - createCustomService('Portal',['a.com']) issues POST
                   /api/v1/custom-services with body {name,domains} and the CSRF
                   header.
                 - updateCustomService(3,…) → PUT /api/v1/custom-services/3.
                   deleteCustomService(3) → DELETE on the same path.
                 - A 409 {message:"…grant 7…", grant_id:7} becomes an ApiError
                   whose messageFor echoes the message and carries grantId 7.
                 - navigate('services') pushes '/services'. A popstate on
                   '/services' sets route 'services'.
       depends_on: []
       status:   pending

Wave 2 (depends wave 1)
  t-6  Sync managed region through grant lifecycle
       files:    internal/grants/rules.go, internal/grants/rules_test.go,
                 internal/grants/grants.go
       description: The engine gains syncRules(ctx, actx, excludeGrant). It
                 loads custom services, children and active grants, renders,
                 then EditUserRules(Splice). Create sends only built-in ids to
                 RemoveBlockedServices, then syncs. revert hands only built-in
                 ids to revertPlan, then syncs with exclude=g.ID. A rules failure
                 fails every client of the grant (Create reports it, End/expiry
                 leave the grant active). Create refuses an unknown custom id
                 under the mutex.
       covers:   c-6, c-7, c-9
       contract: - Create grant [custom:1] for a child on [kid-phone,tv] → fake
                   user_rules region holds `@@||a.com^$client='kid-phone'|'tv'`
                   for each domain, and 0 POST /control/clients/update (no
                   "custom:1" leaks into blocked_services).
                 - Mixed grant [youtube,custom:1] removes youtube from
                   blocked_services and writes the custom exceptions.
                 - Shared tv: grants A (sibling 1) and B (sibling 2) on custom:1.
                   End A → the exception lists 'tv' and B's phone, not A's
                   phone. Then expire B via the fake clock → no `@@` line remains
                   (c-7 on End and on timer expiry).
                 - Seed user_rules ["! mine","||x^","@@||y^"] → after Create,
                   Extend, End and timer expiry, lines outside the markers equal
                   the seed exactly. Extend issues 0 set_rules POSTs.
                 - set_rules fault on Create → Result.Failed == the grant's
                   clients. On End → *ErrRevertFailed and the grant is still
                   active.
                 - Create with custom:99 (no such service) → error, and no grant
                   row is stored.
       depends_on: [t-1, t-2, t-3, t-4]
       status:   pending

  t-7  Merge custom services into the API catalogue
       files:    internal/api/api.go, internal/api/clients.go,
                 internal/api/catalogue_test.go, cmd/adguard-reward/main.go
       description: Adds a Deps.CustomServices store slice (List) wired to st in
                 main. a.catalogue(ctx) = AdGuard.Services ∪ custom services as
                 {id:"custom:n", name, icon:"", custom:true}. GET /services
                 serves the merged list.
       covers:   c-5
       contract: - With custom service "Portal" stored, GET /api/v1/services
                   contains {id:"custom:1",name:"Portal",icon:"",custom:true},
                   and every built-in entry has custom:false.
                 - AdGuard catalogue 502 → GET /services is 502 (custom entries
                   never mask an outage).
                 - TestRun_ButtonsWired-style boot: GET /services through the
                   real main wiring includes the stored custom service (a
                   missing main.go wire panics or omits it).
       depends_on: [t-4]
       status:   pending

  t-8  Add letter-avatar ServiceIcon to grant form
       files:    web/src/lib/ServiceIcon.svelte, web/src/lib/ServiceIcon.test.ts,
                 web/src/lib/GrantForm.svelte, web/src/lib/GrantForm.test.ts
       description: ServiceIcon renders the data-URL <img> when icon is
                 non-empty and otherwise a span avatar with the uppercase first
                 character of the name. GrantForm uses it for every row.
       covers:   c-5
       contract: - ServiceIcon {name:'school portal',icon:''} renders text "S"
                   and no <img>. {icon:'PHN2Zy8+'} renders an <img> with the
                   data URL.
                 - GrantForm with a custom service in its catalogue lists a
                   "school portal" checkbox with avatar "S". Ticking it puts
                   'custom:1' into value.services in tick order. (The button
                   editor reuses GrantForm, so this also covers "button
                   service".)
       depends_on: [t-5]
       status:   pending

Wave 3
  t-9  Converge managed region in reconcile pass
       files:    internal/grants/reconcile.go, internal/grants/reconcile_test.go
       description: Reconcile drops the zero-grants early return and ends every
                 pass with syncRules after overdue reverts. Stats gains
                 RulesWritten. A malformed region is logged as an error and not
                 written.
       covers:   c-8, c-9
       contract: - Custom service stored, no grants, empty user_rules →
                   one Reconcile writes begin/block lines/end. Today's early
                   return would skip it and fail.
                 - Tamper inside the markers (delete a block line, add
                   `@@||a.com^$client='kid-phone'`) → Reconcile restores the
                   exact Render output. A second Reconcile issues 0 set_rules
                   POSTs.
                 - Overdue grant on custom:1 (restart gap, fake clock past
                   ends_at) → after Reconcile no `@@` line remains and the grant
                   is expired.
                 - Foreign lines before and after the region are byte-identical
                   after every pass above.
                 - Only a begin marker present → Reconcile returns or logs
                   ErrMalformedRegion and 0 set_rules POSTs.
       depends_on: [t-6]
       status:   pending

  t-10 Add engine custom-service CRUD under mutex
       files:    internal/grants/custom.go, internal/grants/custom_test.go,
                 internal/grants/grants.go
       description: Engine.CreateCustomService / UpdateCustomService /
                 DeleteCustomService each do the store write and then syncRules
                 inside the engine mutex. The row is the intent: a rules-write
                 failure returns Applied=false and does not roll back. The Store
                 interface gains the custom methods.
       covers:   c-1, c-3, c-4, c-9
       contract: - Create [a.com] with children on [kid-phone,kid-tablet] and
                   adult client "dad-laptop" unmapped → region has
                   `||a.com^$client='kid-phone'|'kid-tablet'`, and "dad-laptop"
                   appears nowhere.
                 - Update [a,b]→[b,c] with an active grant on it → region lines
                   mention b.com and c.com (block and @@), and no line contains
                   a.com (c-3).
                 - Delete → no region line contains any of its domains (c-4).
                   Delete with active grant → *store.ErrServiceInUse and 0
                   set_rules POSTs.
                 - Serialisation: Delete and a grant Create on the same service
                   issued concurrently end in exactly one of {grant exists +
                   409, service gone + Create refused}. They never produce a
                   grant on a deleted service.
                 - set_rules fault on Create → Applied=false, service still
                   listed, and the next Reconcile writes its block lines.
                 - Outside-marker seed lines unchanged after create, edit and
                   delete (c-9).
       depends_on: [t-6]
       status:   pending

  t-11 Accept custom ids in grants and buttons API
       files:    internal/api/grants.go, internal/api/grants_test.go,
                 internal/api/buttons.go, internal/api/buttons_test.go
       description: POST /grants and PUT /buttons (services and icon) validate
                 against a.catalogue instead of AdGuard.Services.
       covers:   c-5, c-6
       contract: - POST /grants {services:["custom:1"]} → 201 through the real
                   engine harness, and the fake's region gains the `@@` lines.
                   Before the change it is 422 "unknown service".
                 - POST /grants ["custom:99"] → 422 naming custom:99.
                 - PUT /buttons with services ["custom:1"] and icon "custom:1"
                   → 200 and GET echoes both. ["custom:99"] → 422
                   buttons[0].services.
       depends_on: [t-6, t-7]
       status:   pending

  t-12 Add custom services to child blocked view
       files:    internal/blocked/blocked.go, internal/blocked/blocked_test.go,
                 internal/api/blocked.go, internal/api/blocked_test.go
       description: blocked.Input gains Custom []adguard.Service. A custom id
                 counts as blocked on a present mapped client unless an active
                 GrantCover covers (id, client). unblocked_by is reused. The
                 handler feeds stored custom services and omits them when the
                 grants read failed.
       covers:   c-5
       contract: - Child on [kid-phone,tv], custom:1, no grants → state
                   "blocked". A grant covering both → "unblocked". A sibling's
                   grant covering only tv → "partial", differs ["tv"],
                   unblocked_by names the sibling's grant.
                 - Missing client (not in AdGuard) is ignored for custom state,
                   the same as for built-ins.
                 - Grants read fails → custom entries absent from services[],
                   built-ins still present (never a false "blocked").
                 - GET /children/{id}/blocked JSON includes the custom service
                   with icon "" and its state.
       depends_on: [t-7]
       status:   pending

  t-13 Show custom services on Home and Buttons
       files:    web/src/lib/Home.svelte, web/src/lib/Home.test.ts,
                 web/src/lib/Buttons.svelte, web/src/lib/Buttons.test.ts
       description: Home's blocked list and button tiles use ServiceIcon (a
                 tile whose first or chosen service is custom shows the
                 letter). The Buttons icon preview and select include custom
                 services. The Home header gains a "Services" nav button next to
                 Buttons.
       covers:   c-5
       contract: - Home blocked list with {id:'custom:1',name:'Portal',icon:'',
                   state:'blocked'} renders avatar "P" and the blocked state
                   label.
                 - A button [custom:1] renders avatar "P" (iconFor no longer
                   returns null for a custom first service).
                 - Buttons: selecting icon custom:1 previews avatar "P", and
                   saving sends icon 'custom:1'.
                 - Clicking Home's "Services" sets location '/services'.
       depends_on: [t-8]
       status:   pending

  t-14 Add Services settings page
       files:    web/src/lib/Services.svelte, web/src/lib/Services.test.ts,
                 web/src/App.svelte, web/src/App.test.ts
       description: Route 'services' renders Services.svelte. It lists custom
                 services with ServiceIcon and domains. The create/edit form has
                 a name plus a textarea with one domain per line (trimmed, blank
                 lines dropped). Delete uses an inline Confirm/Cancel. Every
                 mutation re-renders from the server response.
       covers:   c-1, c-2, c-3, c-4
       contract: - Submitting name "Portal" and "a.com\n\n b.com " calls
                   createCustomService('Portal',['a.com','b.com']), and the new
                   row appears from the response.
                 - A 400 answer shows the server message inline, keeps the
                   textarea text and leaves the list unchanged.
                 - Edit prefills name and domains, and Save calls
                   updateCustomService with the edited list.
                 - Delete → Confirm calls deleteCustomService. A 409 shows the
                   message naming the grant and the row stays. No
                   window.confirm is called.
                 - App with route '/services' at load renders the Services
                   heading (a reload stays on the page).
       depends_on: [t-5, t-8]
       status:   pending

Wave 4
  t-15 Add custom-services CRUD endpoints
       files:    internal/api/custom_services.go,
                 internal/api/custom_services_test.go, internal/api/api.go
       description: GET/POST /custom-services and PUT/DELETE
                 /custom-services/{id} behind the session gate. Name 1–64 runes
                 trimmed. Domains go through rules.NormaliseDomain → 400
                 naming domains[i]. A name equal to a built-in catalogue name
                 (case-insensitive) → 409. Store conflicts → 409. In-use →
                 409 {grant_id}. Unknown → 404. A catalogue read failure →
                 502 before any write.
       covers:   c-1, c-2, c-3, c-4
       contract: - POST {name:"Portal",domains:["a.com"]} → 201
                   {id,service_id:"custom:1",...}. A following GET lists it.
                 - For each c-2 bad form (scheme, path, port, `*.`, space, "\n",
                   `||`, `^`, `$`, `/re/`): POST → 400 with a message naming
                   domains[0]. GET stays [] and the fake has 0 set_rules POSTs.
                   PUT with a bad domain → 400 and the stored domains are
                   unchanged.
                 - POST name "youtube" (fixture built-in "YouTube") → 409.
                   Duplicate custom name in any case → 409. A domain owned by
                   another service → 409 naming that service.
                 - PUT domains [b,c] over [a,b] → 200, and the fake region has
                   no a.com line.
                 - DELETE with active grant 7 → 409 body grant_id 7, message
                   contains "7", service still listed. Without a grant → 204,
                   and the region has no line for its domains.
                 - Missing CSRF header → 403. No session → 401.
       depends_on: [t-1, t-7, t-10]
       status:   pending

Wave 5
  t-16 Prove restart persistence and startup convergence
       files:    cmd/adguard-reward/main_test.go
       description: End-to-end tests through run(): custom services survive a
                 restart, and the startup reconcile (before the listener)
                 converges the region after a gap.
       covers:   c-1, c-8, c-9
       contract: - TestRun_CustomServicesSurviveRestart: POST via the API, stop,
                   run() on the same data_dir → GET /custom-services and
                   GET /services both list it.
                 - TestRun_CustomRulesRestoredAtStartup: store holds a custom
                   service plus a grant whose ends_at passed while down, and the
                   fake holds a stale `@@` region and foreign lines around it →
                   by the first /healthz answer the region has the block lines
                   and no `@@`, the grant is expired, and the foreign lines are
                   identical.
                 - TestRun_CustomRulesReconcilerTick: with reconcileInterval
                   overridden, a manual edit inside the markers is undone
                   within one tick.
       depends_on: [t-9, t-11, t-15]
       status:   pending
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 create + persist across restarts | t-4 (store reopen), t-10 (engine create writes block rules), t-15 (POST/GET), t-5, t-14 (UI), t-16 (run() restart) |
| c-2 non-hostname domain → 400, nothing stored | t-1 (NormaliseDomain table), t-15 (400 + empty list + 0 set_rules), t-14 (inline 400) |
| c-3 edit name/domains → region exactly the new list | t-4 (atomic domain replace), t-10 (region after edit, incl. live grant), t-15 (PUT), t-14 (UI) |
| c-4 delete → no rule for its domains | t-4 (409 in-use + button strip), t-10 (region empty of domains, serialised vs grant create), t-15 (DELETE 409/204), t-14 (UI) |
| c-5 custom alongside built-ins in grant form, button editor, blocked view | t-7 (merged /services), t-11 (grants/buttons accept ids), t-12 (blocked state), t-8 (GrantForm / button editor), t-13 (Home + Buttons), t-5 |
| c-6 grant writes one `@@` per domain covering grant clients | t-1 (Render), t-6 (engine Create), t-11 (POST /grants end-to-end) |
| c-7 end/expire removes exceptions, ref-counted on shared clients | t-1 (Render union), t-6 (End + timer expiry on shared tv) |
| c-8 reconciler converges at startup and on tick | t-9 (pass incl. zero grants, tamper, restart gap), t-3 (no-op skip), t-16 (startup before listener, tick) |
| c-9 outside-marker lines byte-identical; missing region appended | t-2 (Splice property), t-3 (set_rules body), t-6 (grant/extend/end/expiry), t-9 (reconcile), t-10 (create/edit/delete), t-16 |

All 9 criteria covered.

## Judgment calls

- **Region is rendered from intent and fully rewritten, not patched incrementally.** Rejected: per-operation add/remove of individual lines. With a full rewrite, c-7 ref-counting and c-8 tamper-repair become pure-function tests, and one code path serves every trigger.
- **Malformed markers (lone begin/end, duplicate pair) → refuse the write and log an error.** Rejected: treating a lone begin as "region to EOF". That could swallow user lines, which violates r-02 and c-9. The cost is that c-8 does not self-heal a deleted marker. That is deliberate, and t-2/t-9 pin it.
- **Custom catalogue id `custom:<n>` stored in the existing grant_services / button_services string columns.** Rejected: a separate grant_custom_services table. Reusing the columns keeps overlap detection, `heldOpen`, and the API's single services[] unchanged. The engine splits on the prefix so a custom id never reaches blocked_services, and t-6 asserts that with 0 clients/update.
- **Custom block state in the blocked view comes from intent (stored services + active grant covers), not from parsing AdGuard's live user_rules.** Rejected: a rule parser. It would be a second syntax surface to test. The reconciler already converges the region within one tick.
- **A rules-write failure keeps the store row (Applied=false / grant stays active) and leaves the fix to the reconciler.** Rejected: rolling back. This mirrors the existing partial_apply / "row is the intent" contract.
- **Delete runs under the engine mutex, and the engine's Create re-checks that each custom id exists.** Rejected: an API-only check. A delete racing a grant create could otherwise strand a grant on a deleted service, which would silently break the delete_refs lock. t-10 tests the race.
- **Delete also nulls a button icon equal to the deleted id.** The spec says "strip from buttons". A dangling icon id would make the next whole-list PUT fail with 422.
- **Single-label domains ("com", "intranet") are rejected.** Rejected: allowing them. `||com^$client=…` would black-hole a TLD for the kids, and the spec's "plain hostname" is read as a registrable host.
- **Non-ASCII domains are rejected; punycode (`xn--`) is accepted.** Rejected: IDNA conversion, which would add a dependency and a normalisation surface for a rare case.
- **Child-mapping changes re-render on the next reconcile tick, not immediately.** No criterion asks for an immediate re-render, and wiring it into children/rename paths would widen scope. Flagged for the judge.
- **The API uses 400 (not this codebase's usual 422) for bad domains, because c-2 says 400.** Other validation (name length) keeps 422 for consistency. Only the domain path is pinned to 400 by t-15.
- **Services page is a separate `/services` route with a Home nav button next to "Buttons".** Rejected: embedding a section inside Buttons.svelte. Home's header is the de-facto settings nav, and a separate page keeps Buttons.test.ts untouched by CRUD tests.
- **Dedicated end-to-end task (t-16) instead of folding restart tests into the API task.** c-1's "across restarts" and c-8's "at startup" are only observable through run(), so they get their own contract-owning task.
