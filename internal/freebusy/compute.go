package freebusy

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/recurrence"
)

// ExpandedEventSource provides recurrence-expanded VEVENT instances.
type ExpandedEventSource interface {
	ListExpandedEvents(ctx context.Context, from, to time.Time, opts ...recurrence.ExpandOption) ([]recurrence.ExpandedEvent, error)
}

// Compute derives local busy time from expanded VEVENT instances.
func Compute(ctx context.Context, source ExpandedEventSource, from, to time.Time, calendarIDs []int64) (Result, error) {
	expanded, err := source.ListExpandedEvents(ctx, from, to)
	if err != nil {
		return Result{}, err
	}

	calFilter := make(map[int64]struct{}, len(calendarIDs))
	for _, id := range calendarIDs {
		calFilter[id] = struct{}{}
	}

	periods := make([]Period, 0, len(expanded))
	for _, evt := range expanded {
		if len(calFilter) > 0 {
			if _, ok := calFilter[evt.CalendarID]; !ok {
				continue
			}
		}
		if strings.EqualFold(evt.Status, "CANCELLED") || strings.EqualFold(evt.Transp, "TRANSPARENT") {
			continue
		}

		start, end := eventWindow(evt)
		if end.Before(to) || end.Equal(to) {
			// half-open clipping is still applied below
		}
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		if !end.After(start) {
			continue
		}

		kind := Busy
		if strings.EqualFold(evt.Status, "TENTATIVE") {
			kind = BusyTentative
		}
		periods = append(periods, Period{
			Start: start.UTC(),
			End:   end.UTC(),
			Type:  kind,
		})
	}

	return Result{
		Start:   from.UTC(),
		End:     to.UTC(),
		Periods: normalizePeriods(periods),
	}, nil
}

func eventWindow(evt recurrence.ExpandedEvent) (time.Time, time.Time) {
	if evt.RecurrenceID != "" {
		return evt.StartTime.UTC(), evt.EndTime.UTC()
	}
	start := evt.InstanceTime
	if start.IsZero() {
		start = evt.StartTime
	}
	return start.UTC(), start.Add(evt.EndTime.Sub(evt.StartTime)).UTC()
}

func normalizePeriods(periods []Period) []Period {
	if len(periods) == 0 {
		return nil
	}

	boundaries := make([]time.Time, 0, len(periods)*2)
	for _, period := range periods {
		if !period.End.After(period.Start) {
			continue
		}
		boundaries = append(boundaries, period.Start.UTC(), period.End.UTC())
	}
	if len(boundaries) == 0 {
		return nil
	}

	sort.Slice(boundaries, func(i, j int) bool { return boundaries[i].Before(boundaries[j]) })
	uniq := boundaries[:1]
	for _, boundary := range boundaries[1:] {
		if boundary.Equal(uniq[len(uniq)-1]) {
			continue
		}
		uniq = append(uniq, boundary)
	}

	normalized := make([]Period, 0, len(uniq)-1)
	for i := 0; i < len(uniq)-1; i++ {
		start, end := uniq[i], uniq[i+1]
		if !end.After(start) {
			continue
		}

		activeType := ""
		activePriority := 0
		for _, period := range periods {
			if !period.Start.Before(end) || !period.End.After(start) {
				continue
			}
			if priority := typePriority(period.Type); priority > activePriority {
				activePriority = priority
				activeType = normalizeType(period.Type)
			}
		}
		if activeType == "" {
			continue
		}

		if len(normalized) > 0 {
			last := &normalized[len(normalized)-1]
			if last.Type == activeType && last.End.Equal(start) {
				last.End = end
				continue
			}
		}
		normalized = append(normalized, Period{
			Start: start,
			End:   end,
			Type:  activeType,
		})
	}

	return normalized
}
