# Synthesis — phase 01-config-adguard-client

Cold judge over three independent drafts (risk / mvp / verification). Claims about
existing files were checked against the tree: `cmd/adguard-reward/main.go` has the
`--listen` flag + `envOr` + plain-text `/healthz`; `main_test.go` has `TestEnvOr`;
`Makefile` `test` target is `go test ./...` with no `-race`, `build` is
`CGO_ENABLED=0`, `LDFLAGS ?= -s -w` with no `-X`; `deploy/Dockerfile` ends
`CMD ["--config", "/data/config.yaml"]`; `deploy/config.example.yaml` header says
"see docs/" and no `docs/` exists; `internal/adguard/` is empty; `go.mod` has no deps.

## Scores

Scale 1–5.

| dimension | risk (9 tasks / 3 waves) | mvp (3 tasks / 2 waves) | verification (6 tasks / 4 waves) |
|---|---|---|---|
| criteria coverage | 5 — every criterion 2–3 tasks, all locked decisions traced | 4 — all 7 hit, but c-2 is LogValuer-only (no `%v`/`%+v`/`json.Marshal` path) and c-6 has no concurrency or fresh-read contract | 5 — all 7, with a per-criterion "the test that catches the regression" column |
| test-contract specificity | 5 — mutation-style throughout ("removing the mutex yields GET,GET,POST,POST"); base64 auth-token absence; 1 KiB snippet bound | 3 — mostly assertion-shaped, several weak ("non-empty Name and Icon" passes with a vendored catalogue; login contract does not check the Authorization header is absent) | 5 — same rigour as risk, plus positive controls on every absence assertion (the one thing risk lacks) |
| granularity | 3 — t-1 (Secret type, 2 files) and t-5 (Login, 2 files) are under the 10-min floor; t-4/t-6/t-7 split the same package three ways | 2 — t-2 is transport + Login + Status + Clients + Services + RMW + fake + fixtures in one task; multi-hour, no seam for review | 4 — no task under the floor, none over 5 files; t-4 (reads + RMW, 4 files) is the largest and still one layer |
| wave correctness | 3 — t-3 sits in wave 1 with `depends_on: []` while its own contract uses "the fake" (SetStatus, Hang, recorded requests), so it really depends on t-4; the 3-wave count is bought with that error | 4 — graph is trivially right (t-1 ∥ t-2 → t-3) | 5 — every edge is a real type/package dependency (t-3 needs the fake, t-4/t-5 need `adguard.Status`/sentinel, t-6 needs all three); 4 waves is honest, not padded |

**Skeleton: verification.** It has the best granularity and the only wave graph
without an error, contracts as sharp as risk's plus positive controls, and it
already carries the two design choices the other lenses converge on (raw-preserving
RMW, importable stateful fake). Risk contributes the hardening contracts and the
failure-injection knobs; mvp contributes almost nothing structurally but is the
source of two disagreements worth recording (YAML lib, startup-only probe).

## Merged plan

