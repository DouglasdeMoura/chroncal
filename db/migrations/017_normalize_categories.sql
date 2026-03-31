-- +goose Up

-- Junction table for event categories (normalized from CSV column).
CREATE TABLE event_categories (
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    category TEXT    NOT NULL,
    PRIMARY KEY (event_id, category)
);
CREATE INDEX idx_event_categories_category ON event_categories(category);

CREATE TABLE todo_categories (
    todo_id  INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    category TEXT    NOT NULL,
    PRIMARY KEY (todo_id, category)
);
CREATE INDEX idx_todo_categories_category ON todo_categories(category);

-- Populate from existing comma-separated data.
INSERT INTO event_categories (event_id, category)
WITH RECURSIVE split(event_id, cat, rest) AS (
    SELECT id, '', categories || ',' FROM events WHERE categories != ''
    UNION ALL
    SELECT event_id,
           TRIM(SUBSTR(rest, 1, INSTR(rest, ',') - 1)),
           SUBSTR(rest, INSTR(rest, ',') + 1)
    FROM split WHERE rest != ''
)
SELECT event_id, cat FROM split WHERE cat != ''
ON CONFLICT DO NOTHING;

INSERT INTO todo_categories (todo_id, category)
WITH RECURSIVE split(todo_id, cat, rest) AS (
    SELECT id, '', categories || ',' FROM todos WHERE categories != ''
    UNION ALL
    SELECT todo_id,
           TRIM(SUBSTR(rest, 1, INSTR(rest, ',') - 1)),
           SUBSTR(rest, INSTR(rest, ',') + 1)
    FROM split WHERE rest != ''
)
SELECT todo_id, cat FROM split WHERE cat != ''
ON CONFLICT DO NOTHING;

-- Drop the denormalized column (SQLite 3.35+, already used in migration 016).
ALTER TABLE events DROP COLUMN categories;
ALTER TABLE todos DROP COLUMN categories;

-- Views that reconstruct categories from the junction table.
CREATE VIEW events_v AS
SELECT e.*,
    COALESCE((SELECT GROUP_CONCAT(ec.category, ',')
              FROM event_categories ec
              WHERE ec.event_id = e.id
              ORDER BY ec.category), '') AS categories
FROM events e;

CREATE VIEW todos_v AS
SELECT t.*,
    COALESCE((SELECT GROUP_CONCAT(tc.category, ',')
              FROM todo_categories tc
              WHERE tc.todo_id = t.id
              ORDER BY tc.category), '') AS categories
FROM todos t;

-- +goose Down
DROP VIEW IF EXISTS todos_v;
DROP VIEW IF EXISTS events_v;

ALTER TABLE events ADD COLUMN categories TEXT NOT NULL DEFAULT '';
ALTER TABLE todos ADD COLUMN categories TEXT NOT NULL DEFAULT '';

UPDATE events SET categories = (
    SELECT COALESCE(GROUP_CONCAT(ec.category, ','), '')
    FROM event_categories ec WHERE ec.event_id = events.id
);
UPDATE todos SET categories = (
    SELECT COALESCE(GROUP_CONCAT(tc.category, ','), '')
    FROM todo_categories tc WHERE tc.todo_id = todos.id
);

DROP TABLE IF EXISTS todo_categories;
DROP TABLE IF EXISTS event_categories;
