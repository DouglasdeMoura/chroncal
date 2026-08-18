package todo

import (
	"context"
	"errors"
	"testing"
)

func TestTodoServiceCreateRejectsDueDateAndDuration(t *testing.T) {
	svc := newTestService(t)

	_, err := svc.Create(context.Background(), CreateParams{
		CalendarID: 1,
		Summary:    "Invalid",
		DueDate:    "2026-04-01",
		StartDate:  "2026-04-01",
		Duration:   "PT1H",
	})
	if !errors.Is(err, ErrInvalidTiming) {
		t.Fatalf("expected ErrInvalidTiming, got %v", err)
	}
}

func TestTodoServiceUpdateRejectsDurationWithoutStartDate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created := createTodo(t, svc)

	_, err := svc.Update(ctx, created.ID, UpdateParams{
		Summary:    created.Summary,
		CalendarID: created.CalendarID,
		Duration:   "PT1H",
	})
	if !errors.Is(err, ErrInvalidTiming) {
		t.Fatalf("expected ErrInvalidTiming, got %v", err)
	}
}

// The service boundary owns the span rule, so every CLI and TUI write
// path rejects a malformed or negative duration in one place (issue
// #582 round 3). The import path drops such a value with a warning
// before it reaches this boundary.
func TestTodoServiceCreateRejectsInvalidDuration(t *testing.T) {
	svc := newTestService(t)

	for _, dur := range []string{"Pjunk", "-PT1H", "PT0S", "PT5124096H"} {
		_, err := svc.Create(context.Background(), CreateParams{
			CalendarID: 1,
			Summary:    "Invalid duration",
			StartDate:  "2026-04-01",
			Duration:   dur,
		})
		if !errors.Is(err, ErrInvalidTiming) {
			t.Errorf("duration %q: expected ErrInvalidTiming, got %v", dur, err)
		}
	}
}

func TestTodoServiceUpsertRejectsDurationWithoutStartDate(t *testing.T) {
	svc := newTestService(t)

	_, err := svc.UpsertByUID(context.Background(), UpsertParams{
		UID:        "invalid-upsert",
		CalendarID: 1,
		Summary:    "Invalid",
		Duration:   "PT1H",
	})
	if !errors.Is(err, ErrInvalidTiming) {
		t.Fatalf("expected ErrInvalidTiming, got %v", err)
	}
}
