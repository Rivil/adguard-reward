# Phase 01-config-adguard-client — verification-lens draft

Lens: every criterion was first written as the test that would catch its regression;
each task is the smallest change that makes those tests satisfiable. The fake AdGuard
(t-2) is a first-class deliverable, not an afterthought, because five of seven criteria
literally cannot be tested without a stateful, auth-enforcing, body-echoing fake.

New dependency: `gopkg.in/yaml.v3 v3.0.1` (Apache-2.0/MIT, zero transitive deps) for
config parsing. Nothing else is added.

```
Phase 01-config-adguard-client — 6 tasks across 4 waves

Wave 1
  t-1  Config package with Secret redaction
       files:       internal/config/config.go
                    internal/config/config_test.go
                    internal/config/testdata/full.yaml
                    go.mod
                    go.sum
       description: Add `config.Load(path string, explicit bool, lookupEnv func(string) (string, bool)) (*Config, error)`
                    parsing the PLAN.md key set via gopkg.in/yaml.v3, applying ADGUARD_REWARD_* overrides
                    (nested keys joined with `_`), resolving adguard.password_file / ai.api_key_file, then
                    validating adguard.url/username/password. Add `type Secret string` whose String,
                    GoString, LogValue, MarshalJSON and MarshalText all return "[redacted]" with
                    `Reveal()` as the only way out; `Config.SlogLevel()` maps log_level.
       covers:      c-1, c-2
       contract:    - a yaml with adguard.url+username+password loads; removing any one of the three makes
                      Load return an error whose text contains the dotted key ("adguard.password")
                    - ADGUARD_REWARD_ADGUARD_URL set to a different value than the yaml wins over the yaml;
                      a table test covers every leaf key (listen, base_url, tls.cert, tls.key, adguard.url,
                      adguard.username, adguard.password, adguard.password_file, data_dir, ai.provider,
                      ai.api_key, ai.api_key_file, ai.base_url, ai.model, log_level) so a key without an
                      env mapping fails the table
                    - ADGUARD_REWARD_ADGUARD_PASSWORD_FILE pointing at a temp file containing "s3cret\n"
                      yields Password.Reveal()=="s3cret" (trailing newline trimmed, nothing else);
                      a missing file errors naming "adguard.password_file"; same for ai.api_key_file
                    - password and password_file both set → error naming both keys (no silent precedence)
                    - explicit=false with a nonexistent path and env supplying the three required keys
                      loads successfully; explicit=true with a nonexistent path returns an error
                      containing the path
                    - fmt.Sprintf("%v"/"%+v"/"%#v", cfg), json.Marshal(cfg) and a slog TextHandler at
                      Debug logging cfg as an attr all produce output that does not contain the password
                      or api key but does contain "[redacted]"
                    - log_level "debug" → slog.LevelDebug; "bogus" → error naming "log_level"
       depends_on:  []
       status:      pending

  t-2  Fake AdGuard server and v0.107.x fixtures
       files:       internal/adguard/adguardtest/server.go
                    internal/adguard/adguardtest/server_test.go
                    internal/adguard/adguardtest/testdata/clients.json
                    internal/adguard/adguardtest/testdata/blocked_services_get.json
                    internal/adguard/adguardtest/testdata/blocked_services_all.json
                    internal/adguard/adguardtest/testdata/status.json
       description: Importable test package (not _test-only, so cmd/adguard-reward tests reuse it) wrapping
                    httptest.Server. Fixtures are go:embed'd and shaped per AdGuard Home v0.107.x
                    (per-client `blocked_services: [..]` + `blocked_services_schedule`, global
                    `/control/blocked_services/get` → `{ids, schedule}`, `/all` → `{blocked_services:
                    [{id,name,icon_svg,rules}]}`, `/status` → `{version,...}`); each fixture carries a
                    comment-equivalent `_shape` note naming the version it was checked against.
                    Enforces basic auth on every /control/* except /control/login (401 otherwise),
                    records every request (method, path, Authorization present?, raw body), is stateful
                    (POST /control/clients/update replaces the stored client object; GET reflects it),
                    and exposes knobs: StatusCode override for /control/status, LoginBehaviour,
                    UpdateDelay + MaxInFlightUpdates counter, MutateClient(name, fn), Close().
                    The four fixture files are data, not code; the task is one layer.
       covers:      c-3, c-4, c-5, c-6
       contract:    - a GET /control/clients without Authorization, or with the wrong password, returns
                      401 — so any client code that forgets basic auth fails every downstream test
                    - POST /control/login with the configured creds returns 200; with wrong creds 400;
                      it never requires an Authorization header
                    - POST /control/clients/update {name, data} followed by GET /control/clients returns
                      `data` verbatim (unknown keys included) — the echo the c-6 test depends on
                    - each fixture decodes and exposes the documented keys: clients[0].blocked_services
                      is a JSON array of strings, blocked_services_get has `ids` and `schedule`,
                      blocked_services_all.blocked_services[0] has id/name/icon_svg, status has `version`
                    - Requests() returns entries in arrival order with the raw body preserved
       depends_on:  []
       status:      pending

Wave 2 (depends t-2)
  t-3  AdGuard client core, Login, Status
       files:       internal/adguard/client.go
                    internal/adguard/errors.go
                    internal/adguard/client_test.go
       description: `adguard.New(baseURL, username, password string, opts ...Option) (*Client, error)` with
                    WithHTTPClient/WithLogger; unexported `do(ctx, method, path, in, out)` that sets basic
                    auth on every call, decodes JSON, and maps responses: 2xx ok; 401/403 →
                    `fmt.Errorf("%w: %w", ErrBadCredentials, &StatusError{...})`; other non-2xx →
                    `*StatusError{Method, Path, Code}`; transport failure → wrapped `*url.Error`.
                    Debug log per request carries method, path, status, duration only.
                    `Login(ctx, user, pass) error` POSTs /control/login {name,password} WITHOUT basic
                    auth (400/401/403 → ErrBadCredentials). `Status(ctx) (Status, error)` GETs
                    /control/status → Status{Version, Running, ProtectionEnabled}. Default http.Client
                    has a 10s timeout.
       covers:      c-3, c-7, c-2
       contract:    - fake LoginBehaviour=reject (400): errors.Is(err, ErrBadCredentials) is true
                    - fake login returns 502: errors.Is(ErrBadCredentials) is false and errors.As
                      *StatusError gives Code==502 with Path "/control/login"
                    - fake closed before Login: errors.Is(ErrBadCredentials) false, errors.As *url.Error true
                    - the recorded /control/login request has no Authorization header and its body
                      decodes to {"name":"u","password":"p"} exactly
                    - the recorded /control/status request carries "Basic "+base64(user:pass)
                    - fake StatusCode=401 on /control/status → Status returns ErrBadCredentials;
                      StatusCode=503 → *StatusError Code 503; fixture → Version=="v0.107.52" (fixture value)
                    - with WithLogger at Debug writing to a buffer, after Status() the buffer contains
                      "GET" and "/control/status" (positive control) and contains neither the password
                      nor base64(user:pass)
                    - New("://bad") returns an error; New with no WithHTTPClient has Timeout>0
       depends_on:  [t-2]
       status:      pending

Wave 3 (depends t-3)
  t-4  Clients, Services, SetBlockedServices read-modify-write
       files:       internal/adguard/clients.go
                    internal/adguard/services.go
                    internal/adguard/clients_test.go
                    internal/adguard/services_test.go
       description: `Clients(ctx) (ClientsResult, error)` = GET /control/clients + GET
                    /control/blocked_services/get → `[]PersistentClient{Name, IDs, BlockedServices,
                    UseGlobalBlockedServices}` plus `GlobalBlockedServices []string`; each
                    PersistentClient keeps the raw object as unexported `map[string]json.RawMessage`.
                    `Services(ctx) ([]Service{ID, Name, Icon}, error)` from
                    /control/blocked_services/all (icon = icon_svg). `SetBlockedServices(ctx, clientName,
                    ids)` under a client-wide mutex: fresh GET, find by exact name (else
                    ErrClientNotFound), copy raw, replace only "blocked_services" (empty ids → `[]`,
                    never null), POST /control/clients/update {name: clientName, data: raw}.
       covers:      c-4, c-5, c-6
       contract:    - against the fixture, Clients() returns the fixture's client count, and clients[0]
                      Name/IDs/BlockedServices/UseGlobalBlockedServices equal the fixture literals;
                      GlobalBlockedServices equals blocked_services_get.json `ids`
                    - fixture client carries `blocked_services_schedule`, `safe_search` objects,
                      `upstreams_cache_size: null` and an unrecognised `"future_field": 42`; after
                      SetBlockedServices("Kid phone", ["youtube","tiktok"]) the echoed update body's
                      `data`, with "blocked_services" deleted, deep-equals the original raw object with
                      "blocked_services" deleted, and `data.blocked_services` == ["youtube","tiktok"];
                      top-level `name` == "Kid phone" — any typed round-trip that drops or zeroes a field
                      fails this
                    - fake MutateClient changes the client's ids between Clients() and
                      SetBlockedServices(); the echoed body carries the mutated ids (proves a fresh read,
                      not a cached one)
                    - SetBlockedServices("nobody", ...) → errors.Is(ErrClientNotFound) and the fake
                      recorded zero /control/clients/update requests
                    - fake UpdateDelay=20ms, 5 goroutines calling SetBlockedServices concurrently →
                      fake MaxInFlightUpdates()==1 (mutex serialises the RMW)
                    - SetBlockedServices with ids=nil → body has `"blocked_services":[]`
                    - Services() against the 3-entry fixture returns 3 with Icon equal to icon_svg; with
                      the fake serving `{"blocked_services":[]}` returns an empty slice and nil error
                      (nothing baked into the binary — r-01)
                    - fake StatusCode-style 401 on /control/clients → Clients() returns ErrBadCredentials
       depends_on:  [t-3]
       status:      pending

  t-5  Health prober and /healthz handler
       files:       internal/health/health.go
                    internal/health/health_test.go
       description: `health.New(status StatusClient, appVersion string, log *slog.Logger) *Prober` where
                    StatusClient is `interface{ Status(ctx) (adguard.Status, error) }`. `Probe(ctx)
                    Result{State, Version, Err}` classifies: nil → "ok"; errors.Is(ErrBadCredentials) →
                    "unauthorized"; anything else → "unreachable"; stores the snapshot under a mutex and
                    logs one Info line (adguard_version attr) on success or one Error line on failure.
                    `Handler() http.HandlerFunc` always writes 200 application/json
                    {status:"ok", adguard, adguard_version, version}. `Run(ctx, interval)` re-probes on
                    a ticker until ctx is done.
       covers:      c-7
       contract:    - stub returning Status{Version:"v0.107.52"} then GET → 200, Content-Type
                      application/json, body decodes to exactly
                      {"status":"ok","adguard":"ok","adguard_version":"v0.107.52","version":"test-1"}
                    - stub returning an ErrBadCredentials-wrapped error → 200 and adguard=="unauthorized"
                      (status code stays 200: the locked healthz decision)
                    - stub returning *StatusError{Code:503} or *url.Error → 200 and adguard=="unreachable",
                      adguard_version==""
                    - probe fail then succeed → "ok"; succeed then fail → "unreachable" (most recent wins)
                    - Handler before any Probe → 200 with adguard=="unreachable" (closed enum, never 5xx)
                    - Run(ctx, 5ms) with a counting stub: after 50ms the stub was called ≥2 times;
                      cancelling ctx returns from Run
                    - Info log on success contains "v0.107.52"; Error log on failure contains the err text
       depends_on:  [t-3]
       status:      pending

Wave 4 (depends t-1, t-3, t-5)
  t-6  Wire main: --config, startup probe, healthz JSON
       files:       cmd/adguard-reward/main.go
                    cmd/adguard-reward/main_test.go
                    Makefile
       description: Replace the `--listen` flag and envOr with `--config` (default: config.yaml beside
                    os.Executable(); explicit-vs-default detected via flag.Visit). Extract
                    `run(ctx, args []string, lookupEnv func(string) (string, bool), stderr io.Writer,
                    onListen func(net.Addr)) int`; main() calls os.Exit(run(...)). Sequence: load config →
                    slog handler at cfg.SlogLevel() on stderr → adguard.New → health.New → Probe:
                    ErrBadCredentials → Error log "adguard rejected the service credential
                    (adguard.username / adguard.password)" and return 1; any other error → Error log and
                    continue; success → Info with adguard_version. Mount GET /healthz →
                    prober.Handler(); start prober.Run(ctx, 60s); net.Listen(cfg.Listen) then Serve with
                    the existing graceful shutdown. Add `var version = "dev"`; Makefile gains
                    `VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)`
                    and `-X main.version=$(VERSION)` in LDFLAGS. Delete TestEnvOr with envOr.
       covers:      c-1, c-2, c-7
       contract:    - run with --config pointing at a temp yaml lacking adguard.password → returns 1 and
                      stderr contains "adguard.password"
                    - run with --config /nonexistent/config.yaml → returns 1 and stderr contains that
                      path; run with no --config, no config.yaml beside the test binary, and the three
                      ADGUARD_REWARD_ADGUARD_* env vars set → proceeds to the probe (fake records
                      /control/status)
                    - fake StatusCode=401 on /control/status → run returns 1 and stderr contains
                      "rejected" and "adguard.username" (locked startup_probe decision, half one)
                    - fake StatusCode=503 → onListen fires; GET http://<addr>/healthz returns 200 with
                      adguard=="unreachable"; cancelling ctx makes run return 0 (locked startup_probe
                      decision, half two)
                    - fake healthy → /healthz body has adguard=="ok", adguard_version=="v0.107.52" and
                      version=="test-1" (test sets the package var); Content-Type application/json
                    - c-2 end-to-end: yaml with password "pw-CANARY-9f3a" and ai.api_key
                      "sk-CANARY-77b1", ADGUARD_REWARD_LOG_LEVEL=debug, stderr captured through startup
                      and one /healthz request: stderr contains "listening" and "/control/status"
                      (positive control proving debug capture is live) and contains none of
                      "pw-CANARY-9f3a", "sk-CANARY-77b1", base64("svc:pw-CANARY-9f3a")
                    - `make build` produces a binary; `./bin/adguard-reward --config missing.yaml`
                      exits 1 (manual gate, recorded in the verify run, not a go test)
       depends_on:  [t-1, t-3, t-5]
       status:      pending
```

