# pin-unlock: verification-lens draft

Lens: for each criterion I wrote the test that would prove it first, then cut the smallest task that makes that test satisfiable. Every task is named by its contracts. Where a criterion needs proof across process boundaries (restart, TLS, two devices), the proof sits in `cmd/adguard-reward/main_test.go`, where the real binary runs, instead of in a harness that only simulates it.

```
Phase pin-unlock — 11 tasks across 6 waves

Wave 1
  t-1  Add Argon2id PIN hashing and format check
       files:    internal/auth/pin.go, internal/auth/pin_test.go, go.mod, go.sum
       covers:   c-1
       desc:     ValidPin(s) accepts exactly 4-6 ASCII digits. HashPin returns PHC
                 "$argon2id$v=19$m=..,t=..,p=..$salt$hash" with a random 16-byte salt.
                 VerifyPin compares in constant time. Adds golang.org/x/crypto (pinned exact version).
       contract: ValidPin table: "1234","123456" true; "123","1234567","12a4","１２３４"(fullwidth),
                   "١٢٣٤"(Arabic-Indic)," 1234","" false. A unicode.IsDigit mutant fails the non-ASCII rows.
                 HashPin output matches ^\$argon2id\$v=19\$m=\d+,t=\d+,p=\d+\$[A-Za-z0-9+/]+\$[A-Za-z0-9+/]+$
                   and does not contain the PIN.
                 Hashing the same PIN twice gives different strings (the salt is random). VerifyPin is
                   true for the right PIN on both.
                 VerifyPin("1235") against a hash of "1234" is false. A hash with a tampered variant
                   ($argon2i$), a wrong version or a truncated segment returns an error and does not panic.

  t-2  Add devices table and device store
       files:    internal/store/migrations/0010_devices.sql, internal/auth/device.go,
                 internal/store/devices.go, internal/store/devices_test.go
       covers:   c-1, c-4, c-5, c-6
       desc:     The migration creates devices(id, username, token_hash UNIQUE, pin_hash, failed_attempts
                 CHECK 0..4, created_at, last_used_at, expires_at) and adds
                 sessions.device_id REFERENCES devices(id) ON DELETE SET NULL.
                 auth/device.go declares the Device type (LogValue shows id and username only),
                 the DeviceStore interface and ErrDeviceNotFound.
                 Store methods: CreateDevice (inserts the device and links the caller's session in one tx),
                 DeviceByTokenHash, RecordFailure (atomic increment; on the 5th it deletes the device and
                 the sessions linked to it), UnlockSucceeded (counter 0, slides expires_at), ReplacePin
                 (new hash, counter 0), DeleteDevice, DeleteOtherDevices(username, keepID).
       contract: RecordFailure x5 returns remaining 4,3,2,1 and then revoked=true. After that the
                   device row is gone and so is its linked session row.
                 RecordFailure x3, Close, Open on the same dir, RecordFailure gives remaining=1. If the
                   counter is held in memory, this assertion fails.
                 10 goroutines each call RecordFailure once. Exactly 4 non-revoked results come back,
                   with the distinct remaining values {4,3,2,1}. A read-then-write race fails this.
                 UnlockSucceeded after 3 failures puts failed_attempts back to 0. ReplacePin swaps
                   pin_hash and also puts failed_attempts to 0.
                 DeleteDevice leaves the linked session row in place with device_id NULL. This is the
                   ON DELETE SET NULL behaviour.
                 DeleteOtherDevices("mum", A) removes mum's device B, keeps A, keeps dad's device C,
                   and returns 1.
                 A duplicate token_hash insert fails because of the unique constraint. Inserting
                   failed_attempts=5 directly fails the CHECK.
                 slog of a Device renders exactly {id, username}. Neither hash appears in the output.

  t-3  Add PIN/unlock calls and code-aware 401 routing
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts
       covers:   c-3, c-4, c-5, c-6
       desc:     New calls: unlock(pin), pinStatus(), setPin(pin, password?), removePin(password).
                 ApiError gains attemptsRemaining. onUnauthorized now receives the 401 code: the default
                 handler sends pin_required to the 'unlock' route and every other code to 'login'.
                 /api/v1/unlock is exempt from onUnauthorized, the same way LOGIN_PATH already is.
                 New routes 'unlock' at /unlock and 'settings' at /settings. messageFor learns bad_pin,
                 device_revoked and reauth_required.
       contract: A 401 {error:"pin_required"} on GET /api/v1/children calls the injected spy with
                   "pin_required". With the default handler, location.pathname becomes /unlock.
                   A 401 {error:"unauthorized"} goes to /login.
                 A 401 {error:"bad_pin", attempts_remaining:2} from unlock() rejects with
                   code=bad_pin and attemptsRemaining=2, and the onUnauthorized spy is NOT called.
                 setPin("1234") sends {pin} with no password key. setPin("1234","pw") sends
                   {pin,password}. removePin("pw") sends DELETE /api/v1/pin with body {password}.
                   All of them carry the CSRF header.
                 A 403 {error:"bad_credentials"} from removePin does not call onUnauthorized.

Wave 2
  t-4  Apply 15-minute idle lock to device-linked sessions
       files:    internal/auth/auth.go, internal/auth/auth_test.go, internal/store/sessions.go,
                 internal/store/sessions_test.go
       depends:  t-2
       covers:   c-3, c-5
       desc:     Session gains DeviceID. Options gains IdleLock (default 15m). Authenticate slides a
                 linked session by IdleLock and an unlinked one by Lifetime (30d). IssueOnDevice mints
                 a session that is already linked. The store reads and writes sessions.device_id
                 (NULL when the value is 0).
       contract: Fake clock, linked session: Authenticate at +14m59s is OK. A second call
                   +14m59s later is still OK, which proves the expiry slides. Then +15m00s exactly
                   gives ErrNoSession and the row is deleted. That pins !After() at the boundary.
                 An unlinked session at +16m is OK. If the lock is applied globally, this fails.
                 IssueOnDevice(..., 7) gives ExpiresAt = now+15m and DeviceID=7. Plain Issue still
                   gives now+30d.
                 Store round-trip: inserting with DeviceID=7 reads back 7. Inserting with 0 stores
                   SQL NULL. A raw query asserts `device_id IS NULL`, not 0.

  t-5  Add PIN pad and route locked sessions to it
       files:    web/src/lib/Unlock.svelte, web/src/lib/Unlock.test.ts, web/src/App.svelte,
                 web/src/App.test.ts
       depends:  t-3
       covers:   c-3, c-4, c-6
       desc:     Unlock.svelte is a PIN pad: digit buttons, a masked display and submit. It calls
                 unlock(). On 204 it calls onUnlocked, which runs App.refresh(). device_revoked or a
                 plain 401 calls onRevoked, which navigates to login. App renders Unlock on the
                 'unlock' route and Login on 'login'.
       contract: GET /me 401 pin_required renders the PIN pad. The labelled PIN group is present
                   and the "Sign in" button is absent. GET /me 401 unauthorized renders "Sign in".
                 Tapping 1,2,3,4 and submitting POSTs /api/v1/unlock with exactly {pin:"1234"}.
                   A 204 followed by /me 200 renders Home.
                 A 401 bad_pin with attempts_remaining 2 shows "2 attempts left", clears the entered
                   digits and keeps the pad up.
                 A 401 device_revoked renders "Sign in" and location becomes /login.
                 Mid-session: Home's GET /api/v1/children returns 401 pin_required and the PIN pad
                   replaces Home.

Wave 3 (depends t-1, t-2, t-4 | t-5)
  t-6  Add device cookie, pin_required and unlock core
       files:    internal/auth/device.go, internal/auth/middleware.go, internal/auth/device_test.go
       depends:  t-1, t-2, t-4
       covers:   c-2, c-3, c-4, c-5, c-6
       desc:     Options gains Devices and DeviceLifetime (default 90d). Adds the DeviceCookie and
                 ClearDeviceCookie helpers (adguard_reward_device). Manager methods: LookupDevice
                 (rejects malformed, unknown or expired tokens and deletes an expired row on sight),
                 SetFirstPin, ChangePin, RemoveDevice, RevokeOtherDevices, and Unlock(raw, pin), which
                 is serialised by a mutex and returns {Outcome ok|bad_pin|device_revoked|no_device,
                 Username, Remaining, session token}. RequireSession writes code pin_required when the
                 device cookie looks up OK.
       contract: DeviceCookie has HttpOnly, SameSite=Strict, Path=/ and Max-Age=7776000. Secure
                   matches Options.Secure, checked with both true and false.
                 SetFirstPin returns a 43-char raw token. The stored TokenHash equals sha256 of the
                   decoded bytes, and the stored PinHash passes VerifyPin.
                 Unlock with no cookie gives no_device. An unknown token gives device_revoked. A token
                   past expiry (fake clock +90d) gives device_revoked and deletes the row.
                 Unlock with the right PIN gives ok. The session it mints has DeviceID set and the
                   device's Username (a device owned by "dad" mints a session for "dad"). The device's
                   expires_at becomes now+90d, so an unlock at +89d followed by another at +178d still
                   succeeds.
                 Unlock with a wrong PIN gives bad_pin, Remaining 4..1, then device_revoked.
                 RequireSession with no or expired session cookie and a valid device cookie writes 401
                   pin_required. With a deleted, expired or absent device cookie it writes 401
                   unauthorized. With only the device cookie and no session cookie at all it still
                   writes pin_required.

  t-7  Add PIN settings screen and route
       files:    web/src/lib/PinSettings.svelte, web/src/lib/PinSettings.test.ts, web/src/lib/Home.svelte,
                 web/src/App.svelte, web/src/App.test.ts
       depends:  t-5 (App.svelte / App.test.ts are edited by t-5)
       covers:   c-5
       desc:     PinSettings shows pinStatus(). With no PIN it offers a set form with a PIN field and a
                 confirm field and no password field. With a PIN it offers a change form (new PIN plus
                 AdGuard password) and a remove action (password). Home gets a Settings button.
                 App renders PinSettings on 'settings'.
       contract: has_pin=false: no password input rendered. Submit POSTs {pin:"4821"} only.
                 has_pin=true: change POSTs {pin, password}. Remove sends DELETE with {password}, then
                   re-fetches status and shows "No PIN on this device".
                 "12a4", "123" or a mismatched confirm shows an inline error and makes zero fetch calls.
                 A 403 bad_credentials shows "Wrong password" and the route stays 'settings', not
                   'login'. A 429 shows the retry seconds.
                 From Home, clicking Settings sets the path to /settings and renders the PIN status.

Wave 4 (depends t-6)
  t-8  Add GET/POST/DELETE /api/v1/pin with reauth
       files:    internal/api/pin.go, internal/api/pin_test.go, internal/api/api.go, internal/api/login.go,
                 internal/api/login_test.go
       depends:  t-6
       covers:   c-1, c-2, c-5
       desc:     GET returns {has_pin}. POST {pin}: on a device without a PIN it calls SetFirstPin and
                 sets the device cookie. On a device with a PIN it needs {pin,password}, which goes
                 through reauth. DELETE {password} goes through reauth, then RemoveDevice and clears
                 the device cookie. The limiter, the slot and the AdGuard.Login sequence move out of
                 handleLogin into a shared checkPassword helper. Wrong password is 403 bad_credentials,
                 missing password is 403 reauth_required, and a limited IP is 429. The harness wires
                 Devices: st and keeps its data dir.
       contract: POST bodies "123", "1234567", "12a4", "１２３４", the number 1234 (not a string) and {}
                   each return 400. "1234" and "123456" return 204. With no session the result is 401.
                 After POST {pin:"917362"}: the devices row pin_hash matches the PHC regex. The literal
                   917362 is absent from a SELECT * dump of every table in sqlite_master, from the raw
                   bytes of every file in the data dir (db, -wal, -shm), and from the debug-level log
                   buffer. The device cookie value is absent from the DB dump and the log buffer.
                 The device cookie's sha256 equals devices.token_hash.
                 First PIN: /control/login hits stay unchanged across the POST.
                 Change with no password gives 403 reauth_required and pin_hash is unchanged. A wrong
                   password gives 403 bad_credentials and pin_hash is unchanged. The right password
                   gives 204, a different pin_hash, failed_attempts 3→0, and exactly +1 /control/login hit.
                 Five wrong-password changes from one IP, then a sixth attempt, gives 429 with
                   Retry-After and no extra /control/login hit. This proves the shared login limiter.
                 DELETE with the right password gives 204. The devices count becomes 0, Set-Cookie clears
                   adguard_reward_device (Max-Age<0) and the session row has device_id NULL. After the
                   fake clock advances 16m, GET /me is 200 and expires_at is ≈ now+30d.
                 GET /api/v1/pin returns has_pin false, then true after set, then false after DELETE.

Wave 5 (depends t-8)
  t-9  Add POST /api/v1/unlock with outcome logging
       files:    internal/api/unlock.go, internal/api/unlock_test.go, internal/api/api.go
       depends:  t-8
       covers:   c-2, c-3, c-4, c-7
       desc:     No RequireSession on this route. It maps Manager.Unlock outcomes as follows:
                 ok gives 204 with a fresh session cookie and a refreshed device cookie. bad_pin gives
                 401 {error:bad_pin, attempts_remaining}. device_revoked gives 401 device_revoked and
                 clears the device cookie. no_device gives 401 unauthorized. Every attempt logs exactly
                 one Info line {event:unlock, username, ip, outcome}.
       contract: Set the PIN, advance 15m, then GET /me gives 401 pin_required. Remove the device
                   cookie and the same request gives 401 unauthorized. Both sides of the window are
                   covered at 14m59s (200) and 15m00s (401).
                 POST /unlock with the right PIN gives 204. The new session cookie differs from the old
                   one, /me with it is 200 username=mum, and /control/login hits are unchanged across
                   the unlock. Unlocking again 15m later gives pin_required, because the minted session
                   is linked.
                 Wrong PIN x4 gives attempts_remaining 4,3,2,1. The 5th gives device_revoked, a device
                   Set-Cookie with Max-Age<0 and a devices count of 0. The right PIN afterwards gives
                   device_revoked.
                 3 wrong, then 1 right (204), then 4 wrong gives remaining 4..1 and no revoke. If the
                   counter is not reset, the 2nd of these fails.
                 8 concurrent wrong PINs give exactly 4 bad_pin responses. Every other response is
                   device_revoked.
                 Logs: one line each for ok, bad_pin, device_revoked (unknown token and 5th strike) and
                   no_device (username ""). Each has ip equal to the trusted-proxy-resolved client IP.
                   The whole log buffer never contains the PIN or the raw device cookie.

  t-10 Revoke devices on logout and revoke-all
       files:    internal/api/login.go, internal/api/sessions.go, internal/api/device_session_test.go
       depends:  t-8
       covers:   c-6
       desc:     Logout on a device-linked session deletes the session and the device and clears both
                 cookies. revoke-all also calls RevokeOtherDevices(username, sess.DeviceID) and returns
                 devices_revoked. A full login that presents a valid device cookie with the same
                 username links the new session to that device.
       contract: Logout with a PIN: the devices count is 0, both Set-Cookies have Max-Age<0, and the
                   next GET /me with the old device cookie is 401 unauthorized (not pin_required).
                 Logout without a PIN behaves as before. A seeded device belonging to "dad" survives it.
                 revoke-all from device A's session: device B (mum) is gone, A remains, dad's C remains,
                   and the body has devices_revoked=1. B's device cookie on GET /me then gives plain
                   401 unauthorized.
                 Login with a valid device cookie for mum: the session has device_id set, so /me at
                   +15m gives pin_required. With dad's device cookie: device_id is NULL.

Wave 6 (depends t-9, t-10)
  t-11 Wire devices into main; prove restart and TLS
       files:    cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go
       depends:  t-9, t-10
       covers:   c-2, c-4, c-6
       desc:     main passes Devices: st to auth.New. End-to-end tests run against the real binary.
       contract: TestRun_UnlockCounterSurvivesRestart: login, set the PIN, 3 wrong unlocks, stop,
                   start on the same data_dir, then 1 wrong unlock gives attempts_remaining=1 and
                   the next gives device_revoked.
                 TestRun_DeviceCookieSecure: the TLS listener's set-PIN response has Secure on the
                   device cookie. Over plain http it does not.
                 TestRun_RevokeAllRevokesOtherDevice: two clients each set a PIN. A calls revoke-all,
                   B's POST /unlock with the right PIN gives 401 device_revoked, and A's unlock gives 204.
                 If main forgets Devices, the first test fails at set-PIN (500), not silently.
```

