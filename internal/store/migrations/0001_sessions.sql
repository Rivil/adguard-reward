-- Sessions are keyed by rowid; only the SHA-256 of the raw token is stored.
-- Times are Unix seconds UTC.
CREATE TABLE sessions (
    id           INTEGER PRIMARY KEY,
    username     TEXT    NOT NULL,
    token_hash   BLOB    NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);

CREATE INDEX sessions_username_idx   ON sessions (username);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);
