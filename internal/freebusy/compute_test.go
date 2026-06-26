package freebusy

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
)

type stubExpandedEventSource struct {
	events   []recurrence.ExpandedEvent
	gotFrom  time.Time
	gotTo    time.Time
	gotCalls int
}

func (s *stubExpandedEventSource) ListExpandedEvents(_ context.Context, from, to time.Time, _ ...recurrence.ExpandOption) ([]recurrence.ExpandedEvent, error) {
	s.gotFrom = from
	s.gotTo = to
	s.gotCalls++
	return s.events, nil
}

func TestCompute_UsesExpandedEventsAndFiltersCalendars(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	source := &stubExpandedEventSource{
		events: []recurrence.ExpandedEvent{
			{
				Event: event.Event{
					CalendarID: 1,
					StartTime:  time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC),
					EndTime:    time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC),
					Status:     "CONFIRMED",
					Transp:     "OPAQUE",
				},
				InstanceTime: time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC),
			},
			{
				Event: event.Event{
					CalendarID: 2,
					StartTime:  time.Date(2026, 4, 10, 11, 0, 0, 0, time.UTC),
					EndTime:    time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC),
					Status:     "CONFIRMED",
					Transp:     "OPAQUE",
				},
				InstanceTime: time.Date(2026, 4, 10, 11, 0, 0, 0, time.UTC),
			},
		},
	}

	result, err := Compute(context.Background(), source, from, to, []int64{2}, nil)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if source.gotCalls != 1 {
		t.Fatalf("ListExpandedEvents calls = %d, want 1", source.gotCalls)
	}
	if !source.gotFrom.Equal(from) || !source.gotTo.Equal(to) {
		t.Fatalf("ListExpandedEvents range = [%s, %s), want [%s, %s)", source.gotFrom, source.gotTo, from, to)
	}
	if len(result.Periods) != 1 {
		t.Fatalf("periods = %d, want 1", len(result.Periods))
	}
	if !result.Periods[0].Start.Equal(time.Date(2026, 4, 10, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("period start = %s, want 2026-04-10 11:00 UTC", result.Periods[0].Start)
	}
}

func TestCompute_RecurringInstancesUseInstanceTimeAndMergeOverlaps(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	source := &stubExpandedEventSource{
		events: []recurrence.ExpandedEvent{
			{
				Event: event.Event{
					CalendarID: 1,
					StartTime:  time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
					EndTime:    time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
					Status:     "CONFIRMED",
					Transp:     "OPAQUE",
				},
				InstanceTime: time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC),
			},
			{
				Event: event.Event{
					CalendarID: 1,
					StartTime:  time.Date(2026, 4, 10, 9, 30, 0, 0, time.UTC),
					EndTime:    time.Date(2026, 4, 10, 10, 30, 0, 0, time.UTC),
					Status:     "CONFIRMED",
					Transp:     "OPAQUE",
				},
				InstanceTime: time.Date(2026, 4, 10, 9, 30, 0, 0, time.UTC),
			},
			{
				Event: event.Event{
					CalendarID:   1,
					RecurrenceID: "2026-04-10T12:00:00Z",
					StartTime:    time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC),
					EndTime:      time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
					Status:       "CONFIRMED",
					Transp:       "OPAQUE",
				},
				InstanceTime: time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC),
			},
		},
	}

	result, err := Compute(context.Background(), source, from, to, nil, nil)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(result.Periods) != 2 {
		t.Fatalf("periods = %d, want 2", len(result.Periods))
	}
	if !result.Periods[0].Start.Equal(time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("period[0].start = %s", result.Periods[0].Start)
	}
	if !result.Periods[0].End.Equal(time.Date(2026, 4, 10, 10, 30, 0, 0, time.UTC)) {
		t.Fatalf("period[0].end = %s, want 10:30 UTC", result.Periods[0].End)
	}
	if !result.Periods[1].Start.Equal(time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("period[1].start = %s", result.Periods[1].Start)
	}
	if !result.Periods[1].End.Equal(time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("period[1].end = %s", result.Periods[1].End)
	}
}

