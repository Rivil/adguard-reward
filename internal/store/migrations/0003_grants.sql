-- A grant temporarily unblocks one or more services for one child. Times are
-- Unix seconds UTC. There is deliberately no FK to children: the grant stores
-- the client names it unblocked at creation and must outlive its child so the
-- block is still restored at expiry (locked client_set).
CREATE TABLE grants (
    id         INTEGER PRIMARY KEY,
    child_id   INTEGER NOT NULL,
    status     TEXT    NOT NULL CHECK (status IN ('active', 'expired', 'ended')),
    started_at INTEGER NOT NULL,
    ends_at    INTEGER NOT NULL,
    ended_at   INTEGER
);

CREATE INDEX grants_status_ends_at_idx ON grants (status, ends_at);

-- active mirrors grants.status = 'active' so the partial unique index below can
-- enforce one live grant per (child, service) without a join.
CREATE TABLE grant_services (
    grant_id   INTEGER NOT NULL REFERENCES grants(id) ON DELETE CASCADE,
    child_id   INTEGER NOT NULL,
    service_id TEXT    NOT NULL,
    active     INTEGER NOT NULL,
    PRIMARY KEY (grant_id, service_id)
);

CREATE UNIQUE INDEX grant_services_live_idx ON grant_services (child_id, service_id) WHERE active = 1;

CREATE TABLE grant_clients (
    grant_id    INTEGER NOT NULL REFERENCES grants(id) ON DELETE CASCADE,
    client_name TEXT    NOT NULL,
    PRIMARY KEY (grant_id, client_name)
);
