# Risk-lens draft — phase 01-config-adguard-client

Bias: start from what breaks. Every task below owns exactly one family of
failure modes and its test contract is the thing that catches that family.

Failure inventory the graph is shaped around:

| # | Failure | Owner |
|---|---|---|
| F1 | Secret escapes through `%v`/`%+v`/`%#v`/`slog`/JSON of the config struct | t-1 |
| F2 | Wrong precedence, typo'd YAML key silently ignored, `password_file` with trailing `\n`, explicit `--config` typo falls back to env, blank env var, relative `*_file` resolved against the wrong dir, userinfo smuggled into `adguard.url` | t-2 |
| F3 | Basic-auth token (base64 of `user:pass`) or password in request/error logs; hung AdGuard blocks forever; 3xx silently followed (POST→GET); unbounded error body; 401/403 indistinguishable from 5xx | t-3 |
| F4 | Fixtures written from memory pass in tests and fail on nora (v0.107.x shape drift); fake doesn't enforce auth so a missing header is never caught | t-4 |
| F5 | `Login` sends the service basic-auth header (AdGuard would accept any password); 400 vs 403 vs 429 from AdGuard mis-mapped; 5xx reported as "bad password" | t-5 |
| F6 | `clients: null`, `blocked_services: null` panics; a field our struct doesn't know is dropped on decode | t-6 |
| F7 | Read-modify-write drops an unknown field → AdGuard zeroes it on update (e.g. `use_global_settings` → filtering silently off); two concurrent writers interleave read/read/write/write; update on unknown client name | t-7 |
| F8 | `/healthz` calls AdGuard synchronously (liveness hangs when AdGuard hangs); data race between probe writer and handler readers; state stale forever after a single startup probe; later 401 kills the app | t-8 |
| F9 | Exit code 0 on bad config; startup 401 handled as "unreachable" (app keeps running with dead credential); startup 5xx exits (violates locked decision); debug-level startup log dumps config | t-9 |

