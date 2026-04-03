package storage

import "database/sql"

// scanEvents reads rows into []Event. Column order must match the events table:
// id, uid, calendar_id, title, description, location, start_time, end_time,
// all_day, recurrence_rule, timezone, status, transp, sequence, priority,
// class, url, exdates, rdates, recurrence_id, geo, created_at, updated_at,
// duration, dtstamp.
func scanEvents(rows *sql.Rows) ([]Event, error) {
	defer rows.Close()
	items := make([]Event, 0, 64)
	for rows.Next() {
		var i Event
		if err := rows.Scan(
			&i.ID, &i.Uid, &i.CalendarID, &i.Title, &i.Description,
			&i.Location, &i.StartTime, &i.EndTime, &i.AllDay,
			&i.RecurrenceRule, &i.Timezone, &i.Status, &i.Transp,
			&i.Sequence, &i.Priority, &i.Class, &i.Url, &i.Exdates,
			&i.Rdates, &i.RecurrenceID, &i.Geo, &i.CreatedAt,
			&i.UpdatedAt, &i.Duration, &i.Dtstamp,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// scanTodos reads rows into []Todo. Column order must match the todos table:
// id, uid, calendar_id, summary, description, location, due_date, start_date,
// duration, completed_at, percent_complete, status, priority, class, url,
// recurrence_rule, timezone, sequence, exdates, rdates, recurrence_id, geo,
// created_at, updated_at, dtstamp.
func scanTodos(rows *sql.Rows) ([]Todo, error) {
	defer rows.Close()
	items := make([]Todo, 0, 64)
	for rows.Next() {
		var i Todo
		if err := rows.Scan(
			&i.ID, &i.Uid, &i.CalendarID, &i.Summary, &i.Description,
			&i.Location, &i.DueDate, &i.StartDate, &i.Duration,
			&i.CompletedAt, &i.PercentComplete, &i.Status, &i.Priority,
			&i.Class, &i.Url, &i.RecurrenceRule, &i.Timezone,
			&i.Sequence, &i.Exdates, &i.Rdates, &i.RecurrenceID,
			&i.Geo, &i.CreatedAt, &i.UpdatedAt, &i.Dtstamp,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// scanJournals reads rows into []Journal. Column order must match the journals table:
// id, uid, calendar_id, summary, description, start_date, status, class, url,
// recurrence_rule, timezone, sequence, exdates, rdates, recurrence_id, dtstamp,
// created_at, updated_at.
func scanJournals(rows *sql.Rows) ([]Journal, error) {
	defer rows.Close()
	items := make([]Journal, 0, 64)
	for rows.Next() {
		var i Journal
		if err := rows.Scan(
			&i.ID, &i.Uid, &i.CalendarID, &i.Summary, &i.Description,
			&i.StartDate, &i.Status, &i.Class, &i.Url,
			&i.RecurrenceRule, &i.Timezone, &i.Sequence,
			&i.Exdates, &i.Rdates, &i.RecurrenceID, &i.Dtstamp,
			&i.CreatedAt, &i.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}
