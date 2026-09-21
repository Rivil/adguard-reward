-- A button is a preset tap on Home: one child, one or more services, one
-- duration. The list is replaced whole on every PUT, so AUTOINCREMENT keeps an
-- id from ever being reused across replacements; a button has no reason to
-- outlive its child (unlike a grant), hence the cascade.
CREATE TABLE buttons (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    position INTEGER NOT NULL UNIQUE,
    label    TEXT    NOT NULL,
    child_id INTEGER NOT NULL REFERENCES children(id) ON DELETE CASCADE,
    duration INTEGER NOT NULL CHECK (duration > 0) -- seconds
);

-- position keeps the services in the order the parent gave them: the first
-- one supplies the button's icon (locked button_icon).
CREATE TABLE button_services (
    button_id  INTEGER NOT NULL REFERENCES buttons(id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    service_id TEXT    NOT NULL,
    PRIMARY KEY (button_id, service_id)
);
