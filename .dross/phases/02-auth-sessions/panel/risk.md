# Risk-lens draft — 02-auth-sessions

Bias: every task is shaped around a failure mode it alone owns and tests. The
failure inventory that drove the graph:

| # | What breaks | Owner |
|---|---|---|
| R1 | SQLite: `SQLITE_BUSY` under concurrent writers, non-idempotent migrations on restart, half-applied migration, missing/unwritable data_dir | t-1 |
| R2 | Limiter maths: successes consuming budget, global limit escalating, doubling not capped, escalation never resetting, wrong Retry-After, per-IP map growing forever, races under -race | t-2 |
| R3 | Client IP: X-Forwarded-For spoofing when no proxy is trusted, wrong hop picked behind a proxy, IPv6 peers with brackets, bad CIDR accepted, new yaml leaf without an env row | t-3 |
| R4 | Token/cookie: weak token, raw token persisted, token in logs, cookie flags wrong, Secure on plain HTTP, expired session accepted, sliding expiry not extended, revoked session still usable | t-4 |
| R5 | Cross-cutting HTTP: CSRF header check skipped for some method/route, `Cache-Control: no-store` missing on error responses because the middleware is inside the handler, error bodies leaking which layer failed | t-5 |
| R6 | Session rows: token_hash not unique, revoke-all deleting the caller, cross-user revoke, expired rows never swept, session lost across close/reopen | t-6 |
| R7 | SPA fetch layer: missing X-Requested-With, 401 on /login page causing a redirect loop, 502 and 401 collapsed into one message | t-7 |
| R8 | Login handler: burst of parallel attempts overrunning the budget before any failure is recorded, AdGuard 429/5xx/unreachable misreported as bad credentials, oversized/invalid JSON, password in the login event line, raw token in debug logs | t-9 |
| R9 | Sessions API: IDOR on DELETE, revoke-all keeping the wrong session, `current` flag wrong, revoked cookie still valid on next request | t-10 |
| R10 | Wiring: restart logs every phone out, cookie Secure not derived from TLS, sweeper not started, limiter/clientip config not plumbed, canary secrets in stderr across the whole binary | t-11 |

