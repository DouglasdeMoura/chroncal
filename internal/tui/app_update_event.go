package tui

import (
	"context"
	"errors"
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
	m.dialog = m.newDayDialog(msg.Day, dayEvents)
	m.dialogOpen = true
	return m, nil
}

func (m Model) handleEventEdit(msg EventEditMsg) (tea.Model, tea.Cmd) {
	if cmd, blocked := m.blockReadOnlyCalendarMutation("Edit", msg.Event.CalendarID); blocked {
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
	// An edit keeps the calendar of the event unless the user picks another
	// one, so the form always lists that calendar and needs no fallback.
	form, cmd := NewEventFormModelForEditInstance(msg.event, msg.instanceTime, eventEditFormCalendars(m.calendars, msg.event.CalendarID), m.theme)
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
	ev := msg.Event
	if !m.viewDialogOpen {
		// The day dialog and the agenda hold a display summary of the
		// alarms (issue #912). A duplicate from them copies no alarms, as
		// before. The event view loads the full alarms.
		ev.Alarms = nil
	}
	form, cmd := NewEventFormModelForDuplicate(ev, eventFormCalendars(m.calendars), m.theme, m.formDefaultCalendarID())
	if m.viewDialogOpen {
		m.viewReturnEvent = msg.Event
	}
	m.viewDialogOpen = false
	return m.openEventForm(form, cmd)
}

func (m Model) handleEventFormSave(msg EventFormSaveMsg) (tea.Model, tea.Cmd) {
	if cmd, blocked := m.blockReadOnlyCalendarMutation("Save", msg.CalendarID); blocked {
		return m, cmd
	}
	// editID is read from the live form model, not the message. The
	// form's OnSubmit closure is bound before NewEventFormModelForEdit
	// assigns editID, so EventFormSaveMsg cannot carry that value
	// reliably — see event_form.go:EventFormSaveMsg for the rationale.
	editID := m.form.editID
	fromCalendarID, uid := m.form.editCalendarID, m.form.editUID
	m.formOpen = false
	attendees := msg.Attendees
	alarms := msg.Alarms
	// Editing one occurrence of a recurring series → defer the actual
	// write until the user picks a scope. The dialog dispatch
	// (ChoiceDialogResultMsg below) reads the armed save and routes
	// to UpdateInstance / UpdateFromInstance / Update.
	if editID > 0 && !msg.InstanceTime.IsZero() {
		// One occurrence cannot move to another calendar (issue #828). A
		// save with a new calendar then gets no "This event" choice.
		// Each choice that stays then moves the series, so Cancel gets the
		// focus and a quick Enter cancels.
		options := []string{"This event", "This and following", "All events"}
		offset := 0
		if fromCalendarID != 0 && msg.CalendarID != fromCalendarID {
			options, offset = options[1:], 1
		}
		prompt := NewChoiceDialogModel(fmt.Sprintf("Update %q?", msg.Title), m.theme, options...)
		if offset > 0 {
			prompt = prompt.FocusCancel()
		}
		return m.armChoice(
			pendingAction{
				kind:   pendingActionEditScope,
				target: pendingTarget{save: msg, scopeOffset: offset},
			},
			prompt,
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
			return eventUpdatedMsg{calendarID: calID, fromCalendarID: fromCalendarID, uid: uid, err: err}
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
	if event.IsMoveRefusal(msg.err) {
		return m.reopenFormWithError(msg.err), nil
	}
	if msg.err != nil {
		m.err = msg.err
		m.viewReturnEvent = event.Event{}
		return m, nil
	}
	cmds := []tea.Cmd{m.loadEvents()}
	if push := m.runMovePush(msg.fromCalendarID, msg.calendarID, msg.uid); push != nil {
		cmds = append(cmds, push)
	}
	// A scope edit does not return to the view dialog. The row under the
	// edit can be replaced (see eventUpdatedMsg.scoped).
	ev := m.viewReturnEvent
	m.viewReturnEvent = event.Event{}
	if ev.ID != 0 && !msg.scoped {
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
			m.dialog = m.newDayDialog(msg.Day, nil)
			return m, m.loadEvents()
		}
		dayEvents := eventsOn(filterVisibleEvents(m.events, m.hiddenCalendars), msg.Day)
		m.dialog = m.newDayDialog(msg.Day, dayEvents)
		return m, nil
	}
	if m.viewMode == viewWeek {
		prevWeek := m.week.WeekStartDate()
		m.week.cursor = msg.Day
		if m.week.WeekStartDate() != prevWeek {
			m.dialog = m.newDayDialog(msg.Day, nil)
			return m, m.loadEvents()
		}
		dayEvents := eventsOn(filterVisibleEvents(m.events, m.hiddenCalendars), msg.Day)
		m.dialog = m.newDayDialog(msg.Day, dayEvents)
		return m, nil
	}
	m.calendar.cursor = msg.Day
	if msg.Day.Year() != m.calendar.month.Year() || msg.Day.Month() != m.calendar.month.Month() {
		m.calendar.month = time.Date(msg.Day.Year(), msg.Day.Month(), 1, 0, 0, 0, 0, msg.Day.Location())
		m.dialog = m.newDayDialog(msg.Day, nil)
		return m, m.loadEvents()
	}
	dayEvents := eventsOn(filterVisibleEvents(m.events, m.hiddenCalendars), msg.Day)
	m.dialog = m.newDayDialog(msg.Day, dayEvents)
	return m, nil
}

func (m Model) handleEventDelete(msg EventDeleteMsg) (tea.Model, tea.Cmd) {
	if cmd, blocked := m.blockReadOnlyCalendarMutation("Delete", msg.Event.CalendarID); blocked {
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
			).Destructive(),
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
		cmds := []tea.Cmd{m.loadEvents(), m.runOpportunisticPush(msg.calendarID)}
		for _, id := range msg.extraCalendarIDs {
			cmds = append(cmds, m.runOpportunisticPush(id))
		}
		return m, tea.Batch(cmds...)
	}
	m.undoStack.Push(UndoEntry{
		Meta:      msg.meta,
		DeletedAt: time.Now(),
	})
	synced := false // opportunistic push hasn't run yet when toast shows
	toastCmd := m.toast.Deleted(msg.title, synced)
	m.pushDeferralToken++
	token := m.pushDeferralToken
	calID, extra := msg.calendarID, msg.extraCalendarIDs
	deferCmd := tea.Tick(ToastAutoDismissDelay, func(time.Time) tea.Msg {
		return deferredPushMsg{calendarID: calID, extra: extra, token: token}
	})
	return m, tea.Batch(m.loadEvents(), toastCmd, deferCmd)
}

func (m Model) handleDeferredPush(msg deferredPushMsg) (tea.Model, tea.Cmd) {
	// If a restore bumped the token between the delete and this tick,
	// the deferred push is stale — drop it.
	if msg.token != m.pushDeferralToken {
		return m, nil
	}
	cmds := []tea.Cmd{m.runOpportunisticPush(msg.calendarID)}
	for _, id := range msg.extra {
		cmds = append(cmds, m.runOpportunisticPush(id))
	}
	return m, tea.Batch(cmds...)
}

func (m Model) handleEventRestored(msg eventRestoredMsg) (tea.Model, tea.Cmd) {
	// The restore has landed; allow the next undo to dispatch.
	m.undoRestoreInFlight = false
	if errors.Is(msg.err, event.ErrNotDeleted) {
		// The entry has nothing left to undo, so it leaves the stack, and
		// the next undo reaches the entry below it. ErrAlreadyRestored
		// says another path restored the delete first: the trash, the CLI,
		// or a pull. Else a purge took the rows, or the undo data names
		// no rows.
		m.undoStack.Remove(msg.meta)
		reason := "nothing to restore"
		switch {
		case errors.Is(msg.err, event.ErrAlreadyRestored):
			reason = "already restored"
		case errors.Is(msg.err, event.ErrDeletedAgain):
			// A newer delete owns the rows. Its own undo or the trash
			// brings them back.
			reason = event.MsgDeletedAgain
		case errors.Is(msg.err, event.ErrChangedSince):
			// A pull hid the rows again with no log of this delete. The
			// trash still restores them.
			reason = event.MsgChangedSince
		}
		return m, tea.Batch(m.loadEvents(), m.toast.Failed("Undo", reason))
	}
	if errors.Is(msg.err, event.ErrMasterInTrash) {
		// The entry needs its series back first. It leaves the stack, so
		// it never blocks the older entries. The delete stays in the
		// trash, and the trash restores it after the series.
		m.undoStack.Remove(msg.meta)
		return m, tea.Batch(m.loadEvents(), m.toast.Failed("Undo", "restore the series first, then restore this from the trash"))
	}
	if errors.Is(msg.err, event.ErrMasterPurged) {
		// A purge took the master. The rows can never come back, so the
		// entry leaves the stack.
		m.undoStack.Remove(msg.meta)
		return m, tea.Batch(m.loadEvents(), m.toast.Failed("Undo", "the master is purged; nothing to restore"))
	}
	if errors.Is(msg.err, event.ErrUndoNotYet) {
		// The undo can succeed after another restore, for example of a
		// newer truncation of the series. The entry stays on the stack,
		// and the toast tells the user what to restore first.
		return m, m.toast.Failed("Undo", refusalReason(msg.err))
	}
	if errors.Is(msg.err, event.ErrUndoNotPossible) {
		// Any other permanent refusal: a read-only calendar, an ambiguous
		// UID, or a master that changed. A retry gives the same refusal,
		// so the entry leaves the stack and never hides the older entries.
		m.undoStack.Remove(msg.meta)
		return m, tea.Batch(m.loadEvents(), m.toast.Failed("Undo", refusalReason(msg.err)))
	}
	if msg.err != nil {
		// A transient error, for example a busy database or a canceled
		// context. The entry stays on the stack, so the user can retry.
		// The toast schedules its own dismiss tick.
		return m, m.toast.Failed("Undo", refusalReason(msg.err))
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
		done, err := m.app.Trash.RestoreAll(context.Background(), entries)
		return trashActionDoneMsg{action: "restored", title: title, err: err, done: done, total: len(entries)}
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
		// A bulk action can fail after it changes some entries. The reload
		// shows the entries that remain, with notes that are current.
		reason := refusalReason(msg.err)
		if msg.done > 0 {
			reason = fmt.Sprintf("%s %d of %d; %s", msg.action, msg.done, msg.total, reason)
		}
		action := "Purge"
		if msg.action == "restored" {
			action = "Restore"
		}
		return m, tea.Batch(m.loadTrash(), m.loadEvents(), m.toast.Failed(action, reason))
	}
	m.trash = m.trash.ClearMarks()
	cmds := []tea.Cmd{m.loadTrash(), m.loadEvents()}
	switch msg.action {
	case "restored":
		cmds = append(cmds, m.toast.RestoredCount(msg.title, msg.total))
	case "purged":
		cmds = append(cmds, m.toast.PurgedCount(msg.title, msg.total))
	}
	return m, tea.Batch(cmds...)
}

// reopenFormWithError opens the edit form again with a refused save. The
// form keeps the edits of the user, so the user can pick another calendar
// or another scope and save again.
func (m Model) reopenFormWithError(err error) Model {
	m.form = m.form.withCalendarError(err.Error())
	m.formOpen = true
	m.viewReturnEvent = event.Event{}
	return m
}
