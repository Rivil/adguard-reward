# custom-services — panel synthesis

Repo facts checked against the drafts' claims (branch `phase/custom-services`, 219a5a5):

- `Reconcile` returns early on zero active grants (`internal/grants/reconcile.go:41-43`). Risk t-8 and verification t-9 correctly target it; a region-only pass is impossible today.
- No AdGuard filtering endpoint is wrapped: there is no `filtering`/`user_rules` reference in `internal/adguard` and no fake handler for them in `adguardtest/server.go`. All three drafts correctly add them. The fake has `CountRequests`, `SetStatus` and `MaxInFlightUpdates` (clients/update only). Risk's "MaxInFlight on filtering paths" therefore needs a new knob.
- `baselineFrom` (`internal/grants/clients.go:147`) appends `g.Services` of every relevant active grant, and `AdoptClients` hands that set to `AdoptBlockedServices`. Unless that set is filtered, a `custom:<n>` id reaches `blocked_services` the moment a device joins a child that holds a custom grant. **Only risk (t-7) catches this.**
- `revert`/`unblock` pass `g.Services` straight to `Add/RemoveBlockedServices` (`grants.go:188`, `:207`). All three drafts partition these.
- `Services()` has four API call sites: `blocked.go:49`, `buttons.go:148`, `clients.go:275` and `grants.go:232`. Button `icon` is validated against the same catalogue read (locked `icon_source`: "any catalogue service may be the icon").
- The button editor embeds `GrantForm` (`Buttons.svelte:16`). Verification's claim that GrantForm covers "button service" is correct.
- There is no Settings page. Home's header nav (`Home.svelte:233-234`: Children, Buttons) is the de-facto settings nav, so all three drafts agree on a sibling `/services` route.
- The migrations run to `0006_button_icon.sql`, so `0007` is next. `api.Deps` has no custom-service field yet.

## Scores

| Draft | Criteria coverage | Test-contract specificity | Granularity | Wave correctness |
|---|---|---|---|---|
| risk (15 tasks / 4 waves) | 9/9, each criterion pinned at ≥2 layers. It is the only draft that covers the adopt/CreateClient `custom:` leak and rename staleness. | High. The contracts are written as failure modes ("a version that X fails it"), with fail-closed cases on read errors, malformed markers and catalogue outage. | Good. Every task is ≤4 files and within one layer. Two gaps: t-14 contracts on Home and Buttons but omits `Home.test.ts`/`Buttons.test.ts`, and the domain schema loses order (no `position`). | Correct, and t-10 is explicitly held behind t-6 to stop the leak. t-15→t-14 is a file-conflict dependency (Home.svelte), not a data one. Main wiring arrives late (wave 4). |
| mvp (6 tasks / 3 waves) | 9/9 nominally but thin. c-6/c-7/c-8 rest on one task (t-3), c-2 is tested only at the API, nothing covers malformed markers or the delete-vs-grant race, and the adopt leak is missed. | Medium. Concrete inputs and outputs, but few adversarial or failure-path assertions (no RMW concurrency, no read-failure fail-closed beyond status 502). | Too coarse. t-3 bundles render, sync, the lifecycle hooks and reconcile. t-4 (5 files) spans api and blocked, and t-6 has 5 files. Splice logic is buried in the adguard client. | Flawed. t-4 makes POST /grants accept `custom:1` in wave 2, in parallel with t-3, which is the task that strips custom ids from `blocked_services`. That leaves a window where custom ids leak to AdGuard. |
| verification (16 tasks / 5 waves) | 9/9, dense. It has the most exact region and ordering spec and adds the dangling button-icon fix. It misses the adopt/CreateClient leak and leaves rename staleness to the tick. | Highest. Exact strings, exact line counts, ordered output, a deterministic sort key, a 409 body shape and status codes per path. | Mostly good. t-5 (api.ts alone) is under the merge threshold. The catalogue split (t-7) is a sharper boundary than risk's. | Correct but deeper than needed (5 waves). CRUD endpoints wait until wave 4 behind engine CRUD. |

**Skeleton: risk.** It has the same depth of contracts as verification, plus the only plan that handles the confirmed `baselineFrom` leak and the rename/adoption staleness of `$client` rules. Its 4-wave graph is also shallower. Verification supplies most grafts: exact render strings, the catalogue split, the UI split, button-icon cleanup and the e2e tick test.

