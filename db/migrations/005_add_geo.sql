-- +goose Up
ALTER TABLE events ADD COLUMN geo TEXT NOT NULL DEFAULT '';
ALTER TABLE todos ADD COLUMN geo TEXT NOT NULL DEFAULT '';

-- +goose Down
-- SQLite doesn't support DROP COLUMN before 3.35.0;
-- these columns will remain but be unused on downgrade.
