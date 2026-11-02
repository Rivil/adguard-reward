-- A persistent client may now belong to more than one child (a shared
-- device), so child_clients is keyed on the (client, child) pair instead of
-- the client alone. SQLite cannot alter a primary key in place: build the new
-- table, copy every row, drop the old one and take its name. Nothing
-- references child_clients, so the drop and rename touch no other table.
CREATE TABLE child_clients_new (
    client_name TEXT    NOT NULL,
    child_id    INTEGER NOT NULL REFERENCES children(id) ON DELETE CASCADE,
    PRIMARY KEY (client_name, child_id)
);

INSERT INTO child_clients_new (client_name, child_id)
    SELECT client_name, child_id FROM child_clients;

DROP TABLE child_clients;

ALTER TABLE child_clients_new RENAME TO child_clients;

-- The primary key's index leads with client_name, so it serves the
-- client-to-owners lookups; child_id needs its own for the cascade and for
-- reading one child's clients.
CREATE INDEX child_clients_child_id_idx ON child_clients (child_id);
