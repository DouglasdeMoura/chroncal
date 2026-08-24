package tui

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
)

func (m Model) handleChoiceDialogResult(msg ChoiceDialogResultMsg) (tea.Model, tea.Cmd) {
	m.choiceOpen = false
	act := m.pending
	m = m.clearPending()
	if msg.Choice < 0 {
		m.viewReturnEvent = event.Event{}
		return m, nil
	}
	switch act.kind {
	case pendingActionCalendarMoveAccount, pendingActionCalendarMoveCollection:
		return m.calendarMoveChoice(act, msg.Choice)
	case pendingActionAccountSelectionPromote:
		if msg.Choice >= len(act.target.defaultCands) ||
			act.target.selection == nil {
			return m, nil
		}
		candidate := act.target.defaultCands[msg.Choice]
		selection := act.target.selection
		if candidate.id != 0 {
			selection.params.NewDefaultID = candidate.id
		} else {
			selection.params.NewDefaultPath = candidate.path
		}
		return m.showAccountCalendarRemovalConfirmation(selection), nil
	case pendingActionCalendarPromote:
		if msg.Choice >= len(act.target.promoteCands) {
			return m, nil
		}
		promoteID := act.target.promoteCands[msg.Choice]
		promoteName := ""
		if info, ok := m.calendars[promoteID]; ok {
			promoteName = info.Name
		}
		id, name := act.target.calendarID, act.label
		m = m.armCalendarDeleteCount(id, promoteID, name)
		return m, func() tea.Msg {
			count, _ := m.app.Events.CountByCalendar(context.Background(), id)
			return calendarDeleteCountMsg{
				id: id, name: name, eventCount: count,
				promoteID: promoteID, promoteName: promoteName,
			}
		}
	case pendingActionEditScope:
		save := act.target.save
		editID := m.form.editID
		choice := msg.Choice
		return m, func() tea.Msg {
			return m.dispatchEditScope(editID, choice, save)
		}
	case pendingActionEventDeleteScope:
		ev := act.target.ev
		return m, func() tea.Msg {
			if verr := validateEventDeleteScope(context.Background(), m.app, ev, msg.Choice); verr != nil {
				return eventDeletedMsg{calendarID: ev.CalendarID, title: ev.Title, err: verr}
			}
			scopeAt, dErr := recurrence.ScopeInstanceTime(ev)
			if dErr != nil {
				return eventDeletedMsg{calendarID: ev.CalendarID, title: ev.Title,
					err: fmt.Errorf("invalid recurrence id %q: %w", ev.RecurrenceID, dErr)}
			}
			switch msg.Choice {
			case 0: // This event
				meta, err := m.app.Events.DeleteInstanceWithUndo(context.Background(), ev.UID, scopeAt)
				return eventDeletedMsg{
					calendarID: ev.CalendarID,
					meta:       meta,
					title:      ev.Title,
					err:        err,
				}
			case 1: // This and following
				meta, err := m.app.Events.DeleteFromInstanceWithUndo(context.Background(), ev.UID, scopeAt)
				return eventDeletedMsg{
					calendarID: ev.CalendarID,
					meta:       meta,
					title:      ev.Title,
					err:        err,
				}
			case 2: // All events
				meta, err := m.app.Events.DeleteSeriesWithUndo(context.Background(), ev.UID)
				return eventDeletedMsg{
					calendarID: ev.CalendarID,
					meta:       meta,
					title:      ev.Title,
					err:        err,
				}
			}
			return eventDeletedMsg{calendarID: ev.CalendarID}
		}
	default:
		return m, nil
	}
}