## Coverage

| criterion | tasks | the test that catches the regression |
|---|---|---|
| c-1 | t-1, t-6 | t-1 missing-key error names the dotted key; env table over every leaf; *_file indirection; explicit-vs-default path. t-6 run() returns 1 with the key in stderr |
| c-2 | t-1, t-3, t-6 | t-1 Secret redaction under fmt/json/slog; t-3 per-request debug log lacks password and basic token; t-6 whole-startup debug capture with positive control |
| c-3 | t-2, t-3 | t-3 400→ErrBadCredentials, 502→StatusError, closed server→url.Error; login carries no basic auth |
| c-4 | t-2, t-4 | t-4 fixture-typed Clients() incl. global ids; fixtures are the v0.107.x shape (t-2 asserts the keys) |
| c-5 | t-2, t-4 | t-4 Services() maps id/name/icon_svg; empty catalogue → empty result |
| c-6 | t-2, t-4 | t-4 echoed `data` deep-equals original minus blocked_services, incl. unknown field and null; fresh-read; serialised |
| c-7 | t-3, t-5, t-6 | t-3 Status() typed; t-5 classification + always-200 JSON + last-probe-wins; t-6 401 exits 1, 503 keeps serving, healthz reflects probe |

All 7 criteria covered. Locked decisions honoured: startup_probe (t-5/t-6 contracts split into its two halves), healthz (always-200 contract in t-5 and t-6), env_naming (t-1 table uses the `_`-joined names, incl. ADGUARD_REWARD_ADGUARD_PASSWORD_FILE), config_path (t-1 explicit flag + t-6 default-beside-binary), adguard_auth (t-2 fake enforces basic auth on all /control/* so every test asserts it), adguard_api_target (t-2 fixtures, no legacy list endpoint).

## Judgment calls

- **Fake AdGuard is an importable package (`internal/adguard/adguardtest`) with embedded fixtures, not per-test httptest closures.** Rejected inline fakes: c-6's echo, c-3's auth-free login, and c-2's end-to-end capture in package main all need the same stateful, auth-enforcing server; duplicating it in two packages is where the fakes would drift from each other.
- **RMW preserves the raw client object (`map[string]json.RawMessage`) and replaces only `blocked_services`.** Rejected a full typed mirror of AdGuard's clientJSON: it silently drops fields added in later 0.107.x releases and turns `null` pointers into zero values, which is exactly the c-6 violation. The `"future_field": 42` + `upstreams_cache_size: null` contract makes a typed round-trip fail.
- **Per-client wire shape is a flat `blocked_services: [...]` array, not `{ids, schedule}`.** In AdGuard Home v0.107.x the `{ids, schedule}` object is the *global* `GET/PUT /control/blocked_services/get|update` shape; per-client is `blocked_services` (array) plus `blocked_services_schedule`. The locked decision is applied as written — current shape only, no legacy `/control/blocked_services/list` — but the executor must confirm the fixtures against the pinned `openapi/openapi.yaml` tag or nora's live `/control/clients` before t-2 lands, and note the checked version in each fixture. If nora turns out to serve `blocked_services: {ids, schedule}` per client, t-4's raw-preserving design still works; only the fixture and the one key replaced change.
- **One sentinel `ErrBadCredentials` for both Login rejection (400/401/403 on /control/login) and service-credential rejection (401/403 elsewhere).** Rejected a second `ErrUnauthorized`: both consumers (healthz classification, startup exit) need only "credentials rejected", and one sentinel keeps errors.Is checks uniform.
- **Login sends no basic auth.** Rejected routing Login through the authenticated `do()`: AdGuard's optional-auth middleware would see a valid service header, and a wrong submitted password could be masked. The fake records whether Authorization was present so this is asserted, not assumed.
- **Periodic re-probe (`Run`, 60 s) in the health package.** Spec says healthz reflects "the most recent probe" and the locked decision wants the app to outlive a flaky AdGuard; a startup-only probe leaves healthz stuck on "unreachable" after AdGuard restarts, which contradicts the reason for the decision. Rejected startup-only.
- **`gopkg.in/yaml.v3`** over goccy/go-yaml or a hand-rolled parser: smallest, most-audited, no transitive deps; the config surface is 15 scalar leaves.
- **`password` and `password_file` both set is an error**, not a precedence rule. Rejected "file wins": a leftover literal password in yaml plus a container secret file would then silently ignore one of them.
- **`--listen` flag removed**; `listen` lives in config with ADGUARD_REWARD_LISTEN as its env form (the precedent the env_naming decision cites). Rejected keeping the flag: a third precedence layer nobody asked for and no test could justify.
- **`run()` seam with injectable env lookup, stderr and onListen** instead of `os/exec` re-exec tests. Rejected subprocess tests: debug-level log capture and the 503-keeps-serving path need a deterministic in-process handle on the listener and the log stream.
- **Healthz before the first probe reports `unreachable`**, keeping the enum closed as the locked decision specifies. Not observable in practice because main probes before listening; noted so nobody adds an "unknown" state later without revisiting the decision.
- **Every "secret absent" test has a positive control** (buffer contains "GET"/"listening"): an absence assertion over an empty buffer is vacuous, and that is the most likely way c-2 would go false-green.
- **`*_file` contents are trimmed of trailing `\r\n` only.** Rejected TrimSpace: a password with a leading or trailing space is legal and must round-trip.
