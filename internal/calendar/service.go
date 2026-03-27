package calendar

import (
	"context"
	"time"

	"github.com/douglasdemoura/tcal/internal/storage"
)

type Service struct {
	q *storage.Queries
}

func NewService(q *storage.Queries) *Service {
	return &Service{q: q}
}

func (s *Service) List(ctx context.Context) ([]Calendar, error) {
	rows, err := s.q.ListCalendars(ctx)
	if err != nil {
		return nil, err
	}
	cals := make([]Calendar, len(rows))
	for i, r := range rows {
		cals[i] = fromStorage(r)
	}
	return cals, nil
}

func (s *Service) Get(ctx context.Context, id int64) (Calendar, error) {
	r, err := s.q.GetCalendar(ctx, id)
	if err != nil {
		return Calendar{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) Create(ctx context.Context, name, color, description string) (Calendar, error) {
	r, err := s.q.CreateCalendar(ctx, storage.CreateCalendarParams{
		Name:        name,
		Color:       color,
		Description: description,
	})
	if err != nil {
		return Calendar{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) Update(ctx context.Context, id int64, name, color, description string) (Calendar, error) {
	r, err := s.q.UpdateCalendar(ctx, storage.UpdateCalendarParams{
		ID:          id,
		Name:        name,
		Color:       color,
		Description: description,
	})
	if err != nil {
		return Calendar{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.q.DeleteCalendar(ctx, id)
}

func fromStorage(r storage.Calendar) Calendar {
	return Calendar{
		ID:          r.ID,
		Name:        r.Name,
		Color:       r.Color,
		Description: r.Description,
		CreatedAt:   parseTime(r.CreatedAt),
		UpdatedAt:   parseTime(r.UpdatedAt),
	}
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