func TestCompute_SkipsTransparentAndCancelledAndMapsTentative(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	source := &stubExpandedEventSource{
		events: []recurrence.ExpandedEvent{
			{
				Event: event.Event{
					StartTime: time.Date(2026, 4, 10, 8, 0, 0, 0, time.UTC),
					EndTime:   time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC),
					Status:    "CANCELLED",
					Transp:    "OPAQUE",
				},
				InstanceTime: time.Date(2026, 4, 10, 8, 0, 0, 0, time.UTC),
			},
			{
				Event: event.Event{
					StartTime: time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC),
					EndTime:   time.Date(2026, 4, 10, 11, 0, 0, 0, time.UTC),
					Status:    "CONFIRMED",
					Transp:    "TRANSPARENT",
				},
				InstanceTime: time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC),
			},
			{
				Event: event.Event{
					StartTime: time.Date(2026, 4, 10, 13, 0, 0, 0, time.UTC),
					EndTime:   time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
					Status:    "TENTATIVE",
					Transp:    "OPAQUE",
				},
				InstanceTime: time.Date(2026, 4, 10, 13, 0, 0, 0, time.UTC),
			},
		},
	}

	result, err := Compute(context.Background(), source, from, to, nil, nil)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(result.Periods) != 1 {
		t.Fatalf("periods = %d, want 1", len(result.Periods))
	}
	if result.Periods[0].Type != BusyTentative {
		t.Fatalf("period type = %q, want %q", result.Periods[0].Type, BusyTentative)
	}
}

func TestCompute_PreservesAllDayIntervals(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	source := &stubExpandedEventSource{
		events: []recurrence.ExpandedEvent{
			{
				Event: event.Event{
					AllDay:    true,
					StartTime: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
					EndTime:   time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC),
					Status:    "CONFIRMED",
					Transp:    "OPAQUE",
				},
				InstanceTime: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
			},
		},
	}

	result, err := Compute(context.Background(), source, from, to, nil, nil)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(result.Periods) != 1 {
		t.Fatalf("periods = %d, want 1", len(result.Periods))
	}
	if !result.Periods[0].Start.Equal(time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("start = %s", result.Periods[0].Start)
	}
	if !result.Periods[0].End.Equal(time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("end = %s", result.Periods[0].End)
	}
}

// TestCompute_SkipsOwnerDeclinedInstances reproduces issue #302: an event
// the calendar owner has DECLINED (attendee PARTSTAT=DECLINED) must not
// count as busy, while a still-accepted event on the same range does.
func TestCompute_SkipsOwnerDeclinedInstances(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	source := &stubExpandedEventSource{
		events: []recurrence.ExpandedEvent{
			{
				Event: event.Event{
					CalendarID: 1,
					StartTime:  time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC),
					EndTime:    time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC),
					Status:     "CONFIRMED",
					Transp:     "OPAQUE",
					Attendees: []model.Attendee{
						{Email: "mailto:me@example.com", RSVPStatus: "DECLINED"},
						{Email: "organizer@example.com", RSVPStatus: "ACCEPTED", Organizer: true},
					},
				},
				InstanceTime: time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC),
			},
			{
				Event: event.Event{
					CalendarID: 1,
					StartTime:  time.Date(2026, 4, 10, 11, 0, 0, 0, time.UTC),
					EndTime:    time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC),
					Status:     "CONFIRMED",
					Transp:     "OPAQUE",
					Attendees: []model.Attendee{
						{Email: "me@example.com", RSVPStatus: "ACCEPTED"},
					},
				},
				InstanceTime: time.Date(2026, 4, 10, 11, 0, 0, 0, time.UTC),
			},
		},
	}

	ownerEmails := map[int64]string{1: "me@example.com"}
	result, err := Compute(context.Background(), source, from, to, nil, ownerEmails)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(result.Periods) != 1 {
		t.Fatalf("periods = %d, want 1 (declined instance excluded)", len(result.Periods))
	}
	if !result.Periods[0].Start.Equal(time.Date(2026, 4, 10, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("period start = %s, want 2026-04-10 11:00 UTC (accepted instance)", result.Periods[0].Start)
	}
}
