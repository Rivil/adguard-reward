# Verification-lens plan — 02-auth-sessions

Method: for each criterion the ideal test was written first (what would fail, on what
surface, observable how), then the smallest task that makes that test satisfiable.
Real store + real fake AdGuard everywhere; fake clocks are injected `Now func()` /
explicit `now time.Time` args, never `time.Sleep`. Every task contract names the test.

```
Phase 02-auth-sessions — 9 tasks across 4 waves

Wave 1
  t-1  Found internal/store: SQLite, migrations, sessions
       files:    internal/store/store.go, internal/store/migrate.go,
                 internal/store/migrations/0001_sessions.sql, internal/store/sessions.go,
                 internal/store/store_test.go, internal/store/sessions_test.go, go.mod, go.sum
       covers:   c-2, c-3, c-9 (locked: session_storage, session_lifetime)
       depends:  —
       desc:     go get modernc.org/sqlite. store.Open(path) (*Store, error): MkdirAll the parent,
                 DSN with _pragma=journal_mode(WAL), busy_timeout(5000), foreign_keys(1);
                 SetMaxOpenConns(1) so writes never race on the file. Migrations are go:embed'd
                 SQL under migrations/NNNN_*.sql, applied in lexical order inside one tx each,
                 recorded in schema_migrations(version INTEGER PRIMARY KEY, applied_at);
                 Store.SchemaVersion() returns the highest applied. 0001 creates
                 sessions(id INTEGER PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE, username TEXT
                 NOT NULL, created_at, last_seen_at, expires_at INTEGER NOT NULL /* unix secs */)
                 plus an index on (username). Session methods take the hash, never the token:
                 CreateSession(ctx, hash, username, now, ttl) (Session, error);
                 SessionByHash(ctx, hash, now) (Session, error) -> ErrNotFound when absent OR
                 expires_at <= now; TouchSession(ctx, id, now, ttl) sets last_seen_at=now,
                 expires_at=now+ttl; DeleteSession(ctx, id) (deleted bool, err);
                 ListSessions(ctx, username, now) []Session ordered created_at ASC, expired
                 rows excluded; DeleteSessionsExcept(ctx, username, keepID) (n int, err);
                 DeleteExpired(ctx, now) (n int, err). Session{ID int64, Username string,
                 CreatedAt, LastSeenAt, ExpiresAt time.Time}. Close() closes the DB.
       contract: - if migrations stop being idempotent, TestOpen_Migrates fails: Open on a fresh
                   temp path gives SchemaVersion()==1 and sqlite_master lists sessions and
                   schema_migrations; Close then Open on the same path succeeds with
                   SchemaVersion() still 1 and no duplicate schema_migrations row
                 - if Open stops creating the parent directory, TestOpen_MissingDir fails:
                   Open(t.TempDir()/nested/x/app.db) succeeds and the file exists
                 - if a migration is skipped or applied out of order, TestMigrate_Order fails:
                   an embedded-FS stub with 0002 before 0001 in insertion order still lands 0001
                   first (schema_migrations rows are 1,2); a migration that fails mid-way leaves
                   SchemaVersion() unchanged (tx rollback) and Open returns the SQL error naming
                   the file
                 - if the store starts accepting or returning raw tokens, TestSessions_HashOnly
                   fails: CreateSession(hash "h1") then a raw `SELECT * FROM sessions` row has
                   token_hash=="h1" and no column contains anything but h1/username/integers
                   (compile-time: the API has no token parameter)
                 - if expiry is not enforced at read time, TestSessions_Expiry fails: session
                   created at T with ttl 30d is found by SessionByHash at T+30d-1s and
                   returns ErrNotFound at T+30d exactly; TouchSession at T+29d then lookup at
                   T+58d finds it (sliding) and at T+59d does not
                 - if the unique constraint is dropped, TestSessions_UniqueHash fails: two
                   CreateSession calls with the same hash — the second errors, and
                   ListSessions has one row
                 - if scoped deletes leak across users, TestSessions_RevokeScoping fails:
                   alice has 3 sessions, bob has 2; DeleteSessionsExcept("alice", keep=alice#2)
                   returns 2, alice lists exactly #2, bob still lists 2; DeleteSession(bob#1)
                   returns true then false on repeat; DeleteExpired(now) removes only rows with
                   expires_at <= now
                 - if the store is not safe under -race with concurrent callers,
                   TestSessions_Concurrent fails: 20 goroutines each CreateSession+
                   SessionByHash+DeleteSession against one Store complete with no error and no
                   SQLITE_BUSY

  t-2  Rate limiter: failure budget, escalating per-IP lockout
       files:    internal/ratelimit/limiter.go, internal/ratelimit/limiter_test.go
       covers:   c-4, c-11 (locked: rate_limit_thresholds)
       depends:  —
       desc:     ratelimit.New(cfg Config, now func() time.Time) *Limiter with
                 Config{PerIPFailures: 5, GlobalFailures: 20, Window: 1m, BaseLockout: 1m,
                 MaxLockout: 1h, ResetAfter: 1h} and Defaults(). Check(ip netip.Addr)
                 (retryAfter time.Duration, allowed bool): per-IP lockout active -> (remaining,
                 false); else global sliding-window count >= GlobalFailures -> (time until the
                 oldest failure leaves the window, false); else (0, true). RecordFailure(ip):
                 appends now to the IP's and the global failure rings (older than Window pruned);
                 when the IP's count reaches PerIPFailures it trips: failures cleared,
                 lockoutUntil = now + min(BaseLockout<<level, MaxLockout), level++, lastTrip=now.
                 On every Check/RecordFailure, if now-lastTrip >= ResetAfter the level resets to 0.
                 Only callers decide what is a failure (bad credentials only); the limiter has no
                 success or rate-limited-rejection method, so those cannot consume budget by
                 construction. Global escalation does not exist (flat, per decision). Idle
                 entries (no lockout, no failures in Window, level 0) are evicted on the next
                 RecordFailure sweep; Len() reports tracked IPs. Mutex-guarded.
       contract: - if the per-IP threshold moves, TestLimiter_PerIP fails: 4 RecordFailure
                   calls for 10.0.0.1 leave Check allowed; the 5th makes Check return
                   allowed=false with retryAfter==1m; 10.0.0.2 is still allowed
                 - if the escalation or cap breaks, TestLimiter_Escalation fails (fake clock):
                   trip #1 -> retryAfter 1m; advance 61s, 5 failures -> trip #2 -> 2m; repeat ->
                   4m, 8m, 16m, 32m, 1h, 1h (capped, never 2h); Retry-After mid-window equals
                   the remaining time (advance 30s into a 2m lockout -> 1m30s)
                 - if the reset is missing or too eager, TestLimiter_Reset fails (reset is measured
                   from the last trip, not from lockout expiry): after trip #3 (4m lockout)
                   at time L, advance to L+59m59s and trip again -> 8m (no reset); in a fresh
                   limiter reach a 4m lockout at L, advance to L+1h and trip -> 1m (reset)
                 - if the global cap is missing or escalates, TestLimiter_Global fails: 20
                   failures spread over 5 IPs (4 each, none tripped) make Check(fresh IP)
                   return allowed=false; advance 61s -> allowed; do it again -> still exactly
                   Window-based retryAfter (<=1m), never 2m
                 - if rate-limited or successful attempts consume budget, TestLimiter_BudgetIsFailuresOnly
                   fails: 100 Check calls with no RecordFailure never block; 4 failures then 50
                   Check calls still allowed (compile-level: no Success/Rejected method exists)
                 - if idle IPs are never evicted, TestLimiter_Evict fails: 1000 IPs with one
                   failure each, advance 2h, one RecordFailure on a new IP -> Len()==1
                 - if the mutex is removed, TestLimiter_Race fails under -race: 50 goroutines
                   interleaving Check/RecordFailure on 3 IPs

  t-3  trusted_proxies config key and client-IP resolver
       files:    internal/config/config.go, internal/config/config_test.go,
                 internal/config/testdata/full.yaml, internal/clientip/clientip.go,
                 internal/clientip/clientip_test.go
       covers:   c-4, c-8 (locked: client_ip_source)
       depends:  —
       desc:     Config gains TrustedProxies []string `yaml:"trusted_proxies"`; envTable row
                 ADGUARD_REWARD_TRUSTED_PROXIES splits on "," and trims (empty string -> nil).
                 validate() parses each with netip.ParsePrefix (a bare IP is accepted as /32 or
                 /128); a bad entry errors naming trusted_proxies and the offending value.
                 Config.TrustedProxyPrefixes() []netip.Prefix re-parses the validated list.
                 TestEnvTable's want map and its setter round-trip (which sets every var to
                 "v:<key>") gain TRUSTED_PROXIES overridden to "" so validation passes.
                 clientip.From(r *http.Request, trusted []netip.Prefix) netip.Addr: peer =
                 host of r.RemoteAddr (unmapped 4-in-6); if trusted is empty or the peer is not
                 in any prefix -> peer, X-Forwarded-For ignored entirely; else walk the
                 comma-joined XFF hops right-to-left (all XFF header values concatenated in
                 order), skipping hops inside trusted, returning the first untrusted hop; if
                 every hop is trusted or XFF is absent/unparseable -> peer. Unparseable
                 RemoteAddr -> netip.Addr{} (zero) so the caller can still log "ip=invalid".
       contract: - if trusted_proxies loses its env row or parsing, TestEnvTable fails (the
                   want map now contains "trusted_proxies": "ADGUARD_REWARD_TRUSTED_PROXIES" and
                   the leaf walk finds the new field) and TestLoad_TrustedProxies fails: yaml
                   ["10.0.0.0/8", "::1/128"] loads to two prefixes; env "10.0.0.0/8, 192.168.1.1"
                   overrides the yaml list to [10.0.0.0/8, 192.168.1.1/32]; "10.0.0.0/33"
                   errors naming trusted_proxies and 10.0.0.0/33; an empty env value yields nil
                 - if XFF is honoured for an untrusted peer, TestFrom_UntrustedPeerIgnoresXFF
                   fails: RemoteAddr 203.0.113.9:1234 with X-Forwarded-For: 1.2.3.4 and trusted
                   [10.0.0.0/8] returns 203.0.113.9; with trusted nil returns 203.0.113.9
                 - if the last-untrusted-hop rule breaks, TestFrom_TrustedPeer fails: peer
                   10.0.0.2 trusted, XFF "1.2.3.4, 5.6.7.8, 10.0.0.3" -> 5.6.7.8 (10.0.0.3
                   skipped as trusted); XFF "1.2.3.4" -> 1.2.3.4; XFF "10.0.0.5" (all trusted)
                   -> 10.0.0.2; two XFF header lines "1.2.3.4" and "5.6.7.8" -> 5.6.7.8; XFF
                   "garbage" -> 10.0.0.2
                 - if IPv6 handling breaks, TestFrom_V6 fails: RemoteAddr "[::ffff:10.0.0.2]:80"
                   matches trusted 10.0.0.0/8; RemoteAddr "[2001:db8::1]:80" untrusted returns
                   2001:db8::1; RemoteAddr "nonsense" returns a zero Addr with no panic

  t-4  Web API layer with vitest: fetch wrapper, messages, routing
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts, web/src/lib/router.ts,
                 web/src/lib/router.test.ts, web/package.json, web/pnpm-lock.yaml,
                 web/vite.config.ts, web/tsconfig.app.json, Makefile
       covers:   c-5, c-7
       depends:  —
       desc:     Add vitest (devDependency) and "test": "vitest run"; vite.config.ts gets
                 test.environment "node" (no jsdom: every tested module is DOM-free);
                 tsconfig.app.json types gains "vitest/globals" or tests import from vitest.
                 Makefile: test-web (cd web && pnpm test), test-go (go test -race ./...), and
                 test depends on both. api.ts: createApi({fetch, onUnauthorized}) returns
                 {request(path, init) -> Promise<Response>} that always sends
                 X-Requested-With: adguard-reward, credentials "same-origin",
                 Content-Type application/json when a body is given; on a 401 from any path
                 other than /api/v1/login it calls onUnauthorized() exactly once per response
                 and still returns the Response. Exported LOGIN_MESSAGES: {401: "Wrong username
                 or password", 502: "Can't reach AdGuard Home — is it running?", 429: "Too many
                 attempts, try again in {retryAfter}s", default: "Login failed (HTTP {status})"}
                 and loginErrorMessage(status, retryAfter?) returning the filled string.
                 router.ts: routeFor(path, authed: boolean) -> "/login" when !authed (any path),
                 "/" when authed and path is "/login", else path; plus navigate(path) wrapping
                 history.pushState and a currentPath() reading location (untested, DOM-only).
       contract: - if the header is dropped from the wrapper, TestApi_Header (api.test.ts
                   "sends X-Requested-With on every method") fails: a stubbed fetch records the
                   Request for GET and POST and both carry X-Requested-With: adguard-reward and
                   credentials same-origin
                 - if the 401 redirect hook breaks or fires on login, api.test.ts "401 calls
                   onUnauthorized once, never for /api/v1/login" fails: stubbed 401 on
                   /api/v1/me -> hook called once and the returned Response has status 401;
                   stubbed 401 on /api/v1/login -> hook not called; stubbed 200 -> not called
                 - if wrong-password and unreachable messages converge, api.test.ts "messages
                   are distinct per status" fails: loginErrorMessage(401) !==
                   loginErrorMessage(502), both non-empty, neither equal to
                   loginErrorMessage(500); loginErrorMessage(429, 90) contains "90"
                 - if the SPA stops sending 401s to /login or stops landing logged-in parents
                   on home, router.test.ts fails: routeFor("/", false)==="/login",
                   routeFor("/anything", false)==="/login", routeFor("/login", true)==="/",
                   routeFor("/", true)==="/"
                 - if the web test suite is unplugged from the gate, `make test` fails: the
                   test target lists test-web, and a deliberately failing assertion in
                   api.test.ts makes `make test` exit non-zero (checked once during the task,
                   then reverted)

Wave 2 (depends t-1 / t-4)
  t-5  Session manager, cookie, and API middlewares
       files:    internal/auth/session.go, internal/auth/middleware.go,
                 internal/auth/session_test.go, internal/auth/middleware_test.go
       covers:   c-2, c-3, c-5, c-6, c-10 (locked: session_lifetime)
       depends:  t-1
       desc:     auth.NewManager(st *store.Store, opts ...ManagerOption) *Manager with
                 WithSecure(bool), WithNow(func() time.Time), WithTTL (default 30*24h).
                 const CookieName = "adguard_reward_session". Issue(ctx, username) (raw string,
                 store.Session, error): 32 bytes from crypto/rand, raw = base64.RawURLEncoding,
                 hash = hex(sha256(raw)); store.CreateSession(hash,...). Cookie(raw)
                 *http.Cookie{Name, Value: raw, Path: "/", HttpOnly: true, SameSite: Strict,
                 Secure: secure, MaxAge: int(ttl.Seconds())}; ClearCookie() same attrs with
                 MaxAge -1 and empty value. Resolve(ctx, r) (store.Session, error): reads the
                 cookie, hashes, SessionByHash(now), TouchSession(now, ttl) and returns the
                 touched session; no cookie / unknown / expired -> ErrNoSession. Revoke(ctx, r)
                 deletes the session named by the request cookie. SessionFrom(ctx)
                 (store.Session, bool) reads the value RequireSession stored.
                 middleware.go: NoStore(next) sets Cache-Control: no-store before calling next;
                 RequireHeader(next) rejects any method other than GET/HEAD/OPTIONS lacking
                 X-Requested-With: adguard-reward with 403 {"error":"missing_header"} without
                 calling next; RequireSession(m *Manager)(next) resolves, on ErrNoSession writes
                 401 {"error":"unauthorized"} plus ClearCookie() when a cookie was present, else
                 re-issues Cookie(raw) (fresh Max-Age, sliding) and calls next with the session
                 in ctx. All error bodies are application/json.
       contract: - if the token loses entropy or its hash stops being what is stored,
                   TestIssue_TokenAndHash fails: raw decodes (RawURLEncoding) to exactly 32
                   bytes; 100 Issues yield 100 distinct raws; the sessions row for the returned
                   ID has token_hash == hex(sha256(raw)) and a `SELECT count(*) FROM sessions
                   WHERE token_hash = ?` with the raw value returns 0
                 - if a cookie attribute regresses, TestCookie_Attributes fails: Cookie(raw)
                   serialised via a ResponseRecorder has HttpOnly, SameSite=Strict, Path=/,
                   Max-Age=2592000 and no Secure with WithSecure(false); has Secure with
                   WithSecure(true); ClearCookie() has Max-Age<0 and empty Value
                 - if sliding expiry breaks at the manager level, TestResolve_Sliding fails
                   (WithNow fake clock): Issue at T; Resolve at T+29d succeeds and the row's
                   expires_at becomes T+59d; Resolve at T+59d+1s -> ErrNoSession; a session
                   never touched -> ErrNoSession at T+30d
                 - if unauthenticated requests slip through, TestRequireSession_401 fails: a
                   spy next handler behind RequireSession sees zero calls for (no cookie),
                   (cookie with random value), (cookie from a session deleted via Revoke), and
                   all three get 401 application/json; the deleted-session response carries a
                   Set-Cookie clearing the cookie; a valid cookie reaches next once and
                   SessionFrom(r.Context()) inside next returns the issued username, and the
                   response carries a refreshed Set-Cookie with Max-Age=2592000
                 - if the header gate weakens or runs handlers, TestRequireHeader fails: spy
                   next behind RequireHeader records zero calls and 403 for POST, PUT, PATCH,
                   DELETE without the header; records one call each for GET, HEAD, OPTIONS
                   without the header and for POST with "X-Requested-With: adguard-reward";
                   the header value "AdGuard-Reward" (wrong case in the value) is 403
                 - if no-store is lost, TestNoStore fails: NoStore(next) sets Cache-Control:
                   no-store on a 200 from next and on a 401 written by RequireSession nested
                   inside it, and the header is present even when next writes nothing
                 - if Revoke deletes the wrong row, TestRevoke fails: two issued sessions,
                   Revoke with cookie A -> Resolve(A) ErrNoSession, Resolve(B) still succeeds

  t-6  Svelte login page, placeholder home, 401 routing
       files:    web/src/App.svelte, web/src/lib/Login.svelte, web/src/lib/Home.svelte,
                 web/src/lib/Counter.svelte (delete), web/src/main.ts, web/src/app.css
       covers:   c-7
       depends:  t-4
       desc:     App.svelte: $state path from currentPath(); on mount request /api/v1/me
                 (via createApi with onUnauthorized -> navigate(routeFor(path,false)));
                 200 -> me = {username, expires_at}, navigate(routeFor(path, true)); listens to
                 popstate. Renders Login when path is /login else Home; a "loading" view until
                 /me answers. Login.svelte: username + password inputs, submit posts
                 /api/v1/login, 204 -> navigate("/") after re-fetching /me, otherwise shows
                 loginErrorMessage(status, Retry-After) in a role="alert" element; button
                 disabled while in flight. Home.svelte: "Signed in as {username}", the
                 session expiry, and a Logout button posting /api/v1/logout then
                 navigate("/login"). Remove the Vite scaffold (Counter, hero, ticks CSS).
                 Assets svelte.svg/vite.svg/hero.png may stay unreferenced or be deleted.
       contract: - if a component stops type-checking or references a removed export,
                   `pnpm check` (make typecheck) fails: svelte-check reports 0 errors across
                   App.svelte, Login.svelte, Home.svelte with Counter.svelte gone
                 - if Login.svelte hardcodes its own strings instead of the tested map,
                   TestLoginUsesMessages (a vitest in api.test.ts reading
                   web/src/lib/Login.svelte as text) fails: the source contains
                   "loginErrorMessage(" and contains neither literal "Wrong username" nor
                   "AdGuard Home" — the only source of those strings is LOGIN_MESSAGES
                 - if the SPA stops routing 401s, router.test.ts still fails (t-4) and a source
                   assertion in router.test.ts fails: App.svelte contains "onUnauthorized" and
                   "routeFor(" (the hook is wired, not re-implemented)
                 - manual gate recorded in the task commit body: `make dev` + `make dev-web`
                   against a fake-less AdGuard: wrong password shows the 401 message,
                   AdGuard stopped shows the 502 message, correct login lands on Home showing
                   the username (no DOM test runner in this phase — see judgment calls)

Wave 3 (depends t-2, t-3, t-5)
  t-7  Login, logout, me handlers; router; login log
       files:    internal/auth/handlers.go, internal/auth/router.go,
                 internal/auth/handlers_test.go, internal/auth/router_test.go
       covers:   c-1, c-2, c-3, c-4, c-5, c-6, c-8, c-10
       depends:  t-2, t-3, t-5
       desc:     type LoginClient interface{ Login(ctx, user, pass string) error } (satisfied by
                 *adguard.Client). auth.NewHandlers(ag LoginClient, m *Manager, rl
                 *ratelimit.Limiter, trusted []netip.Prefix, log *slog.Logger) *Handlers.
                 Login: body via http.MaxBytesReader 4 KiB, strict JSON {username, password};
                 malformed/empty -> 400 without touching AdGuard or the limiter; ip :=
                 clientip.From; rl.Check(ip) blocked -> 429, Retry-After: ceil(seconds),
                 {"error":"rate_limited"}; ag.Login: nil -> Issue + Set-Cookie + 204;
                 errors.Is(ErrBadCredentials) -> rl.RecordFailure(ip) + 401
                 {"error":"invalid_credentials"}; any other error (transport, 5xx,
                 ErrRateLimited from AdGuard) -> 502 {"error":"adguard_unreachable"}. Exactly
                 one log.Info("login", "event","login", "username",u, "ip",ip, "outcome",
                 ok|bad_credentials|adguard_unreachable|rate_limited) per attempt; the 400 path
                 logs nothing (no attempt was made). Debug lines may name the session ID and
                 username, never the raw token or its hash prefix. Logout (POST, behind
                 RequireSession): Revoke + ClearCookie + 204. Me (GET): 200 {"username",
                 "expires_at" RFC3339}. router.go: Router(h *Handlers, m *Manager)
                 http.Handler = NoStore(RequireHeader(mux)) where mux has
                 "POST /api/v1/login" -> h.Login and "/api/v1/" -> RequireSession(m)(protected)
                 with protected mux holding "POST /api/v1/logout", "GET /api/v1/me" (t-8 adds
                 its routes to protected); so an unknown /api/v1/x is 401 without a session and
                 404 with one. Handlers export the protected mux registration via
                 (h *Handlers) register(protected *http.ServeMux) so t-8 extends one place.
       contract: - if the login round-trip breaks, TestLogin_OK fails: fake AdGuard with
                   Options{User:"mum",Pass:"pw"}; POST /api/v1/login {"username":"mum",
                   "password":"pw"} with the header -> 204, Set-Cookie named
                   adguard_reward_session with HttpOnly/SameSite=Strict/Path=/; the fake's
                   recorded /control/login body decodes to {name: mum, password: pw} with no
                   Authorization header
                 - if bad credentials leak detail or are confused with outages,
                   TestLogin_Outcomes fails: wrong password -> 401 body exactly
                   {"error":"invalid_credentials"} and no Set-Cookie; SetStatus(/control/login,
                   503) -> 502 body {"error":"adguard_unreachable"}; fake Close()d -> 502 same
                   body; SetStatus(/control/login, 429) -> 502 (not 401); the 401 and 502
                   bodies differ
                 - if the limiter is bypassed or AdGuard is still called while limited,
                   TestLogin_RateLimited fails: 5 wrong-password posts from RemoteAddr
                   10.0.0.1:1 -> five 401s; the 6th -> 429 with Retry-After "60" and
                   {"error":"rate_limited"}; len(fake.Requests()) for /control/login == 5
                   before and after the 6th; a post from 10.0.0.2:1 with the right password ->
                   204 (per-IP scoping); successes never count: 10 correct logins then 4 wrong
                   ones from one IP -> the 5th wrong is 401 not 429
                 - if the global limit is missing at the HTTP layer, TestLogin_GlobalLimit
                   fails: 20 wrong-password posts spread across 10 IPs (2 each) then a correct
                   login from an 11th IP -> 429 with Retry-After <= 60, fake login hits == 20
                 - if the escalation is not surfaced through Retry-After,
                   TestLogin_RetryAfterEscalates fails (Manager and Limiter share a fake
                   clock): trip #1 Retry-After "60"; advance 61s, trip #2 -> "120"; advance
                   30s -> "90"
                 - if trusted_proxies is ignored at the HTTP layer, TestLogin_ProxiedIP fails:
                   trusted [127.0.0.0/8], RemoteAddr 127.0.0.1, XFF "198.51.100.7": 5 failures
                   lock out XFF 198.51.100.7 but XFF 198.51.100.8 from the same peer is still
                   401 (not 429); with trusted nil, the same two XFF values share one budget
                   (the 6th is 429)
                 - if the structured login line is missing, wrong, or leaks the password,
                   TestLoginLog_PerOutcome fails: JSONHandler at Info into a buffer; one
                   attempt per outcome (ok, bad_credentials, adguard_unreachable,
                   rate_limited); decoding every line finds exactly one with event=="login"
                   per attempt, each with username=="mum", ip=="10.0.0.1", outcome==the
                   expected value; the whole buffer lacks "pw" and "wrongpw"; the 400
                   (malformed) path adds no event==login line
                 - if debug logging leaks the session token, TestLogs_NoRawToken fails:
                   TextHandler at Debug captured across login, GET /me, POST /logout; buffer
                   contains "login" and "/control/login" (positive control) and contains
                   neither the Set-Cookie value nor its hex(sha256) nor the first 8 chars of
                   either
                 - if logout does not revoke server-side, TestLogout_Replay fails: login ->
                   /me 200 -> logout 204 with Set-Cookie Max-Age<0 -> /me with the old cookie
                   401 -> sessions table count for the user == 0
                 - if /me drifts, TestMe fails: with a valid cookie GET /api/v1/me is 200 with
                   Content-Type application/json and body decoding to exactly {username:
                   "mum", expires_at: RFC3339 within 30d±1s of the fake clock}; without a
                   cookie 401; the response carries Cache-Control: no-store
                 - if the router lets unknown or unauthenticated routes through,
                   TestRouter_Fallthrough fails: GET /api/v1/does-not-exist without a cookie ->
                   401 (not 404); with a cookie -> 404; POST /api/v1/login without the
                   X-Requested-With header -> 403 and the fake recorded zero /control/login
                   hits; GET /healthz is not under Router (compile-level: Router only mounts
                   /api/v1/, asserted by a request for /healthz through Router returning 404)
                 - if no-store is missing from either branch, TestRouter_NoStore fails: the
                   authenticated /me 200 and the cookieless /me 401 both carry Cache-Control:
                   no-store, as does the 403 header rejection
                 - if the body bound is removed, TestLogin_BodyBound fails: a 1 MiB body -> 400
                   or 413 and zero /control/login hits

Wave 4 (depends t-7)
  t-8  Sessions list, revoke one, revoke all
       files:    internal/auth/sessions.go, internal/auth/sessions_test.go,
                 internal/auth/router.go
       covers:   c-9
       depends:  t-7
       desc:     ListSessions GET /api/v1/sessions -> 200 {"sessions": [{id, created_at,
                 last_seen_at, current}]} ordered by created_at, times RFC3339, current ==
                 (id == SessionFrom(ctx).ID); an empty list is [] not null. RevokeSession
                 DELETE /api/v1/sessions/{id} -> 204 when the id belongs to the caller's
                 username; 404 {"error":"not_found"} when absent or another user's (no
                 existence leak); revoking the current session is allowed and also clears the
                 cookie. RevokeAll POST /api/v1/sessions/revoke-all -> 200 {"revoked": n}
                 deleting every session of the caller's username except the current. All
                 registered in register(protected) so they sit behind NoStore/RequireHeader/
                 RequireSession.
       contract: - if the list shape or `current` breaks, TestSessions_List fails: log in three
                   times as mum (cookies A, B, C); GET with C returns 3 entries in created_at
                   order, exactly one current (C's id), every entry has id>0, created_at and
                   last_seen_at RFC3339; a fresh user with one session lists exactly one
                 - if revoke-one is unscoped or leaks existence, TestSessions_RevokeOne fails:
                   with C, DELETE sessions/{A.id} -> 204 and a request with A -> 401 while B
                   still works; DELETE the same id again -> 404; log in as dad (fake configured
                   via a second adguardtest server or SetAuth-independent login users — use a
                   second fake with Options{User:"dad"}) and DELETE mum's B.id -> 404 and B
                   still works; DELETE sessions/abc -> 404
                 - if revoke-all touches the caller or other users, TestSessions_RevokeAll fails:
                   mum A,B,C and dad D; POST revoke-all with C -> {"revoked": 2}; A and B -> 401
                   on their next request; C -> 200 on /me; D -> 200 on /me; a second
                   revoke-all -> {"revoked": 0}
                 - if the sessions routes escape the middleware chain,
                   TestSessions_Protected fails: GET /api/v1/sessions without a cookie -> 401;
                   DELETE without the header -> 403 with zero store changes (count before ==
                   after); every response carries Cache-Control: no-store
                 - if self-revoke leaves a dangling cookie, TestSessions_RevokeSelf fails:
                   DELETE sessions/{C.id} with C -> 204 with a clearing Set-Cookie, then /me
                   with C -> 401

  t-9  Wire main: store, auth router, TLS Secure
       files:    cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go,
                 deploy/config.example.yaml, deploy/docker-compose.yml
       covers:   c-1, c-2, c-3, c-10 (locked: session_storage restart survival)
       depends:  t-7
       desc:     After the startup probe: os.MkdirAll(cfg.DataDir) -> store.Open(filepath.Join
                 (cfg.DataDir, "adguard-reward.db")) (fatal, exit 1, error names data_dir and
                 the path); defer Close. secure := cfg.TLS.Cert != "" && cfg.TLS.Key != "";
                 auth.NewManager(st, WithSecure(secure)); ratelimit.New(ratelimit.Defaults(),
                 time.Now); auth.NewHandlers(client, mgr, rl, cfg.TrustedProxyPrefixes(), log);
                 mux.Handle("/api/v1/", auth.Router(h, mgr)); /healthz stays on the outer mux
                 untouched. Serve: if secure, srv.ServeTLS(ln, cfg.TLS.Cert, cfg.TLS.Key)
                 else srv.Serve(ln); the listening log line gains "tls": secure. Hourly
                 goroutine calling store.DeleteExpired(ctx, now) until ctx is done (logged at
                 Debug with the count). main_test writeConfig gains data_dir: t.TempDir()
                 so existing tests stop creating ./data beside the test binary; a
                 loginClient(t, addr) helper posts with the header and returns the cookie.
                 config.example.yaml gains trusted_proxies: [] with a comment on X-Forwarded-For;
                 docker-compose gains a data volume comment if absent (no key changes).
       contract: - if the API is not mounted or healthz is caught by it, TestRun_LoginFlow
                   fails: fake with Options{User:"mum",Pass:"pw"}; GET /healthz without a
                   cookie -> 200; POST /api/v1/login (header, right creds) -> 204 with the
                   cookie; GET /api/v1/me with it -> 200 username mum; POST /api/v1/logout
                   -> 204; GET /api/v1/me with the old cookie -> 401; GET /api/v1/nope without
                   a cookie -> 401; all /api/v1 responses carry Cache-Control: no-store
                 - if sessions stop surviving a restart, TestRun_SessionSurvivesRestart fails:
                   start, login, stop (exit 0); start again on the same config (same data_dir,
                   fresh listen port); GET /api/v1/me with the first run's cookie -> 200; the
                   db file exists at <data_dir>/adguard-reward.db
                 - if the data dir failure is swallowed, TestRun_DataDirUnwritable fails:
                   data_dir pointing at a path whose parent is a regular file -> run returns 1
                   and stderr contains data_dir and the path
                 - if Secure does not follow TLS, TestRun_TLSSecureCookie fails: the test
                   writes a self-signed cert/key (crypto/x509, 127.0.0.1 SAN) into the temp
                   dir and sets tls.cert/tls.key; onListen fires; an https client with
                   InsecureSkipVerify logs in and the Set-Cookie has Secure; stderr contains
                   tls=true; the plain-http run's Set-Cookie lacks Secure and its log says
                   tls=false
                 - if any log line leaks the token or password end-to-end,
                   TestRun_NoSecretsInLogs (extended) fails: ADGUARD_REWARD_LOG_LEVEL=debug,
                   login as mum with password pw-PARENT-CANARY, /me, logout; stderr contains
                   event=login and outcome=ok (positive control) and none of
                   pw-PARENT-CANARY, the cookie value, hex(sha256(cookie value)), the service
                   password pw-CANARY-9f3a, base64(svc:pw-CANARY-9f3a)
                 - if the login limit does not reach the wire, TestRun_RateLimited fails: 5
                   wrong-password logins then a 6th -> 429 with Retry-After "60"; fake
                   /control/login hits == 5 (loopback peer, no trusted_proxies)
                 - if the reaper is wired wrong, TestRun_ReaperLogs fails: probeInterval-style
                   package var reapInterval set to 10ms by the test; stderr at debug contains
                   "expired sessions" within 500ms and run still returns 0 on cancel
                 - if the example config drifts from the loader, TestExampleConfigLoads fails:
                   config.Load("deploy/config.example.yaml") with only the three required
                   ADGUARD_REWARD_ADGUARD_* env vars set succeeds and TrustedProxyPrefixes()
                   is empty
```

