package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/uidmove"
)

// findSeriesMasterTx finds the live master of the series of uid for an edit
// of the series. Such an edit is a truncation, an occurrence delete, a series
// delete, or an export. Two calendars can hold the same UID (issue #842), so
// calendarID names the calendar of the series.
//
// A calendar with a live master of the UID gives that master. A calendar
// with a master of the UID in the trash gives sql.ErrNoRows: the series is
// not live there. A calendar with no master row of the UID holds a legacy
// override at most (see storage.LegacyOverride). The one live master of
// the UID owns such a row (uidmove.LegacyOwner). One live master is then
// the choice, and more give ErrAmbiguousUID. A calendar with no
// live row of the UID gives sql.ErrNoRows. calendarID 0 names no
// calendar: one live master is then the only choice, none gives
// sql.ErrNoRows, and more give ErrAmbiguousUID. The view
// event_series_rows answers the rule for a calendar in one query.
func findSeriesMasterTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) (storage.Event, error) {
	return findSeriesMaster(ctx, qtx, calendarID, uid, false)
}

// findSeriesMasterIncludingDeletedTx is findSeriesMasterTx for a restore. The
// master can be in the trash, so a master row in the trash counts as the
// master of its calendar. With calendarID 0, or for a legacy override, the
// owner rule (uidmove.LegacyOwner) applies in full. One live master is the
// choice. With no live master, one master in the trash is the choice. A copy
// of the UID in the trash of another calendar, for example an old copy that
// the purge did not drop yet, then never blocks a restore.
func findSeriesMasterIncludingDeletedTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) (storage.Event, error) {
	return findSeriesMaster(ctx, qtx, calendarID, uid, true)
}

func findSeriesMaster(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string, includeDeleted bool) (storage.Event, error) {
	if calendarID == 0 {
		return seriesMasterOfUID(ctx, qtx, uid, includeDeleted)
	}
	// The view event_series_rows answers the rule for a calendar in one
	// query: the master whose series owns the rows of the calendar.
	master, err := qtx.GetSeriesMasterOfCalendar(ctx, storage.GetSeriesMasterOfCalendarParams{
		Uid: uid, CalendarID: calendarID, IncludeDeleted: includeDeleted,
	})
	if !errors.Is(err, sql.ErrNoRows) {
		return master, err
	}
	// No series owns the rows of the calendar. When the UID has more than one
	// live master and the calendar holds a row of the UID, the rule cannot
	// choose. The caller can then name the series.
	rows, err := qtx.ListEventMastersByUIDIncludingDeleted(ctx, uid)
	if err != nil {
		return storage.Event{}, err
	}
	// A calendar with its own master row owns its rows, also when that
	// master is in the trash. The view found no live owner, so the series
	// of the calendar is not live. The masters of other calendars make no
	// ambiguity here.
	for _, m := range rows {
		if m.CalendarID == calendarID {
			return storage.Event{}, sql.ErrNoRows
		}
	}
	masters := uidMasters(rows)
	if !includeDeleted {
		// A live edit sees the live masters only: two masters in the
		// trash make no ambiguity for a live legacy override.
		masters = liveMasters(masters)
	}
	if _, _, ambiguous := uidmove.LegacyOwner(masters); !ambiguous {
		return storage.Event{}, sql.ErrNoRows
	}
	// A live edit counts the live rows of the calendar only. A calendar
	// whose rows of the UID are all in the trash holds no live series. It
	// gets sql.ErrNoRows, not the ambiguity.
	n, err := qtx.CountEventUIDRowsInCalendar(ctx, storage.CountEventUIDRowsInCalendarParams{
		CalendarID: calendarID, Uid: uid, IncludeDeleted: includeDeleted,
	})
	if err != nil {
		return storage.Event{}, err
	}
	held := n.Masters + n.Overrides
	if held == 0 {
		return storage.Event{}, sql.ErrNoRows
	}
	return storage.Event{}, ErrAmbiguousUID
}

// seriesMasterOfUID is findSeriesMaster for calendar 0. No calendar anchors
// the lookup, so the owner rule (uidmove.LegacyOwner) picks the master among
// every master row of the UID.
func seriesMasterOfUID(ctx context.Context, qtx *storage.Queries, uid string, includeDeleted bool) (storage.Event, error) {
	rows, err := qtx.ListEventMastersByUIDIncludingDeleted(ctx, uid)
	if err != nil {
		return storage.Event{}, err
	}
	masters := uidMasters(rows)
	if !includeDeleted {
		// A live lookup wants a live master. With none, the UID is not
		// found, also when two calendars hold it in the trash.
		masters = liveMasters(masters)
	}
	owner, ok, ambiguous := uidmove.LegacyOwner(masters)
	if ambiguous {
		return storage.Event{}, ErrAmbiguousUID
	}
	if !ok {
		return storage.Event{}, sql.ErrNoRows
	}
	for _, m := range rows {
		if m.CalendarID == owner {
			return m, nil
		}
	}
	return storage.Event{}, sql.ErrNoRows
}

