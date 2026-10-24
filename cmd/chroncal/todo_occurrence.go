package main

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// resolveTodoOccurrence resolves a todo reference with an optional
// --recurrence-id (issue #887). With a recurrence ID and a series UID, it
// returns the override row of the occurrence. When the occurrence has no
// row yet, it returns the generated occurrence and create is true. The
// caller then writes the override with todo.Service.CreateOverride or
// todo.Service.DeleteInstance.
//
// The error names the cause: no todo of the UID, a todo that does not
// recur, a cancelled series, a time that is not an occurrence, or an
// override in the trash.
func resolveTodoOccurrence(ctx context.Context, a *app.App, ref, recurrenceID string) (t todo.Todo, create bool, err error) {
	if recurrenceID == "" {
		t, err := resolveTodo(ctx, a, ref, "")
		return t, false, err
	}
	if _, parseErr := strconv.ParseInt(ref, 10, 64); parseErr == nil {
		// resolveTodo refuses a numeric ID with a recurrence ID.
		t, err := resolveTodo(ctx, a, ref, recurrenceID)
		return t, false, err
	}
	id, trashed, found, err := a.Todos.OverrideRow(ctx, ref, recurrenceID)
	if err != nil {
		return todo.Todo{}, false, err
	}
	if found && trashed {
		// A restore of the row alone while its master is in the trash
		// gives an override with no series, so the hint names the series.
		switch _, err := a.Todos.GetByUID(ctx, ref); {
		case errors.Is(err, sql.ErrNoRows):
			return todo.Todo{}, false, errInvalidInputf("the series of todo %s is in the trash; \"chroncal todo restore %s\" brings it back", ref, ref)
		case err != nil:
			return todo.Todo{}, false, err
		}
		return todo.Todo{}, false, errInvalidInputf("the occurrence %s of todo %s is in the trash as todo %d; \"chroncal todo restore %d\" brings it back", recurrenceID, ref, id, id)
	}
	if found {
		t, err := a.Todos.Get(ctx, id)
		if err != nil {
			return todo.Todo{}, false, notFoundErrFor(err, "todo", ref, false)
		}
		return t, false, nil
	}
	// The lookup above takes a date-only RECURRENCE-ID too, so the create
	// takes the same forms.
	at, err := timeutil.ParseRecurrenceID(strings.TrimSpace(recurrenceID))
	if err != nil {
		return todo.Todo{}, false, errInvalidInputf("--recurrence-id: invalid time %q (use RFC 3339 or YYYY-MM-DD)", recurrenceID)
	}
	at = at.UTC()
	master, err := a.Todos.GetByUID(ctx, ref)
	if err != nil {
		return todo.Todo{}, false, notFoundErrFor(err, "todo", ref, false)
	}
	occ, ok := recurrence.TodoOccurrence(master, at)
	if !ok {
		if master.RecurrenceRule == "" && master.RDates == "" {
			return todo.Todo{}, false, errInvalidInputf("todo %s does not recur, so --recurrence-id does not apply", ref)
		}
		if strings.EqualFold(master.Status, "CANCELLED") {
			return todo.Todo{}, false, errInvalidInputf("the series of todo %s is cancelled, so it has no occurrences", ref)
		}
		return todo.Todo{}, false, errInvalidInputf("--recurrence-id %s is not an occurrence of todo %s", at.Format(time.RFC3339), ref)
	}
	return occ, true, nil
}

// occurrenceWriteErr gives an invalid-input error for the refusals of
// todo.Service.CreateOverride and DeleteInstance. A sync pull between the
// lookup and the write can cause them.
func occurrenceWriteErr(err error) error {
	for _, refusal := range []error{todo.ErrNotOccurrence, todo.ErrOverrideExists, todo.ErrOccurrenceInTrash} {
		if errors.Is(err, refusal) {
			return errInvalidInputf("%v", err)
		}
	}
	return err
}

// todoParamsOf returns the update parameters that keep every field of t.
func todoParamsOf(t todo.Todo) todo.UpdateParams {
	return todo.UpdateParams{
		Summary:         t.Summary,
		Description:     t.Description,
		Location:        t.Location,
		DueDate:         t.DueDate,
		StartDate:       t.StartDate,
		Duration:        t.Duration,
		CompletedAt:     t.CompletedAt,
		PercentComplete: t.PercentComplete,
		Status:          t.Status,
		CalendarID:      t.CalendarID,
		Priority:        t.Priority,
		Class:           t.Class,
		URL:             t.URL,
		Geo:             t.Geo,
		Categories:      t.Categories,
		RecurrenceRule:  t.RecurrenceRule,
		Timezone:        t.Timezone,
		ExDates:         t.ExDates,
		RDates:          t.RDates,
	}
}
