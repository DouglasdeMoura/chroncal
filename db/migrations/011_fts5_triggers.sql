-- +goose Up

-- Replace standalone FTS tables with trigger-maintained equivalents.
-- Fixes ghost results after CASCADE deletes (e.g. calendar deletion)
-- and eliminates the full-rebuild-on-startup cost.

DROP TABLE IF EXISTS events_fts;
DROP TABLE IF EXISTS todos_fts;

CREATE VIRTUAL TABLE events_fts USING fts5(
    title, description, location, categories,
    tokenize='unicode61 remove_diacritics 2'
);

CREATE VIRTUAL TABLE todos_fts USING fts5(
    summary, description, location, categories,
    tokenize='unicode61 remove_diacritics 2'
);

-- ── Event triggers ──────────────────────────────────────────────────────

-- Categories are inserted separately, so the initial FTS row has empty categories.
-- +goose StatementBegin
CREATE TRIGGER events_fts_ai AFTER INSERT ON events BEGIN
    INSERT INTO events_fts(rowid, title, description, location, categories)
    VALUES (NEW.id, NEW.title, COALESCE(NEW.description, ''), COALESCE(NEW.location, ''), '');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER events_fts_au AFTER UPDATE ON events BEGIN
    DELETE FROM events_fts WHERE rowid = OLD.id;
    INSERT INTO events_fts(rowid, title, description, location, categories)
    VALUES (NEW.id, NEW.title, COALESCE(NEW.description, ''), COALESCE(NEW.location, ''),
        COALESCE((SELECT GROUP_CONCAT(ec.category, ' ')
                  FROM event_categories ec WHERE ec.event_id = NEW.id), ''));
END;
-- +goose StatementEnd

-- BEFORE DELETE so it fires before CASCADE removes categories.
-- +goose StatementBegin
CREATE TRIGGER events_fts_bd BEFORE DELETE ON events BEGIN
    DELETE FROM events_fts WHERE rowid = OLD.id;
END;
-- +goose StatementEnd

-- ── Event category triggers ─────────────────────────────────────────────

-- +goose StatementBegin
CREATE TRIGGER event_categories_fts_ai AFTER INSERT ON event_categories BEGIN
    DELETE FROM events_fts WHERE rowid = NEW.event_id;
    INSERT INTO events_fts(rowid, title, description, location, categories)
    SELECT e.id, e.title, COALESCE(e.description, ''), COALESCE(e.location, ''),
        COALESCE((SELECT GROUP_CONCAT(ec.category, ' ')
                  FROM event_categories ec WHERE ec.event_id = e.id), '')
    FROM events e WHERE e.id = NEW.event_id;
END;
-- +goose StatementEnd

-- Guard: skip when the event itself was CASCADE-deleted (row already gone).
-- +goose StatementBegin
CREATE TRIGGER event_categories_fts_ad AFTER DELETE ON event_categories
WHEN EXISTS (SELECT 1 FROM events WHERE id = OLD.event_id) BEGIN
    DELETE FROM events_fts WHERE rowid = OLD.event_id;
    INSERT INTO events_fts(rowid, title, description, location, categories)
    SELECT e.id, e.title, COALESCE(e.description, ''), COALESCE(e.location, ''),
        COALESCE((SELECT GROUP_CONCAT(ec.category, ' ')
                  FROM event_categories ec WHERE ec.event_id = e.id), '')
    FROM events e WHERE e.id = OLD.event_id;
END;
-- +goose StatementEnd

-- ── Todo triggers ───────────────────────────────────────────────────────

-- +goose StatementBegin
CREATE TRIGGER todos_fts_ai AFTER INSERT ON todos BEGIN
    INSERT INTO todos_fts(rowid, summary, description, location, categories)
    VALUES (NEW.id, NEW.summary, COALESCE(NEW.description, ''), COALESCE(NEW.location, ''), '');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER todos_fts_au AFTER UPDATE ON todos BEGIN
    DELETE FROM todos_fts WHERE rowid = OLD.id;
    INSERT INTO todos_fts(rowid, summary, description, location, categories)
    VALUES (NEW.id, NEW.summary, COALESCE(NEW.description, ''), COALESCE(NEW.location, ''),
        COALESCE((SELECT GROUP_CONCAT(tc.category, ' ')
                  FROM todo_categories tc WHERE tc.todo_id = NEW.id), ''));
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER todos_fts_bd BEFORE DELETE ON todos BEGIN
    DELETE FROM todos_fts WHERE rowid = OLD.id;
