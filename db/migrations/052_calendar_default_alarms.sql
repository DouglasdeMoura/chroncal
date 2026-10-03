-- +goose Up
-- Per-calendar default-alarm setting (issue #815). The value is a
-- comma-separated default-alarm spec list ("-PT15M,AUDIO:-PT5M"). The
-- column is a tri-state:
--
--   NULL  the calendar inherits the global [alarms] default from config
--   ''    default alarms are off for this calendar
--   list  the calendar's own default-alarm specs
ALTER TABLE calendars ADD COLUMN default_alarms TEXT;

-- +goose Down
ALTER TABLE calendars DROP COLUMN default_alarms;