```
Phase 01-config-adguard-client — 6 tasks across 4 waves

Wave 1
  t-1  Config package with Secret redaction                       [verification+risk+mvp]
       files:       internal/config/config.go
                    internal/config/secret.go
                    internal/config/config_test.go
                    internal/config/testdata/full.yaml
                    go.mod, go.sum
       description: `config.Load(path string, explicit bool, lookupEnv func(string) (string, bool))
                    (*Config, error)`. Struct mirrors the PLAN.md key set (listen, base_url, tls.cert/key,
                    adguard.url/username/password/password_file, data_dir, ai.provider/api_key/
                    api_key_file/base_url/model, log_level). Parse via gopkg.in/yaml.v3 v3.0.1 with
                    KnownFields(true). Precedence defaults < YAML < env; env names from an explicit
                    table (dotted key → ADGUARD_REWARD_ + upper-cased path joined with `_`); a set-but-
                    empty env var overrides to empty (it does NOT fall through to YAML). After merge:
                    `*_file` read, trailing `\r\n` trimmed only (not TrimSpace), path resolved relative
                    to the config file's directory (CWD when env-only); `password`+`password_file`
                    both set → error naming both. Validation names the dotted key: adguard.url (http/
                    https scheme, no userinfo), adguard.username, adguard.password, log_level.
                    Missing default file → env-only; missing explicit path → error containing the path.
                    `type Secret string` in secret.go: String/GoString/Format/MarshalText/MarshalJSON/
                    LogValue all yield "[redacted]", `Reveal()` is the only way out; used for
                    adguard.password and ai.api_key. `Config.SlogLevel()` maps log_level.
       covers:      c-1, c-2
       contract:    - [all] yaml with adguard.url+username+password loads; removing any one → error whose
                      text contains the dotted key ("adguard.password" / "adguard.url" / "adguard.username")
                    - [mvp+risk] YAML password "yamlpw" + ADGUARD_REWARD_ADGUARD_PASSWORD=envpw →
                      Password.Reveal()=="envpw"; [risk] env set to "" → error naming adguard.password
                      (blank env does not fall through to YAML)
                    - [verification+risk] a table test pins every leaf's env name (ADGUARD_REWARD_LISTEN,
                      _BASE_URL, _TLS_CERT, _TLS_KEY, _ADGUARD_URL, _ADGUARD_USERNAME, _ADGUARD_PASSWORD,
                      _ADGUARD_PASSWORD_FILE, _DATA_DIR, _AI_PROVIDER, _AI_API_KEY, _AI_API_KEY_FILE,
                      _AI_BASE_URL, _AI_MODEL, _LOG_LEVEL); a reflection walk over the struct asserts every
                      yaml-tagged leaf has a table row, so a renamed or added key without a row fails
                    - [all] ADGUARD_REWARD_ADGUARD_PASSWORD_FILE → temp file "s3cret\n" → Reveal()=="s3cret";
                      [risk] empty file → error naming adguard.password_file; unreadable path → same;
                      ADGUARD_REWARD_AI_API_KEY_FILE behaves identically for ai.api_key
                    - [risk] `password_file: secret.txt` relative, config in t.TempDir(), CWD elsewhere → loads
                    - [verification+risk] password and password_file both set → error naming both keys
                    - [risk] YAML key `passwrod:` under adguard → error mentioning "passwrod" (KnownFields);
                      YAML syntax error → error carrying a line number
                    - [all] Load("/nope/config.yaml", explicit=true) → error containing the path;
                      Load(same, explicit=false) with env supplying the three required keys → ok
                    - [verification+risk] fmt.Sprintf %v/%+v/%#v/%s/%q, json.Marshal(cfg), and a slog
                      TextHandler AND JSONHandler at Debug logging cfg as an attr: output lacks the
                      password and api key, contains "[redacted]"; Reveal() returns the original
                    - [verification] log_level "debug" → slog.LevelDebug; "bogus" → error naming "log_level"
       depends_on:  []
       status:      pending

  t-2  Fake AdGuard server and v0.107.x fixtures                  [verification+risk]
       files:       internal/adguard/adguardtest/server.go
                    internal/adguard/adguardtest/server_test.go
                    internal/adguard/adguardtest/testdata/clients.json
                    internal/adguard/adguardtest/testdata/blocked_services_get.json
                    internal/adguard/adguardtest/testdata/blocked_services_all.json
                    internal/adguard/adguardtest/testdata/status.json
       description: Importable (non-_test) package wrapping httptest.Server, `adguardtest.New(t, Options{
                    User, Pass})`. Fixtures go:embed'd in the v0.107.x shape: per-client
                    `blocked_services: [..]` + `blocked_services_schedule`; global /control/blocked_services/get
                    → `{ids, schedule}`; /all → `{blocked_services: [{id,name,icon_svg,rules}]}`; /status →
                    `{version,...}`. Fixture clients include one with use_global_blocked_services=true, one
                    "Kid phone" with ids ["youtube","tiktok"], one with `"blocked_services": null`, and the
                    "Kid phone" object carries `"future_field": 42`, `safe_search` object and
                    `upstreams_cache_size: null`. Each fixture notes the version/openapi tag it was checked
                    against. Enforces basic auth on every /control/* except /control/login (401 otherwise);
                    /control/login takes JSON {name,password}, 403 on mismatch (status overridable); POST
                    /control/clients/update strict-decodes {name, data} (400 on unknown top-level key),
                    stores data verbatim so a following GET /control/clients reflects it, exposes
                    LastUpdate(). Records every request (method, path, Authorization present?, raw body) via
                    Requests(). Knobs: SetStatus(path, code), Hang(path, d), SetAuth(ok), MutateClient(name,
                    fn), MaxInFlightUpdates(), Close().
       covers:      c-3, c-4, c-5, c-6 (test infrastructure)
       contract:    - [both] GET /control/clients without Authorization → 401; wrong password → 401; right
                      password → the embedded fixture bytes verbatim (bytes.Equal)
                    - [both] POST /control/login with configured creds and NO Authorization → 200; wrong
                      password → 403 with body "invalid username or password"; SetStatus("/control/login",
                      400) makes the rejection 400
                    - [verification] POST /control/clients/update {name,data} then GET /control/clients returns
                      `data` verbatim, unknown keys included (the echo c-6 depends on)
                    - [risk] POST /control/clients/update with an unknown top-level key → 400
                    - [risk] after Hang("/control/status", 1s) a GET takes ≥ 1 s
                    - [both] every fixture decodes; clients.json has use_global_blocked_services,
                      blocked_services (array-or-null) and blocked_services_schedule on every client;
                      blocked_services_get has `ids` and `schedule`; blocked_services_all.blocked_services[0]
                      has id/name/icon_svg; status has `version`
                    - [verification] Requests() returns entries in arrival order with raw bodies preserved
       depends_on:  []
       status:      pending

Wave 2 (depends t-2)
  t-3  AdGuard client core, Login, Status                          [verification+risk]
       files:       internal/adguard/client.go
                    internal/adguard/errors.go
                    internal/adguard/client_test.go
       description: `adguard.New(baseURL, username, password string, opts ...Option) (*Client, error)` with
                    WithHTTPClient/WithLogger. New validates the URL: http/https scheme, no userinfo,
                    trailing slash stripped. Unexported `do(ctx, method, path, in, out)` sets basic auth on
                    every call, never follows redirects (CheckRedirect returns ErrUseLastResponse), reads
                    error bodies through a 1 KiB LimitReader, 10 s default timeout. Mapping: 2xx ok;
                    401/403 → `fmt.Errorf("%w: %w", ErrBadCredentials, &StatusError{...})`; 429 →
                    ErrRateLimited; other non-2xx → `*StatusError{Method, Path, Code, Snippet}`; transport →
                    wrapped `*url.Error` with method+path. Debug log per request: method, path, status,
                    duration_ms — never headers. `Login(ctx, user, pass) error` POSTs /control/login
                    {name,password} WITHOUT the service basic-auth header, discards the cookie, drains and
                    closes the body; 400/401/403 → ErrBadCredentials, 429 → ErrRateLimited, 5xx →
                    *StatusError. `Status(ctx) (Status{Version, Running, ProtectionEnabled}, error)` GETs
                    /control/status.
       covers:      c-3, c-7, c-2
       contract:    - [both] fake login rejecting with 403 (default), 400 and 401 → errors.Is(err,
                      ErrBadCredentials) for each; correct creds → nil
                    - [both] fake login 502/503 → !errors.Is(ErrBadCredentials), errors.As *StatusError with
                      Code and Path "/control/login"; fake closed before Login → !ErrBadCredentials and
                      errors.As *url.Error
                    - [risk] SetStatus("/control/login", 429) → ErrRateLimited, not ErrBadCredentials
                    - [both] the recorded /control/login request has NO Authorization header and its body
                      decodes to exactly {"name":"u","password":"p"}
                    - [both] the recorded /control/status request carries "Basic "+base64(user:pass)
                    - [both] SetStatus("/control/status", 401) → Status returns ErrBadCredentials; 503 →
                      *StatusError Code 503; fixture → Version=="v0.107.52" (fixture value)
                    - [risk] SetStatus(..., 503) with a 10 KiB body → StatusError.Snippet ≤ 1024 bytes
                    - [risk] fake responding 302 → *StatusError Code 302 (redirect not followed)
                    - [risk] Hang("/control/status", 200ms) with ctx deadline 50ms → errors.Is(err,
                      context.DeadlineExceeded)
                    - [both] WithLogger at Debug into a buffer, across a 200, 500, 401 and closed-port call
                      plus every returned error's Error(): buffer contains "GET" and "/control/status"
                      (positive control) and contains neither the password nor base64(user:pass)
                    - [both] New("://bad"), New("127.0.0.1:80") (no scheme) and New("http://u:p@host")
                      (userinfo) return an error naming adguard.url; New without WithHTTPClient has Timeout>0
       depends_on:  [t-2]
       status:      pending

Wave 3 (depends t-3)
  t-4  Clients, Services, SetBlockedServices read-modify-write      [verification+risk]
       files:       internal/adguard/clients.go
                    internal/adguard/services.go
                    internal/adguard/clients_test.go
                    internal/adguard/services_test.go
       description: `Clients(ctx) (ClientsResult, error)` = GET /control/clients (`clients`; auto_clients
                    ignored) + GET /control/blocked_services/get → `[]PersistentClient{Name, IDs,
                    BlockedServices, UseGlobalBlockedServices}` + `GlobalBlockedServices []string`; each
                    PersistentClient keeps its raw object as unexported `map[string]json.RawMessage`. Nil JSON
                    lists → empty non-nil slices. `Services(ctx) ([]Service{ID, Name, Icon}, error)` from
                    /control/blocked_services/all (Icon = icon_svg). `SetBlockedServices(ctx, clientName,
                    ids)` under a client-wide sync.Mutex: fresh GET, find by exact name (else
                    ErrClientNotFound, no write), copy raw, replace only "blocked_services" with the sorted,
                    deduplicated ids (nil/empty → `[]`, never null), POST /control/clients/update
                    {name: clientName, data: raw}. Warn-level log (not a refusal) when the client has
                    use_global_blocked_services=true.
       covers:      c-4, c-5, c-6
       contract:    - [both] against the fixture, Clients() returns the fixture's client count; "Kid phone"
                      Name/IDs/BlockedServices/UseGlobalBlockedServices equal the fixture literals;
                      GlobalBlockedServices equals blocked_services_get.json `ids`
                    - [risk] the fixture client with `"blocked_services": null` decodes to an empty non-nil
                      slice; fake serving `{"clients": null}` → empty Persistent slice, no panic
                    - [both] after SetBlockedServices("Kid phone", ["youtube","tiktok"]) the echoed update
                      body has name=="Kid phone" and `data` with "blocked_services" deleted deep-equals the
                      original raw object with "blocked_services" deleted — `"future_field": 42`,
                      `blocked_services_schedule`, `safe_search`, `upstreams_cache_size: null`, `tags`,
                      `upstreams` all preserved; data.blocked_services == ["youtube","tiktok"] (a typed
                      round-trip that drops or zeroes a field fails this)
                    - [risk] ids ["b","a","a"] are written as ["a","b"]; [both] ids nil → `"blocked_services":[]`
                    - [verification] MutateClient changes the client's ids between Clients() and
                      SetBlockedServices(); the echoed body carries the mutated ids (fresh read, not cached)
                    - [both] SetBlockedServices("nobody", ...) → errors.Is(ErrClientNotFound) and the fake
                      recorded zero /control/clients/update requests
                    - [verification] Hang("/control/clients", 20ms), 5 goroutines concurrent →
                      MaxInFlightUpdates()==1 and the recorded sequence never interleaves GET,GET,POST,POST
                      (removing the mutex fails this)
                    - [risk] SetStatus("/control/clients/update", 400) → *StatusError Code 400 with snippet;
                      SetAuth(false) → ErrBadCredentials
                    - [risk] a client with use_global_blocked_services=true → warn-level log line containing
                      "use_global_blocked_services" and the write still happens
                    - [risk] fake 401 on /control/blocked_services/get only → Clients() returns
                      ErrBadCredentials and no partial result (second-call failure not swallowed)
                    - [both] Services() against the 3-entry fixture returns 3 with Icon == icon_svg; fake
                      serving exactly two services, one with id "zzz-not-in-any-catalogue" → exactly those two
                      (a vendored catalogue cannot pass — r-01); `{"blocked_services": null}` → empty slice,
                      nil error
       depends_on:  [t-3]
       status:      pending

  t-5  Health prober and /healthz handler                          [verification+risk]
       files:       internal/health/health.go
                    internal/health/health_test.go
                    Makefile
       description: `health.New(status StatusClient, appVersion string, log *slog.Logger) *Prober` where
                    StatusClient is `interface{ Status(ctx) (adguard.Status, error) }`. `Probe(ctx)
                    Result{State, Version, Err}` classifies: nil → "ok"; errors.Is(ErrBadCredentials) →
                    "unauthorized"; anything else → "unreachable"; stores the snapshot under a sync.RWMutex;
                    one Info line (adguard_version attr) on success, one Error line on failure. `Handler()
                    http.HandlerFunc` always writes 200 application/json {status:"ok", adguard,
                    adguard_version, version} from stored state only — never calls AdGuard. `Run(ctx,
                    interval)` re-probes on a ticker until ctx is done; a later "unauthorized" only changes
                    the body, never exits. Makefile `test` target gains `-race`.
       covers:      c-7
       contract:    - [verification] stub returning Status{Version:"v0.107.52"} then GET → 200, Content-Type
                      application/json, body decodes to exactly {"status":"ok","adguard":"ok",
                      "adguard_version":"v0.107.52","version":"test-1"}
                    - [both] stub returning ErrBadCredentials-wrapped error → 200, adguard=="unauthorized";
                      *StatusError{503} or *url.Error → 200, adguard=="unreachable"; in both
                      adguard_version is "" (not null) and status is "ok" (locked healthz: never a non-200)
                    - [both] probe fail then succeed → "ok"; succeed then fail → "unreachable"; Run(ctx, 10ms)
                      against a stub flipped ok→503→ok→ErrBadCredentials transitions through
                      unreachable/ok/unauthorized within 200 ms each and Run keeps running (no exit)
                    - [verification] Handler before any Probe → 200 with adguard=="unreachable" (closed enum)
                    - [risk] a stub that blocks on a channel while a probe is in flight: a concurrent GET
                      /healthz returns within 100 ms (handler never awaits AdGuard)
                    - [risk] 50 goroutines hitting Handler while Run probes at 1 ms passes under
                      `go test -race` (removing the RWMutex fails the race detector)
                    - [verification] Run(ctx, 5ms) with a counting stub: ≥2 calls after 50 ms; cancelling ctx
                      returns from Run; Info log on success contains "v0.107.52"; Error log contains err text
       depends_on:  [t-3]
       status:      pending

Wave 4 (depends t-1, t-3, t-5)
  t-6  Wire main: --config, startup probe, healthz JSON            [verification+risk+mvp]
       files:       cmd/adguard-reward/main.go
                    cmd/adguard-reward/main_test.go
                    Makefile
                    deploy/config.example.yaml
       description: Replace the `--listen` flag and envOr with `--config` (default: config.yaml beside
                    os.Executable(); explicit-vs-default via flag.Visit). Extract `run(ctx, args []string,
                    lookupEnv func(string) (string, bool), stderr io.Writer, onListen func(net.Addr)) int`;
                    main() calls os.Exit(run(...)). Sequence: config.Load → slog handler at cfg.SlogLevel()
                    on stderr → adguard.New → health.New → Probe: ErrBadCredentials → Error log "adguard
                    rejected the service credential (adguard.username / adguard.password)" + AdGuard URL,
                    return 1; any other error → Error log, continue; success → Info with adguard_version.
                    Mount GET /healthz → prober.Handler(); start prober.Run(ctx, 60s); net.Listen(cfg.Listen)
                    then Serve with the existing graceful shutdown. `var version = "dev"`; Makefile gains
                    `VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)` and
                    `-X main.version=$(VERSION)` in LDFLAGS. Delete envOr and TestEnvOr. Replace the
                    example config's "see docs/" header with the env-naming rule (ADGUARD_REWARD_ + path
                    joined by `_`) and the *_file keys.
       covers:      c-1, c-2, c-7
       contract:    - [all] run with --config at a temp yaml lacking adguard.password → 1, stderr contains
                      "adguard.password"; [mvp] env lacking ADGUARD_REWARD_ADGUARD_URL → 1, stderr "adguard.url"
                    - [all] run with --config /nonexistent/config.yaml → 1, stderr contains that path; run
                      with no --config, no config.yaml beside the test binary, three ADGUARD_REWARD_ADGUARD_*
                      env vars set → proceeds to the probe (fake records /control/status)
                    - [all] SetStatus("/control/status", 401) → run returns 1, stderr contains "rejected",
                      "adguard.username" and the AdGuard URL, not the password (startup_probe half one)
                    - [all] SetStatus("/control/status", 503) → run does NOT return within 500 ms; onListen
                      fires; GET http://<addr>/healthz → 200 with adguard=="unreachable"; cancelling ctx
                      makes run return 0 (startup_probe half two)
                    - [all] fake healthy → /healthz adguard=="ok", adguard_version=="v0.107.52",
                      version=="test-1" (test sets the package var), Content-Type application/json
                    - [verification+risk] c-2 end-to-end: yaml password "pw-CANARY-9f3a", ai.api_key
                      "sk-CANARY-77b1", ADGUARD_REWARD_LOG_LEVEL=debug, stderr captured through startup and
                      one /healthz request: contains "listening" and "/control/status" (positive control)
                      and none of "pw-CANARY-9f3a", "sk-CANARY-77b1", base64("svc:pw-CANARY-9f3a")
                    - [risk] `version` is asserted package-level and non-const so `-X main.version` can bind;
                      [verification] `make build` then `./bin/adguard-reward --config missing.yaml` exits 1
                      (manual gate recorded in the verify run, not a go test)
       depends_on:  [t-1, t-3, t-5]
       status:      pending
```

