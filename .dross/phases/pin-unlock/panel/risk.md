# pin-unlock — risk-lens draft

Lens: every task owns a named set of failure modes and the tests that would catch them. Tasks are cut so that
each failure mode has exactly one owner; where two tasks touch the same surface, the split is by *which way it
breaks* (e.g. the store owns "the counter races", the unlock endpoint owns "the 5th strike has the wrong HTTP
semantics").

Risk register (owner in brackets):

| # | Failure mode | Owner |
|---|---|---|
| R1 | "4-6 digits" accepts Unicode digits (`١٢٣٤`, `１２３４`), whitespace, a JSON number (`0123` → 123) | t-1 (format), t-9 (HTTP shape) |
| R2 | Hash is not Argon2id PHC, verify accepts a malformed/foreign PHC, params hard-coded so old hashes stop verifying | t-1 |
| R3 | Plaintext PIN lands in a DB column, the DB/WAL bytes, or a debug log line | t-9 (set), t-10 (unlock) |
| R4 | Parallel wrong PINs each read failed=0, all run Argon2, and an attacker gets N guesses instead of 5 | t-2 |
| R5 | 5-strike revocation deletes the device but `ON DELETE SET NULL` leaves its live session untagged → it silently becomes a 30-day session (escalation) | t-2 |
| R6 | PIN session slides by 30 days instead of 15 min; boundary off by one | t-5 |
| R7 | Setting a PIN leaves the current session's `expires_at` at +30d until the next request → phone never locks if put down right after setting | t-9 (HTTP), t-5 (Attach primitive) |
| R8 | Removing a PIN leaves `expires_at` at +15m → session dies after 15 min despite c-5 "continues under 30-day" | t-5 (Detach primitive), t-12 (HTTP) |
| R9 | After the first `pin_required` the session cookie is cleared, so the *second* request has no session cookie and falls through to plain 401 | t-7 |
| R10 | Expired/unknown device cookie still yields `pin_required` (SPA shows a pad that can never succeed) | t-7 |
| R11 | Device expiry slides on every authenticated request instead of only on unlock (locked device_token_lifetime) | t-7 (gate), t-10 (unlock slides) |
| R12 | Two `Set-Cookie` headers for the device cookie on one response (set + clear) | t-7 |
| R13 | "Has a PIN" decided from the device cookie → strip the cookie and POST /pin overwrites the PIN without a password | t-9 |
| R14 | PIN change/remove re-check bypasses or forks the login limiter, or re-checks a client-supplied username | t-3 (helper), t-12 (endpoints) |
| R15 | Unlock counter held in memory, so a restart resets it | t-13 |
| R16 | Unlock mints a session for the wrong identity or calls /control/login | t-10 |
| R17 | Logout on a PIN device leaves the device row → next visit shows a pad, not login | t-11 |
| R18 | revoke-all deletes the caller's own device, or another user's devices | t-11 |
| R19 | SPA treats every 401 as "go to login": a wrong PIN on /unlock bounces to login; `pin_required` shows login | t-4 (transport), t-6 (screens) |
| R20 | Log-scan test is flaky: a 6-digit PIN can occur in slog's wall-clock nanoseconds | t-9, t-10 (both strip `time` before scanning) |
| R21 | Production wiring missing (`Devices` nil in main) — every API test green, feature dead in the binary; device cookie not Secure behind TLS | t-13 |

---

```
Phase pin-unlock — 13 tasks across 6 waves

Wave 1
  t-1  Add PIN validation and Argon2id PHC hashing
       files:    internal/auth/pin.go, internal/auth/pin_test.go, go.mod, go.sum
       desc:     ValidatePIN (bytes '0'-'9' only, len 4-6), HashPIN → $argon2id$v=19$m=..,t=..,p=..$salt$hash with a
                 random 16-byte salt, VerifyPIN parsing params from the stored string, constant-time compare.
                 Adds golang.org/x/crypto (argon2) pinned in go.mod/go.sum.
       covers:   c-1
       contract: ValidatePIN rejects "١٢٣٤" (Arabic-Indic), "１２３４" (fullwidth), " 1234", "1234\n", "123", "1234567",
                 "12a4", "" and accepts "0000", "000000", "0123";
                 HashPIN output matches ^\$argon2id\$v=19\$m=\d+,t=\d+,p=\d+\$[A-Za-z0-9+/]+\$[A-Za-z0-9+/]+$ and two
                 hashes of the same PIN differ (salt);
                 VerifyPIN("0123", HashPIN("123")) is false; VerifyPIN of a "$argon2i$" string, a v=16 string, or a
                 truncated PHC returns an error, never true;
                 a hash built with m=8192,t=1,p=1 still verifies after the defaults are raised (params read from the
                 string, not the constants).

  t-2  Add devices schema, store and attempt reservation
       files:    internal/store/migrations/0010_devices.sql, internal/auth/device.go, internal/store/devices.go,
                 internal/store/devices_test.go
       desc:     devices(id, username, token_hash UNIQUE, pin_hash CHECK LIKE '$argon2id$%', failed_attempts CHECK 0..5,
                 created_at, expires_at) and sessions.device_id NULL REFERENCES devices ON DELETE SET NULL.
                 auth.Device (LogValue = id+username only) + auth.DeviceStore interface; store implements it, incl.
                 ReserveAttempt (atomic increment-first), ResetAttempts, ReplacePIN, RevokeDevice (sessions + device in
                 one tx), DeleteDevice, DeleteOtherDevices(username, keepID), DeleteExpiredDevices, TouchDevice.
       covers:   c-1, c-4, c-6
       contract: 20 goroutines calling ReserveAttempt on one device → exactly 5 succeed, returning 1..5 once each, and
                 15 are refused (if the increment is read-then-write, a count repeats and the test fails);
                 RevokeDevice deletes the device AND every session whose device_id points at it, leaving other
                 sessions untouched (R5 — if it relies on SET NULL alone, the tagged session survives and the test fails);
                 DeleteDevice (the remove path) leaves the tagged session row with device_id NULL;
                 inserting pin_hash "583920" or failed_attempts 6 fails the CHECK;
                 DeleteOtherDevices("mum", A) removes mum's B and C, keeps A and dad's D; keepID 0 removes all of mum's;
                 slog of a Device renders no token_hash/pin_hash key;
                 schema_migrations lists 0010_devices.sql immediately after 0009_grant_history.sql.

  t-3  Extract shared password re-check from login
       files:    internal/api/login.go, internal/api/reauth.go, internal/api/reauth_test.go
       desc:     Move the limiter double-check + loginSlot + AdGuard.Login + Fail logic out of handleLogin into
                 recheckPassword(r, username, password) used by both login and (later) PIN change/remove.
       covers:   c-5
       contract: five wrong recheckPassword calls from 203.0.113.1 → the 6th returns rate-limited with zero new
                 /control/login hits, and a subsequent POST /api/v1/login from that IP is 429 (one shared budget);
                 an AdGuard 502 from the re-check consumes no budget (6 unreachable re-checks, then a correct one passes);
                 the /control/login body the fake records names the username argument, never one from the request body;
                 TestLogin_Burst's "at most 5 AdGuard hits in a 20-request burst" still holds after the extraction.

  t-4  Route 401 by code in the SPA transport
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts
       desc:     request(): 401 pin_required → onPinRequired (sets a `locked` store), other 401 → onUnauthorized, and
                 neither for /api/v1/unlock or /api/v1/login. ApiError.attemptsRemaining. unlock, pinStatus, setPin,
                 changePin, removePin; Route 'settings' ↔ /settings; messageFor for bad_pin/device_revoked.
       covers:   c-3, c-4, c-5, c-6
       contract: a 401 {error:'pin_required'} from GET /api/v1/children calls the pin spy and NOT the login spy;
                 a 401 {error:'bad_pin', attempts_remaining:3} from POST /api/v1/unlock calls neither spy and the
                 thrown ApiError has attemptsRemaining 3 (non-integer or missing → undefined);
                 a 401 device_revoked from /api/v1/unlock calls neither spy (the screen decides);
                 setPin('0123') sends body exactly {"pin":"0123"} (string, no password key); changePin sends {pin,password};
                 removePin sends DELETE /api/v1/pin with {password}; navigate('settings') pushes /settings.

Wave 2
  t-5  Tag sessions with a device; 15-minute idle window
       files:    internal/auth/auth.go, internal/auth/auth_test.go, internal/store/sessions.go,
                 internal/store/sessions_test.go
       desc:     Session.DeviceID (nullable); store persists/scans device_id. Authenticate slides a device-tagged
                 session by 15 min and an untagged one by 30 days. Manager.IssueForDevice, AttachDevice (sets
                 device_id AND expires_at=now+15m), DetachDevice (clears it AND sets expires_at=now+30d).
       depends:  t-2
       covers:   c-3, c-5
       contract: fake clock — tagged session last seen at T: Authenticate at T+14m59s succeeds with
                 ExpiresAt = now+15m; with no request in between, Authenticate at T'+15m (exactly) is ErrNoSession;
                 an untagged session at T+16m still authenticates with ExpiresAt = now+30d;
                 AttachDevice at T, no further request, Authenticate at T+15m01s → ErrNoSession (R7);
                 DetachDevice at T, no further request, Authenticate at T+16m → OK with ExpiresAt now+30d (R8);
                 store round-trip of device_id survives Insert → ByTokenHash, and NULL scans as no device.
  t-6  Add PIN pad and lock state to App
       files:    web/src/lib/PinPad.svelte, web/src/lib/PinPad.test.ts, web/src/App.svelte, web/src/App.test.ts
       desc:     App renders PinPad whenever `locked` is set, keeping the current URL; bad_pin shows attempts left;
                 device_revoked or no_device clears lock and navigates to login; success clears lock and re-mounts the
                 route so it refetches.
       depends:  t-4
       covers:   c-3, c-4, c-6
       contract: on /children, GET /api/v1/me → 401 pin_required renders the pad (no "Sign in" button) and pathname
                 stays /children;
                 wrong PIN → "4 attempts left" visible, pad still shown, input cleared, location unchanged;
                 401 device_revoked from unlock → Login renders and pathname is /login;
                 correct PIN (204) → pad gone, /api/v1/children fetched again and Children renders;
                 two concurrent 401 pin_required from Home's fetches render exactly one pad;
                 submit is disabled below 4 digits and a non-digit keystroke never reaches the input value.

Wave 3
  t-7  Add device cookie and pin_required gate
       files:    internal/auth/device.go, internal/auth/middleware.go, internal/auth/auth.go,
                 internal/auth/middleware_test.go
       desc:     Options.Devices + DeviceLifetime (90d). IssueDevice (256-bit token, SHA-256 stored via newToken),
                 LookupDevice (expired → deleted on sight), DeviceCookie/ClearDeviceCookie with single-Set-Cookie
                 dedupe. RequireSession's 401 path: live device cookie → code pin_required (session cookie cleared,
                 device cookie kept); otherwise plain unauthorized + device cookie cleared. RunSweeper also drops
                 expired devices.
       depends:  t-5
       covers:   c-2, c-3
       contract: expired tagged session + live device cookie → 401 pin_required, session cookie cleared, no device
                 Set-Cookie;
                 NO session cookie + live device cookie → 401 pin_required (R9: the request after the first lock);
                 device cookie for a row past 90d → 401 unauthorized, device cookie cleared (Max-Age<0), row deleted;
                 malformed / unknown device cookie → 401 unauthorized and device cookie cleared (R10);
                 a successful authenticated request leaves the device's expires_at unchanged (R11);
                 device cookie is HttpOnly, SameSite=Strict, Path=/, Max-Age=7776000, Secure iff Options.Secure, name
                 differs from the session cookie; set-then-clear on one response leaves exactly one device Set-Cookie;
                 DeviceStore error on the 401 path → 500 internal and neither cookie cleared;
                 RunSweeper tick removes a device at expires_at<=now and keeps one at now+1s.
  t-8  Add Settings screen for device PIN
       files:    web/src/lib/Settings.svelte, web/src/lib/Settings.test.ts, web/src/lib/Home.svelte,
                 web/src/App.svelte
       desc:     /settings route + Home nav button. Shows has_pin; first set = PIN + confirm only; change = PIN +
                 confirm + AdGuard password; remove = password. Errors keyed by code.
       depends:  t-4, t-6
       covers:   c-5
       contract: has_pin false → no password field rendered; submit POSTs body exactly {"pin":"583920"};
                 PIN/confirm mismatch → no request sent and an inline error;
                 has_pin true → change POSTs {pin,password}, remove sends DELETE with {password};
                 401 bad_credentials → error shown and has_pin display unchanged; 429 → retry message with seconds;
                 successful remove → "No PIN on this device" shown without reload;
                 Home's Settings button navigates to /settings and App renders Settings there.

Wave 4
  t-9  Add first-PIN set and PIN status endpoints
       files:    internal/api/pin.go, internal/api/pin_test.go, internal/api/api.go, internal/api/login_test.go
       desc:     Deps.Devices; GET /api/v1/pin {has_pin} from the session's device_id; POST /api/v1/pin {pin} for an
                 untagged session: hash, IssueDevice, AttachDevice, set device cookie. A tagged session without a
                 password is refused here (change lands in t-12). Harness wires Devices into auth.New and Deps.
       depends:  t-1, t-7
       covers:   c-1, c-2, c-3, c-5
       contract: POST {"pin":"583920"} → 204 + device cookie; the devices row's pin_hash is PHC and token_hash =
                 sha256(decoded cookie); "583920" occurs in no column of devices/sessions, not in the .db or -wal bytes,
                 and not in the debug log buffer with each line's `time` attr removed (R3, R20);
                 400 and no devices row for "123", "1234567", "12a4", "١٢٣٤", {"pin":1234} (number), missing pin,
                 unknown key, >4 KiB body;
                 set PIN at T, no further request, GET /api/v1/me at T+15m01s → 401 pin_required (R7);
                 tagged session POST {"pin":"111111"} with NO device cookie and no password → 400, pin_hash unchanged (R13);
                 GET /api/v1/pin is {has_pin:false} before and {has_pin:true} after.

Wave 5
  t-10 Add unlock endpoint with persisted strikes and log
       files:    internal/api/unlock.go, internal/api/unlock_test.go, internal/api/api.go, internal/api/errors.go
       desc:     POST /api/v1/unlock {pin} outside requireSession. Absent/malformed cookie → no_device; present but no
                 live row → device_revoked (+clear). ReserveAttempt before Argon2; success → ResetAttempts,
                 TouchDevice(+90d), IssueForDevice(device.username), both cookies set; 5th failure → RevokeDevice.
                 Codes bad_pin, device_revoked, pin_required; one event=unlock info line per attempt.
       depends:  t-9
       covers:   c-2, c-4, c-6, c-7
       contract: correct PIN, no session cookie → 204, new session cookie, GET /me returns the device's username, and
                 the fake's /control/login count is unchanged (R16);
                 wrong PINs → 401 bad_pin with attempts_remaining 4,3,2,1; 5th → 401 device_revoked, devices row gone,
                 device cookie Max-Age<0; 6th with the same cookie → device_revoked;
                 3 wrong, 1 correct, 1 wrong → attempts_remaining 4 (reset);
                 20 parallel wrong unlocks → exactly four bad_pin responses carrying {4,3,2,1} and sixteen device_revoked;
                 an unexpired session tagged to the device → after the 5th strike its next GET /me is 401 (R5 end-to-end);
                 unlock at device age 89d23h → 204 and expires_at = now+90d, device cookie Max-Age 90d; at 90d+1s with
                 no unlock in between → device_revoked;
                 malformed JSON / >4 KiB body → 400, no unlock log line, failed_attempts unchanged; "12a4" → bad_pin and
                 one attempt consumed;
                 each outcome ok|bad_pin|device_revoked|no_device emits exactly one line {event:unlock, username, ip,
                 outcome} (username "" for no_device); with `time` stripped, the log contains neither the PIN, the raw
                 device token, nor the new raw session token.
  t-11 Tear down devices on logout and revoke-all
       files:    internal/api/login.go, internal/api/sessions.go, internal/api/teardown_test.go
       desc:     Logout on a tagged session → RevokeDevice + clear both cookies. revoke-all → DeleteOtherDevices
                 (keep the caller's session device, 0 if untagged) then DeleteOthers; response gains revoked_devices.
                 Login with a live device cookie of the same username tags the new session; otherwise clears the
                 device cookie.
       depends:  t-3, t-9
       covers:   c-6
       contract: logout on a PIN device → 204, device row and pin_hash gone, exactly one Set-Cookie each for session
                 and device with Max-Age<0; GET /me with the old device cookie → plain 401 unauthorized, not
                 pin_required (R17);
                 mum has devices A (caller), B, C and dad has D → revoke-all from A deletes B, C; A and D remain;
                 unlock with B's cookie → 401 device_revoked (R18);
                 revoke-all from an untagged session deletes all of mum's devices and none of dad's;
                 full login with mum's live device cookie → new session is tagged (GET /me at +15m01s → pin_required);
                 full login with dad's device cookie as mum → device cookie cleared, session untagged (30d).
  t-12 Add PIN change and remove with re-auth
       files:    internal/api/pin.go, internal/api/pin_change_test.go, internal/api/api.go
       desc:     POST /api/v1/pin {pin,password} on a tagged session → recheckPassword(session.Username) then
                 ReplacePIN (resets counter, keeps token). DELETE /api/v1/pin {password} → recheck, DetachDevice,
                 DeleteDevice, clear device cookie.
       depends:  t-3, t-9
       covers:   c-5
       contract: change with the right password after 3 wrong unlocks → old PIN unlock is bad_pin with
                 attempts_remaining 4 (counter reset), new PIN unlocks, device cookie value unchanged;
                 change/remove with a wrong password → 401 bad_credentials, pin_hash and device row unchanged; the 6th
                 wrong attempt → 429 with zero new /control/login hits (R14);
                 missing password on change/remove → 400 with zero /control/login hits;
                 the recorded /control/login body names the session's username; a body with a "username" key → 400;
                 remove → 204, device row gone, device cookie Max-Age<0, and GET /me at +16m is 200 with
                 expires_at ≈ now+30d (R8);
                 DELETE /api/v1/pin on an untagged session → 404 not_found;
                 first PIN still needs no password (no AdGuard hit) — locked pin_change_reauth.

Wave 6
  t-13 Wire devices into main; prove restart durability
       files:    cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go
       desc:     Pass st as auth.Options.Devices and api.Deps.Devices; device cookie inherits the existing Secure
                 decision; sweeper covers devices.
       depends:  t-10, t-11, t-12
       covers:   c-2, c-4
       contract: run(): set PIN, 2 wrong unlocks, stop run(), start again on the same data dir, 3rd wrong unlock →
                 attempts_remaining 2, not 4 (R15);
                 after the restart the pre-restart device cookie + correct PIN → 204 (Devices non-nil in production, R21);
                 base_url https:// or TLS listener → device cookie Secure; plain http → not Secure.
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 PIN set, 4-6 ASCII digits, Argon2id PHC only, no plaintext in DB/logs | t-1 (format + PHC), t-2 (CHECK constraint), t-9 (HTTP 400s, row/DB/log scan) |
| c-2 device token 256-bit/SHA-256, cookie attrs, unlock → 204 fresh session, zero /control/login | t-7 (token + cookie attrs), t-9 (issued on set), t-10 (unlock 204, zero login hits), t-13 (Secure under TLS in the binary) |
| c-3 15-min idle, pin_required vs plain 401, SPA pad, fake clock both sides | t-5 (window both sides), t-7 (pin_required vs plain), t-9 (window starts at set), t-4 + t-6 (SPA) |
| c-4 attempts_remaining, 5th revokes + clears cookie, reset on correct, persisted across restart, SPA login | t-2 (atomic reservation), t-10 (HTTP semantics), t-13 (restart), t-4 + t-6 (SPA) |
| c-5 settings screen; change replaces hash + resets counter; remove deletes token + hash, cookie cleared, session on 30d | t-3 (re-check), t-5 (Detach → 30d), t-9 (status), t-12 (change/remove), t-4 + t-8 (SPA) |
| c-6 revoke-all deletes other devices; revoked unlock → device_revoked; SPA login | t-2 (DeleteOtherDevices scoping), t-11 (endpoint + revoked unlock), t-6 (SPA) |
| c-7 one structured unlock log line per outcome, no PIN/token | t-10 |

Locked decisions:

| Decision | Honoured in |
|---|---|
| idle_window (15 min, sliding on every authenticated request) | t-5 (Authenticate slides tagged sessions by 15m), t-7 (pin_required on expiry) |
| device_token_lifetime (90 d, sliding only on successful unlock) | t-7 (lookup enforces 90d; RequireSession never touches device expiry), t-10 (TouchDevice on ok only) |
| pin_change_reauth (change/remove need AdGuard password via /control/login + login rate limit; first PIN needs session only) | t-3 (shared limiter path), t-9 (first set: no password; refuses change), t-12 (change/remove re-check) |
| logout_semantics (logout deletes session + device token + PIN hash) | t-11 |
| unlock_identity (session minted for the device's username) | t-2 (devices.username), t-10 (IssueForDevice(device.username)) |

## Judgment calls

- **Which sessions are "on a PIN device":** a `sessions.device_id` column (server truth), not presence of the device cookie — rejected cookie-presence because stripping the cookie would turn a locked session into a 30-day one and let POST /pin skip re-auth (R13).
- **FK `ON DELETE SET NULL` + explicit `RevokeDevice` tx:** SET NULL so PIN removal keeps the session (c-5); revocation deletes tagged sessions first — rejected CASCADE (breaks c-5) and bare SET NULL (5 wrong PINs on an unlocked phone would upgrade it to 30 days, R5).
- **Increment-first attempt reservation in SQL:** rejected an in-process unlock mutex like `loginSlot` — it would bound parallel guesses but not survive restart, and it hides the race from the store test.
- **Attach/Detach rewrite `expires_at` immediately:** rejected "the next Touch will fix it" — the gap is exactly the "parent puts the phone down" case (R7/R8).
- **Unlock outcome mapping:** absent or malformed cookie → `no_device`; well-formed cookie with no live row (revoked, expired, deleted by revoke-all) → `device_revoked`. Rejected a tombstone table to distinguish forged from revoked: it adds a table to make a distinction the SPA doesn't use.
- **Malformed body vs bad PIN string:** non-JSON/oversize → 400, not an attempt, not logged; well-formed `{pin}` with a non-digit string → `bad_pin`, consumes an attempt. Otherwise a crafted body type could probe without spending the budget, or garbage would burn strikes.
- **Password re-check extracted from handleLogin (t-3) instead of copied:** a second copy of the limiter dance would drift, and R14 is a brute-force path against the AdGuard password.
- **Re-check uses the session's username; the body has no username field:** rejected accepting `{username, password}` (lets one admin's session check another admin's password).
- **SPA lock is a `locked` store, not a `/unlock` route:** the URL survives the lock and unlock returns the parent where they were; a route would need a "return-to" parameter, which is another thing to get wrong.
- **Login with a live device cookie tags the new session (t-11):** rejected leaving it untagged (a full login on a PIN device would silently disable the idle lock while the PIN still exists) and rejected deleting the device on login (data loss nobody asked for).
- **revoke-all returns a new `revoked_devices` count** alongside the unchanged `revoked` (sessions); rejected folding devices into `revoked`, which would change an existing field's meaning.
- **Waves follow code dependency only:** t-10 and t-12 both add one route line to `internal/api/api.go` and run in the same wave; under pair-mode sequential execution that is an ordering detail, not a conflict.
- **New dependency `golang.org/x/crypto`:** Go has no stdlib Argon2. It is the only new module, pinned in go.sum. Rejected hand-rolling Argon2.
- **Fixed test PIN `583920` with `time` stripped before log scans:** rejected scanning raw lines, because slog's wall-clock nanoseconds can contain any 6-digit run (R20).
