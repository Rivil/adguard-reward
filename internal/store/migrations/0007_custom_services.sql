-- A custom service is a parent-named set of domains the app blocks for the
-- children through the managed user_rules region. Grants and buttons refer
-- to it as "custom:<id>", so AUTOINCREMENT keeps an id from ever being
-- reused: an ended grant's history must never name a later service.
-- name_key is the lowercased name: names are unique case-insensitively
-- (locked uniqueness); the built-in catalogue clash is checked by the API.
CREATE TABLE custom_services (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    name     TEXT    NOT NULL,
    name_key TEXT    NOT NULL UNIQUE
);

-- A domain belongs to at most one custom service (locked uniqueness), which
-- keeps per-domain rules single-owner. position keeps the parent's order.
CREATE TABLE custom_service_domains (
    service_id INTEGER NOT NULL REFERENCES custom_services(id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    domain     TEXT    NOT NULL UNIQUE,
    PRIMARY KEY (service_id, position)
);
