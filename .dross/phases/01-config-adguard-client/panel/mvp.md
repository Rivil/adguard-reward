# Planner draft — MVP lens

Bias: smallest task set that satisfies every criterion. Two layers exist in
this phase (config, AdGuard client) plus the wiring in `main`; that is the
whole decomposition. Nothing here exists to make a later phase easier.

```
Phase 01-config-adguard-client — 3 tasks across 2 waves

Wave 1
  t-1  Config loader with env, *_file, redaction
       files:       internal/config/config.go
                    internal/config/config_test.go
                    go.mod, go.sum
       description: Config struct mirroring PLAN.md keys (listen, base_url, tls.cert/key,
                    adguard.url/username/password/password_file, data_dir,
                    ai.provider/api_key/api_key_file/base_url/model, log_level).
                    Load(path string, explicit bool, getenv func(string) string)
                    reads YAML (github.com/goccy/go-yaml), applies ADGUARD_REWARD_*
                    overrides from a flat key table (dotted key -> env name by
                    uppercasing and joining with _), resolves password_file /
                    api_key_file into password / api_key, then validates
                    adguard.url, adguard.username, adguard.password. Config implements
                    slog.LogValuer so any slog of the struct emits [REDACTED] for
                    password and api_key.
       covers:      c-1, c-2 (redaction half; log capture test is in t-3)
       contract:    - missing adguard.password with url+username set: Load returns an
                      error whose message contains "adguard.password"; same test table
                      for adguard.url and adguard.username
                    - YAML password "yamlpw" + env ADGUARD_REWARD_ADGUARD_PASSWORD=envpw:
                      cfg.AdGuard.Password == "envpw" (env wins)
                    - ADGUARD_REWARD_ADGUARD_PASSWORD_FILE pointing at a temp file
                      containing "filepw\n": cfg.AdGuard.Password == "filepw" (trailing
                      newline trimmed); same for ADGUARD_REWARD_AI_API_KEY_FILE -> AI.APIKey
                    - explicit=false with a nonexistent path and full env: Load succeeds;
                      explicit=true with a nonexistent path: Load returns an error wrapping
                      fs.ErrNotExist
                    - slog.TextHandler at debug logging cfg via slog.Any("config", cfg):
                      output does not contain "yamlpw"/"sk-test" and does contain
                      "[REDACTED]"
       depends_on:  []
       status:      pending

  t-2  Typed AdGuard client against fake server
       files:       internal/adguard/client.go
                    internal/adguard/client_test.go
                    internal/adguard/testdata/clients.json
                    internal/adguard/testdata/blocked_services_all.json
       description: Client{BaseURL, Username, Password, HTTP *http.Client, Log *slog.Logger}
                    with basic auth on every request (locked: adguard_auth) and a
                    debug log line per request carrying method+path only.
                    Methods: Login(ctx, user, pass) error; Status(ctx) (Status, error)
                    for /control/status {version}; Clients(ctx) (Inventory, error) from
                    GET /control/clients + GET /control/blocked_services/get; Services(ctx)
                    ([]Service, error) from GET /control/blocked_services/all;
                    SetBlockedServices(ctx, clientName, ids) error which GETs
                    /control/clients, finds the client as a raw map[string]any, replaces
                    only ["blocked_services"]["ids"], and POSTs {name, data} to
                    /control/clients/update. Sentinel ErrBadCredentials for 401/403;
                    all other non-2xx wrap a *StatusError{Code, Body}; transport
                    failures wrap the net error. Types target v0.107.x only
                    (blocked_services: {ids, schedule}) — locked.
                    Fixtures are hand-written JSON in the v0.107.x shape (three
                    clients: one with use_global_blocked_services=true, one with
                    ids=["youtube","tiktok"], one with an extra unknown field
                    "future_field": true) — not copied AdGuard assets (r-01).
       covers:      c-3, c-4, c-5, c-6 (+ Status() needed by c-7)
       contract:    - fake /control/login answering 401: errors.Is(err, ErrBadCredentials)
                      is true; answering 503: errors.Is(err, ErrBadCredentials) is false
                      and errors.As(err, *StatusError) yields Code 503; client pointed at
                      a closed listener: err is neither ErrBadCredentials nor StatusError
                      and is non-nil
                    - Clients() against clients.json: 3 clients; client "kid-phone" has
                      IDs ["192.168.1.20","aa:bb:cc:dd:ee:ff"], BlockedServices.IDs
                      ["youtube","tiktok"], UseGlobalBlockedServices false; Inventory.
                      GlobalBlockedServices equals the ids from /control/blocked_services/get
                    - Services() against blocked_services_all.json: len == fixture count and
                      the entry with ID "youtube" has non-empty Name and Icon
                    - SetBlockedServices("kid-phone", ["tiktok"]) against a fake that serves
                      clients.json and records the POST body: body.name == "kid-phone";
                      body.data deep-equals the fixture client object with
                      blocked_services.ids replaced by ["tiktok"] — including
                      "future_field": true and blocked_services.schedule unchanged;
                      unknown clientName returns ErrClientNotFound and no POST is made
                    - every fake handler asserts the Authorization header decodes to
                      user:pass (basic auth on every call) — a handler seeing no header
                      fails the test
                    - the request-log test captures the client's logger at debug during
                      Login("user","s3cret") and asserts "s3cret" is absent
       depends_on:  []
       status:      pending

Wave 2 (depends t-1, t-2)
  t-3  Wire config, startup probe, JSON healthz
       files:       cmd/adguard-reward/main.go
                    cmd/adguard-reward/main_test.go
                    Makefile
       description: Refactor main into run(ctx, args []string, getenv, stderr io.Writer) int.
                    Flags: --config (default config.yaml next to os.Executable, explicit
                    when passed — locked config_path), --listen kept as an override of
                    cfg.Listen. Build slog with level from cfg.LogLevel; log the config via
                    slog.Any (redacted). Probe adguard.Status() once at startup: on
                    ErrBadCredentials log and return 1 (locked startup_probe); on other
                    errors log error and continue. Store the probe outcome in a
                    mutex-guarded struct; GET /healthz writes 200 application/json
                    {status:"ok", adguard:"ok"|"unreachable"|"unauthorized",
                    adguard_version, version}. `var version = "dev"` set by
                    -X main.version=$(VERSION) in Makefile LDFLAGS.
       covers:      c-7, c-2 (log-capture test), c-1 (--config / env-only wiring)
       contract:    - run() with a fake AdGuard whose /control/status returns 401 exits 1
                      and stderr contains "credentials"; with /control/status returning
                      503, run() keeps serving and GET /healthz returns 200 with
                      adguard:"unreachable"; with 200 {"version":"v0.107.52"} healthz
                      returns adguard:"ok" and adguard_version:"v0.107.52"
                    - healthz status code is 200 in all three cases above (locked healthz)
                    - run() with env ADGUARD_REWARD_ADGUARD_PASSWORD=s3cret-pw and
                      ADGUARD_REWARD_AI_API_KEY=sk-test-key, ADGUARD_REWARD_LOG_LEVEL=debug,
                      no config file: captured stderr contains "listening" and the
                      startup probe line, and contains neither "s3cret-pw" nor
                      "sk-test-key" (c-2)
                    - run() with --config /nonexistent exits 1 and stderr names the path;
                      run() with no --config and no config.yaml next to the binary but
                      full env starts (c-1 config_path)
                    - run() with env lacking ADGUARD_REWARD_ADGUARD_URL exits 1 and
                      stderr contains "adguard.url"
       depends_on:  [t-1, t-2]
       status:      pending
```

