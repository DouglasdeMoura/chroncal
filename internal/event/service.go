package event

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/douglasdemoura/tcal/internal/storage"
)

type Service struct {
	q *storage.Queries
}

func NewService(q *storage.Queries) *Service {
	return &Service{q: q}
}

type CreateParams struct {
	CalendarID     int64
	Title          string
	Description    string
	Location       string
	StartTime      time.Time
	EndTime        time.Time
	AllDay         bool
	RecurrenceRule string
}

type UpdateParams struct {
	Title          string
	Description    string
	Location       string
	StartTime      time.Time
	EndTime        time.Time
	AllDay         bool
	RecurrenceRule string
	CalendarID     int64
}

type UpsertParams struct {
	UID            string
	CalendarID     int64
	Title          string
	Description    string
	Location       string
	StartTime      time.Time
	EndTime        time.Time
	AllDay         bool
	RecurrenceRule string
}

func (s *Service) ListByDateRange(ctx context.Context, from, to time.Time) ([]Event, error) {
	rows, err := s.q.ListEventsByDateRange(ctx, storage.ListEventsByDateRangeParams{
		StartTime:   from.Format(time.RFC3339),
		StartTime_2: to.Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}
	return fromStorageSlice(rows), nil
}

func (s *Service) ListByCalendarAndDateRange(ctx context.Context, calID int64, from, to time.Time) ([]Event, error) {
	rows, err := s.q.ListEventsByCalendarAndDateRange(ctx, storage.ListEventsByCalendarAndDateRangeParams{
		CalendarID:  calID,
		StartTime:   from.Format(time.RFC3339),
		StartTime_2: to.Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}
	return fromStorageSlice(rows), nil
}

func (s *Service) Get(ctx context.Context, id int64) (Event, error) {
	r, err := s.q.GetEvent(ctx, id)
	if err != nil {
		return Event{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) Create(ctx context.Context, p CreateParams) (Event, error) {
	allDay := int64(0)
	if p.AllDay {
		allDay = 1
	}
	r, err := s.q.CreateEvent(ctx, storage.CreateEventParams{
		Uid:            uuid.New().String(),
		CalendarID:     p.CalendarID,
		Title:          p.Title,
		Description:    p.Description,
		Location:       p.Location,
		StartTime:      p.StartTime.Format(time.RFC3339),
		EndTime:        p.EndTime.Format(time.RFC3339),
		AllDay:         allDay,
		RecurrenceRule: p.RecurrenceRule,
	})
	if err != nil {
		return Event{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) Update(ctx context.Context, id int64, p UpdateParams) (Event, error) {
	allDay := int64(0)
	if p.AllDay {
		allDay = 1
	}
	r, err := s.q.UpdateEvent(ctx, storage.UpdateEventParams{
		ID:             id,
		Title:          p.Title,
		Description:    p.Description,
		Location:       p.Location,
		StartTime:      p.StartTime.Format(time.RFC3339),
		EndTime:        p.EndTime.Format(time.RFC3339),
		AllDay:         allDay,
		RecurrenceRule: p.RecurrenceRule,
		CalendarID:     p.CalendarID,
	})
	if err != nil {
		return Event{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) UpsertByUID(ctx context.Context, p UpsertParams) (Event, error) {
	allDay := int64(0)
	if p.AllDay {
		allDay = 1
	}
	r, err := s.q.UpsertEventByUID(ctx, storage.UpsertEventByUIDParams{
		Uid:            p.UID,
		CalendarID:     p.CalendarID,
		Title:          p.Title,
		Description:    p.Description,
		Location:       p.Location,
		StartTime:      p.StartTime.Format(time.RFC3339),
		EndTime:        p.EndTime.Format(time.RFC3339),
		AllDay:         allDay,
		RecurrenceRule: p.RecurrenceRule,
	})
	if err != nil {
		return Event{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.q.DeleteEvent(ctx, id)
}

func fromStorage(r storage.Event) Event {
	return Event{
		ID:             r.ID,
		UID:            r.Uid,
		CalendarID:     r.CalendarID,
		Title:          r.Title,
		Description:    r.Description,
		Location:       r.Location,
		StartTime:      parseTime(r.StartTime),
		EndTime:        parseTime(r.EndTime),
		AllDay:         r.AllDay == 1,
		RecurrenceRule: r.RecurrenceRule,
		CreatedAt:      parseTime(r.CreatedAt),
		UpdatedAt:      parseTime(r.UpdatedAt),
	}
}

func fromStorageSlice(rows []storage.Event) []Event {
	events := make([]Event, len(rows))
	for i, r := range rows {
		events[i] = fromStorage(r)
	}
	return events
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
