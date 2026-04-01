-- +goose Up

-- Composite indexes for the most common calendar+time range queries.

-- Covers ListEventsByCalendarAndDateRange and similar queries.
-- Also satisfies calendar_id-only lookups (leftmost prefix), so drop
-- the now-redundant single-column index.
DROP INDEX IF EXISTS idx_events_calendar_id;
CREATE INDEX idx_events_cal_start ON events(calendar_id, start_time);

-- Covers ListTodosByCalendar ordering and calendar+due_date filtering.
DROP INDEX IF EXISTS idx_todos_calendar_id;
CREATE INDEX idx_todos_cal_due ON todos(calendar_id, due_date);