// liveMasters returns the live masters of masters.
func liveMasters(masters []uidmove.Master) []uidmove.Master {
	out := make([]uidmove.Master, 0, len(masters))
	for _, m := range masters {
		if !m.Trashed {
			out = append(out, m)
		}
	}
	return out
}

// wrapLookup keeps ErrAmbiguousUID and ErrAmbiguousOverride bare, so a
// caller can match them, and wraps every other lookup error with prefix.
func wrapLookup(prefix string, err error) error {
	if errors.Is(err, ErrAmbiguousUID) || errors.Is(err, ErrAmbiguousOverride) {
		return err
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

// wrapMasterLookup is wrapLookup for a master lookup.
func wrapMasterLookup(err error) error {
	return wrapLookup("get master", err)
}

// ensureSeriesWritableIn enforces the remote access and component policy on
// every calendar that holds a row of the series of uid in calendarID. These
// are that calendar and each calendar with a legacy override of the UID (see
// storage.LegacyOverride). The other calendars that hold the UID stay out. A
// read-only copy of the UID in calendar A then does not block a delete or a
// restore of the series in calendar B. calendarID 0 names no calendar: the
// guard then covers every calendar that holds a row of the UID, live or in
// the trash.
func ensureSeriesWritableIn(ctx context.Context, q *storage.Queries, calendarID int64, uid string) error {
	var ids []int64
	var err error
	if calendarID == 0 {
		ids, err = q.ListEventCalendarIDsByUID(ctx, uid)
	} else {
		ids, err = q.ListSeriesCalendarIDs(ctx, storage.ListSeriesCalendarIDsParams{CalendarID: calendarID, Uid: uid})
	}
	if err != nil {
		return fmt.Errorf("list series calendars: %w", err)
	}
	if len(ids) == 0 && calendarID != 0 {
		ids = []int64{calendarID}
	}
	for _, id := range ids {
		if err := calendaraccess.EnsureWritable(ctx, q, id, "VEVENT"); err != nil {
			return err
		}
	}
	return nil
}

// SeriesMaster returns the live master of the series of uid in calendarID.
// See findSeriesMasterTx for the rule. calendarID 0 picks the one live
// master of the UID, or it returns ErrAmbiguousUID.
func (s *Service) SeriesMaster(ctx context.Context, calendarID int64, uid string) (Event, error) {
	r, err := findSeriesMasterTx(ctx, s.q, calendarID, uid)
	if err != nil {
		return Event{}, err
	}
	e := FromStorage(r)
	s.populateSingleCategories(ctx, &e)
	return e, nil
}

// SeriesOverride returns the live override row of the occurrence recID of
// the series of uid in calendarID. A row in the calendar of the master
// wins. Else it takes a legacy override of the occurrence (see
// storage.LegacyOverride). The override rows of a master in another
// calendar stay out, and a row in the trash stays out. No row gives
// sql.ErrNoRows. See findSeriesMasterTx for the master rule.
func (s *Service) SeriesOverride(ctx context.Context, calendarID int64, uid, recID string) (Event, error) {
	master, err := findSeriesMasterTx(ctx, s.q, calendarID, uid)
	if err != nil {
		return Event{}, err
	}
	r, err := findInstanceOverrideTx(ctx, s.q, master, recID, false)
	if err != nil {
		return Event{}, err
	}
	e := FromStorage(r)
	s.populateSingleCategories(ctx, &e)
	return e, nil
}

// SeriesOccurrence finds the master of the series of uid in calendarID (see
// findSeriesMasterTx). In the same pass it finds the stored override of the
// occurrence recID in that series. override is nil with no stored override.
// The CLI resolves an occurrence edit with it.
func (s *Service) SeriesOccurrence(ctx context.Context, calendarID int64, uid, recID string) (Event, *Event, error) {
	return s.seriesOccurrence(ctx, calendarID, uid, recID, false)
}

// seriesOccurrence is SeriesOccurrence. strict names the rule for two
// legacy rows of the occurrence (see findInstanceOverrideTx).
func (s *Service) seriesOccurrence(ctx context.Context, calendarID int64, uid, recID string, strict bool) (Event, *Event, error) {
	m, err := findSeriesMasterTx(ctx, s.q, calendarID, uid)
	if err != nil {
		return Event{}, nil, err
	}
	master := FromStorage(m)
	r, err := findInstanceOverrideTx(ctx, s.q, m, recID, strict)
	if errors.Is(err, sql.ErrNoRows) {
		// The caller uses the master, so its categories load now only.
		s.populateSingleCategories(ctx, &master)
		return master, nil, nil
	}
	if err != nil {
		return Event{}, nil, err
	}
	override := FromStorage(r)
	s.populateSingleCategories(ctx, &override)
	return master, &override, nil
}

// IsSeriesRowOf reports whether the series of calendarID owns the row id:
// a row of that calendar, or a legacy override that the series owns.
func (s *Service) IsSeriesRowOf(ctx context.Context, calendarID, id int64) (bool, error) {
	return s.q.IsSeriesRowOfCalendar(ctx, storage.IsSeriesRowOfCalendarParams{ID: id, CalendarID: calendarID})
}

// ErrNotExportedHere reports a UID whose rows in a calendar belong to a
// series that the export of the calendar does not carry: a live series of
// another calendar, a series with its master in the trash, or no series.
var ErrNotExportedHere = errors.New("event: the calendar exports no series of the UID")

// ExportMaster returns the live master of uid that the export of
// calendarID carries, with its categories. It applies the owner rule of
// findSeriesMaster:
//   - The live master of the calendar is the result.
//   - A series of another calendar, a master in the trash, or rows that
//     belong to no series give ErrNotExportedHere. A PUT of the rows alone
//     would store a body with no master.
//   - A UID with no master row anywhere gives sql.ErrNoRows: the rows of
//     the calendar are orphan instances, and they export on their own.
func (s *Service) ExportMaster(ctx context.Context, calendarID int64, uid string) (Event, error) {
	// The common case: one lookup on the UNIQUE(calendar_id, uid,
	// recurrence_id) key, with no view.
	own, err := s.q.GetEventByCalendarUID(ctx, storage.GetEventByCalendarUIDParams{CalendarID: calendarID, Uid: uid})
	if err == nil {
		return s.FromStorageWithCategories(ctx, own), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Event{}, fmt.Errorf("get master: %w", err)
	}
	_, err = findSeriesMasterIncludingDeletedTx(ctx, s.q, calendarID, uid)
	switch {
	case err == nil, errors.Is(err, ErrAmbiguousUID):
		return Event{}, ErrNotExportedHere
	case !errors.Is(err, sql.ErrNoRows):
		return Event{}, err
	}
	// No owner. A calendar with no row of a UID that has masters elsewhere
	// exports nothing. Only a UID with no master row anywhere leaves
	// orphan instances.
	masters, err := s.q.ListEventMastersByUIDIncludingDeleted(ctx, uid)
	if err != nil {
		return Event{}, fmt.Errorf("list masters: %w", err)
	}
	if len(masters) > 0 {
		return Event{}, ErrNotExportedHere
	}
	return Event{}, sql.ErrNoRows
}

// MasterInTrash reports whether calendarID holds its own master of uid in
// the trash.
func (s *Service) MasterInTrash(ctx context.Context, calendarID int64, uid string) (bool, error) {
	m, found, err := calendarMasterTx(ctx, s.q, calendarID, uid)
	return found && m.DeletedAt != nil, err
}

// LiveRowInCalendar returns the one live override row of uid in calendarID,
// with no series rule. No row gives sql.ErrNoRows. More rows give an
// *AmbiguousOverrideError with their IDs, so the user chooses one.
func (s *Service) LiveRowInCalendar(ctx context.Context, calendarID int64, uid string) (Event, error) {
	rows, err := s.q.ListLiveOverridesInCalendarByUID(ctx, storage.ListLiveOverridesInCalendarByUIDParams{
		CalendarID: calendarID, Uid: uid,
	})
	if err != nil {
		return Event{}, err
	}
	return s.oneRow(ctx, rows, false)
}

// ListLiveOverridesInCalendar returns the live override rows of uid in
// calendarID with no series rule. The sync export of a calendar that holds
// orphan instances reads it (see ListLiveOverridesInCalendarByUID).
func (s *Service) ListLiveOverridesInCalendar(ctx context.Context, calendarID int64, uid string) ([]Event, error) {
	rows, err := s.q.ListLiveOverridesInCalendarByUID(ctx, storage.ListLiveOverridesInCalendarByUIDParams{
		CalendarID: calendarID, Uid: uid,
	})
	if err != nil {
		return nil, err
	}
	events := fromStorageSlice(rows)
	s.populateCategories(ctx, events)
	return events, nil
}

// ListSeriesOverrides returns the live override rows of the series of uid
// in calendarID. These are the rows of that calendar and the legacy
// overrides that the series owns (see storage.LegacyOverride). The list
// holds one row per occurrence: the query gives the rows in the order of
// the tie rule, and the first row of each recurrence ID stays. The export
// then never holds two VEVENTs with one RECURRENCE-ID.
func (s *Service) ListSeriesOverrides(ctx context.Context, calendarID int64, uid string) ([]Event, error) {
	rows, err := s.q.ListSeriesOverridesInCalendar(ctx, storage.ListSeriesOverridesInCalendarParams{
		CalendarID: calendarID, Uid: uid,
	})
	if err != nil {
		return nil, err
	}
	events := fromStorageSlice(storage.FirstPerRecurrenceID(rows))
	s.populateCategories(ctx, events)
	return events, nil
}
