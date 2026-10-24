package todo

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/recurset"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// ErrOccurrenceInTrash reports an edit of an occurrence whose override row
// is in the trash. The user restores the row first (issue #887).
var ErrOccurrenceInTrash = errors.New("todo: the override of this occurrence is in the trash; restore it first")

// ErrOverrideExists reports a create of an override at an occurrence that
// already has a live override row. The caller edits that row by its ID.
var ErrOverrideExists = errors.New("todo: the occurrence already has an override")

// ErrNotOccurrence reports a recurrence ID that is not an occurrence of the
// series: a todo that does not recur, a cancelled series, a time off the
// rule, or a time that an EXDATE excludes.
var ErrNotOccurrence = errors.New("todo: the time is not an occurrence of the series")

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
// todo uid (issue #887). recurrenceID is the time of the occurrence. p holds
// the fields of the override row. The caller fills p from the generated
// occurrence (recurrence.TodoOccurrence) and applies the edit. The override
// also takes the alarms, the attendees, and the other child rows of the
// master, as the occurrence had them. The master does not change.
//
// The rules:
//   - recurrenceID must be an occurrence of the series (ErrNotOccurrence).
//   - The override lives in the calendar of the master. A p.CalendarID of
//     another calendar returns ErrInstanceCalendarMove.
//   - An override row at the occurrence returns ErrOverrideExists when it
//     is live, and ErrOccurrenceInTrash when it is in the trash.
//
// The work joins an outer transaction of a service bound with WithTx.
func (s *Service) CreateOverride(ctx context.Context, uid, recurrenceID string, p UpdateParams) (Todo, error) {
	if err := p.normalize(); err != nil {
		return Todo{}, err
	}
	var t Todo
	err := s.inTx(ctx, func(txs *Service) error {
		var err error
		t, err = txs.createOverride(ctx, uid, recurrenceID, p)
		return err
	})
	if err != nil {
		return Todo{}, err
	}
	return t, nil
}

// DeleteInstance deletes one occurrence of the recurring todo uid that has
// no override row (issue #887). It writes the override as CreateOverride
// does, and it then deletes that row as Delete does: an EXDATE on the
// master, the record of the EXDATE, and the soft-delete of the row. One
// transaction holds the writes. The trash then shows the occurrence, and
// RestoreByID brings it back. It returns the override row in the trash.
func (s *Service) DeleteInstance(ctx context.Context, uid, recurrenceID string, p UpdateParams) (Todo, error) {
	if err := p.normalize(); err != nil {
		return Todo{}, err
	}
	var t Todo
	err := s.inTx(ctx, func(txs *Service) error {
		var err error
		if t, err = txs.createOverride(ctx, uid, recurrenceID, p); err != nil {
			return err
		}
		return deleteOverrideTx(ctx, txs.q, t)
	})
	if err != nil {
		return Todo{}, err
	}
	return t, nil
}

