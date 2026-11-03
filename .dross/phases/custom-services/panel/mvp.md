# Panel draft — mvp lens

Core idea: the managed region is a **pure function of stored state** —
`desiredRules(custom services, children mappings, active grants)` — and every
mutation (create / edit / delete / grant / extend / end / expiry / reconcile)
just re-syncs that function's output into the markers. That one mechanism
delivers c-3, c-4, c-6, c-7 and c-8 without per-operation diff logic, and the
ref-counting of c-7 falls out for free (an exception exists exactly while some
active grant still produces it). The catalogue side of c-5 is one decorator over
the API's AdGuard dependency, so none of the four existing `Services()` call
sites (or any Svelte component that renders services) needs editing.

Custom service ids on the wire and in grants / buttons are `custom:<n>` — the
colon can never appear in an AdGuard catalogue id. Management wire shape (fixed
here so t-6 can start in wave 1): `GET/POST /api/v1/custom-services`,
`PUT/DELETE /api/v1/custom-services/{n}`, body/row `{id: n, name, domains: []}`.

```
Phase custom-services — 6 tasks across 3 waves

Wave 1
  t-1  Add custom services store and migration
       files:    internal/store/migrations/0007_custom_services.sql,
                 internal/store/custom_services.go,
                 internal/store/custom_services_test.go
       description:
                 Tables custom_services (name UNIQUE COLLATE NOCASE) and
                 custom_service_domains (FK cascade, position, domain UNIQUE).
                 List/Get/Create/Update/Delete with ErrCustomNameTaken and
                 *ErrDomainTaken{Domain, Owner}; Update replaces name + domains in
                 one tx; Delete in one tx removes "custom:<n>" from button_services
                 and deletes any button left with no services. CustomServiceID(n)
                 helper returns "custom:<n>".
       covers:   c-1, c-4
       depends_on: —
       contract: - a service created, then the store closed and re-Opened on the
                   same data_dir, lists with the same name and domains in order
                 - Create "roblox" after "Roblox" returns ErrCustomNameTaken; a
                   domain already owned returns *ErrDomainTaken naming the owner
                   and no row is inserted
                 - Delete strips custom:N from a two-service button (other service
                   kept, order kept) and deletes a button whose only service was
                   custom:N; unrelated buttons untouched
                 - Update [a.com,b.com] → [b.com,c.com] lists exactly [b.com,c.com]
       status:   pending

  t-2  Add managed-region user_rules read-modify-write
       files:    internal/adguard/rules.go, internal/adguard/rules_test.go,
                 internal/adguard/adguardtest/server.go
       description:
                 Client.SetManagedRules(ctx, lines): under c.rmw, GET
                 /control/filtering/status → user_rules, splice lines between
                 "! adguard-reward begin"/"! adguard-reward end" (append the pair at
                 the end when absent), POST /control/filtering/set_rules; skip the
                 POST when the region already matches. Fake serves both endpoints
                 from an in-memory list with SetUserRules/UserRules knobs.
       covers:   c-9
       depends_on: —
       contract: - user_rules [a, begin, old, end, b] + SetManagedRules([new])
                   posts exactly [a, begin, new, end, b]; any change to a/b
                   content or order fails the byte-for-byte comparison
                 - user_rules [a, b] with no markers → [a, b, begin, new, end]
                 - region already equal → CountRequests(POST set_rules) == 0
                 - filtering/status 502 → error returned and zero set_rules POSTs
                   (never replace the list blindly)
       status:   pending

  t-6  Add Services settings page
       files:    web/src/lib/CustomServices.svelte,
                 web/src/lib/CustomServices.test.ts, web/src/lib/api.ts,
                 web/src/App.svelte, web/src/lib/Home.svelte
       description:
                 api.ts: CustomService type, list/create/update/delete functions,
                 Route += 'services'. Home gets a "Services" nav button after
                 "Buttons"; App routes 'services'. Page lists services (letter
                 avatar from name), create/edit form (name + one domain per line),
                 inline Delete → Confirm/Cancel; every mutation re-renders from the
                 response; 400/409 show the server message and keep the input.
       covers:   c-1, c-3, c-4, c-5
       depends_on: —
       contract: - Save with textarea "a.com\n\nb.com" sends POST
                   /api/v1/custom-services {name, domains:["a.com","b.com"]}
                   (blank line dropped) and renders the returned row
                 - a 400 response shows its message and the form keeps its values
                 - Delete fires no request until Confirm; a 409 shows the message
                   naming the grant and the row stays listed
                 - Home "Services" click sets route 'services'; a reload on
                   /services stays on the page
       status:   pending

Wave 2 (depends t-1, t-2)
  t-3  Derive and sync managed rules in grants engine
       files:    internal/grants/rules.go, internal/grants/rules_test.go,
                 internal/grants/grants.go, internal/grants/reconcile.go
       description:
                 Pure desiredRules: per custom domain `||d^$client=<every mapped
                 child client>` (omitted when there are none), plus per active
                 grant × covered custom service × domain `@@||d^$client=<grant's
                 stored clients>`; names as '<name>' with ' and \ escaped, joined
                 by |. Engine.syncRules (caller holds mu) runs after Create, End,
                 expireLocked and every Reconcile pass (including zero grants);
                 exported SyncRules(ctx) takes mu for the API. unblock/revert drop
                 custom: ids from blocked_services writes. Store iface +=
                 ListCustomServices; AdGuard iface += SetManagedRules.
       covers:   c-6, c-7, c-8
       depends_on: t-1, t-2
       contract: - Create for custom:1 {a.com,b.com} on kid-phone + kid-tablet →
                   fake user_rules hold `@@||a.com^$client='kid-phone'|'kid-tablet'`
                   and the b.com twin; no clients/update body contains "custom:1"
                 - shared TV: two children's active grants on custom:1; End the
                   first → TV still has the second grant's @@ line; End the second
                   → no @@ line for a.com remains
                 - timer expiry of a custom grant removes its @@ lines
                 - region emptied / hand-edited inside the markers with one active
                   grant → Start (and a later Reconcile) restores block + exception
                   lines; Reconcile with zero active grants still writes the
                   block lines
                 - client named `Bob's tab` renders as 'Bob\'s tab'
                 - no child has any client → no `||a.com^` line at all (a rule
                   without $client would block adults)
                 - Reconcile when the region already matches → zero set_rules POSTs
       status:   pending

  t-4  Merge custom services into API catalogue and blocked view
       files:    internal/api/catalogue.go, internal/api/catalogue_test.go,
                 internal/api/api.go, internal/api/blocked.go,
                 internal/blocked/blocked.go
       description:
                 Deps.CustomServices (List only). api.New wraps Deps.AdGuard so
                 Services() returns the AdGuard catalogue + each custom service as
                 {id "custom:<n>", name, icon: base64 letter-avatar SVG}, keeping
                 the raw client for built-in-only checks. blocked.Input.Extra
                 (client → custom ids blocked there: every mapped client minus
                 those an active grant covers) is unioned into each effective set.
       covers:   c-5
       depends_on: t-1
       contract: - GET /api/v1/services lists {id:"custom:1", name:"Roblox"} with a
                   base64 SVG icon that decodes to text "R", after AdGuard's entries
                 - POST /api/v1/grants services ["custom:1"] → 201 (was 422);
                   ["custom:99"] → 422 unknown service
                 - PUT /api/v1/buttons with services/icon "custom:1" → 200
                 - GET /children/{id}/blocked: custom:1 "blocked" with no grant,
                   "unblocked" once the child's active grant covers it, "partial"
                   when another child's grant covers only a shared client
       status:   pending

