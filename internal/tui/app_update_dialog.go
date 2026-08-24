package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
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
			// Validate the scope against the master's raw rule set before
			// touching storage. The deletion key is the original
			// RECURRENCE-ID, never a moved override's display start.
			scopeAt, scopeErr := recurrence.ScopeInstanceTime(ev)
			if scopeErr == nil {
				master, mErr := m.app.Events.GetByUID(context.Background(), ev.UID)
				if mErr != nil {
					scopeErr = mErr
				} else {
					switch msg.Choice {
					case 0:
						if !recurrence.OccurrenceExistsAt(master, scopeAt) {
							scopeErr = fmt.Errorf("no occurrence matches %s", scopeAt.Format(time.RFC3339))
						}
					case 1:
						if !recurrence.HasOccurrenceFrom(master, scopeAt) {
							scopeErr = fmt.Errorf("no occurrences at or after %s", scopeAt.Format(time.RFC3339))
						}
					}
				}
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
