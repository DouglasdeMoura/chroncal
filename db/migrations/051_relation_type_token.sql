-- +goose Up
-- RFC 5545 makes RELTYPE an extensible token. PARENT, CHILD, and SIBLING are
-- the three values the standard names, but a server can send any other token.
-- iCloud does. The old CHECK(rel_type IN ('PARENT','CHILD','SIBLING')) then
-- failed the insert, the pull withheld the sync-token, and the calendar
-- re-pulled everything on every sync (issue #768).
--
-- Widen the constraint to any non-empty token. SQLite cannot alter a CHECK,
-- so rebuild each of the three relation tables. Each table is a leaf: no
-- other table references it, and no trigger reads it. The rebuild copies the
-- rows with their ids, then recreates the index.

CREATE TABLE event_relations_new (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    rel_type TEXT    NOT NULL DEFAULT 'PARENT'
        CHECK(rel_type <> ''),
    rel_uid  TEXT    NOT NULL
);

INSERT INTO event_relations_new (id, event_id, rel_type, rel_uid)
SELECT id, event_id, rel_type, rel_uid FROM event_relations;

DROP TABLE event_relations;
ALTER TABLE event_relations_new RENAME TO event_relations;

CREATE INDEX idx_event_relations_event_id ON event_relations(event_id);

CREATE TABLE todo_relations_new (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id  INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    rel_type TEXT    NOT NULL DEFAULT 'PARENT'
        CHECK(rel_type <> ''),
    rel_uid  TEXT    NOT NULL
);

INSERT INTO todo_relations_new (id, todo_id, rel_type, rel_uid)
SELECT id, todo_id, rel_type, rel_uid FROM todo_relations;

DROP TABLE todo_relations;
ALTER TABLE todo_relations_new RENAME TO todo_relations;

CREATE INDEX idx_todo_relations_todo_id ON todo_relations(todo_id);

CREATE TABLE journal_relations_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    journal_id INTEGER NOT NULL REFERENCES journals(id) ON DELETE CASCADE,
    rel_type   TEXT    NOT NULL DEFAULT 'PARENT'
        CHECK(rel_type <> ''),
    rel_uid    TEXT    NOT NULL
);

INSERT INTO journal_relations_new (id, journal_id, rel_type, rel_uid)
SELECT id, journal_id, rel_type, rel_uid FROM journal_relations;

DROP TABLE journal_relations;
ALTER TABLE journal_relations_new RENAME TO journal_relations;

CREATE INDEX idx_journal_relations_journal_id ON journal_relations(journal_id);

-- +goose Down
-- The narrow CHECK cannot hold a token outside the three named values. The
-- Down step therefore drops each row that carries another token. This matches
-- migration 044, which also drops the rows that the old CHECK refuses.

CREATE TABLE event_relations_old (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    rel_type TEXT    NOT NULL DEFAULT 'PARENT'
        CHECK(rel_type IN ('PARENT','CHILD','SIBLING')),
    rel_uid  TEXT    NOT NULL
);

INSERT INTO event_relations_old (id, event_id, rel_type, rel_uid)
SELECT id, event_id, rel_type, rel_uid FROM event_relations
WHERE rel_type IN ('PARENT','CHILD','SIBLING');

DROP TABLE event_relations;
ALTER TABLE event_relations_old RENAME TO event_relations;

CREATE INDEX idx_event_relations_event_id ON event_relations(event_id);

CREATE TABLE todo_relations_old (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id  INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    rel_type TEXT    NOT NULL DEFAULT 'PARENT'
        CHECK(rel_type IN ('PARENT','CHILD','SIBLING')),
    rel_uid  TEXT    NOT NULL
);

INSERT INTO todo_relations_old (id, todo_id, rel_type, rel_uid)
SELECT id, todo_id, rel_type, rel_uid FROM todo_relations
WHERE rel_type IN ('PARENT','CHILD','SIBLING');

DROP TABLE todo_relations;
ALTER TABLE todo_relations_old RENAME TO todo_relations;

CREATE INDEX idx_todo_relations_todo_id ON todo_relations(todo_id);

CREATE TABLE journal_relations_old (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    journal_id INTEGER NOT NULL REFERENCES journals(id) ON DELETE CASCADE,
    rel_type   TEXT    NOT NULL DEFAULT 'PARENT'
        CHECK(rel_type IN ('PARENT','CHILD','SIBLING')),
    rel_uid    TEXT    NOT NULL
);

INSERT INTO journal_relations_old (id, journal_id, rel_type, rel_uid)
SELECT id, journal_id, rel_type, rel_uid FROM journal_relations
WHERE rel_type IN ('PARENT','CHILD','SIBLING');

DROP TABLE journal_relations;
ALTER TABLE journal_relations_old RENAME TO journal_relations;

CREATE INDEX idx_journal_relations_journal_id ON journal_relations(journal_id);