## Merged plan

Wire conventions (consolidated; risk's custom-service `id` was ambiguous between `n` and `custom:n`, so verification's explicit shape is used):

- A custom catalogue id is `custom:<n>`. The resource is `{id: n, service_id: "custom:n", name, domains[], applied}` at `/api/v1/custom-services[/{n}]`. `applied: false` means the row is stored but the region write failed, and the reconciler will converge it.
- `GET /api/v1/services` entries gain `custom: bool`. Custom entries carry `icon: ""`.
- AdGuard: `GET /control/filtering/status` → `user_rules[]`, and `POST /control/filtering/set_rules {"rules":[...]}` (never `null`).
- Region order: `! adguard-reward begin`, then all `||d^$client=…` block lines, then all `@@||d^$client=…` exception lines, each sorted by (service id, domain), then `! adguard-reward end`.

```
Phase custom-services — 17 tasks across 4 waves

Wave 1
  t-1  Splice managed region into user_rules                          [risk+verification]
       files:      internal/rules/region.go, internal/rules/region_test.go
       description: Pure Splice(current, managed []string) ([]string, error). It replaces the
                   lines between `! adguard-reward begin` / `! adguard-reward end` (markers kept)
                   with managed. When no region exists it appends begin+managed+end at the tail.
                   A lone begin, a lone end, end-before-begin or a second pair returns
                   ErrMalformedRegion with a nil slice and no repair.
       covers:     c-9
       contract:   - property/table test (blank lines, duplicate lines, trailing spaces, `!`/`#`
                     comments, unicode, and a line containing "adguard-reward begin" as a substring
                     rather than the exact marker): the prefix and suffix outside the markers are
                     reflect.DeepEqual before and after. A version that trims, dedupes, sorts or
                     substring-matches the marker fails it.
                   - no markers → output == current ++ [begin, managed..., end]. Prepending or
                     inserting after the first comment fails it.
                   - each of the 4 malformed shapes → ErrMalformedRegion and a nil slice. A version
                     that "repairs" by appending a second region fails it.
                   - Splice(Splice(x, m), m) == Splice(x, m)
       depends_on: []

  t-2  Validate domains and render region rules                       [risk+verification]
       files:      internal/rules/domain.go, internal/rules/domain_test.go,
                   internal/rules/render.go, internal/rules/render_test.go
       description: NormalizeDomain does three things. It lowercases, strips one trailing dot,
                   and accepts only ASCII LDH labels (≤253 chars, ≥2 labels, punycode accepted,
                   non-ASCII rejected). QuoteClient single-quotes a client name and
                   backslash-escapes `\ ' " , |`. Verification also escapes `$`; confirm that
                   against AdGuard's $client grammar before adding it. Render(services,
                   childClients, covers) returns the deterministic body lines in the order above:
                   per domain, a block line over the union of mapped child clients, and an @@ line
                   over the union of clients any active grant covers that service on (see D4).
                   Markers are excluded because t-1 owns them.
       covers:     c-2, c-6, c-7
       contract:   - NormalizeDomain table: `https://a.com`, `a.com/x`, `a.com:443`, `*.a.com`,
                     `a .com`, ` a.com`, `a.com\n`, `||a.com^`, `a.com^`, `a.com$important`,
                     `/a\.com/`, `@@a.com`, `com`, `""` and `ä.com` each return an error. Dropping
                     any check fails its row. `Example.COM.` → `example.com`.
                   - QuoteClient(`Ann's "tab", |x`) == `'Ann\'s \"tab\"\, \|x'` (exact string). A
                     client name containing `\n` is dropped from the line and logged, never split.
                   - Render with 1 service [a.com,b.com], child clients [kid-phone,tv] and no
                     covers → exactly `||a.com^$client='kid-phone'|'tv'` and the b.com twin. An
                     adult client is never present.
                   - zero mapped child clients → no block line at all. A version emitting an
                     unscoped `||d^` (which blocks adults) fails it.
                   - cover {custom:1,[kid-phone,tv]} adds exactly one `@@` line per domain (c-6).
                   - covers A{custom:1,[a-phone,tv]} + B{custom:1,[b-phone,tv]}, then only B → the
                     a.com exception is `'b-phone'|'tv'`: tv is kept and a-phone is gone (c-7
                     ref-count by derivation). Dropping both removes the line.
                   - 100 shuffled-input runs give byte-identical output.
       depends_on: []

  t-3  AdGuard user_rules locked read-modify-write + fake             [risk+verification+mvp]
       files:      internal/adguard/rules.go, internal/adguard/rules_test.go,
                   internal/adguard/adguardtest/server.go,
                   internal/adguard/adguardtest/testdata/filtering_status.json
       description: Client.EditUserRules(ctx, fn) runs under a dedicated mutex. It reads
                   GET /control/filtering/status .user_rules, applies fn, and POSTs set_rules only
                   when the result differs. The fake serves both endpoints from mutable state. It
                   adds SetUserRules/UserRules knobs and an in-flight counter on the filtering
                   paths, and reuses SetStatus for faults.
       covers:     c-9 (c-8 via no-op skip)
       contract:   - fn returning the same lines → 0 CountRequests("POST","/control/filtering/set_rules")
                   - GET 502 → fn is never called, 0 POSTs, error returned. fn error → 0 POSTs, error
                     returned.
                   - `user_rules: null` decodes to []. An appended region posts `{"rules":[...]}`,
                     never `null`.
                   - 20 concurrent EditUserRules, each appending a distinct line → all 20 present
                     and filtering in-flight max == 1. Removing the mutex fails it.
                   - the set_rules body carries every original line byte-for-byte, including blank
                     lines and trailing whitespace
                   - set_rules 500 → *StatusError
       depends_on: []

  t-4  Store custom services with ownership constraints               [risk+verification+mvp]
       files:      internal/store/migrations/0007_custom_services.sql,
                   internal/store/custom_services.go, internal/store/custom_services_test.go
       description: Two tables: custom_services(id, name, name_key UNIQUE) and
                   custom_service_domains(service_id FK CASCADE, position, domain UNIQUE).
                   Create, Get and List. Update replaces name and the whole domain set in one tx.
                   Delete runs in one tx: an active grant listing `custom:<id>` returns
                   *ErrServiceInUse{GrantID}. Otherwise it strips the id from button_services,
                   deletes buttons left empty, nulls any button icon equal to the id (see D6) and
                   deletes the row. Exports CustomServiceID, ParseCustomID and IsCustomServiceID.
       covers:     c-1, c-3, c-4
       contract:   - create, close, store.Open on the same dir → same name and domains in their
                     original order (c-1)
                   - "TikTok Web" then "tiktok web" → ErrNameTaken with one row. A domain owned by
                     A → *ErrDomainTaken{Owner: A} and nothing inserted. Update B to take A's domain
                     → same error, and B's old domains are intact (tx rollback).
                   - update {a,b}→{b,c} → exactly {b,c}. An injected failure after the delete step
                     rolls back to {a,b}.
                   - delete with active grant 7 on custom:1 → *ErrServiceInUse{GrantID:7}, with
                     the service, domains and buttons untouched. An ended or expired grant does
                     not block the delete.
                   - after a successful delete: [custom:1, youtube] → [youtube] (position kept), a
                     [custom:1]-only button is gone, and icon "custom:1" reads back ""
                   - IsCustomServiceID: "youtube" → false, "custom:7" → true, "custom:" → false.
                     TestMigrate_Order lists 0007 last.
       depends_on: []

  t-5  SPA custom-service client, route and letter avatar             [risk+verification]
       files:      web/src/lib/api.ts, web/src/lib/api.test.ts,
                   web/src/lib/ServiceIcon.svelte, web/src/lib/ServiceIcon.test.ts
       description: Service gains `custom: boolean`. Adds the CustomService type,
                   list/create/update/deleteCustomService, and Route 'services' ↔ '/services'.
                   ServiceIcon renders the data-URL <img> when icon is non-empty. Otherwise it
                   renders a span with the uppercased first grapheme of the name, or `?` when the
                   name is blank.
       covers:     c-1, c-5
       contract:   - ServiceIcon {name:'school portal', icon:''} renders "S" and no <img>. The
                     `iconUrl("")` data URL is a broken image. "  " → "?". A leading emoji renders
                     whole, not half a surrogate pair. A non-empty icon renders the <img>.
                   - createCustomService('Portal',['a.com']) → POST /api/v1/custom-services
                     {name,domains} with the CSRF header. update(3,…) → PUT …/3. delete(3) →
                     DELETE …/3.
                   - 400 → messageFor names the bad domain. 409 {message,grant_id:7} → ApiError
                     carrying grantId 7.
                   - navigate('services') pushes '/services'. popstate on '/services' sets route
                     'services'.
       depends_on: []

