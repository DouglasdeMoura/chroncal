-- +goose Up
-- A prompt-mode (412) conflict that the user never resolves was re-inserted on
-- every sync tick, so sync_conflicts could accumulate many duplicate rows for
-- the same (calendar_id, uid). Collapse any existing duplicates to the newest
-- row, then enforce a single open conflict per resource so future inserts can
-- upsert instead of pile up. See issue #104.
DELETE FROM sync_conflicts
WHERE id NOT IN (
    SELECT MAX(id) FROM sync_conflicts GROUP BY calendar_id, uid
);

CREATE UNIQUE INDEX idx_sync_conflicts_calendar_uid
    ON sync_conflicts(calendar_id, uid);

-- +goose Down
DROP INDEX IF EXISTS idx_sync_conflicts_calendar_uid;
