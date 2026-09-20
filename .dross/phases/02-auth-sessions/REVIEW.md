# Plan Review — 02-auth-sessions

Reviewed: 2026-09-20
Plan: 11 tasks across 4 waves

## BLOCKING
- [locked-decision] t-4 `TestAuthenticate_Sliding` mandates accepting an expired session. The contract says: Issue at t0, Authenticate at t0+29d records expires = t0+59d, then "Authenticate at t0+59d+1h -> ok". t0+59d+1h is one hour past the recorded expiry, so under the locked `session_lifetime` decision ("a session unused for 30 days expires") and the task's own rule ("ExpiresAt <= now -> Delete + ErrNoSession") that call must return ErrNoSession. An executor writing this test literally will either see it fail or, worse, add a grace window to make it pass.
  Suggestion: change the third step to a time strictly inside the slid window (e.g. t0+58d+23h -> ok, Touch records t0+88d+23h), then keep the 31-day gap -> ErrNoSession.

- [test-contract] t-2's contracts contradict each other on the escalation reset. The task defines reset as "lastTrip >= EscalationReset ago" measured from the last trip (confirmed by `TestLimiter_Reset`: L+59m59s -> no reset, L+1h -> reset). Under that rule `TestPerIP_Doubling`'s sequence "1m, 2m, 4m, 8m, 16m, 32m, 1h, 1h" is unreachable: trip #7 at L7 locks until L7+1h; the earliest possible trip #8 is at >= L7+1h, at which point lastTrip is >= 1h ago and trips resets to 0, so trip #8 yields 1m, not 1h. The 1h cap can be reached exactly once and never repeated. This is a semantic choice about c-11 ("resets after 1h without a trip"), not an implementation detail, and must be made by the author rather than improvised mid-execution.
  Suggestion: either (a) measure the reset from lockout expiry (an IP can only "go 1h without a trip" while it is able to trip), which makes the cap sticky and matches the doubling contract as written, or (b) keep reset-from-last-trip and change the doubling contract's tail to "..., 32m, 1h, 1m". Update `TestLimiter_Reset`/`TestPerIP_Reset` timings to whichever anchor is chosen.

## FLAG
- [test-contract] t-9's description orders `Limiter.Check(ip)` *before* acquiring the single in-flight slot, but `TestLogin_Burst` expects 12 concurrent wrong-password posts from one IP to yield exactly 5 fake hits and 7 x 429. With Check-then-slot, all 12 pass Check while the counter is 0, queue on the slot, and each reaches AdGuard in turn: 12 hits, 0 x 429. The slot only bounds the budget if Check is (re-)run while holding it.
  Suggestion: state explicitly "Check before the slot for the fast 429 path, and Check again after acquiring the slot; a request that became limited while queued returns 429 without calling AdGuard". Same applies to the global budget in `TestLogin_Global`.

- [test-contract] t-5 says noStore "sets Cache-Control: no-store before calling next", but `TestNoStore` asserts that "a stub that writes Cache-Control: max-age=60" still ends with no-store. A handler that sets the header after the outer layer overwrites it; the described implementation cannot pass that sub-assertion.
  Suggestion: either drop that sub-case (all handlers are in-house) or specify a ResponseWriter wrapper that forces no-store at WriteHeader time. Pick one so the description and the contract agree.

