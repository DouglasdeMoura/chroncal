package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/douglasdemoura/chroncal/internal/event"
)

func (m Model) handleEventsLoaded(msg eventsLoadedMsg) (tea.Model, tea.Cmd) {
	// Guard against a stale load. Rapid navigation (e.g. repeated [/] in
	// the agenda) fires multiple in-flight queries. Drop any whose range
	// no longer matches the active view. A late stale response then cannot
	// overwrite correct rows with an empty set.
	//
	// For full (merge=false) responses the query range must equal the
	// current expected range exactly. For incremental (merge=true)
	// responses the queried slice must lie inside the expected range
	// and abut the currently-loaded range. The append is then meaningful.
	// Otherwise the user has since jumped elsewhere.
	expectedFrom, expectedTo := m.expectedEventRange()
	if msg.merge {
		if msg.from.Before(expectedFrom) || msg.to.After(expectedTo) {
			return m, nil
		}
		if !msg.from.Equal(m.loadedTo) && !msg.to.Equal(m.loadedFrom) {
			return m, nil
		}
	} else if !msg.from.Equal(expectedFrom) || !msg.to.Equal(expectedTo) {
		return m, nil
	}
	m.err = msg.err
	if msg.merge {
		m.events = mergeEvents(m.events, msg.events)
		if msg.from.Before(m.loadedFrom) {
			m.loadedFrom = msg.from
		}
		if msg.to.After(m.loadedTo) {
			m.loadedTo = msg.to
		}
	} else {
		m.events = msg.events
		m.loadedFrom = msg.from
		m.loadedTo = msg.to
	}
	calEvents := eventsToCalendar(msg.events, m.calendars, m.hiddenCalendars)
	switch m.viewMode {
	case viewDay:
		m.day = m.day.SetEvents(calEvents)
	case viewWeek:
		m.week = m.week.SetEvents(calEvents)
	case viewAgenda:
		// Pass m.events (the merged cache), not msg.events. msg.events
		// is only the delta for incremental responses. Otherwise a
		// merge would rebuild the agenda rows with only the new
		// slice. The events shown before would vanish.
		m.agenda = m.agenda.SetEvents(filterVisibleEvents(m.events, m.hiddenCalendars), m.calendars)
	default:
		m.calendar = m.calendar.SetEvents(calEvents)
	}
	if m.dialogOpen {
		dayEvents := eventsOn(filterVisibleEvents(m.events, m.hiddenCalendars), m.dialog.day)
		m.dialog = m.dialog.SetEvents(dayEvents)
	}
	// After an agenda load lands, pull in the next month if the loaded
	// rows do not fill the viewport. That prevents a stare at blank
	// space below a sparse month until the user navigates.
	var cmds []tea.Cmd
	if m.viewMode == viewAgenda {
		if cmd := m.agenda.MaybeFillViewport(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if m.pendingOpenEvent.ID != 0 {
		matched := matchOpenEvent(m.events, m.pendingOpenEvent)
		m.agenda = m.agenda.SelectEvent(matched.ID, matched.StartTime)
		openEv := matched
		m.pendingOpenEvent = event.Event{}
		cmds = append(cmds, func() tea.Msg { return EventViewRequestedMsg{Event: openEv} })
	}
	return m, tea.Batch(cmds...)
}

func (m Model) handleCalendarDaySelected(msg CalendarDaySelectedMsg) (tea.Model, tea.Cmd) {
	dayEvents := eventsOn(filterVisibleEvents(m.events, m.hiddenCalendars), msg.Day)
	if m.clickedEventID > 0 {
		clicked := m.clickedEventID
		m.clickedEventID = 0
		for _, e := range dayEvents {
			if e.ID == clicked {
				ev := e
				return m, func() tea.Msg { return EventViewRequestedMsg{Event: ev} }
			}
		}
	}
	m.dialog = NewEventDialogModel(msg.Day, dayEvents, m.calendars, newThemedHelp(m.theme)).
		SetSelectedColor(m.theme.Selected).
		SetSize(m.width, m.height)
	m.dialogOpen = true
	return m, nil
}

func (m Model) handleEventEdit(msg EventEditMsg) (tea.Model, tea.Cmd) {
	if cmd, blocked := m.blockReadOnlyCalendarMutation(msg.Event.CalendarID); blocked {
		return m, cmd
	}
	ev := msg.Event
	// ev.StartTime carries the clicked occurrence's time for recurring
	// events (queryEventsRange overwrites it with InstanceTime). The
	// fresh Get below returns the master row, which has the original
	// DTSTART. Capture the clicked instance time first. Pass it
	// alongside so the form can prompt for scope on save.
	instanceTime := ev.StartTime
	instanceEnd := ev.EndTime
	return m, func() tea.Msg {
		ctx := context.Background()
		fresh, err := m.app.Events.Get(ctx, ev.ID)
		if err != nil {
			return eventEditLoadedMsg{err: err}
		}
		attendees, err := m.app.Events.ListAttendees(ctx, ev.ID)
		if err != nil {
			return eventEditLoadedMsg{err: err}
		}
		fresh.Attendees = attendees
		// Load the alarms too. A save writes the form list over the
		// stored rows, so an empty list deletes every alarm of the
		// event, including a preserved sync-only one (issue #579).
		alarms, err := m.app.Events.ListAlarms(ctx, ev.ID)
		if err != nil {
			return eventEditLoadedMsg{err: err}
		}
		fresh.Alarms = alarms
		// For a recurring instance, overwrite the master's DTSTART with
		// the clicked occurrence. The form's date/time fields then
		// reflect what the user actually clicked, not the original
		// series start.
		if fresh.RecurrenceRule != "" && !instanceTime.IsZero() {
			fresh.StartTime = instanceTime
			if !instanceEnd.IsZero() {
				fresh.EndTime = instanceEnd
			}
			return eventEditLoadedMsg{event: fresh, instanceTime: instanceTime}
		}
		return eventEditLoadedMsg{event: fresh}
	}
}

func (m Model) handleEventEditLoaded(msg eventEditLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	form, cmd := NewEventFormModelForEditInstance(msg.event, msg.instanceTime, eventFormCalendars(m.calendars), m.theme)
	m.dialogOpen = false
	if m.viewDialogOpen {
		m.viewReturnEvent = msg.event
	}
	m.viewDialogOpen = false
	return m.openEventForm(form, cmd)
}

func (m Model) handleEventViewRequested(msg EventViewRequestedMsg) (tea.Model, tea.Cmd) {
	ev := msg.Event
	return m, func() tea.Msg {
		ctx := context.Background()
		fresh, err := m.app.Events.Get(ctx, ev.ID)
		if err != nil {
			return eventViewLoadedMsg{err: err}
		}
		attendees, err := m.app.Events.ListAttendees(ctx, ev.ID)
		if err != nil {
			return eventViewLoadedMsg{err: err}
		}
		fresh.Attendees = attendees
		alarms, err := m.app.Events.ListAlarms(ctx, ev.ID)
		if err != nil {
			return eventViewLoadedMsg{err: err}
		}
		fresh.Alarms = alarms
		fresh = applyOccurrenceTimes(fresh, ev)
		return eventViewLoadedMsg{event: fresh}
	}
}

func (m Model) handleEventViewLoaded(msg eventViewLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	cal := m.calendars[msg.event.CalendarID]
	m.viewDialog = NewEventViewDialogModel(msg.event, cal, m.theme).
		SetSize(m.width, m.height)
	m.viewDialogOpen = true
	return m, nil
}

func (m Model) handleEventDuplicate(msg EventDuplicateMsg) (tea.Model, tea.Cmd) {
	form, cmd := NewEventFormModelForDuplicate(msg.Event, eventFormCalendars(m.calendars), m.theme)
	if m.viewDialogOpen {
		m.viewReturnEvent = msg.Event
	}
	m.viewDialogOpen = false
	return m.openEventForm(form, cmd)
}

func (m Model) handleEventFormSave(msg EventFormSaveMsg) (tea.Model, tea.Cmd) {
	if cmd, blocked := m.blockReadOnlyCalendarMutation(msg.CalendarID); blocked {
		return m, cmd
	}
	// editID is read from the live form model, not the message. The
	// form's OnSubmit closure is bound before NewEventFormModelForEdit
	// assigns editID, so EventFormSaveMsg cannot carry that value
	// reliably — see event_form.go:EventFormSaveMsg for the rationale.
	editID := m.form.editID
	m.formOpen = false
	attendees := msg.Attendees
	alarms := msg.Alarms
	// Editing one occurrence of a recurring series → defer the actual
	// write until the user picks a scope. The dialog dispatch
	// (ChoiceDialogResultMsg below) reads the armed save and routes
	// to UpdateInstance / UpdateFromInstance / Update.
	if editID > 0 && !msg.InstanceTime.IsZero() {
		return m.armChoice(
			pendingAction{
				kind:   pendingActionEditScope,
				target: pendingTarget{save: msg},
			},
			NewChoiceDialogModel(
				fmt.Sprintf("Update %q?", msg.Title),
				m.theme,
				"This event", "This and following", "All events",
			),
		), nil
	}
	if editID > 0 {
		eventID := editID
		calID := msg.CalendarID
		return m, func() tea.Msg {
			ctx := context.Background()
			// Update the row and its attendees/alarms in one transaction so a
			// failed child write rolls the whole edit back (issue #87).
			_, err := m.app.Events.UpdateWithRelations(ctx, eventID, event.UpdateParams{
				CalendarID:     calID,
				Title:          msg.Title,
				Description:    msg.Description,
				Location:       msg.Location,
				ConferenceURI:  msg.ConferenceURI,
				StartTime:      msg.StartTime,
				EndTime:        msg.EndTime,
				AllDay:         msg.AllDay,
				RecurrenceRule: msg.RecurrenceRule,
				Timezone:       msg.Timezone,
				Transp:         msg.Transp,
				Class:          msg.Class,
				Categories:     msg.Categories,
			}, attendees, alarms)
			return eventUpdatedMsg{calendarID: calID, err: err}
		}
	}
	calID := msg.CalendarID
	return m, func() tea.Msg {
		ctx := context.Background()
		created, err := m.app.Events.Create(ctx, event.CreateParams{
			CalendarID:     calID,
			Title:          msg.Title,
			Description:    msg.Description,
			Location:       msg.Location,
			ConferenceURI:  msg.ConferenceURI,
			StartTime:      msg.StartTime,
			EndTime:        msg.EndTime,
			AllDay:         msg.AllDay,
			RecurrenceRule: msg.RecurrenceRule,
			Timezone:       msg.Timezone,
			Transp:         msg.Transp,
			Class:          msg.Class,
			Categories:     msg.Categories,
		})
		if err != nil {
			return eventCreatedMsg{calendarID: calID, err: err}
		}
		if len(attendees) > 0 {
			if err = m.app.Events.ReplaceAttendees(ctx, created.ID, attendees); err != nil {
				return eventCreatedMsg{calendarID: calID, err: err}
			}
		}
		if len(alarms) > 0 {
			err = m.app.Events.ReplaceAlarms(ctx, created.ID, alarms)
		}
		return eventCreatedMsg{calendarID: calID, err: err}
	}
}

func (m Model) handleEventFormClosed(msg EventFormClosedMsg) (tea.Model, tea.Cmd) {
	m.formOpen = false
	if m.viewReturnEvent.ID != 0 {
		ev := m.viewReturnEvent
		m.viewReturnEvent = event.Event{}
		return m, func() tea.Msg { return EventViewRequestedMsg{Event: ev} }
	}
	return m, nil
}

func (m Model) handlePaletteSelected(msg PaletteSelectedMsg) (tea.Model, tea.Cmd) {
	m.paletteOpen = false
	if msg.Action == nil {
		return m, nil
	}
	action := msg.Action
	return m, func() tea.Msg { return action() }
}

func (m Model) handleEventUpdateAfterScope(msg eventUpdateAfterScopeMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		m.viewReturnEvent = event.Event{}
		return m, nil
	}
	cmds := []tea.Cmd{m.loadEvents()}
	if push := m.runOpportunisticPush(msg.calendarID); push != nil {
		cmds = append(cmds, push)
	}
	// On scope-dispatched edits we don't return to the prior view dialog;
	// the underlying row may have been replaced (e.g. "This and following"
	// creates a new master with a fresh UID).
	m.viewReturnEvent = event.Event{}
	return m, tea.Batch(cmds...)
}

func (m Model) handleEventCreated(msg eventCreatedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		m.viewReturnEvent = event.Event{}
		return m, nil
	}
	cmds := []tea.Cmd{m.loadEvents()}
	if push := m.runOpportunisticPush(msg.calendarID); push != nil {
		cmds = append(cmds, push)
	}
	if m.viewReturnEvent.ID != 0 {
		ev := m.viewReturnEvent
		m.viewReturnEvent = event.Event{}
		cmds = append(cmds, func() tea.Msg { return EventViewRequestedMsg{Event: ev} })
	}
	return m, tea.Batch(cmds...)
}