```
Phase 02-auth-sessions — 11 tasks across 5 waves

Wave 1
  t-1  Found internal/store: open, pragmas, migrations
       files:    internal/store/store.go, internal/store/migrate.go,
                 internal/store/migrations/0001_sessions.sql,
                 internal/store/store_test.go, go.mod, go.sum
       covers:   c-3, c-9   (session_storage locked decision)
       depends:  —
       desc:     Add modernc.org/sqlite. store.Open(dataDir string, log *slog.Logger) (*Store, error):
                 MkdirAll(dataDir, 0o700), opens <dataDir>/adguard-reward.db with
                 _pragma=journal_mode(WAL), busy_timeout(5000), foreign_keys(ON), synchronous(NORMAL);
                 db.SetMaxOpenConns(1) so modernc never sees two writers (R1). Migrations are
                 go:embed'd *.sql files applied in name order, each in its own transaction, tracked
                 in schema_migrations(name TEXT PRIMARY KEY, applied_at INTEGER); a failing statement
                 rolls back that migration and Open returns an error naming the file.
                 0001_sessions.sql: sessions(id TEXT PRIMARY KEY, username TEXT NOT NULL,
                 token_hash BLOB NOT NULL UNIQUE, created_at INTEGER NOT NULL, last_seen_at INTEGER
                 NOT NULL, expires_at INTEGER NOT NULL) + index on (username), index on (expires_at).
                 Store exposes DB() *sql.DB for the query files, Close(), and Migrations() []string.
                 Errors from Open name data_dir so the operator sees the config key.
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
                   the stated types and NOT NULL flags; PRAGMA index_list shows token_hash UNIQUE
                 - if data_dir handling breaks, TestOpen_DataDir fails: a nonexistent nested dir is
                   created with mode 0700; a data_dir that is a regular file errors containing
                   "data_dir"
                 - if the WAL pragma is lost, TestOpen_Pragmas fails: PRAGMA journal_mode returns
                   "wal" and PRAGMA foreign_keys returns 1

  t-2  Login rate limiter with escalating lockout
       files:    internal/ratelimit/limiter.go, internal/ratelimit/limiter_test.go
       covers:   c-4, c-11   (rate_limit_thresholds locked decision)
       depends:  —
       desc:     ratelimit.New(Config{PerIPFailures: 5, Window: 1m, BaseLockout: 1m, MaxLockout: 1h,
                 EscalationReset: 1h, GlobalFailures: 20, Now func() time.Time}) *Limiter.
                 Check(ip) (allowed bool, retryAfter time.Duration) and Fail(ip) under one sync.Mutex.
                 Per IP: failures counted in a fixed 1-minute window; recording the 5th failure trips
                 the IP: lockedUntil = now + BaseLockout << (trips-1) capped at MaxLockout, trips++,
                 lastTrip = now; the failure counter clears when the lockout ends. On Check/Fail, an IP
                 whose lastTrip is ≥ EscalationReset ago has trips reset to 0. Global: one fixed
                 1-minute window; the 20th failure denies every IP until the window ends, never
                 escalates. retryAfter is ceil'd to whole seconds and ≥ 1s whenever allowed==false.
                 Prune() (also run lazily inside Fail) drops IPs with no active lockout, no failures in
                 the current window and lastTrip older than EscalationReset. Len() for tests. No
                 logging, no net/http — pure policy with an injected clock.
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

  t-3  trusted_proxies config and client IP resolver
       files:    internal/config/config.go, internal/config/config_test.go,
                 internal/config/testdata/full.yaml, deploy/config.example.yaml,
                 internal/api/clientip.go, internal/api/clientip_test.go
       covers:   c-4   (client_ip_source locked decision)
       depends:  —
       desc:     Config gains TrustedProxies []string `yaml:"trusted_proxies"`; env row
                 ADGUARD_REWARD_TRUSTED_PROXIES splits on commas (trimmed, empties dropped; set-but-
                 empty → empty list). validate() parses every entry with net.ParseCIDR (a bare IP is
                 accepted as /32 or /128) and errors naming trusted_proxies and the bad entry.
                 Config.TrustedProxyNets() []*net.IPNet. api.ClientIP(trusted []*net.IPNet)
                 func(*http.Request) netip.Addr: peer = host of r.RemoteAddr (port stripped, IPv6
                 brackets removed, zone dropped). With an empty list or a peer outside every net,
                 X-Forwarded-For is ignored. Otherwise the XFF hops are walked right to left; the first
                 hop not inside a trusted net is the client; if every hop is trusted or a hop fails
                 to parse, fall back to the peer. Unparseable RemoteAddr → netip.Addr{} and the
                 caller keys on "invalid" (no panic). Example config documents the key.
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

  t-4  Session tokens, cookie policy, RequireSession middleware
       files:    internal/auth/token.go, internal/auth/auth.go, internal/auth/middleware.go,
                 internal/auth/auth_test.go
       covers:   c-2, c-3, c-6   (session_lifetime locked decision)
       depends:  —
       desc:     auth.SessionStore interface: Insert(ctx, Session) error; ByTokenHash(ctx, [32]byte)
                 (Session, error) returning ErrNotFound; Touch(ctx, id string, lastSeen, expires
                 time.Time) error; Delete(ctx, id) error; ListByUser(ctx, user) ([]Session, error);
                 DeleteByUser(ctx, user, id) (bool, error); DeleteOthers(ctx, user, keepID) (int,
                 error); DeleteExpired(ctx, now) (int, error). Session{ID, Username, TokenHash,
                 CreatedAt, LastSeenAt, ExpiresAt}. auth.New(store, Options{Secure bool, Lifetime:
                 30*24h, Now func() time.Time, Log}) *Manager. Issue(ctx, username) (rawToken string,
                 Session, error): 32 bytes crypto/rand → base64.RawURLEncoding (43 chars); ID is a
                 separate 16-byte random hex; only sha256(raw) is stored. Authenticate(ctx, raw)
                 (Session, error): decode → hash → ByTokenHash; ExpiresAt ≤ now → Delete + ErrNoSession;
                 otherwise Touch(now, now+Lifetime) and return the refreshed session (sliding, on
                 every request per the locked decision, no coalescing). Cookie(raw) *http.Cookie:
                 Name "adguard_reward_session", HttpOnly, SameSite=Strict, Path=/, Secure=Options.Secure,
                 MaxAge = Lifetime seconds. ClearCookie() same attrs with MaxAge -1. RequireSession
                 (next) http.Handler: missing/invalid cookie → 401 via the injected errorWriter
                 (func(w, status, code, msg)) so the JSON envelope stays in internal/api; success puts
                 the Session in ctx (auth.FromContext). RunSweeper(ctx, interval) calls DeleteExpired
                 on a ticker. The raw token is never passed to slog; Session has no field holding it
                 and Session.LogValue() emits only id and username.
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

  t-5  API router skeleton, CSRF header, no-store, error envelope
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
                 route via API.mux for ordering assertions.
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
  t-6  Sessions queries implementing auth.SessionStore
       files:    internal/store/sessions.go, internal/store/sessions_test.go
       covers:   c-3, c-9   (session_storage locked decision)
       depends:  t-1, t-4
       desc:     *store.Store implements every auth.SessionStore method with parameterised SQL over
                 the 0001 table; times stored as Unix seconds UTC. ByTokenHash returns
                 auth.ErrNotFound (not sql.ErrNoRows) on miss. DeleteByUser deletes only when both
                 username and id match, returning whether a row went. DeleteOthers deletes the user's
                 rows except keepID and returns the count. ListByUser orders by created_at ASC then
                 id. DeleteExpired removes expires_at ≤ now. A compile-time
                 `var _ auth.SessionStore = (*Store)(nil)` pins the contract.
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

  t-7  SPA fetch layer with CSRF header and 401 routing
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/package.json, web/vite.config.ts,
                 Makefile
       covers:   c-5, c-7
       depends:  t-5
       desc:     api.ts exports request<T>(method, path, body?) that always sends credentials:
                 'same-origin' and X-Requested-With: adguard-reward, parses the {error, message}
                 envelope into ApiError{status, code, message, retryAfter?} (Retry-After header read
                 on 429), and on 401 for any path other than /api/v1/login invokes an injectable
                 onUnauthorized() (default: navigate to /login) exactly once per response. Typed
                 helpers login(u, p), logout(), me(), plus a route store (writable<'login'|'home'>)
                 driven by location.pathname and history.pushState. Adds vitest (devDependency,
                 `test` script, vite.config.ts test.environment = 'node') and a Makefile test-web
                 target that `make test` depends on, so a broken fetch layer fails the phase gate.
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

Wave 3 (depends t-2, t-3, t-4, t-5, t-6, t-7)
  t-8  Login page, placeholder home, routing shell
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

  t-9  Login, logout, me handlers with login event log
       files:    internal/api/login.go, internal/api/login_test.go, internal/api/api.go
       covers:   c-1, c-2, c-3, c-4, c-6, c-8, c-10
       depends:  t-2, t-3, t-4, t-5, t-6
       desc:     POST /api/v1/login: body via http.MaxBytesReader(4 KiB) strict-decoded {username,
                 password}; malformed JSON, unknown key, or empty field → 400 bad_request with no
                 AdGuard call, no budget consumed and no login event. ip := Deps.ClientIP(r).
                 Limiter.Check(ip) false → 429 rate_limited, Retry-After: <seconds>, outcome
                 rate_limited, no AdGuard call. Otherwise acquire a single in-flight slot (buffered
                 chan of 1, select on r.Context()) so parallel bursts cannot overrun either budget
                 (R8), call AdGuard.Login: nil → auth.Issue, Set-Cookie, 204, outcome ok;
                 ErrBadCredentials → Limiter.Fail(ip), 401 {bad_credentials, "invalid username or
                 password"} (identical body for every rejection); anything else (StatusError 5xx,
                 *url.Error, ErrRateLimited) → 502 {adguard_unavailable, "AdGuard Home is
                 unreachable"}, outcome adguard_unreachable, no budget consumed. Every outcome logs
                 exactly one Info line slog.Info("login", "event", "login", "username", u, "ip",
                 ip.String(), "outcome", o) — the password value is never an attr. POST
                 /api/v1/logout (RequireSession): store.Delete(sess.ID), ClearCookie, 204. GET
                 /api/v1/me (RequireSession): 200 {username, expires_at RFC3339}. Routes mounted in
                 api.go inside the t-5 chain. Tests run against store.Open in t.TempDir(), the
                 adguardtest fake, a fake clock and a JSON slog handler into a buffer.
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

Wave 4 (depends t-9)
  t-10 Sessions list, revoke one, revoke all
       files:    internal/api/sessions.go, internal/api/sessions_test.go, internal/api/api.go
       covers:   c-9
       depends:  t-9
       desc:     All under RequireSession. GET /api/v1/sessions → 200 {sessions: [{id, created_at,
                 last_seen_at, current}]} for the caller's username, current = id == ctx session id,
                 RFC3339 times. DELETE /api/v1/sessions/{id} → store.DeleteByUser(user, id): true →
                 204 (and ClearCookie when id is the caller's own); false → 404 not_found (never 403,
                 so foreign ids are indistinguishable from unknown ones). POST
                 /api/v1/sessions/revoke-all → DeleteOthers(user, current) → 200 {revoked: n}. Tests
                 log in twice as the fake user and once as a second user (fake accepts one user, so
                 the second identity is inserted directly through the store).
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

Wave 5 (depends t-1, t-2, t-3, t-10)
  t-11 Wire main: store, limiter, API, sweeper, Secure cookie
       files:    cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go
       covers:   c-1, c-2, c-3, c-10   (session_storage, client_ip_source locked decisions)
       depends:  t-1, t-2, t-3, t-10
       desc:     After the startup probe: store.Open(cfg.DataDir) (error → Error log naming
                 data_dir, return 1); ratelimit.New with the locked thresholds; auth.New(store,
                 Secure: cfg.TLS.Cert != "" || strings.HasPrefix(cfg.BaseURL, "https://")); api.New
                 with ClientIP built from cfg.TrustedProxyNets() and AdGuard = the phase-01 client;
                 mux.Handle("/api/v1/", api.Handler()); go auth.RunSweeper(ctx, 1h); store.Close
                 after Serve returns. /healthz stays on the plain mux outside the API chain.
                 main_test gains a login helper posting with the CSRF header against a running run().
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
```

