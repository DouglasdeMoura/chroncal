package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

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
// override at most (see storage.LegacyOverride). Such an override belongs
// to the one live master of the UID, so one live master is the choice,
// and more give ErrAmbiguousUID. A calendar with no row of the UID gives
// sql.ErrNoRows. calendarID 0 names no calendar: one live master is then
// the only choice, none gives sql.ErrNoRows, and more give ErrAmbiguousUID.
//
// The second result lists every master row of the UID, live and in the
// trash, for the override lookup (findInstanceOverrideTx).
func findSeriesMasterTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) (storage.Event, []uidmove.Master, error) {
	return findSeriesMaster(ctx, qtx, calendarID, uid, false)
}

// findSeriesMasterIncludingDeletedTx is findSeriesMasterTx for a restore.
// The master can be in the trash, so a master row in the trash counts as
// the master of its calendar. With calendarID 0, or for a legacy
// override, one master row of the UID, live or in the trash, is the
// choice.
func findSeriesMasterIncludingDeletedTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) (storage.Event, []uidmove.Master, error) {
	return findSeriesMaster(ctx, qtx, calendarID, uid, true)
}

func findSeriesMaster(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string, includeDeleted bool) (storage.Event, []uidmove.Master, error) {
	rows, err := qtx.ListEventMastersByUIDIncludingDeleted(ctx, uid)
	if err != nil {
		return storage.Event{}, nil, err
	}
	masters := uidMasters(rows)
	var candidates []storage.Event
	for _, m := range rows {
		if calendarID != 0 && m.CalendarID == calendarID {
			if includeDeleted || m.DeletedAt == nil {
				return m, masters, nil
			}
			// The master of the calendar is in the trash, so the
			// series is not live there.
			return storage.Event{}, nil, sql.ErrNoRows
		}
		if includeDeleted || m.DeletedAt == nil {
			candidates = append(candidates, m)
		}
	}
	if calendarID != 0 {
		// The calendar holds no master row of the UID. A legacy override
		// in it belongs to the series of the one master. Any other
		// calendar holds no row of the series.
		n, err := qtx.CountEventUIDRowsInCalendar(ctx, storage.CountEventUIDRowsInCalendarParams{
			CalendarID: calendarID, Uid: uid,
		})
		if err != nil {
			return storage.Event{}, nil, err
		}
		if n.Overrides == 0 {
			return storage.Event{}, nil, sql.ErrNoRows
		}
	}
	switch len(candidates) {
	case 0:
		return storage.Event{}, nil, sql.ErrNoRows
	case 1:
		return candidates[0], masters, nil
	default:
		return storage.Event{}, nil, ErrAmbiguousUID
	}
}

// wrapMasterLookup keeps ErrAmbiguousUID bare, so a caller can match it,
// and wraps every other lookup error with "get master".
func wrapMasterLookup(err error) error {
	if errors.Is(err, ErrAmbiguousUID) {
		return err
	}
	return fmt.Errorf("get master: %w", err)
}

// SeriesMaster returns the live master of the series of uid in calendarID.
// See findSeriesMasterTx for the rule. calendarID 0 picks the one live
// master of the UID, or it returns ErrAmbiguousUID.
func (s *Service) SeriesMaster(ctx context.Context, calendarID int64, uid string) (Event, error) {
	r, _, err := findSeriesMasterTx(ctx, s.q, calendarID, uid)
	if err != nil {
		return Event{}, err
	}
	e := FromStorage(r)
	s.populateSingleCategories(ctx, &e)
	return e, nil
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
