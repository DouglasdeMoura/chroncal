package todo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// ErrOccurrenceInTrash reports an edit of an occurrence whose override row
// is in the trash. The user restores the row first (issue #887).
var ErrOccurrenceInTrash = errors.New("todo: the override of this occurrence is in the trash; restore it first")

// ErrOverrideExists reports a create of an override at an occurrence that
// already has a live override row. The caller edits that row by its ID.
var ErrOverrideExists = errors.New("todo: the occurrence already has an override")

// OverrideRow returns the override row of the occurrence recurrenceID of
// the series uid, live or in the trash. found is false when no row exists.
// See overrideRow for the spellings of recurrenceID that it finds.
func (s *Service) OverrideRow(ctx context.Context, uid, recurrenceID string) (id int64, trashed, found bool, err error) {
	return overrideRow(ctx, s.q, uid, recurrenceID)
}

// overrideRow looks up the override row of the occurrence recurrenceID. A
// stored RECURRENCE-ID can have another spelling of the same time, so the
// lookup takes the exact recurrenceID, its UTC RFC 3339 form, and, for a
// time at UTC midnight, its date-only form.
func overrideRow(ctx context.Context, q *storage.Queries, uid, recurrenceID string) (id int64, trashed, found bool, err error) {
	ids := []string{recurrenceID}
	if at, err := timeutil.ParseRecurrenceID(recurrenceID); err == nil {
		at = at.UTC()
		ids = append(ids, at.Format(time.RFC3339))
		if at.Equal(at.Truncate(24 * time.Hour)) {
			ids = append(ids, at.Format("2006-01-02"))
		}
	}
	seen := map[string]bool{}
	for _, rid := range ids {
		if seen[rid] {
			continue
		}
		seen[rid] = true
		row, err := q.GetTodoCalendarByUIDAndRecurrenceID(ctx, storage.GetTodoCalendarByUIDAndRecurrenceIDParams{Uid: uid, RecurrenceID: rid})
		found, err := storage.Found(err)
		if err != nil {
			return 0, false, false, err
		}
		if found {
			return row.ID, row.Trashed == 1, true, nil
		}
	}
	return 0, false, false, nil
}

// CreateOverride creates the override of one occurrence of the recurring
// todo uid (issue #887). recurrenceID is the UTC RFC 3339 time of the
// occurrence. p holds every field of the override. The caller fills p from
// the generated occurrence (recurrence.TodoOccurrence) and applies the
// edit. The master does not change.
//
// The override lives in the calendar of the master. A p.CalendarID of
// another calendar returns ErrInstanceCalendarMove. An override row at the
// occurrence returns ErrOverrideExists when it is live, and
// ErrOccurrenceInTrash when it is in the trash.
func (s *Service) CreateOverride(ctx context.Context, uid, recurrenceID string, p UpdateParams) (Todo, error) {
	p.Status, p.Class = defaults(p.Status, p.Class)
	p.CompletedAt = completedAtFor(p.Status, p.CompletedAt)
	p.PercentComplete = percentCompleteFor(p.Status, p.PercentComplete)
	if err := validateTiming(p.DueDate, p.StartDate, p.Duration); err != nil {
		return Todo{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Todo{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	t, err := createOverrideTx(ctx, qtx, uid, recurrenceID, p)
	if err != nil {
		return Todo{}, err
	}
	// The override changes the server object of the series, so the series
	// gets the dirty mark in the same transaction (issue #107).
	if err := storage.MarkResourceDirty(ctx, tx, t.CalendarID, uid, "todo"); err != nil {
		return Todo{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Todo{}, fmt.Errorf("commit override: %w", err)
	}
	return t, nil
}

// DeleteInstance deletes one occurrence of the recurring todo uid that has
// no override row (issue #887). It writes the override row from p, and it
// then deletes that row as Delete does: an EXDATE on the master, the record
// of the EXDATE, and the soft-delete of the row. One transaction holds the
// three writes. The trash then shows the occurrence, and RestoreByID brings
// it back. It returns the override row in the trash.
func (s *Service) DeleteInstance(ctx context.Context, uid, recurrenceID string, p UpdateParams) (Todo, error) {
	p.Status, p.Class = defaults(p.Status, p.Class)
	p.CompletedAt = completedAtFor(p.Status, p.CompletedAt)
	p.PercentComplete = percentCompleteFor(p.Status, p.PercentComplete)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Todo{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	t, err := createOverrideTx(ctx, qtx, uid, recurrenceID, p)
	if err != nil {
		return Todo{}, err
	}
	if err := deleteOverrideTx(ctx, tx, qtx, t); err != nil {
		return Todo{}, err
	}
	if err := tx.Commit(); err != nil {
		return Todo{}, fmt.Errorf("commit occurrence delete: %w", err)
	}
	return t, nil
}

// createOverrideTx writes the override row of the occurrence recurrenceID
// of the series uid from p. See CreateOverride for the rules.
func createOverrideTx(ctx context.Context, qtx *storage.Queries, uid, recurrenceID string, p UpdateParams) (Todo, error) {
	at, err := timeutil.ParseRecurrenceID(recurrenceID)
	if err != nil {
		return Todo{}, fmt.Errorf("parse recurrence_id %q: %w", recurrenceID, err)
	}
	recurrenceID = at.UTC().Format(time.RFC3339)
	master, err := qtx.GetTodoByUID(ctx, uid)
	if err != nil {
		return Todo{}, err
	}
	if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, todoComponent); err != nil {
		return Todo{}, err
	}
	if p.CalendarID != 0 && p.CalendarID != master.CalendarID {
		return Todo{}, ErrInstanceCalendarMove
	}
	_, trashed, found, err := overrideRow(ctx, qtx, uid, recurrenceID)
	switch {
	case err != nil:
		return Todo{}, fmt.Errorf("check override: %w", err)
	case found && trashed:
		return Todo{}, ErrOccurrenceInTrash
	case found:
		return Todo{}, ErrOverrideExists
	}

	r, err := qtx.CreateTodo(ctx, storage.CreateTodoParams{
		Uid:             uid,
		CalendarID:      master.CalendarID,
		Summary:         p.Summary,
		Description:     storage.StringToNullable(p.Description),
		Location:        storage.StringToNullable(p.Location),
		DueDate:         storage.StringToNullable(p.DueDate),
		StartDate:       storage.StringToNullable(p.StartDate),
		Duration:        storage.StringToNullable(p.Duration),
		CompletedAt:     storage.StringToNullable(p.CompletedAt),
		PercentComplete: p.PercentComplete,
		Status:          p.Status,
		Priority:        p.Priority,
		Class:           p.Class,
		Url:             storage.StringToNullable(p.URL),
		// An override is one occurrence. It has no rule and no date lists.
		Timezone:     storage.StringToNullable(p.Timezone),
		Sequence:     master.Sequence + 1,
		RecurrenceID: recurrenceID,
		Geo:          storage.StringToNullable(p.Geo),
		Dtstamp:      storage.StringToNullable(p.DtStamp),
	})
	if err != nil {
		return Todo{}, fmt.Errorf("create override: %w", err)
	}
	t := fromStorage(r)
	if err := replaceCategoriesTx(ctx, qtx, t.ID, timeutil.ParseCategoryList(p.Categories)); err != nil {
		return Todo{}, fmt.Errorf("replace categories: %w", err)
	}
	t.Categories = p.Categories
	return t, nil
}
