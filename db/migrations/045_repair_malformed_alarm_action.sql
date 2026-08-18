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
-- DISPLAY is the fallback the parser, the write rule, and the exporter all
-- use for an action they cannot represent, so the repaired row keeps the
-- behavior the exporter already gave it.
--
-- The GLOB pattern matches a value that holds a character outside the
-- token set (ALPHA, DIGIT, and "-"). It mirrors model.ValidAlarmActionToken.
-- A preserved foreign action such as X-APPLE-SOUND or the Google NONE
-- sentinel holds only token characters, so this migration leaves it alone.

UPDATE event_alarms
SET action = 'DISPLAY'
WHERE action = '' OR action GLOB '*[^A-Za-z0-9-]*';

UPDATE todo_alarms
SET action = 'DISPLAY'
WHERE action = '' OR action GLOB '*[^A-Za-z0-9-]*';

-- +goose Down

-- The repair is irreversible. The original malformed value is gone, and no
-- column records it, so a Down cannot tell a repaired row from a row that
-- always held DISPLAY. This statement is deliberately empty.
SELECT 1;
