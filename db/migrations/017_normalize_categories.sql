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

-- +goose Down
DROP TABLE IF EXISTS todo_categories;
DROP TABLE IF EXISTS event_categories;
