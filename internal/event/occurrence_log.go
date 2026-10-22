package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/softdelete"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// This file binds the occurrence rule of softdelete to the event queries. An
// occurrence log records each delete of one occurrence X of a series (see
// softdelete.RecordOccurrenceDelete and softdelete.SettleOccurrence).

// parseOccurrenceLog reads the entries of the occurrence log l. hidden_ids
// holds one seq:row item for each hidden row, and seq: for a delete with no
// row (migration 057).
func parseOccurrenceLog(l storage.EventExdateDelete) (softdelete.OccurrenceLog, error) {
	log := softdelete.OccurrenceLog{ID: l.ID, OwnsExdate: l.OwnsExdate == 1, LastSeq: l.DeleteSeq}
	if l.HiddenIds == "" {
		return log, nil
	}
	index := make(map[int64]int)
	for _, item := range strings.Split(l.HiddenIds, ",") {
		seqText, rowText, ok := strings.Cut(item, ":")
		if !ok {
			return softdelete.OccurrenceLog{}, fmt.Errorf("exdate log %d: malformed entry %q", l.ID, item)
		}
		seq, err := strconv.ParseInt(seqText, 10, 64)
		if err != nil {
			return softdelete.OccurrenceLog{}, fmt.Errorf("exdate log %d: malformed entry %q: %w", l.ID, item, err)
		}
		i, seen := index[seq]
		if !seen {
			i = len(log.Entries)
			index[seq] = i
			log.Entries = append(log.Entries, softdelete.OccurrenceEntry{Seq: seq})
		}
		if rowText == "" {
			continue
		}
		row, err := strconv.ParseInt(rowText, 10, 64)
		if err != nil {
			return softdelete.OccurrenceLog{}, fmt.Errorf("exdate log %d: malformed entry %q: %w", l.ID, item, err)
		}
		log.Entries[i].Rows = append(log.Entries[i].Rows, row)
	}
	return log, nil
}

// formatOccurrenceEntries is the inverse of parseOccurrenceLog for the
// entries.
func formatOccurrenceEntries(entries []softdelete.OccurrenceEntry) string {
	var items []string
	for _, e := range entries {
		seq := strconv.FormatInt(e.Seq, 10)
		if len(e.Rows) == 0 {
			items = append(items, seq+":")
			continue
		}
		for _, r := range e.Rows {
			items = append(items, seq+":"+strconv.FormatInt(r, 10))
		}
	}
	return strings.Join(items, ",")
}

// occurrenceLogTx reads the occurrence log of recID in calendarID. found is
// false with no log. A pruned marker is no log.
func occurrenceLogTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid, recID string) (softdelete.OccurrenceLog, bool, error) {
	l, found, err := exdateLogTx(ctx, qtx, calendarID, uid, recID)
	if err != nil || !found {
		return softdelete.OccurrenceLog{}, false, err
	}
	log, err := parseOccurrenceLog(l)
	return log, err == nil, err
}

// occurrenceStoreTx binds the queries of the occurrence log of recID in
// calendarID. A delete upserts the log on its key, so the time of the delete
// moves and a pruned marker turns into a log. A restore updates the log by
// its ID, and the time stays.
func occurrenceStoreTx(qtx *storage.Queries, calendarID int64, uid, recID string, upsert bool) softdelete.OccurrenceStore {
	return softdelete.OccurrenceStore{
		UpdateExdates: func(ctx context.Context, masterID int64, exdates string) error {
			return qtx.UpdateEventExdates(ctx, storage.UpdateEventExdatesParams{Exdates: storage.StringToNullable(exdates), ID: masterID})
		},
		SaveLog: func(ctx context.Context, log softdelete.OccurrenceLog) (int64, error) {
			owns := int64(0)
			if log.OwnsExdate {
				owns = 1
			}
			if upsert {
				return qtx.RecordEventExdateDelete(ctx, storage.RecordEventExdateDeleteParams{
					CalendarID: calendarID, Uid: uid, RecurrenceID: recID,
					HiddenIds: formatOccurrenceEntries(log.Entries), OwnsExdate: owns, DeleteSeq: log.LastSeq,
				})
			}
			return log.ID, qtx.UpdateEventExdateDeleteState(ctx, storage.UpdateEventExdateDeleteStateParams{
				HiddenIds: formatOccurrenceEntries(log.Entries), OwnsExdate: owns, ID: log.ID,
			})
		},
		DeleteLog: qtx.DeleteEventExdateDelete,
	}
}

// occurrenceOf returns the state of the occurrence recID of the series of
// master, after the write of the caller. master is the row that the caller
// holds in the transaction.
func occurrenceOf(ctx context.Context, qtx *storage.Queries, master storage.Event, recID string) (softdelete.Occurrence, error) {
	at, err := timeutil.ParseRecurrenceID(recID)
	if err != nil {
		return softdelete.Occurrence{}, fmt.Errorf("parse recurrence_id %q: %w", recID, err)
	}
	// An all-day master keeps date-only EXDATEs, so the EXDATE serializes
	// as VALUE=DATE, as DTSTART does (RFC 5545 section 3.8.5.1, issue #221).
	if master.AllDay == 1 {
		at = timeutil.AsDateOnly(at)
	}
	live, err := qtx.ListSeriesOverridesAtRecurrenceID(ctx, storage.ListSeriesOverridesAtRecurrenceIDParams{
		CalendarID: master.CalendarID, Uid: master.Uid, RecurrenceID: recID,
	})
	if err != nil {
		return softdelete.Occurrence{}, fmt.Errorf("list overrides: %w", err)
	}
	return softdelete.Occurrence{
		RecurrenceID: recID, Exdate: at,
		MasterID: master.ID, MasterExdates: storage.NullableToString(master.Exdates),
		LiveOverride: len(live) > 0,
	}, nil
}