## Coverage

| criterion | tasks |
|---|---|
| c-1 login → AdGuard, 204/401/502 split | t-9 (handler + outcomes), t-11 (over the real listener) |
| c-2 256-bit token, hash-only, cookie flags, token never logged | t-4 (token/cookie/logs in isolation), t-9 (debug-log capture across login/me/logout), t-11 (Secure from TLS, binary-level canary) |
| c-3 401 everywhere but login, /healthz open, logout revokes | t-4 (RequireSession), t-6 (Delete persists), t-9 (logout replay), t-11 (healthz stays open) |
| c-4 per-IP + global limit, 429 + Retry-After, zero AdGuard hits | t-2 (maths), t-3 (which IP), t-9 (zero fake hits, burst, global), t-11 (wired) |
| c-5 X-Requested-With gate | t-5 (middleware), t-7 (client sends it), t-9 (real route ordering) |
| c-6 GET /me | t-4 (context), t-9 (handler) |
| c-7 Svelte login page, distinct messages, 401 → /login | t-7 (fetch layer, messages), t-8 (pages) |
| c-8 login event line per outcome, no password | t-9 |
| c-9 sessions list / revoke / revoke-all, revoked cookie → 401 | t-1 (schema), t-6 (queries), t-10 (API) |
| c-10 Cache-Control: no-store | t-5 (outer layer), t-9 (asserted on /me 200 and 401), t-10, t-11 |
| c-11 doubling lockout, cap, reset, flat global | t-2 |

