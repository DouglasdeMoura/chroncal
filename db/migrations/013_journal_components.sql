-- +goose Up

-- RFC 5545 sub-components for journals: ATTENDEE, ATTACH, etc.
-- VJOURNAL does not support VALARM, so no alarm tables here.

-- ATTENDEE and ORGANIZER properties.
CREATE TABLE journal_attendees (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    journal_id     INTEGER NOT NULL REFERENCES journals(id) ON DELETE CASCADE,
    email          TEXT    NOT NULL,
    name           TEXT,
    rsvp_status    TEXT    NOT NULL DEFAULT 'NEEDS-ACTION'
        CHECK(rsvp_status IN ('NEEDS-ACTION','ACCEPTED','DECLINED','TENTATIVE','DELEGATED','COMPLETED','IN-PROCESS')),
    role           TEXT    NOT NULL DEFAULT 'REQ-PARTICIPANT'
        CHECK(role IN ('CHAIR','REQ-PARTICIPANT','OPT-PARTICIPANT','NON-PARTICIPANT')),
    organizer      INTEGER NOT NULL DEFAULT 0
        CHECK(organizer IN (0, 1)),
    cutype         TEXT
        CHECK(cutype IS NULL OR cutype IN ('INDIVIDUAL','GROUP','RESOURCE','ROOM','UNKNOWN')),
    rsvp           TEXT
        CHECK(rsvp IS NULL OR rsvp IN ('TRUE','FALSE')),
    sent_by        TEXT,
    delegated_to   TEXT,
    delegated_from TEXT,
    member         TEXT,
    dir            TEXT,
    language       TEXT
);

CREATE INDEX idx_journal_attendees_journal_id ON journal_attendees(journal_id);

-- ATTACH property.
CREATE TABLE journal_attachments (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    journal_id INTEGER NOT NULL REFERENCES journals(id) ON DELETE CASCADE,
    uri        TEXT,
    fmttype    TEXT,
    data       BLOB,
    filename   TEXT,
    CHECK (uri IS NOT NULL OR data IS NOT NULL)
);

CREATE INDEX idx_journal_attachments_journal_id ON journal_attachments(journal_id);

-- COMMENT property.
CREATE TABLE journal_comments (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    journal_id INTEGER NOT NULL REFERENCES journals(id) ON DELETE CASCADE,
    text       TEXT    NOT NULL
);

CREATE INDEX idx_journal_comments_journal_id ON journal_comments(journal_id);

-- CONTACT property.
CREATE TABLE journal_contacts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    journal_id INTEGER NOT NULL REFERENCES journals(id) ON DELETE CASCADE,
    text       TEXT    NOT NULL
);

CREATE INDEX idx_journal_contacts_journal_id ON journal_contacts(journal_id);

-- RELATED-TO property.
CREATE TABLE journal_relations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    journal_id INTEGER NOT NULL REFERENCES journals(id) ON DELETE CASCADE,
    rel_type   TEXT    NOT NULL DEFAULT 'PARENT'
        CHECK(rel_type IN ('PARENT','CHILD','SIBLING')),
    rel_uid    TEXT    NOT NULL
);

CREATE INDEX idx_journal_relations_journal_id ON journal_relations(journal_id);

-- +goose Down
DROP TABLE IF EXISTS journal_relations;
DROP TABLE IF EXISTS journal_contacts;
DROP TABLE IF EXISTS journal_comments;
DROP TABLE IF EXISTS journal_attachments;
DROP TABLE IF EXISTS journal_attendees;
