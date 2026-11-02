# Plan Review — 05-phone-ui-buttons-pwa

Reviewed: 2026-09-21
Plan: 13 tasks across 5 waves

## BLOCKING
(none)

## FLAG
- [test-contract] t-1's `TestButtons_ReplaceAtomic`/`TestButtons_ReplaceRace` require that `ListButtons` never observes a half-written replace, but the description specifies List as "two queries each scanned to completion" with no transaction. With `SetMaxOpenConns(1)` the connection is released between List's buttons query and its services query, so a concurrent `ReplaceButtons` tx can commit in that gap: List then returns list X's buttons with empty services (their ids no longer exist in `button_services`). That is exactly the "mix" the race test forbids.
  Suggestion: state in t-1 that `ListButtons` runs both queries inside one `BeginTx` (read tx holds the single connection), or use a single JOIN ordered by `(buttons.position, button_services.position)` and group in Go.

- [test-contract] t-2's resolve table is internally inconsistent on `/.gitkeep`: the rules say "dotfiles treated as missing" and "a missing path with a file extension → 404", yet the expected result is `index.html`. Go's `path.Ext("/.gitkeep")` returns `".gitkeep"`, so a literal implementation yields 404 and `TestResolve_Table` fails on the plan's own row.
  Suggestion: define "has an extension" as `path.Ext(base) != "" && !strings.HasPrefix(base, ".")`, or change the expected row to 404. Either is fine; pick one and write it down.

- [test-contract] t-13's exit gate says "no Survived mutant for api.ts:49 (ConditionalExpression->true, LogicalOperator)". The current mutation.json has three `ConditionalExpression → true` mutants on line 49 (ids 1440, 1443, 1446). 1443 mutates the `typeof history !== 'undefined'` clause to `true`, which is equivalent under jsdom and belongs to the already-accepted `stryker-node-env-guard` category. It will survive, and a gate phrased by line+operator fails on an accepted equivalent while the two routed keys (d2e9f9c6 = 1440, 47163e3c = 1442) are dead.
  Suggestion: phrase the gate by survivor key (d2e9f9c6, 47163e3c, 56313b4a, 95210673, f868399f, 77ea4718, d248d03c, c68261e3, a094a307, beec743e) and say explicitly that 1443 (and 1445, the `'undefined'` string literal) are expected to remain and are accepted under the existing category.

- [granularity] t-12 is one task that should be two. It carries 15 test contracts across five concerns: per-child button rendering, tap outcome handling (201/applied=false/409/network), the multi-target 409 offer (extend each overlap, create the remainder, 404 retry, second-409 stop), the ad-hoc form, and the lazy blocked-services disclosure. Home.svelte will be whole-file mutated by Stryker (no .svelte parser), so the bigger the component the worse the attribution the plan is already fighting in t-13.
  Suggestion: extract the tap/offer orchestration into a pure module (e.g. `web/src/lib/tap.ts`: `runTap(spec, deps) -> outcome` and `acceptOffer(targets, remaining, spec, deps)`), tested under node with a fake api, as its own wave-3 task depending on t-4/t-7. Home.svelte then only wires DOM to it. That also gives the 409/overlap logic per-test attribution instead of whole-file.

- [granularity] t-2 touches 7 files across three unrelated mechanisms: the SPA handler (`internal/spa/*`, fully testable with `fstest.MapFS`), the embed + `.gitkeep` + two `.gitignore` edits, and the Makefile dependency/lint changes.
  Suggestion: split into t-2a `internal/spa` handler + table tests, and t-2b embed/gitkeep/gitignore/Makefile with `TestEmbed_CompilesWithoutBuild`. Both stay in wave 1; t-11 depends on both.