func (m Model) handleEventUpdated(msg eventUpdatedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		m.viewReturnEvent = event.Event{}
		return m, nil
	}
	cmds := []tea.Cmd{m.loadEvents()}
	if push := m.runOpportunisticPush(msg.calendarID); push != nil {
		cmds = append(cmds, push)
	}
	if m.viewReturnEvent.ID != 0 {
		ev := m.viewReturnEvent
		m.viewReturnEvent = event.Event{}
		cmds = append(cmds, func() tea.Msg { return EventViewRequestedMsg{Event: ev} })
	}
	return m, tea.Batch(cmds...)
}

func (m Model) handleEventRSVP(msg EventRSVPMsg) (tea.Model, tea.Cmd) {
	ev := msg.Event
	ownerEmail := m.calendars[ev.CalendarID].OwnerEmail
	return m, func() tea.Msg {
		ctx := context.Background()
		attendees, err := m.app.Events.ListAttendees(ctx, ev.ID)
		if err != nil {
			return eventRSVPUpdatedMsg{err: err}
		}
		for i, att := range attendees {
			if strings.EqualFold(att.Email, ownerEmail) {
				attendees[i].RSVPStatus = msg.Status
				break
			}
		}
		if err := m.app.Events.ReplaceAttendees(ctx, ev.ID, attendees); err != nil {
			return eventRSVPUpdatedMsg{err: err}
		}
		ev.Attendees = attendees
		return eventRSVPUpdatedMsg{event: ev}
	}
}