Wave 3 (depends t-3, t-4)
  t-5  Add custom services CRUD endpoints
       files:    internal/api/custom_services.go,
                 internal/api/custom_services_test.go, internal/api/api.go,
                 cmd/adguard-reward/main.go
       description:
                 GET/POST /api/v1/custom-services, PUT/DELETE …/{id}. validDomain
                 rejects non-hostnames → 400 before any store or AdGuard call; name
                 vs raw AdGuard catalogue (case-insensitive) and store → 409; owned
                 domain → 409; DELETE → 409 naming the grant while an active grant
                 includes custom:<n>. Each successful mutation calls
                 Grants.SyncRules. main.go wires CustomServices: st.
       covers:   c-1, c-2, c-3, c-4
       depends_on: t-3, t-4
       contract: - POST with each of "https://a.com", "a.com/x", "a.com:443",
                   "*.a.com", "a .com", "a.com\nb.com", "||a.com^", "a.com^",
                   "a.com$important", "/a/" → 400; list stays empty and zero
                   set_rules POSTs
                 - POST {Roblox, [a.com]} → 201 and fake user_rules carry
                   `||a.com^$client=` naming every mapped child client, none of
                   the unmapped (adult) clients
                 - PUT [a.com,b.com] → [b.com,c.com]: region has b.com and c.com
                   lines and no line containing a.com
                 - DELETE while a grant covers it → 409 naming the grant id, still
                   listed; after End, DELETE → 204, no region line contains its
                   domains, its sole-service button is gone from GET /buttons
                 - POST name "youtube" (a built-in name, other case) → 409; a
                   domain owned by another custom service → 409
       status:   pending
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 create + persists across restarts | t-1 (re-Open persistence), t-5 (POST), t-6 (UI) |
| c-2 non-hostname domain → 400, nothing stored | t-5 |
| c-3 edit name/domains; region carries exactly new list | t-5 (PUT + SyncRules), t-3 (derivation), t-6 (UI) |
| c-4 delete; region has no rule for its domains | t-5 (409 / 204 + SyncRules), t-1 (button strip), t-6 (UI) |
| c-5 appear in grant form, button editor, blocked view | t-4 (catalogue decorator + blocked fold), t-6 (management page) |
| c-6 grant writes one @@ per domain over grant's clients | t-3 |
| c-7 end/expire removes exceptions, ref-counted on shared clients | t-3 |
| c-8 reconciler converges region at startup and tick | t-3 |
| c-9 outside-marker lines byte-identical; missing region appended | t-2 |