END;
-- +goose StatementEnd

-- ── Todo category triggers ──────────────────────────────────────────────

-- +goose StatementBegin
CREATE TRIGGER todo_categories_fts_ai AFTER INSERT ON todo_categories BEGIN
    DELETE FROM todos_fts WHERE rowid = NEW.todo_id;
    INSERT INTO todos_fts(rowid, summary, description, location, categories)
    SELECT t.id, t.summary, COALESCE(t.description, ''), COALESCE(t.location, ''),
        COALESCE((SELECT GROUP_CONCAT(tc.category, ' ')
                  FROM todo_categories tc WHERE tc.todo_id = t.id), '')
    FROM todos t WHERE t.id = NEW.todo_id;
END;
-- +goose StatementEnd

-- Guard: skip when the todo itself was CASCADE-deleted (row already gone).
-- +goose StatementBegin
CREATE TRIGGER todo_categories_fts_ad AFTER DELETE ON todo_categories
WHEN EXISTS (SELECT 1 FROM todos WHERE id = OLD.todo_id) BEGIN
    DELETE FROM todos_fts WHERE rowid = OLD.todo_id;
    INSERT INTO todos_fts(rowid, summary, description, location, categories)
    SELECT t.id, t.summary, COALESCE(t.description, ''), COALESCE(t.location, ''),
        COALESCE((SELECT GROUP_CONCAT(tc.category, ' ')
                  FROM todo_categories tc WHERE tc.todo_id = t.id), '')
    FROM todos t WHERE t.id = OLD.todo_id;
END;
-- +goose StatementEnd

-- ── Backfill ────────────────────────────────────────────────────────────

INSERT INTO events_fts (rowid, title, description, location, categories)
SELECT e.id, e.title, COALESCE(e.description, ''), COALESCE(e.location, ''),
       COALESCE((SELECT GROUP_CONCAT(ec.category, ' ')
                 FROM event_categories ec WHERE ec.event_id = e.id), '')
FROM events e;

INSERT INTO todos_fts (rowid, summary, description, location, categories)
SELECT t.id, t.summary, COALESCE(t.description, ''), COALESCE(t.location, ''),
       COALESCE((SELECT GROUP_CONCAT(tc.category, ' ')
                 FROM todo_categories tc WHERE tc.todo_id = t.id), '')
FROM todos t;

-- +goose Down

DROP TRIGGER IF EXISTS todo_categories_fts_ad;
DROP TRIGGER IF EXISTS todo_categories_fts_ai;
DROP TRIGGER IF EXISTS todos_fts_bd;
DROP TRIGGER IF EXISTS todos_fts_au;
DROP TRIGGER IF EXISTS todos_fts_ai;
DROP TRIGGER IF EXISTS event_categories_fts_ad;
DROP TRIGGER IF EXISTS event_categories_fts_ai;
DROP TRIGGER IF EXISTS events_fts_bd;
DROP TRIGGER IF EXISTS events_fts_au;
DROP TRIGGER IF EXISTS events_fts_ai;

DROP TABLE IF EXISTS events_fts;
DROP TABLE IF EXISTS todos_fts;

-- Restore standalone FTS tables (pre-trigger schema).
CREATE VIRTUAL TABLE events_fts USING fts5(
    title, description, location, categories,
    tokenize='unicode61 remove_diacritics 2'
);
CREATE VIRTUAL TABLE todos_fts USING fts5(
    summary, description, location, categories,
    tokenize='unicode61 remove_diacritics 2'
);

INSERT INTO events_fts (rowid, title, description, location, categories)
SELECT e.id, e.title, COALESCE(e.description, ''), COALESCE(e.location, ''),
       COALESCE((SELECT GROUP_CONCAT(ec.category, ' ')
                 FROM event_categories ec WHERE ec.event_id = e.id), '')
FROM events e;

INSERT INTO todos_fts (rowid, summary, description, location, categories)
SELECT t.id, t.summary, COALESCE(t.description, ''), COALESCE(t.location, ''),
       COALESCE((SELECT GROUP_CONCAT(tc.category, ' ')
                 FROM todo_categories tc WHERE tc.todo_id = t.id), '')
FROM todos t;
