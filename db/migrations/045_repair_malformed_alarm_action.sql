-- +goose Up

-- Repair a stored alarm action that is not an RFC 5545 iana-token or
-- x-name (issue #595). A build older than that rule stored any non-empty
-- value, so a row can hold " " or "NO NE". Export writes such a value as a
-- malformed ACTION line, and a strict CalDAV server rejects the whole
-- resource for it.
--
-- The write rule now refuses these values, which left every path that
-- reads a stored row and writes it back with a choice between two bad
-- outcomes: fail every edit of the owning record, or drop the row and
-- delete another client's VALARM on the next push. Repair the value once
-- here instead, so no later path has to compensate.
--
-- The repair writes X-CHRONCAL-UNSUPPORTED, not DISPLAY. A row with a
-- malformed action holds the VALARM of another client, which an older
-- build stored verbatim. DISPLAY would make that row a normal reminder:
-- the carry-over would stop protecting it, so the next --alarm edit would
-- delete it; the UID backfill would stamp a chroncal UID on it; and the
-- alarm engine would fire it. X-CHRONCAL-UNSUPPORTED is a valid x-name, so
-- every write rule accepts it, and it stays outside the fireable set, so
-- the row keeps the treatment a preserved foreign alarm gets (issue #603).
--
-- The repair loses the original malformed bytes. Export could never write
-- them, and the engine could never fire them, so the row keeps every
-- behavior it had.
--
-- The GLOB pattern matches a value that holds a character outside the
-- token set (ALPHA, DIGIT, and "-"). It mirrors model.ValidAlarmActionToken.
-- A preserved foreign action such as X-APPLE-SOUND or the Google NONE
-- sentinel holds only token characters, so this migration leaves it alone.

UPDATE event_alarms
SET action = 'X-CHRONCAL-UNSUPPORTED'
WHERE action GLOB '*[^A-Za-z0-9-]*';

UPDATE todo_alarms
SET action = 'X-CHRONCAL-UNSUPPORTED'
WHERE action GLOB '*[^A-Za-z0-9-]*';

-- +goose Down

-- The repair is irreversible. The original malformed value is gone, and no
-- column records it, so a Down cannot restore it. This statement is
-- deliberately empty.
SELECT 1;
