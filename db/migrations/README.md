# Migrations

Goose runs the SQL files of this directory in order. `make generate` reads
them as the schema of sqlc.

## Rebuild of the events table

SQLite cannot drop a table constraint, so a change of a constraint of
`events` rebuilds the table (see migration 050). The rebuild creates
`events_new`, copies the rows, drops `events`, and renames `events_new`.

The views `event_series_rows` (migration 057) and `live_move_copies`
(migration 060) read `events`. SQLite refuses the rename while a view reads
the dropped table. A rebuild of `events` must follow these steps:

1. Drop the two views: `DROP VIEW IF EXISTS event_series_rows;` and
   `DROP VIEW IF EXISTS live_move_copies;`
2. Rebuild the table as migration 050 does.
3. Create `event_series_rows` again with the statement below.
4. Create `live_move_copies` again. Copy its statement from migration 060.
5. Create the seven triggers of migration 057 again:
   `events_master_revive_drops_series_log`,
   `events_master_insert_drops_stale_logs`,
   `events_override_revive_drops_stale_logs`,
   `events_override_insert_drops_prune_marker`,
   `events_purge_drops_row_from_logs`,
   `events_master_change_settles_unowned_edits`, and
   `events_master_purge_settles_unowned_edits`. The drop of `events` drops
   them. Copy their statements from migration 057.

The triggers drop the stale delete logs of a calendar. The revive
trigger of a master drops the series-delete log of its delete when the
master comes back from the trash. That log has the delete token of the
master. The insert trigger of a master drops the series-delete log and
the occurrence logs of the UID when a new live master arrives. The two
triggers of an override drop the pruned marker of an occurrence when its
row comes back. The revive trigger also removes the row from the delete
logs. Without them, a log that a pull or an import left behind can bring
back the wrong rows on the next restore. It can also strip an imported
EXDATE. The purge trigger removes a purged override row from the delete
logs, so a log never names a row that is gone. An orphan series-delete
log that names only the purged row goes too. The revive trigger of a
master keeps the rows that a delete of orphan rows merged into its log.
The log then becomes an orphan log with only those rows. The two settle
triggers of a master run when a master changes or goes. An edit of a row
that no series owned (`edit_unowned`) then reaches the owner of the row
again. The calendar of a live owner master gets a dirty mark, and the mark
of the row clears.

Each statement below must stay equal to the one in migration 057. The
storage test `TestMigrationsReadmeMatches057` compares them, and the
storage test `TestEventsRebuildKeepsSeriesView` runs this sequence
against the statements of migration 057.

```sql
CREATE VIEW event_series_rows AS
SELECT
    e.id AS id,
    e.uid AS uid,
    e.calendar_id AS calendar_id,
    e.recurrence_id AS recurrence_id,
    e.deleted_at AS deleted_at,
    c.calendar_id AS owner_calendar_id,
    CAST(c.calendar_id = e.calendar_id AS INTEGER) AS beside_master
FROM events AS e
JOIN events AS c ON c.uid = e.uid AND c.recurrence_id = ''
WHERE c.calendar_id = e.calendar_id
   OR (NOT EXISTS (
        SELECT 1 FROM events AS m
        WHERE m.uid = e.uid AND m.recurrence_id = '' AND m.calendar_id = e.calendar_id)
       AND NOT EXISTS (
        SELECT 1 FROM events AS o
        WHERE o.uid = e.uid AND o.recurrence_id = '' AND o.calendar_id != c.calendar_id
          AND (o.deleted_at IS NULL OR c.deleted_at IS NOT NULL)))
UNION ALL
SELECT
    e.id AS id,
    e.uid AS uid,
    e.calendar_id AS calendar_id,
    e.recurrence_id AS recurrence_id,
    e.deleted_at AS deleted_at,
    e.calendar_id AS owner_calendar_id,
    0 AS beside_master
FROM events AS e
WHERE NOT EXISTS (
    SELECT 1 FROM events AS m WHERE m.uid = e.uid AND m.recurrence_id = '');
```

```sql
CREATE TRIGGER events_master_revive_drops_series_log
AFTER UPDATE OF deleted_at ON events
WHEN OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL AND NEW.recurrence_id = ''
BEGIN
    UPDATE event_series_deletes
    SET hidden_ids = (
            SELECT group_concat(e.id) FROM events AS e
            WHERE e.uid = NEW.uid AND e.deleted_at IS NOT NULL AND e.delete_token != OLD.delete_token
              AND ',' || event_series_deletes.hidden_ids || ',' LIKE '%,' || e.id || ',%'),
        had_master = 0, delete_token = 0
    WHERE calendar_id = NEW.calendar_id AND uid = NEW.uid AND delete_token = OLD.delete_token
      AND (OLD.delete_token != 0 OR had_master = 1)
      AND EXISTS (
            SELECT 1 FROM events AS e
            WHERE e.uid = NEW.uid AND e.deleted_at IS NOT NULL AND e.delete_token != OLD.delete_token
              AND ',' || event_series_deletes.hidden_ids || ',' LIKE '%,' || e.id || ',%');
    DELETE FROM event_series_deletes
    WHERE calendar_id = NEW.calendar_id AND uid = NEW.uid AND delete_token = OLD.delete_token
      AND (OLD.delete_token != 0 OR had_master = 1);
END;
```

