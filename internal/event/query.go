package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

type SearchParams struct {
	Query      string
	CalendarID int64  // 0 = all calendars
	From       string // RFC3339 or empty
	To         string // RFC3339 or empty
	Status     string // empty = all
}

type ExportParams struct {
	CalendarID int64  // 0 = all
	From       string // RFC3339 or empty
	To         string // RFC3339 or empty
	Category   string // empty = all
	Status     string // empty = all
}

func (s *Service) CountByCalendar(ctx context.Context, calendarID int64) (int64, error) {
	return s.q.CountEventsByCalendar(ctx, calendarID)
}

func (s *Service) ListByDateRange(ctx context.Context, from, to time.Time) ([]Event, error) {
	qFrom, qTo := timeutil.CoverWindow(from, to)
	rows, err := s.q.ListEventsByDateRange(ctx, storage.ListEventsByDateRangeParams{
		StartTime: qTo.UTC().Format(time.RFC3339),   // start_time < to
		EndTime:   qFrom.UTC().Format(time.RFC3339), // end_time > from
	})
	if err != nil {
		return nil, err
	}
	events := inWindow(fromStorageSlice(rows), from, to)
	s.populateCategories(ctx, events)
	return events, nil
}

func (s *Service) ListByCalendarAndDateRange(ctx context.Context, calID int64, from, to time.Time) ([]Event, error) {
	qFrom, qTo := timeutil.CoverWindow(from, to)
	rows, err := s.q.ListEventsByCalendarAndDateRange(ctx, storage.ListEventsByCalendarAndDateRangeParams{
		CalendarID: calID,
		StartTime:  qTo.UTC().Format(time.RFC3339),   // start_time < to
		EndTime:    qFrom.UTC().Format(time.RFC3339), // end_time > from
	})
	if err != nil {
		return nil, err
	}
	events := inWindow(fromStorageSlice(rows), from, to)
	s.populateCategories(ctx, events)
	return events, nil
}

func (s *Service) Search(ctx context.Context, p SearchParams) ([]Event, error) {
	ftsQuery := storage.FTSQuery(p.Query)
	if ftsQuery == "" {
		return []Event{}, nil
	}
	rows, err := s.q.SearchEventsFTS(ctx, ftsQuery, p.CalendarID, p.From, p.To, p.Status)
	if err != nil {
		return nil, fmt.Errorf("search events: %w", err)
	}
	events := fromStorageSlice(rows)
	s.populateCategories(ctx, events)
	return events, nil
}

func (s *Service) ExportFiltered(ctx context.Context, p ExportParams) ([]Event, error) {
	from, err := parseExportBound("from", p.From)
	if err != nil {
		return nil, err
	}
	to, err := parseExportBound("to", p.To)
	if err != nil {
		return nil, err
	}
	qFrom, qTo := timeutil.CoverWindow(from, to)
	rows, err := s.q.ListEventsForExport(ctx, storage.EventFilterParams{
		CalendarID:   p.CalendarID,
		FromTime:     timeutil.RangeBound(qFrom),
		ToTime:       timeutil.RangeBound(qTo),
		Category:     p.Category,
		FilterStatus: p.Status,
	})
	if err != nil {
		return nil, fmt.Errorf("export events: %w", err)
	}
	events := inWindow(fromStorageSlice(rows), from, to)
	s.populateCategories(ctx, events)
	return events, nil
}

// parseExportBound parses an RFC 3339 bound of ExportParams. An empty
// bound gives the zero time (no bound). A bad bound gives an error, so a
// typo never removes the filter.
func parseExportBound(name, s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("export events: bad %s bound %q: %w", name, s, err)
	}
	return t, nil
}

// inWindow keeps the events that overlap [from, to) by the rule of their
// own kind (Event.Overlaps). The rows come from a query over
// timeutil.CoverWindow, so the query also reads the events of the other
// kind of window.
func inWindow(events []Event, from, to time.Time) []Event {
	kept := events[:0]
	for _, e := range events {
		if e.Overlaps(from, to) {
			kept = append(kept, e)
		}
	}
	return kept
}

func (s *Service) Get(ctx context.Context, id int64) (Event, error) {
	r, err := s.q.GetEvent(ctx, id)
	if err != nil {
		return Event{}, err
	}
	e := FromStorage(r)
	s.populateSingleCategories(ctx, &e)
	return e, nil
}

// GetByUID returns the live master of uid. Two calendars can hold the
// same UID (issue #842). With one live master, that master is the result.
// With more, the lookup cannot choose, so it returns ErrAmbiguousUID. Use
// GetByCalendarUID or SeriesMaster with the calendar of the series.
func (s *Service) GetByUID(ctx context.Context, uid string) (Event, error) {
	return s.SeriesMaster(ctx, 0, uid)
}

func (s *Service) GetByCalendarUID(ctx context.Context, calendarID int64, uid string) (Event, error) {
	r, err := s.q.GetEventByCalendarUID(ctx, storage.GetEventByCalendarUIDParams{
		CalendarID: calendarID,
		Uid:        uid,
	})
	if err != nil {
		return Event{}, err
	}
	e := FromStorage(r)
	s.populateSingleCategories(ctx, &e)
	return e, nil
}