Wave 2
  t-6  Engine-owned region sync and custom-service CRUD                [risk+verification]
       files:      internal/grants/rules.go, internal/grants/rules_test.go,
                   internal/grants/grants.go
       description: syncRules(ctx, actx, exclude int64) runs under e.mu. It reads custom services,
                   children and active grants (dropping `exclude`), then runs Render →
                   EditUserRules(Splice). Any store read error aborts with no AdGuard call.
                   Engine.CreateService, UpdateService and DeleteService do the store op and then
                   syncRules under e.mu. They return {Service, Applied}; a region-write failure
                   gives Applied=false and keeps the row. The Store and AdGuard interfaces are
                   extended.
       covers:     c-1, c-3, c-4, c-9
       contract:   - a ListActiveGrants or custom-list read failure → 0 set_rules POSTs. A version
                     that renders an empty region on read failure (unblocking every kid) fails it.
                   - Create [a.com] with children on [kid-phone,kid-tablet] and adult "dad-laptop"
                     unmapped → `||a.com^$client='kid-phone'|'kid-tablet'`, and "dad-laptop"
                     appears nowhere
                   - Update {a,b}→{b,c} under an active grant → block and @@ lines name only b.com
                     and c.com, and no line contains a.com (c-3)
                   - Delete without a grant → no region line contains its domains, and lines
                     outside the markers are byte-identical (c-4, c-9). Delete with an active grant
                     → ErrServiceInUse and 0 POSTs.
                   - race (-race): concurrent Delete and grant Create on the same service end in
                     exactly one of {grant exists + 409, service gone + Create refused}, never a
                     grant on a deleted service
                   - set_rules failure → service stored, Applied=false, nil error. A malformed
                     region in the fake → Applied=false, 0 POSTs, user_rules unchanged.
       depends_on: [t-1, t-2, t-3, t-4]

  t-7  Merge custom services into the API catalogue and wire main     [verification+risk]
       files:      internal/api/api.go, internal/api/clients.go,
                   internal/api/catalogue_test.go, cmd/adguard-reward/main.go
       description: Deps gains CustomServices (List), wired to the store in main. a.catalogue(ctx)
                   returns AdGuard.Services plus the custom services as {id:"custom:n", name,
                   icon:"", custom:true}, and handleServices serves it. The raw AdGuard catalogue
                   stays reachable for built-in-only checks (see D7).
       covers:     c-5
       contract:   - with "Portal" stored, GET /api/v1/services lists the catalogue entries
                     (custom:false), then {id:"custom:1",name:"Portal",icon:"",custom:true}
                   - AdGuard catalogue 502 → /services 502. Custom entries never mask an outage.
                   - a TestRun_ButtonsWired-style boot through run() lists the stored custom
                     service. A missing main.go wire omits it or panics.
       depends_on: [t-4]

  t-8  Letter avatar in the grant form / button editor                [verification]
       files:      web/src/lib/GrantForm.svelte, web/src/lib/GrantForm.test.ts
       description: GrantForm renders every row through ServiceIcon. The button editor embeds
                   GrantForm (Buttons.svelte:16), so this is also c-5's "button service".
       covers:     c-5
       contract:   - a `custom: true` catalogue entry is listed as selectable with avatar "S" and is
                     NOT marked "no longer in catalogue"
                   - ticking it puts 'custom:1' into value.services in tick order
       depends_on: [t-5]

  t-9  Custom services on Home and in Buttons; Services nav           [verification+risk]
       files:      web/src/lib/Home.svelte, web/src/lib/Home.test.ts,
                   web/src/lib/Buttons.svelte, web/src/lib/Buttons.test.ts
       description: Home's button tiles and blocked list use ServiceIcon; iconFor no longer returns
                   null for a custom first service. The Buttons icon preview uses ServiceIcon, and
                   its select includes custom services (see D6). Home's header gains "Services"
                   next to "Buttons".
       covers:     c-5
       contract:   - Home blocked list {id:'custom:1',name:'Portal',icon:'',state:'blocked'} →
                     avatar "P" plus the blocked label. A partial state shows the partial badge.
                   - a button [custom:1] renders avatar "P" with no broken <img>
                   - in Buttons, selecting icon custom:1 previews "P", and saving sends
                     icon 'custom:1'
                   - clicking Home's "Services" sets location '/services'
       depends_on: [t-5]

  t-10 Services settings page                                          [risk+verification+mvp]
       files:      web/src/lib/Services.svelte, web/src/lib/Services.test.ts,
                   web/src/App.svelte, web/src/App.test.ts
       description: Route 'services' renders Services.svelte. It lists custom services (avatar,
                   name, domains). The create/edit form has a name and a one-domain-per-line
                   textarea (trimmed, blank lines dropped). Delete is an inline Confirm/Cancel.
                   Every mutation re-renders from the server response, and `applied:false` shows
                   "saved — AdGuard will catch up".
       covers:     c-1, c-2, c-3, c-4
       contract:   - "Portal" + "a.com\n\n b.com " → createCustomService('Portal',['a.com','b.com'])
                     and the row rendered from the response
                   - 400 → server message inline (naming the domain), textarea text kept, list
                     unchanged
                   - Edit prefills the form and Save calls updateCustomService. The row is replaced
                     from the response, not from the local draft.
                   - Delete fires nothing until Confirm. A 409 shows the message naming the grant
                     and the row stays. window.confirm is never called.
                   - an `applied:false` response shows the catch-up note. Loading '/services'
                     renders the Services heading.
       depends_on: [t-5]

