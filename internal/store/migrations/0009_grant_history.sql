-- Grant history: how each grant ended, every extension, every service a
-- permanent block trimmed off it, and the service names as the parent saw
-- them at grant start. Nothing here rewrites existing rows: a pre-upgrade
-- grant keeps its status and times and reads end_reason NULL, so history
-- never claims a reason it does not know (locked legacy_end_reason).

-- end_reason is NULL for every live grant and every pre-upgrade grant.
ALTER TABLE grants ADD COLUMN end_reason TEXT
    CHECK (end_reason IS NULL OR end_reason IN ('expired', 'ended_early', 'permanent_block'));

-- SQLite reuses a deleted child's id (no AUTOINCREMENT), so a child's history
-- keeps only grants with id above the highest grant id at its creation.
-- Children that predate this migration read 0 and see all their grants.
ALTER TABLE children ADD COLUMN history_after_grant_id INTEGER NOT NULL DEFAULT 0;

-- The display name of each service, snapshotted when the grant starts
-- (locked service_names). Pre-upgrade grants have no rows here.
CREATE TABLE grant_service_names (
    grant_id   INTEGER NOT NULL REFERENCES grants(id) ON DELETE CASCADE,
    service_id TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    PRIMARY KEY (grant_id, service_id)
);

-- A service a permanent block trimmed off a live grant. grant_services keeps
-- its DELETE-on-trim so the live paths are untouched; this log is what lets
-- history still list the service.
CREATE TABLE grant_service_removals (
    grant_id   INTEGER NOT NULL REFERENCES grants(id) ON DELETE CASCADE,
    service_id TEXT    NOT NULL,
    removed_at INTEGER NOT NULL,
    reason     TEXT    NOT NULL CHECK (reason IN ('permanent_block')),
    PRIMARY KEY (grant_id, service_id)
);

-- One row per extension, never upserted, so a grant extended twice keeps
-- both entries.
CREATE TABLE grant_extensions (
    id          INTEGER PRIMARY KEY,
    grant_id    INTEGER NOT NULL REFERENCES grants(id) ON DELETE CASCADE,
    extended_at INTEGER NOT NULL,
    old_ends_at INTEGER NOT NULL,
    new_ends_at INTEGER NOT NULL
);

CREATE INDEX grant_extensions_grant_idx ON grant_extensions (grant_id, id);

-- Per-child history reads newest first by id.
CREATE INDEX grants_child_history_idx ON grants (child_id, id);
