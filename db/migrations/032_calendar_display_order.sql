-- +goose Up
ALTER TABLE calendars ADD COLUMN display_order INTEGER NOT NULL DEFAULT 0;

-- Backfill: rank existing calendars by name so the persisted starting order
-- matches the alphabetical order the sidebar showed before this column
-- existed. Names are UNIQUE, so the correlated count yields a stable, gap-free
-- 0-based rank with no ties.
UPDATE calendars SET display_order = (
    SELECT COUNT(*) FROM calendars c2 WHERE c2.name < calendars.name
);

-- +goose Down
ALTER TABLE calendars DROP COLUMN display_order;