Wave 3
  t-11 Apply grant lifecycle to custom services                       [risk+verification]
       files:      internal/grants/grants.go, internal/grants/grants_test.go
       description: Create, unblock, revert, End and expireLocked partition services: built-in ids
                   keep the blocked_services path, and custom ids never reach it. Create refuses an
                   unknown custom id under mu, then syncs with the new grant included. End and
                   expiry sync with the grant excluded BEFORE the status CAS; a failed region write
                   leaves the grant active (*ErrRevertFailed on End). See D1. Extend writes nothing.
       covers:     c-6, c-7, c-9
       contract:   - grant [custom:1] on [kid-phone,tv] → `@@||a.com^$client='kid-phone'|'tv'` per
                     domain and 0 POST /control/clients/update on create, end and expiry. Any
                     `custom:` in a clients/update body fails it.
                   - [youtube, custom:1] → youtube removed from blocked_services AND the @@ lines
                     written (c-6)
                   - shared tv: grants A and B on custom:1. End A → the exception keeps tv and B's
                     phone and drops A's phone. Expiring B via the fake clock → no `@@` line remains
                     (c-7 on End and on expiry).
                   - seed ["! mine","||x^","@@||y^"] → identical outside the markers after Create,
                     Extend, End and expiry. Extend issues 0 set_rules POSTs.
                   - set_rules fault: on Create → Result.Failed == the grant's clients. On End →
                     *ErrRevertFailed, grant still active, @@ lines still present.
                   - Create with custom:99 → error, and no grant row stored
       depends_on: [t-6]

  t-12 Keep custom ids out of adoption; resync on rename/adoption     [risk]
       files:      internal/grants/clients.go, internal/grants/rename.go,
                   internal/grants/clients_test.go, internal/grants/rename_test.go
       description: baselineFrom filters custom ids out of the set handed to AdoptBlockedServices
                   and NewClient.Blocked. This is a confirmed leak: clients.go appends g.Services
                   of active grants. AdoptClients, CreateClient (with a child) and RenameClient
                   call syncRules after their store write (see D2).
       covers:     c-6, c-8
       contract:   - AdoptClients while the child has a [custom:1] grant → no clients/update or
                     clients/add body contains `custom:`
                   - rename "Tab" → "Tablet" mid-grant → the region has no `'Tab'`, and both the
                     block and @@ lines name `'Tablet'` before the call returns
                   - a device added to a child via CreateClient is in the custom block lines before
                     the call returns
                   - AdGuard rename succeeds but the store fails (existing rollback path) → region
                     not rewritten
       depends_on: [t-6]

  t-13 Converge region in every reconcile pass                        [risk+verification]
       files:      internal/grants/reconcile.go, internal/grants/reconcile_test.go
       description: Drop the zero-grants early return (reconcile.go:41). Every pass ends with
                   syncRules after the overdue loop, including when the clients read fails. Stats
                   gains RulesWritten. A rewritten region is logged at info and a malformed region
                   at error with no write.
       covers:     c-8, c-9
       contract:   - zero grants, one custom service, empty user_rules → one pass appends the
                     region. Today's early return fails this.
                   - tamper inside the markers (delete a block line, add
                     `@@||a.com^$client='kid-phone'`) → the next pass restores the exact Render
                     output. A line added outside the markers survives. A second pass issues 0
                     set_rules POSTs.
                   - /control/clients failing still lets the region converge
                   - overdue grant on custom:1 (restart gap) → after one pass no @@ line remains and
                     the grant is expired
                   - a service stored with Applied=false (set_rules fault) → the next pass writes
                     its block lines
                   - only a begin marker → error logged, 0 POSTs, grant repair continues
       depends_on: [t-6]

  t-14 Custom services CRUD endpoints                                  [risk+verification+mvp]
       files:      internal/api/custom_services.go, internal/api/custom_services_test.go,
                   internal/api/api.go
       description: GET and POST /api/v1/custom-services, plus PUT and DELETE /{n}, behind the
                   session gate. Validation runs cheapest-first: body bound, then name 1–64 runes
                   (422), then 1–50 domains each through NormalizeDomain (400 naming domains[i]),
                   then one raw-catalogue read for a case-insensitive built-in name clash (409; a
                   read failure is 502 before any write), then the engine. Store conflicts → 409.
                   In-use → 409 {grant_id}. Unknown → 404.
       covers:     c-1, c-2, c-3, c-4
       contract:   - POST each c-2 bad form (scheme, path, port, `*.`, space, "\n", `||`, `^`, `$`,
                     `/re/`), alone and as ["ok.com", bad] → 400 naming the bad domain. GET stays []
                     and 0 set_rules POSTs. PUT with a bad domain → 400 and the stored domains are
                     unchanged.
                   - POST {Portal,[a.com]} → 201 {id,service_id:"custom:1",…}. The fake region
                     carries `||a.com^$client=` naming every mapped child client and no unmapped one.
                   - "youtube" vs fixture "YouTube" → 409. Catalogue 502 → 502 and nothing stored
                     (fail closed). A duplicate custom name in any case → 409. A domain owned
                     elsewhere → 409 naming the owner.
                   - PUT [a,b]→[b,c] → 200 and no region line contains a.com
                   - DELETE with active grant 7 → 409 with body grant_id 7 and "7" in the message,
                     still listed. Without a grant → 204 and no region line for its domains.
                   - set_rules failing on POST → 201 with applied:false. No session → 401. Missing
                     CSRF → 403.
       depends_on: [t-6, t-7]

  t-15 Fold custom services into the child blocked view               [risk+verification]
       files:      internal/blocked/blocked.go, internal/blocked/blocked_test.go,
                   internal/api/blocked.go, internal/api/blocked_test.go
       description: blocked.Input gains Custom. Each custom service gets a state derived from
                   intent: on each present mapped client it is unblocked iff an active GrantCover
                   covers (id, client), otherwise blocked. partial, differs and unblocked_by work
                   as for built-ins. The handler feeds the stored custom services. If the custom
                   list or the grants read fails, the custom entries are omitted, never reported
                   as a false "blocked", and there is no 500.
       covers:     c-5
       contract:   - child on [c1,c2] with no grant → "blocked". Own grant on both → "unblocked". A
                     sibling's grant on shared c2 only → "partial", differs ["c2"], unblocked_by
                     names that grant.
                   - a client missing from AdGuard does not count toward partial
                   - custom-list read failing → 200 with built-ins only. Grants read failing →
                     custom entries absent and built-ins present.
                   - JSON entries carry icon "" and custom:true
       depends_on: [t-7]

