package main

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// resolveJournalOccurrence resolves a journal reference with an optional
// --recurrence-id (issue #916). With a recurrence ID and a series UID, it
// returns the override row of the occurrence. When the occurrence has no
// row yet, it returns the generated occurrence and create is true. The
// caller then writes the override with journal.Service.CreateOverride or
// journal.Service.DeleteInstance.
//
// The error names the cause: no journal of the UID, a journal that does not
// recur, a cancelled series, a time that is not an occurrence, or an
// override in the trash.
func resolveJournalOccurrence(ctx context.Context, a *app.App, ref, recurrenceID string) (j journal.Journal, create bool, err error) {
	// A stray space must not miss the override row below. A value of
	// spaces only names no occurrence, so it must not fall back to the
	// series: a delete would then delete the whole series.
	if recurrenceID != "" {
		if recurrenceID = strings.TrimSpace(recurrenceID); recurrenceID == "" {
			return journal.Journal{}, false, errInvalidInputf("--recurrence-id: the value is empty")
		}
	}
	if recurrenceID == "" {
		j, err := resolveJournal(ctx, a, ref, "")
		return j, false, err
	}
	if _, parseErr := strconv.ParseInt(ref, 10, 64); parseErr == nil {
		// resolveJournal refuses a numeric ID with a recurrence ID.
		j, err := resolveJournal(ctx, a, ref, recurrenceID)
		return j, false, err
	}
	id, trashed, found, err := a.Journals.OverrideRow(ctx, ref, recurrenceID)
	if err != nil {
		return journal.Journal{}, false, err
	}
	if found && trashed {
		// A restore of the row alone while its master is in the trash
		// gives an override with no series, so the hint names the series.
		switch _, err := a.Journals.GetByUID(ctx, ref); {
		case errors.Is(err, sql.ErrNoRows):
			return journal.Journal{}, false, errInvalidInputf("the series of journal %s is in the trash; \"chroncal journal restore %s\" brings it back", ref, ref)
		case err != nil:
			return journal.Journal{}, false, err
		}
		return journal.Journal{}, false, errInvalidInputf("the occurrence %s of journal %s is in the trash as journal %d; \"chroncal journal restore %d\" brings it back", recurrenceID, ref, id, id)
	}
	if found {
		j, err := a.Journals.Get(ctx, id)
		if err != nil {
			return journal.Journal{}, false, notFoundErrFor(err, "journal", ref, false)
		}
		return j, false, nil
	}
	// The lookup above takes a date-only RECURRENCE-ID too, so the create
	// takes the same forms.
	at, err := timeutil.ParseRecurrenceID(recurrenceID)
	if err != nil {
		return journal.Journal{}, false, errInvalidInputf("--recurrence-id: invalid time %q (use RFC 3339 or YYYY-MM-DD)", recurrenceID)
	}
	at = at.UTC()
	master, err := a.Journals.GetByUID(ctx, ref)
	if err != nil {
		return journal.Journal{}, false, notFoundErrFor(err, "journal", ref, false)
	}
	occ, ok := recurrence.JournalOccurrence(master, at)
	if !ok {
		if master.RecurrenceRule == "" && master.RDates == "" {
			return journal.Journal{}, false, errInvalidInputf("journal %s does not recur, so --recurrence-id does not apply", ref)
		}
		if strings.EqualFold(master.Status, "CANCELLED") {
			return journal.Journal{}, false, errInvalidInputf("the series of journal %s is cancelled, so it has no occurrences", ref)
		}
		return journal.Journal{}, false, errInvalidInputf("--recurrence-id %s is not an occurrence of journal %s", at.Format(time.RFC3339), ref)
	}
	return occ, true, nil
}

// journalParamsOf returns the update parameters that keep every field of j.
func journalParamsOf(j journal.Journal) journal.UpdateParams {
	return journal.UpdateParams{
		Summary:        j.Summary,
		Description:    j.Description,
		StartDate:      j.StartDate,
		Status:         j.Status,
		CalendarID:     j.CalendarID,
		Class:          j.Class,
		URL:            j.URL,
		Categories:     j.Categories,
		RecurrenceRule: j.RecurrenceRule,
		Timezone:       j.Timezone,
		ExDates:        j.ExDates,
		RDates:         j.RDates,
	}
}