func (m Model) handleEventRSVPUpdated(msg eventRSVPUpdatedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	// Rebuild the open view dialog so the user sees their new RSVP
	// status immediately; loadEvents only repaints the grid behind it.
	if m.viewDialogOpen && m.viewDialog.event.ID == msg.event.ID {
		cal := m.calendars[msg.event.CalendarID]
		m.viewDialog = NewEventViewDialogModel(msg.event, cal, m.theme).
			SetSize(m.width, m.height)
	}
	return m, m.loadEvents()
}

func (m Model) handleDialogDayChanged(msg DialogDayChangedMsg) (tea.Model, tea.Cmd) {
	if m.viewMode == viewDay {
		prevDay := m.day.cursor.Format("2006-01-02")
		m.day.cursor = msg.Day
		if m.day.cursor.Format("2006-01-02") != prevDay {
			m.dialog = NewEventDialogModel(msg.Day, nil, m.calendars, newThemedHelp(m.theme)).
				SetSelectedColor(m.theme.Selected).
				SetSize(m.width, m.height)
			return m, m.loadEvents()
		}
		dayEvents := eventsOn(filterVisibleEvents(m.events, m.hiddenCalendars), msg.Day)
		m.dialog = NewEventDialogModel(msg.Day, dayEvents, m.calendars, newThemedHelp(m.theme)).
			SetSelectedColor(m.theme.Selected).
			SetSize(m.width, m.height)
		return m, nil
	}
	if m.viewMode == viewWeek {
		prevWeek := m.week.WeekStartDate()
		m.week.cursor = msg.Day
		if m.week.WeekStartDate() != prevWeek {
			m.dialog = NewEventDialogModel(msg.Day, nil, m.calendars, newThemedHelp(m.theme)).
				SetSelectedColor(m.theme.Selected).
				SetSize(m.width, m.height)
			return m, m.loadEvents()
		}
		dayEvents := eventsOn(filterVisibleEvents(m.events, m.hiddenCalendars), msg.Day)
		m.dialog = NewEventDialogModel(msg.Day, dayEvents, m.calendars, newThemedHelp(m.theme)).
			SetSelectedColor(m.theme.Selected).
			SetSize(m.width, m.height)
		return m, nil
	}
	m.calendar.cursor = msg.Day
	if msg.Day.Year() != m.calendar.month.Year() || msg.Day.Month() != m.calendar.month.Month() {
		m.calendar.month = time.Date(msg.Day.Year(), msg.Day.Month(), 1, 0, 0, 0, 0, msg.Day.Location())
		m.dialog = NewEventDialogModel(msg.Day, nil, m.calendars, newThemedHelp(m.theme)).
			SetSelectedColor(m.theme.Selected).
			SetSize(m.width, m.height)
		return m, m.loadEvents()
	}
	dayEvents := eventsOn(filterVisibleEvents(m.events, m.hiddenCalendars), msg.Day)
	m.dialog = NewEventDialogModel(msg.Day, dayEvents, m.calendars, newThemedHelp(m.theme)).
		SetSelectedColor(m.theme.Selected).
		SetSize(m.width, m.height)
	return m, nil
}

