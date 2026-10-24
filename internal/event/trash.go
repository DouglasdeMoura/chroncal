package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/softdelete"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TrashKind discriminates the three shapes a trash row can have:
//
//   - a full soft-deleted events row
//   - a recurring-instance delete captured in the event_exdate_deletes log
//     (no row was deleted; an EXDATE was added)
//   - an RRULE truncation captured in event_truncate_deletes. The master's
//     recurrence was shortened. Any overrides past the cutoff were
//     soft-deleted.
type TrashKind int

const (
	// TrashKindEvent is a soft-deleted events row. Restore clears deleted_at.
	TrashKindEvent TrashKind = iota
	// TrashKindInstance is an EXDATE-based instance delete. Restore removes
	// the EXDATE from the master and deletes the log row.
	TrashKindInstance
	// TrashKindTruncation is an RRULE truncation ("This and following").
	// Restore rewrites the master's RRULE back to its pre-truncation value
	// AND un-hides any overrides soft-deleted by the truncation.
	TrashKindTruncation
)

// TrashEntry is a unified row for the "Recently deleted" view. For
// TrashKindEvent, ID is the events row ID. For TrashKindInstance, ID is
// the event_exdate_deletes row ID. For TrashKindTruncation, ID is the
// event_truncate_deletes row ID and CutoffTime/PreviousRRule have values.
//
// Display-ready event fields (StartTime/EndTime/AllDay/Location/Description/
// Status/Categories) have values so the trash dialog can show full
// details inline. A second view is not needed. For TrashKindInstance and
// TrashKindTruncation these come from the master (the series shape).
// StartTime/EndTime shift to the instance or cutoff window when relevant.
type TrashEntry struct {
	Kind          TrashKind
	ID            int64
	CalendarID    int64
	UID           string
	Title         string
	InstanceTime  time.Time // populated for TrashKindInstance
	CutoffTime    time.Time // populated for TrashKindTruncation
	PreviousRRule string    // populated for TrashKindTruncation
	DeletedAt     time.Time
	// NewerTruncationID names the newest truncation of the same series that
	// is newer than this one (TrashKindTruncation). The restore of this entry
	// refuses with ErrNewerTruncation until the user restores that entry. It
	// is 0 when this entry restores. NewerTruncationCutoff is the cutoff of
	// that entry.
	NewerTruncationID     int64
	NewerTruncationCutoff time.Time

	StartTime   time.Time
	EndTime     time.Time
	AllDay      bool
	Location    string
	Description string
	Status      string
	Categories  string
	// KeptOverrideIDs lists the live override rows that the series of a
	// master in the trash still owns (TrashKindEvent). The purge keeps such
	// a master until the rows go. See KeptNote.
	KeptOverrideIDs []int64
}

// KeptNote tells the user why the purge keeps a master in the trash, and
// names the live override rows that keep it. With no row, it is empty.
func KeptNote(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	return "kept: the series still has live overrides, rows " + strings.ReplaceAll(formatRowIDs(ids), ",", ", ")
}

