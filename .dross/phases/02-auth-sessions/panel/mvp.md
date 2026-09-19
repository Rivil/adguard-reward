# MVP-lens draft — phase 02-auth-sessions

Lens: smallest task set that satisfies c-1..c-11 and the four locked decisions. No
router library, no test framework, no SPA embedding, no store interface, no
per-request log middleware — each of those was considered and cut because no
criterion needs it (see Judgment calls).

Package layout: `internal/store` (locked name) for SQLite; everything HTTP/auth
lives in one new package `internal/auth` (session, limiter, client IP, handlers,
middleware, routes). `cmd/adguard-reward/main.go` only wires. `web/` gets one
fetch helper, one login component and a rewritten `App.svelte`.

```
Phase 02-auth-sessions — 7 tasks across 4 waves

Wave 1
  t-1  Found internal/store with sessions table
       files:    internal/store/store.go, internal/store/sessions.go, internal/store/store_test.go, go.mod, go.sum
       covers:   c-2, c-3, c-9
       depends:  —
       desc:     go.mod gains modernc.org/sqlite. store.Open(path) (*Store, error) opens the file with
                 journal_mode=WAL, busy_timeout=5000, foreign_keys=on, then runs embedded migrations
                 tracked by PRAGMA user_version (a []string of SQL statements in store.go; migration 1 =
                 sessions(id INTEGER PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE, username TEXT NOT NULL,
                 created_at, last_seen_at, expires_at INTEGER NOT NULL) + index on username). Close().
                 Session{ID int64, Username, CreatedAt, LastSeenAt, ExpiresAt time.Time} — no hash field so
                 it can never be listed or marshalled. Every method takes now time.Time explicitly (fake
                 clocks need no option plumbing): CreateSession(ctx, tokenHash, username, now, ttl)
                 (Session, error); TouchSession(ctx, tokenHash, now, ttl) (Session, error) deletes and
                 returns ErrNotFound when the row is expired, otherwise sets last_seen_at=now,
                 expires_at=now+ttl (session_lifetime: 30d sliding) and returns the updated row;
                 ListSessions(ctx, username, now) newest first, expired rows excluded;
                 DeleteSession(ctx, username, id) ErrNotFound unless the row belongs to username;
                 DeleteOtherSessions(ctx, username, keepID) (int64, error). var ErrNotFound.
       contract: - if migrations stop being idempotent, TestOpen_Migrates fails: Open on a fresh temp path
                   creates table sessions (sqlite_master) with PRAGMA user_version == 1; a second Open on
                   the same path succeeds, user_version stays 1 and a row created before the reopen is
                   still returned by TouchSession
                 - if the token_hash UNIQUE constraint is dropped, TestCreateSession_UniqueHash fails: a
                   second CreateSession with the same hash returns an error
                 - if sliding expiry breaks, TestTouchSession_Sliding fails: create at t0 with 30d ttl;
                   Touch at t0+29d returns LastSeenAt == t0+29d and ExpiresAt == t0+59d; Touch at
                   t0+59d+1s returns ErrNotFound and ListSessions(username, t0) is then empty (the row was
                   deleted, not just hidden)
                 - if an unknown hash is accepted, TestTouchSession_Unknown fails: Touch with a hash never
                   created returns ErrNotFound
                 - if revoke stops being scoped to the owner, TestDeleteSession_Scoped fails:
                   DeleteSession("bob", aliceID) returns ErrNotFound and alice's hash still Touches;
                   DeleteSession("alice", aliceID) returns nil and the next Touch is ErrNotFound
                 - if revoke-all keeps the wrong session, TestDeleteOtherSessions fails: alice with ids
                   1,2,3 and bob with id 4; DeleteOtherSessions("alice", 2) returns 2; ListSessions(alice)
                   is exactly [2]; bob's hash still Touches
                 - if ListSessions leaks expired rows or loses ordering, TestListSessions fails: sessions
                   created at t0 and t0+1h; List at t0+2h returns both, newest first; List at t0+30d+1s
                   returns only the second; json.Marshal of a Session contains no "hash" substring
                 - if busy_timeout is dropped, TestStore_Concurrent fails under -race: 20 goroutines each
                   doing CreateSession + TouchSession on one *Store report zero errors

  t-2  Login rate limiter with escalating lockout
       files:    internal/auth/ratelimit.go, internal/auth/ratelimit_test.go
       covers:   c-4, c-11
       depends:  —
       desc:     auth.NewLimiter(now func() time.Time) *Limiter with the locked thresholds as fields
                 (PerIP 5, Global 20, Window 1m, BaseLockout 1m, MaxLockout 1h, ResetAfter 1h). Allow(ip
                 string) (retryAfter time.Duration, ok bool): !ok while the IP is locked (retryAfter =
                 lockedUntil-now) or while the global window holds >= Global failures (retryAfter = time
                 until the oldest one ages out; flat, never escalates). Fail(ip) records one bad-credential
                 outcome: prunes the IP's failures older than Window, appends; on reaching PerIP it trips —
                 trips reset to 0 first if the last trip is older than ResetAfter, then trips++,
                 lockedUntil = now + min(BaseLockout << (trips-1), MaxLockout), failures cleared. Global
                 failures are a sliding list of timestamps. There is no Success method: only Fail consumes
                 budget (rate_limit_thresholds). sync.Mutex; idle IP entries (unlocked, no failures in
                 Window, last trip older than ResetAfter) are swept on each Fail.
       contract: - if the per-IP threshold drifts, TestLimiter_Threshold fails: 4 Fail(ip) then Allow(ip)
                   is ok; the 5th Fail makes Allow return !ok with retryAfter == 1m; another IP is still ok
                 - if the lockout stops doubling or the cap is lost, TestLimiter_Escalates fails (fake
                   clock): trip 1 gives 1m; after each lockout expires, 5 more fails give 2m, 4m, 8m, 16m,
                   32m, then 1h; an 8th trip still gives exactly 1h
                 - if the 1h reset is dropped, TestLimiter_Reset fails: two trips (2m window), advance 1h+1s
                   with no failures, five fails → retryAfter == 1m again; with only 59m idle the same
                   sequence gives 4m
                 - if the global cap escalates or is per-IP, TestLimiter_Global fails: 20 distinct IPs each
                   Fail once → Allow of a fresh 21st IP is !ok with 0 < retryAfter <= 1m; each of the 20
                   was ok individually before the 20th failure; advance 61s → ok; a second global trip
                   gives retryAfter <= 1m again (flat)
                 - if the window stops sliding, TestLimiter_WindowSlides fails: 4 fails at t0, 1 fail at
                   t0+61s → Allow ok (one failure in the window)
                 - if a rate-limited rejection or success consumed budget, TestLimiter_OnlyFailCounts
                   fails: 100 Allow calls on a fresh IP never make it !ok
                 - if the mutex is removed, TestLimiter_Race fails under -race: 50 goroutines mixing
                   Fail and Allow

  t-3  trusted_proxies config and client IP resolution
       files:    internal/config/config.go, internal/config/config_test.go, deploy/config.example.yaml, internal/auth/clientip.go, internal/auth/clientip_test.go
       covers:   c-4
       depends:  —
       desc:     Config gains TrustedProxies []string `yaml:"trusted_proxies"` with env row
                 ADGUARD_REWARD_TRUSTED_PROXIES (comma-separated, blanks skipped); validate parses each
                 with netip.ParsePrefix and errors naming trusted_proxies and the bad entry;
                 (*Config).TrustedPrefixes() []netip.Prefix. TestEnvTable's want map and its
                 set-every-var override map gain the new row (the generic "v:trusted_proxies" value would
                 fail CIDR validation). config.example.yaml gains `trusted_proxies: []` with a one-line
                 comment. auth.ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr: peer = host of
                 r.RemoteAddr; X-Forwarded-For is ignored unless the peer is inside a trusted prefix, in
                 which case the header's addresses (all values, comma-split) are walked right to left
                 skipping trusted hops and the first untrusted one wins; unparseable or all-trusted
                 falls back to the peer (client_ip_source).
       contract: - if X-Forwarded-For is honoured by default, TestClientIP_Default fails: RemoteAddr
                   203.0.113.9:1234 with X-Forwarded-For: 1.2.3.4 and nil trusted yields 203.0.113.9
                 - if the last-untrusted-hop rule breaks, TestClientIP_Trusted fails: trusted [10.0.0.0/8],
                   RemoteAddr 10.0.0.2:1: XFF "1.2.3.4, 5.6.7.8, 10.0.0.3" → 5.6.7.8; XFF "1.2.3.4" →
                   1.2.3.4; XFF "10.0.0.3" (all trusted) → 10.0.0.2; XFF "foo" → 10.0.0.2; two XFF header
                   values "1.2.3.4" and "5.6.7.8" → 5.6.7.8; RemoteAddr 192.0.2.1:1 (untrusted) with XFF
                   1.2.3.4 → 192.0.2.1
                 - if address parsing regresses, TestClientIP_Forms fails: RemoteAddr "[::1]:8080" → ::1;
                   "::1" with no port → ::1; "garbage" → the zero netip.Addr (IsValid false), never a panic
                 - if trusted_proxies loses its env row, TestEnvTable fails: the want map pins
                   trusted_proxies → ADGUARD_REWARD_TRUSTED_PROXIES and the leaf walk still finds a row
                   for every leaf
                 - if CIDR validation or env splitting breaks, TestLoad_TrustedProxies fails: yaml
                   [10.0.0.0/8, fd00::/8] gives TrustedPrefixes() of length 2; env "10.0.0.0/8, 192.168.0.0/16"
                   gives 2 and replaces the yaml list; yaml [10.0.0.1] (no length) and env "nope" each
                   error containing trusted_proxies and the offending text; an empty list is valid

  t-4  Svelte login page, home placeholder, 401 routing
       files:    web/src/App.svelte, web/src/lib/Login.svelte, web/src/lib/api.ts, web/src/lib/api.test.ts, web/package.json, web/tsconfig.app.json, Makefile
       covers:   c-7
       depends:  —
       desc:     lib/api.ts: api(path, init) wraps fetch with credentials: 'same-origin', header
                 X-Requested-With: adguard-reward, JSON Content-Type when a body is given, and calls the
                 registered setUnauthorizedHandler(fn) on any 401 except from /api/v1/login;
                 login(username, password) → {ok: true} | {ok: false, message}; me() → {username,
                 expires_at} | null; logout(); loginErrorMessage(status, retryAfter) maps 401 → "Wrong
                 username or password", 502 → "AdGuard is unreachable — check that it is running", 429 →
                 "Too many attempts, try again in <Retry-After>s", else "Login failed (HTTP <status>)".
                 App.svelte (scaffold content deleted, lib/Counter.svelte and src/assets/* removed):
                 route = $state<'loading'|'login'|'home'>; onMount calls me() → home or login; the
                 unauthorized handler sets route = 'login' and history.replaceState('/login'); home is a
                 placeholder "Signed in as {username}" with a Logout button. Login.svelte: username +
                 password form, submit disabled while pending, shows message from login(); on ok the
                 parent re-runs me() and shows home. Tests use node:test + node:assert run by Node 24's
                 native type stripping — no vitest: package.json gains "test": "node --test
                 'src/lib/*.test.ts'", tsconfig.app.json types gains "node" so svelte-check accepts the
                 test file, Makefile test target runs `cd web && pnpm test` after go test.
       contract: - if the CSRF header or cookie mode is dropped, api.test "sends X-Requested-With" fails:
                   with fetch stubbed, api('/api/v1/me') calls fetch once with headers containing
                   X-Requested-With: adguard-reward and credentials 'same-origin'
                 - if the 401 hook breaks or fires on login's own 401, api.test "routes 401" fails: a stubbed
                   401 from /api/v1/me invokes the registered handler exactly once; a stubbed 401 from
                   /api/v1/login invokes it zero times; a stubbed 200 invokes it zero times
                 - if the two error messages are conflated, api.test "loginErrorMessage" fails: 401 and 502
                   return different non-empty strings, the 502 one contains "AdGuard", the 401 one does
                   not; 429 with retryAfter "30" contains "30"
                 - if login stops posting JSON credentials, api.test "login posts credentials" fails:
                   login('a','b') calls fetch with method POST to /api/v1/login, Content-Type
                   application/json and body '{"username":"a","password":"b"}'; a stubbed 204 returns
                   {ok: true}, a stubbed 502 returns {ok: false, message} equal to loginErrorMessage(502)
                 - if App.svelte or Login.svelte regress in types or rune usage, `make typecheck`
                   (svelte-check) fails with a non-zero exit — this is the gate for the two components,
                   which have no unit tests

Wave 2 (depends t-1)
  t-5  Session cookie, RequireSession, me/logout/sessions
       files:    internal/auth/session.go, internal/auth/handlers.go, internal/auth/middleware.go, internal/auth/auth_test.go
       covers:   c-2, c-3, c-5, c-6, c-9, c-10
       depends:  t-1
       desc:     auth.New(Config{Store *store.Store, Log *slog.Logger, Secure bool, Now func()
                 time.Time}) *Service (t-6 adds AdGuard/Limiter/TrustedProxies fields). const CookieName =
                 "session", SessionTTL = 30*24h. newToken() (raw, hash string, error): 32 bytes from
                 crypto/rand, raw = base64.RawURLEncoding, hash = hex(sha256(raw)); only hash reaches the
                 store. (s *Service) issue(ctx, w, username) creates the row and sets the cookie; cookie(v,
                 maxAge) builds http.Cookie{HttpOnly, SameSite: Strict, Path: "/", Secure: s.secure}.
                 RequireSession(next) reads the cookie, hashes, store.TouchSession(now, SessionTTL);
                 ErrNotFound/no cookie → 401 {"error":"unauthorized"}; else the store.Session goes in ctx
                 (SessionFrom(ctx)). Handlers: Me → 200 {username, expires_at RFC3339}; Logout →
                 store.DeleteSession(username, id), Set-Cookie with MaxAge -1 and empty value, 204;
                 ListSessions → 200 [{id, created_at, last_seen_at, current}]; DeleteSession
                 (r.PathValue("id"), non-numeric → 404) → 204 or 404 on ErrNotFound; RevokeAll → 204.
                 Middleware: RequireHeader(next) 403 {"error":"missing X-Requested-With"} for any method
                 other than GET/HEAD/OPTIONS unless X-Requested-With == "adguard-reward"; NoStore(next)
                 sets Cache-Control: no-store before calling next. Nothing logs the raw token or hash.
       contract: - if the token shrinks or the stored value is not its SHA-256, TestToken fails: raw
                   base64url-decodes to exactly 32 bytes; hash == hex(sha256(raw)); 1000 tokens are
                   pairwise distinct; after issue, `SELECT token_hash FROM sessions` equals
                   hex(sha256(cookie value)) and does not equal the cookie value
                 - if cookie attributes regress, TestCookie_Attributes fails: the issued Set-Cookie parses
                   with HttpOnly, SameSite=Strict, Path=/, Max-Age=2592000; Secure is false with
                   Config.Secure=false and true with Config.Secure=true
                 - if RequireSession accepts a bad cookie, TestRequireSession fails: no cookie → 401 JSON
                   {"error":"unauthorized"} and next not called; a random 43-char cookie → 401; an issued
                   cookie with one byte flipped → 401; the issued cookie → next called and SessionFrom
                   yields the username
                 - if sliding expiry is not applied per request, TestRequireSession_Slides fails (fake
                   clock): issue at t0; Me at t0+29d → 200 with expires_at == t0+59d; Me at t0+59d+1s → 401
                 - if logout stops deleting server-side, TestLogout fails: Logout with a valid cookie → 204
                   whose Set-Cookie has Max-Age=0 and an empty value; replaying the old cookie on Me → 401;
                   store.ListSessions for the user is empty
                 - if Me's shape breaks, TestMe fails: body decodes to exactly the keys username and
                   expires_at, expires_at parses as RFC3339; without a cookie → 401
                 - if the sessions API leaks across users or mislabels current, TestSessions fails: alice
                   issued cookies A1,A2,A3, bob B1; GET sessions with A2 lists 3 entries with keys id,
                   created_at, last_seen_at, current and exactly one current==true whose id is A2's;
                   DELETE bob's id with A2 → 404 and B1 still 200 on Me; DELETE A1's id with A2 → 204 and
                   A1 gets 401 on Me; POST revoke-all with A2 → 204, then A3 → 401, A2 → 200, B1 → 200;
                   DELETE /sessions/abc → 404
                 - if RequireHeader lets a write through, TestRequireHeader fails: POST without
                   X-Requested-With → 403 and next not called; POST with X-Requested-With: XMLHttpRequest →
                   403; POST/DELETE with adguard-reward → next called; GET, HEAD and OPTIONS without the
                   header → next called
                 - if NoStore is dropped, TestNoStore fails: a wrapped 200 handler and a wrapped
                   RequireSession 401 both respond with Cache-Control: no-store

Wave 3 (depends t-2, t-3, t-5)
  t-6  Login handler, login log line, API routes
       files:    internal/auth/login.go, internal/auth/routes.go, internal/auth/login_test.go, internal/auth/session.go
       covers:   c-1, c-2, c-3, c-4, c-5, c-8, c-10, c-11
       depends:  t-2, t-3, t-5
       desc:     Config gains AdGuard Loginer (interface{ Login(ctx, user, pass string) error },
                 satisfied by *adguard.Client), Limiter *Limiter, TrustedProxies []netip.Prefix. Login
                 handler: strict-decode {username, password} through a 1 KiB LimitReader (malformed → 400,
                 no log line); ip := ClientIP(r, trusted); Limiter.Allow(ip) !ok → 429 {"error":"rate
                 limited"} with Retry-After = ceil(seconds), minimum 1, and AdGuard is never called;
                 AdGuard.Login: nil → issue cookie, 204; errors.Is(ErrBadCredentials) → Limiter.Fail(ip),
                 401 {"error":"invalid credentials"}; any other error → 502 {"error":"adguard
                 unreachable"}. Every attempt that reaches the outcome switch emits exactly one
                 Log.Info("login", "event","login", "username",u, "ip",ip.String(), "outcome",
                 ok|bad_credentials|adguard_unreachable|rate_limited); the password is never an attr.
                 (s *Service) Routes() http.Handler: a ServeMux with POST /api/v1/login (open), POST
                 /api/v1/logout, GET /api/v1/me, GET /api/v1/sessions, DELETE /api/v1/sessions/{id}, POST
                 /api/v1/sessions/revoke-all (each RequireSession-wrapped) and a catch-all "/api/v1/" →
                 RequireSession(http.NotFoundHandler()) so unknown paths are 401 before 404; the whole mux
                 is wrapped NoStore(RequireHeader(mux)). Tests drive Routes() against adguardtest.New with a
                 real adguard.Client and a temp store.
       contract: - if login stops forwarding the submitted credentials, TestLogin_OK fails: POST login with
                   the fake's creds → 204 with a session Set-Cookie; fake.Requests() holds exactly one POST
                   /control/login whose body decodes to {name: username, password: password} with no
                   Authorization header; GET /me with the cookie → 200 with that username
                 - if the three outcomes are conflated, TestLogin_Outcomes fails: wrong password → 401 body
                   {"error":"invalid credentials"} with no Set-Cookie; SetStatus(/control/login, 503) → 502
                   body {"error":"adguard unreachable"}; a fake closed before the request → 502; the 401
                   and 502 bodies differ byte-for-byte
                 - if the limiter is bypassed or AdGuard is still called while limited,
                   TestLogin_RateLimited fails: 5 wrong-password posts from RemoteAddr 203.0.113.5:1 → five
                   401s; the 6th → 429 with Retry-After: 60 and no Set-Cookie; three more attempts
                   (including one with the correct password) → 429 and fake /control/login count stays 5;
                   the correct password from 203.0.113.6:1 → 204
                 - if successes consume budget, TestLogin_SuccessFree fails: 4 wrong then 50 correct logins
                   from one IP → all 50 are 204; the next wrong → 401; the one after → 429
                 - if escalation, reset or Retry-After drift, TestLogin_Escalation fails (one fake clock
                   injected into Limiter and Service): trip → Retry-After 60; advance 60s, 5 wrong →
                   Retry-After 120; advance 120s, 5 wrong → 240; advance 1h+1s idle, 5 wrong → 60; a
                   lockout with 500ms left reports Retry-After 1, never 0
                 - if the global cap is dropped or escalates, TestLogin_Global fails: one wrong login from
                   each of 20 distinct RemoteAddrs → a 21st address's first attempt is 429 with
                   1 <= Retry-After <= 60 and fake login count == 20; advance 61s → that attempt reaches
                   AdGuard (401) and count == 21
                 - if the structured log line breaks or leaks the password, TestLogin_Log fails: slog
                   JSONHandler into a buffer; one attempt per outcome (ok, bad_credentials via wrong
                   password pw-CANARY-1, adguard_unreachable via SetStatus 503, rate_limited after 5
                   failures) → for each, exactly one record with event == "login", username == the
                   submitted name, ip == "203.0.113.5", outcome == the expected value; the buffer never
                   contains pw-CANARY-1 or the correct password; a malformed body → 400 and zero login
                   records
                 - if the raw token reaches a log, TestLog_NoToken fails: logger at Debug across login
                   (204) → GET /me with the cookie → POST /logout; the buffer contains "login" (positive
                   control) and contains neither the cookie value nor its hex SHA-256
                 - if the route table opens a hole, TestRoutes_Unauthenticated fails: with X-Requested-With
                   but no cookie, GET /api/v1/me, GET /api/v1/sessions, DELETE /api/v1/sessions/1, POST
                   /api/v1/sessions/revoke-all, POST /api/v1/logout and GET /api/v1/does-not-exist all →
                   401 with Cache-Control: no-store; POST /api/v1/login with correct creds → 204
                 - if the CSRF gate runs after a handler, TestRoutes_Header fails: POST /api/v1/login with
                   correct creds and no X-Requested-With → 403, fake login count 0, no Set-Cookie; the same
                   with the header → 204; POST /api/v1/logout with a valid cookie and no header → 403 and
                   GET /me with that cookie afterwards → 200 (the session was not touched); GET /me with a
                   cookie and no header → 200
                 - if no-store is missing from the assembled handler, TestRoutes_NoStore fails: GET /me with
                   a cookie (200), GET /me without (401) and POST /login (204) all carry Cache-Control:
                   no-store

Wave 4 (depends t-6)
  t-7  Wire store, auth routes and TLS in main
       files:    cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go
       covers:   c-1, c-2, c-3, c-4, c-7
       depends:  t-6
       desc:     After the startup probe: tlsOn := cfg.TLS.Cert != "" || cfg.TLS.Key != "" — exactly one
                 set → Error log "tls.cert and tls.key must both be set", return 1;
                 os.MkdirAll(cfg.DataDir, 0o700) then store.Open(filepath.Join(cfg.DataDir,
                 "adguard-reward.db")), Error log naming data_dir and return 1 on failure, Close on exit;
                 svc := auth.New(auth.Config{Store, AdGuard: client, Limiter: auth.NewLimiter(time.Now),
                 Log: log, Secure: tlsOn, TrustedProxies: cfg.TrustedPrefixes(), Now: time.Now});
                 mux.Handle("/api/v1/", svc.Routes()); srv.ServeTLS(ln, cert, key) when tlsOn, srv.Serve
                 otherwise. /healthz stays outside the auth mux. main_test's writeConfig helper adds
                 data_dir: <t.TempDir()> so tests never write ./data; the fake login creds are the
                 fake's Options.User/Pass.
       contract: - if the store is not opened under data_dir, TestRun_DataDir fails: after start with
                   data_dir=<tmp>/nested, <tmp>/nested/adguard-reward.db exists; a data_dir pointing at a
                   regular file makes run return 1 with stderr containing data_dir
                 - if the API is not mounted end-to-end, TestRun_LoginFlow fails: POST /api/v1/login with
                   X-Requested-With and the fake's creds → 204 with a session cookie; GET /api/v1/me with
                   it → 200 username; without it → 401; GET /healthz without any cookie or header → 200
                 - if sessions stop surviving a restart, TestRun_SessionSurvivesRestart fails: login, stop
                   run, start again on the same data_dir, GET /api/v1/me with the old cookie → 200
                 - if Secure does not follow TLS or the TLS listener is not wired, TestRun_TLS fails: a
                   self-signed cert/key pair generated in the test, tls.cert/tls.key set → run listens and
                   an https client with InsecureSkipVerify gets a 204 login whose Set-Cookie has Secure;
                   the plain-http config's cookie lacks Secure; tls.cert set without tls.key → run returns 1
                   with stderr containing tls.key
                 - if trusted_proxies is not plumbed through, TestRun_TrustedProxies fails: with
                   trusted_proxies [127.0.0.0/8] and X-Forwarded-For: 203.0.113.7, five wrong logins → the
                   6th is 429 and the login log lines carry ip=203.0.113.7; without trusted_proxies the
                   same header leaves ip=127.0.0.1 in the log lines
                 - if a login attempt leaks a secret through stderr, TestRun_NoSecretsInLogs (extended)
                   fails: ADGUARD_REWARD_LOG_LEVEL=debug, one wrong login with password pw-CANARY-2 and one
                   correct login → stderr contains event=login (positive control) and contains none of
                   pw-CANARY-2, the fake's password, the cookie value or base64(svc:pw-CANARY-9f3a)
```

