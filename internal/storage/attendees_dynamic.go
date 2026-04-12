package storage

import (
	"context"
	"strings"
)

// ListAttendeesByEventIDs returns attendees for the given event IDs.
// Hand-written because database/sql does not support slice parameters.
func (q *Queries) ListAttendeesByEventIDs(ctx context.Context, ids []int64) ([]EventAttendee, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	query := "SELECT id, event_id, email, name, rsvp_status, role, organizer, cutype, rsvp, sent_by, delegated_to, delegated_from, member, dir, language FROM event_attendees WHERE event_id IN (" + placeholders + ") ORDER BY event_id, organizer DESC, name"

	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []EventAttendee
	for rows.Next() {
		var i EventAttendee
		if err := rows.Scan(
			&i.ID, &i.EventID, &i.Email, &i.Name,
			&i.RsvpStatus, &i.Role, &i.Organizer, &i.Cutype,
			&i.Rsvp, &i.SentBy, &i.DelegatedTo, &i.DelegatedFrom,
			&i.Member, &i.Dir, &i.Language,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}
