-- +goose Up
CREATE TABLE event_resources (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    text     TEXT    NOT NULL
);

CREATE INDEX idx_event_resources_event_id ON event_resources(event_id);

CREATE TABLE todo_resources (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    text    TEXT    NOT NULL
);

CREATE INDEX idx_todo_resources_todo_id ON todo_resources(todo_id);

-- +goose Down
DROP TABLE todo_resources;
DROP TABLE event_resources;