### Coverage

| criterion | tasks | regression-catching test |
|---|---|---|
| c-1 | t-1, t-6 | t-1 missing-key names the dotted key; env table + reflection walk; *_file indirection; explicit-vs-default path. t-6 run() returns 1 with the key in stderr |
| c-2 | t-1, t-3, t-6 | t-1 Secret under fmt/json/slog text+json; t-3 request log lacks password and basic token across four outcomes; t-6 whole-startup debug capture with positive control |
| c-3 | t-2, t-3 | t-3 400/401/403 → ErrBadCredentials, 429 → ErrRateLimited, 5xx → StatusError, closed port → url.Error; login has no basic auth |
| c-4 | t-2, t-4 | t-4 typed Clients() incl. global ids, null-safety; t-2 asserts fixture keys are v0.107.x |
| c-5 | t-2, t-4 | t-4 Services() maps id/name/icon_svg; unknown-id catalogue round-trips (no vendoring) |
| c-6 | t-2, t-4 | t-4 echoed `data` deep-equals original minus blocked_services incl. unknown field + null; fresh read; serialised; unknown client → no POST |
| c-7 | t-3, t-5, t-6 | t-3 Status() typed; t-5 classification + always-200 + last-probe-wins + non-blocking + race-clean; t-6 401 exits 1, 503 keeps serving |