```
Phase 01-config-adguard-client — 9 tasks across 3 waves

Wave 1
  t-1  Add redacting Secret type for credentials
       files:       internal/config/secret.go, internal/config/secret_test.go
       description: `config.Secret` string type whose String/GoString/Format/MarshalText/
                    MarshalJSON/LogValue all yield "[redacted]"; `Reveal()` returns the value.
                    Used for adguard.password and ai.api_key.
       covers:      c-2
       contract:    - a struct holding Secret("hunter2") logged at slog.LevelDebug through
                      BOTH slog.NewTextHandler and slog.NewJSONHandler into a buffer, plus
                      fmt.Sprintf with %v, %+v, %#v, %s, %q, and json.Marshal: the buffer/output
                      does not contain "hunter2"; removing any one of the redaction methods
                      makes that verb's assertion fail
                    - Reveal() returns "hunter2" (redaction is not lossy)
       depends_on:  []
       status:      pending

  t-3  AdGuard HTTP transport core with status classification
       files:       internal/adguard/client.go, internal/adguard/errors.go,
                    internal/adguard/client_test.go
       description: `adguard.New(baseURL, user, pass string, opts...)` validates the URL (http/https
                    scheme, no userinfo, trailing slash stripped), sets basic auth on every request
                    via a `do(ctx, method, path, in, out)` helper, 10 s default timeout, no redirect
                    following, error bodies read through a 1 KiB LimitReader. Sentinels:
                    `ErrUnauthorized` (401/403), `ErrRateLimited` (429), `*StatusError{Method,
                    Path, Status, Snippet}` for other non-2xx, transport errors wrapped with
                    method+path. Debug log per request: method, path, status, duration_ms — never
                    headers. Includes `Status(ctx) (Status{Version string}, error)` from
                    GET /control/status as the transport's own smoke test.
       covers:      c-2, c-7
       contract:    - a debug-level slog buffer captured across a 200, a 500, a 401 and a
                      connection-refused call, plus every returned error's Error() string, does
                      not contain the password nor base64("user:pass") (the Authorization header
                      value); logging the request headers fails this test
                    - New("127.0.0.1:80") (no scheme) and New("http://u:p@host") (userinfo) both
                      return an error naming adguard.url
                    - fake responding 401 → errors.Is(err, ErrUnauthorized); 403 → same; 429 →
                      ErrRateLimited; 503 → *StatusError with Status==503 and Snippet ≤ 1024
                      bytes when the body is 10 KiB; closed port → err wraps a *net.OpError and is
                      NOT ErrUnauthorized
                    - fake that sleeps 200 ms with a ctx deadline of 50 ms → errors.Is(err,
                      context.DeadlineExceeded)
                    - fake responding 302 → *StatusError with Status==302 (redirect not followed)
                    - every request recorded by the fake carries `Authorization: Basic …`
       depends_on:  []
       status:      pending

  t-4  Fake AdGuard server and v0.107.x fixtures
       files:       internal/adguard/adguardtest/fake.go, internal/adguard/adguardtest/fake_test.go,
                    internal/adguard/adguardtest/testdata/clients.json,
                    internal/adguard/adguardtest/testdata/blocked_services_get.json,
                    internal/adguard/adguardtest/testdata/blocked_services_all.json,
                    internal/adguard/adguardtest/testdata/status.json
       description: httptest-backed fake AdGuard (`adguardtest.New(t, Options{User, Pass})`) that
                    enforces basic auth on every /control/* route except /control/login, serves the
                    fixtures for GET /control/status, /control/clients, /control/blocked_services/get,
                    /control/blocked_services/all, implements POST /control/login (JSON {name,
                    password}; 403 "invalid username or password" on mismatch, status overridable)
                    and POST /control/clients/update (strict-decodes {name, data}, stores data
                    verbatim, exposes `LastUpdate()`), records every request (method, path,
                    headers, body) and supports failure injection: `SetStatus(path, code)`,
                    `Hang(path, d)`, `SetAuth(ok bool)`. Fixtures recorded with curl against a real
                    v0.107.x (nora) or transcribed from upstream openapi.yaml at that tag — API
                    only, nothing vendored; fixture client includes `"future_field": 42` and a
                    client with `"blocked_services": null`.
       covers:      c-3, c-4, c-5, c-6 (test infrastructure)
       contract:    - request to /control/clients without Authorization → 401; with wrong
                      password → 401; with right password → fixture bytes verbatim
                      (bytes.Equal against the embedded file)
                    - POST /control/login without Authorization and correct body creds → 200;
                      wrong password → 403 with body "invalid username or password"
                    - POST /control/clients/update with an unknown top-level key → 400 (strict
                      decode guards our own request shape)
                    - after Hang("/control/status", time.Second) a GET takes ≥ 1 s
                    - fixture JSON files unmarshal into map[string]any without error and
                      clients.json contains keys use_global_blocked_services,
                      blocked_services_schedule and blocked_services on every client (shape check
                      against the v0.107.x clientJSON)
       depends_on:  []
       status:      pending

Wave 2 (depends t-1 / t-3 / t-4)
  t-2  Config loader: YAML, env override, file indirection, validation
       files:       internal/config/config.go, internal/config/config_test.go, go.mod, go.sum
       description: `config.Load(path string, explicit bool, getenv func(string) (string, bool))
                    (*Config, error)`. Adds gopkg.in/yaml.v3 (v3.0.1, zero transitive deps,
                    KnownFields(true) for strict decode). Precedence: defaults < YAML < env, where
                    env keys are derived from yaml tags by reflection (ADGUARD_REWARD_ + path
                    joined with _, upper-cased) and a set-but-empty env var overrides to empty.
                    After merge: `*_file` read, trailing \r\n trimmed, resolved relative to the
                    config file's dir (CWD when env-only); both `password` and `password_file`
                    set → error. Validation names the dotted key: adguard.url (scheme, no
                    userinfo), adguard.username, adguard.password, log_level (slog parse).
                    Missing default file → env-only; missing explicit path → error. Secrets are
                    `config.Secret`.
       covers:      c-1
       contract:    - YAML with `adguard: {url, username}` and no password → error whose text
                      contains "adguard.password"; same for url and username individually
                    - YAML password "fromfile" vs env ADGUARD_REWARD_ADGUARD_PASSWORD="fromenv" →
                      Reveal()=="fromenv"; env set to "" → error naming adguard.password (blank
                      env does not fall through to YAML)
                    - `password_file` containing "s3cret\n" → Reveal()=="s3cret"; empty file →
                      error naming adguard.password_file; unreadable path → error naming
                      adguard.password_file; ADGUARD_REWARD_AI_API_KEY_FILE works the same for
                      ai.api_key
                    - `password_file: secret.txt` relative, config in t.TempDir(), CWD elsewhere
                      → loads (resolved against the config dir)
                    - both password and password_file set → error naming both keys
                    - YAML key `passwrod:` under adguard → error mentioning "passwrod"
                      (KnownFields); YAML syntax error → error with line number
                    - Load("/nope/config.yaml", explicit=true) → error containing the path;
                      Load(same, explicit=false) with env providing url/username/password → ok
                    - reflection walk: for every leaf field the derived env name appears in a
                      table that pins ADGUARD_REWARD_LISTEN, ADGUARD_REWARD_BASE_URL,
                      ADGUARD_REWARD_TLS_CERT, ADGUARD_REWARD_TLS_KEY, ADGUARD_REWARD_ADGUARD_URL,
                      ADGUARD_REWARD_ADGUARD_USERNAME, ADGUARD_REWARD_ADGUARD_PASSWORD,
                      ADGUARD_REWARD_ADGUARD_PASSWORD_FILE, ADGUARD_REWARD_DATA_DIR,
                      ADGUARD_REWARD_AI_PROVIDER, ADGUARD_REWARD_AI_API_KEY,
                      ADGUARD_REWARD_AI_API_KEY_FILE, ADGUARD_REWARD_AI_BASE_URL,
                      ADGUARD_REWARD_AI_MODEL, ADGUARD_REWARD_LOG_LEVEL — a renamed or added key
                      without a table entry fails
       depends_on:  [t-1]
       status:      pending

  t-5  Client.Login with ErrBadCredentials classification
       files:       internal/adguard/login.go, internal/adguard/login_test.go
       description: `Login(ctx, user, pass string) error` POSTs {name, password} to
                    /control/login WITHOUT the service basic-auth header and discards the cookie.
                    400/401/403 → `ErrBadCredentials`; 429 → `ErrRateLimited`; 5xx → *StatusError;
                    transport → wrapped. Body drained and closed on every path.
       covers:      c-3
       contract:    - the login request recorded by the fake has NO Authorization header
                      (adding basic auth to login makes this fail)
                    - correct creds → nil; wrong password → errors.Is(err, ErrBadCredentials)
                      for fake status 403, and also when the fake is switched to 400 and 401
                    - fake SetStatus("/control/login", 429) → ErrRateLimited, not
                      ErrBadCredentials
                    - fake SetStatus("/control/login", 503) → !errors.Is(ErrBadCredentials) and
                      errors.As(&StatusError) with Status 503; closed port → !errors.Is(
                      ErrBadCredentials) and a wrapped *net.OpError
                    - the submitted password never appears in a debug-level log buffer or in
                      the returned error string
       depends_on:  [t-3, t-4]
       status:      pending

  t-6  Typed Clients() and Services() reads
       files:       internal/adguard/clients.go, internal/adguard/services.go,
                    internal/adguard/clients_test.go, internal/adguard/services_test.go
       description: `Client{Name, IDs []string, BlockedServices []string,
                    UseGlobalBlockedServices bool, Raw json.RawMessage}` decoded from
                    GET /control/clients (`clients` array; auto_clients ignored); `Clients(ctx)
                    (ClientsResult{Persistent []Client, GlobalBlockedServices []string}, error)`
                    also reads GET /control/blocked_services/get ({ids, schedule}). `Services(ctx)
                    ([]Service{ID, Name, IconSVG}, error)` from GET /control/blocked_services/all
                    (`blocked_services` array, `icon_svg` field). Nil JSON lists → empty non-nil
                    slices.
       covers:      c-4, c-5
       contract:    - against the fixture, Clients() yields the fixture's client count, and for
                      the first client Name, IDs, BlockedServices and UseGlobalBlockedServices
                      equal the fixture values; GlobalBlockedServices equals the fixture's
                      `ids` from blocked_services_get.json
                    - the fixture client with `"blocked_services": null` decodes to an empty,
                      non-nil slice; `{"clients": null}` decodes to an empty Persistent slice
                      without panic
                    - Client.Raw for the fixture client containing `"future_field": 42` still
                      contains that key/value (json.Unmarshal into map and compare)
                    - fake returning 401 on /control/blocked_services/get → Clients() returns
                      ErrUnauthorized and no partial result (second-call failure is not
                      swallowed)
                    - Services() against a fake returning exactly two services, one with id
                      "zzz-not-in-any-catalogue", returns exactly those two — a vendored
                      catalogue could not pass this
                    - Services() with `{"blocked_services": null}` → empty slice, nil error
       depends_on:  [t-3, t-4]
       status:      pending

  t-8  Health probe state and /healthz handler
       files:       internal/health/health.go, internal/health/health_test.go, Makefile
       description: `health.New(prober, version string, log)` where `prober` is an interface
                    `Status(ctx) (adguard.Status, error)`. `Probe(ctx) Outcome` classifies:
                    nil → ok+version; errors.Is(ErrUnauthorized) → unauthorized; anything else →
                    unreachable; stores {adguard, adguard_version, at} under a sync.RWMutex.
                    `Run(ctx, interval)` re-probes on a ticker. `Handler()` writes 200 +
                    application/json `{status:"ok", adguard, adguard_version, version}` from the
                    stored state only — never calls AdGuard. Makefile `test` gains `-race`.
       covers:      c-7
       contract:    - after Probe against fixture /control/status, GET /healthz → 200, body
                      adguard=="ok" and adguard_version equals the fixture version
                    - fake SetStatus(status, 503) → healthz 200 with adguard=="unreachable";
                      fake SetAuth(false) → healthz 200 with adguard=="unauthorized"; closed port
                      → 200 "unreachable"; in all three adguard_version is "" (not null) and
                      status is "ok"
                    - fake Hang("/control/status", 2s) while a probe is in flight: a concurrent
                      GET /healthz returns within 100 ms (handler never awaits AdGuard)
                    - Run with interval 10 ms against a fake flipped from 200 to 503 after the
                      first probe: healthz transitions to "unreachable" within 200 ms, and back
                      to "ok" when flipped again (state is not frozen at startup); a later
                      SetAuth(false) yields "unauthorized" and Run keeps running (no exit)
                    - 50 goroutines hitting Handler while Run probes at 1 ms interval passes
                      under `go test -race` (removing the RWMutex fails the race detector)
       depends_on:  [t-3, t-4]
       status:      pending

Wave 3 (depends t-6 / t-2, t-3, t-8)
  t-7  SetBlockedServices read-modify-write under mutex
       files:       internal/adguard/update.go, internal/adguard/update_test.go
       description: `SetBlockedServices(ctx, clientName string, ids []string) error`: under a
                    per-Client sync.Mutex, GET /control/clients, find by exact name (missing →
                    `ErrClientNotFound`, no write), take that client's Raw object, replace only
                    the `blocked_services` member with the sorted, deduplicated ids, POST
                    /control/clients/update `{"name": clientName, "data": <object>}`. Warns (does
                    not refuse) when use_global_blocked_services is true.
       covers:      c-6
       contract:    - after SetBlockedServices("Kid phone", ["youtube","tiktok"]) the fake's
                      LastUpdate() body has name=="Kid phone" and data equal, as
                      map[string]any, to the fixture client with only `blocked_services`
                      replaced — including `"future_field": 42`, `blocked_services_schedule`,
                      `use_global_settings`, `tags`, `upstreams` byte-for-byte in value (decoding
                      into a typed struct and re-encoding drops future_field and fails this)
                    - ids ["b","a","a"] are written as ["a","b"]; ids nil → `[]` not null
                    - unknown client name → ErrClientNotFound and the fake recorded zero POSTs
                    - fake Hang("/control/clients", 30ms) with two concurrent calls on the same
                      Client: the fake's recorded sequence is GET,POST,GET,POST (removing the
                      mutex yields GET,GET,POST,POST)
                    - fake SetStatus(update, 400) → *StatusError with Status 400 and the
                      snippet text; fake SetAuth(false) → ErrUnauthorized
                    - a client with use_global_blocked_services=true produces a warn-level log
                      line containing "use_global_blocked_services" and the write still happens
       depends_on:  [t-6]
       status:      pending

  t-9  Wire main: --config, startup probe, exit policy
       files:       cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go, Makefile,
                    deploy/config.example.yaml
       description: Extract `run(ctx, args []string, getenv, stderr io.Writer, onListen
                    func(addr string)) int`. `--config` default = filepath.Join(dir(os.Executable),
                    "config.yaml"), `explicit` detected via flag.Visit; config.Load; slog level
                    from config; `adguard.New`; startup probe via health.Probe: unauthorized →
                    stderr message + return 1; unreachable → log error, continue; start
                    health.Run at 60 s; mount GET /healthz; `var version = "dev"` set by
                    Makefile LDFLAGS `-X main.version=$(git describe)`. Drop envOr and
                    TestEnvOr (env handled by config). Example config header documents the env
                    naming rule and the *_file keys.
       covers:      c-1, c-2, c-7
       contract:    - run with env providing url+username but no password → returns 1 and stderr
                      contains "adguard.password"; run with `--config /nope.yaml` → 1 and
                      stderr contains "/nope.yaml"; run with no --config, no file next to the
                      test binary, env complete → proceeds past config (fails later only at
                      probe or not at all)
                    - fake SetAuth(false) → run returns 1 and stderr contains "unauthorized"
                      and the AdGuard URL (not the password)
                    - fake SetStatus(status, 503) with listen 127.0.0.1:0 → run does NOT return
                      within 500 ms; onListen fires; GET /healthz on that addr → 200 with
                      adguard=="unreachable" and version=="dev"; cancelling ctx makes run
                      return 0
                    - fake healthy, log_level=debug: the complete stderr captured across
                      startup contains neither the password nor base64("user:pass") nor the AI
                      key "ai-key-test" while it does contain "adguard_version"
                    - `go build -ldflags "-X main.version=1.2.3"` then run → healthz
                      version=="1.2.3" (Makefile LDFLAGS wiring; tested via a build-tagged
                      test or by asserting the variable is package-level and non-const)
       depends_on:  [t-2, t-3, t-8]
       status:      pending
```

