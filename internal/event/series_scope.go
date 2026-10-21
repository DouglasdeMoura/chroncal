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

// findSeriesMasterTx finds the live master of the series of uid for an
// edit of the series: a truncation, an occurrence delete, a series delete,
// or an export. Two calendars can hold the same UID (issue #842), so
// calendarID names the calendar of the series.
//
// A calendar with a live master of the UID gives that master. A calendar
// with a master of the UID in the trash gives sql.ErrNoRows: the series is
// not live there. A calendar with no master row of the UID holds a legacy
// override at most (see storage.LegacyOverride). The owner of such a row
// is the one live master of the UID (uidmove.LegacyOwner), so one live
// master is the choice, and more give ErrAmbiguousUID. A calendar with no
// live row of the UID gives sql.ErrNoRows. calendarID 0 names no
// calendar: one live master is then the only choice, none gives
// sql.ErrNoRows, and more give ErrAmbiguousUID. The view
// event_series_rows answers the rule for a calendar in one query.
func findSeriesMasterTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) (storage.Event, error) {
	return findSeriesMaster(ctx, qtx, calendarID, uid, false)
}

// findSeriesMasterIncludingDeletedTx is findSeriesMasterTx for a restore.
// The master can be in the trash, so a master row in the trash counts as
// the master of its calendar. With calendarID 0, or for a legacy
// override, the owner rule (uidmove.LegacyOwner) applies in full: one
// live master is the choice, and with no live master one master in the
// trash is the choice. A copy of the UID in the trash of another
// calendar, for example a purge-pending copy, then never blocks a
// restore.
func findSeriesMasterIncludingDeletedTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) (storage.Event, error) {
	return findSeriesMaster(ctx, qtx, calendarID, uid, true)
}

func findSeriesMaster(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string, includeDeleted bool) (storage.Event, error) {
	if calendarID == 0 {
		return seriesMasterOfUID(ctx, qtx, uid, includeDeleted)
	}
	// The view event_series_rows answers the rule for a calendar in one
	// query: the master whose series owns the rows of the calendar.
	var master storage.Event
	var err error
	if includeDeleted {
		master, err = qtx.GetSeriesMasterOfCalendarIncludingDeleted(ctx, storage.GetSeriesMasterOfCalendarIncludingDeletedParams{
			CalendarID: calendarID, Uid: uid,
		})
	} else {
		master, err = qtx.GetLiveSeriesMasterOfCalendar(ctx, storage.GetLiveSeriesMasterOfCalendarParams{
			CalendarID: calendarID, Uid: uid,
		})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return master, err
	}
	// No series owns the rows of the calendar. When the UID has more
	// than one live master and the calendar holds a row of the UID, the
	// rule cannot choose, so the caller can name the series.
	rows, err := qtx.ListEventMastersByUIDIncludingDeleted(ctx, uid)
	if err != nil {
		return storage.Event{}, err
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
	// A live edit counts the live rows of the calendar only: a calendar
	// whose rows of the UID are all in the trash holds no live series,
	// so it gets sql.ErrNoRows, not the ambiguity.
	var held int64
	if includeDeleted {
		n, err := qtx.CountEventUIDRowsInCalendar(ctx, storage.CountEventUIDRowsInCalendarParams{
			CalendarID: calendarID, Uid: uid,
		})
		if err != nil {
			return storage.Event{}, err
		}
		held = n.Masters + n.Overrides
	} else {
		n, err := qtx.CountLiveEventUIDRowsInCalendar(ctx, storage.CountLiveEventUIDRowsInCalendarParams{
			CalendarID: calendarID, Uid: uid,
		})
		if err != nil {
			return storage.Event{}, err
		}
		held = n
	}
	if held == 0 {
		return storage.Event{}, sql.ErrNoRows
	}
	return storage.Event{}, ErrAmbiguousUID
}

// seriesMasterOfUID is findSeriesMaster for calendar 0: no calendar
// anchors the lookup, so the owner rule (uidmove.LegacyOwner) picks the
// master among every master row of the UID.
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
		if len(masters) == 0 {
			return storage.Event{}, sql.ErrNoRows
		}
	}
	owner, ok, ambiguous := uidmove.LegacyOwner(masters)
	if ambiguous {
		return storage.Event{}, ErrAmbiguousUID
	}
	if !ok {
		return storage.Event{}, sql.ErrNoRows
	}
	for _, m := range rows {
		if m.CalendarID != owner {
			continue
		}
		if m.DeletedAt != nil && !includeDeleted {
			// The owner is in the trash, so no live series exists.
			return storage.Event{}, sql.ErrNoRows
		}
		return m, nil
	}
	return storage.Event{}, sql.ErrNoRows
}

// liveMasters returns the masters that are not in the trash.
func liveMasters(masters []uidmove.Master) []uidmove.Master {
	live := make([]uidmove.Master, 0, len(masters))
	for _, m := range masters {
		if !m.Trashed {
			live = append(live, m)
		}
	}
	return live
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

// ensureSeriesWritableIn enforces the remote access and component policy
// on every calendar that holds a row of the series of uid in calendarID:
// that calendar and each calendar with a legacy override of the UID (see
// storage.LegacyOverride). The other calendars that hold the UID stay
// out, so a read-only copy of the UID in calendar A does not block a
// delete or a restore of the series in calendar B. calendarID 0 names no
// calendar: the guard then covers every calendar that holds a row of the
// UID, live or in the trash.
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
	r, err := findInstanceOverrideTx(ctx, s.q, master, recID)
	if err != nil {
		return Event{}, err
	}
	e := FromStorage(r)
	s.populateSingleCategories(ctx, &e)
	return e, nil
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
// in calendarID: the rows of that calendar and the legacy overrides of
// the UID (see storage.LegacyOverride). The override rows of a master in
// another calendar stay out.
func (s *Service) ListSeriesOverrides(ctx context.Context, calendarID int64, uid string) ([]Event, error) {
	rows, err := s.q.ListSeriesOverridesInCalendar(ctx, storage.ListSeriesOverridesInCalendarParams{
		CalendarID: calendarID, Uid: uid,
	})
	if err != nil {
		return nil, err
	}
	events := fromStorageSlice(rows)
	s.populateCategories(ctx, events)
	return events, nil
}