func (m Model) handleEventDelete(msg EventDeleteMsg) (tea.Model, tea.Cmd) {
	if cmd, blocked := m.blockReadOnlyCalendarMutation(msg.Event.CalendarID); blocked {
		return m, cmd
	}
	if msg.Event.RecurrenceRule != "" {
		return m.armChoice(
			pendingAction{
				kind:   pendingActionEventDeleteScope,
				target: pendingTarget{ev: msg.Event},
			},
			NewChoiceDialogModel(
				fmt.Sprintf("Delete %q?", msg.Event.Title),
				m.theme,
				"This event", "This and following", "All events",
			),
		), nil
	}
	return m.armConfirm(
		pendingAction{
			kind:   pendingActionEventDelete,
			target: pendingTarget{ev: msg.Event},
		},
		NewConfirmDialogModel(
			fmt.Sprintf("Delete %q?", msg.Event.Title),
			"Delete",
			m.theme,
		).Destructive(),
	), nil
}

func (m Model) handleTrashViewRequested(msg TrashViewRequestedMsg) (tea.Model, tea.Cmd) {
	if m.trashOpen {
		return m, nil
	}
	m.trashOpen = true
	m.trash = NewTrashModel(m.calendars, newThemedHelp(m.theme)).
		SetSelectedColor(m.theme.Selected).
		SetSize(m.width, m.height)
	return m, m.loadTrash()
}

