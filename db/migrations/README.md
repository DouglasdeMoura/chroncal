# Migrations

Goose runs the SQL files of this directory in order. `make generate` reads
them as the schema of sqlc.

## Rebuild of the events table

SQLite cannot drop a table constraint, so a change of a constraint of
`events` rebuilds the table (see migration 050). The rebuild creates
`events_new`, copies the rows, drops `events`, and renames `events_new`.

The view `event_series_rows` (migration 057) reads `events`. SQLite refuses
the rename while a view reads the dropped table. A rebuild of `events`
must follow these steps:

1. Drop the view: `DROP VIEW IF EXISTS event_series_rows;`
2. Rebuild the table as migration 050 does.
3. Create the view again with the statement below.
4. Create the five triggers of migration 057 again:
   `events_master_revive_drops_series_log`,
   `events_master_insert_drops_stale_logs`,
   `events_override_revive_drops_stale_logs`,
   `events_override_insert_drops_prune_marker`, and
   `events_purge_drops_row_from_logs`. The drop of `events` drops them.
   Copy their statements from migration 057.

The triggers drop the stale delete logs of a calendar. The revive
trigger of a master drops the series-delete log when a master of that
calendar comes back from the trash. The insert trigger of a master drops
the series-delete log and the occurrence logs of the UID when a new live
master arrives. The two triggers of an override drop the pruned marker
of an occurrence when its row comes back. The revive trigger also
removes the row from the delete logs. Without them, a log that a pull or
an import left behind can bring back the wrong rows on the next restore.
It can also strip an imported EXDATE. The purge trigger removes a purged
override row from the delete logs, so a log never names a row that is
gone.

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
    DELETE FROM event_series_deletes WHERE calendar_id = NEW.calendar_id AND uid = NEW.uid;
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
      AND NOT EXISTS (
        SELECT 1 FROM events AS m
        WHERE m.calendar_id = event_series_deletes.calendar_id AND m.uid = NEW.uid
          AND m.recurrence_id = '' AND m.deleted_at IS NOT NULL);
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
