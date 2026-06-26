-- +goose Up
-- Record which overrides a "this and following" truncation actually hid, so
-- restore re-shows only those and never resurrects an override the user had
-- already deleted independently (issue #287). Comma-separated RFC 3339
-- recurrence_ids; '' means the truncation hid no overrides. NULL marks
-- pre-#287 log rows that recorded no provenance, so restore falls back to the
-- old "un-hide every override at/after the cutoff" behavior for those.
ALTER TABLE event_truncate_deletes ADD COLUMN hidden_overrides TEXT;

-- +goose Down
ALTER TABLE event_truncate_deletes DROP COLUMN hidden_overrides;
