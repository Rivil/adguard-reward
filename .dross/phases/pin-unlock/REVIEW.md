# Plan Review — pin-unlock

Reviewed: 2026-11-04
Plan: 13 tasks across 6 waves

## BLOCKING
(none)

## FLAG
- [locked-decision / spec gap] Home's active-grants poll defeats the idle lock in the most common case. `web/src/lib/activeGrants.ts:66` runs `start(intervalMs = 15000)`, which calls GET /api/v1/grants every 15 s and again on every `visibilitychange`. Under the locked `idle_window` ("sliding on every authenticated request") and t-5's Authenticate slide, each poll pushes a tagged session out by another 15 minutes. A phone left open on Home therefore never locks. That defeats the decision's stated reason ("a kid picking up the parent's phone later finds it locked"). Background tabs throttled to one timer a minute still poll inside the window. No task mentions this interaction, and no contract covers "app left open on Home". Every idle-lock test (t-5 TestAuthenticate_PinIdleWindow, t-9 TestSetPIN_IdleLock, t-10 TestUnlock_SessionTagged) assumes no requests arrive during the idle period, so all of them pass while the real-world lock never happens.
  Suggestion: Before execution, take this back to the spec owner. One fix is to exempt background polls from sliding. That would contradict the locked wording, so it needs a spec amendment, not a plan tweak. If the current behaviour is accepted, record it as a known limitation in spec.toml.

- [antipattern: missing nil-Devices contract] t-7 adds `Options.Devices` and routes RequireSession's 401 path through it. Main only wires it in t-13 (wave 6), and t-9 updates only the `login_test.go` harness. `internal/api/custom_services_test.go:46` and `internal/api/catalogue_test.go:56` build `auth.New(..., auth.Options{...})` without Devices and will keep doing so. From wave 3 until wave 6, the binary and those harnesses run with a nil DeviceStore. No t-7 contract says what RequireSession does when Devices is nil. A nil-deref on the 401 path would panic in those suites or, worse, in the running binary.
  Suggestion: Add a t-7 contract that says how RequireSession and the sweeper behave with nil Devices: the old plain 401 and no device cookie handling, consistent with how `api.requireSession` fails closed on a nil Auth.

- [wave order / file conflict] t-10 and t-12 are both wave 5 and both edit `internal/api/api.go`. t-12's description admits this: "Runs after t-10 when executed sequentially". Parallel execution (`--solo` wave fan-out) will give conflicting edits to the route table.
  Suggestion: Make the ordering real. Either add `depends_on = ["t-10"]` to t-12 and move it to wave 6 (t-13 then moves to wave 7), or move t-12's route registration into a file t-10 does not touch.

- [antipattern: file not listed] t-9 must return `403 reauth_required` (TestSetPIN_TaggedWithoutCookie). The repo keeps error codes as `Code*` constants in `internal/api/errors.go`, described as "part of the contract". But `errors.go` appears only in t-10's files, and t-10 runs a wave after t-9. t-12 also emits `reauth_required` and `bad_credentials` without listing errors.go. As written, t-9 either edits a file outside its scope or hard-codes a string literal, breaking the convention.
  Suggestion: Add `internal/api/errors.go` to t-9's files and have t-9 add `CodeReauthRequired`. t-10 then adds only the unlock-specific codes.

- [locked-decision / spec fidelity] t-10's TestUnlock_BadBody says malformed JSON or a >4 KiB body gives "no unlock log line". c-7 says "**Every** /api/v1/unlock attempt emits one structured info log line" with outcome in `ok|bad_pin|device_revoked|no_device`. The plan creates a silent path the criterion does not allow. A store-error 500 path also has no outcome defined. Not blocking, because "attempt" is arguably ambiguous, but a verifier reading c-7 literally will mark the 400 path as a miss.
  Suggestion: Either log 400s under an existing outcome, or get the carve-out ("a request that never reaches a PIN check is not an attempt") written into spec.toml so verification has something to check against.

## NOTE
- [coverage] All seven criteria are covered, and the coverage has substance. c-1's "plaintext nowhere in DB or debug logs" becomes TestSetPIN_NoPlaintext, which scans every table, the .db/-wal/-shm bytes and the log buffer. c-4's persisted counter is proven at the store level (t-2 SurvivesReopen) and at the binary level (t-13 PinSurvivesRestart). c-6's "SPA shows the full login page" is split cleanly between t-13 (unlock returns device_revoked) and t-6 (device_revoked renders Login).
- [locked-decision check] All five locked decisions are implemented as written: 15m sliding (t-5), 90d sliding only on unlock (t-7 NoDeviceSlide, t-10 DeviceLifetime), re-auth on change/remove sharing the login limiter (t-3 SharedBudget, t-12 WrongPassword gets 429), logout revokes the device (t-11), and unlock mints a session for device.username (t-10). Only the idle-window flag above concerns their effect, not their implementation.
- [strength] The test contracts are mutation-shaped ("if X, TestY fails: …") with exact boundaries: 14m59s vs 15m00s, 89d23h vs 90d+1s, and Arabic-Indic and fullwidth digits against `unicode.IsDigit`. That is the right level of specificity for this phase's gremlins/stryker verification.
- [strength] The strike counter's concurrency is pinned at two layers with exact counts: t-2's 20 goroutines give exactly 5 reservations, and t-10's 20 parallel unlocks give four bad_pin {4,3,2,1} and sixteen device_revoked. The two contracts agree with each other and with "ReserveAttempt before Argon2".
- [strength] Defining a "PIN session" by server state (`sessions.device_id IS NOT NULL`, never by cookie presence) closes a real hole. t-9 TestSetPIN_TaggedWithoutCookie proves that dropping the device cookie cannot bypass the locked `pin_change_reauth` rule.
- [rules] No conflicts with project rules r-01..r-04. There is no global `~/.claude/dross/rules.toml`. The new `golang.org/x/crypto` dependency is pinned to an exact version and does not touch any locked stack choice.
- [granularity] t-7 (9 contracts: token issuance, cookie attributes, 401 gate, sweeper) and t-10 (9 contracts) are the heaviest tasks. Each stays within one layer and at most 4 files, so neither is a split candidate under the rules, but expect them to take the most time.

## Summary
The plan is sound and well-specified, with complete coverage and no locked-decision violations. Fix the t-10/t-12 api.go wave collision, the nil-Devices contract and the errors.go scoping before execution, and raise the polling-versus-idle-lock interaction with the spec owner. As planned, that interaction lets the lock pass every test without ever triggering on a phone left open on Home.
