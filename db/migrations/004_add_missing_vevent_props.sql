-- +goose Up

-- URI attachments for events (RFC 5545 ATTACH)
CREATE TABLE event_attachments (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    uri      TEXT    NOT NULL,
    fmttype  TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_event_attachments_event_id ON event_attachments(event_id);

-- URI attachments for todos
CREATE TABLE todo_attachments (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    uri     TEXT    NOT NULL,
    fmttype TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_todo_attachments_todo_id ON todo_attachments(todo_id);

-- Comments for events (RFC 5545 COMMENT)
CREATE TABLE event_comments (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    text     TEXT    NOT NULL
);

CREATE INDEX idx_event_comments_event_id ON event_comments(event_id);

-- Comments for todos
CREATE TABLE todo_comments (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    text    TEXT    NOT NULL
);

CREATE INDEX idx_todo_comments_todo_id ON todo_comments(todo_id);

-- Relations for events (RFC 5545 RELATED-TO)
CREATE TABLE event_relations (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    rel_type TEXT    NOT NULL DEFAULT 'PARENT',
    rel_uid  TEXT    NOT NULL
);

CREATE INDEX idx_event_relations_event_id ON event_relations(event_id);

-- Relations for todos
CREATE TABLE todo_relations (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id  INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    rel_type TEXT    NOT NULL DEFAULT 'PARENT',
    rel_uid  TEXT    NOT NULL
);

CREATE INDEX idx_todo_relations_todo_id ON todo_relations(todo_id);

-- +goose Down
DROP TABLE todo_relations;
DROP TABLE event_relations;
DROP TABLE todo_comments;
DROP TABLE event_comments;
DROP TABLE todo_attachments;
DROP TABLE event_attachments;
