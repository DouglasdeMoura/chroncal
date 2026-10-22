package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// The queries of this file read the delete logs of one UID that name a row of
// a set of row IDs. The identity check of an undo (identityRefusalTx in the
// event service) needs only those logs. sqlc cannot bind an argument inside
// json_each, so the queries live here.
//
// The row IDs go in one bound value, a JSON array, so the count of IDs has no
// parameter limit. The uid filter reads the index on uid of each log table.
// Each log stores its rows as a list with delimiters, and the match includes
// the delimiters. Row 1 thus never matches row 12:
//   - a series-delete log and a truncation log: ",1,2,3" with commas;
//   - an occurrence log: "seq:row" entries with commas, so a row follows ":".

// rowIDsJSON encodes ids as a JSON array for json_each.
func rowIDsJSON(ids []int64) (string, error) {
	b, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("encode row IDs: %w", err)
	}
	return string(b), nil
}

const listEventExdateDeletesNamingRows = `SELECT d.id, d.calendar_id, d.uid, d.recurrence_id, d.deleted_at, d.pruned, d.hidden_ids, d.owns_exdate, d.delete_seq
FROM event_exdate_deletes AS d
WHERE d.uid = ? AND d.pruned = 0 AND EXISTS (
    SELECT 1 FROM json_each(?) AS j
    WHERE instr(',' || d.hidden_ids || ',', ':' || j.value || ',') > 0)
ORDER BY d.id`

// ListEventExdateDeletesNamingRows returns the occurrence logs of uid, with no
// pruned marker, that name a row of ids.
func (q *Queries) ListEventExdateDeletesNamingRows(ctx context.Context, uid string, ids []int64) ([]EventExdateDelete, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	arg, err := rowIDsJSON(ids)
	if err != nil {
		return nil, err
	}
	rows, err := q.db.QueryContext(ctx, listEventExdateDeletesNamingRows, uid, arg)
	if err != nil {
		return nil, err
	}
	return scanLogRows(rows, func(r *sql.Rows) (EventExdateDelete, error) {
		var i EventExdateDelete
		err := r.Scan(&i.ID, &i.CalendarID, &i.Uid, &i.RecurrenceID, &i.DeletedAt, &i.Pruned, &i.HiddenIds, &i.OwnsExdate, &i.DeleteSeq)
		return i, err
	})
}

const listEventSeriesDeletesNamingRows = `SELECT d.id, d.calendar_id, d.uid, d.hidden_ids, d.deleted_at, d.had_master, d.delete_token
FROM event_series_deletes AS d
WHERE d.uid = ? AND EXISTS (
    SELECT 1 FROM json_each(?) AS j
    WHERE instr(',' || d.hidden_ids || ',', ',' || j.value || ',') > 0)
ORDER BY d.id`

// ListEventSeriesDeletesNamingRows returns the series-delete logs of uid that
// name a row of ids.
func (q *Queries) ListEventSeriesDeletesNamingRows(ctx context.Context, uid string, ids []int64) ([]EventSeriesDelete, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	arg, err := rowIDsJSON(ids)
	if err != nil {
		return nil, err
	}
	rows, err := q.db.QueryContext(ctx, listEventSeriesDeletesNamingRows, uid, arg)
	if err != nil {
		return nil, err
	}
	return scanLogRows(rows, func(r *sql.Rows) (EventSeriesDelete, error) {
		var i EventSeriesDelete
		err := r.Scan(&i.ID, &i.CalendarID, &i.Uid, &i.HiddenIds, &i.DeletedAt, &i.HadMaster, &i.DeleteToken)
		return i, err
	})
}

const listEventTruncateDeletesNamingRows = `SELECT d.id, d.calendar_id, d.uid, d.cutoff_time, d.previous_rrule, d.deleted_at, d.hidden_overrides, d.removed_rdates, d.hidden_ids, d.delete_token
FROM event_truncate_deletes AS d
WHERE d.uid = ? AND d.hidden_ids IS NOT NULL AND EXISTS (
    SELECT 1 FROM json_each(?) AS j
    WHERE instr(',' || d.hidden_ids || ',', ',' || j.value || ',') > 0)
ORDER BY d.id`

// ListEventTruncateDeletesNamingRows returns the truncation logs of uid that
// name a row of ids. A log from before the row IDs (hidden_ids NULL) names
// no row.
func (q *Queries) ListEventTruncateDeletesNamingRows(ctx context.Context, uid string, ids []int64) ([]EventTruncateDelete, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	arg, err := rowIDsJSON(ids)
	if err != nil {
		return nil, err
	}
	rows, err := q.db.QueryContext(ctx, listEventTruncateDeletesNamingRows, uid, arg)
	if err != nil {
		return nil, err
	}
	return scanLogRows(rows, func(r *sql.Rows) (EventTruncateDelete, error) {
		var i EventTruncateDelete
		err := r.Scan(&i.ID, &i.CalendarID, &i.Uid, &i.CutoffTime, &i.PreviousRrule, &i.DeletedAt, &i.HiddenOverrides, &i.RemovedRdates, &i.HiddenIds, &i.DeleteToken)
		return i, err
	})
}

// scanLogRows reads every row of rows with scan, and closes rows.
func scanLogRows[T any](rows *sql.Rows, scan func(*sql.Rows) (T, error)) ([]T, error) {
	defer rows.Close()
	var items []T
	for rows.Next() {
		i, err := scan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}