// ListTrash merges soft-deleted events, EXDATE-based instance deletes,
// and RRULE truncation deletes for a calendar, newest first. The caller
// is responsible for visibility filters (hidden calendars, and others).
func (s *Service) ListTrash(ctx context.Context, calendarID int64) ([]TrashEntry, error) {
	evts, err := s.q.ListDeletedEventsByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list deleted events: %w", err)
	}
	instLogs, err := s.q.ListEventExdateDeletesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list exdate deletes: %w", err)
	}
	truncLogs, err := s.q.ListEventTruncateDeletesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list truncate deletes: %w", err)
	}

	deletedEvents := make([]Event, len(evts))
	for i, r := range evts {
		deletedEvents[i] = FromStorage(r)
	}
	s.populateCategories(ctx, deletedEvents)
	kept, err := s.keptOverrideIDs(ctx, deletedEvents)
	if err != nil {
		return nil, err
	}

	entries := make([]TrashEntry, 0, len(evts)+len(instLogs)+len(truncLogs))
	for _, ev := range deletedEvents {
		deletedAt := time.Time{}
		if ev.DeletedAt != nil {
			deletedAt = *ev.DeletedAt
		}
		entries = append(entries, TrashEntry{
			Kind:        TrashKindEvent,
			ID:          ev.ID,
			CalendarID:  ev.CalendarID,
			UID:         ev.UID,
			Title:       ev.Title,
			DeletedAt:   deletedAt,
			StartTime:   ev.StartTime,
			EndTime:     ev.EndTime,
			AllDay:      ev.AllDay,
			Location:    ev.Location,
			Description: ev.Description,
			Status:      ev.Status,
			Categories:  ev.Categories,

			KeptOverrideIDs: kept[ev.ID],
		})
	}

	// For log rows, look up the master (soft-deleted included) once per
	// UID so the trash dialog can show full context: title, location,
	// description, status. The user does not need a second view.
	// Fetch each distinct master once, then batch category population into a
	// single query rather than one per master. A log row names the
	// calendar of its master: two calendars can hold the same UID.
	type masterKey struct {
		calendarID int64
		uid        string
	}
	masters := make(map[masterKey]*Event, len(instLogs)+len(truncLogs))
	for _, l := range instLogs {
		masters[masterKey{l.CalendarID, l.Uid}] = nil
	}
	for _, l := range truncLogs {
		masters[masterKey{l.CalendarID, l.Uid}] = nil
	}
	masterList := make([]Event, 0, len(masters))
	for key := range masters {
		r, err := s.q.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{
			CalendarID: key.calendarID, Uid: key.uid,
		})
		if err == nil {
			masterList = append(masterList, FromStorage(r))
		}
	}
	s.populateCategories(ctx, masterList)
	for i := range masterList {
		masters[masterKey{masterList[i].CalendarID, masterList[i].UID}] = &masterList[i]
	}
	lookupMaster := func(calendarID int64, uid string) *Event { return masters[masterKey{calendarID, uid}] }

	for _, l := range instLogs {
		// An occurrence log with no master in its calendar is no entry. The log of
		// a delete by ID of an orphan row names a row that the trash lists already.
		// A stale log names nothing to restore.
		m := lookupMaster(l.CalendarID, l.Uid)
		if m == nil {
			continue
		}
		inst, _ := time.Parse(time.RFC3339, l.RecurrenceID)
		entry := TrashEntry{
			Kind:         TrashKindInstance,
			ID:           l.ID,
			CalendarID:   l.CalendarID,
			UID:          l.Uid,
			InstanceTime: inst,
			DeletedAt:    parseStorageTime(l.DeletedAt),
		}
		entry.Title = m.Title
		entry.AllDay = m.AllDay
		entry.Location = m.Location
		entry.Description = m.Description
		entry.Status = m.Status
		entry.Categories = m.Categories
		// Project the master's duration onto this occurrence so
		// "When" reads as "Fri, Apr 3 09:00 – 09:30" rather than
		// the master's original time.
		entry.StartTime = inst
		if !m.EndTime.IsZero() && !m.StartTime.IsZero() {
			entry.EndTime = inst.Add(m.EndTime.Sub(m.StartTime))
		}
		entries = append(entries, entry)
	}
	truncBySeries := make(map[masterKey][]storage.EventTruncateDelete, len(truncLogs))
	for _, l := range truncLogs {
		key := masterKey{l.CalendarID, l.Uid}
		truncBySeries[key] = append(truncBySeries[key], l)
	}
	for _, l := range truncLogs {
		cutoff, _ := time.Parse(time.RFC3339, l.CutoffTime)
		entry := TrashEntry{
			Kind:          TrashKindTruncation,
			ID:            l.ID,
			CalendarID:    l.CalendarID,
			UID:           l.Uid,
			CutoffTime:    cutoff,
			PreviousRRule: l.PreviousRrule,
			DeletedAt:     parseStorageTime(l.DeletedAt),
		}
		// The same rule as the restore: a newer truncation of the series
		// comes first. The entry names it, so the user can restore it first.
		if newer, found := newestNewerTruncation(truncBySeries[masterKey{l.CalendarID, l.Uid}], l); found {
			entry.NewerTruncationID = newer.ID
			entry.NewerTruncationCutoff, _ = time.Parse(time.RFC3339, newer.CutoffTime)
		}
		if m := lookupMaster(l.CalendarID, l.Uid); m != nil {
			entry.Title = m.Title
			entry.AllDay = m.AllDay
			entry.Location = m.Location
			entry.Description = m.Description
			entry.Status = m.Status
			entry.Categories = m.Categories
			entry.StartTime = m.StartTime
			entry.EndTime = m.EndTime
		}
		entries = append(entries, entry)
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].DeletedAt.After(entries[j].DeletedAt)
	})
	return entries, nil
}

