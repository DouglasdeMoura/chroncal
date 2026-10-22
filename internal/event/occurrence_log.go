package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

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

// rowStatesTx reads the calendar and the trash state of each row of ids. A
// purged row is not in the result.
func rowStatesTx(ctx context.Context, qtx *storage.Queries, ids []int64) ([]storage.ListEventRowStatesByIDsRow, error) {
	// The IN list goes in chunks under the parameter limit of SQLite.
	var rows []storage.ListEventRowStatesByIDsRow
	for _, chunk := range storage.ChunkSlice(ids, idChunk) {
		part, err := qtx.ListEventRowStatesByIDs(ctx, chunk)
		if err != nil {
			return nil, fmt.Errorf("list states of rows: %w", err)
		}
		rows = append(rows, part...)
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
// is gone. With no row back, notBack is the error of that state:
// ErrAlreadyRestored when a row is live, else ErrNotDeleted.
func restoreHiddenRowsTx(ctx context.Context, qtx *storage.Queries, calendarID int64, ids []int64) (notBack, err error) {
	rows, err := rowStatesTx(ctx, qtx, ids)
	if err != nil {
		return nil, err
	}
	notBack = ErrNotDeleted
	trashed := make([]storage.ListEventRowStatesByIDsRow, 0, len(rows))
	for _, r := range rows {
		if r.DeletedAt == nil {
			notBack = ErrAlreadyRestored
			continue
		}
		trashed = append(trashed, r)
	}
	if len(trashed) == 0 {
		if len(ids) == 0 {
			return nil, nil
		}
		return notBack, nil
	}
	_, err = restoreStatesTx(ctx, qtx, calendarID, trashed)
	return nil, err
}

// restoreStatesTx un-hides the rows of states in one statement, after the
// guard of the calendar of each row. calendarID is the calendar of the
// series, which the caller guards; 0 guards every calendar. The caller read
// the states in the transaction. It returns the IDs that came back.
func restoreStatesTx(ctx context.Context, qtx *storage.Queries, calendarID int64, states []storage.ListEventRowStatesByIDsRow) ([]int64, error) {
	if len(states) == 0 {
		return nil, nil
	}
	if err := guardRowCalendarsTx(ctx, qtx, calendarID, states); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(states))
	for _, r := range states {
		ids = append(ids, r.ID)
	}
	// The revive trigger of migration 057 removes each row from the delete
	// logs of its UID.
	if _, err := restoreEventsByIDsTx(ctx, qtx, ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// sameExdateSet reports whether the EXDATE lists a and b hold the same
// instants, with the same count of each. The order and the text form do not
// count.
func sameExdateSet(a, b string) bool {
	count := make(map[string]int)
	for _, t := range timeutil.ParseTimeList(a) {
		count[t.UTC().Format(time.RFC3339)]++
	}
	for _, t := range timeutil.ParseTimeList(b) {
		count[t.UTC().Format(time.RFC3339)]--
	}
	for _, n := range count {
		if n != 0 {
			return false
		}
	}
	return true
}

// purgedOccurrence is an occurrence of a live series that a purge touches.
type purgedOccurrence struct {
	master storage.Event
	recID  string
}

// purgedOccurrencesTx resolves the live master of each override of rows,
// before the purge. After the purge, the calendar of a legacy row can hold
// no row of its UID, and the series rule then finds no master. A series with
// no live master keeps its logs: the restore of the series reads them.
func purgedOccurrencesTx(ctx context.Context, qtx *storage.Queries, rows []storage.ListEventRowStatesByIDsRow) ([]purgedOccurrence, error) {
	type key struct {
		calendarID int64
		uid, recID string
	}
	type series struct {
		calendarID int64
		uid        string
	}
	// One lookup for each series of the rows: the rows of one calendar and
	// one UID share their master.
	masters := make(map[series]*storage.Event)
	done := make(map[key]bool)
	var out []purgedOccurrence
	for _, r := range rows {
		if r.RecurrenceID == "" {
			continue
		}
		sk := series{r.CalendarID, r.Uid}
		master, seen := masters[sk]
		if !seen {
			// The row is in the trash, so the lookup reads the rows of the
			// trash too. Only a live master settles.
			m, err := findSeriesMasterIncludingDeletedTx(ctx, qtx, r.CalendarID, r.Uid)
			switch {
			case errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrAmbiguousUID):
			case err != nil:
				return nil, fmt.Errorf("get master: %w", err)
			case m.DeletedAt == nil:
				master = &m
			}
			masters[sk] = master
		}
		if master == nil {
			continue
		}
		k := key{master.CalendarID, master.Uid, r.RecurrenceID}
		if done[k] {
			continue
		}
		done[k] = true
		out = append(out, purgedOccurrence{master: *master, recID: r.RecurrenceID})
	}
	return out, nil
}

// settlePurgedTx settles the occurrence log of each occurrence of list after
// the purge. The purge trigger of migration 057 removed the rows from the
// logs. A log that then hides nothing goes (softdelete.SettleOccurrence).
func settlePurgedTx(ctx context.Context, qtx *storage.Queries, list []purgedOccurrence) error {
	keep := func(l softdelete.OccurrenceLog) softdelete.OccurrenceLog { return l }
	for _, o := range list {
		// The settle of an earlier occurrence can write the EXDATE list of
		// the same master, so each settle reads the master again.
		master, err := qtx.GetEventIncludingDeleted(ctx, o.master.ID)
		if err != nil {
			return fmt.Errorf("get master: %w", err)
		}
		if err := settleOccurrenceTx(ctx, qtx, master, o.recID, keep); err != nil {
			return err
		}
	}
	return nil
}

// loggedRows is the record of the occurrence logs of one UID in one calendar.
type loggedRows struct {
	// ids holds the override rows that the delete logs name by ID.
	ids map[int64]bool
	// pruned holds the occurrences of the pruned markers. A marker is no
	// delete of the user, so it names no row.
	pruned map[string]bool
}

// loggedRowIDsTx reads the occurrence logs of uid in calendarID once.
func loggedRowIDsTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) (loggedRows, error) {
	logs, err := qtx.ListEventExdateDeletesByCalendarUID(ctx, storage.ListEventExdateDeletesByCalendarUIDParams{CalendarID: calendarID, Uid: uid})
	if err != nil {
		return loggedRows{}, fmt.Errorf("list exdate logs: %w", err)
	}
	out := loggedRows{ids: make(map[int64]bool), pruned: make(map[string]bool)}
	for _, l := range logs {
		if l.Pruned == 1 {
			out.pruned[l.RecurrenceID] = true
			continue
		}
		log, err := parseOccurrenceLog(l)
		if err != nil {
			return loggedRows{}, err
		}
		for _, id := range log.AllRows() {
			out.ids[id] = true
		}
	}
	return out, nil
}

// deleteIdentity names one delete for its undo. The zero value names a
// delete that wrote no log: a delete by ID beside a trashed master.
type deleteIdentity struct {
	// exdateLogID and seq name the entry of an occurrence log or an orphan
	// log.
	exdateLogID, seq int64
	// seriesLogID names a series-delete log.
	seriesLogID int64
	// token is the delete token that the delete wrote (UndoMeta.DeleteToken),
	// or 0 when the caller has none.
	token int64
}

// logless reports whether the delete of id wrote no log.
func (id deleteIdentity) logless() bool {
	return id.exdateLogID == 0 && id.seriesLogID == 0
}

// owner names the record that holds a row in the trash for the undo.
type owner int

const (
	ownedBySelf  owner = iota + 1 // the identity of the delete names the row
	ownedByOther                  // a newer entry or log names the row
)

// identityRefusalTx decides the undo of the delete of id, whose rows are
// ids. It reads the delete logs of uid once. A newer record wins over the
// identity: a pruned marker is no log. For each row:
//   - No row, or a live row: the row is done. A purge took it, or a restore
//     came first.
//   - A row in the trash that a newer entry or log names: the undo refuses
//     with ErrDeletedAgain.
//   - A row in the trash that id names: the row comes back.
//   - A row in the trash that no log names: a trigger dropped the log, or a
//     pull hid the row with no log. The delete token of the row tells the
//     two apart. A trigger leaves the row as the delete left it, so a token
//     equal to the token of the delete brings the row back. A later delete,
//     a pull or a prune included, wrote a newer token. The undo then refuses
//     with ErrChangedSince, and the trash still restores the row. With no
//     token, a delete that wrote no log takes such a row.
//
// The undo is all or nothing: one refusal refuses the whole undo. It returns
// the states of the rows to bring back. With none, it gives
// ErrAlreadyRestored when a row is live, else ErrNotDeleted.
func identityRefusalTx(ctx context.Context, qtx *storage.Queries, uid string, ids []int64, id deleteIdentity) ([]storage.ListEventRowStatesByIDsRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	owners := make(map[int64]owner)
	set := func(rows []int64, o owner) {
		for _, r := range rows {
			if owners[r] != ownedByOther {
				owners[r] = o
			}
		}
	}
	ownerOf := func(self bool) owner {
		if self {
			return ownedBySelf
		}
		return ownedByOther
	}
	occurrences, err := qtx.ListEventExdateDeletesByUID(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("list exdate logs: %w", err)
	}
	for _, l := range occurrences {
		log, err := parseOccurrenceLog(l)
		if err != nil {
			return nil, err
		}
		for _, e := range log.Entries {
			set(e.Rows, ownerOf(id.exdateLogID != 0 && l.ID == id.exdateLogID && e.Seq == id.seq))
		}
	}
	series, err := qtx.ListEventSeriesDeletesByUID(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("list series delete logs: %w", err)
	}
	for _, l := range series {
		rows, err := parseRowIDs(l.HiddenIds)
		if err != nil {
			return nil, err
		}
		set(rows, ownerOf(id.seriesLogID != 0 && l.ID == id.seriesLogID))
	}
	truncs, err := qtx.ListEventTruncateDeletesByUID(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("list truncation logs: %w", err)
	}
	for _, l := range truncs {
		rows, err := parseRowIDs(storage.NullableToString(l.HiddenIds))
		if err != nil {
			return nil, err
		}
		set(rows, ownedByOther)
	}
	states, err := rowStatesTx(ctx, qtx, ids)
	if err != nil {
		return nil, err
	}
	var restore []storage.ListEventRowStatesByIDsRow
	live := false
	for _, st := range states {
		if st.DeletedAt == nil {
			live = true
			continue
		}
		switch owners[st.ID] {
		case ownedByOther:
			return nil, ErrDeletedAgain
		case ownedBySelf:
			// The log of the delete names the row. The token rule still
			// holds: the row must carry the token of the delete.
			if !tokenMatches(st, id.token) {
				return nil, ErrChangedSince
			}
			restore = append(restore, st)
		default:
			sameDelete := id.logless()
			if id.token != 0 {
				sameDelete = tokenMatches(st, id.token)
			}
			if !sameDelete {
				return nil, ErrChangedSince
			}
			restore = append(restore, st)
		}
	}
	if len(restore) == 0 {
		if live {
			return nil, ErrAlreadyRestored
		}
		return nil, ErrNotDeleted
	}
	return restore, nil
}

// tokenSplit holds the rows of a set in the trash, sorted by the token rule
// of an undo.
type tokenSplit struct {
	// match holds the rows with the delete token of the undo.
	match []storage.ListEventRowStatesByIDsRow
	// other holds the rows with another token: a later delete hid them, a
	// pull included.
	other []storage.ListEventRowStatesByIDsRow
}

// tokenSplitTx reads the rows of ids in the transaction of the restore, and
// sorts the rows in the trash by the token rule. A live or a purged row is
// done, and it is in neither list. token 0 means that the caller has no
// token, and every row in the trash is a match.
func tokenSplitTx(ctx context.Context, qtx *storage.Queries, ids []int64, token int64) (tokenSplit, error) {
	states, err := rowStatesTx(ctx, qtx, ids)
	if err != nil {
		return tokenSplit{}, err
	}
	var split tokenSplit
	for _, st := range states {
		switch {
		case st.DeletedAt == nil:
		case tokenMatches(st, token):
			split.match = append(split.match, st)
		default:
			split.other = append(split.other, st)
		}
	}
	return split, nil
}

// refuseOther is the filter of an undo that brings back every row of its
// set: a row of a later delete refuses with ErrChangedSince. It returns the
// rows to bring back.
func (s tokenSplit) refuseOther() ([]storage.ListEventRowStatesByIDsRow, error) {
	if len(s.other) > 0 {
		return nil, ErrChangedSince
	}
	return s.match, nil
}

// matchIDs is the filter of an undo that takes its own rows of a set. It
// returns the IDs of the matches and leaves the rows of other deletes.
func (s tokenSplit) matchIDs() []int64 {
	ids := make([]int64, 0, len(s.match))
	for _, st := range s.match {
		ids = append(ids, st.ID)
	}
	return ids
}

// tokenMatches reports whether the row st in the trash carries the delete
// token of the undo. token 0 matches each row.
func tokenMatches(st storage.ListEventRowStatesByIDsRow, token int64) bool {
	return token == 0 || st.DeleteToken == token
}

// restoreEventsByIDsTx un-hides the rows of ids in chunks under the parameter
// limit of SQLite. It returns the count of rows that came back.
func restoreEventsByIDsTx(ctx context.Context, qtx *storage.Queries, ids []int64) (int64, error) {
	var total int64
	for _, chunk := range storage.ChunkSlice(ids, idChunk) {
		n, err := qtx.RestoreEventsByIDs(ctx, chunk)
		if err != nil {
			return 0, fmt.Errorf("restore rows: %w", err)
		}
		total += n
	}
	return total, nil
}

// softDeleteEventsByIDsTx hides the rows of ids in chunks under the parameter
// limit of SQLite. Every chunk writes the one delete token of the statement
// of the caller.
func softDeleteEventsByIDsTx(ctx context.Context, qtx *storage.Queries, ids []int64, token int64) error {
	for _, chunk := range storage.ChunkSlice(ids, idChunk) {
		if err := qtx.SoftDeleteEventsByIDs(ctx, storage.SoftDeleteEventsByIDsParams{Ids: chunk, DeleteToken: token}); err != nil {
			return err
		}
	}
	return nil
}
