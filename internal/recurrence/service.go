package recurrence

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/teambition/rrule-go"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/storage"
)

// Service handles recurrence expansion and caching
type Service struct {
	db *sql.DB
	q  *storage.Queries
}

func NewService(db *sql.DB, q *storage.Queries) *Service {
	return &Service{db: db, q: q}
}

// ExpandEvent generates all occurrences of an event within a date range
// Returns instances even for non-recurring events (single instance)
func ExpandEvent(evt event.Event, from, to time.Time) []ExpandedEvent {
	if evt.RecurrenceRule == "" {
		// Non-recurring event - return single instance if in range
		if evt.StartTime.Before(from) || !evt.StartTime.Before(to) {
			return nil
		}
		return []ExpandedEvent{{
			Event:        evt,
			InstanceTime: evt.StartTime,
			IsOverride:   false,
		}}
	}

	// Parse RRULE
	rruleStr := "RRULE:" + evt.RecurrenceRule
	set, err := rrule.StrToRRuleSet(rruleStr)
	if err != nil {
		// Invalid RRULE - fall back to single instance
		if evt.StartTime.Before(from) || !evt.StartTime.Before(to) {
			return nil
		}
		return []ExpandedEvent{{
			Event:        evt,
			InstanceTime: evt.StartTime,
			IsOverride:   false,
		}}
	}

	// Set DTSTART to the event's start time
	set.DTStart(evt.StartTime)

	// Add EXDATEs
	for _, ex := range evt.ParseExDates() {
		set.ExDate(ex)
	}

	// Add RDATEs
	for _, rd := range evt.ParseRDates() {
		set.RDate(rd)
	}

	// Get all occurrences in range
	occurrences := set.Between(from, to, true)

	var instances []ExpandedEvent
	for _, occ := range occurrences {
		// Check if this occurrence is from RDATE (override) or RRULE
		isRDate := false
		for _, rd := range evt.ParseRDates() {
			if occ.Equal(rd) {
				isRDate = true
				break
			}
		}

		instances = append(instances, ExpandedEvent{
			Event:        evt,
			InstanceTime: occ,
			IsOverride:   isRDate,
		})
	}

	return instances
}

// ExpandAndCache generates and caches instances for a single event
func (s *Service) ExpandAndCache(ctx context.Context, evt event.Event, from, to time.Time) error {
	if evt.RecurrenceRule == "" {
		return nil // Nothing to cache for non-recurring
	}

	instances := ExpandEvent(evt, from, to)

	// Use transaction for atomicity
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	q := s.q.WithTx(tx)

	// Clear existing instances in this range
	if err := q.DeleteRecurrenceInstances(ctx, storage.DeleteRecurrenceInstancesParams{
		EventID:    evt.ID,
		InstanceAt: from.Format(time.RFC3339),
	}); err != nil {
		return err
	}

	// Insert new instances
	for _, inst := range instances {
		_, err := q.InsertRecurrenceInstance(ctx, storage.InsertRecurrenceInstanceParams{
			EventID:    evt.ID,
			OriginalID: evt.ID,
			InstanceAt: inst.InstanceTime.Format(time.RFC3339),
			IsOverride: 0,
		})
		if err != nil && !isDuplicateError(err) {
			return err
		}
	}

	return tx.Commit()
}

// isDuplicateError checks if error is a unique constraint violation
func isDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// ListExpandedEvents returns events with their instances in a date range
// This merges both recurring and non-recurring events
func (s *Service) ListExpandedEvents(ctx context.Context, from, to time.Time) ([]ExpandedEvent, error) {
	// Get all events that might have occurrences in this range
	// For recurring events, we need the parent event regardless of its start time
	// For non-recurring, they must be in range
	rows, err := s.q.ListAllEvents(ctx)
	if err != nil {
		return nil, err
	}

	var results []ExpandedEvent

	for _, row := range rows {
		evt := event.Event{
			ID:             row.ID,
			UID:            row.Uid,
			CalendarID:     row.CalendarID,
			Title:          row.Title,
			Description:    row.Description,
			Location:       row.Location,
			StartTime:      parseTime(row.StartTime),
			EndTime:        parseTime(row.EndTime),
			AllDay:         row.AllDay != 0,
			RecurrenceRule: row.RecurrenceRule,
			Timezone:       row.Timezone,
			Status:         row.Status,
			Transp:         row.Transp,
			Sequence:       row.Sequence,
			Priority:       row.Priority,
			Class:          row.Class,
			URL:            row.Url,
			Categories:     row.Categories,
			ExDates:        row.Exdates,
			RDates:         row.Rdates,
			RecurrenceID:   row.RecurrenceID,
			Geo:            row.Geo,
			CreatedAt:      parseTime(row.CreatedAt),
			UpdatedAt:      parseTime(row.UpdatedAt),
		}

		instances := ExpandEvent(evt, from, to)
		results = append(results, instances...)
	}

	return results, nil
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