- [wave-order] t-5 is in wave 1 with no `depends_on`, yet its description declares `Deps` with fields typed as `Store`, `Auth`, `Limiter` (t-1, t-4, t-2 packages) and says "a wrapper for 401 satisfies auth.RequireSession's errorWriter" (t-4's signature). Executed in parallel with the rest of wave 1, `internal/api` cannot compile. It works only if wave 1 happens to be executed sequentially in id order.
  Suggestion: either declare `depends_on = ["t-1","t-2","t-4"]` and move t-5 to wave 2, or have t-5 declare `Deps` against api-local interfaces (`Authenticator`, `LoginLimiter`, `SessionStore`) and an api-owned `errorWriter` type, leaving it genuinely independent. Note t-3 also creates `internal/api/clientip.go` in wave 1, so the package's first file has two authors; distinct files, no conflict, but worth stating.

- [antipattern] Double Set-Cookie on logout and delete-self. t-4's `RequireSession` re-issues `Cookie(raw)` (Max-Age=2592000) on success before calling next; t-9's logout and t-10's `DELETE /sessions/{own id}` then call `ClearCookie()` via http.SetCookie, which appends. The response carries two Set-Cookie headers for `adguard_reward_session` with conflicting Max-Age. `TestLogout_Replay` / `TestSessions_DeleteSelf` ("Set-Cookie with Max-Age=0") can pass while the refreshed cookie is also present; browser behaviour is last-wins in practice but not something to rely on.
  Suggestion: have `ClearCookie` usage go through a helper that removes any existing Set-Cookie for that name first, or have RequireSession defer its refresh until after next via a wrapper that skips it when the handler cleared the cookie. Add "exactly one Set-Cookie header" to those two contracts.

- [granularity] t-3 touches 6 files across two packages (`internal/config` and `internal/api`) and its two halves are independent: `api.ClientIP(trusted []*net.IPNet)` takes parsed nets and does not import config. Both halves would still be wave 1.
  Suggestion: split into t-3a (config key, env row, validation, example/full yaml) and t-3b (ClientIP resolver). Smaller commits and the config half can be reviewed against `TestEnvTable` on its own.

- [wave-order] t-7 (`depends_on = ["t-5"]`, wave 2) needs nothing built by t-5. Its inputs are the envelope shape `{error, message}`, the header name/value, and the 429 Retry-After convention — all fixed in the plan text. It could run in wave 1 and t-8 in wave 2.
  Suggestion: drop the depends_on or state that it is a contract dependency kept deliberately so the envelope is frozen before the client is written. Either is fine; the current form overstates the coupling.

- [granularity] t-7 also mixes tooling (vitest devDependency, `vite.config.ts` test block, Makefile `test-go`/`test-web`/`test` targets) with the fetch layer. The `make test` prerequisite contract is the only test for the tooling half.
  Suggestion: acceptable as one task, but note the executor must run `pnpm install` (lockfile changes) and that `make test` now requires node_modules in every environment that runs the phase gate, including CI.

- [granularity] t-9 carries 15 contracts, covers 7 criteria and owns login, logout, me, the catch-all route, the in-flight slot and the event log. It is the phase's integration hub and is defensible, but logout/me/catch-all could be their own small task landing before login (they only need t-4/t-5/t-6) which would shrink the highest-risk commit.
  Suggestion: consider a split; if kept, add `c-11` to t-9's `covers` since `TestLogin_RetryAfterEscalates` is the only HTTP-level assertion that Retry-After tracks the escalated window.

- [test-contract] t-2 says failures are "counted in a fixed 1-minute window" without saying what anchors it. Calendar-aligned fixed windows let 8 failures in two seconds straddle a boundary without tripping (4 + 4); first-failure-anchored windows do not. Both readings pass the fake-clock tests as written.
  Suggestion: state "window starts at the first failure after the previous window/lockout ended" (or choose sliding) so the executor does not pick by accident.

- [antipattern] t-11 puts the "tls.cert and tls.key must both be set" check in main. `config.validate()` is the established home for cross-field rules (password vs password_file mutual exclusion lives there), and putting it in config means env-only loads and `config.Load` tests get the same error.
  Suggestion: move the both-or-neither check into `validate()` (t-3 already touches config.go) and keep t-11's `TestRun_TLS` stderr assertion as the integration check.

- [antipattern] t-3 adds `trusted_proxies` to `internal/config/testdata/full.yaml`, but `TestLoad_Full` pins that file to an exact struct (config_test.go:47) and the task does not mention updating its expected value. The executor will hit a failing pre-existing test.
  Suggestion: add "TestLoad_Full's want struct gains TrustedProxies" to t-3's description.

- [test-contract] c-7's automated coverage is thin by design (no DOM runner). t-8's second contract ("removing ApiError.code from api.ts stops `pnpm check`") tests the type-checker, not behaviour, and the real acceptance is a manual gate recorded in the commit body.
  Suggestion: keep, but the executor must actually perform the manual gate before writing it into the commit message (commit-safety rule 1); dross-verify should treat c-7 as manually verified, not test-backed.

- [antipattern] t-9 maps AdGuard's own 429 (`ErrRateLimited`) to 502 `{adguard_unavailable, "AdGuard Home is unreachable"}`. The status split is right (never 401), but the message is false when AdGuard is reachable and throttling; the Svelte page will tell the parent AdGuard is down.
  Suggestion: either a distinct code (`adguard_throttled`) or a neutral message ("AdGuard Home did not accept the login attempt; try again shortly") for the 502 body.

- [antipattern] PLAN.md specifies the SQLite file at mode 0600; t-1 only sets the directory to 0700 and lets modernc create the file under the process umask (typically 0644).
  Suggestion: add an explicit `os.Chmod(dbPath, 0o600)` after open (or pre-create the file) and a sub-assertion in `TestOpen_DataDir`.

## NOTE
- [strengths] Every contract is mutation-shaped ("if X breaks, TestY fails") with concrete inputs and expected values (43-char tokens, Max-Age=2592000, 150s/1s Retry-After, 4 KiB body cap, canary strings). This is the right shape for dross-verify's mutation pass and is unusually consistent across all 11 tasks.
- [strengths] Security invariants are enforced by construction rather than by test alone: no `Success`/`Rejected` method on the limiter (budget can only be failures), `Session` has no raw-token field and a `LogValue()` that emits only id/username, IDOR answered with 404 not 403, identical 401 body for every credential rejection, and debug-level log canaries across login -> me -> logout at three layers (auth, api, main).
- [strengths] The plan reuses existing test infrastructure correctly. I verified `adguardtest.Options{User,Pass}`, `SetStatus`, `Hang`, `Requests`, `Close`, the fake's wrong-password -> 403 -> `ErrBadCredentials` path, `writeConfig`, and the reflection-based `TestEnvTable`/`walkLeaves` (a `[]string` field is a leaf, so the new env row is genuinely required).
- [coverage] All 11 criteria are covered; c-11 by t-2 only (see the covers FLAG on t-9).
- [locked-decision] t-11 derives `Secure` from `tlsOn || base_url starts with https://`, extending c-2's "Secure when the app serves TLS" to TLS-terminating proxies. Sensible and not a conflict; record it as a deliberate extension.
- [locked-decision] 400 (bad body) and 403 (missing CSRF header) login attempts emit no login event. c-8 enumerates only ok|bad_credentials|adguard_unreachable|rate_limited, so this is consistent; worth stating as intentional in the commit.
- [antipattern] t-8 deletes `Counter.svelte` and the asset imports but does not list `web/src/assets/hero.png`, `svelte.svg`, `vite.svg` or `web/public/icons.svg` in `files`; they become dead files unless removed in the same commit.
- [antipattern] t-7 specifies the route store as `writable<'login'|'home'>` (svelte/store) while t-8 says "Uses Svelte 5 runes". Both work; pick one so App.svelte does not mix idioms on day one.
- [antipattern] PLAN.md places rate limiting under `internal/auth/`; the plan creates `internal/ratelimit/`. Cleaner separation (pure policy, no auth import) — ARCHITECTURE.md will need regenerating after the phase.
- [antipattern] `sweepInterval` as a mutable package var is safe today because `main_test.go` has no `t.Parallel`; if a later phase parallelises those tests it becomes a data race.
- [antipattern] `store.Open` in a container: the distroless image runs nonroot (uid 65532) and compose bind-mounts `./data` owned by the host user. Once the store writes a DB, `MkdirAll`/open can fail and the container exits 1 where phase 01 ran fine. Out of scope for this phase's criteria, but expect it at first deploy.
- [antipattern] The single global in-flight login slot serialises every login behind one AdGuard round-trip. Fine for a household; a hung AdGuard blocks all logins until each request's context is cancelled, so the adguard client's HTTP timeout is what bounds it.
- [forbidden-actions] No rule violations found. `runtime.mode = native`; `make test`, `go test`, `pnpm` are all permitted. r-03 (never log credentials) is actively asserted by t-4, t-9 and t-11 canary tests. r-01 (no vendored AdGuard code) untouched.

## Summary
Coverage is complete and the contracts are strong, but two of them are internally impossible as written — the sliding-expiry step in t-4 contradicts the locked 30-day lifetime, and t-2's doubling contract cannot coexist with its own reset rule — so the author needs to fix those two numbers/semantics before execution; the remaining flags are ordering (t-5's undeclared deps, t-9's Check-before-slot) and cookie/no-store mechanics that would otherwise be discovered as red tests mid-phase.