## Coverage

| criterion | tasks | note |
|---|---|---|
| c-1 | t-2, t-9 | t-2 owns precedence/indirection/validation errors; t-9 owns the non-zero exit and the flag/default-path semantics |
| c-2 | t-1, t-3, t-9 | t-1: formatting/slog redaction; t-3: request-log and error-string leaks incl. the base64 auth token; t-9: end-to-end stderr capture at debug |
| c-3 | t-5 (infra t-4) | |
| c-4 | t-6 (infra t-4) | |
| c-5 | t-6 (infra t-4) | |
| c-6 | t-7 (infra t-4, types t-6) | |
| c-7 | t-3 (Status), t-8 (state + handler), t-9 (startup policy, locked decisions startup_probe + healthz) | |

All 7 criteria covered. Locked decisions honoured: startup_probe (t-9), healthz (t-8 always-200, body-only state), env_naming (t-2 derived table), config_path (t-2 + t-9), adguard_auth (t-3 basic auth on every call; t-5 explicitly exempts login), adguard_api_target (t-4 fixtures pinned to v0.107.x, no legacy branch anywhere).

## Judgment calls

- **Spec slip flagged, not overridden:** in v0.107.x the *per-client* object is `blocked_services: []string` + `blocked_services_schedule: {...}`; the `{ids, schedule}` object in the `adguard_api_target` decision is the *global* `GET /control/blocked_services/get` shape. Read c-6's "only blocked_services.ids changes" as "only the client's blocked-services id list changes". t-4 pins this by shape-checking the fixture keys; if the live recording disagrees, the fixture wins and the judge should re-read the decision text.
- **Raw-preserving round-trip over typed struct** for c-6: chose to keep each client's `json.RawMessage` and splice one member, rejected decoding into a full typed struct — a struct drops any field added by a newer AdGuard and the update endpoint zeroes omitted fields (turning `use_global_settings` off silently disables filtering). Cost: a slightly uglier `Client` type. The `future_field` fixture makes this failure mode a red test.
- **Periodic re-probe (60 s) included** in t-8, rejected startup-only: c-7's "most recent probe" with a single probe means `/healthz` reports the boot-time state forever; a flaky AdGuard would show "unreachable" for the life of the process. A later 401 only changes the body, never exits — exit-on-401 is a startup-only policy in t-9.
- **Set-but-empty env var overrides to empty and fails validation loudly**, rejected the existing `envOr` "empty = unset" precedent: a forgotten `ADGUARD_REWARD_ADGUARD_PASSWORD=` in a compose file should die naming the key, not fall through to whatever the YAML says. `envOr` is removed in t-9.
- **`password` + `password_file` both set is an error**, rejected "file wins" or "inline wins": either silent precedence hides a misconfiguration; the error names both keys.
- **`*_file` relative to the config file's directory** (CWD when env-only), rejected CWD-always: systemd `WorkingDirectory` and the Docker entrypoint differ from a dev shell; anchoring to the config file is the only location the operator actually wrote down.
- **Reject userinfo in `adguard.url`** rather than supporting `http://user:pass@host`: it is a second credential channel that Go's `*url.Error` and our own logs would print; refusing it removes the leak class instead of redacting it.
- **Login maps 400/401/403 → ErrBadCredentials and 429 → ErrRateLimited** (an extra sentinel beyond the spec's two): AdGuard v0.107 minor versions have returned different codes for a bad password, and its login rate-limiter returns 429 — treating that as "wrong password" would tell a parent their password is wrong when it isn't. `ErrRateLimited` is cheap here and the login handler (phase 02) needs it.
- **`Login` deliberately omits basic auth** — the one exception to `adguard_auth`: the request's purpose is to test the *submitted* credential; sending the service credential alongside would make AdGuard authenticate the service, not the parent.
- **gopkg.in/yaml.v3 v3.0.1**, rejected goccy/go-yaml: yaml.v3 is archived (2025) but has zero transitive deps and `KnownFields(true)`; the input is a local operator-owned file so unpatched parser DoS is not in the threat model, and a frozen dep is a smaller supply-chain surface than an actively churning one. Rejected a hand-rolled subset parser outright.
- **Env names derived by reflection from yaml tags** with a pinned table test, rejected a hand-maintained switch: a new key must fail a test until its env name is documented, so the mapping and docs cannot drift.
- **`-race` added to `make test`** (t-8): the healthz reader/probe writer race is only observable under the detector. Note for phase 06: CI images with `CGO_ENABLED=0` must build with cgo on for tests or use a separate target.
- **Fake AdGuard is its own wave-1 task with self-tests**, rejected building it ad hoc inside each test file: four tasks depend on its auth enforcement and body echo; if the fake is lax (no auth check, lenient decode) every downstream "green" is meaningless.
- **Deploy finding, not fixed here:** `deploy/Dockerfile` ends with `CMD ["--config", "/data/config.yaml"]` — an *explicit* path, so under the locked `config_path` decision an env-only container exits on boot. Belongs to 06-deploy-nora; the judge may want to note it in the deferred list.