## Coverage

| criterion | tasks |
|---|---|
| c-1  | t-6, t-7 |
| c-2  | t-1, t-5, t-6, t-7 |
| c-3  | t-1, t-5, t-6, t-7 |
| c-4  | t-2, t-3, t-6, t-7 |
| c-5  | t-5, t-6 |
| c-6  | t-5 |
| c-7  | t-4, t-7 |
| c-8  | t-6 |
| c-9  | t-1, t-5 |
| c-10 | t-5, t-6 |
| c-11 | t-2, t-6 |

Locked decisions: session_storage → t-1, t-7 (restart test); session_lifetime → t-1, t-5;
rate_limit_thresholds → t-2, t-6; client_ip_source → t-3, t-7.

## Judgment calls

- **One `internal/auth` package for session, limiter, client IP, handlers and routes; no `internal/api` or `internal/httpx`.** Rejected a separate ratelimit/middleware package: nothing outside auth consumes them this milestone, and a package boundary with one importer is structure no criterion asks for.
- **Store methods take `now time.Time`; no clock option on the store.** Rejected a `WithClock` option: the fake-clock tests (c-11, sliding expiry) only need the caller to pass a time, and the Service already owns `Now`.
- **`*store.Store` concrete in `auth.Config`, no Store interface.** Tests use a real temp SQLite file (fast, pure Go). An interface plus a fake would be a second implementation to keep in sync for zero criteria.
- **`AdGuard` is a one-method `Loginer` interface but the tests still use the real `adguard.Client` against `adguardtest`.** c-4 demands "the fake records zero /control/login hits", so a stub Loginer cannot prove it; the interface exists only so main can pass the client without an import cycle in tests.
- **Sessions `id` is `INTEGER PRIMARY KEY`, exposed as a number.** Rejected a random opaque id: revoke is scoped by `(username, id)` so guessing an id gains nothing, and a second random value per row is speculative.
- **Sliding expiry writes on every authenticated request (Touch = one UPDATE).** Rejected throttling the write to once per N minutes: a write per request is the smallest correct implementation of "pushed out on every authenticated request", and the load is a household of phones.
- **Malformed login body → 400 with no login log line; empty credentials are forwarded to AdGuard like any other.** c-8's outcome enum is closed at four values; a non-JSON body is not an attempt. Rejected special-casing empty username/password client-side: c-1 says "calls AdGuard with the submitted credentials" and AdGuard's rejection then consumes budget naturally.
- **AdGuard's own 429 (`adguard.ErrRateLimited`) maps to 502/adguard_unreachable.** It is not a bad credential and c-1 only names two failure buckets; a fifth outcome would extend a closed enum.
- **Unknown `/api/v1/*` paths get 401 before 404.** A catch-all `RequireSession(NotFound)` is one line and is the only way "every /api/v1/* route except login" is true for paths that do not exist yet (phase 03 routes).
- **No SPA embedding in the binary this phase; `make dev-web` proxies `/api` and `/healthz` to the Go API.** c-7 is satisfied through the Vite dev server. Embedding (`embed.FS` + SPA fallback) is a stack lock but not a criterion here; it lands with the first phase whose criterion needs a built binary to serve pages.
- **No client-side router; `App.svelte` holds a three-state `route` and calls `history.replaceState`.** Two views do not justify a dependency; c-7 only needs "sends any 401 to /login" and "lands on a placeholder home".
- **Frontend tests via `node --test` on Node 24 type stripping, not vitest.** One `api.ts` module with pure functions (header injection, 401 hook, message mapping, credential POST) is fully testable with `node:test` plus a stubbed `globalThis.fetch`; the only edits are a `test` script, `"node"` in `tsconfig.app.json` types, and a Makefile line. The two `.svelte` files are gated by `svelte-check` only — they contain no logic beyond wiring.
- **TLS listener wired in t-7 (ServeTLS when both cert/key set) rather than left "for a later phase".** c-2 says Secure "when the app serves TLS"; without a TLS listener the Secure flag could never be true and the criterion would be vacuous. It is ~10 lines plus a self-signed pair in the test.
- **t-5/t-6 kept as two sequential tasks rather than one 9-criterion task.** Merging would not gain parallelism (login needs `issue`) and would push one package over the 5-file limit; splitting at "what needs the limiter and AdGuard" vs "what needs only the store" keeps each task's test file readable.
- **t-4 touches 7 files but one layer.** package.json, tsconfig.app.json, Makefile are one-line mechanical edits; the substantive files are four. Splitting would create a task with no criterion of its own.
