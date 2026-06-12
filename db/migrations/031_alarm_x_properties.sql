-- +goose Up

-- Extend x_properties polymorphic ownership to alarms so VALARM X-*
-- properties survive import → export round-trips instead of being dropped.
-- SQLite cannot alter a CHECK constraint, so rebuild the table. The
-- BEFORE INSERT triggers on x_properties die with the DROP and are
-- recreated below. The AFTER DELETE cleanup triggers live on the owner
-- tables and reference x_properties by name, so they must be dropped
-- before the rebuild (the rename reparses the schema and would choke on
-- a trigger pointing at a missing table) and recreated after.
DROP TRIGGER IF EXISTS events_x_properties_ad;
DROP TRIGGER IF EXISTS todos_x_properties_ad;
DROP TRIGGER IF EXISTS journals_x_properties_ad;

CREATE TABLE x_properties_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type TEXT NOT NULL,  -- 'event' | 'todo' | 'journal' | 'event_alarm' | 'todo_alarm'
    owner_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    value TEXT NOT NULL DEFAULT '',
    params TEXT NOT NULL DEFAULT '{}',
    CHECK (owner_type IN ('event', 'todo', 'journal', 'event_alarm', 'todo_alarm')),
    CHECK (json_valid(params))
);

INSERT INTO x_properties_new (id, owner_type, owner_id, name, value, params)
SELECT id, owner_type, owner_id, name, value, params FROM x_properties;

DROP TABLE x_properties;
ALTER TABLE x_properties_new RENAME TO x_properties;

CREATE INDEX idx_xprops_owner ON x_properties(owner_type, owner_id);

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

-- +goose StatementBegin
CREATE TRIGGER x_properties_event_alarm_owner_exists
BEFORE INSERT ON x_properties
WHEN NEW.owner_type = 'event_alarm'
 AND NOT EXISTS (SELECT 1 FROM event_alarms WHERE id = NEW.owner_id) BEGIN
    SELECT RAISE(ABORT, 'x_properties owner does not exist');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER x_properties_todo_alarm_owner_exists
BEFORE INSERT ON x_properties
WHEN NEW.owner_type = 'todo_alarm'
 AND NOT EXISTS (SELECT 1 FROM todo_alarms WHERE id = NEW.owner_id) BEGIN
    SELECT RAISE(ABORT, 'x_properties owner does not exist');
END;
-- +goose StatementEnd

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

-- +goose StatementBegin
CREATE TRIGGER event_alarms_x_properties_ad
AFTER DELETE ON event_alarms BEGIN
    DELETE FROM x_properties WHERE owner_type = 'event_alarm' AND owner_id = OLD.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER todo_alarms_x_properties_ad
AFTER DELETE ON todo_alarms BEGIN
    DELETE FROM x_properties WHERE owner_type = 'todo_alarm' AND owner_id = OLD.id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS todo_alarms_x_properties_ad;
DROP TRIGGER IF EXISTS event_alarms_x_properties_ad;
DROP TRIGGER IF EXISTS x_properties_todo_alarm_owner_exists;
DROP TRIGGER IF EXISTS x_properties_event_alarm_owner_exists;
DROP TRIGGER IF EXISTS events_x_properties_ad;
DROP TRIGGER IF EXISTS todos_x_properties_ad;
DROP TRIGGER IF EXISTS journals_x_properties_ad;

-- Intentional, unrecoverable data loss: the pre-031 CHECK constraint cannot
-- hold alarm-owned rows, so rolling back discards all VALARM X-properties.
DELETE FROM x_properties WHERE owner_type IN ('event_alarm', 'todo_alarm');

CREATE TABLE x_properties_old (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type TEXT NOT NULL,
    owner_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    value TEXT NOT NULL DEFAULT '',
    params TEXT NOT NULL DEFAULT '{}',
    CHECK (owner_type IN ('event', 'todo', 'journal')),
    CHECK (json_valid(params))
);

INSERT INTO x_properties_old (id, owner_type, owner_id, name, value, params)
SELECT id, owner_type, owner_id, name, value, params FROM x_properties;

DROP TABLE x_properties;
ALTER TABLE x_properties_old RENAME TO x_properties;

CREATE INDEX idx_xprops_owner ON x_properties(owner_type, owner_id);

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