func (m Model) handleEventDeleted(msg eventDeletedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	m.viewDialogOpen = false
	// Every soft-delete is reversible; push the undo entry unconditionally.
	// UID being empty means no row was actually deleted (defensive).
	if msg.meta.UID == "" {
		return m, tea.Batch(m.loadEvents(), m.runOpportunisticPush(msg.calendarID))
	}
	m.undoStack.Push(UndoEntry{
		Meta:      msg.meta,
		DeletedAt: time.Now(),
	})
	synced := false // opportunistic push hasn't run yet when toast shows
	toastCmd := m.toast.Deleted(msg.title, synced)
	m.pushDeferralToken++
	token := m.pushDeferralToken
	calID := msg.calendarID
	deferCmd := tea.Tick(ToastAutoDismissDelay, func(time.Time) tea.Msg {
		return deferredPushMsg{calendarID: calID, token: token}
	})
	return m, tea.Batch(m.loadEvents(), toastCmd, deferCmd)
}

func (m Model) handleDeferredPush(msg deferredPushMsg) (tea.Model, tea.Cmd) {
	// If a restore bumped the token between the delete and this tick,
	// the deferred push is stale — drop it.
	if msg.token != m.pushDeferralToken {
		return m, nil
	}
	if push := m.runOpportunisticPush(msg.calendarID); push != nil {
		return m, push
	}
	return m, nil
}