// RestoreTrash reverses the delete recorded in entry. Dispatches by kind.
// For TrashKindEvent it un-hides the row and bumps sequence. For
// TrashKindInstance it removes the EXDATE from the master and deletes the
// log row. For TrashKindTruncation it rewrites the master's RRULE back
// and un-hides any overrides that were soft-deleted by the truncation.
func (s *Service) RestoreTrash(ctx context.Context, entry TrashEntry) error {
	switch entry.Kind {
	case TrashKindEvent:
		return s.RestoreByID(ctx, entry.ID)
	case TrashKindInstance:
		return s.restoreInstanceByLogID(ctx, entry.ID)
	case TrashKindTruncation:
		return s.restoreTruncationByLogID(ctx, entry.ID, 0)
	default:
		return fmt.Errorf("unknown trash kind %d", entry.Kind)
	}
}

// oldLogRow is the shape of the rows of ListOldEventExdateDeletes,
// ListOldEventTruncateDeletes, and ListOldEventSeriesDeletes.
type oldLogRow = struct {
	ID         int64
	CalendarID int64
	Uid        string
}

// purgeOldLogsTx drops the log rows that list gives for cutoff, except the
// logs of the series of keep. The purge of the same run kept those masters
// (PurgeReport.Skipped). del drops the rows by ID, in chunks. It returns the
// count of purged rows. A purge of an exdate log or a truncate log keeps
// the EXDATE or the RRULE on the master. The user intended those
// occurrences to be gone. A restore of a series with no log brings the
// master back alone.
func purgeOldLogsTx[R ~oldLogRow](ctx context.Context, cutoff string, keep []SkippedMaster,
	list func(context.Context, string) ([]R, error), del func(context.Context, []int64) (int64, error),
) (int, error) {
	rows, err := list(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	kept := keptSeries(keep)
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		if row := oldLogRow(r); !kept[seriesKeyOf(row.CalendarID, row.Uid)] {
			ids = append(ids, row.ID)
		}
	}
	var total int64
	for _, chunk := range storage.ChunkSlice(ids, idChunk) {
		n, err := del(ctx, chunk)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return int(total), nil
}

// PurgeTrashEntry hard-removes entry from the trash. For TrashKindEvent it
// drops the events row. For TrashKindInstance it drops the log row and the
// override rows that the log names. For TrashKindTruncation it drops the
// log row only. The EXDATE (or truncated RRULE plus deleted overrides) stays
// on the master. That matches the "deleted forever" semantic the user
// expressed at delete time.
func (s *Service) PurgeTrashEntry(ctx context.Context, entry TrashEntry) error {
	switch entry.Kind {
	case TrashKindEvent:
		return s.PurgeByID(ctx, entry.ID)
	case TrashKindInstance:
		return s.purgeInstanceByLogID(ctx, entry.ID)
	case TrashKindTruncation:
		return s.q.DeleteEventTruncateDelete(ctx, entry.ID)
	default:
		return fmt.Errorf("unknown trash kind %d", entry.Kind)
	}
}

// keptOverrideIDs returns, for each master in evts, the live override rows
// that its series still owns, by the ID of the master. The purge keeps such a
// master (see purgeDeletedTx). One read of the view covers every master.
func (s *Service) keptOverrideIDs(ctx context.Context, evts []Event) (map[int64][]int64, error) {
	var masters []seriesRef
	for _, ev := range evts {
		if !ev.IsRecurrenceOverride() {
			masters = append(masters, seriesRef{ID: ev.ID, CalendarID: ev.CalendarID, UID: ev.UID})
		}
	}
	if len(masters) == 0 {
		return nil, nil
	}
	overrides, err := seriesOverridesTx(ctx, s.q, masters)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]int64)
	for _, m := range masters {
		if live := overrides.liveIDs(m); len(live) > 0 {
			out[m.ID] = live
		}
	}
	return out, nil
}

