package journal

import (
	"context"
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// Attendee CRUD

func (s *Service) ListAttendees(ctx context.Context, journalID int64) ([]model.Attendee, error) {
	rows, err := s.q.ListJournalAttendeesByJournalID(ctx, journalID)
	if err != nil {
		return nil, err
	}
	attendees := make([]model.Attendee, len(rows))
	for i, r := range rows {
		attendees[i] = model.Attendee{
			ID: r.ID, EventID: r.JournalID,
			Email: r.Email, Name: storage.NullableToString(r.Name),
			RSVPStatus: r.RsvpStatus, Role: r.Role,
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
	return attendees, nil
}

func (s *Service) ReplaceAttendees(ctx context.Context, journalID int64, attendees []model.Attendee) error {
	// Prepare before the transaction opens. A standalone call then
	// rejects a bad attendee without a write lock. See
	// model.PrepareAttendeesForWrite.
	attendees, err := model.PrepareAttendeesForWrite(model.TaskAttendee, attendees)
	if err != nil {
		return err
	}
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()

	if err := qtx.DeleteJournalAttendeesByJournalID(ctx, journalID); err != nil {
		return fmt.Errorf("delete attendees: %w", err)
	}
	for _, a := range attendees {
		org := storage.BoolToInt(a.Organizer)
		rsvp := ""
		if a.RSVPRequested {
			rsvp = "TRUE"
		}
		_, err := qtx.CreateJournalAttendee(ctx, storage.CreateJournalAttendeeParams{
			JournalID:     journalID,
			Email:         a.Email,
			Name:          storage.StringToNullable(a.Name),
			RsvpStatus:    a.RSVPStatus,
			Role:          a.Role,
			Organizer:     org,
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
	if err := commit(); err != nil {
		return err
	}
	return s.markDirtyByID(ctx, journalID)
}
