-- +goose Up
ALTER TABLE event_alarms ADD COLUMN uid TEXT NOT NULL DEFAULT '';
ALTER TABLE todo_alarms ADD COLUMN uid TEXT NOT NULL DEFAULT '';

-- Partial unique index: only enforced for non-empty UIDs (backfill sets empty first).
CREATE UNIQUE INDEX idx_event_alarms_uid ON event_alarms(event_id, uid) WHERE uid != '';
CREATE UNIQUE INDEX idx_todo_alarms_uid ON todo_alarms(todo_id, uid) WHERE uid != '';

-- +goose Down
DROP INDEX IF EXISTS idx_todo_alarms_uid;
DROP INDEX IF EXISTS idx_event_alarms_uid;
ALTER TABLE todo_alarms DROP COLUMN uid;
ALTER TABLE event_alarms DROP COLUMN uid;