// restoreInstanceByLogID removes the EXDATE for (uid, recurrence_id) from the
// master and deletes the log row. It un-hides the override rows that the
// occurrence deletes hid (the hidden_ids of the log). The write guard covers
// each calendar of the series and the calendar of each row. A master in the
// trash gives ErrMasterInTrash and writes nothing. Live rows beside a
// trashed master would block its purge, and the series restore brings back
// the rows. The restore marks the master dirty, so the next push sends the
// EXDATE removal.
func (s *Service) restoreInstanceByLogID(ctx context.Context, logID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	log, err := qtx.GetEventExdateDelete(ctx, logID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotDeleted
		}
		return fmt.Errorf("get exdate log: %w", err)
	}
	// A pruned marker is no delete of the user, so the trash has no entry
	// of it.
	if log.Pruned == 1 {
		return ErrNotDeleted
	}

	// Read the master inside the transaction so the EXDATE list we rewrite
	// reflects a concurrent writer's changes rather than a pre-transaction
	// snapshot (issue #116). The log row names the calendar of the
	// master: two calendars can hold the same UID (issue #842).
	master, err := qtx.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{
		CalendarID: log.CalendarID, Uid: log.Uid,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("get master: %w", ErrNotDeleted)
		}
		return fmt.Errorf("get master: %w", err)
	}
	if master.DeletedAt != nil {
		return ErrMasterInTrash
	}
	if err := ensureSeriesWritableIn(ctx, qtx, log.CalendarID, log.Uid); err != nil {
		return err
	}
	olog, err := parseOccurrenceLog(log)
	if err != nil {
		return err
	}
	hidden := olog.AllRows()
	// A row that a purge or an earlier restore took is no error: the
	// EXDATE still goes.
	if _, err := restoreHiddenRowsTx(ctx, qtx, log.CalendarID, hidden); err != nil {
		return err
	}
	// Every entry goes, so the log gives up its EXDATE and goes too
	// (softdelete.SettleOccurrence).
	if err := settleOccurrenceTx(ctx, qtx, master, log.RecurrenceID, func(l softdelete.OccurrenceLog) softdelete.OccurrenceLog {
		l.Entries = nil
		return l
	}); err != nil {
		return err
	}
	if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, log.Uid, "event"); err != nil {
		return fmt.Errorf("mark resource dirty: %w", err)
	}
	return tx.Commit()
}

// restoreTruncationByLogID is restoreTruncation for the log row logID. The
// trash view names the row by ID.
func (s *Service) restoreTruncationByLogID(ctx context.Context, logID, token int64) error {
	return s.restoreTruncation(ctx, truncationKey{logID: logID, token: token})
}

// truncationKey names one truncation log row. The trash view names the row by
// ID (logID). The undo names it by its series, its cutoff, and its delete
// token: each truncation has its own row. token is the delete token of an
// undo, and 0 for a restore from the trash. masterUpdatedBefore is the
// updated_at of the master after the truncation, for the stale-master guard
// of the undo. The zero time skips the guard.
type truncationKey struct {
	logID               int64
	calendarID          int64
	uid                 string
	cutoff              string
	token               int64
	masterUpdatedBefore time.Time
}

