-- +goose Up
-- Cache table for expanded recurring event instances
CREATE TABLE recurrence_instances (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id    INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    original_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    instance_at TEXT    NOT NULL, -- RFC 3339 datetime of this occurrence
    is_override INTEGER NOT NULL DEFAULT 0, -- 1 if from RDATE or recurrence override
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE UNIQUE INDEX idx_recurrence_unique ON recurrence_instances(event_id, instance_at);
CREATE INDEX idx_recurrence_event ON recurrence_instances(event_id);
CREATE INDEX idx_recurrence_instance_at ON recurrence_instances(instance_at);
CREATE INDEX idx_recurrence_original ON recurrence_instances(original_id);

-- Same table for todos
CREATE TABLE todo_recurrence_instances (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id     INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    original_id INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    instance_at TEXT    NOT NULL,
    is_override INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE UNIQUE INDEX idx_todo_recurrence_unique ON todo_recurrence_instances(todo_id, instance_at);
CREATE INDEX idx_todo_recurrence_todo ON todo_recurrence_instances(todo_id);
CREATE INDEX idx_todo_recurrence_instance_at ON todo_recurrence_instances(instance_at);

-- +goose Down
DROP INDEX IF EXISTS idx_todo_recurrence_instance_at;
DROP INDEX IF EXISTS idx_todo_recurrence_todo;
DROP INDEX IF EXISTS idx_todo_recurrence_unique;
DROP TABLE IF EXISTS todo_recurrence_instances;

DROP INDEX IF EXISTS idx_recurrence_original;
DROP INDEX IF EXISTS idx_recurrence_instance_at;
DROP INDEX IF EXISTS idx_recurrence_event;
DROP INDEX IF EXISTS idx_recurrence_unique;
DROP TABLE IF EXISTS recurrence_instances;
