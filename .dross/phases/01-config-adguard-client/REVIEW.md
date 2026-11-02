# Plan Review — 01-config-adguard-client

Reviewed: 2026-09-19
Plan: 6 tasks across 4 waves

## BLOCKING
(none)

## FLAG
- [locked-decision] t-3 maps `400/401/403 -> ErrBadCredentials` inside the shared `do()`, so every endpoint — not just `/control/login` — classifies a 400 as a credential rejection. Consequences: (a) the health prober (t-5) reports `unauthorized` for a 400 on `/control/status`; (b) main (t-6) exits 1 on a 400 from the startup probe, which widens the locked `startup_probe` decision ("rejected (401/403) exits non-zero") to a status it never named; (c) t-4's `TestSetBlockedServices_Errors` expects `SetStatus('/control/clients/update', 400)` to yield `*StatusError Code 400` — that still passes because `ErrBadCredentials` *wraps* the `*StatusError`, so the test cannot catch a malformed-body 400 being misreported as bad credentials. The 400 mapping exists only because older AdGuard builds answer `/control/login` with 400 on a bad password.
  Suggestion: scope the 400 case to `Login` (map 400 -> ErrBadCredentials only for `/control/login`; leave `do()` at 401/403), and add `!errors.Is(err, ErrBadCredentials)` to the 400 branch of `TestSetBlockedServices_Errors` so the distinction is pinned.

- [forbidden-actions] Rule r-01 forbids vendoring AdGuard assets. t-2's `blocked_services_all.json` fixture carries `icon_svg` per entry; if the fixture is captured verbatim from nora or upstream `openapi.yaml`, the repo ends up holding AdGuard's GPL-licensed service icons (test-only, but still committed). `TestServices` only asserts `Icon == icon_svg` from the fixture, so real SVG content is not needed.
  Suggestion: use placeholder `icon_svg` values (e.g. `"<svg/>"`) in the fixture and say so in the fixture's version note.

- [antipattern] `deploy/Dockerfile` already has `CMD ["--config", "/data/config.yaml"]`. Under the locked `config_path` rule an explicitly passed missing path is an error, so an env-only container start — which that decision's `why` names as *the* container case — will exit 1 once t-6 lands. Nothing in the plan touches or records this; the Docker release is an M1 non-goal, so it is easy to lose.
  Suggestion: either add `deploy/Dockerfile` to t-6's files with the one-line CMD change (drop the explicit `--config`, or make the default path fall back to `/data/config.yaml` when set), or record it in spec.toml `[[deferred]]` targeting the M4 Docker phase so it is not forgotten.

- [test-contract] t-6's last contract mixes a compile-time property with a manual step: "version is asserted package-level and non-const" is not something a `go test` can fail on (taking `&version` is a build-time check), and "manual gate recorded in the verify run: make build then ./bin/adguard-reward --config missing.yaml exits 1" is a human procedure inside a test contract. The verify phase maps criteria to tests; a manual gate here will map to nothing.
  Suggestion: keep the `&version` compile check as a one-liner in `main_test.go`, and move the `make build` smoke into a Makefile `smoke` target (or a verify.toml note) rather than a test contract.

- [test-contract] Timing-bound assertions in t-5 (`TestRun_Transitions` "within 200ms each", `TestRun_Ticker` "at least 2 calls after 50ms") and t-6 (`TestRun_StartupUnreachable` "does not return within 500ms") are the usual `-race` flake sources on a loaded machine. Not wrong, but t-5 also adds `-race` to `make test`, which slows goroutine scheduling by 2-10x.
  Suggestion: make the stubs signal on a channel (probe-completed / listener-up) and wait on that with a generous timeout, instead of asserting wall-clock bounds.

- [antipattern] t-3 says `do()` "never follows redirects" and `TestDo_NoRedirect` pins it, but `WithHTTPClient` lets a caller inject an `http.Client` whose `CheckRedirect` is nil (follow by default). The contract only tests the default client, so the policy silently disappears for any injected client — including the one t-6 or a later phase might supply for timeouts.
  Suggestion: have `WithHTTPClient` copy the client and force `CheckRedirect`, or have `TestDo_NoRedirect` also run with an injected plain `&http.Client{}`.

## NOTE
- [coverage] All seven criteria are covered: c-1 (t-1, t-6), c-2 (t-1, t-3, t-6), c-3 (t-2, t-3), c-4 (t-2, t-4), c-5 (t-2, t-4), c-6 (t-2, t-4), c-7 (t-3, t-5, t-6). c-2 in particular is pinned three times, at increasing scope (type, client log, process stderr), which is the right shape for a never-leak criterion.
- [locked-decision] `Login` deliberately omits the service basic-auth header and calls it "the one documented exception to adguard_auth". This is consistent with the decision's intent (the app's *own* calls use the service credential; Login forwards a parent's credential), not a conflict. Worth keeping that sentence in the code comment.
- [wave-order] Wave assignments are tight: t-1 and t-2 are genuinely independent; t-4 and t-5 both need t-3's `Status`/`ErrBadCredentials` types and touch disjoint files; t-6 correctly does not depend on t-4 (main never calls `Clients`). No task could drop a wave.
- [granularity] t-1 lists 6 files but two are `go.mod`/`go.sum` (yaml.v3 dependency) — one package, not a split candidate. `Makefile` is edited by both t-5 (`-race`) and t-6 (VERSION/LDFLAGS/dev), but t-6 depends on t-5 so the edits are sequential.
- [antipattern] All referenced pre-existing files exist (`cmd/adguard-reward/main.go`, `main_test.go`, `Makefile`, `deploy/config.example.yaml`, `go.mod`); `go.sum` is created by t-1 and the Dockerfile's `go.sum*` glob already tolerates its absence. No stale `--listen`/`envOr` references exist outside `main.go` and `main_test.go`, so t-6's removal is clean.
- [antipattern] The "Kid phone" fixture client carries a synthetic `"future_field": 42`, so `clients.json` is by construction not a verbatim capture even after the "confirm against nora / openapi.yaml" step. That is fine (the RMW-preservation test needs it) but the fixture's version note should say which keys are synthetic, or the next person to refresh the fixture will drop it and break `TestSetBlockedServices_Preserves`.
- [antipattern] `make dev` now passes `--config config.yaml` explicitly (needed because `os.Executable()` under `go run` is a temp dir). A developer without a `config.yaml` gets a hard error from `make dev` rather than an env-only start; that is the locked rule applied correctly, but the `dev` target's help text should say so.
- [strength] Test contracts are uniformly of the form "if X breaks, TestY fails: <observable>" with concrete values (`s3cret\n`, `pw-CANARY-9f3a`, `base64(user:pass)`, `zzz-not-in-any-catalogue`). Every contract names the surface that breaks; none is "tests pass".
- [strength] Building the fake AdGuard as an importable package with request recording, `SetStatus`/`Hang`/`MutateClient`/`MaxInFlightUpdates` knobs, and its own self-tests (t-2) is what makes t-3/t-4/t-6 contracts cheap and precise — the RMW field-preservation and mutex-serialisation tests would be hand-waved without it.
- [strength] The `Secret` type covering `String/GoString/Format/MarshalText/MarshalJSON/LogValue` with `Reveal()` as the only exit, plus positive controls in every no-leak test ("contains 'listening' and '/control/status'"), means a silent logger cannot make a leak test pass vacuously.

## Summary
No blocking findings; the plan is executable as written, with the global 400 -> ErrBadCredentials mapping, the GPL icon content in the fixture, and the Dockerfile's explicit `--config` being the three items worth fixing before execution.
