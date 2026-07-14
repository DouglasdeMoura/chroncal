package calendar

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// ErrLastCalendar is returned when attempting to delete the only remaining calendar.
var ErrLastCalendar = errors.New("cannot delete the last calendar")

// ErrDuplicateName is returned when a calendar name already exists.
var ErrDuplicateName = errors.New("a calendar with this name already exists")

// ErrDefaultCalendarRequiresPromotion is returned by Delete when the target
// row is the current default. Callers must pick a replacement and call
// DeleteAndPromote instead, so the default is never silently moved.
var ErrDefaultCalendarRequiresPromotion = errors.New("cannot delete the default calendar without promoting a replacement")

// ErrInvalidPromotionTarget is returned by DeleteAndPromote when the
// replacement default ID is the same as the calendar being deleted, or
// does not exist.
var ErrInvalidPromotionTarget = errors.New("invalid promotion target for default calendar")

type Service struct {
	db *sql.DB
	q  *storage.Queries
}

func NewService(db *sql.DB, q *storage.Queries) *Service {
	return &Service{db: db, q: q}
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

// SetOrder persists the given calendar IDs as display order 0..n-1 in a single
// transaction, so the sidebar (and manage-calendars dialog) render in this
// order. The caller passes the full ordered ID list — typically the sidebar's
// current row order after a move — making the operation idempotent and
// self-healing against any drift. IDs not present in the slice are left
// untouched.
func (s *Service) SetOrder(ctx context.Context, ids []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	qtx := s.q.WithTx(tx)
	for i, id := range ids {
		if err := qtx.SetCalendarDisplayOrder(ctx, storage.SetCalendarDisplayOrderParams{
			DisplayOrder: int64(i),
			ID:           id,
		}); err != nil {
			return fmt.Errorf("set display order: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Service) Get(ctx context.Context, id int64) (Calendar, error) {
	r, err := s.q.GetCalendar(ctx, id)
	if err != nil {
		return Calendar{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) Create(ctx context.Context, name, color, description string) (Calendar, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Calendar{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	qtx := s.q.WithTx(tx)
	r, err := qtx.CreateCalendar(ctx, storage.CreateCalendarParams{
		Name:        name,
		Color:       color,
		Description: storage.StringToNullable(description),
	})
	if err != nil {
		if isUniqueViolation(err, "calendars.name") {
			return Calendar{}, ErrDuplicateName
		}
		return Calendar{}, err
	}

	// If no other calendar holds the default, promote this one. This is
	// the silent "first calendar wins" rule, matching how Mail.app marks
	// the first added account as default — it never leaves the user
	// without a default to write into.
	count, err := qtx.CountDefaultCalendars(ctx)
	if err != nil {
		return Calendar{}, fmt.Errorf("count default calendars: %w", err)
	}
	if count == 0 {
		if err := qtx.SetCalendarAsDefault(ctx, r.ID); err != nil {
			return Calendar{}, fmt.Errorf("set default: %w", err)
		}
		r.IsDefault = 1
	}

	if err := tx.Commit(); err != nil {
		return Calendar{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) Update(ctx context.Context, id int64, name, color, description string) (Calendar, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Calendar{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	qtx := s.q.WithTx(tx)
	existing, err := qtx.GetCalendar(ctx, id)
	if err != nil {
		return Calendar{}, err
	}

	if _, err := qtx.UpdateCalendar(ctx, storage.UpdateCalendarParams{
		ID:          id,
		Name:        name,
		Color:       color,
		Description: storage.StringToNullable(description),
	}); err != nil {
		if isUniqueViolation(err, "calendars.name") {
			return Calendar{}, ErrDuplicateName
		}
		return Calendar{}, err
	}

	if existing.AccountID != nil && existing.Color != color {
		if err := qtx.MarkCalendarColorDirty(ctx, id); err != nil {
			return Calendar{}, err
		}
	}

	updated, err := qtx.GetCalendar(ctx, id)
	if err != nil {
		return Calendar{}, err
	}
	if err := tx.Commit(); err != nil {
		return Calendar{}, err
	}
	return fromStorage(updated), nil
}

func (s *Service) SetOwnerEmail(ctx context.Context, id int64, email string) error {
	return s.q.UpdateCalendarOwnerEmail(ctx, storage.UpdateCalendarOwnerEmailParams{
		OwnerEmail: email,
		ID:         id,
	})
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.deleteWithOptionalPromotion(ctx, id, 0)
}

// DeleteAndPromote deletes a calendar and, when it is the current default,
// promotes newDefaultID in the same transaction so the database is never
// observed without a default. Pass newDefaultID = 0 when the target is not
// the default; the call then behaves exactly like Delete.
func (s *Service) DeleteAndPromote(ctx context.Context, id, newDefaultID int64) error {
	return s.deleteWithOptionalPromotion(ctx, id, newDefaultID)
}

func (s *Service) deleteWithOptionalPromotion(ctx context.Context, id, newDefaultID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	qtx := s.q.WithTx(tx)

	// Return not-found before the count guard so callers can distinguish
	// a missing ID from the last-calendar constraint.
	target, err := qtx.GetCalendar(ctx, id)
	if err != nil {
		return err
	}

	count, err := qtx.CountCalendars(ctx)
	if err != nil {
		return fmt.Errorf("count calendars: %w", err)
	}
	if count <= 1 {
		return ErrLastCalendar
	}

	if target.IsDefault == 1 {
		if newDefaultID == 0 {
			return ErrDefaultCalendarRequiresPromotion
		}
		if newDefaultID == id {
			return ErrInvalidPromotionTarget
		}
		if _, err := qtx.GetCalendar(ctx, newDefaultID); err != nil {
			return ErrInvalidPromotionTarget
		}
	}

	if err := qtx.DeleteCalendar(ctx, id); err != nil {
		return fmt.Errorf("delete calendar: %w", err)
	}

	if target.IsDefault == 1 {
		if err := qtx.ClearDefaultCalendar(ctx); err != nil {
			return fmt.Errorf("clear default: %w", err)
		}
		if err := qtx.SetCalendarAsDefault(ctx, newDefaultID); err != nil {
			return fmt.Errorf("promote default: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete calendar: %w", err)
	}
	return nil
}

// SetDefault makes id the only default calendar. It is idempotent and
// transactional so the partial unique index never sees two defaults at once.
func (s *Service) SetDefault(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	qtx := s.q.WithTx(tx)
	if _, err := qtx.GetCalendar(ctx, id); err != nil {
		return err
	}
	if err := qtx.ClearDefaultCalendar(ctx); err != nil {
		return fmt.Errorf("clear default: %w", err)
	}
	if err := qtx.SetCalendarAsDefault(ctx, id); err != nil {
		return fmt.Errorf("set default: %w", err)
	}
	return tx.Commit()
}

// GetDefault returns the current default calendar, or sql.ErrNoRows if no
// default exists. A live database should always have exactly one default
// because Create promotes the first calendar automatically; callers that
// hit ErrNoRows are looking at a database in an inconsistent state.
func (s *Service) GetDefault(ctx context.Context) (Calendar, error) {
	r, err := s.q.GetDefaultCalendar(ctx)
	if err != nil {
		return Calendar{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) ListByAccount(ctx context.Context, accountID int64) ([]Calendar, error) {
	rows, err := s.q.ListCalendarsByAccount(ctx, &accountID)
	if err != nil {
		return nil, err
	}
	cals := make([]Calendar, len(rows))
	for i, r := range rows {
		cals[i] = fromStorage(r)
	}
	return cals, nil
}

func (s *Service) UpdateSyncState(ctx context.Context, id int64, ctag, syncToken string) error {
	return s.q.UpdateCalendarSyncState(ctx, storage.UpdateCalendarSyncStateParams{
		ID:        id,
		Ctag:      storage.StringToNullable(ctag),
		SyncToken: storage.StringToNullable(syncToken),
	})
}

func (s *Service) LinkToAccount(ctx context.Context, id, accountID int64, remoteURL string) error {
	return s.q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        id,
		AccountID: &accountID,
		RemoteUrl: storage.StringToNullable(remoteURL),
	})
}

func (s *Service) UnlinkFromAccount(ctx context.Context, id int64) error {
	return s.q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: id,
	})
}

func (s *Service) UpdateColorFromSync(ctx context.Context, id int64, localColor, remoteColor string) error {
	return s.q.UpdateCalendarColorFromSync(ctx, storage.UpdateCalendarColorFromSyncParams{
		ID:          id,
		Color:       localColor,
		RemoteColor: storage.StringToNullable(remoteColor),
	})
}

func (s *Service) ClearColorDirty(ctx context.Context, id int64, remoteColor string) error {
	return s.q.ClearCalendarColorDirty(ctx, storage.ClearCalendarColorDirtyParams{
		ID:          id,
		RemoteColor: storage.StringToNullable(remoteColor),
	})
}

func fromStorage(r storage.Calendar) Calendar {
	var accountID int64
	if r.AccountID != nil {
		accountID = *r.AccountID
	}
	return Calendar{
		ID:                  r.ID,
		Name:                r.Name,
		Color:               r.Color,
		Description:         storage.NullableToString(r.Description),
		OwnerEmail:          r.OwnerEmail,
		DisplayOrder:        r.DisplayOrder,
		CreatedAt:           timeutil.ParseDateTime(r.CreatedAt),
		UpdatedAt:           timeutil.ParseDateTime(r.UpdatedAt),
		AccountID:           accountID,
		RemoteURL:           storage.NullableToString(r.RemoteUrl),
		CTag:                storage.NullableToString(r.Ctag),
		SyncToken:           storage.NullableToString(r.SyncToken),
		LastSyncAt:          storage.NullableToString(r.LastSyncAt),
		LastSyncAttemptedAt: storage.NullableToString(r.LastSyncAttemptedAt),
		LastSyncError:       storage.NullableToString(r.LastSyncError),
		RemoteColor:         storage.NullableToString(r.RemoteColor),
		ColorDirty:          r.ColorDirty != 0,
		RemoteName:          r.RemoteName,
		RemoteAccess:        r.RemoteAccess,
		RemoteComponents:    r.RemoteComponents,
		RemoteMissing:       r.RemoteMissing != 0,
		IsDefault:           r.IsDefault != 0,
	}
}

// isUniqueViolation returns true when the error is a SQLite UNIQUE constraint
// violation on the given column (e.g. "calendars.name").
func isUniqueViolation(err error, column string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") && strings.Contains(msg, column)
}
