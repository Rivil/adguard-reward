# pin-unlock — panel synthesis

Inputs: risk.md (13 tasks / 6 waves), mvp.md (7 / 4), verification.md (11 / 6). Repo claims checked against
source: `internal/auth/device.go` does not exist yet (all drafts create it); `golang.org/x/crypto` is not in go.mod
(new dependency in every draft); `foreign_keys(ON)` is set in `internal/store/store.go`, so `ON DELETE SET NULL`
will actually fire; the API test harness lives in `internal/api/login_test.go` (`newHarness`) and `TestLogin_Burst`
exists; `cmd/adguard-reward/main_test.go` already has `TestRun_SessionSurvivesRestart`, `TestRun_CookieSecure` and
`TestRun_TLS`, so the restart/TLS pattern is established; `web/src/lib/api.ts:170` calls `onUnauthorized()` on
**every** 401 except `LOGIN_PATH`. Last migration is `0009_grant_history.sql`.

## Scores

| Draft | Dimension | Score | Note |
|---|---|---|---|
| risk | criteria coverage | 5/5 | Every criterion and locked decision owned; only draft that catches "set PIN leaves session at +30d" (R7) and the session-escalation-on-revoke path (R5). |
| risk | test-contract specificity | 5/5 | Each contract names the failure mode it kills (R1–R21); boundary, race, log-scan-flake and wiring cases all concrete. |
| risk | granularity | 4/5 | All tasks ≤4 files and ≤2 layers; t-1 and t-3 are small but not merge candidates (different files/layers). |
| risk | wave correctness | 3/5 | t-11 contract calls `/unlock` (t-10, same wave) and t-12 contract unlocks with old/new PIN (t-10, same wave) — two contracts unsatisfiable at their wave. t-4's transport also routes the 401 bad_credentials its own t-8/t-12 expect to the login page. |
| mvp | criteria coverage | 3/5 | Nominally all seven covered, but no binary-level wiring/TLS proof, no concurrency bound on the counter, and 5th-strike delete leaves the tagged session alive at 30d. |
| mvp | test-contract specificity | 3/5 | Good on the 400 table and DB/log scan; weak elsewhere ("400/401", no race test, no "request after the first lock" case). |
| mvp | granularity | 2/5 | t-4 spans api + cmd, 5 files, 6 criteria; t-3 owns five criteria in one Manager task; t-1 is 6 files. |
| mvp | wave correctness | 4/5 | Dependencies are real; t-5 waits on t-4 only for harness wiring, acknowledged. |
| verification | criteria coverage | 5/5 | Full coverage plus restart/TLS/two-device e2e in main_test; deletes linked sessions on 5th strike. Misses R7 (session expiry on set). |
| verification | test-contract specificity | 5/5 | Proving-test column per criterion; mutant-aware cases (unicode.IsDigit, `!After()` boundary, `IS NULL` not 0). |
| verification | granularity | 3/5 | t-6 is a five-criterion Manager core; t-7 and t-8 are 5 files each (acknowledged). |
| verification | wave correctness | 5/5 | Explicitly avoids the risk draft's same-wave unlock dependency by proving revocation via `/me` in t-10 and via `/unlock` in t-11 e2e. |

**Skeleton: risk.** It has the finest failure-mode ownership and the most specific contracts; its two wave defects are
contract-level and fixable by grafting verification's routing of those assertions, whereas verification's coarse
t-6/t-8 would need re-cutting and mvp would need both re-cutting and new contracts.

## Merged plan

