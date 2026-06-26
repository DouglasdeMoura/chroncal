package storage

import "context"

// overrideUIDBatch bounds how many UIDs go into a single `uid IN (...)` override
// query. SQLite (modernc) caps host parameters at 32766; staying well under that
// keeps the batched fetch from failing on accounts with very many recurring
// masters, at the cost of a few extra queries instead of one.
const overrideUIDBatch = 500

// ListOverridesByUIDs returns all recurrence overrides (rows with a non-empty
// recurrence_id) for the given master UIDs. It is the batched form of
// ListOverridesByUID, used by recurrence expansion to avoid one SELECT per
// master. Results are ordered by uid then recurrence_id; soft-deleted rows are
// excluded. The UIDs are chunked so the IN clause never exceeds SQLite's
// parameter limit. Hand-written because database/sql has no slice-parameter
// support.
func (q *Queries) ListOverridesByUIDs(ctx context.Context, uids []string) ([]Event, error) {
	var out []Event
	for _, chunk := range chunkSlice(uids, overrideUIDBatch) {
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

// ListTodoOverridesByUIDs is the batched form of ListTodoOverridesByUID.
func (q *Queries) ListTodoOverridesByUIDs(ctx context.Context, uids []string) ([]Todo, error) {
	var out []Todo
	for _, chunk := range chunkSlice(uids, overrideUIDBatch) {
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
	for _, chunk := range chunkSlice(uids, overrideUIDBatch) {
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
