-- +goose Up
-- Directory of .ics files that feeds a local calendar (issue #809). A
-- sync tool such as vdirsyncer or pimsync writes one .ics file for each
-- event into that directory. "chroncal ical import" without a path reads
-- every calendar that has a local_path. An empty value means the calendar
-- has no directory.
ALTER TABLE calendars ADD COLUMN local_path TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE calendars DROP COLUMN local_path;
