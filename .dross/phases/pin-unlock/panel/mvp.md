# pin-unlock — mvp draft

```
Phase pin-unlock — 7 tasks across 4 waves

Wave 1
  t-1  Add Argon2id PIN hashing and device types
       files:    internal/auth/pin.go, internal/auth/pin_test.go, internal/auth/device.go,
                 internal/auth/auth.go, go.mod, go.sum
       desc:     pin.go: ValidPIN (4-6 ASCII '0'-'9' only), HashPIN -> PHC "$argon2id$v=19$m=..,t=..,p=..$salt$hash"
                 (golang.org/x/crypto/argon2), VerifyPIN (constant-time). device.go: Device struct (ID, Username,
                 TokenHash, PINHash, FailedAttempts, CreatedAt, ExpiresAt), DeviceStore interface, DeviceCookieName,
                 DeviceLifetime = 90d, device cookie builder reusing newToken/hashToken. auth.go: Session.DeviceID
                 (0 = not a PIN device) and Options.Devices — fields only, no behaviour.
       covers:   c-1, c-2
       contract: ValidPIN accepts "1234"/"123456" and rejects "123", "1234567", "12a4", "" and fullwidth "１２３４";
                 HashPIN("4821") output matches ^\$argon2id\$v=19\$m=\d+,t=\d+,p=\d+\$ and does not contain "4821";
                 VerifyPIN against that hash is true for "4821", false for "4822" and false for a hash whose
                 salt was altered; two HashPIN calls on the same PIN differ (salt is random);
                 device cookie is HttpOnly, SameSite=Strict, Path=/, MaxAge=90d, Secure only when Options.Secure
       depends:  —

  t-6  SPA lock flow: PIN pad on pin_required
       files:    web/src/lib/api.ts, web/src/lib/PinPad.svelte, web/src/App.svelte, web/src/App.test.ts
       desc:     api.ts: ApiError gains attemptsRemaining (from envelope attempts_remaining); a `locked` store set by a
                 401 pin_required (any other 401 still routes to login); unlock(pin) on POST /api/v1/unlock excluded
                 from the generic 401 handler like LOGIN_PATH; pinStatus/setPin/removePin client fns and a
                 'settings' Route/path (consumed by t-7). PinPad.svelte: digit entry, submit, shows attempts left;
                 device_revoked or no_device -> clear lock, navigate('login'). App.svelte renders PinPad while locked.
       covers:   c-3, c-4, c-6
       contract: /me answering 401 {error:"pin_required"} renders the PIN pad and no "Sign in" button, while
                 401 {error:"unauthorized"} renders "Sign in"; a wrong-PIN 401 {error:"bad_pin",attempts_remaining:3}
                 keeps the pad and shows "3"; 401 {error:"device_revoked"} from /unlock replaces the pad with the
                 full login page; a 204 from /unlock re-fetches /me and shows Home
       depends:  —

Wave 2
  t-2  Add devices table and device store
       files:    internal/store/migrations/0010_devices.sql, internal/store/devices.go,
                 internal/store/sessions.go, internal/store/devices_test.go
       desc:     0010: devices(id, username, token_hash BLOB UNIQUE, pin_hash TEXT NOT NULL, failed_attempts INTEGER
                 NOT NULL DEFAULT 0, created_at, expires_at) + sessions.device_id INTEGER REFERENCES devices(id)
                 ON DELETE SET NULL. devices.go implements auth.DeviceStore (insert, by-hash, set PIN+reset counter,
                 increment-and-return counter, reset counter + slide expiry, delete, delete-others-of-user,
                 set session device). sessions.go reads/writes device_id.
       covers:   c-1, c-3, c-4, c-6
       contract: 3 failed attempts, Close, re-Open on the same dir -> counter reads 3 (persisted, c-4);
                 SetPIN resets failed_attempts to 0 and replaces pin_hash; DeleteOtherDevices("mum", keep)
                 leaves keep and every "dad" device, removes mum's others; duplicate token_hash insert fails on the
                 UNIQUE constraint; a session inserted with DeviceID round-trips it, and deleting that device leaves
                 the session row with DeviceID 0 (ON DELETE SET NULL)
       depends:  t-1

  t-3  Device-aware sessions, unlock and revoke in Manager
       files:    internal/auth/auth.go, internal/auth/middleware.go, internal/auth/device.go,
                 internal/auth/auth_test.go
       desc:     Authenticate slides by 15m when Session.DeviceID != 0, else 30d. RequireSession's 401 is
                 code pin_required when the request carries a device cookie naming a live (unexpired) device row,
                 plain unauthorized otherwise. Manager methods: IssueDevice (insert row, link session), Unlock
                 (verify PIN; ok -> reset counter, slide device to +90d, mint session for the device's username with
                 DeviceID; bad -> increment, 5th deletes row and reports revoked; unknown/expired -> revoked),
                 ChangePIN, RemoveDevice (delete row, unlink session, re-slide it to +30d), RevokeOtherDevices.
       covers:   c-2, c-3, c-4, c-5, c-6
       contract: fake clock: PIN-device session advanced 14m59s passes and slides; advanced another 15m01s gets 401
                 pin_required with a live device cookie, 401 unauthorized with no device cookie, and 401 unauthorized
                 when the device row is expired; a non-device session survives 15m01s idle;
                 Unlock: 4 wrong -> attempts_remaining 1, 5th wrong -> revoked and row gone; wrong, wrong, right,
                 then 4 wrong still not revoked (correct PIN reset the counter); a second Manager over the same fake
                 store continues the count (no in-memory counter); a successful unlock moves device expiry to now+90d
                 and the minted session's Username equals the device row's username; RemoveDevice leaves the
                 session alive at +30d
       depends:  t-1

  t-7  Settings screen to set, change, remove PIN
       files:    web/src/lib/Settings.svelte, web/src/lib/Settings.test.ts, web/src/App.svelte,
                 web/src/lib/Home.svelte
       desc:     Settings.svelte: GET /api/v1/pin -> "This device has a PIN" / "No PIN"; set form (PIN only) when
                 none, change form (new PIN + AdGuard password) and Remove (password) when one exists; errors via
                 messageFor. Home nav gains "Settings"; App renders Settings on the 'settings' route.
       covers:   c-5
       contract: has_pin:false shows a set form with no password field and POSTs {pin} to /api/v1/pin;
                 has_pin:true shows a password field and POSTs {pin,password}; Remove sends DELETE /api/v1/pin
                 with {password} and then shows "No PIN"; a 400 bad_request from POST shows the server message;
                 the Home nav "Settings" button navigates to /settings
       depends:  t-6

Wave 3
  t-4  PIN and unlock endpoints with audit log
       files:    internal/api/pin.go, internal/api/pin_test.go, internal/api/api.go,
                 internal/api/login_test.go, cmd/adguard-reward/main.go
       desc:     GET /api/v1/pin {has_pin}; POST /api/v1/pin {pin[,password]} (first PIN needs only the session;
                 change needs password re-checked via AdGuard.Login under Limiter + loginSlot); DELETE /api/v1/pin
                 {password}; POST /api/v1/unlock {pin} outside requireSession, 401 bodies carry attempts_remaining,
                 one info log {event:unlock, username, ip, outcome}. Wire auth.Options.Devices=st in main and the
                 harness (harness also keeps its data dir for the DB scan).
       covers:   c-1, c-2, c-3, c-4, c-5, c-7
       contract: POST /pin with "123", "1234567", "12a4", "１２３４" -> 400, "4821" -> 204 + Set-Cookie
                 adguard_reward_device (HttpOnly, SameSite=Strict, Path=/); after it, the devices row pin_hash
                 starts "$argon2id$" and neither "4821" nor the raw device token appears in any devices/sessions
                 column, the DB file bytes, or the debug-level log buffer;
                 unlock with the right PIN -> 204 + new session cookie, and fake.CountRequests(POST,/control/login)
                 is unchanged across the unlock; clock +15m01s then GET /me -> 401 pin_required with the device
                 cookie, 401 unauthorized without it;
                 wrong PIN -> 401 bad_pin attempts_remaining 4..1, 5th -> 401 device_revoked + device cookie
                 cleared (Max-Age<0) and the next unlock -> device_revoked;
                 second POST /pin without password -> 400/401 and the hash is unchanged; with a wrong password ->
                 401 bad_credentials and Limiter budget consumed (6th attempt 429); with the right password the
                 hash changes and failed_attempts is 0; DELETE /pin removes the row, clears the device cookie, and
                 the same session still answers GET /me after clock +16m;
                 one "unlock" log line per attempt with outcome ok / bad_pin / device_revoked / no_device
                 (no device cookie) and the PIN and raw device token absent from all log output
       depends:  t-2, t-3

Wave 4 (depends t-4)
  t-5  Logout and revoke-all remove device tokens
       files:    internal/api/login.go, internal/api/sessions.go, internal/api/sessions_test.go
       desc:     handleLogout also deletes the session's device row and clears the device cookie;
                 handleSessionsRevokeAll also calls RevokeOtherDevices(username, sess.DeviceID).
       covers:   c-6
       contract: on a PIN device, POST /logout removes the device row and sets a clearing device cookie, and a
                 following /unlock with the old device cookie -> 401 device_revoked; revoke-all from device A
                 leaves A's device row and deletes mum's device B, after which B's /unlock -> 401 device_revoked
                 while another username's device survives
       depends:  t-4
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 set PIN, 4-6 ASCII digits, Argon2id PHC only, no plaintext in DB/logs | t-1 (validation, PHC hash), t-2 (pin_hash column), t-4 (endpoint, 400 table, DB+log scan) |
| c-2 device token + cookie, unlock -> 204 + session, zero /control/login | t-1 (token, cookie attrs), t-3 (Unlock mints session), t-4 (endpoint, fake login count) |
| c-3 15m idle -> 401 pin_required vs plain 401, SPA PIN pad, fake-clock both sides | t-2 (sessions.device_id), t-3 (window + middleware code, boundary test), t-4 (e2e 401 codes), t-6 (PIN pad) |
| c-4 attempts_remaining, 5th revokes, reset on correct, persisted counter, SPA login | t-2 (persisted counter), t-3 (counter/revoke/reset logic), t-4 (HTTP shape, cookie cleared), t-6 (login after device_revoked) |
| c-5 settings: status, set/change/remove; change resets counter; remove keeps 30d session | t-3 (RemoveDevice re-slides 30d), t-4 (endpoints), t-7 (screen) |
| c-6 revoke-all deletes other devices; revoked unlock -> device_revoked, SPA login | t-2 (DeleteOtherDevices), t-3 (RevokeOtherDevices), t-5 (handler), t-6 (SPA login) |
| c-7 one unlock log line per outcome, no PIN/token in logs | t-4 |

Locked decisions:
- idle_window (15m sliding on PIN device) — t-3 (Authenticate lifetime by DeviceID; fake-clock boundary test), t-4 e2e.
- device_token_lifetime (90d, sliding on successful unlock) — t-1 (DeviceLifetime, cookie MaxAge), t-3 (Unlock slides expiry; expired row -> revoked).
- pin_change_reauth (change/remove need AdGuard password via /control/login under login rate limit; first PIN session-only) — t-4 (Limiter + loginSlot, 429 contract), t-7 (password field only when has_pin).
- logout_semantics (logout deletes device token + PIN) — t-5.
- unlock_identity (session minted for the device row's username) — t-3 (contract asserts Username), t-4.

## Judgment calls

- Device logic lives on auth.Manager (via Options.Devices), not a new api.Deps field — RequireSession needs the device store for pin_required anyway, and one owner keeps the fake-store tests in internal/auth.
- PIN hash lives on the device row (one `devices` table), not a separate pins table — a device exists iff it has a PIN, so remove/revoke is a single DELETE.
- Session ↔ device link is a nullable sessions.device_id with ON DELETE SET NULL, not a per-session lifetime column — the 15m/30d choice derives from it, and remove-PIN just unlinks.
- No sweeper for expired device rows — expiry is checked lazily on unlock and in the pin_required check; no criterion needs eager deletion.
- No separate unlock rate limiter — the persisted 5-strike counter is the brute-force bound c-4 asks for; no_device attempts cost one hash lookup and no Argon2 work.
- Re-auth for change/remove calls Limiter/loginSlot directly in pin.go rather than refactoring handleLogin — avoids pulling login.go into t-4 (it's t-5's file).
- t-5 is its own wave-4 task rather than folded into t-4 — merging would put 8 files in one task; it waits on t-4 only for the harness wiring.
- SPA work starts in wave 1 against mocked fetch (the error codes are fixed by the spec), and all api.ts changes sit in t-6 so t-7 never touches it.
- Adds golang.org/x/crypto (Go-team module) for argon2 — the only new dependency; hand-rolling Argon2id is not an option. go.mod/go.sum are counted in t-1 but are lockfile churn, not work.
- Argon2id parameters left to the executor at no less than OWASP's minimum (m=19456 KiB, t=2, p=1), so unlock stays fast on a Pi-class AdGuard host.
