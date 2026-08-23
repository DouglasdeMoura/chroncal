package event

import (
	"context"
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// Attendee CRUD

func (s *Service) ListAttendees(ctx context.Context, eventID int64) ([]model.Attendee, error) {
	rows, err := s.q.ListAttendeesByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	attendees := make([]model.Attendee, len(rows))
	for i, r := range rows {
		attendees[i] = fromStorageAttendee(r)
	}
	return attendees, nil
}

func (s *Service) ReplaceAttendees(ctx context.Context, eventID int64, attendees []model.Attendee) error {
	if err := s.ensureEventWritable(ctx, eventID, 0); err != nil {
		return err
	}
	return s.ReplaceAttendeesForSync(ctx, eventID, attendees)
}

// ReplaceAttendeesForSync applies an attendee set without remote
// access/component policy. It is reserved for the CalDAV sync engine, which
// mirrors server-originated VEVENTs into the local cache regardless of the
// linked collection's advertised write support. User-originated edits (for
// example the TUI RSVP flow) must route through ReplaceAttendees so the
// policy is enforced.
func (s *Service) ReplaceAttendeesForSync(ctx context.Context, eventID int64, attendees []model.Attendee) error {
	// Prepare before the transaction opens. A standalone call then
	// rejects a bad attendee without a write lock. See
	// model.PrepareAttendeesForWrite.
	attendees, err := model.PrepareAttendeesForWrite(model.EventAttendee, attendees)
	if err != nil {
		return err
	}
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()

	if err := replaceAttendeesTx(ctx, qtx, eventID, attendees); err != nil {
		return err
	}
	if err := commit(); err != nil {
		return err
	}
	return s.markDirtyByID(ctx, eventID)
}

// replaceAttendeesTx replaces an event's attendees using a tx-bound Queries. It
// opens no transaction so callers can compose it with the event row write
// inside one transaction.
func replaceAttendeesTx(ctx context.Context, qtx *storage.Queries, eventID int64, attendees []model.Attendee) error {
	if err := qtx.DeleteAttendeesByEventID(ctx, eventID); err != nil {
		return fmt.Errorf("delete attendees: %w", err)
	}
	for _, a := range attendees {
		rsvp := ""
		if a.RSVPRequested {
			rsvp = "TRUE"
		}
		_, err := qtx.CreateAttendee(ctx, storage.CreateAttendeeParams{
			EventID:       eventID,
			Email:         a.Email,
			Name:          storage.StringToNullable(a.Name),
			RsvpStatus:    a.RSVPStatus,
			Role:          a.Role,
			Organizer:     storage.BoolToInt(a.Organizer),
			Cutype:        storage.StringToNullable(a.CUType),
			Rsvp:          storage.StringToNullable(rsvp),
			SentBy:        storage.StringToNullable(a.SentBy),
			DelegatedTo:   storage.StringToNullable(a.DelegatedTo),
			DelegatedFrom: storage.StringToNullable(a.DelegatedFrom),
			Member:        storage.StringToNullable(a.Member),
			Dir:           storage.StringToNullable(a.Dir),
			Language:      storage.StringToNullable(a.Language),
		})
		if err != nil {
			return fmt.Errorf("create attendee: %w", err)
		}
	}
	return nil
}

func fromStorageAttendee(r storage.EventAttendee) model.Attendee {
	return model.Attendee{
		ID:            r.ID,
		EventID:       r.EventID,
		Email:         r.Email,
		Name:          storage.NullableToString(r.Name),
		RSVPStatus:    r.RsvpStatus,
		Role:          r.Role,
		Organizer:     r.Organizer == 1,
		CUType:        storage.NullableToString(r.Cutype),
		RSVPRequested: strings.EqualFold(storage.NullableToString(r.Rsvp), "TRUE"),
		SentBy:        storage.NullableToString(r.SentBy),
		DelegatedTo:   storage.NullableToString(r.DelegatedTo),
		DelegatedFrom: storage.NullableToString(r.DelegatedFrom),
		Member:        storage.NullableToString(r.Member),
		Dir:           storage.NullableToString(r.Dir),
		Language:      storage.NullableToString(r.Language),
	}
}
