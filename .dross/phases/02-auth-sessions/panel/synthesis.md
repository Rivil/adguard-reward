# Synthesis — 02-auth-sessions

Judge read: spec.toml, project.toml, rules.toml, the three drafts, and the repo
(cmd/adguard-reward/main.go + main_test.go, internal/config/config.go + config_test.go,
internal/adguard/errors.go + adguardtest/server.go, Makefile, go.mod, web/package.json,
web/vite.config.ts, web/tsconfig.app.json, deploy/*, phase-01 plan.toml for task shape).

Repo facts that corrected the drafts:
- `probeInterval` in main.go is a **const**, not a package var. Risk t-11 and verification t-9
  both say "package var mirroring probeInterval" — the sweep interval must be introduced as a
  `var` (there is no existing pattern to mirror).
- `TestEnvTable` sets every env var to `v:<key>` and loads; `v:trusted_proxies` fails CIDR
  validation. MVP and verification handle this (override to ""); risk does not.
- `writeConfig` in main_test.go does not set `data_dir`, so once main opens the store every
  existing `TestRun_*` would write `./data` beside the test binary. MVP and verification add
  `data_dir: <t.TempDir()>` to the helper; risk only does it for the restart test.
- `adguardtest.Options` accepts exactly one User/Pass. Risk inserts the second identity through
  the store; verification builds a second fake (needs a second handler set — clunkier).
- docker-compose.yml already mounts `./data:/data`; verification's compose note is moot.
- Node is v24.14 → mvp's `node --test` with type stripping would run; vitest is not the only option.
- Phase-01 plan tasks carry 3–6 files and 6–12 contract bullets each; risk's granularity
  matches that convention best.

## Scores

| Dimension | risk (11 tasks / 5 waves) | mvp (7 tasks / 4 waves) | verification (9 tasks / 4 waves) |
|---|---|---|---|
| Criteria coverage | Full; every criterion has a unit-level owner and an integration-level re-assertion (c-4: t-2 maths, t-3 IP, t-9 handler, t-11 wire). | Full, but c-6/c-9/c-10 lean on one fat task each (t-5 six criteria, t-6 eight); no sweeper so a lost phone's row lives forever. | Full; c-8 credited to t-3 is a stretch; c-7 rests on source-text greps of .svelte plus a recorded manual gate. |
| Test-contract specificity | Highest: every bullet names the test, exact values, positive controls; unique bullets no one else has (TestLogin_Burst, TestMigrate_Rollback, TestPrune, TestCSRF_BeforeHandler with a panicking stub). Misses the `v:` env-table override. | High; strong end-to-end bullets (TestLogin_Escalation through HTTP with Retry-After 60/120/240/60, TestRun_TrustedProxies asserting `ip=` in log lines, TestRun_TLS with a generated cert). | Very high at the edges: TestMigrate_Order (FS stub), TestLimiter_Reset pinning "measured from last trip" at L+59m59s, TestLogin_ProxiedIP at the handler layer, TestRouter_Fallthrough (401 before 404, /healthz not under Router), TestExampleConfigLoads. Weakest on c-7 (greps). |
| Granularity | Finest and closest to the phase-01 convention; t-9 is still large (7 criteria, 11 bullets) but is the natural integration point. t-5 is a thin skeleton task. | Coarsest: t-5/t-6 carry 14 criterion-claims between them; t-4 touches 7 files across three concerns (fetch layer, pages, test wiring). | Middle: t-1 folds store + sessions (8 files), t-4 nine files, t-7 eight criteria / 12 bullets. |
| Wave correctness | Dependencies correct; wave 5 is unnecessary — main wiring never touches internal/api and can run beside the sessions API (verification spotted this). | Correct and minimal critical path (4); frontend in wave 1 is right because its api.ts keys on HTTP status, not the body. | Best: t-8 and t-9 parallel in wave 4 with the explicit "shared router.go is a merge hazard" reasoning; Svelte pages off the critical path in wave 2. |

**Skeleton: risk.** It has the sharpest contracts, one owner per failure mode, and the task size the
phase-01 plan already uses. Its two structural weaknesses — the spare fifth wave and the missed
env-table/data_dir test plumbing — are fixed by grafting from verification and mvp. Its one
schema choice that both other lenses reject (random hex session id) is overridden (D3).

## Merged plan

Tags: [risk] / [mvp] / [verification] name the draft(s) a task or bullet comes from. Skeleton
bullets are copied verbatim from risk; grafted bullets are listed under their own heading so
each stays verbatim from its source (adjusted only where D3's integer id or a renamed file
forces it — marked "(adjusted)").

```
Phase 02-auth-sessions — 11 tasks across 4 waves

Wave 1
  t-1  Found internal/store: open, pragmas, migrations                        [risk+verification]
       files:    internal/store/store.go, internal/store/migrate.go,
                 internal/store/migrations/0001_sessions.sql,
                 internal/store/store_test.go, go.mod, go.sum
       covers:   c-3, c-9   (session_storage locked decision)
       depends:  —
       desc:     Add modernc.org/sqlite. store.Open(dataDir string, log *slog.Logger) (*Store, error):
                 MkdirAll(dataDir, 0o700), opens <dataDir>/adguard-reward.db with
                 _pragma=journal_mode(WAL), busy_timeout(5000), foreign_keys(ON), synchronous(NORMAL);
                 db.SetMaxOpenConns(1) so modernc never sees two writers. Migrations are go:embed'd
                 *.sql files applied in name order, each in its own transaction, tracked in
                 schema_migrations(name TEXT PRIMARY KEY, applied_at INTEGER); a failing statement
                 rolls back that migration and Open returns an error naming the file.
                 0001_sessions.sql: sessions(id INTEGER PRIMARY KEY, username TEXT NOT NULL,
                 token_hash BLOB NOT NULL UNIQUE, created_at INTEGER NOT NULL, last_seen_at INTEGER
                 NOT NULL, expires_at INTEGER NOT NULL) + index on (username), index on (expires_at).
                 (id is the SQLite rowid per D3 — not a random hex string.) Store exposes DB() *sql.DB
                 for the query files, Close(), and Migrations() []string. Errors from Open name
                 data_dir so the operator sees the config key. The migration runner takes its fs.FS
                 as a parameter (default: the embedded FS) so TestMigrate_Order can inject a stub.
       contract: - if MaxOpenConns/busy_timeout are dropped, TestOpen_ConcurrentWriters fails: 20
                   goroutines each doing 20 INSERTs into a scratch table under go test -race finish
                   with zero errors (no SQLITE_BUSY / "database is locked")
                 - if migrations are re-run on reopen, TestOpen_Idempotent fails: Open, Close, Open on
                   the same dir succeeds and schema_migrations has exactly one row per embedded file
                 - if a failing migration leaves partial state, TestMigrate_Rollback fails: an injected
                   migration whose second statement is invalid SQL makes Open error naming that file,
                   and the table its first statement created does not exist afterwards
                 - if the schema drifts, TestSchema_Sessions fails: PRAGMA table_info(sessions)
                   lists exactly id, username, token_hash, created_at, last_seen_at, expires_at with
                   the stated types and NOT NULL flags, id being INTEGER PRIMARY KEY; PRAGMA
                   index_list shows token_hash UNIQUE   (adjusted per D3)
                 - if data_dir handling breaks, TestOpen_DataDir fails: a nonexistent nested dir is
                   created with mode 0700; a data_dir that is a regular file errors containing
                   "data_dir"
                 - if the WAL pragma is lost, TestOpen_Pragmas fails: PRAGMA journal_mode returns
                   "wal" and PRAGMA foreign_keys returns 1
       contract (grafted from verification):
                 - if a migration is skipped or applied out of order, TestMigrate_Order fails:
                   an embedded-FS stub with 0002 before 0001 in insertion order still lands 0001
                   first (schema_migrations rows are 0001, 0002 in applied order)   (adjusted: rows
                   are file names, not integers)

  t-2  Login rate limiter with escalating lockout                             [risk+verification]
       files:    internal/ratelimit/limiter.go, internal/ratelimit/limiter_test.go
       covers:   c-4, c-11   (rate_limit_thresholds locked decision)
       depends:  —
       desc:     ratelimit.New(Config{PerIPFailures: 5, Window: 1m, BaseLockout: 1m, MaxLockout: 1h,
                 EscalationReset: 1h, GlobalFailures: 20, Now func() time.Time}) *Limiter.
                 Check(ip) (allowed bool, retryAfter time.Duration) and Fail(ip) under one sync.Mutex.
                 Per IP: failures counted in a fixed 1-minute window; recording the 5th failure trips
                 the IP: lockedUntil = now + BaseLockout << (trips-1) capped at MaxLockout, trips++,
                 lastTrip = now; the failure counter clears when the lockout ends. On Check/Fail, an IP
                 whose lastTrip is ≥ EscalationReset ago has trips reset to 0 (reset is measured from
                 the last trip, not from lockout expiry). Global: one fixed 1-minute window; the 20th
                 failure denies every IP until the window ends, never escalates (see D14). retryAfter
                 is ceil'd to whole seconds and ≥ 1s whenever allowed==false. Prune() (also run lazily
                 inside Fail) drops IPs with no active lockout, no failures in the current window and
                 lastTrip older than EscalationReset. Len() for tests. There is no Success or Rejected
                 method: only Fail consumes budget, so successes and 429s cannot count by
                 construction. No logging, no net/http — pure policy with an injected clock.
       contract: - if the trip threshold is off by one, TestPerIP_Threshold fails: 5 Fail() calls
                   within 1m leave Check false (retryAfter 60s); 4 Fail() calls leave Check true
                 - if doubling or the cap breaks, TestPerIP_Doubling fails: with a fake clock, tripping
                   the same IP repeatedly immediately after each unlock yields lockouts of exactly
                   1m, 2m, 4m, 8m, 16m, 32m, 1h, 1h (capped)
                 - if escalation never resets, TestPerIP_Reset fails: after a 4m trip, advancing 1h
                   past the trip with no failures and tripping again yields a 1m lockout, not 8m
                 - if Retry-After does not track the current window, TestPerIP_RetryAfter fails:
                   during a 4m lockout Check at +90s returns retryAfter == 150s; at +239.2s returns
                   1s (ceil), never 0
                 - if the global limit escalates or is miscounted, TestGlobal_Flat fails: 20 failures
                   spread over 20 distinct IPs deny a 21st fresh IP with retryAfter == remaining
                   window; after the window, 20 more failures deny again with a 1m-bounded
                   retryAfter (no doubling)
                 - if the failure counter survives a lockout, TestPerIP_ClearOnUnlock fails: after a
                   lockout ends, a single Fail() does not re-lock (needs 5 fresh failures)
                 - if pruning breaks, TestPrune fails: 100 IPs tripped once, clock advanced 1h + 1m,
                   Prune() leaves Len() == 0; an IP still inside a 1h lockout survives Prune()
                 - if the mutex is removed, TestLimiter_Race fails under go test -race: 50
                   goroutines interleaving Check/Fail on 5 IPs
       contract (grafted from verification):
                 - if the reset is missing or too eager, TestLimiter_Reset fails (reset is measured
                   from the last trip, not from lockout expiry): after trip #3 (4m lockout)
                   at time L, advance to L+59m59s and trip again -> 8m (no reset); in a fresh
                   limiter reach a 4m lockout at L, advance to L+1h and trip -> 1m (reset)
                 - if rate-limited or successful attempts consume budget, TestLimiter_BudgetIsFailuresOnly
                   fails: 100 Check calls with no RecordFailure never block; 4 failures then 50
                   Check calls still allowed (compile-level: no Success/Rejected method exists)
                   (adjusted: RecordFailure reads Fail in this package)

  t-3  trusted_proxies config and client IP resolver              [risk+mvp+verification]
       files:    internal/config/config.go, internal/config/config_test.go,
                 internal/config/testdata/full.yaml, deploy/config.example.yaml,
                 internal/api/clientip.go, internal/api/clientip_test.go
       covers:   c-4   (client_ip_source locked decision)
       depends:  —
       desc:     Config gains TrustedProxies []string `yaml:"trusted_proxies"`; env row
                 ADGUARD_REWARD_TRUSTED_PROXIES splits on commas (trimmed, empties dropped; set-but-
                 empty → empty list). validate() parses every entry with net.ParseCIDR (a bare IP is
                 accepted as /32 or /128) and errors naming trusted_proxies and the bad entry.
                 Config.TrustedProxyNets() []*net.IPNet. TestEnvTable's set-every-var round-trip
                 (which sets each var to "v:<key>") gains an override of
                 ADGUARD_REWARD_TRUSTED_PROXIES to "" in its mergeMaps block, since "v:trusted_proxies"
                 would fail CIDR validation. api.ClientIP(trusted []*net.IPNet)
                 func(*http.Request) netip.Addr: peer = host of r.RemoteAddr (port stripped, IPv6
                 brackets removed, zone dropped, 4-in-6 mapped addresses unmapped). With an empty list
                 or a peer outside every net, X-Forwarded-For is ignored. Otherwise the XFF hops
                 (all header values concatenated in order) are walked right to left; the first hop
                 not inside a trusted net is the client; if every hop is trusted or a hop fails to
                 parse, fall back to the peer. Unparseable RemoteAddr → netip.Addr{} and the caller
                 keys on "invalid" (no panic). config.example.yaml gains `trusted_proxies: []` with a
                 one-line comment on X-Forwarded-For; full.yaml gains the key.
       contract: - if the reflection env-table test is bypassed, TestEnvTable fails: the want map
                   gains trusted_proxies → ADGUARD_REWARD_TRUSTED_PROXIES and walkLeaves reports the
                   new []string leaf
                 - if list parsing breaks, TestLoad_TrustedProxies fails: yaml list [10.0.0.0/8,
                   ::1] loads both; env "10.0.0.1, 192.168.0.0/16" yields [10.0.0.1/32,
                   192.168.0.0/16]; env set to "" yields an empty list overriding yaml
                 - if validation stops naming the key, TestLoad_TrustedProxiesInvalid fails: entry
                   "10.0.0.0/33" and entry "proxy" each error containing trusted_proxies and the entry
                 - if XFF is honoured without opt-in, TestClientIP_Spoof fails: no trusted nets,
                   RemoteAddr 203.0.113.9:4444, XFF "1.2.3.4" → 203.0.113.9
                 - if hop selection breaks, TestClientIP_Proxied fails: trusted [10.0.0.0/8],
                   RemoteAddr 10.0.0.5:1, XFF "1.2.3.4, 5.6.7.8, 10.0.0.7" → 5.6.7.8; XFF
                   "10.0.0.7" (all trusted) → 10.0.0.5; XFF "garbage, 10.0.0.7" → 10.0.0.5;
                   untrusted peer 198.51.100.2 with the same XFF → 198.51.100.2
                 - if IPv6 handling breaks, TestClientIP_V6 fails: RemoteAddr "[2001:db8::1]:5"
                   → 2001:db8::1; "[fe80::1%eth0]:5" → fe80::1
                 - if a bad RemoteAddr panics, TestClientIP_Malformed fails: RemoteAddr "pipe"
                   returns an invalid Addr and no panic
       contract (grafted from verification):
                 - if IPv6 handling breaks, TestFrom_V6 fails: RemoteAddr "[::ffff:10.0.0.2]:80"
                   matches trusted 10.0.0.0/8; RemoteAddr "[2001:db8::1]:80" untrusted returns
                   2001:db8::1; RemoteAddr "nonsense" returns a zero Addr with no panic
                   (adjusted: fold the 4-in-6 case into TestClientIP_V6 rather than a second test)
                 - if the example config drifts from the loader, TestExampleConfigLoads fails:
                   config.Load("deploy/config.example.yaml") with only the three required
                   ADGUARD_REWARD_ADGUARD_* env vars set succeeds and TrustedProxyPrefixes()
                   is empty   (adjusted: lives in config_test.go, path ../../deploy/config.example.yaml,
                   accessor is TrustedProxyNets())
       contract (grafted from mvp):
                 - if two XFF header lines are mishandled, TestClientIP_Trusted's case "two XFF header
                   values "1.2.3.4" and "5.6.7.8" → 5.6.7.8" fails   (adjusted: added as a case of
                   TestClientIP_Proxied)

  t-4  Session tokens, cookie policy, RequireSession middleware              [risk+verification]
       files:    internal/auth/token.go, internal/auth/auth.go, internal/auth/middleware.go,
                 internal/auth/auth_test.go
       covers:   c-2, c-3, c-6   (session_lifetime locked decision)
       depends:  —
       desc:     auth.SessionStore interface: Insert(ctx, Session) (id int64, err error);
                 ByTokenHash(ctx, [32]byte) (Session, error) returning ErrNotFound; Touch(ctx, id
                 int64, lastSeen, expires time.Time) error; Delete(ctx, id int64) error; ListByUser(ctx,
                 user) ([]Session, error); DeleteByUser(ctx, user, id int64) (bool, error);
                 DeleteOthers(ctx, user, keepID int64) (int, error); DeleteExpired(ctx, now) (int,
                 error). Session{ID int64, Username, TokenHash, CreatedAt, LastSeenAt, ExpiresAt}.
                 auth.New(store, Options{Secure bool, Lifetime: 30*24h, Now func() time.Time, Log})
                 *Manager. Issue(ctx, username) (rawToken string, Session, error): 32 bytes crypto/rand
                 → base64.RawURLEncoding (43 chars); ID is assigned by the store (rowid); only
                 sha256(raw) is stored. Authenticate(ctx, raw) (Session, error): decode → hash →
                 ByTokenHash; ExpiresAt ≤ now → Delete + ErrNoSession; otherwise Touch(now,
                 now+Lifetime) and return the refreshed session (sliding, on every request per the
                 locked decision, no coalescing). Cookie(raw) *http.Cookie: Name
                 "adguard_reward_session", HttpOnly, SameSite=Strict, Path=/, Secure=Options.Secure,
                 MaxAge = Lifetime seconds. ClearCookie() same attrs with MaxAge -1. RequireSession
                 (next) http.Handler: missing/invalid cookie → 401 via the injected errorWriter
                 (func(w, status, code, msg)) so the JSON envelope stays in internal/api, plus
                 ClearCookie() when a cookie was present; success re-issues Cookie(raw) with a fresh
                 MaxAge (so the browser's expiry slides with the server's — D8) and puts the Session
                 in ctx (auth.FromContext). RunSweeper(ctx, interval) calls DeleteExpired on a ticker.
                 The raw token is never passed to slog; Session has no field holding it and
                 Session.LogValue() emits only id and username.
       contract: - if token entropy or the hash-only rule breaks, TestIssue_Token fails: two Issue()
                   calls return distinct 43-char base64url tokens; the fake store's inserted
                   TokenHash equals sha256(raw) and no stored field equals raw
                 - if cookie attributes drift, TestCookie_Attrs fails: Secure=false gives HttpOnly,
                   SameSite=Strict, Path=/, no Secure; Secure=true adds Secure; ClearCookie has
                   MaxAge -1 and identical Name/Path
                 - if sliding expiry breaks, TestAuthenticate_Sliding fails: Issue at t0,
                   Authenticate at t0+29d → ok and the fake store's Touch recorded expires ==
                   t0+29d+30d; Authenticate at t0+59d+1h → ok; then a 31-day gap → ErrNoSession and
                   the fake store recorded Delete(id)
                 - if expiry is not enforced, TestAuthenticate_Expired fails: Issue at t0,
                   Authenticate at t0+30d+1s → ErrNoSession
                 - if a revoked session is still accepted, TestAuthenticate_Revoked fails: Issue,
                   store.Delete(id), Authenticate → ErrNoSession
                 - if a tampered token is accepted, TestAuthenticate_Garbage fails: "", "notbase64!",
                   a valid-looking token from another Manager, and raw with one flipped character
                   each yield ErrNoSession with no Touch call
                 - if the middleware leaks through, TestRequireSession fails: no cookie → 401 and
                   next never runs; valid cookie → next runs with auth.FromContext(r.Context()).Username
                   == the issued username; expired → 401
                 - if the token reaches the logs, TestNoTokenInLogs fails: a Debug-level JSON slog
                   handler into a buffer across Issue, Authenticate, RequireSession and a
                   slog.Info("s", "session", sess) contains the session id (positive control) and
                   never the raw token or its base64url/hex hash
                 - if the sweeper stops running, TestRunSweeper fails: RunSweeper(ctx, 5ms) records
                   ≥ 2 DeleteExpired calls on the fake store within 100ms and returns on cancel
       contract (grafted from verification):
                 - if unauthenticated requests slip through, TestRequireSession_401 fails: a
                   spy next handler behind RequireSession sees zero calls for (no cookie),
                   (cookie with random value), (cookie from a session deleted via Revoke), and
                   all three get 401 application/json; the deleted-session response carries a
                   Set-Cookie clearing the cookie; a valid cookie reaches next once and
                   SessionFrom(r.Context()) inside next returns the issued username, and the
                   response carries a refreshed Set-Cookie with Max-Age=2592000
                   (adjusted: "deleted via Revoke" reads "deleted via store.Delete"; SessionFrom reads
                   auth.FromContext; fold into TestRequireSession)

  t-5  API router skeleton, CSRF header, no-store, error envelope                     [risk]
       files:    internal/api/api.go, internal/api/middleware.go, internal/api/errors.go,
                 internal/api/middleware_test.go
       covers:   c-5, c-10
       depends:  —
       desc:     api.New(Deps) *API with Handler() http.Handler; Deps declared now (Store, Auth,
                 Limiter, AdGuard interface{ Login(ctx, u, p) error }, ClientIP func, Log, Secure)
                 with fields filled in by later tasks. Handler() = noStore(csrf(mux)) mounted under
                 /api/v1/ so the chain wraps 404s and 405s too. noStore sets Cache-Control: no-store
                 before calling next (header set first, so every path incl. errors carries it).
                 csrf: methods other than GET/HEAD/OPTIONS require X-Requested-With == "adguard-reward"
                 (exact, case-sensitive value) else 403 forbidden before any route matches. errors.go:
                 writeError(w, status, code, message) emits application/json {"error": code,
                 "message": message}; codes: bad_request, unauthorized, forbidden, bad_credentials,
                 adguard_unavailable, rate_limited, not_found. A wrapper for 401 satisfies
                 auth.RequireSession's errorWriter. No business routes yet — the test mounts a stub
                 route via API.mux for ordering assertions. (The "/api/v1/" catch-all → RequireSession
                 (NotFound) lands in t-9 where Deps.Auth is populated — D12.)
       contract: - if the CSRF check misses a method, TestCSRF_Methods fails: POST, PUT, PATCH,
                   DELETE to a stub /api/v1/x without the header → 403 and the stub never runs; GET,
                   HEAD, OPTIONS without the header reach the stub; POST with the header reaches it
                 - if the header value is loosely matched, TestCSRF_Value fails: "AdGuard-Reward",
                   "adguard-reward " and "XMLHttpRequest" are all 403
                 - if the check runs after the route, TestCSRF_BeforeHandler fails: POST to an
                   unmounted /api/v1/nope without the header is 403 (not 404); a stub that panics is
                   never reached
                 - if no-store is set inside handlers instead of the outer layer, TestNoStore fails:
                   the 403 from csrf, a 404 for an unknown route, a 200 stub and a stub that writes
                   Cache-Control: max-age=60 before the outer layer all end with Cache-Control:
                   no-store
                 - if the envelope drifts, TestWriteError fails: writeError(w, 401, "unauthorized",
                   "m") yields status 401, Content-Type application/json and body decoding to
                   exactly {error: unauthorized, message: m}

Wave 2 (depends t-1, t-4, t-5)
  t-6  Sessions queries implementing auth.SessionStore                                [risk]
       files:    internal/store/sessions.go, internal/store/sessions_test.go
       covers:   c-3, c-9   (session_storage locked decision)
       depends:  t-1, t-4
       desc:     *store.Store implements every auth.SessionStore method with parameterised SQL over
                 the 0001 table; times stored as Unix seconds UTC; Insert returns LastInsertId as the
                 session id. ByTokenHash returns auth.ErrNotFound (not sql.ErrNoRows) on miss.
                 DeleteByUser deletes only when both username and id match, returning whether a row
                 went. DeleteOthers deletes the user's rows except keepID and returns the count.
                 ListByUser orders by created_at ASC then id. DeleteExpired removes expires_at ≤ now.
                 A compile-time `var _ auth.SessionStore = (*Store)(nil)` pins the contract.
       contract: - if the compile-time assertion is dropped, the package no longer proves the
                   interface: sessions_test.go references auth.SessionStore via the var line so a
                   signature drift fails `go vet ./...`
                 - if the unique constraint is lost, TestSessions_UniqueHash fails: two Inserts with
                   the same TokenHash — the second errors and ListByUser shows one row
                 - if cross-user revocation is possible, TestSessions_DeleteByUser fails:
                   DeleteByUser("bob", aliceID) returns false and alice's row remains;
                   DeleteByUser("alice", aliceID) returns true
                 - if revoke-all keeps the wrong row, TestSessions_DeleteOthers fails: alice has 3
                   sessions, bob 1; DeleteOthers("alice", a2) returns 2, ListByUser("alice") == [a2],
                   bob's row untouched
                 - if Touch stops writing both columns, TestSessions_Touch fails: after Touch(id,
                   ls, ex), ByTokenHash returns LastSeenAt == ls and ExpiresAt == ex to the second
                 - if expiry sweep is off by one, TestSessions_DeleteExpired fails: rows expiring at
                   t, t+1s with now = t deletes exactly one (the ≤ row)
                 - if sessions do not survive a restart, TestSessions_Persist fails: Insert, Close,
                   Open same dir, ByTokenHash returns the row with all fields equal
                 - if a miss is reported as a driver error, TestSessions_NotFound fails:
                   ByTokenHash(unknown) yields errors.Is(err, auth.ErrNotFound)

  t-7  SPA fetch layer with CSRF header and 401 routing                        [risk+verification]
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/package.json, web/pnpm-lock.yaml,
                 web/vite.config.ts, Makefile
       covers:   c-5, c-7
       depends:  t-5
       desc:     api.ts exports request<T>(method, path, body?) that always sends credentials:
                 'same-origin' and X-Requested-With: adguard-reward, parses the {error, message}
                 envelope into ApiError{status, code, message, retryAfter?} (Retry-After header read
                 on 429), and on 401 for any path other than /api/v1/login invokes an injectable
                 onUnauthorized() (default: navigate to /login) exactly once per response.
                 Typed helpers login(u, p), logout(), me(), plus a route store (writable<'login'|'home'>)
                 driven by location.pathname and history.pushState. Adds vitest (devDependency,
                 `test` script "vitest run", vite.config.ts test.environment = 'node') and Makefile
                 targets test-go (go test -race ./...) and test-web (cd web && pnpm test) with `test`
                 depending on both, so a broken fetch layer fails the phase gate. (D10 records the
                 node --test alternative.)
       contract: - if the CSRF header is dropped, api.test.ts "sends X-Requested-With" fails: a
                   mocked fetch sees the header on POST login and on GET me
                 - if 401 handling loops, api.test.ts "401 on /login does not redirect" fails:
                   a 401 from /api/v1/login resolves to ApiError{code: bad_credentials} and
                   onUnauthorized is not called; a 401 from /api/v1/me calls it exactly once
                 - if 502 and 401 collapse, api.test.ts "distinguishes adguard_unavailable" fails:
                   a 502 {error: adguard_unavailable} yields ApiError.code adguard_unavailable, a
                   401 {error: bad_credentials} yields bad_credentials, and the two messages differ
                 - if Retry-After is ignored, api.test.ts "429 carries retryAfter" fails: a 429
                   with Retry-After: 120 yields ApiError.retryAfter === 120
                 - if the web tests fall out of the gate, `make test` no longer runs `pnpm test`:
                   the Makefile test target's prerequisites include test-web
       contract (grafted from mvp):
                 - if login stops posting JSON credentials, api.test "login posts credentials" fails:
                   login('a','b') calls fetch with method POST to /api/v1/login, Content-Type
                   application/json and body '{"username":"a","password":"b"}'

Wave 3 (depends t-2, t-3, t-4, t-5, t-6, t-7)
  t-8  Login page, placeholder home, routing shell                                     [risk]
       files:    web/src/App.svelte, web/src/lib/Login.svelte, web/src/lib/Home.svelte,
                 web/src/lib/Counter.svelte, web/src/main.ts
       covers:   c-7
       depends:  t-7
       desc:     App.svelte replaces the Vite scaffold: on mount calls me(); 200 → home, 401 → login.
                 Login.svelte: username/password form, submit disabled while pending; on ApiError
                 shows one of three distinct strings keyed by code — bad_credentials "Wrong username
                 or password", adguard_unavailable "Can't reach AdGuard Home — try again in a
                 moment", rate_limited "Too many attempts — wait {retryAfter}s"; other codes show
                 the server message. Success navigates to Home.svelte, which shows "Signed in as
                 {username}" and a Log out button calling logout() then routing to login.
                 Counter.svelte and the scaffold assets imports are deleted. Uses Svelte 5 runes;
                 passes `pnpm check`.
       contract: - if the three messages collapse, api.test.ts "loginMessage" fails: exported
                   messageFor(ApiError) in api.ts (used by Login.svelte) returns three distinct
                   strings for bad_credentials / adguard_unavailable / rate_limited, the last one
                   containing the retryAfter number
                 - if the Svelte 5 types break, `pnpm check` (make typecheck) fails: Login.svelte's
                   form handler references ApiError.code, so removing the field from api.ts stops
                   the check
                 - if the scaffold survives, `grep -rl Counter web/src` returns nothing (asserted by
                   the reviewer at verify time; no runtime test)
       contract (grafted from verification):
                 - manual gate recorded in the task commit body: `make dev` + `make dev-web`
                   against a fake-less AdGuard: wrong password shows the 401 message,
                   AdGuard stopped shows the 502 message, correct login lands on Home showing
                   the username (no DOM test runner in this phase — see judgment calls)

  t-9  Login, logout, me handlers with login event log            [risk+mvp+verification]
       files:    internal/api/login.go, internal/api/login_test.go, internal/api/api.go
       covers:   c-1, c-2, c-3, c-4, c-6, c-8, c-10
       depends:  t-2, t-3, t-4, t-5, t-6
       desc:     POST /api/v1/login: body via http.MaxBytesReader(4 KiB) strict-decoded {username,
                 password}; malformed JSON, unknown key, or empty field → 400 bad_request with no
                 AdGuard call, no budget consumed and no login event. ip := Deps.ClientIP(r).
                 Limiter.Check(ip) false → 429 rate_limited, Retry-After: <seconds>, outcome
                 rate_limited, no AdGuard call. Otherwise acquire a single in-flight slot (buffered
                 chan of 1, select on r.Context()) so parallel bursts cannot overrun either budget
                 (D6), call AdGuard.Login: nil → auth.Issue, Set-Cookie, 204, outcome ok;
                 ErrBadCredentials → Limiter.Fail(ip), 401 {bad_credentials, "invalid username or
                 password"} (identical body for every rejection); anything else (StatusError 5xx,
                 *url.Error, ErrRateLimited) → 502 {adguard_unavailable, "AdGuard Home is
                 unreachable"}, outcome adguard_unreachable, no budget consumed. Every outcome logs
                 exactly one Info line slog.Info("login", "event", "login", "username", u, "ip",
                 ip.String(), "outcome", o) — the password value is never an attr. POST
                 /api/v1/logout (RequireSession): store.Delete(sess.ID), ClearCookie, 204. GET
                 /api/v1/me (RequireSession): 200 {username, expires_at RFC3339}. Routes mounted in
                 api.go inside the t-5 chain, plus a catch-all "/api/v1/" → RequireSession
                 (http.NotFoundHandler()) so an unknown path is 401 without a session and 404 with
                 one (D12). Tests run against store.Open in t.TempDir(), the adguardtest fake, a fake
                 clock shared by Manager and Limiter, and a JSON slog handler into a buffer.
       contract: - if the credential/transport split breaks, TestLogin_Outcomes fails: right creds →
                   204 with a Set-Cookie for adguard_reward_session; wrong password → 401 body
                   exactly {error: bad_credentials, message: "invalid username or password"}; fake
                   SetStatus(/control/login, 503) → 502 with error adguard_unavailable; fake Close()d
                   → 502 adguard_unavailable; fake SetStatus(/control/login, 429) → 502, not 401
                 - if the fake is called while limited, TestLogin_RateLimited fails: 5 wrong-password
                   posts from 203.0.113.1, then the fake's Requests() /control/login count is
                   snapshotted; 3 more posts each return 429 with a numeric Retry-After ≥ 1 and the
                   count is unchanged; a post from 203.0.113.2 still reaches the fake
                 - if successes or 502s consume budget, TestLogin_BudgetOnlyOnFailure fails: 4
                   failures + 10 successes + 3 fake-503 outcomes from one IP leave the next
                   wrong-password post reaching the fake (401, not 429)
                 - if a parallel burst overruns the budget, TestLogin_Burst fails: Hang
                   (/control/login, 30ms), 12 concurrent wrong-password posts from one IP yield exactly
                   5 recorded /control/login hits and 7 responses of 429
                 - if the global limit is not wired, TestLogin_Global fails: 20 wrong-password posts
                   from 20 distinct IPs (via a stub ClientIP reading a test header) make a 21st IP's
                   post 429 with no new fake hit
                 - if the event line breaks or leaks, TestLogin_EventLog fails: one line with
                   event=login and the matching outcome exists for each of ok, bad_credentials,
                   adguard_unreachable, rate_limited, with username and ip attrs; the captured
                   Debug-level buffer never contains the submitted password "pw-CANARY-4c1e"
                 - if the raw token reaches any log, TestLogin_NoTokenInLogs fails: Debug-level
                   buffer across login → GET /me → POST /logout contains "/control/login" and
                   event=login (positive control) and never the cookie value from Set-Cookie
                 - if logout does not revoke server-side, TestLogout_Replay fails: login, logout
                   (204, Set-Cookie with Max-Age=0), then GET /me with the old cookie → 401
                 - if /me or the auth gate breaks, TestMe fails: with cookie → 200 {username ==
                   fake user, expires_at parses and is ≈ now+30d}; without cookie → 401 unauthorized;
                   both responses carry Cache-Control: no-store
                 - if bad bodies reach AdGuard, TestLogin_BadBody fails: "{", {"username":"a"},
                   {"username":"","password":""}, a 5 KiB body and {"username":"a","password":"b",
                   "extra":1} each → 400 with zero fake hits and no event=login line
                 - if the CSRF ordering is lost on the real route, TestLogin_CSRF fails: a
                   well-formed login POST without X-Requested-With → 403 with zero fake hits;
                   the same with the header → 204
       contract (grafted from verification):
                 - if the login round-trip breaks, TestLogin_OK fails: fake AdGuard with
                   Options{User:"mum",Pass:"pw"}; POST /api/v1/login {"username":"mum",
                   "password":"pw"} with the header -> 204, Set-Cookie named
                   adguard_reward_session with HttpOnly/SameSite=Strict/Path=/; the fake's
                   recorded /control/login body decodes to {name: mum, password: pw} with no
                   Authorization header
                 - if the escalation is not surfaced through Retry-After,
                   TestLogin_RetryAfterEscalates fails (Manager and Limiter share a fake
                   clock): trip #1 Retry-After "60"; advance 61s, trip #2 -> "120"; advance
                   30s -> "90"
                 - if trusted_proxies is ignored at the HTTP layer, TestLogin_ProxiedIP fails:
                   trusted [127.0.0.0/8], RemoteAddr 127.0.0.1, XFF "198.51.100.7": 5 failures
                   lock out XFF 198.51.100.7 but XFF 198.51.100.8 from the same peer is still
                   401 (not 429); with trusted nil, the same two XFF values share one budget
                   (the 6th is 429)
                 - if the router lets unknown or unauthenticated routes through,
                   TestRouter_Fallthrough fails: GET /api/v1/does-not-exist without a cookie ->
                   401 (not 404); with a cookie -> 404; POST /api/v1/login without the
                   X-Requested-With header -> 403 and the fake recorded zero /control/login
                   hits; GET /healthz is not under Router (compile-level: Router only mounts
                   /api/v1/, asserted by a request for /healthz through Router returning 404)
                   (adjusted: "Router" reads api.Handler())

Wave 4 (depends t-9)
  t-10 Sessions list, revoke one, revoke all                                          [risk]
       files:    internal/api/sessions.go, internal/api/sessions_test.go, internal/api/api.go
       covers:   c-9
       depends:  t-9
       desc:     All under RequireSession. GET /api/v1/sessions → 200 {sessions: [{id, created_at,
                 last_seen_at, current}]} for the caller's username, current = id == ctx session id,
                 RFC3339 times, an empty list is [] not null. DELETE /api/v1/sessions/{id} (non-numeric
                 id → 404) → store.DeleteByUser(user, id): true → 204 (and ClearCookie when id is the
                 caller's own); false → 404 not_found (never 403, so foreign ids are indistinguishable
                 from unknown ones). POST /api/v1/sessions/revoke-all → DeleteOthers(user, current)
                 → 200 {revoked: n}. Tests log in twice as the fake user and once as a second user
                 (fake accepts one user, so the second identity is inserted directly through the
                 store — D15).
       contract: - if `current` or the listing breaks, TestSessions_List fails: two logins A and B
                   for the same user; GET with cookie A lists exactly 2 with current true only on A's
                   id; a third session inserted for user "other" is absent
                 - if IDOR is possible, TestSessions_DeleteForeign fails: DELETE of "other"'s id with
                   cookie A → 404, the row still lists for "other"; DELETE of a random id → 404
                 - if revoke-one leaves the cookie usable, TestSessions_DeleteOne fails: DELETE B
                   with cookie A → 204; GET /me with cookie B → 401; GET /me with cookie A → 200
                 - if deleting yourself is mishandled, TestSessions_DeleteSelf fails: DELETE A with
                   cookie A → 204 with a Max-Age=0 Set-Cookie; GET /me with A → 401
                 - if revoke-all revokes the caller, TestSessions_RevokeAll fails: sessions A, B, C
                   for one user; POST revoke-all with A → 200 {revoked: 2}; /me with B and C → 401;
                   /me with A → 200; revoke-all again → {revoked: 0}
                 - if the CSRF or no-store chain skips these routes, TestSessions_Chain fails:
                   DELETE without X-Requested-With → 403; every response above carries
                   Cache-Control: no-store
       contract (grafted from mvp):
                 - if a non-numeric id is mishandled, TestSessions's case "DELETE /sessions/abc → 404"
                   fails   (adjusted: added as a case of TestSessions_DeleteForeign)

  t-11 Wire main: store, limiter, API, sweeper, TLS listener, Secure cookie  [risk+mvp+verification]
       files:    cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go
       covers:   c-1, c-2, c-3, c-10   (session_storage, client_ip_source locked decisions)
       depends:  t-1, t-2, t-3, t-9
       desc:     After the startup probe: store.Open(cfg.DataDir) (error → Error log naming
                 data_dir, return 1); ratelimit.New with the locked thresholds; tlsOn :=
                 cfg.TLS.Cert != "" || cfg.TLS.Key != "" — exactly one set → Error log "tls.cert and
                 tls.key must both be set", return 1; auth.New(store, Secure: tlsOn ||
                 strings.HasPrefix(cfg.BaseURL, "https://")) (D5); api.New with ClientIP built from
                 cfg.TrustedProxyNets() and AdGuard = the phase-01 client; mux.Handle("/api/v1/",
                 api.Handler()); go auth.RunSweeper(ctx, sweepInterval) where `var sweepInterval =
                 time.Hour` is a package var (probeInterval is a const, so there is no existing var
                 pattern — this introduces one); srv.ServeTLS(ln, cert, key) when tlsOn, srv.Serve
                 otherwise, and the "listening" line gains tls=<bool>; store.Close after Serve
                 returns. /healthz stays on the plain mux outside the API chain. main_test's
                 writeConfig helper adds data_dir: <t.TempDir()> so no existing test writes ./data,
                 and gains a login helper posting with the CSRF header against a running run().
       contract: - if sessions do not survive a restart, TestRun_SessionSurvivesRestart fails:
                   start run() with data_dir in t.TempDir(), login, GET /me 200, cancel ctx (run
                   returns 0), start run() again on the same dir and port, GET /me with the same
                   cookie → 200
                 - if the Secure flag is not derived from TLS config, TestRun_CookieSecure fails:
                   plain config → Set-Cookie lacks Secure; config with base_url https://... → Secure
                   present (TLS listener itself is out of scope; the flag is what is asserted)
                 - if the API chain is mounted wrong, TestRun_ApiChain fails: POST /api/v1/login
                   without the header → 403; GET /api/v1/me without cookie → 401 with Cache-Control:
                   no-store; GET /healthz without cookie → 200 (still open)
                 - if a failed store open is not fatal, TestRun_StoreOpenFails fails: data_dir
                   pointing at a regular file makes run return 1 with stderr containing data_dir
                 - if the limiter or client IP are not plumbed, TestRun_RateLimitWired fails: 6
                   wrong-password logins over the real listener → the 6th is 429 with Retry-After
                   and the fake recorded 5 /control/login hits
                 - if any startup or request log leaks a secret, TestRun_NoSecretsInLogs (extended)
                   fails: the existing canaries plus a login with password "pw-LOGIN-CANARY-1d7f"
                   and the resulting cookie value are absent from stderr at LOG_LEVEL=debug, while
                   "listening", "/control/login" and event=login are present
                 - if the sweeper is not started, TestRun_Sweeper fails: a session row inserted
                   directly with expires_at in the past is gone from the DB within 2s when run()
                   is started with a test hook lowering the sweep interval (package var
                   sweepInterval set by the test, mirroring probeInterval)
       contract (grafted from mvp):
                 - if Secure does not follow TLS or the TLS listener is not wired, TestRun_TLS fails: a
                   self-signed cert/key pair generated in the test, tls.cert/tls.key set → run listens and
                   an https client with InsecureSkipVerify gets a 204 login whose Set-Cookie has Secure;
                   the plain-http config's cookie lacks Secure; tls.cert set without tls.key → run returns 1
                   with stderr containing tls.key
                 - if trusted_proxies is not plumbed through, TestRun_TrustedProxies fails: with
                   trusted_proxies [127.0.0.0/8] and X-Forwarded-For: 203.0.113.7, five wrong logins → the
                   6th is 429 and the login log lines carry ip=203.0.113.7; without trusted_proxies the
                   same header leaves ip=127.0.0.1 in the log lines
```

Notes on the skeleton bullets that the grafts supersede rather than duplicate:
- t-11 TestRun_CookieSecure's parenthetical "(TLS listener itself is out of scope…)" is
  overridden by D5; keep the bullet (base_url https case) and let TestRun_TLS carry the
  listener case.
- t-11 TestRun_Sweeper's "mirroring probeInterval" is wrong as written (const); the desc
  states the corrected mechanism.

## Coverage

| criterion | tasks (merged) |
|---|---|
| c-1 login → AdGuard, 204/401/502 split | t-9 (TestLogin_OK forwarded body, TestLogin_Outcomes), t-11 (TestRun_ApiChain, TestRun_TLS over the real listener) |
| c-2 256-bit token, hash-only, cookie flags, Secure on TLS, token never logged | t-4 (TestIssue_Token, TestCookie_Attrs, TestNoTokenInLogs), t-9 (TestLogin_NoTokenInLogs across login/me/logout), t-11 (TestRun_CookieSecure, TestRun_TLS, TestRun_NoSecretsInLogs) |
| c-3 401 everywhere but login, /healthz open, logout revokes | t-4 (TestRequireSession), t-6 (Delete persists), t-9 (TestLogout_Replay, TestRouter_Fallthrough), t-11 (TestRun_ApiChain healthz open) |
| c-4 per-IP + global limit, 429 + Retry-After, zero AdGuard hits | t-2 (maths), t-3 (which IP), t-9 (TestLogin_RateLimited zero hits, Burst, Global, ProxiedIP), t-11 (TestRun_RateLimitWired, TestRun_TrustedProxies) |
| c-5 X-Requested-With gate before handlers | t-5 (TestCSRF_*), t-7 (client sends it), t-9 (TestLogin_CSRF real route), t-10 (TestSessions_Chain) |
| c-6 GET /me | t-4 (context), t-9 (TestMe) |
| c-7 Svelte login page, distinct messages, 401 → /login, home | t-7 (fetch layer, 401 hook, messages), t-8 (pages, messageFor, manual gate) |
| c-8 login event line per outcome, no password | t-9 (TestLogin_EventLog, TestLogin_BadBody no line on 400) |
| c-9 sessions list / revoke / revoke-all, revoked cookie → 401 | t-1 (schema), t-6 (queries), t-10 (API) |
| c-10 Cache-Control: no-store | t-5 (outer layer incl. 403/404), t-9 (TestMe 200 + 401), t-10, t-11 |
| c-11 doubling lockout, cap, reset, flat global, Retry-After tracks | t-2 (TestPerIP_Doubling/Reset/RetryAfter, TestLimiter_Reset, TestGlobal_Flat), t-9 (TestLogin_RetryAfterEscalates through HTTP) |

Locked decisions: session_storage → t-1, t-6, t-11 (restart test); session_lifetime → t-4
(sliding + cookie re-issue); rate_limit_thresholds → t-2 (no Success method), t-9
(TestLogin_BudgetOnlyOnFailure); client_ip_source → t-3, t-9 (ProxiedIP), t-11 (TrustedProxies).

## Disagreements

**D1 — Package layout.** risk: internal/store, internal/ratelimit, internal/auth, internal/api
(clientip inside api). mvp: internal/store plus one internal/auth holding limiter, client IP,
handlers, middleware, routes ("a package with one importer is structure no criterion asks for").
verification: internal/store, internal/ratelimit, internal/clientip, internal/auth. Default:
risk's layout — the skeleton's tasks are cut along it and each package has its own test surface.
Why it matters: mvp's single package would force t-2/t-3/t-4/t-5 to share a directory and
`go test ./internal/auth` to be the only gate; the merge hazard is small but the task
independence in wave 1 is real.

**D2 — auth tested over a SessionStore interface with a map fake (risk) vs concrete
*store.Store in every auth test (mvp, verification).** verification's argument: a mock lets
"only the hash is stored" pass while the real schema stores the token. risk's: layer isolation
and t-4 runs in wave 1 without waiting on t-1. Default: risk's interface — the mock risk is
covered by t-6 (real SQL) and t-9/t-11 (real store end-to-end, TestLogin_NoTokenInLogs,
TestRun_NoSecretsInLogs). Why it matters: a wave-1 t-4 shortens the critical path by one wave;
the cost is ~60 lines of fake store.

**D3 — Session id.** risk: separate 16-byte random hex. mvp and verification: INTEGER PRIMARY
KEY (rowid). risk's rationale only argues against id = hash, not against integers; revoke is
scoped by (username, id) so a guessable id gains nothing. Default: integer id (2 of 3; one
fewer random value; changes t-1's TestSchema_Sessions and t-4/t-6 signatures as marked).
Why it matters: an integer id leaks a per-instance creation count in /sessions; if the user
cares, revert to risk's hex id in t-1/t-4/t-6/t-10.

**D4 — Migration mechanism.** risk and verification: go:embed'd *.sql files with a
schema_migrations table. mvp: []string statements tracked by PRAGMA user_version. Default:
embedded files (majority; verification's TestMigrate_Order and risk's TestMigrate_Rollback both
need per-file identity). Why it matters: user_version is ~15 lines less; phase 03 adds tables
either way.

**D5 — TLS listener and how Secure is derived.** risk: no listener this phase; Secure =
tls.cert set || base_url https (the config.go comment says the listener "is wired in a later
phase"). mvp and verification: wire ServeTLS now (~10 lines + a generated cert in the test)
because c-2's "Secure when the app serves TLS" is vacuous otherwise; Secure = cert && key.
Default: wire ServeTLS (2 of 3, makes c-2 testable end-to-end) AND keep risk's base_url-https
clause so a TLS-terminating proxy deployment still gets a Secure cookie. Also mvp's
"exactly one of cert/key → exit 1". Why it matters: without the listener a Secure cookie can
never be exercised; without the base_url clause the common reverse-proxy deployment ships a
non-Secure cookie.

**D6 — Serialising in-flight AdGuard login calls (risk only).** risk adds a single buffered
slot so a parallel burst cannot reach AdGuard 12 times before the first failure is recorded
(TestLogin_Burst). mvp and verification have no such guard. Default: include as written —
without it "5 per minute" is bounded by connection concurrency, not by the limiter. Cost: a
hung AdGuard queues every login for up to the 10s client timeout each (acquisition is
ctx-aware). Why it matters: the alternative (a per-IP slot) closes the same hole with less
head-of-line blocking but is in no draft; raise it at plan review if the queueing is judged
unacceptable.

**D7 — Expired-session sweeper.** risk (RunSweeper in auth, hourly, wired in main) and
verification (hourly goroutine in main) include one; mvp relies on read-time expiry only,
so a lost phone's row lives forever. Default: include (majority). Why it matters: one
goroutine, one test hook (`var sweepInterval`); dropping it leaves a slowly growing table.

**D8 — Cookie re-issued on every authenticated request (verification only).** risk and mvp set
Max-Age = 30d once at issue and only slide the server row. With a fixed Max-Age the browser
drops the cookie 30 days after login regardless of use, so the locked decision's "a phone in
daily use never re-logs in" is false. Default: adopt verification's re-issue in
RequireSession (grafted into t-4). Why it matters: this is the only change that makes the
session_lifetime decision true at the browser; cost is one Set-Cookie header per request.

**D9 — Error envelope and what the SPA keys on.** risk: {"error": code, "message": text},
SPA keys on code. mvp: {"error": human text}, SPA keys on HTTP status. verification:
{"error": code}, SPA keys on status. Default: risk's envelope (superset; c-1's "a body that
distinguishes it" is satisfied explicitly by the code field, and the SPA does not need to
infer meaning from 502). Related shape choice: revoke-all returns 200 {revoked: n} (risk,
verification) not 204 (mvp). Why it matters: phase 03+ handlers inherit the envelope; changing
it later touches the SPA.

**D10 — Frontend test runner.** risk and verification: vitest. mvp: `node --test` with Node 24
type stripping (zero new deps; Node 24.14 is installed). Default: vitest (majority; the
Vite-native runner will be needed once a component test is ever wanted). Why it matters: mvp's
option keeps package.json unchanged; if dependency minimalism is a project value, switch t-7.

**D11 — How the Svelte components are verified.** verification: source-text assertions
(Login.svelte must contain "loginErrorMessage(" and not the literal strings; App.svelte must
contain "onUnauthorized") plus a manual gate recorded in the commit body. risk: messageFor in
api.ts under vitest + `pnpm check`. mvp: svelte-check only. Default: risk's, plus
verification's recorded manual gate (grafted into t-8); the source greps are dropped as brittle.
Why it matters: c-7 is the least machine-verified criterion in every draft; the manual gate
line is the honest record of that.

**D12 — Unknown /api/v1/* path without a session: 401 or 404.** mvp and verification add a
"/api/v1/" catch-all → RequireSession(NotFound) so c-3's "every /api/v1/* route" holds for
paths that do not exist yet; risk's mux would answer 404. Default: the catch-all (grafted into
t-9 with TestRouter_Fallthrough). Why it matters: without it phase-03 routes are protected
only if each one remembers to wrap itself.

**D13 — Wave count.** risk puts main wiring in a fifth wave after the sessions API;
verification runs them in parallel because main never touches the API package. Default: 4
waves, t-11 depends on t-9 not t-10 (t-10 edits api.go, t-11 edits main.go — no shared file).
Why it matters: one fewer serial step; TestRun_ApiChain does not need sessions routes.

**D14 — Global limiter window.** risk: one fixed 1-minute window. mvp and verification:
sliding list of failure timestamps ("retryAfter = time until the oldest failure leaves the
window"). Both are "flat". Default: risk's fixed window (skeleton, simpler, TestGlobal_Flat
as written). Why it matters: a fixed window admits up to 39 failures in 61s across the
boundary; at 20/min on a family app this is cosmetic, but it is a behavioural difference the
implementer must not "fix" halfway.

**D15 — Second user identity in cross-user session tests.** risk: insert the second user's row
directly through the store. verification: a second adguardtest fake with Options{User:"dad"}
(needs a second handler set over a shared store). Default: risk's direct insert (the
fake accepts exactly one User/Pass; a second fake buys nothing the store insert does not).
Why it matters: only test ergonomics.

**D16 — Body bound on login.** risk and verification: http.MaxBytesReader 4 KiB. mvp: 1 KiB
LimitReader. Default: 4 KiB via MaxBytesReader (majority; MaxBytesReader also closes the
connection on overflow). Why it matters: none beyond consistency with the adguard client's
1 KiB error-body bound; either is fine.