// recordOccurrenceDeleteTx records a delete that hid rows at recID in the
// series of the live master. The caller hides the rows first, so the check
// of a live override sees the state after the delete. It returns the log ID
// and the seq of the delete, for the undo.
func recordOccurrenceDeleteTx(ctx context.Context, qtx *storage.Queries, master storage.Event, recID string, rows []int64) (logID, seq int64, err error) {
	log, _, err := occurrenceLogTx(ctx, qtx, master.CalendarID, master.Uid, recID)
	if err != nil {
		return 0, 0, err
	}
	o, err := occurrenceOf(ctx, qtx, master, recID)
	if err != nil {
		return 0, 0, err
	}
	return softdelete.RecordOccurrenceDelete(ctx, occurrenceStoreTx(qtx, master.CalendarID, master.Uid, recID, true), o, log, rows)
}

// settleOccurrenceTx writes the occurrence log of recID in the series of the
// live master after a restore (see softdelete.SettleOccurrence). change
// removes the restored rows or entries from the log. No log means nothing to
// settle.
func settleOccurrenceTx(ctx context.Context, qtx *storage.Queries, master storage.Event, recID string, change func(softdelete.OccurrenceLog) softdelete.OccurrenceLog) error {
	log, found, err := occurrenceLogTx(ctx, qtx, master.CalendarID, master.Uid, recID)
	if err != nil || !found {
		return err
	}
	o, err := occurrenceOf(ctx, qtx, master, recID)
	if err != nil {
		return err
	}
	return softdelete.SettleOccurrence(ctx, occurrenceStoreTx(qtx, master.CalendarID, master.Uid, recID, false), o, change(log))
}

// withoutRows returns a change for settleOccurrenceTx that removes ids.
func withoutRows(ids ...int64) func(softdelete.OccurrenceLog) softdelete.OccurrenceLog {
	return func(l softdelete.OccurrenceLog) softdelete.OccurrenceLog { return l.WithoutRows(ids) }
}

// settleLiveOverrideTx holds the invariant after a write that leaves the
// override row r live: a pull upsert or an import. A master of the series
// that is not live, or no master, leaves nothing to settle.
func settleLiveOverrideTx(ctx context.Context, qtx *storage.Queries, r storage.Event) error {
	if r.RecurrenceID == "" || r.DeletedAt != nil {
		return nil
	}
	master, err := findSeriesMasterTx(ctx, qtx, r.CalendarID, r.Uid)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrAmbiguousUID) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get master: %w", err)
	}
	return settleOccurrenceTx(ctx, qtx, master, r.RecurrenceID, withoutRows(r.ID))
}

// rowStatesTx reads the calendar and the trash state of each row of ids in
// one query. A purged row is not in the result.
func rowStatesTx(ctx context.Context, qtx *storage.Queries, ids []int64) ([]storage.ListEventRowStatesByIDsRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := qtx.ListEventRowStatesByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list states of rows: %w", err)
	}
	return rows, nil
}

// rowCalendars returns the distinct calendars of rows, in the order of rows.
func rowCalendars(rows []storage.ListEventRowStatesByIDsRow) []int64 {
	seen := make(map[int64]bool, len(rows))
	var out []int64
	for _, r := range rows {
		if !seen[r.CalendarID] {
			seen[r.CalendarID] = true
			out = append(out, r.CalendarID)
		}
	}
	return out
}

// guardRowCalendarsTx enforces the remote access policy on the calendar of
// each row. It skips calendarID, the calendar of the series, which the caller
// guards. A legacy override lives in another calendar than its master, and
// its restore writes that calendar.
func guardRowCalendarsTx(ctx context.Context, qtx *storage.Queries, calendarID int64, rows []storage.ListEventRowStatesByIDsRow) error {
	for _, c := range rowCalendars(rows) {
		if c == calendarID {
			continue
		}
		if err := calendaraccess.EnsureWritable(ctx, qtx, c, "VEVENT"); err != nil {
			return err
		}
	}
	return nil
}

// restoreHiddenRowsTx un-hides the rows of ids in one statement, after the
// guard of the calendar of each row. One read sorts the rows. A row in the
// trash comes back. A live row is a restore that came first, and a purged row
// is gone. It returns the IDs that came back. With no row back, notBack is
// the error of that state: ErrAlreadyRestored when a row is live, else
// ErrNotDeleted.
func restoreHiddenRowsTx(ctx context.Context, qtx *storage.Queries, calendarID int64, ids []int64) (restored []int64, notBack, err error) {
	rows, err := rowStatesTx(ctx, qtx, ids)
	if err != nil {
		return nil, nil, err
	}
	if err := guardRowCalendarsTx(ctx, qtx, calendarID, rows); err != nil {
		return nil, nil, err
	}
	notBack = ErrNotDeleted
	for _, r := range rows {
		if r.DeletedAt == nil {
			notBack = ErrAlreadyRestored
			continue
		}
		restored = append(restored, r.ID)
	}
	if len(restored) == 0 {
		if len(ids) == 0 {
			return nil, nil, nil
		}
		return nil, notBack, nil
	}
	// The revive trigger of migration 057 removes each row from the delete
	// logs of its UID.
	if _, err := qtx.RestoreEventsByIDs(ctx, restored); err != nil {
		return nil, nil, fmt.Errorf("restore rows: %w", err)
	}
	return restored, nil, nil
}