// inTx runs fn with a service bound to one transaction. It joins the
// transaction of a service bound with WithTx. Otherwise it opens a
// transaction, and it commits it when fn returns no error.
func (s *Service) inTx(ctx context.Context, fn func(txs *Service) error) error {
	if s.tx != nil {
		return fn(s)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := fn(s.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

// OccursAt reports whether the series of master has an occurrence at
// recurrenceID. EXDATEs apply. A cancelled series has no occurrence. It
// uses the anchor of the expansion: DTSTART, else DUE. CreateOverride and
// recurrence.TodoOccurrence share this one rule.
func OccursAt(master Todo, recurrenceID string) bool {
	if master.RecurrenceRule == "" && master.RDates == "" {
		return false
	}
	if master.RecurrenceRule != "" && strings.EqualFold(master.Status, "CANCELLED") {
		return false
	}
	anchor := master.ParseStartDate()
	if anchor.IsZero() {
		anchor = master.ParseDueDate()
	}
	if anchor.IsZero() {
		return false
	}
	rs, ok := recurset.New(master.RecurrenceRule, master.Timezone, anchor, 0, master.ParseExDates(), master.ParseRDates())
	return ok && rs.OccursAt(recurrenceID)
}

// createOverride writes the override row of the occurrence recurrenceID of
// the series uid from p, with the child rows of the master. s is bound to
// the transaction. See CreateOverride for the rules.
func (s *Service) createOverride(ctx context.Context, uid, recurrenceID string, p UpdateParams) (Todo, error) {
	qtx := s.q
	at, err := timeutil.ParseRecurrenceID(recurrenceID)
	if err != nil {
		return Todo{}, fmt.Errorf("parse recurrence_id %q: %w", recurrenceID, err)
	}
	recurrenceID = at.UTC().Format(time.RFC3339)
	row, err := qtx.GetTodoByUID(ctx, uid)
	if err != nil {
		return Todo{}, err
	}
	master := fromStorage(row)
	// A row of the occurrence comes first: the delete of an override also
	// excludes its occurrence, so the occurrence check would hide the cause.
	_, trashed, found, err := overrideRow(ctx, qtx, uid, recurrenceID)
	switch {
	case err != nil:
		return Todo{}, fmt.Errorf("check override: %w", err)
	case found && trashed:
		return Todo{}, ErrOccurrenceInTrash
	case found:
		return Todo{}, ErrOverrideExists
	}
	if !OccursAt(master, recurrenceID) {
		return Todo{}, ErrNotOccurrence
	}

	// The access checks, the move rules, and the dirty mark of the series
	// run in the order of uidmove.Rows.Update, as in Update. The rules
	// refuse a move of one occurrence (ErrInstanceCalendarMove).
	var t Todo
	err = uidMoveRows(qtx, uid).Update(ctx, todoComponent, "todo", recurrenceID, master.CalendarID, p.CalendarID, func(ctx context.Context, calendarID int64) error {
		var err error
		t, err = insertOverrideTx(ctx, qtx, uid, recurrenceID, calendarID, master.Sequence+1, p)
		return err
	})
	if err != nil {
		return Todo{}, err
	}
	if err := s.copyChildRows(ctx, master, t.ID); err != nil {
		return Todo{}, err
	}
	return t, nil
}

// copyChildRows gives the override overrideID the child rows of master:
// the alarms, the attendees, the attachments, the comments, the contacts,
// the resources, the relations, and the x-properties. A copy of an alarm
// gets a new UID, because an alarm UID names one alarm. s is bound to the
// transaction of the override.
func (s *Service) copyChildRows(ctx context.Context, master Todo, overrideID int64) error {
	if err := s.Hydrate(ctx, &master); err != nil {
		return fmt.Errorf("read the child rows of the series: %w", err)
	}
	alarms := slices.Clone(master.Alarms)
	for i := range alarms {
		alarms[i].ID, alarms[i].UID = 0, ""
	}
	attendees := slices.Clone(master.Attendees)
	for i := range attendees {
		attendees[i].ID = 0
	}
	steps := []struct {
		name string
		run  func() error
	}{
		{"alarms", func() error { return s.ReplaceAlarms(ctx, overrideID, alarms) }},
		{"attendees", func() error { return s.ReplaceAttendees(ctx, overrideID, attendees) }},
		{"attachments", func() error { return s.ReplaceAttachments(ctx, overrideID, master.Attachments) }},
		{"comments", func() error { return s.ReplaceComments(ctx, overrideID, master.Comments) }},
		{"contacts", func() error { return s.ReplaceContacts(ctx, overrideID, master.Contacts) }},
		{"resources", func() error { return s.ReplaceResources(ctx, overrideID, master.Resources) }},
		{"relations", func() error { return s.ReplaceRelations(ctx, overrideID, master.Relations) }},
		{"x-properties", func() error { return s.ReplaceXProperties(ctx, overrideID, master.XProperties) }},
	}
	for _, step := range steps {
		if err := step.run(); err != nil {
			return fmt.Errorf("copy %s: %w", step.name, err)
		}
	}
	return nil
}

// insertOverrideTx inserts the override row and its categories.
func insertOverrideTx(ctx context.Context, qtx *storage.Queries, uid, recurrenceID string, calendarID, sequence int64, p UpdateParams) (Todo, error) {
	r, err := qtx.CreateTodo(ctx, storage.CreateTodoParams{
		Uid:             uid,
		CalendarID:      calendarID,
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
		Sequence:     sequence,
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