Locked decisions: startup_probe (t-5 classification, t-6 exit policy), healthz
(t-5/t-6 always-200), env_naming (t-1 table incl. ADGUARD_REWARD_ADGUARD_PASSWORD_FILE),
config_path (t-1 explicit flag + t-6 default beside binary), adguard_auth (t-2 fake
enforces basic auth on every /control/* so every test asserts it; Login is the one
documented exception), adguard_api_target (t-2 fixtures, no legacy branch; see flag 1).

Grafts taken without contest (present in one draft, contradicted by none): risk's
`*_file` relative to the config dir; risk's userinfo rejection in adguard.url;
risk's redirect/LimitReader/deadline hardening; risk's sorted+dedup ids; risk's
warn-on-use_global_blocked_services; risk's `-race`; verification's positive
controls; verification's MutateClient fresh-read test; verification's
`Handler-before-Probe → unreachable`.

## Disagreements

1. **Task count and where the seams are.** risk 9 / mvp 3 / verification 6. mvp's
   t-2 is one task for transport, Login, Status, reads, RMW, fake and fixtures; risk
   splits Secret and Login into their own 2-file tasks. Default: verification's six
   (Secret folded into config; Login folded into transport; reads+RMW together). Why
   it matters: the mvp shape has no reviewable commit boundary for the c-6 RMW, and
   risk's extra seams cost a wave without adding parallelism.

2. **Wave placement of the transport task.** risk puts it in wave 1 with no deps;
   verification makes it wave 2 after the fake. risk's contract uses the fake in five
   lines, so its wave-1 claim only holds if the task builds throwaway httptest
   closures too. Default: wave 2. Why it matters: it is the difference between a
   3-wave and a 4-wave plan, and an honest 4 is better than a 3 that duplicates the
   fake.

3. **YAML parser.** mvp: github.com/goccy/go-yaml (MIT, maintained). risk +
   verification: gopkg.in/yaml.v3 v3.0.1 (archived April 2025, zero transitive deps,
   `KnownFields(true)`). Default: yaml.v3. Why it matters: KnownFields gives the
   typo'd-key test for free and the input is an operator-owned local file, so
   unpatched-parser DoS is out of the threat model; but the archived status is a
   supply-chain talking point the user may weigh differently (flag 4).

4. **Env-name derivation.** risk: reflection over yaml tags at load time. mvp:
   explicit table. verification: table test over every leaf, mechanism unstated.
   Default: explicit table in code plus a reflection-based *test* that walks the
   struct and fails on any leaf without a table row. Why it matters: risk's goal
   (a new key cannot ship undocumented) is met without runtime reflection, which
   mvp rightly calls more machinery than 15 keys need.

5. **Blank env var semantics.** risk: set-but-empty overrides to empty and fails
   validation naming the key. mvp's `getenv func(string) string` cannot tell empty
   from unset, i.e. it keeps the existing `envOr` "empty = unset" behaviour.
   verification's `lookupEnv (string, bool)` can tell but does not say. Default:
   risk. Why it matters: a compose file with `ADGUARD_REWARD_ADGUARD_PASSWORD=`
   (unset substitution) either dies naming the key or silently runs on whatever the
   YAML says; the locked startup_probe rationale ("bad config must die loudly")
   points at the former.

6. **Error sentinel set.** risk: `ErrUnauthorized` (401/403 on service calls),
   `ErrBadCredentials` (Login), `ErrRateLimited` (429). mvp + verification: one
   `ErrBadCredentials` for both. Default: one `ErrBadCredentials` for 400/401/403
   everywhere plus `ErrRateLimited` for 429. Why it matters: c-3 only demands one
   distinct credentials error and both consumers (healthz classification, startup
   exit) need only "rejected"; but mapping AdGuard's login rate-limiter 429 to "wrong
   password" would lie to a parent, so risk's third sentinel is kept. Splitting
   Unauthorized from BadCredentials later is a one-line change if phase 02 wants it.

7. **Periodic re-probe.** risk + verification: `Run(ctx, 60s)`. mvp: startup-only,
   "re-probing is the reconciler's job in phase 04". Default: periodic. Why it
   matters: with a single probe, /healthz reports the boot-time state for the life of
   the process, which contradicts the locked healthz rationale (keep AdGuard state
   visible to anyone curling it). Cost is ~20 lines in t-5.

8. **Login and basic auth.** risk + verification: Login sends no Authorization
   header and the fake asserts its absence. mvp: "every fake handler asserts the
   Authorization header decodes to user:pass" — read literally that includes
   /control/login, which would make AdGuard authenticate the service, not the
   parent. Default: no basic auth on Login, asserted. Why it matters: it is the one
   deliberate exception to the locked adguard_auth decision and must be recorded as
   such, not discovered.

9. **Per-client blocked_services shape.** mvp: `blocked_services.ids` per client,
   taken from the decision text. risk + verification: v0.107.x per-client is a flat
   `blocked_services: []string` + `blocked_services_schedule`; `{ids, schedule}` is
   the global /control/blocked_services/get shape. Default: flat array; raw-preserving
   RMW means only the fixture and one key name change if nora disagrees. Why it
   matters: c-6's "only blocked_services.ids changes" and the locked decision's
   wording are both technically about the wrong endpoint (flag 1).

10. **`--listen` flag.** verification removes it (listen lives in config, env form
    ADGUARD_REWARD_LISTEN). mvp keeps it as an override of cfg.Listen. risk drops
    envOr but is silent on the flag. Default: remove. Why it matters: a third
    precedence layer no test justifies; but see flag 6 for the `make dev` consequence.

11. **deploy/config.example.yaml.** risk updates its header to document the env
    rule and *_file keys; mvp explicitly says "no change, the 'see docs/' comment
    stays until a docs phase exists". verification silent. Default: update the header
    in t-6 (one comment block, no key changes). Why it matters: `docs/` does not exist,
    so the current comment points at nothing; the fix is smaller than the disagreement.

12. **Health test style.** risk tests the prober against the fake with
    SetStatus/SetAuth/Hang; verification tests it against a stub `StatusClient`.
    Default: stub in t-5 (unit), fake in t-6 (integration). Why it matters: it
    decides whether the fake needs `Hang()` for health at all — kept anyway because
    t-3's deadline contract and t-4's concurrency contract use it.

## Flags for the user

1. [risk, verification] Spec decision `adguard_api_target` says "blocked_services:
   {ids, schedule}" and c-6 says "only blocked_services.ids changes" — in v0.107.x
   that object is the *global* GET/PUT /control/blocked_services shape; per-client
   is `blocked_services: [..]` + `blocked_services_schedule`. Plan follows the real
   shape; consider re-wording the decision/criterion so verify does not trip on it.
2. [risk] `deploy/Dockerfile` ends `CMD ["--config", "/data/config.yaml"]` — an
   explicit path, so under locked `config_path` an env-only container exits on boot.
   Not fixed here; belongs to 06-deploy-nora.
3. [risk] `-race` in `make test` needs cgo; `make build` and the Dockerfile use
   `CGO_ENABLED=0`. Test target must not inherit that; CI image for phase 06 needs
   gcc or a separate no-race target.
4. [mvp, risk] gopkg.in/yaml.v3 is archived (April 2025). Chosen anyway for zero
   deps + KnownFields; if the user prefers a maintained parser, swap to goccy/go-yaml
   in t-1 and drop the KnownFields typo contract.
5. [mvp, verification] Exact /control/login rejection status on nora (400 vs 403)
   must be confirmed during t-2/t-3; the fake defaults to 403, the client accepts
   400/401/403 so either answer passes. Fixtures should be checked against the
   pinned openapi.yaml tag or nora's live /control/clients before t-2 lands.
6. [judge — not raised by any draft] Removing `--listen` (disagreement 10) means
   `make dev` (`go run ./cmd/adguard-reward`, no args) resolves the default
   config.yaml beside go run's temp binary, finds nothing, falls to env-only and
   exits on missing adguard.url. Either `make dev` passes `--config config.yaml`
   or the user exports ADGUARD_REWARD_ADGUARD_* in their shell.
7. [verification] /healthz before the first probe reports adguard=="unreachable"
   (closed enum per the locked decision). Not observable because main probes before
   listening; noted so nobody adds an "unknown" state without revisiting the decision.
8. [risk] Basic-auth token base64("user:pass") is treated as a secret in every c-2
   contract, not just the password — worth a line in rules.toml r-03 if the user agrees.