```
Phase pin-unlock — 13 tasks across 6 waves

Wave 1
  t-1  Add PIN validation and Argon2id PHC hashing                                [risk+verification+mvp]
       files:    internal/auth/pin.go, internal/auth/pin_test.go, go.mod, go.sum
       desc:     ValidatePIN (bytes '0'-'9' only, len 4-6); HashPIN → $argon2id$v=19$m=..,t=..,p=..$salt$hash with a
                 random 16-byte salt, defaults no lower than m=19456 KiB, t=2, p=1 [mvp]; VerifyPIN parses params from
                 the stored string, constant-time compare. Adds golang.org/x/crypto pinned to an exact version.
       covers:   c-1
       depends:  —
       contract:
         - ValidatePIN rejects "١٢٣٤" (Arabic-Indic), "１２３４" (fullwidth), " 1234", "1234\n", "123", "1234567",
           "12a4", "" and accepts "0000", "000000", "0123" — a unicode.IsDigit implementation fails the non-ASCII rows.
         - HashPIN output matches ^\$argon2id\$v=19\$m=\d+,t=\d+,p=\d+\$[A-Za-z0-9+/]+\$[A-Za-z0-9+/]+$, does not
           contain the PIN, and two hashes of the same PIN differ (if the salt is fixed, this fails).
         - VerifyPIN("0123", HashPIN("123")) is false (leading-zero/number coercion); VerifyPIN with a tampered salt is
           false; a "$argon2i$" string, a v=16 string or a truncated PHC returns an error and never true, never panics.
         - A hash built with m=8192,t=1,p=1 still verifies after defaults are raised (if params come from constants,
           this fails).

  t-2  Add devices schema, store and attempt reservation                           [risk+verification+mvp]
       files:    internal/store/migrations/0010_devices.sql, internal/auth/device.go, internal/store/devices.go,
                 internal/store/devices_test.go
       desc:     devices(id, username, token_hash UNIQUE, pin_hash CHECK LIKE '$argon2id$%', failed_attempts CHECK 0..5,
                 created_at, expires_at) + sessions.device_id NULL REFERENCES devices ON DELETE SET NULL. auth.Device
                 (LogValue = id+username) + auth.DeviceStore; store implements ReserveAttempt (atomic increment-first),
                 ResetAttempts, ReplacePIN, RevokeDevice (tagged sessions + device in one tx), DeleteDevice,
                 DeleteOtherDevices(username, keepID), DeleteExpiredDevices, TouchDevice.
       covers:   c-1, c-4, c-5, c-6
       depends:  —
       contract:
         - 20 goroutines calling ReserveAttempt on one device → exactly 5 succeed returning 1..5 once each and 15 are
           refused (read-then-write repeats a count and fails).
         - 3 ReserveAttempt, Close, re-Open on the same dir → the next ReserveAttempt returns 4 (in-memory counter
           fails) [mvp+verification].
         - RevokeDevice deletes the device AND every session whose device_id points at it, leaving other sessions
           untouched (bare SET NULL leaves the tagged session alive and fails — R5).
         - DeleteDevice (remove path) leaves the tagged session row with device_id NULL.
         - ResetAttempts after 3 reservations and ReplacePIN both leave failed_attempts 0; ReplacePIN swaps pin_hash
           and keeps token_hash [verification].
         - Inserting pin_hash "583920" or failed_attempts 6 fails the CHECK; a duplicate token_hash fails UNIQUE
           [mvp+verification].
         - DeleteOtherDevices("mum", A) removes mum's B and C, keeps A and dad's D, returns 2; keepID 0 removes all
           of mum's.
         - slog of a Device renders exactly {id, username}; neither hash appears.
         - schema_migrations lists 0010_devices.sql immediately after 0009_grant_history.sql.

  t-3  Extract shared password re-check from login                                [risk; verification agrees on extraction]
       files:    internal/api/login.go, internal/api/reauth.go, internal/api/reauth_test.go
       desc:     Move the Limiter double-check + loginSlot + AdGuard.Login + Fail sequence out of handleLogin into
                 recheckPassword(r, username, password), reused by login and PIN change/remove.
       covers:   c-5
       depends:  —
       contract:
         - Five wrong recheckPassword calls from 203.0.113.1 → the 6th is rate-limited with zero new /control/login
           hits, and a following POST /api/v1/login from that IP is 429 (one shared budget; a forked limiter fails).
         - An AdGuard 502 from the re-check consumes no budget: 6 unreachable re-checks, then a correct one passes.
         - The /control/login body the fake records names the username argument, never one from a request body.
         - TestLogin_Burst ("at most 5 AdGuard hits in a 20-request burst") still passes after the extraction.

  t-4  Route 401 by code in the SPA transport                                     [risk+verification]
       files:    web/src/lib/api.ts, web/src/lib/api.test.ts
       desc:     request(): 401 pin_required → onPinRequired (sets a `locked` store); other 401 → onUnauthorized;
                 neither for /api/v1/unlock or /api/v1/login. ApiError.attemptsRemaining. unlock, pinStatus, setPin,
                 changePin, removePin; Route 'settings' ↔ /settings; messageFor for bad_pin, device_revoked,
                 reauth_required.
       covers:   c-3, c-4, c-5, c-6
       depends:  —
       contract:
         - 401 {error:'pin_required'} from GET /api/v1/children calls the pin spy and NOT the login spy.
         - 401 {error:'bad_pin', attempts_remaining:3} from POST /api/v1/unlock calls neither spy and the ApiError
           has attemptsRemaining 3 (non-integer or missing → undefined).
         - 401 device_revoked and 401 unauthorized from /api/v1/unlock call neither spy (the screen decides).
         - 403 bad_credentials / 403 reauth_required from changePin or removePin call neither spy [verification; see
           Disagreement 3].
         - setPin('0123') sends body exactly {"pin":"0123"} (string, no password key); changePin sends {pin,password};
           removePin sends DELETE /api/v1/pin with {password}; all carry the CSRF header [verification].
         - navigate('settings') pushes /settings.

Wave 2
  t-5  Tag sessions with a device; 15-minute idle window                          [risk+verification]
       files:    internal/auth/auth.go, internal/auth/auth_test.go, internal/store/sessions.go,
                 internal/store/sessions_test.go
       desc:     Session.DeviceID (nullable); store persists/scans device_id. Authenticate slides a tagged session by
                 15 min, an untagged one by 30 days. Manager.IssueForDevice, AttachDevice (sets device_id AND
                 expires_at=now+15m), DetachDevice (clears it AND sets expires_at=now+30d).
       covers:   c-3, c-5
       depends:  t-2
       contract:
         - Fake clock, tagged session: Authenticate at +14m59s OK with ExpiresAt=now+15m; again +14m59s later still
           OK (proves the slide); then exactly +15m00s idle → ErrNoSession and the row is deleted (pins !After()).
         - Untagged session at +16m still authenticates with ExpiresAt=now+30d (a global lock fails this).
         - IssueForDevice(..., 7) → ExpiresAt=now+15m, DeviceID=7; plain Issue still now+30d [verification].
         - AttachDevice at T, no further request, Authenticate at T+15m01s → ErrNoSession (R7).
         - DetachDevice at T, no further request, Authenticate at T+16m → OK with ExpiresAt now+30d (R8).
         - Store round-trip: DeviceID 7 reads back 7; DeviceID 0 stores SQL NULL — a raw query asserts
           `device_id IS NULL`, not 0 [verification].

  t-6  Add PIN pad and lock state to App                                          [risk+mvp+verification]
       files:    web/src/lib/PinPad.svelte, web/src/lib/PinPad.test.ts, web/src/App.svelte, web/src/App.test.ts
       desc:     App renders PinPad whenever `locked` is set, keeping the current URL. bad_pin shows attempts left;
                 device_revoked or unauthorized from unlock clears lock and navigates to login; success clears lock
                 and re-mounts the route so it refetches.
       covers:   c-3, c-4, c-6
       depends:  t-4
       contract:
         - On /children, GET /api/v1/me → 401 pin_required renders the pad (no "Sign in" button) and pathname stays
           /children; 401 unauthorized renders "Sign in" [mvp+verification].
         - Tapping 1,2,3,4 and submitting POSTs /api/v1/unlock with exactly {pin:"1234"} [verification].
         - Wrong PIN → "4 attempts left" visible, pad still shown, input cleared, location unchanged.
         - 401 device_revoked from unlock → Login renders and pathname is /login.
         - Correct PIN (204) → pad gone, /api/v1/children fetched again and Children renders.
         - Two concurrent 401 pin_required from Home's fetches render exactly one pad.
         - Submit disabled below 4 digits; a non-digit keystroke never reaches the input value.

Wave 3
  t-7  Add device cookie and pin_required gate                                    [risk+verification]
       files:    internal/auth/device.go, internal/auth/middleware.go, internal/auth/auth.go,
                 internal/auth/middleware_test.go
       desc:     Options.Devices + DeviceLifetime (90d). IssueDevice (256-bit token, SHA-256 stored via newToken),
                 LookupDevice (expired → deleted on sight), DeviceCookie/ClearDeviceCookie with single-Set-Cookie
                 dedupe. RequireSession's 401 path: live device cookie → pin_required (session cookie cleared, device
                 cookie kept); otherwise plain unauthorized + device cookie cleared. RunSweeper also drops expired
                 devices (see Disagreement 10).
       covers:   c-2, c-3
       depends:  t-5
       contract:
         - IssueDevice returns a 43-char raw token; stored token_hash = sha256(decoded bytes) [verification].
         - Expired tagged session + live device cookie → 401 pin_required, session cookie cleared, no device Set-Cookie.
         - NO session cookie + live device cookie → 401 pin_required (R9: the request after the first lock).
         - Device row past 90d → 401 unauthorized, device cookie cleared (Max-Age<0), row deleted.
         - Malformed / unknown device cookie → 401 unauthorized and device cookie cleared (R10).
         - A successful authenticated request leaves the device's expires_at unchanged (R11, locked lifetime).
         - Device cookie: HttpOnly, SameSite=Strict, Path=/, Max-Age=7776000, Secure iff Options.Secure (checked
           both ways), name differs from the session cookie; set-then-clear on one response leaves exactly one
           device Set-Cookie.
         - DeviceStore error on the 401 path → 500 internal and neither cookie cleared.
         - RunSweeper tick removes a device at expires_at<=now and keeps one at now+1s.

  t-8  Add Settings screen for device PIN                                         [risk+verification+mvp]
       files:    web/src/lib/Settings.svelte, web/src/lib/Settings.test.ts, web/src/lib/Home.svelte,
                 web/src/App.svelte
       desc:     /settings route + Home nav button. Shows has_pin; first set = PIN + confirm only; change = PIN +
                 confirm + AdGuard password; remove = password. Errors keyed by code.
       covers:   c-5
       depends:  t-4, t-6
       contract:
         - has_pin false → no password field rendered; submit POSTs body exactly {"pin":"583920"}.
         - "12a4", "123" or a PIN/confirm mismatch → inline error and zero fetch calls [verification].
         - has_pin true → change POSTs {pin,password}; remove sends DELETE with {password}, then shows
           "No PIN on this device" without reload.
         - 403 bad_credentials → "Wrong password" shown, has_pin display unchanged, route stays 'settings' (not
           'login') [verification]; 429 → retry message with seconds.
         - Home's Settings button navigates to /settings and App renders Settings there.

Wave 4
  t-9  Add first-PIN set and PIN status endpoints                                 [risk+verification]
       files:    internal/api/pin.go, internal/api/pin_test.go, internal/api/api.go, internal/api/login_test.go
       desc:     Deps.Devices; GET /api/v1/pin {has_pin} from the session's device_id; POST /api/v1/pin {pin} on an
                 untagged session: hash, IssueDevice, AttachDevice, set device cookie. A tagged session without a
                 password is refused here (change lands in t-12). Harness wires Devices and keeps its data dir.
       covers:   c-1, c-2, c-3, c-5
       depends:  t-1, t-7
       contract:
         - POST {"pin":"583920"} → 204 + device cookie; pin_hash is PHC; token_hash = sha256(decoded cookie);
           /control/login hits unchanged across the POST (first PIN needs no password — locked pin_change_reauth).
         - "583920" and the raw device cookie are absent from a SELECT * dump of every table in sqlite_master, from
           the bytes of every file in the data dir (.db, -wal, -shm) [verification], and from the debug log buffer
           with each line's `time` attr removed (R3, R20).
         - 400 and no devices row for "123", "1234567", "12a4", "١٢٣٤", {"pin":1234} (number), missing pin, unknown
           key, >4 KiB body; no session → 401.
         - Fake clock at HTTP level: after set, GET /me at +14m59s → 200, then +15m00s idle → 401 pin_required; same
           request without the device cookie → 401 unauthorized [verification]; set at T with no further request,
           GET /me at T+15m01s → 401 pin_required (R7).
         - Tagged session POST {"pin":"111111"} with NO device cookie and no password → 403 reauth_required,
           pin_hash unchanged (R13: has-PIN is server state, not cookie presence).
         - GET /api/v1/pin is {has_pin:false} before and {has_pin:true} after.

Wave 5
  t-10 Add unlock endpoint with persisted strikes and log                         [risk+verification]
       files:    internal/api/unlock.go, internal/api/unlock_test.go, internal/api/api.go, internal/api/errors.go
       desc:     POST /api/v1/unlock {pin} outside requireSession. Absent/malformed cookie → outcome no_device (wire:
                 401 unauthorized); well-formed with no live row → device_revoked (+clear). ReserveAttempt before
                 Argon2; ok → ResetAttempts, TouchDevice(+90d), IssueForDevice(device.username), both cookies set;
                 5th failure → RevokeDevice. One event=unlock info line per attempt.
       covers:   c-2, c-4, c-6, c-7
       depends:  t-9
       contract:
         - Correct PIN, no session cookie → 204, new session cookie differing from the old, GET /me 200 with the
           device's username, /control/login count unchanged (R16).
         - The minted session is tagged: 15m idle after unlock → GET /me 401 pin_required [verification].
         - Wrong PINs → 401 bad_pin with attempts_remaining 4,3,2,1; 5th → 401 device_revoked, devices row gone,
           device cookie Max-Age<0; 6th with the same cookie (even with the right PIN) → device_revoked.
         - 3 wrong, 1 correct, 1 wrong → attempts_remaining 4 (reset).
         - 20 parallel wrong unlocks → exactly four bad_pin carrying {4,3,2,1} and sixteen device_revoked.
         - An unexpired session tagged to the device → after the 5th strike its next GET /me is 401 (R5 end-to-end).
         - Unlock at device age 89d23h → 204, expires_at = now+90d, device cookie Max-Age 90d; at 90d+1s with no
           unlock in between → device_revoked.
         - Malformed JSON / >4 KiB body → 400, no unlock log line, failed_attempts unchanged; "12a4" → bad_pin and
           one attempt consumed.
         - Each outcome ok|bad_pin|device_revoked (unknown token and 5th strike)|no_device emits exactly one line
           {event:unlock, username, ip, outcome} (username "" for no_device); ip equals the trusted-proxy-resolved
           client IP [verification]; with `time` stripped, the log contains neither the PIN, the raw device token,
           nor the new raw session token.

  t-11 Tear down devices on logout and revoke-all                                 [risk+verification]
       files:    internal/api/login.go, internal/api/sessions.go, internal/api/teardown_test.go
       desc:     Logout on a tagged session → RevokeDevice + clear both cookies. revoke-all → DeleteOtherDevices
                 (keep the caller's device, 0 if untagged) then DeleteOthers; response gains revoked_devices. Login
                 with a live same-user device cookie tags the new session; otherwise clears the device cookie.
       covers:   c-6
       depends:  t-3, t-9
       contract:
         - Logout on a PIN device → 204, device row and pin_hash gone, exactly one Set-Cookie each for session and
           device with Max-Age<0; GET /me with the old device cookie → plain 401 unauthorized, not pin_required (R17).
         - Logout without a PIN behaves as before; a seeded device of "dad" survives it [verification].
         - mum has A (caller), B, C; dad has D → revoke-all from A deletes B, C; A and D remain; body has
           revoked_devices=2; B's device cookie on GET /me → plain 401 unauthorized (R18). The `/unlock →
           device_revoked` half is proved in t-13, because t-10 is same-wave [verification].
         - revoke-all from an untagged session deletes all of mum's devices and none of dad's.
         - Full login with mum's live device cookie → new session tagged (GET /me at +15m01s → pin_required);
           with dad's device cookie as mum → device cookie cleared, session untagged (30d).

  t-12 Add PIN change and remove with re-auth                                     [risk+verification]
       files:    internal/api/pin.go, internal/api/pin_change_test.go, internal/api/api.go
       desc:     POST /api/v1/pin {pin,password} on a tagged session → recheckPassword(session.Username), then
                 ReplacePIN (resets counter, keeps token). DELETE /api/v1/pin {password} → recheck, DetachDevice,
                 DeleteDevice, clear device cookie.
       covers:   c-5
       depends:  t-3, t-9
       contract:
         - Change with the right password with failed_attempts seeded to 3 → 204, pin_hash differs and
           VerifyPIN(new) holds, failed_attempts 0, device cookie value unchanged, exactly +1 /control/login hit
           [verification; replaces risk's unlock-based check, which needed same-wave t-10].
         - Change/remove with a wrong password → 403 bad_credentials, pin_hash and device row unchanged; the 6th
           wrong attempt → 429 with Retry-After and zero new /control/login hits (R14).
         - Missing password on change/remove → 403 reauth_required with zero /control/login hits.
         - The recorded /control/login body names the session's username; a body with a "username" key → 400.
         - Remove → 204, device row gone, device cookie Max-Age<0, session device_id NULL, and GET /me at +16m is 200
           with expires_at ≈ now+30d (R8).
         - DELETE /api/v1/pin on an untagged session → 404 not_found.
         - GET /api/v1/pin is has_pin false after DELETE.
       note:     t-10 and t-12 both add a route line to api.go; under parallel execution run them sequentially.

Wave 6
  t-13 Wire devices into main; prove restart, TLS and cross-device revoke         [risk+verification]
       files:    cmd/adguard-reward/main.go, cmd/adguard-reward/main_test.go
       desc:     Pass st as auth.Options.Devices and api.Deps.Devices; device cookie inherits the existing Secure
                 decision; sweeper covers devices.
       covers:   c-2, c-4, c-6
       depends:  t-10, t-11, t-12
       contract:
         - run(): set PIN, 2 wrong unlocks, stop, start on the same data_dir, 3rd wrong unlock → attempts_remaining 2,
           not 4 (R15); the pre-restart device cookie + correct PIN → 204 (Devices nil in main fails at set-PIN with
           500 — R21).
         - base_url https:// or TLS listener → device cookie Secure; plain http → not Secure.
         - Two clients each set a PIN; A calls revoke-all; B's POST /unlock with the right PIN → 401 device_revoked;
           A's unlock → 204 [verification].
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 POST /pin, 4-6 ASCII digits else 400, Argon2id PHC only, no plaintext in DB/logs | t-1, t-2, t-9 |
| c-2 256-bit device token / SHA-256, cookie attrs + Secure under TLS, unlock → 204 + fresh session, zero /control/login | t-7, t-9, t-10, t-13 |
| c-3 15-min idle, pin_required vs plain 401, SPA pad, fake clock both sides | t-5, t-7, t-9, t-4, t-6 |
| c-4 attempts_remaining, 5th revokes + clears cookie, reset on correct, persisted across restart, SPA login | t-2, t-10, t-13, t-4, t-6 |
| c-5 settings screen; change replaces hash + resets counter; remove deletes token + hash, cookie cleared, 30-day session continues | t-2, t-3, t-5, t-9, t-12, t-4, t-8 |
| c-6 revoke-all deletes the user's other devices; revoked unlock → device_revoked; SPA login | t-2, t-11, t-13, t-4, t-6 |
| c-7 one structured unlock log line per outcome; no PIN / raw token in logs | t-10 |

| Locked decision | Tasks |
|---|---|
| idle_window (15 min, sliding on every authenticated request) | t-5 (slide + boundary), t-7 (pin_required on expiry), t-9 (window starts at set, HTTP boundary), t-10 (minted session tagged), t-11 (login on PIN device tagged) |
| device_token_lifetime (90 d, sliding on successful unlock only) | t-7 (90d lookup, Max-Age, no slide on normal requests), t-10 (TouchDevice on ok; 89d23h / 90d+1s) |
| pin_change_reauth (change/remove need AdGuard password via /control/login + login rate limit; first PIN needs session only) | t-3 (shared limiter path), t-9 (first set: zero login hits; refuses change without password), t-12 (re-check + 429), t-8 (password field only when has_pin) |
| logout_semantics (logout ends session and deletes device token + PIN hash) | t-11 |
| unlock_identity (session minted for the device row's username) | t-2 (devices.username), t-10 (IssueForDevice(device.username), /me username) |

## Disagreements

1. **What happens to a live session when its device is revoked by the 5th wrong PIN.**
   risk (RevokeDevice) and verification (RecordFailure on the 5th) delete the device's tagged sessions in the same
   transaction. mvp deletes only the device row and relies on `ON DELETE SET NULL`. All three use SET NULL for
   PIN removal (c-5).
   *Default:* delete tagged sessions on revocation (t-2 RevokeDevice, t-10 R5 end-to-end), and detach via SET NULL only on remove.
   *Why it matters:* `/unlock` sits outside requireSession. Under mvp's design, anyone holding an unlocked phone can
   send 5 wrong PINs, which turns the 15-minute PIN session into an untagged 30-day sliding session with no lock.
   That is a privilege escalation, and c-4 reads as a security control. It also decides what logout does, since logout reuses RevokeDevice.

2. **How the 5-strike counter is enforced under concurrency.**
   risk: `ReserveAttempt` increments in SQL *before* Argon2, capped by `CHECK 0..5`. A correct PIN spends a
   reservation and then resets the count. verification: Argon2 runs first, then an atomic `RecordFailure` that
   deletes on the 5th (`CHECK 0..4`), plus an in-process mutex on Manager.Unlock to bound Argon2 memory. mvp:
   increment after verify, with no concurrency contract.
   *Default:* risk's increment-first reservation. It bounds both guesses and Argon2 work per device without any
   process state, and the 20-goroutine contract proves it.
   *Why it matters:* with a read-then-write counter, N parallel requests each get a guess, so the "5 tries" bound
   in c-4 can be bypassed. Choosing verification's model changes the CHECK range, the store API
   (RecordFailure vs ReserveAttempt), and adds a mutex that a restart does not preserve. On a Pi-class host,
   unbounded parallel Argon2id (≥19 MiB each) is also a memory-DoS concern.

3. **Status code for a wrong or missing password on PIN change/remove.**
   risk and mvp: 401 bad_credentials (mvp: "400/401" for missing). verification: 403 bad_credentials, and
   403 reauth_required when the password is missing. risk: 400 when it is missing.
   *Default:* 403 bad_credentials / 403 reauth_required (t-4, t-8, t-9, t-12).
   *Why it matters:* `web/src/lib/api.ts:170` currently sends every non-login 401 to `onUnauthorized()` →
   `navigate('login')`. risk's own t-4 keeps "other 401 → onUnauthorized" and exempts only /unlock and /login. A
   mistyped password in Settings would therefore throw the parent to the login page, which contradicts risk's own
   t-8 contract. Keeping 401 instead requires the transport to exempt /api/v1/pin or to route by code. Either way, the server
   status and the SPA routing have to be decided together.

4. **Where device and unlock logic lives, and how the API work is cut.**
   risk: thin Manager primitives (IssueForDevice, Attach/Detach, IssueDevice, LookupDevice). Unlock orchestration
   lives in the handler over store primitives, and the endpoints are split three ways: first-set (t-9), unlock (t-10),
   change/remove (t-12). verification: a single `auth.Manager.Unlock` that returns an outcome table, with
   SetFirstPin/ChangePin/RemoveDevice in one auth task (t-6) and GET/POST/DELETE /pin in one API task (t-8).
   mvp: everything in one Manager task (t-3) and one API task (t-4, which also wires main).
   *Default:* risk's split, which is the skeleton.
   *Why it matters:* the Manager-centric model lets fake-clock/fake-store tests pin the 90d and 15m boundaries without HTTP
   and keeps the outcome mapping in one place. The handler-centric model keeps each task ≤4 files and gives each
   failure mode one owner. Switching later would move most unlock contracts from internal/api to internal/auth.

5. **Whether a binary-level wiring/restart/TLS task exists.**
   risk (t-13) and verification (t-11) prove restart survival, the Secure cookie under TLS and wiring in
   `cmd/adguard-reward/main_test.go`. mvp wires main inside the endpoint task and proves persistence only with a
   store reopen test.
   *Default:* both. The store reopen test is grafted into t-2 and the main_test task is kept as t-13.
   *Why it matters:* without t-13, a nil `Devices` in main would leave every API test green while the binary is
   broken (R21). c-2's "Secure under TLS" and c-4's "restart does not reset" would then be proved only against the
   harness, which cannot restart a process.

6. **How the SPA shows the lock: overlay state or a route.**
   risk and mvp: a `locked` store with the PIN pad drawn over the current URL. verification: a dedicated `/unlock` route
   (Unlock.svelte), with pin_required navigating to it.
   *Default:* the `locked` store (t-4, t-6).
   *Why it matters:* the overlay keeps the parent's place (/children?…) without a return-to parameter. The route is
   simpler to deep-link and test, but it needs return-to handling, or every unlock lands on Home. This changes the
   App.svelte structure and the t-4/t-6 contracts.

7. **Password re-check: extract a shared helper or call the limiter inline.**
   risk: extract `recheckPassword` from handleLogin as its own wave-1 task. verification: the same extraction, done
   inside its /pin task (t-8). mvp: call Limiter/loginSlot directly in pin.go, so the PIN task never touches login.go.
   *Default:* extract it, as a separate wave-1 task (t-3).
   *Why it matters:* a second copy of the double-check-plus-slot sequence can drift from login. R14 is a
   brute-force path against the AdGuard admin password, and the locked pin_change_reauth decision requires the same limit.
   The cost is that login.go is touched in t-3 and again in t-11.

8. **A full login on a device that still has a live device cookie.**
   risk and verification: a same-user device cookie tags the new session, so the idle lock still applies. For a
   different user's device cookie, risk clears the cookie, while verification leaves the session unlinked and says
   nothing about the cookie. mvp does not address the case.
   *Default:* tag on a same-user cookie and clear on a different-user cookie (risk; t-11).
   *Why it matters:* without tagging, typing the password on a PIN device produces a 30-day unlocked session while
   the PIN still exists. That arguably breaks c-3's "on a device with a PIN". Clearing versus keeping a different
   user's cookie decides whether a shared phone keeps the other parent's PIN.

9. **Wire code for an unlock attempt with no device cookie.**
   mvp's PIN pad, and arguably risk's t-6, handle an error code `no_device`. risk's t-10 lists only bad_pin,
   device_revoked and pin_required. verification returns plain `401 unauthorized` and logs `outcome=no_device`.
   *Default:* plain 401 unauthorized on the wire, with no_device only in the c-7 log (t-10, t-6).
   *Why it matters:* the choice adds a public error code or doesn't. The SPA behaves the same either way (go to
   login), so the stakes are low, but t-4/t-6/t-10 have to agree.

10. **Eager sweep of expired device rows.**
    risk extends RunSweeper to delete expired devices. mvp explicitly rejects this because no criterion needs it
    and expiry is checked lazily. verification deletes only on sight.
    *Default:* keep the skeleton's sweeper (t-7, t-13). It is one tick contract.
    *Why it matters:* without it, an abandoned phone's row, including its Argon2 hash of a 4-6-digit PIN (≤10^6
    candidates, so it can be cracked offline from a DB copy), stays in the database indefinitely. With it, there is one more
    background path to test. The risk is small, but the trade-off belongs to the user.
