-- +goose Up

-- Generic key-value store for unknown/extension properties on any component.
-- owner_type: 'event', 'todo', 'journal'
-- owner_id: FK to the owning row's id
CREATE TABLE x_properties (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type TEXT NOT NULL,  -- 'event' | 'todo' | 'journal'
    owner_id INTEGER NOT NULL,
    name TEXT NOT NULL,        -- e.g. 'X-GOOGLE-CONFERENCE' or 'X-LIC-ERROR'
    value TEXT NOT NULL DEFAULT '',
    params TEXT NOT NULL DEFAULT '{}',  -- JSON object: {"KEY": ["val1"], "OTHER": ["val"]}
    CHECK (owner_type IN ('event', 'todo', 'journal')),
    CHECK (json_valid(params))
);

CREATE INDEX idx_xprops_owner ON x_properties(owner_type, owner_id);

-- Reject inserts for missing owners. Polymorphic ownership prevents a normal FK,
-- so these triggers enforce existence for each supported owner type.
-- +goose StatementBegin
CREATE TRIGGER x_properties_event_owner_exists
BEFORE INSERT ON x_properties
WHEN NEW.owner_type = 'event'
 AND NOT EXISTS (SELECT 1 FROM events WHERE id = NEW.owner_id) BEGIN
    SELECT RAISE(ABORT, 'x_properties owner does not exist');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER x_properties_todo_owner_exists
BEFORE INSERT ON x_properties
WHEN NEW.owner_type = 'todo'
 AND NOT EXISTS (SELECT 1 FROM todos WHERE id = NEW.owner_id) BEGIN
    SELECT RAISE(ABORT, 'x_properties owner does not exist');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER x_properties_journal_owner_exists
BEFORE INSERT ON x_properties
WHEN NEW.owner_type = 'journal'
 AND NOT EXISTS (SELECT 1 FROM journals WHERE id = NEW.owner_id) BEGIN
    SELECT RAISE(ABORT, 'x_properties owner does not exist');
END;
-- +goose StatementEnd

-- Clean up extension properties when the owning component is deleted.
-- +goose StatementBegin
CREATE TRIGGER events_x_properties_ad
AFTER DELETE ON events BEGIN
    DELETE FROM x_properties WHERE owner_type = 'event' AND owner_id = OLD.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER todos_x_properties_ad
AFTER DELETE ON todos BEGIN
    DELETE FROM x_properties WHERE owner_type = 'todo' AND owner_id = OLD.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER journals_x_properties_ad
AFTER DELETE ON journals BEGIN
    DELETE FROM x_properties WHERE owner_type = 'journal' AND owner_id = OLD.id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS journals_x_properties_ad;
DROP TRIGGER IF EXISTS todos_x_properties_ad;
DROP TRIGGER IF EXISTS events_x_properties_ad;
DROP TRIGGER IF EXISTS x_properties_journal_owner_exists;
DROP TRIGGER IF EXISTS x_properties_todo_owner_exists;
DROP TRIGGER IF EXISTS x_properties_event_owner_exists;
DROP TABLE IF EXISTS x_properties;
