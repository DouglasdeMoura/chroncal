-- +goose NO TRANSACTION

-- +goose Up
-- SQLite cannot drop the UNIQUE constraint embedded in calendars.name, so
-- rebuild the table while foreign-key rewriting is disabled. legacy_alter_table
-- keeps child tables pointing at "calendars" instead of the temporary name.
PRAGMA foreign_keys = OFF;
PRAGMA legacy_alter_table = ON;

ALTER TABLE calendars RENAME TO calendars_old;

CREATE TABLE calendars (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    name                   TEXT NOT NULL,
    color                  TEXT NOT NULL DEFAULT '#7C3AED',
    description            TEXT,
    created_at             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    account_id             INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    remote_url             TEXT DEFAULT '',
    ctag                   TEXT DEFAULT '',
    sync_token             TEXT DEFAULT '',
    last_sync_at           TEXT DEFAULT '',
    last_sync_attempted_at TEXT DEFAULT '',
    last_sync_error        TEXT DEFAULT '',
    remote_color           TEXT DEFAULT '',
    color_dirty            INTEGER NOT NULL DEFAULT 0,
    owner_email            TEXT NOT NULL DEFAULT '',
    is_default             INTEGER NOT NULL DEFAULT 0,
    display_order          INTEGER NOT NULL DEFAULT 0,
    remote_name            TEXT NOT NULL DEFAULT '',
    remote_access          TEXT NOT NULL DEFAULT 'unknown'
        CHECK (remote_access IN ('unknown', 'read', 'write', 'owner')),
    remote_components      TEXT NOT NULL DEFAULT '',
    remote_missing         INTEGER NOT NULL DEFAULT 0
        CHECK (remote_missing IN (0, 1))
);

INSERT INTO calendars (
    id, name, color, description, created_at, updated_at,
    account_id, remote_url, ctag, sync_token,
    last_sync_at, last_sync_attempted_at, last_sync_error,
    remote_color, color_dirty, owner_email, is_default, display_order
)
SELECT
    id, name, color, description, created_at, updated_at,
    account_id, remote_url, ctag, sync_token,
    last_sync_at, last_sync_attempted_at, last_sync_error,
    remote_color, color_dirty, owner_email, is_default, display_order
FROM calendars_old;

DROP TABLE calendars_old;

CREATE UNIQUE INDEX idx_calendars_is_default ON calendars(is_default)
    WHERE is_default = 1;
CREATE UNIQUE INDEX idx_calendars_account_remote_url
    ON calendars(account_id, remote_url)
    WHERE account_id IS NOT NULL AND remote_url <> '';

PRAGMA legacy_alter_table = OFF;
PRAGMA foreign_keys = ON;

-- +goose Down
-- Fail before rebuilding when duplicate names cannot fit the old schema.
CREATE UNIQUE INDEX calendars_name_down_guard ON calendars(name);
DROP INDEX calendars_name_down_guard;

PRAGMA foreign_keys = OFF;
PRAGMA legacy_alter_table = ON;

ALTER TABLE calendars RENAME TO calendars_new;

CREATE TABLE calendars (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    name                   TEXT NOT NULL UNIQUE,
    color                  TEXT NOT NULL DEFAULT '#7C3AED',
    description            TEXT,
    created_at             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    account_id             INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    remote_url             TEXT DEFAULT '',
    ctag                   TEXT DEFAULT '',
    sync_token             TEXT DEFAULT '',
    last_sync_at           TEXT DEFAULT '',
    last_sync_attempted_at TEXT DEFAULT '',
    last_sync_error        TEXT DEFAULT '',
    remote_color           TEXT DEFAULT '',
    color_dirty            INTEGER NOT NULL DEFAULT 0,
    owner_email            TEXT NOT NULL DEFAULT '',
    is_default             INTEGER NOT NULL DEFAULT 0,
    display_order          INTEGER NOT NULL DEFAULT 0
);

INSERT INTO calendars (
    id, name, color, description, created_at, updated_at,
    account_id, remote_url, ctag, sync_token,
    last_sync_at, last_sync_attempted_at, last_sync_error,
    remote_color, color_dirty, owner_email, is_default, display_order
)
SELECT
    id, name, color, description, created_at, updated_at,
    account_id, remote_url, ctag, sync_token,
    last_sync_at, last_sync_attempted_at, last_sync_error,
    remote_color, color_dirty, owner_email, is_default, display_order
FROM calendars_new;

DROP TABLE calendars_new;

CREATE UNIQUE INDEX idx_calendars_is_default ON calendars(is_default)
    WHERE is_default = 1;

PRAGMA legacy_alter_table = OFF;
PRAGMA foreign_keys = ON;
