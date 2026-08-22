package event

import (
	"context"
	"fmt"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
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
	rows, err := s.q.ListEventsByDateRange(ctx, storage.ListEventsByDateRangeParams{
		StartTime: to.UTC().Format(time.RFC3339),   // start_time < to
		EndTime:   from.UTC().Format(time.RFC3339), // end_time > from
	})
	if err != nil {
		return nil, err
	}
	events := fromStorageSlice(rows)
	s.populateCategories(ctx, events)
	return events, nil
}

func (s *Service) ListByCalendarAndDateRange(ctx context.Context, calID int64, from, to time.Time) ([]Event, error) {
	rows, err := s.q.ListEventsByCalendarAndDateRange(ctx, storage.ListEventsByCalendarAndDateRangeParams{
		CalendarID: calID,
		StartTime:  to.UTC().Format(time.RFC3339),   // start_time < to
		EndTime:    from.UTC().Format(time.RFC3339), // end_time > from
	})
	if err != nil {
		return nil, err
	}
	events := fromStorageSlice(rows)
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
	rows, err := s.q.ListEventsForExport(ctx, storage.EventFilterParams{
		CalendarID:   p.CalendarID,
		FromTime:     p.From,
		ToTime:       p.To,
		Category:     p.Category,
		FilterStatus: p.Status,
	})
	if err != nil {
		return nil, fmt.Errorf("export events: %w", err)
	}
	events := fromStorageSlice(rows)
	s.populateCategories(ctx, events)
	return events, nil
}

func (s *Service) ListOverridesByUID(ctx context.Context, uid string) ([]Event, error) {
	rows, err := s.q.ListOverridesByUID(ctx, uid)
	if err != nil {
		return nil, err
	}
	events := fromStorageSlice(rows)
	s.populateCategories(ctx, events)
	return events, nil
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

func (s *Service) GetByUID(ctx context.Context, uid string) (Event, error) {
	r, err := s.q.GetEventByUID(ctx, uid)
	if err != nil {
		return Event{}, err
	}
	e := FromStorage(r)
	s.populateSingleCategories(ctx, &e)
	return e, nil
}

func (s *Service) GetByUIDAndRecurrenceID(ctx context.Context, uid, recurrenceID string) (Event, error) {
	r, err := s.q.GetEventByUIDAndRecurrenceID(ctx, storage.GetEventByUIDAndRecurrenceIDParams{
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