func (m Model) handleConfirmDialogResult(msg ConfirmDialogResultMsg) (tea.Model, tea.Cmd) {
	m.confirmOpen = false
	act := m.pending
	m = m.clearPending()
	if act.kind == pendingActionQuit {
		if msg.Confirmed {
			m.oauthFlow.Abort() // release any in-flight OAuth listener
			return m, tea.Quit
		}
		return m, nil
	}
	if !msg.Confirmed {
		return m, nil
	}
	switch act.kind {
	case pendingActionAccountRemove:
		m.calendarManagerOpen = false
		m.syncing = true
		m.syncStatus = "Removing account…"
		return m, tea.Batch(m.syncSpinner.Tick, m.removeAccount(act.target.accountID, act.label))
	case pendingActionAccountSelection:
		m.syncing = true
		m.syncStatus = "Applying calendar changes…"
		return m, tea.Batch(
			m.syncSpinner.Tick,
			m.reconcileAndSyncAccountCalendars(act.target.selection),
		)
	case pendingActionPurgeEntries:
		entries := act.target.entries
		title := act.label
		return m, func() tea.Msg {
			for _, e := range entries {
				if err := m.app.Trash.Purge(context.Background(), e); err != nil {
					return trashActionDoneMsg{action: "purged", title: title, err: err}
				}
			}
			return trashActionDoneMsg{action: "purged", title: title, err: nil}
		}
	case pendingActionCalendarKeepLocal:
		id := act.target.calendarID
		return m, func() tea.Msg {
			ctx := context.Background()
			cal, err := m.app.Calendars.Get(ctx, id)
			if err != nil {
				return calendarMutationDoneMsg{err: err}
			}
			credStore, _ := m.openCredentialStore()
			return calendarMutationDoneMsg{err: m.app.Calendars.Disconnect(ctx, cal, credStore)}
		}
	case pendingActionCalendarDelete:
		// Delete confirmed: close the edit dialog too.
		m.calendarManagerOpen = false
		id := act.target.calendarID
		newDefaultID := act.target.promoteID
		return m, func() tea.Msg {
			credStore, _ := m.openCredentialStore()
			err := m.app.Calendars.DeleteWithRemoteCleanup(context.Background(), id, newDefaultID, credStore)
			return calendarMutationDoneMsg{err: err}
		}
	case pendingActionEventDelete:
		ev := act.target.ev
		return m, func() tea.Msg {
			meta, err := m.app.Events.DeleteWithUndo(context.Background(), ev.ID)
			return eventDeletedMsg{
				calendarID: ev.CalendarID,
				meta:       meta,
				title:      ev.Title,
				err:        err,
			}
		}
	default:
		// Nothing destructive waits behind this confirm. No fallback
		// action fires.
		return m, nil
	}
}

// validateEventDeleteScope checks an instance-scoped recurring delete against
// the series master's raw rule set before any storage call runs. Whole-series
// deletes need no scope time and pass through. The deletion key is the
// original RECURRENCE-ID, never a moved override's display start; a live
// override at that RECURRENCE-ID stays deletable even when an imported EXDATE
// hides the master slot.
func validateEventDeleteScope(ctx context.Context, a *app.App, ev event.Event, choice int) error {
	if choice != 0 && choice != 1 {
		return nil
	}
	scopeAt, err := recurrence.ScopeInstanceTime(ev)
	if err != nil {
		return fmt.Errorf("invalid recurrence id %q: %w", ev.RecurrenceID, err)
	}
	master, err := a.Events.GetByUID(ctx, ev.UID)
	if err != nil {
		return fmt.Errorf("get series master: %w", err)
	}
	switch choice {
	case 0:
		if recurrence.OccurrenceExistsAt(master, scopeAt) {
			return nil
		}
		if _, oErr := a.Events.GetByUIDAndRecurrenceID(
			ctx, ev.UID, scopeAt.UTC().Format(time.RFC3339)); oErr == nil {
			return nil
		} else if !errors.Is(oErr, sql.ErrNoRows) {
			return fmt.Errorf("look up override: %w", oErr)
		}
	case 1:
		if recurrence.HasOccurrenceFrom(master, scopeAt) {
			return nil
		}
		has, hErr := a.Events.HasLiveOverrideFrom(ctx, ev.UID, scopeAt)
		if hErr != nil {
			return fmt.Errorf("check overrides: %w", hErr)
		}
		if has {
			return nil
		}
	}
	return fmt.Errorf("no occurrence matches %s", scopeAt.Format(time.RFC3339))
}
