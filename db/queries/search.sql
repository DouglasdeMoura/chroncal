-- name: SearchEvents :many
SELECT * FROM events
WHERE (
    lower_unicode(title) LIKE '%' || lower_unicode(sqlc.arg(query)) || '%' OR
    lower_unicode(description) LIKE '%' || lower_unicode(sqlc.arg(query)) || '%' OR
    lower_unicode(location) LIKE '%' || lower_unicode(sqlc.arg(query)) || '%' OR
    EXISTS (SELECT 1 FROM event_categories ec WHERE ec.event_id = events.id AND lower_unicode(ec.category) LIKE '%' || lower_unicode(sqlc.arg(query)) || '%')
)
AND (sqlc.arg(calendar_id) = 0 OR calendar_id = sqlc.arg(calendar_id))
AND (sqlc.arg(from_time) = '' OR start_time >= sqlc.arg(from_time))
AND (sqlc.arg(to_time) = '' OR start_time < sqlc.arg(to_time))
AND (sqlc.arg(filter_status) = '' OR status = sqlc.arg(filter_status))
ORDER BY start_time ASC;

-- name: SearchTodos :many
SELECT * FROM todos
WHERE (
    lower_unicode(summary) LIKE '%' || lower_unicode(sqlc.arg(query)) || '%' OR
    lower_unicode(description) LIKE '%' || lower_unicode(sqlc.arg(query)) || '%' OR
    lower_unicode(location) LIKE '%' || lower_unicode(sqlc.arg(query)) || '%' OR
    EXISTS (SELECT 1 FROM todo_categories tc WHERE tc.todo_id = todos.id AND lower_unicode(tc.category) LIKE '%' || lower_unicode(sqlc.arg(query)) || '%')
)
AND (sqlc.arg(calendar_id) = 0 OR calendar_id = sqlc.arg(calendar_id))
AND (sqlc.arg(filter_status) = '' OR status = sqlc.arg(filter_status))
AND (sqlc.arg(completed_filter) = 0 OR (sqlc.arg(completed_filter) = 1 AND completed_at IS NOT NULL) OR (sqlc.arg(completed_filter) = 2 AND completed_at IS NULL))
ORDER BY due_date ASC, summary ASC;