// readTx reads the log row of k in the transaction of the restore. The undo
// finds the master of its series first (findSeriesMasterIncludingDeletedTx),
// because the log row lives in the calendar of the master.
func (k truncationKey) readTx(ctx context.Context, qtx *storage.Queries) (storage.EventTruncateDelete, error) {
	if k.logID != 0 {
		return qtx.GetEventTruncateDelete(ctx, k.logID)
	}
	master, err := findSeriesMasterIncludingDeletedTx(ctx, qtx, k.calendarID, k.uid)
	if err != nil {
		return storage.EventTruncateDelete{}, wrapMasterLookup(err)
	}
	return qtx.GetEventTruncateDeleteByCalendarUIDCutoffToken(ctx, storage.GetEventTruncateDeleteByCalendarUIDCutoffTokenParams{
		CalendarID: master.CalendarID, Uid: k.uid, CutoffTime: k.cutoff, DeleteToken: k.token,
	})
}

// newerTruncationTx returns the newest truncation log row of the series of log
// that is newer than log, at any cutoff (newestNewerTruncation).
func newerTruncationTx(ctx context.Context, qtx *storage.Queries, log storage.EventTruncateDelete) (storage.EventTruncateDelete, bool, error) {
	logs, err := qtx.ListEventTruncateDeletesByCalendarUID(ctx, storage.ListEventTruncateDeletesByCalendarUIDParams{
		CalendarID: log.CalendarID, Uid: log.Uid,
	})
	if err != nil {
		return storage.EventTruncateDelete{}, false, fmt.Errorf("list truncate logs: %w", err)
	}
	best, found := newestNewerTruncation(logs, log)
	return best, found, nil
}

// newestNewerTruncation returns the newest row of logs that is newer than
// log. logs holds the truncation log rows of the series of log. A higher
// delete token is newer. Two rows from before the token (token 0) compare by
// deleted_at, then by ID. The restore of a truncation refuses while such a
// row exists, and the trash list names that row (TrashEntry.NewerTruncationID).
func newestNewerTruncation(logs []storage.EventTruncateDelete, log storage.EventTruncateDelete) (storage.EventTruncateDelete, bool) {
	newer := func(a, b storage.EventTruncateDelete) bool {
		if a.DeleteToken != b.DeleteToken {
			return a.DeleteToken > b.DeleteToken
		}
		if a.DeletedAt != b.DeletedAt {
			return a.DeletedAt > b.DeletedAt
		}
		return a.ID > b.ID
	}
	var best storage.EventTruncateDelete
	found := false
	for _, l := range logs {
		if l.ID != log.ID && newer(l, log) && (!found || newer(l, best)) {
			best, found = l, true
		}
	}
	return best, found
}