// GetByUIDAndRecurrenceID returns the live override row of one occurrence of
// uid. Two calendars can hold the same UID (issue #842). The series rule
// names the series (see SeriesOccurrence). Two live masters give
// ErrAmbiguousUID. Two legacy rows of the occurrence in one series give
// ErrAmbiguousOverride with the row IDs. The CLI delete and get resolve a UID
// and a recurrence ID with it, and the user chooses the row. A live master
// with no override at the occurrence gives the one live row outside the live
// series (oneLiveRowAt). A UID with no live master reads its orphan instances
// (OrphanOccurrence). To name the series, use SeriesOverride or
// GetByCalendarUIDAndRecurrenceID.
func (s *Service) GetByUIDAndRecurrenceID(ctx context.Context, uid, recurrenceID string) (Event, error) {
	master, override, err := s.seriesOccurrence(ctx, 0, uid, recurrenceID, true)
	switch {
	case err == nil && override != nil:
		return *override, nil
	case err == nil:
		// The live series has no row at the occurrence. A calendar with
		// its own master in the trash can hold a live row there, outside
		// the live series.
		return s.oneLiveRowAt(ctx, uid, recurrenceID, master.CalendarID)
	case !errors.Is(err, sql.ErrNoRows):
		return Event{}, err
	}
	return s.OrphanOccurrence(ctx, 0, uid, recurrenceID)
}

// oneLiveRowAt returns the one live row of uid at recurrenceID outside the
// calendar skip, in one query. 0 skips no calendar. No row gives
// sql.ErrNoRows. More rows give an *AmbiguousOverrideError beside a live
// series (skip set), and ErrAmbiguousUID for orphan rows (skip 0).
func (s *Service) oneLiveRowAt(ctx context.Context, uid, recurrenceID string, skip int64) (Event, error) {
	return s.liveRowAt(ctx, uid, recurrenceID, func(r storage.Event) bool { return r.CalendarID != skip }, skip == 0)
}

// liveRowAt returns the one live row of uid at recurrenceID that keep
// accepts. orphan picks the error for more rows: ErrAmbiguousUID for orphan
// rows, else an *AmbiguousOverrideError.
func (s *Service) liveRowAt(ctx context.Context, uid, recurrenceID string, keep func(storage.Event) bool, orphan bool) (Event, error) {
	rows, err := s.q.ListEventsByUIDAndRecurrenceID(ctx, storage.ListEventsByUIDAndRecurrenceIDParams{
		Uid: uid, RecurrenceID: recurrenceID,
	})
	if err != nil {
		return Event{}, err
	}
	kept := make([]storage.Event, 0, len(rows))
	for _, r := range rows {
		if keep(r) {
			kept = append(kept, r)
		}
	}
	return s.oneRow(ctx, kept, orphan)
}

// oneRow returns the one row of rows. No row gives sql.ErrNoRows. More rows
// give ErrAmbiguousUID when orphan is true, else an *AmbiguousOverrideError
// with their IDs.
func (s *Service) oneRow(ctx context.Context, rows []storage.Event, orphan bool) (Event, error) {
	switch {
	case len(rows) == 0:
		return Event{}, sql.ErrNoRows
	case len(rows) > 1 && orphan:
		return Event{}, ErrAmbiguousUID
	case len(rows) > 1:
		ids := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		return Event{}, &AmbiguousOverrideError{IDs: ids}
	}
	e := FromStorage(rows[0])
	s.populateSingleCategories(ctx, &e)
	return e, nil
}

// OrphanOccurrence returns the live row of one occurrence of a UID with no
// live master anywhere. Such a row is an orphan instance, for example a
// Google instance under its own UID. With one row, that row is the result.
// With more, the lookup cannot choose, so it returns ErrAmbiguousUID.
// calendarID 0 reads every calendar. Another calendarID reads that calendar
// only, so --calendar can choose one of two orphan rows.
func (s *Service) OrphanOccurrence(ctx context.Context, calendarID int64, uid, recurrenceID string) (Event, error) {
	if calendarID == 0 {
		return s.oneLiveRowAt(ctx, uid, recurrenceID, 0)
	}
	return s.liveRowAt(ctx, uid, recurrenceID, func(r storage.Event) bool { return r.CalendarID == calendarID }, true)
}

func (s *Service) GetByCalendarUIDAndRecurrenceID(ctx context.Context, calendarID int64, uid, recurrenceID string) (Event, error) {
	r, err := s.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID:   calendarID,
		Uid:          uid,
		RecurrenceID: recurrenceID,
	})
	if err != nil {
		return Event{}, err
	}
	e := FromStorage(r)
	s.populateSingleCategories(ctx, &e)
	return e, nil
}

// Category hydration

// FromStorageWithCategories converts r and loads its categories. A caller
// that read the row already uses it in place of Get.
func (s *Service) FromStorageWithCategories(ctx context.Context, r storage.Event) Event {
	e := FromStorage(r)
	s.populateSingleCategories(ctx, &e)
	return e
}

func (s *Service) populateSingleCategories(ctx context.Context, e *Event) {
	cats, err := s.ListCategories(ctx, e.ID)
	if err != nil {
		log.Printf("populateSingleCategories failed for event %d: %v", e.ID, err)
		return
	}
	e.Categories = timeutil.JoinCategoryList(cats)
}

func (s *Service) populateCategories(ctx context.Context, events []Event) {
	if len(events) == 0 {
		return
	}
	ids := make([]int64, len(events))
	for i := range events {
		ids[i] = events[i].ID
	}
	rows, err := s.q.ListCategoriesByEventIDs(ctx, ids)
	if err != nil {
		log.Printf("populateCategories failed for %d events: %v", len(events), err)
		return
	}
	catMap := make(map[int64][]string, len(events))
	for _, r := range rows {
		catMap[r.EventID] = append(catMap[r.EventID], r.Category)
	}
	for i := range events {
		if cats, ok := catMap[events[i].ID]; ok {
			events[i].Categories = timeutil.JoinCategoryList(cats)
		}
	}
}
