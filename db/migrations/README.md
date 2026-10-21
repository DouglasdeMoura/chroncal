# Migrations

Goose runs the SQL files of this directory in order. `make generate` reads
them as the schema of sqlc.

## Rebuild of the events table

SQLite cannot drop a table constraint, so a change of a constraint of
`events` rebuilds the table (see migration 050): create `events_new`, copy
the rows, drop `events`, and rename `events_new`.

The view `event_series_rows` (migration 057) reads `events`. SQLite refuses
the rename while a view reads the dropped table. A rebuild of `events`
must follow these steps:

1. Drop the view: `DROP VIEW IF EXISTS event_series_rows;`
2. Rebuild the table as migration 050 does.
3. Create the view again with the statement below.
4. Create the trigger `events_master_revive_drops_series_log` again. The
   drop of `events` drops it. Copy its statement from migration 057.

The trigger drops the series-delete log of a calendar when a master of
that calendar comes back from the trash. Without it, a log that a pull or
an import left behind brings back the wrong rows on the next restore.

The statement must stay equal to the one in migration 057. The storage
test `TestEventsRebuildKeepsSeriesView` runs this sequence against the
statement of migration 057.

```sql
CREATE VIEW event_series_rows AS
SELECT
    e.id AS id,
    e.uid AS uid,
    e.calendar_id AS calendar_id,
    e.recurrence_id AS recurrence_id,
    e.deleted_at AS deleted_at,
    c.calendar_id AS owner_calendar_id
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
    e.calendar_id AS owner_calendar_id
FROM events AS e
WHERE NOT EXISTS (
    SELECT 1 FROM events AS m WHERE m.uid = e.uid AND m.recurrence_id = '');
```
