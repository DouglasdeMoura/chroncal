-- +goose Up
CREATE TABLE accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,                -- display name, e.g. "Google - Work"
    server_url TEXT NOT NULL,          -- CalDAV principal URL
    auth_type TEXT NOT NULL DEFAULT 'basic',  -- 'basic', 'oauth2', 'bearer'
    username TEXT NOT NULL DEFAULT '',
    -- credentials stored separately (keyring / encrypted file / plaintext)
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Link calendars to remote accounts
ALTER TABLE calendars ADD COLUMN account_id INTEGER REFERENCES accounts(id) ON DELETE SET NULL;
ALTER TABLE calendars ADD COLUMN remote_url TEXT DEFAULT '';    -- CalDAV calendar URL (href)
ALTER TABLE calendars ADD COLUMN ctag TEXT DEFAULT '';          -- CalDAV getctag for change detection
ALTER TABLE calendars ADD COLUMN sync_token TEXT DEFAULT '';    -- CalDAV sync-token (preferred over ctag)

-- +goose Down
ALTER TABLE calendars DROP COLUMN account_id;
ALTER TABLE calendars DROP COLUMN remote_url;
ALTER TABLE calendars DROP COLUMN ctag;
ALTER TABLE calendars DROP COLUMN sync_token;
DROP TABLE IF EXISTS accounts;
