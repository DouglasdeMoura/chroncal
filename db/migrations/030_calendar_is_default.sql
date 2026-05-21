-- +goose Up
ALTER TABLE calendars ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0;

-- At most one default calendar at a time. Enforced by a partial unique
-- index because non-default rows all carry the value 0; without the
-- WHERE clause every additional calendar would collide.
CREATE UNIQUE INDEX idx_calendars_is_default ON calendars(is_default)
    WHERE is_default = 1;

-- Backfill: promote the oldest calendar (lowest id) to default so
-- existing databases keep a single, stable default after upgrade.
UPDATE calendars SET is_default = 1
    WHERE id = (SELECT MIN(id) FROM calendars);

-- +goose Down
DROP INDEX IF EXISTS idx_calendars_is_default;
ALTER TABLE calendars DROP COLUMN is_default;