## Coverage

| Criterion | Tasks | Ideal test (what proves it) |
|---|---|---|
| c-1 login -> AdGuard, 204/401/502 | t-7, t-9 | TestLogin_OK, TestLogin_Outcomes (auth); TestRun_LoginFlow (main) |
| c-2 256-bit token, SHA-256 only, cookie attrs, Secure on TLS, no raw token in debug logs | t-1, t-5, t-7, t-9 | TestSessions_HashOnly, TestIssue_TokenAndHash, TestCookie_Attributes, TestLogs_NoRawToken, TestRun_TLSSecureCookie, TestRun_NoSecretsInLogs |
| c-3 401 everywhere but login, /healthz open, logout revokes | t-1, t-5, t-7, t-9 | TestRequireSession_401, TestRouter_Fallthrough, TestLogout_Replay, TestRun_LoginFlow |
| c-4 per-IP + global limit, 429 + Retry-After, zero AdGuard hits | t-2, t-3, t-7, t-9 | TestLimiter_PerIP/Global, TestLogin_RateLimited, TestLogin_GlobalLimit, TestLogin_ProxiedIP, TestRun_RateLimited |
| c-5 X-Requested-With gate before handlers | t-4, t-5, t-7, t-8 | TestRequireHeader (spy handler zero calls), TestRouter_Fallthrough, TestSessions_Protected, api.test.ts header assertion |
| c-6 /me shape | t-5, t-7 | TestMe |
| c-7 Svelte login page, distinct messages, 401 -> /login, home | t-4, t-6 | api.test.ts messages/401 hook, router.test.ts, source assertions, `pnpm check`, recorded manual gate |
| c-8 one structured login line per attempt, no password | t-3, t-7 | TestLoginLog_PerOutcome |
| c-9 sessions list / revoke / revoke-all | t-1, t-8 | TestSessions_RevokeScoping (store), TestSessions_List/RevokeOne/RevokeAll/RevokeSelf/Protected |
| c-10 Cache-Control: no-store on every /api/v1 response | t-5, t-7, t-9 | TestNoStore, TestRouter_NoStore (200 + 401 + 403), TestRun_LoginFlow |
| c-11 doubling lockout, 1h cap, 1h reset, Retry-After tracks, global flat | t-2, t-7 | TestLimiter_Escalation, TestLimiter_Reset, TestLimiter_Global, TestLogin_RetryAfterEscalates |