## Coverage

| Criterion | Tasks | Proving test(s) |
|---|---|---|
| c-1 PIN set, 4-6 ASCII digits, Argon2id PHC, no plaintext in DB or logs | t-1, t-2, t-8 | pin_test ValidPin/PHC table; pin_test no-plaintext over DB dump, data-dir bytes and log buffer |
| c-2 device token, cookie attrs, unlock gives 204 with zero /control/login hits | t-6, t-8, t-9, t-11 | device_test cookie attrs and sha256; unlock_test login-hit delta; main_test Secure under TLS |
| c-3 15-min idle gives pin_required or plain 401; SPA PIN pad; fake clock both sides | t-4, t-6, t-9, t-3, t-5 | auth_test boundary at 14m59s/15m00s; unlock_test HTTP-level window; App.test PIN pad vs Sign in |
| c-4 attempts_remaining, 5th revokes, reset on success, persisted counter | t-2, t-6, t-9, t-11, t-5 | devices_test atomic/reopen; unlock_test 4..1 sequence plus reset plus concurrency; main_test restart; Unlock.test revoked → login |
| c-5 settings set/change/remove, change resets counter, remove keeps 30-day session | t-2, t-4, t-8, t-7 | pin_test reauth and remove-then-16m /me 200; PinSettings.test forms |
| c-6 revoke-all kills other devices, revoked unlock gives device_revoked, SPA login | t-2, t-10, t-11, t-3, t-5 | devices_test DeleteOtherDevices; device_session_test; main_test two-client e2e |
| c-7 one log line per unlock outcome, no PIN or raw token in logs | t-9 (plus t-8 for the set/change paths) | unlock_test per-outcome log lines and log-buffer scan |

