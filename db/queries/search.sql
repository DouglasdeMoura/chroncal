-- name: SearchEvents :many
SELECT * FROM events_v
WHERE (
    title LIKE '%' || sqlc.arg(query) || '%' OR
    description LIKE '%' || sqlc.arg(query) || '%' OR
    location LIKE '%' || sqlc.arg(query) || '%' OR
    EXISTS (SELECT 1 FROM event_categories ec WHERE ec.event_id = events_v.id AND ec.category LIKE '%' || sqlc.arg(query) || '%')
)
AND (sqlc.arg(calendar_id) = 0 OR calendar_id = sqlc.arg(calendar_id))
AND (sqlc.arg(from_time) = '' OR start_time >= sqlc.arg(from_time))
AND (sqlc.arg(to_time) = '' OR start_time < sqlc.arg(to_time))
AND (sqlc.arg(filter_status) = '' OR status = sqlc.arg(filter_status))
ORDER BY start_time ASC;

-- name: SearchTodos :many
SELECT * FROM todos_v
WHERE (
    summary LIKE '%' || sqlc.arg(query) || '%' OR
    description LIKE '%' || sqlc.arg(query) || '%' OR
    location LIKE '%' || sqlc.arg(query) || '%' OR
    EXISTS (SELECT 1 FROM todo_categories tc WHERE tc.todo_id = todos_v.id AND tc.category LIKE '%' || sqlc.arg(query) || '%')
)
AND (sqlc.arg(calendar_id) = 0 OR calendar_id = sqlc.arg(calendar_id))
AND (sqlc.arg(filter_status) = '' OR status = sqlc.arg(filter_status))
AND (sqlc.arg(completed_filter) = 0 OR (sqlc.arg(completed_filter) = 1 AND completed_at != '') OR (sqlc.arg(completed_filter) = 2 AND completed_at = ''))
ORDER BY due_date ASC, summary ASC;
