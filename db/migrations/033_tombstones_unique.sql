-- +goose Up

-- Tombstones had no uniqueness, so repeated deletes of the same resource before
-- the sync engine consumed the tombstone accumulated duplicate rows and caused
-- redundant remote DELETEs. Collapse any existing duplicates (keeping the most
-- recent row per resource) and enforce one tombstone per (calendar_id, uid).
DELETE FROM tombstones
WHERE id NOT IN (
    SELECT MAX(id) FROM tombstones GROUP BY calendar_id, uid
);

CREATE UNIQUE INDEX idx_tombstones_calendar_uid ON tombstones(calendar_id, uid);

-- +goose Down
DROP INDEX IF EXISTS idx_tombstones_calendar_uid;