Locked decisions:

- **idle_window (15m sliding)**: t-4 sets the IdleLock default. The boundary tests in t-4 (auth level) and t-9 (HTTP level) cover the slide, the 14m59s pass and the 15m00s fail. t-10 makes a full login on a PIN device subject to the lock too.
- **device_token_lifetime (90d, slides on unlock only)**: t-6 sets the DeviceLifetime default and Max-Age=7776000. The unlock at +89d is followed by an unlock at +178d. A token at +90d with no unlock is revoked. Only Unlock(ok) calls UnlockSucceeded. Session traffic does not slide the device.
- **pin_change_reauth**: t-8. The first PIN causes zero /control/login hits. Change and remove need the password, cost exactly +1 hit and share the login limiter (429 with no hit). The checkPassword helper is extracted from handleLogin so both use one limiter dance.
- **logout_semantics**: t-10. Logout deletes the session and the device and clears both cookies. The next /me is plain 401, which sends the SPA to the login page.
- **unlock_identity**: t-6. A device owned by "dad" mints a session for "dad". t-10 links on login only when the username matches.

## Judgment calls

- **Where the idle lock lives**: I chose a `sessions.device_id` column (the server decides) over inferring "PIN device" from the presence of the device cookie on each request. With the cookie approach, a cleared cookie would quietly turn a locked session back into a 30-day one. ON DELETE SET NULL also gives c-5's "session continues at 30 days" behaviour for free.
- **Where the unlock logic lives**: I put it in auth.Manager.Unlock (t-6), with the HTTP mapping in t-9. A thick API handler was the alternative. Splitting it lets fake-clock and fake-store tests pin the 90d and 15m boundaries without HTTP, and keeps the outcome table in one place.
- **Atomic counter**: the increment and the 5th-strike delete happen in SQL inside one transaction (t-2). An in-memory or read-then-write counter would fail both the reopen test and the 10-goroutine test. Manager.Unlock also holds a mutex to bound Argon2id memory under bursts.
- **5th strike also deletes the device's linked sessions**: otherwise ON DELETE SET NULL would turn a lingering locked session into a 30-day one.
- **Wrong reauth password is 403, not 401**: api.ts treats every non-login 401 as "signed out" and would throw the parent out of settings. The SPA test in t-3 asserts the spy is not called.
- **Unlock code for no_device is plain 401 unauthorized**: it is not a new code. The log still says outcome=no_device. The SPA only needs bad_pin versus "go to login".
- **A well-formed JSON unlock with a non-digit or wrong-length pin counts as bad_pin**: a 400 would hand the caller a free format oracle and would escape the c-7 log. A body that is not JSON is still 400 and is not logged.
- **A device cookie with no session cookie gives pin_required**: RequireSession clears the session cookie on expiry, so the reload after a lock would otherwise show the login page.
- **Login on a valid same-user device cookie links the session (t-10)**: without this, a password login on a PIN device gives a 30-day session, which contradicts "on a device with a PIN" in c-3. A different-user device is left unlinked rather than rebound.
- **Restart, TLS and two-device proofs are in main_test (t-11)**, not only in the harness. The harness cannot restart a process. The main test also fails loudly if main forgets to wire Devices.
- **t-8 has 5 files**: all of them are in internal/api, and two (api.go routes and the login_test.go harness) are one-line wiring. Splitting would add a seventh serial wave.
- **t-7 has 5 files**: App.svelte and App.test.ts carry only the route branch and one navigation test. It waits on t-5 only because both tasks edit those files.
- **t-9 and t-10 run in parallel in wave 5**: t-10 proves "revoked device" at the HTTP level through /me (plain 401). The literal "next unlock gives device_revoked" is proved end-to-end in t-11 rather than by making t-10 wait on t-9.
- **Argon2id comes from golang.org/x/crypto/argon2 at a pinned exact version**: hand-rolling Argon2id was the alternative. x/crypto is pure Go, so it keeps CGO_ENABLED=0. The PHC encoding and decoding is ours, about 40 lines, tested by the tamper cases in t-1.
- **Session cookie Max-Age stays 30d on linked sessions**: the server row is the authority, and a shorter browser Max-Age would only add a second clock to test.