// restoreTruncation rewrites the master's RRULE back to the rule before the
// truncation. It re-adds the RDATEs the truncation trimmed. It un-hides the
// overrides that the truncation hid. It then drops the log row. It marks the
// master dirty, so the next push sends the restored series. The log row
// is read in the transaction, so a concurrent restore cannot consume it
// between the read and the writes.
//
// Each truncation has its own log row. A newer truncation of the series, at
// any cutoff, recorded the rule that this truncation wrote. The restore then
// refuses with a *NewerTruncationError, so the user restores the newer one
// first.
func (s *Service) restoreTruncation(ctx context.Context, key truncationKey) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	log, err := key.readTx(ctx, qtx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotDeleted
		}
		return fmt.Errorf("get truncate log: %w", err)
	}
	logID, token := log.ID, key.token
	// Only the newest truncation of the series restores cleanly. A newer one
	// recorded the rule that this one wrote, so its restore would truncate
	// the series again.
	if newer, found, err := newerTruncationTx(ctx, qtx, log); err != nil {
		return err
	} else if found {
		cutoff, _ := time.Parse(time.RFC3339, newer.CutoffTime)
		// The master gives the all-day flag of the series. A failed read
		// leaves the flag false: the refusal stays, and only the form of
		// the cutoff in the message changes.
		master, err := qtx.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{
			CalendarID: log.CalendarID, Uid: log.Uid,
		})
		return &NewerTruncationError{Cutoff: cutoff, AllDay: err == nil && master.AllDay != 0}
	}
	// The restore writes the master in the calendar of the log. A log
	// with row IDs guards the calendars of its rows in restoreLoggedIDsTx.
	// An older log restores by recurrence ID through the series, so the
	// guard covers the calendars of the series.
	if log.HiddenIds != nil {
		if err := calendaraccess.EnsureWritable(ctx, qtx, log.CalendarID, "VEVENT"); err != nil {
			return err
		}
	} else if err := ensureSeriesWritableIn(ctx, qtx, log.CalendarID, log.Uid); err != nil {
		return err
	}

	// Read the master inside the transaction. Then a concurrent purge or
	// soft-delete between the read and the in-tx UPDATE cannot make
	// UpdateEventRecurrenceRule match 0 rows while we still consume the log
	// row. That would drop the RRULE restore in silence (issue #413, mirrors
	// restoreInstanceByLogID / #116). The log row names the calendar of
	// the master: two calendars can hold the same UID (issue #842).
	master, err := qtx.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{
		CalendarID: log.CalendarID, Uid: log.Uid,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("get master: %w", ErrNotDeleted)
		}
		return fmt.Errorf("get master: %w", err)
	}
	if master.DeletedAt != nil {
		// The truncation rewrites the rule of a live master. The entry
		// stays, and the restore of the series comes first.
		return ErrMasterInTrash
	}
	// The stale-master guard of the undo: an edit of the master after the
	// truncation refuses the undo, so the undo does not overwrite the edit.
	// The check reads the master in this transaction. The baseline is the
	// updated_at that the truncation read back from the row, so the check
	// allows no drift. An edit in the same second as the truncation keeps
	// the same updated_at, and the guard cannot see it.
	if !key.masterUpdatedBefore.IsZero() {
		if updated := parseStorageTime(master.UpdatedAt); updated.After(key.masterUpdatedBefore) {
			return fmt.Errorf("%w (expected updated_at <= %s, got %s)", ErrMasterAdvanced,
				key.masterUpdatedBefore.Format(time.RFC3339), updated.Format(time.RFC3339))
		}
	}
	// The undo of a truncation (token non-zero) refuses when a row of the
	// log went to the trash again in a later delete (tokenSplitTx). The
	// check comes before any write.
	if token != 0 && log.HiddenIds != nil {
		ids, err := parseRowIDs(*log.HiddenIds)
		if err != nil {
			return err
		}
		split, err := tokenSplitTx(ctx, qtx, ids, token)
		if err != nil {
			return err
		}
		if _, err := split.refuseOther(); err != nil {
			return err
		}
	}

	if err := qtx.UpdateEventRecurrenceRule(ctx, storage.UpdateEventRecurrenceRuleParams{
		RecurrenceRule: storage.StringToNullable(log.PreviousRrule),
		ID:             master.ID,
	}); err != nil {
		return fmt.Errorf("restore rrule: %w", err)
	}
	if err := restoreTruncatedRDates(ctx, qtx, master, log); err != nil {
		return fmt.Errorf("restore rdates: %w", err)
	}
	// The restore consumes the log row first. The drop of the restored rows
	// from the other logs then does not rewrite this row.
	if err := qtx.DeleteEventTruncateDelete(ctx, logID); err != nil {
		return fmt.Errorf("delete log row: %w", err)
	}
	if err := restoreTruncatedOverrides(ctx, qtx, log); err != nil {
		return fmt.Errorf("restore overrides: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, log.Uid, "event"); err != nil {
		return fmt.Errorf("mark resource dirty: %w", err)
	}
	return tx.Commit()
}

