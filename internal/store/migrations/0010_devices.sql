-- PIN devices: a browser that holds a device cookie and the PIN that unlocks
-- it. Only the SHA-256 of the raw device token and an Argon2id PHC hash of
-- the PIN are stored. Times are Unix seconds UTC.
CREATE TABLE devices (
    id              INTEGER PRIMARY KEY,
    username        TEXT    NOT NULL,
    token_hash      BLOB    NOT NULL UNIQUE,
    -- GLOB, not LIKE: LIKE is case-insensitive and would admit $ARGON2ID$.
    pin_hash        TEXT    NOT NULL CHECK (pin_hash GLOB '$argon2id$*'),
    -- Consecutive wrong PINs. The 5th revokes the device, so 5 is the cap
    -- and an increment past it fails the CHECK rather than wrapping.
    failed_attempts INTEGER NOT NULL DEFAULT 0 CHECK (failed_attempts BETWEEN 0 AND 5),
    created_at      INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL
);

CREATE INDEX devices_username_idx   ON devices (username);
CREATE INDEX devices_expires_at_idx ON devices (expires_at);

-- A session tagged with a device is a PIN session (15-minute idle window).
-- Every existing session reads NULL: untagged, 30-day sliding, as before.
-- Deleting a device detaches its sessions; revocation deletes them
-- explicitly first.
ALTER TABLE sessions ADD COLUMN device_id INTEGER REFERENCES devices (id) ON DELETE SET NULL;

CREATE INDEX sessions_device_id_idx ON sessions (device_id);
