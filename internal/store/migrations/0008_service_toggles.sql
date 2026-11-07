-- A service toggle is a parent's permanent per-(child, service) intent: the
-- service stays blocked or unblocked for every client of the child until the
-- parent flips it again. Services never toggled have no row and stay
-- AdGuard-owned (locked drift_enforcement). service_id is a built-in AdGuard
-- id or "custom:<n>"; it carries no FK because built-ins live in AdGuard.
CREATE TABLE service_toggles (
    child_id   INTEGER NOT NULL REFERENCES children(id) ON DELETE CASCADE,
    service_id TEXT    NOT NULL,
    state      TEXT    NOT NULL CHECK (state IN ('blocked', 'unblocked')),
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (child_id, service_id)
);

-- A deleted custom service takes its toggles with it. The id is matched
-- exactly, never by prefix, so deleting custom:1 leaves custom:10 alone.
CREATE TRIGGER service_toggles_custom_delete
AFTER DELETE ON custom_services
BEGIN
    DELETE FROM service_toggles WHERE service_id = 'custom:' || OLD.id;
END;
