package event

import (
	"context"
	"log"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// Converters

// FromStorage maps a storage.Event row to a domain Event. It is exported so
// recurrence expansion shares one mapper with the event service. The two then
// cannot drift. Every new storage.Event column lands here once.
func FromStorage(r storage.Event) Event {
	var deletedAt *time.Time
	if r.DeletedAt != nil && *r.DeletedAt != "" {
		t := timeutil.ParseDateTime(*r.DeletedAt)
		deletedAt = &t
	}
	return Event{
		ID:             r.ID,
		UID:            r.Uid,
		CalendarID:     r.CalendarID,
		Title:          r.Title,
		Description:    storage.NullableToString(r.Description),
		Location:       storage.NullableToString(r.Location),
		StartTime:      timeutil.ParseDateTime(r.StartTime),
		EndTime:        timeutil.ParseDateTime(r.EndTime),
		AllDay:         r.AllDay == 1,
		RecurrenceRule: storage.NullableToString(r.RecurrenceRule),
		Timezone:       storage.NullableToString(r.Timezone),
		Status:         r.Status,
		Transp:         r.Transp,
		Sequence:       r.Sequence,
		Priority:       r.Priority,
		Class:          r.Class,
		URL:            storage.NullableToString(r.Url),
		ConferenceURI:  r.ConferenceUri,
		ExDates:        storage.NullableToString(r.Exdates),
		RDates:         storage.NullableToString(r.Rdates),
		RecurrenceID:   r.RecurrenceID,
		Geo:            storage.NullableToString(r.Geo),
		DurationValue:  storage.NullableToString(r.Duration),
		DtStamp:        storage.NullableToString(r.Dtstamp),
		CreatedAt:      timeutil.ParseDateTime(r.CreatedAt),
		UpdatedAt:      timeutil.ParseDateTime(r.UpdatedAt),
		DeletedAt:      deletedAt,
	}
}

func fromStorageSlice(rows []storage.Event) []Event {
	events := make([]Event, len(rows))
	for i, r := range rows {
		events[i] = FromStorage(r)
	}
	return events
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