```sql
CREATE TRIGGER events_master_insert_drops_stale_logs
AFTER INSERT ON events
WHEN NEW.recurrence_id = '' AND NEW.deleted_at IS NULL
BEGIN
    DELETE FROM event_series_deletes WHERE calendar_id = NEW.calendar_id AND uid = NEW.uid;
    DELETE FROM event_exdate_deletes WHERE calendar_id = NEW.calendar_id AND uid = NEW.uid;
END;
```

```sql
CREATE TRIGGER events_override_revive_drops_stale_logs
AFTER UPDATE OF deleted_at ON events
WHEN OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL AND NEW.recurrence_id != ''
BEGIN
    DELETE FROM event_exdate_deletes
    WHERE calendar_id = NEW.calendar_id AND uid = NEW.uid AND recurrence_id = NEW.recurrence_id AND pruned = 1;
    DELETE FROM event_series_deletes
    WHERE uid = NEW.uid AND hidden_ids = CAST(NEW.id AS TEXT)
      AND (had_master = 0 OR NOT EXISTS (
        SELECT 1 FROM events AS m
        WHERE m.calendar_id = event_series_deletes.calendar_id AND m.uid = NEW.uid
          AND m.recurrence_id = '' AND m.deleted_at IS NOT NULL));
    UPDATE event_series_deletes
    SET hidden_ids = TRIM(REPLACE(',' || hidden_ids || ',', ',' || NEW.id || ',', ','), ',')
    WHERE uid = NEW.uid AND ',' || hidden_ids || ',' LIKE '%,' || NEW.id || ',%';
    UPDATE event_truncate_deletes
    SET hidden_ids = TRIM(REPLACE(',' || hidden_ids || ',', ',' || NEW.id || ',', ','), ',')
    WHERE uid = NEW.uid AND hidden_ids IS NOT NULL AND ',' || hidden_ids || ',' LIKE '%,' || NEW.id || ',%';
    UPDATE event_exdate_deletes
    SET hidden_ids = TRIM(REPLACE(',' || hidden_ids || ',', ':' || NEW.id || ',', ':,'), ',')
    WHERE uid = NEW.uid AND ',' || hidden_ids || ',' LIKE '%:' || NEW.id || ',%';
END;
```

```sql
CREATE TRIGGER events_override_insert_drops_prune_marker
AFTER INSERT ON events
WHEN NEW.recurrence_id != '' AND NEW.deleted_at IS NULL
BEGIN
    DELETE FROM event_exdate_deletes
    WHERE calendar_id = NEW.calendar_id AND uid = NEW.uid AND recurrence_id = NEW.recurrence_id AND pruned = 1;
END;
```

```sql
CREATE TRIGGER events_purge_drops_row_from_logs
AFTER DELETE ON events
WHEN OLD.recurrence_id != ''
BEGIN
    DELETE FROM event_series_deletes
    WHERE uid = OLD.uid AND had_master = 0 AND hidden_ids = CAST(OLD.id AS TEXT);
    UPDATE event_series_deletes
    SET hidden_ids = TRIM(REPLACE(',' || hidden_ids || ',', ',' || OLD.id || ',', ','), ',')
    WHERE uid = OLD.uid AND ',' || hidden_ids || ',' LIKE '%,' || OLD.id || ',%';
    UPDATE event_truncate_deletes
    SET hidden_ids = TRIM(REPLACE(',' || hidden_ids || ',', ',' || OLD.id || ',', ','), ',')
    WHERE uid = OLD.uid AND hidden_ids IS NOT NULL AND ',' || hidden_ids || ',' LIKE '%,' || OLD.id || ',%';
    UPDATE event_exdate_deletes
    SET hidden_ids = TRIM(REPLACE(',' || hidden_ids || ',', ':' || OLD.id || ',', ':,'), ',')
    WHERE uid = OLD.uid AND ',' || hidden_ids || ',' LIKE '%:' || OLD.id || ',%';
END;
```

```sql
CREATE TRIGGER events_master_change_settles_unowned_edits
AFTER UPDATE OF deleted_at, calendar_id ON events
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
```

```sql
CREATE TRIGGER events_master_purge_settles_unowned_edits
AFTER DELETE ON events
WHEN OLD.recurrence_id = '' AND EXISTS (
    SELECT 1 FROM events AS u WHERE u.uid = OLD.uid AND u.edit_unowned = 1)
BEGIN
    INSERT INTO sync_resources (calendar_id, uid, owner_type, dirty, sync_strategy)
    SELECT DISTINCT s.owner_calendar_id, s.uid, 'event', 1, 'sync-token'
    FROM event_series_rows AS s
    JOIN events AS m ON m.calendar_id = s.owner_calendar_id AND m.uid = s.uid
        AND m.recurrence_id = '' AND m.deleted_at IS NULL
    JOIN calendars AS c ON c.id = s.owner_calendar_id AND c.account_id IS NOT NULL AND c.account_id != 0
    WHERE s.uid = OLD.uid AND s.id IN (
        SELECT u.id FROM events AS u WHERE u.uid = OLD.uid AND u.edit_unowned = 1 AND u.deleted_at IS NULL)
    ON CONFLICT(calendar_id, uid) DO UPDATE SET dirty = 1, rev = rev + 1;
    UPDATE events SET edit_unowned = 0
    WHERE uid = OLD.uid AND edit_unowned = 1
      AND id IN (SELECT s.id FROM event_series_rows AS s WHERE s.uid = OLD.uid);
END;
```
