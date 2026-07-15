-- +goose Up
-- CalDAV account discovery mirrors remote collection metadata onto local
-- calendars. ALTER TABLE ADD COLUMN adds the four new columns in place, which
-- keeps the existing UNIQUE calendars.name constraint and every foreign-key
-- reference (events, todos, journals, ...) intact. Because this is plain DDL,
-- it runs inside goose's per-migration transaction and rolls back atomically
-- on failure instead of the previous NO TRANSACTION table rebuild.

ALTER TABLE calendars ADD COLUMN remote_name TEXT NOT NULL DEFAULT '';
ALTER TABLE calendars ADD COLUMN remote_access TEXT NOT NULL DEFAULT 'unknown'
    CHECK (remote_access IN ('unknown', 'read', 'write', 'owner'));
ALTER TABLE calendars ADD COLUMN remote_components TEXT NOT NULL DEFAULT '';
ALTER TABLE calendars ADD COLUMN remote_missing INTEGER NOT NULL DEFAULT 0
    CHECK (remote_missing IN (0, 1));

-- A remote collection is uniquely identified by its URL within an account.
-- Scope the uniqueness with a partial index so unlinked (local) calendars —
-- which all carry a NULL account_id and empty remote_url — never collide.
CREATE UNIQUE INDEX IF NOT EXISTS idx_calendars_account_remote_url
    ON calendars(account_id, remote_url)
    WHERE account_id IS NOT NULL AND remote_url <> '';

-- +goose Down
-- Drop only what this migration introduced: the partial uniqueness index and
-- the four mirror columns. The original calendars schema (including the
-- UNIQUE name constraint) is untouched.
DROP INDEX IF EXISTS idx_calendars_account_remote_url;
ALTER TABLE calendars DROP COLUMN remote_missing;
ALTER TABLE calendars DROP COLUMN remote_components;
ALTER TABLE calendars DROP COLUMN remote_access;
ALTER TABLE calendars DROP COLUMN remote_name;
