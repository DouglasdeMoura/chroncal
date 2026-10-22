package storage

import "context"

// ListOverridesByUIDs returns all recurrence overrides (rows with a non-empty
// recurrence_id) for the given master UIDs. Recurrence expansion uses it
// to avoid one SELECT per master. Results are ordered by uid then recurrence_id; soft-deleted rows are
// excluded. The UIDs are chunked so the IN clause never exceeds SQLite's
// parameter limit. Hand-written because database/sql has no slice-parameter
// support.
func (q *Queries) ListOverridesByUIDs(ctx context.Context, uids []string) ([]Event, error) {
	var out []Event
	for _, chunk := range chunkSlice(uids, sqliteINBatch) {
		placeholders, args := expandStringPlaceholders(chunk)
		where := "WHERE uid IN (" + placeholders + ") AND recurrence_id != '' AND deleted_at IS NULL /* ListOverridesByUIDs */"
		rows, err := q.queryEvents(ctx, where, args, "uid ASC, recurrence_id ASC")
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// FirstPerRecurrenceID keeps the first row of each recurrence ID, in the
// order of rows. The rows come in the order of the tie rule (see the query
// ListSeriesOverridesInCalendar), so the first row of an occurrence wins.
// The export, the edit, and the expansion share it.
func FirstPerRecurrenceID(rows []Event) []Event {
	seen := make(map[string]bool, len(rows))
	kept := make([]Event, 0, len(rows))
	for _, r := range rows {
		if !seen[r.RecurrenceID] {
			seen[r.RecurrenceID] = true
			kept = append(kept, r)
		}
	}
	return kept
}

// MasterKey names one master row of a UID in a calendar.
type MasterKey struct {
	CalendarID int64
	Uid        string
}

// ListMasterKeysByUIDs returns the calendar and the UID of each master row
// of the UIDs, live or in the trash. The expansion reads which override
// calendars hold a master row with it. The UIDs go in chunks, as in
// ListOverridesByUIDs.
func (q *Queries) ListMasterKeysByUIDs(ctx context.Context, uids []string) ([]MasterKey, error) {
	var out []MasterKey
	for _, chunk := range chunkSlice(uids, sqliteINBatch) {
		placeholders, args := expandStringPlaceholders(chunk)
		query := "SELECT calendar_id, uid FROM events WHERE uid IN (" + placeholders +
			") AND recurrence_id = '' /* ListMasterKeysByUIDs */"
		rows, err := q.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k MasterKey
			if err := rows.Scan(&k.CalendarID, &k.Uid); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, k)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SeriesOwner names the owner calendar of one override row.
type SeriesOwner struct {
	ID              int64
	OwnerCalendarID int64
}

// ListSeriesOwnersByUIDs returns the owner calendar of each live override
// row of the UIDs. It reads the view event_series_rows of migration 057.
// The result has no row for an override with no owner. The UIDs go in
// chunks, as in ListOverridesByUIDs.
func (q *Queries) ListSeriesOwnersByUIDs(ctx context.Context, uids []string) ([]SeriesOwner, error) {
	var out []SeriesOwner
	for _, chunk := range chunkSlice(uids, sqliteINBatch) {
		placeholders, args := expandStringPlaceholders(chunk)
		query := "SELECT id, owner_calendar_id FROM event_series_rows WHERE uid IN (" + placeholders +
			") AND recurrence_id != '' AND deleted_at IS NULL /* ListSeriesOwnersByUIDs */"
		rows, err := q.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var o SeriesOwner
			if err := rows.Scan(&o.ID, &o.OwnerCalendarID); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, o)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ListTodoOverridesByUIDs is the batched form of ListTodoOverridesByUID.
func (q *Queries) ListTodoOverridesByUIDs(ctx context.Context, uids []string) ([]Todo, error) {
	var out []Todo
	for _, chunk := range chunkSlice(uids, sqliteINBatch) {
		placeholders, args := expandStringPlaceholders(chunk)
		where := "WHERE uid IN (" + placeholders + ") AND recurrence_id != '' AND deleted_at IS NULL /* ListTodoOverridesByUIDs */"
		rows, err := q.queryTodos(ctx, where, args, "uid ASC, recurrence_id ASC")
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// ListJournalOverridesByUIDs is the batched form of ListJournalOverridesByUID.
func (q *Queries) ListJournalOverridesByUIDs(ctx context.Context, uids []string) ([]Journal, error) {
	var out []Journal
	for _, chunk := range chunkSlice(uids, sqliteINBatch) {
		placeholders, args := expandStringPlaceholders(chunk)
		where := "WHERE uid IN (" + placeholders + ") AND recurrence_id != '' AND deleted_at IS NULL /* ListJournalOverridesByUIDs */"
		rows, err := q.queryJournals(ctx, where, args, "uid ASC, recurrence_id ASC")
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// chunkSlice splits vals into consecutive slices of at most size elements.
// Returns nil for empty input. The returned slices alias vals (no copy).
func chunkSlice[T any](vals []T, size int) [][]T {
	if len(vals) == 0 {
		return nil
	}
	var chunks [][]T
	for i := 0; i < len(vals); i += size {
		end := i + size
		if end > len(vals) {
			end = len(vals)
		}
		chunks = append(chunks, vals[i:end])
	}
	return chunks
}