Wave 4
  t-16 Accept custom ids in grant and button APIs                     [risk+verification]
       files:      internal/api/grants.go, internal/api/grants_test.go,
                   internal/api/buttons.go, internal/api/buttons_test.go
       description: POST /grants and PUT /buttons (both services and icon) validate against
                   a.catalogue instead of AdGuard.Services. This waits on t-11 so that `custom:`
                   ids can never reach the unpatched blocked_services path.
       covers:     c-5, c-6
       contract:   - POST /grants {services:["custom:1"]} → 201 through the real engine and fake,
                     and the region gains the @@ lines. Before the change it is 422.
                   - ["custom:99"] → 422 naming it. A version accepting any `custom:` prefix fails
                     it.
                   - PUT /buttons services ["custom:1"], icon "custom:1" → 200 and echoed.
                     ["custom:99"] → 422 buttons[0].services.
                   - a grant on custom:1, then DELETE /custom-services/1 → 409. After End → 204, and
                     the button holding only custom:1 is gone from GET /buttons.
       depends_on: [t-11, t-14]

  t-17 Prove restart persistence and startup/tick convergence         [risk+verification]
       files:      cmd/adguard-reward/main_test.go
       description: End-to-end through run() against adguardtest. t-7 already does the main.go
                   wiring, so this task is tests only.
       covers:     c-1, c-8, c-9
       contract:   - TestRun_CustomServicesSurviveRestart: POST via the API, stop, run() on the same
                     data_dir → GET /custom-services and GET /services both list it (c-1)
                   - TestRun_CustomRulesRestoredAtStartup: a stored service plus a grant whose
                     ends_at passed while the app was down, and a fake holding a stale @@ region
                     wrapped in foreign lines → by the first /healthz answer the block lines are
                     present, no @@ remains, the grant is expired, and the foreign lines are
                     byte-identical (c-8, c-9)
                   - TestRun_CustomRulesReconcilerTick: with reconcileInterval overridden, a manual
                     edit inside the markers is undone within one tick
       depends_on: [t-11, t-13, t-14]
