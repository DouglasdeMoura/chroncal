-- +goose Up
CREATE TABLE event_contacts (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    text     TEXT    NOT NULL
);

CREATE INDEX idx_event_contacts_event_id ON event_contacts(event_id);

CREATE TABLE todo_contacts (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    text    TEXT    NOT NULL
);

CREATE INDEX idx_todo_contacts_todo_id ON todo_contacts(todo_id);

-- +goose Down
DROP TABLE todo_contacts;
DROP TABLE event_contacts;