All 11 criteria covered; all 4 locked decisions honoured (session_storage: t-1/t-6/t-11;
session_lifetime: t-4; rate_limit_thresholds: t-2/t-9; client_ip_source: t-3/t-11).

## Judgment calls

- **auth over an interface, not over the store** — t-4 tests token/expiry/revocation policy against a map fake so it runs in wave 1; t-6 proves the SQL side separately. Rejected: one wave-3 task testing policy through SQLite, because a failing sliding-expiry test would then not say which layer broke.
- **Serialise AdGuard login calls (one in flight)** — a parallel burst can otherwise reach AdGuard 12 times before the first failure is recorded, making "5 per minute" a fiction. Rejected: pre-reserving budget on Check, because the locked decision says only bad-credential outcomes consume budget. Cost: a hung AdGuard queues logins for up to the 10s client timeout; acquisition is ctx-aware so clients can give up.
- **Sliding expiry writes on every request, no coalescing** — the locked decision says "pushed out on every authenticated request"; a 60s write-coalescing optimisation would be a (small) deviation and a second code path to test. One UPDATE per request on WAL is nothing at family scale.
- **AdGuard 429 (its own limiter) → 502 adguard_unavailable, no budget consumed** — it is not a credential verdict, so it cannot be 401, and counting it would let AdGuard's limiter trip ours. Rejected: 503 with a fourth code, because the SPA and the log outcome enum only know three failure shapes.
- **400 on malformed login bodies emits no login event** — the outcome enum in c-8 has no slot for it and a malformed body is not an attempt against a credential. Rejected: logging it as bad_credentials, which would consume budget for junk.
- **DELETE of a foreign session id → 404, never 403** — a 403 confirms the id exists. Rejected: allowing any admin to revoke any session; c-9 says "the caller's sessions".
- **Session id is separate from the token hash** — listing exposes ids; exposing the hash would hand a client a lookup key for the token column. Rejected: id = hex(token_hash) for simplicity.
- **Cookie Secure = TLS cert configured OR base_url is https** — the TLS listener is not wired in this phase, so "when the app serves TLS" is read as "when the deployment terminates TLS", including behind a proxy. Rejected: wiring the TLS listener here (out of spec); rejected: always Secure (breaks the http://127.0.0.1:5173 dev proxy).
- **All-trusted or unparseable X-Forwarded-For → peer address** — the safe key when the header cannot name an untrusted hop. Rejected: keying on the raw string, which lets a proxy-side bug create unbounded limiter entries.
- **Vitest added for the fetch layer** — the 401-redirect-loop and missing-header risks live in plain TS and are the only SPA risks that can be pinned by a test; `make test` now runs them. Rejected: leaving the SPA at `pnpm check` only, which cannot catch either.
- **Frontend split into fetch layer (wave 2) and pages (wave 3)** — the layer depends only on t-5's error envelope; pages depend on the layer. Rejected: one 9-file UI task.
- **SetMaxOpenConns(1)** on the store — the simplest way to make modernc's single-writer model race-free under `-race`; the cost (no parallel reads) is irrelevant at this scale. Rejected: separate read/write pools, which need a second test surface for BUSY.