```

Merge notes (grafts, not divergences):

- Verification split the catalogue (t-7) out of risk's CRUD API task. That moves main wiring from wave 4 to wave 2 and lets the blocked view (t-15) run in wave 3.
- Risk's single UI task t-14 listed 4 files but contracted on untested Home and Buttons. Adding the test files made it 6 files, so it was split along verification's lines (t-8 GrantForm, t-9 Home+Buttons). The Services nav moved into t-9 so that t-10 no longer conflicts on Home.svelte, which removes risk's t-15→t-14 file-only dependency. t-8 is small (2 files) but kept separate because merging it anywhere pushes a task past 5 files.
- The `position` column and "domains in order" come from mvp and verification. Icon nulling on delete comes from verification. Engine refusal of unknown custom ids and Result.Failed on Create also come from verification.
- Risk's t-12 deps were extended to [t-11, t-13, t-14]: the restart test expires an overdue custom grant, which needs t-11's revert partition.

## Disagreements

**D1. A region-write failure on End or expiry: fail closed or best effort.**
- risk and verification: write the region before the status CAS. On failure the grant stays active, End returns *ErrRevertFailed, and the reconciler retries. On Create, the grant's clients go into Result.Failed.
- mvp: sync after commit and only log failures. ErrRevertFailed stays scoped to blocked_services, and the next tick converges.
- **Default: fail closed** (t-11).
- Why it matters: under best effort a parent can see "ended" while the child stays unblocked until the next successful pass. A persistently failing set_rules is invisible. Fail-closed matches the existing built-in revert semantics and partial_apply, at the cost of a new error path the UI must surface.

**D2. Mapping changes (rename, adoption, new device): resync immediately or wait for the tick.**
- risk (t-12): AdoptClients, CreateClient and RenameClient call syncRules synchronously. A renamed `$client` silently stops matching, so the child is unblocked for custom services for up to 60 s.
- mvp and verification: rely on the reconcile tick, because no criterion requires immediacy.
- **Default: include risk's t-12.** The `baselineFrom` filtering half is not optional: it is a confirmed leak (`clients.go:147` appends `g.Services` into `AdoptBlockedServices`) that neither other draft caught. If a human rejects immediate resync, t-12 should shrink to the filter plus its adopt contract, not disappear.

**D3. Where custom-service CRUD and sync run.**
- risk and verification: engine methods under `e.mu`, with the in-use check in the same store tx. That serialises delete against a concurrent grant create.
- mvp: the API handler writes the store, then calls `Grants.SyncRules`.
- **Default: engine-owned** (t-6, race contract).
- Why it matters: under mvp's shape a DELETE racing POST /grants can leave a grant on a deleted service. That silently breaks the locked delete_refs ("never silently re-blocks mid-reward") and orphans `@@` rendering.

**D4. Malformed markers and where splice lives.**
- risk and verification: a pure `internal/rules` package. A lone or duplicated marker makes the write refuse with an error log and no repair.
- mvp: splice inside `adguard.Client.SetManagedRules`, with malformed markers unaddressed.
- **Default: pure package, refuse on malformed** (t-1, t-3, t-13).
- Why it matters: a guessed repair (for example "lone begin = region to EOF") can delete a parent's own rules (r-02, c-9). The accepted cost is that c-8 does not self-heal a parent-deleted end marker: the region stays unmanaged until a human fixes it, with an error logged every tick. A human should confirm that c-8 is acceptable under this reading.

**D5. Exception line shape.**
- risk and verification: one `@@` line per domain over the union of clients any active grant covers. Ref-counting falls out of the union.
- mvp: one `@@` line per active grant × domain, reading c-6's "covering the grant's clients" literally.
- **Default: union per domain** (t-2).
- Why it matters: this decides how c-6 ("one exception per domain") is judged at verify time. Under the union shape two grants share one line. Under mvp's shape there are N lines per domain. Both satisfy c-7, but the verify step must use the same reading as the plan.

**D6. A custom service as a button icon.**
- verification: allowed. Delete nulls a matching icon, because otherwise the next whole-list PUT gets a 422.
- mvp: allowed through the decorated catalogue, with no cleanup.
- risk: catalogue-only, so icon `custom:N` gets a 422 and the picker hides custom services.
- **Default: allow, with icon-nulling on delete** (t-4, t-9, t-16).
- Why it matters: the locked `icon_source` says "any catalogue service may be the icon", and custom services are now in the catalogue. Excluding them carves an exception into a locked decision. Allowing them adds a cleanup duty that the delete_refs lock ("strips the service from buttons") does not mention.

**D7. How custom services join the catalogue.**
- mvp: a decorator over `Deps.AdGuard.Services()`, so there is zero diff at the four call sites.
- verification: an explicit `a.catalogue(ctx)` that handlers opt into.
- risk: append in handleServices, with grants and buttons reading the custom list themselves.
- **Default: verification's explicit `a.catalogue`** (t-7, t-16).
- Why it matters: the decorator also feeds custom ids into `blocked.go:49`, where they would be computed from blocked_services and show as "unblocked". It also feeds the built-in name-clash check unless a raw client is kept beside it. The explicit approach costs edits at `grants.go:232` and `buttons.go:148` but keeps built-in-only paths honest.

**D8. Where the letter avatar is produced.**
- risk and verification: a client-side `ServiceIcon` with wire `icon:""` and `custom:true`.
- mvp: the server generates a base64 SVG with the letter into `icon`, so no frontend icon change is needed.
- **Default: client-side** (t-5, t-8, t-9).
- Why it matters: mvp's route avoids touching three Svelte components but renders parent-typed names into server-generated SVG, which adds an escaping surface. The wire also loses any custom marker the UI could use (for example to keep custom services out of AdGuard-only affordances).

**D9. A dedicated run()-level restart and startup task.**
- risk and verification: yes (t-17).
- mvp: no. A store re-open proves c-1, and the engine's Start contract proves c-8.
- **Default: include.**
- Why it matters: c-1 "across restarts" and c-8 "at startup … undoing a restart gap" are only observable end-to-end through `run()`, which does the startup reconcile before the listener. Without the task, a missing main.go wire or an ordering regression passes every unit test.

## Coverage

| Criterion | Merged tasks |
|---|---|
| c-1 create + persists across restarts | t-4, t-5, t-6, t-10, t-14, t-17 |
| c-2 non-hostname domain → 400, nothing stored | t-2, t-10, t-14 |
| c-3 edit name/domains; region carries exactly the new list | t-4, t-6, t-10, t-14 |
| c-4 delete; no rule for its domains remains | t-4, t-6, t-10, t-14 |
| c-5 appears in grant form, button editor, blocked view | t-5, t-7, t-8, t-9, t-15, t-16 |
| c-6 grant writes one `@@` per domain over the grant's clients | t-2, t-11, t-12, t-16 |
| c-7 end/expiry removes exceptions, ref-counted on shared clients | t-2, t-11 |
| c-8 reconciler converges at startup and on tick | t-3, t-12, t-13, t-17 |
| c-9 outside-marker lines byte-identical; missing region appended | t-1, t-3, t-6, t-11, t-13, t-17 |

All 9 criteria covered; no new criteria introduced.
