-- +goose Up
-- A new master settles the edits that no series owned, as a change or a
-- purge of a master does (migration 057). An insert of a master can give
-- a marked row (edit_unowned) one owner again: for example, a pull or an
-- import that writes a master into the calendar of the marked row. Without
-- this trigger the mark stays until the next edit of the row (issue #862).
-- The body is the body of events_master_change_settles_unowned_edits.
-- +goose StatementBegin
CREATE TRIGGER events_master_insert_settles_unowned_edits
AFTER INSERT ON events
WHEN NEW.recurrence_id = '' AND EXISTS (
    SELECT 1 FROM events AS u WHERE u.uid = NEW.uid AND u.edit_unowned = 1)
BEGIN
    INSERT INTO sync_resources (calendar_id, uid, owner_type, dirty, sync_strategy)
    SELECT DISTINCT s.owner_calendar_id, s.uid, 'event', 1, 'sync-token'
    FROM event_series_rows AS s
    JOIN events AS m ON m.calendar_id = s.owner_calendar_id AND m.uid = s.uid
        AND m.recurrence_id = '' AND m.deleted_at IS NULL
    JOIN calendars AS c ON c.id = s.owner_calendar_id AND c.account_id IS NOT NULL AND c.account_id != 0
    WHERE s.uid = NEW.uid AND s.id IN (
        SELECT u.id FROM events AS u WHERE u.uid = NEW.uid AND u.edit_unowned = 1 AND u.deleted_at IS NULL)
    ON CONFLICT(calendar_id, uid) DO UPDATE SET dirty = 1, rev = rev + 1;
    UPDATE events SET edit_unowned = 0
    WHERE uid = NEW.uid AND edit_unowned = 1
      AND id IN (SELECT s.id FROM event_series_rows AS s WHERE s.uid = NEW.uid);
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS events_master_insert_settles_unowned_edits;