9/9 covered.

## Judgment calls

- Region is fully derived from store state and re-synced after every mutation; rejected per-operation add/remove diffs — derivation gives c-3/c-4/c-7/c-8 from one function and makes ref-counting automatic.
- One `@@` line per active grant × domain; rejected one merged line per domain over the union of clients — matches c-6's "covering the grant's clients" literally and costs the same.
- c-5 via a decorator on `Deps.AdGuard.Services()`; rejected editing the four call sites (clients.go, grants.go, buttons.go, blocked.go) — zero diff there, and the grant form / button editor / Home already render whatever the catalogue returns.
- Letter avatar generated server-side as a base64 SVG in the catalogue `icon` field; rejected a Svelte avatar component threaded through GrantForm/Buttons/Home — no frontend icon change at all, and it is still a letter avatar with no upload (custom_icon).
- Custom-service block state in the blocked view derived from active grants (`blocked.Input.Extra`); rejected reading back and parsing user_rules — the reconciler makes the region equal to that derivation, and parsing rules is new surface.
- Region sync after End / expiry / Create is best-effort post-commit (failure logged, next 60 s reconcile converges); rejected making a rules-write failure keep the grant active — `ErrRevertFailed` stays scoped to blocked_services, and a new failure mode adds API mapping work no criterion asks for.
- Child-mapping changes and client renames reach the region on the next reconcile tick, not synchronously; rejected hooking children/rename handlers — no criterion requires it and c-8 already covers convergence.
- Services page is a sibling route `services` with a nav button next to "Buttons"; rejected embedding it inside Buttons.svelte — the locked management_ui reads as a sibling section and Buttons.svelte is already 400 lines.
- t-6 sits in wave 1 against the wire shape fixed above (fetch is mocked in vitest); rejected waiting for t-5 — it consumes no backend output.
- t-5 split from t-4 because they share api.go; rejected merging them — 8+ files.
- Restart persistence (c-1) asserted at the store layer by re-Opening the data_dir; rejected a main_test restart test — the store re-open is the restart boundary, and the startup sync is in t-3's Start contract.
