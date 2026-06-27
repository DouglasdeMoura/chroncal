-- +goose Up
-- Record the master RDATEs a "this and following" truncation dropped, so
-- restore re-adds exactly those (issue #463). rrule-go expands RDATEs
-- independently of the RRULE's UNTIL bound, so truncation must trim post-cutoff
-- RDATEs too — and remember them to make the delete reversible. Comma-separated
-- RFC 3339 (or date-only) values; '' means the truncation dropped no RDATEs.
-- NULL marks pre-#463 log rows that recorded no RDATE provenance.
ALTER TABLE event_truncate_deletes ADD COLUMN removed_rdates TEXT;

-- +goose Down
ALTER TABLE event_truncate_deletes DROP COLUMN removed_rdates;