Locked decisions: session_storage -> t-1 + TestRun_SessionSurvivesRestart (proves the *why*,
not just the table); session_lifetime -> TestSessions_Expiry + TestResolve_Sliding;
rate_limit_thresholds -> TestLimiter_PerIP/Global/BudgetIsFailuresOnly (successes and 429s
cannot consume budget because the limiter has no method for them); client_ip_source ->
TestFrom_UntrustedPeerIgnoresXFF/TrustedPeer + TestLogin_ProxiedIP + TestEnvTable.

## Judgment calls

- **vitest added (t-4) rather than "Svelte via pnpm check only".** c-7 asks for distinct
  messages and 401 routing; a type-check cannot fail when 401 and 502 share a string. All
  tested TS is DOM-free so no jsdom; `make test` now gates web too. Rejected: leaving c-7
  with only a manual check — that is "tests pass" by another name.
- **No DOM component test runner (@testing-library/svelte + jsdom).** Cost is two more deps
  and a happy-dom/jsdom quirk budget for one placeholder page. Instead the Svelte files are
  held to the tested modules by source assertions (Login.svelte must call
  loginErrorMessage; App.svelte must wire onUnauthorized/routeFor) plus a recorded manual
  gate. Rejected the alternative of asserting nothing beyond svelte-check.
