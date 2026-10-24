-- +goose Up
-- RFC 9074 needs an alarm UID that is unique in its component only.
-- Servers and clients put one VALARM UID on two resources, for example on
-- a copy of an event in another calendar. The indexes of migration 044
-- refused such a UID, so the store kept a minted UID for the row and the
-- alarm state of the row could go (issue #922). The new indexes refuse a
-- second use of a UID in one event or in one todo only.
DROP INDEX IF EXISTS idx_event_alarms_uid;
CREATE UNIQUE INDEX idx_event_alarms_event_uid ON event_alarms(event_id, uid) WHERE uid IS NOT NULL;
DROP INDEX IF EXISTS idx_todo_alarms_uid;
CREATE UNIQUE INDEX idx_todo_alarms_todo_uid ON todo_alarms(todo_id, uid) WHERE uid IS NOT NULL;

-- +goose Down
-- The old indexes refuse a UID on two items. The row with the lowest ID
-- keeps the UID, and the other rows lose it. The startup heal gives a new
-- UID to a fireable alarm with no UID. A preserved foreign alarm keeps no
-- UID, because the store does not mint a UID for it (issue #586).
DROP INDEX IF EXISTS idx_event_alarms_event_uid;
UPDATE event_alarms SET uid = NULL
WHERE uid IS NOT NULL
  AND id NOT IN (SELECT MIN(id) FROM event_alarms WHERE uid IS NOT NULL GROUP BY uid);
CREATE UNIQUE INDEX idx_event_alarms_uid ON event_alarms(uid) WHERE uid IS NOT NULL;
DROP INDEX IF EXISTS idx_todo_alarms_todo_uid;
UPDATE todo_alarms SET uid = NULL
WHERE uid IS NOT NULL
  AND id NOT IN (SELECT MIN(id) FROM todo_alarms WHERE uid IS NOT NULL GROUP BY uid);
CREATE UNIQUE INDEX idx_todo_alarms_uid ON todo_alarms(uid) WHERE uid IS NOT NULL;