// restoreTruncatedRDates re-adds to the master the RDATEs a truncation trimmed.
// removed_rdates is non-NULL only for #463-era log rows. Pre-#463 rows store
// NULL (the truncation left RDATEs untouched). An empty string means the
// truncation dropped none. Both no-op. The kept RDATEs (already on the
// master after a concurrent edit) are merged with the recorded removed ones.
func restoreTruncatedRDates(ctx context.Context, qtx *storage.Queries, master storage.Event, log storage.EventTruncateDelete) error {
	if log.RemovedRdates == nil || *log.RemovedRdates == "" {
		return nil
	}
	merged := append(ParseTimeList(storage.NullableToString(master.Rdates)), ParseTimeList(*log.RemovedRdates)...)
	return qtx.UpdateEventRdates(ctx, storage.UpdateEventRdatesParams{
		Rdates: storage.StringToNullable(SerializeTimeList(merged)),
		ID:     master.ID,
	})
}

// restoreTruncatedOverrides un-hides the overrides a truncation soft-deleted.
// When the log recorded provenance (hidden_overrides is non-NULL), it restores
// only those specific overrides. An override the user deleted on its own
// before the truncation then stays deleted (issue #287). Pre-#287 log rows
// store NULL provenance. For those it falls back to un-hiding every
// soft-deleted override at or after the cutoff. The overrides are the
// rows of the series in the calendar of the log row (issue #842).
func restoreTruncatedOverrides(ctx context.Context, qtx *storage.Queries, log storage.EventTruncateDelete) error {
	if log.HiddenIds != nil {
		// A log row of migration 057 and later names the hidden rows by ID. The
		// restore then does not depend on the series owner of today. The calendar
		// of each row takes the write.
		ids, err := parseRowIDs(*log.HiddenIds)
		if err != nil {
			return err
		}
		_, err = restoreLoggedIDsTx(ctx, qtx, log.CalendarID, ids)
		return err
	}
	if log.HiddenOverrides == nil {
		return qtx.RestoreSeriesOverridesAtOrAfter(ctx, storage.RestoreSeriesOverridesAtOrAfterParams{
			CalendarID:   log.CalendarID,
			Uid:          log.Uid,
			RecurrenceID: log.CutoffTime,
		})
	}
	if *log.HiddenOverrides == "" {
		return nil
	}
	recIDs, err := parseCommaList(*log.HiddenOverrides, parseRecurrenceID)
	if err != nil {
		return err
	}
	for _, recID := range recIDs {
		if _, err := qtx.RestoreSeriesOverrideByRecurrenceID(ctx, storage.RestoreSeriesOverrideByRecurrenceIDParams{
			CalendarID:   log.CalendarID,
			Uid:          log.Uid,
			RecurrenceID: recID,
		}); err != nil {
			return err
		}
	}
	return nil
}

// keptSeries returns the set of the series of keep, by seriesKeyOf.
func keptSeries(keep []SkippedMaster) map[string]bool {
	set := make(map[string]bool, len(keep))
	for _, m := range keep {
		set[seriesKeyOf(m.CalendarID, m.UID)] = true
	}
	return set
}

// seriesKeyOf names the series of uid in calendarID.
func seriesKeyOf(calendarID int64, uid string) string {
	return strconv.FormatInt(calendarID, 10) + "\x00" + uid
}

// purgeInstanceByLogID hard-removes the occurrence delete of the log logID
// from the trash. The rows that the log names go with the log, and the
// EXDATE stays: the delete stands for good. A pruned marker gives
// ErrNotDeleted.
func (s *Service) purgeInstanceByLogID(ctx context.Context, logID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	log, err := qtx.GetEventExdateDelete(ctx, logID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotDeleted
		}
		return fmt.Errorf("get exdate log: %w", err)
	}
	if log.Pruned == 1 {
		return ErrNotDeleted
	}
	parsed, err := parseOccurrenceLog(log)
	if err != nil {
		return err
	}
	rows := parsed.AllRows()
	// A purge of the local trash makes no server write, so it needs no
	// write guard.
	// The log goes first, so the settle of the purge finds no log.
	if err := qtx.DeleteEventExdateDelete(ctx, logID); err != nil {
		return fmt.Errorf("delete exdate log: %w", err)
	}
	if _, err := purgeIDsTx(ctx, qtx, rows); err != nil {
		return err
	}
	return tx.Commit()
}