## Coverage

| criterion | tasks |
|---|---|
| c-1 | t-1 (load/env/*_file/validation), t-3 (--config default vs explicit, env-only start) |
| c-2 | t-1 (LogValuer redaction), t-2 (request log carries no credential), t-3 (end-to-end log capture at debug) |
| c-3 | t-2 |
| c-4 | t-2 |
| c-5 | t-2 |
| c-6 | t-2 |
| c-7 | t-2 (Status()), t-3 (probe + /healthz) |

All 7 criteria covered. No task exists that does not map to a criterion.

## Judgment calls

- **3 tasks, not 5–6.** Rejected splitting the AdGuard client into read (c-4/c-5) and write (c-6) tasks, and splitting healthz from the startup probe. The client methods share one transport helper and one fake server; the probe and healthz share one state struct. Splitting adds merge points without adding parallelism (t-1 and t-2 already run in parallel).
- **YAML parser: github.com/goccy/go-yaml (MIT), one dependency.** Rejected gopkg.in/yaml.v3 (repository archived April 2025) and a hand-rolled parser (the config has nested maps; not worth the bug surface). Rejected viper/koanf: reflection-based env binding is more than 15 keys need.
- **Env overrides via an explicit key table, not reflection.** 15 keys, one `map[string]*string`-style table in config.go. Testable by table, no struct tags, no surprises with the locked `ADGUARD_REWARD_ADGUARD_PASSWORD_FILE` naming.
- **SetBlockedServices operates on the raw JSON object, not the typed struct.** c-6 demands "every other field exactly as read". A typed struct silently drops fields it doesn't declare; a map[string]any round-trips them. The fixture carries an unknown field so the test proves it. Typed structs are used only for the read path (c-4).
- **ErrBadCredentials on 401 and 403 for all endpoints; /control/login's exact rejection status must be confirmed on nora during execution.** AdGuard Home returns 401 for bad basic auth; the fake uses 401. If a real instance answers /control/login with a different code, extend the mapping in t-2 — the test structure doesn't change.
- **Probe once at startup, no periodic re-probe.** c-7 says "reflecting the most recent probe"; the only probe in scope is the startup one. Re-probing on a timer is the reconciler's job in phase 04 and would be speculative here.
- **`version` is `var version = "dev"` plus a one-line Makefile `-X`.** Rejected a build-info/`debug.ReadBuildInfo` approach; the criterion only needs a field present.
- **No config.example.yaml change.** It already lists every key in PLAN.md; the env-name convention comment ("see docs/") stays until a docs phase exists.
- **Deferred items honoured.** tls.cert/key are parsed (they are in the struct) but no listener wiring; no tls_skip_verify key; no global→per-client migration — Clients() only reads use_global_blocked_services and the global list.