- **Real store in every auth test, no store interface/mocks.** modernc is pure Go and a temp
  file opens in milliseconds; mocks would let "only the hash is stored" pass while the
  real schema stored the token. Only AdGuard is faked (existing adguardtest).
- **Fake clocks are `Now func()` on Manager and Limiter, explicit `now` args on the store.**
  Rejected `time.Sleep`-based tests for 30-day sliding expiry and 1h lockout reset — they
  are untestable otherwise; the store takes `now` explicitly so its tests need no clock.
- **Cookie re-issued on every authenticated request** (fresh Max-Age) so the browser's
  cookie expiry slides with the server's. Rejected a throttled touch (write only if
  last_seen older than N minutes): fewer writes but a contract with a time hole; a family
  app's request rate does not need it.
- **Session ID is the integer row id** exposed in /sessions; the token hash is never
  returned. Rejected a second random public id — extra column, no criterion needs it.
- **Router uses a nested mux with `/api/v1/` -> RequireSession fallthrough** so an unknown
  route without a session is 401, not 404 — otherwise c-3's "every /api/v1/* route" would be
  true only for routes that exist. TestRouter_Fallthrough pins this.
- **Malformed login body -> 400, no AdGuard call, no budget, no login log line.** Rejected
  treating it as bad_credentials: it would let a garbage-body flood trip a real user's IP
  and c-8 says the line describes an *attempt*, which this is not.
