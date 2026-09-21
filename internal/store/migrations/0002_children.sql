-- A child is a name plus the AdGuard persistent clients (by name) that belong
-- to it. Names are unique case-insensitively; a client name belongs to at most
-- one child. Rows reference clients by name only — no MAC/IP ids — because
-- name is the only key /control/clients/update and $client= rules accept.
CREATE TABLE children (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    created_at INTEGER NOT NULL
);

CREATE TABLE child_clients (
    client_name TEXT    PRIMARY KEY,
    child_id    INTEGER NOT NULL REFERENCES children(id) ON DELETE CASCADE
);

CREATE INDEX child_clients_child_id_idx ON child_clients (child_id);