func (m Model) handleEventRestored(msg eventRestoredMsg) (tea.Model, tea.Cmd) {
	// The restore has landed; allow the next undo to dispatch.
	m.undoRestoreInFlight = false
	if msg.err != nil {
		// Route the dismiss tick — previously dropped, so failed toasts
		// never auto-cleared in the live app.
		cmd := m.toast.Failed(msg.err.Error())
		// Leave the entry on the stack so the user can retry with
		// different context (e.g. restoring the calendar first).
		return m, cmd
	}
	// Remove the entry that was actually restored, by identity, not the
	// current top. A delete that lands while the restore is in flight may
	// have pushed a newer entry. That entry's undo affordance must survive.
	m.undoStack.Remove(msg.meta)
	toastCmd := m.toast.Restored(msg.title)
	return m, tea.Batch(m.loadEvents(), toastCmd)
}

func (m Model) handleTrashLoaded(msg trashLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	m.trash = m.trash.SetEntries(msg.entries, m.calendars)
	return m, nil
}

func (m Model) handleTrashRestoreRequested(msg TrashRestoreRequestedMsg) (tea.Model, tea.Cmd) {
	entries := msg.Entries
	if len(entries) == 0 {
		return m, nil
	}
	title := trashBulkTitle(entries)
	return m, func() tea.Msg {
		for _, e := range entries {
			if err := m.app.Trash.Restore(context.Background(), e); err != nil {
				return trashActionDoneMsg{action: "restored", title: title, err: err}
			}
		}
		return trashActionDoneMsg{action: "restored", title: title, err: nil}
	}
}

func (m Model) handleTrashPurgeRequested(msg TrashPurgeRequestedMsg) (tea.Model, tea.Cmd) {
	if len(msg.Entries) == 0 {
		return m, nil
	}
	var message string
	if len(msg.Entries) == 1 {
		message = fmt.Sprintf("Purge %q forever? This can't be undone.", msg.Entries[0].Title)
	} else {
		message = fmt.Sprintf("Purge %d items forever? This can't be undone.", len(msg.Entries))
	}
	m = m.armConfirm(
		pendingAction{
			kind:   pendingActionPurgeEntries,
			target: pendingTarget{entries: msg.Entries},
			label:  trashBulkTitle(msg.Entries),
		},
		NewConfirmDialogModel(message, "Purge", m.theme).
			Destructive(),
	)
	return m, nil
}
func (m Model) handleTrashActionDone(msg trashActionDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		cmd := m.toast.Failed(msg.err.Error())
		return m, cmd
	}
	m.trash = m.trash.ClearMarks()
	cmds := []tea.Cmd{m.loadTrash(), m.loadEvents()}
	switch msg.action {
	case "restored":
		cmds = append(cmds, m.toast.Restored(msg.title))
	case "purged":
		cmds = append(cmds, m.toast.Purged(msg.title))
	}
	return m, tea.Batch(cmds...)
}