- **AdGuard 429 (ErrRateLimited) maps to 502 adguard_unreachable.** The spec names only
  bad-credentials vs unreachable/5xx; AdGuard throttling us is an upstream problem, not the
  parent's. Rejected a third status — the SPA would need a message no criterion asks for.
- **TLS listener wired in t-9 (ServeTLS when tls.cert/key are set).** c-2's "Secure when the
  app serves TLS" is unverifiable if the app cannot serve TLS; a self-signed cert in the test
  is ~30 lines. Rejected deriving Secure from config without serving TLS — Secure=true on a
  plain-http listener would lock every phone out.
- **t-8 (sessions) is wave 4 after t-7, not parallel in wave 3.** Both edit router.go's
  protected-mux registration; a shared file across parallel tasks is a merge hazard for a
  30-minute task. t-9 runs alongside t-8 because it never touches internal/auth.
- **Limiter has no Success/Rejected method.** The locked decision that only bad-credential
  outcomes consume budget is enforced by API shape, not by handler discipline — the test
  contract notes it is compile-level.
- **Idle-IP eviction in the limiter** is not a criterion but an unbounded map keyed by
  attacker-chosen IPs is the obvious hole in a rate limiter; one test, one sweep.
- **Hourly DeleteExpired reaper in main** rather than relying on read-time expiry alone: a
  lost phone's row would otherwise sit forever. Made observable via a package-var interval
  and a debug line, matching phase 01's probeInterval pattern.
