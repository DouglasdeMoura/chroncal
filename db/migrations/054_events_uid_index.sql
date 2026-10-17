-- +goose Up
-- Issue #843. Migration 050 replaced UNIQUE(uid, recurrence_id) with
-- UNIQUE(calendar_id, uid, recurrence_id), so the remaining unique index
-- starts with calendar_id and cannot serve a lookup by uid alone.
-- GetEventByUID, ListEventMastersByUIDIncludingDeleted, and
-- ListEventsByUIDAndRecurrenceID then scan the whole events table on every
-- edit, sync pull, and alarm check. Index the leading uid columns those
-- queries filter on.
CREATE INDEX idx_events_uid ON events(uid, recurrence_id);

-- +goose Down
DROP INDEX idx_events_uid;