- [granularity] t-4 bundles three independent pieces of work: api.ts endpoint additions, the pure `grants.ts` helpers, and the Login.svelte tweak + its two mutant kills. The Login half shares nothing with the other two beyond `messageFor`.
  Suggestion: move Login.svelte/Login.test.ts into its own wave-1 task (or into t-13's residual-kill scope). If kept together, at least make its commit separate so the verify diff for Login is one hunk.

- [wave-order] t-8 (GrantForm) depends on t-4 but uses only `Child` and `Service`, both of which already exist in api.ts; the minute bounds are hard-coded. It could run in wave 1.
  Suggestion: drop t-8 to wave 1 with no depends_on. Then t-10 must add `depends_on = ["t-4"]` explicitly (it currently gets `listButtons`/`saveButtons` only transitively through t-8).

- [locked-decision] t-4/t-9 clamp `ownDuration` to `[60, 86400]`. For any grant whose span exceeds 24 h (only reachable after one extension of a 24 h grant) this posts 24 h rather than "the grant's own duration (ends_at − started_at)" as `extend_amount` is worded. Not blocking: the raw span is unservable (checkDuration 422s it), so the literal rule has no working implementation there and the panel recorded the bend as D14.
  Suggestion: add a one-line note under the `extend_amount` decision in spec.toml (or in the verify notes) so the verifier does not score the clamp as a deviation.

- [feasibility] t-6 puts `sw.ts` under `tsconfig.app.json`, which inherits the DOM lib. Service-worker globals (`ServiceWorkerGlobalScope`, `ExtendableEvent`, `FetchEvent`) need the `webworker` lib, and `/// <reference lib="webworker" />` in a program that already has `dom` produces duplicate-identifier errors. `make typecheck` (`svelte-check --tsconfig ./tsconfig.app.json`) will fail and the plan does not say how it is avoided.
  Suggestion: name the mechanism in t-6: either a `tsconfig.sw.json` (lib `["ES2023","WebWorker"]`, include only `src/sw.ts` and `src/lib/sw-routing.ts`) wired into `pnpm check` and excluded from the app config, or local `declare const self: ServiceWorkerGlobalScope`-style minimal declarations in sw.ts with no lib reference.

- [feasibility] t-11's `TestRun_SPA*` call `t.Fatalf` when `web/dist/index.html` is absent. `make test-go` builds first, but the go mutation leg (gremlins) and any bare `go test ./cmd/adguard-reward` do not. `web/dist` is gitignored, so a sandbox built from tracked files, or a fresh clone, turns every t-11 test red before any mutant is applied — and a red baseline aborts the gremlins run for the whole package.
  Suggestion: before executing t-11, confirm how dross verify's gremlins adapter obtains its working tree (copy of the on-disk dir, which has dist, vs git-tracked files). If the latter, either run `make build-web` as a verify precondition or gate the SPA tests on a `-tags spa`/env var that `make test-go` sets, keeping the Fatalf inside that gate.

## NOTE
- [test-contract] t-3's fourth contract (Chrome installability from a phone over TLS) is manual by necessity; the criterion cannot be automated without a real browser. Record device, Chrome version and the Lighthouse result in the verify notes so the "pass" is an observation, not a claim.
- [test-contract] t-1's `TestButtons_ChildCheckInTx` ("a child deleted on another goroutine between two Replace calls") is non-deterministic as written and, with a single-connection pool, cannot actually interleave inside the tx. The atomic-replace and cascade tests already pin the behaviour; this one will either be flaky or tautological.
- [granularity] t-6 spans 6 files (pure routing, worker, vite build plugin, registration). Less urgent than t-2/t-4 because the pieces are genuinely coupled through `__PRECACHE__`, but the vite plugin + `main.ts` registration could stand alone if t-6 proves large.
- [granularity] t-13 lists four test files it "may" touch. It is a process gate, not a code task; its `files` are speculative. Fine as long as the executor treats the list as an upper bound.
- [antipattern] t-4's Login.svelte change (`autocapitalize="none" spellcheck="false"`) is described as "a real phone-UI change so the file enters the verify diff". It is a legitimate improvement, but say so in the commit body rather than presenting it as diff-scope engineering.
- [antipattern] t-3's icon glyph comes from "the existing mark" — `web/public/favicon.svg` is the Vite+Svelte template's placeholder, not a project mark. Not a rule violation (nothing AdGuard-derived, r-01 is clean), but the PWA will install with the scaffold's logo.
- [test-contract] t-6's "vite build emits sw.js with a precache list" spawns a full vite build inside vitest. Under Stryker it re-runs for every mutant that its coverage touches; expect it to dominate the scoped run's wall time. Consider putting it under a `describe` that only runs when not inside Stryker (`process.env.__STRYKER_ACTIVE__`).
- [strengths] The test contracts are unusually precise: each names the test, the surface that breaks, and — for c-7 — the exact assertion that was missing last time (App.test asserting `location.pathname === '/login'` is precisely what phase 02's test lacked to kill d248d03c; the Login idle/in-flight assertions map one-to-one onto 56313b4a/95210673/f868399f/77ea4718).
- [strengths] The plan attacks the mutation-attribution ceiling structurally rather than by hoping: pure modules (`grants.ts`, `sw-routing.ts`, `spa.resolve`) put logic where Stryker/gremlins attribute per-test, and t-13 is an observed run with the result quoted in the commit body, not a "verified" claim.
- [strengths] Consistent preference for observable failure over silent pass: server-truth merge from the 201 (no optimistic UI), `[]` never `null` on every wire list, `Fatalf` instead of `Skip` on an unbuilt dist, 503 instead of a blank page when `index.html` is missing, in-tx child re-check surfacing as a typed error rather than a raw FK string.

## Summary
No blocking findings; the plan covers all eight criteria and every locked decision, but t-1's List needs a transaction to satisfy its own race test, t-2 and t-13 each carry a self-contradicting contract row, and t-12 should be split before it becomes the next whole-file attribution problem.
