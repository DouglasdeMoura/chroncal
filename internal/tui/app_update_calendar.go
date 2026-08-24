package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/icaltransfer"
)

func (m Model) handleCalendarsLoaded(msg calendarsLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err == nil {
		m.calendars = msg.calendars
		m.accounts = msg.accounts
		// Overlay any unconfirmed reorder so a reload that races an
		// in-flight SetOrder reflects the user's move instead of the stale
		// DB order. Applied to m.calendars itself so the sidebar items and
		// the manage dialog (both built below from m.calendars) agree.
		for id, pos := range m.pendingOrder {
			if c, ok := m.calendars[id]; ok {
				c.DisplayOrder = pos
				m.calendars[id] = c
			} else {
				// Gone from a full reload (deleted) — drop the stale pending
				// entry so the map can't grow without bound across
				// reorder+delete cycles.
				delete(m.pendingOrder, id)
			}
		}
		for accountID, pos := range m.pendingAccountOrder {
			found := false
			for id, c := range m.calendars {
				if c.AccountID != accountID {
					continue
				}
				found = true
				c.AccountOrder = pos
				m.calendars[id] = c
			}
			if !found {
				delete(m.pendingAccountOrder, accountID)
			}
		}
		m.sidebar = m.sidebar.SetList(m.sidebar.List().SetItems(sortedCalendarListItems(m.calendars)))
		// Prune stale hidden IDs after CalendarListModel has done its pruning.
		m.hiddenCalendars = m.sidebar.List().HiddenSet()
		m.saveUIState()
		// Rebuild the per-view CalendarEvent slices so rename/color edits
		// reflect immediately — eventsToCalendar reads colors from
		// m.calendars at conversion time.
		m = m.refreshCalendarViews()
		if m.calendarManagerOpen {
			m.calendarManager = m.calendarManager.SetData(m.calendars, m.hiddenCalendars)
			if m.calendarManager.Screen() == CalendarManagerScreenAccount {
				if params, ok := m.accountSettingsParams(m.calendarManager.ActiveAccountID()); ok {
					m.calendarManager = m.calendarManager.OpenAccount(params).SetSize(m.width, m.height)
				}
			}
		}
	}
	return m, nil
}

func (m Model) handleCalendarVisibilityToggled(msg CalendarVisibilityToggledMsg) (tea.Model, tea.Cmd) {
	// Mirror the toggle into the open calendar manager. Its root list
	// and reopened detail params then never go stale. The manager's own
	// updateCalendar mirror is only reachable through this forward.
	if m.calendarManagerOpen {
		m.calendarManager, _ = m.calendarManager.Update(msg)
	}
	if m.hiddenCalendars == nil {
		m.hiddenCalendars = map[int64]bool{}
	}
	if msg.Hidden {
		m.hiddenCalendars[msg.ID] = true
	} else {
		delete(m.hiddenCalendars, msg.ID)
	}
	list := m.sidebar.List().SetHidden(msg.ID, msg.Hidden)
	m.sidebar = m.sidebar.SetList(list)
	m.saveUIState()
	m = m.refreshCalendarViews()
	if m.dialogOpen {
		dayEvents := eventsOn(filterVisibleEvents(m.events, m.hiddenCalendars), m.dialog.day)
		m.dialog = m.dialog.SetEvents(dayEvents)
	}
	return m, nil
}

func (m Model) handleCalendarReordered(msg CalendarReorderedMsg) (tea.Model, tea.Cmd) {
	// A reorder can originate from the sidebar list or the manage
	// dialog. Mirror it into m.calendars so both views (and any later
	// reload-from-cache) stay coherent. Record it as pending. A
	// reload that races the async SetOrder below then does not revert
	// to the stale DB order.
	ids := msg.IDs
	if m.pendingOrder == nil {
		m.pendingOrder = make(map[int64]int64, len(ids))
	}
	for i, id := range ids {
		m.pendingOrder[id] = int64(i)
		if info, ok := m.calendars[id]; ok {
			info.DisplayOrder = int64(i)
			m.calendars[id] = info
		}
	}
	// Re-sort the sidebar from the updated order. A dialog-originated
	// reorder then shows up behind the dialog. For a sidebar-originated
	// one it re-applies the order the list already swapped to. Keep
	// the cursor by calendar identity. A reorder from either surface
	// then never leaves the sidebar highlight on a different calendar.
	m.sidebar = m.sidebar.SetList(m.sidebar.List().SetItemsPreservingCursor(sortedCalendarListItems(m.calendars)))
	if m.calendarManagerOpen {
		m.calendarManager = m.calendarManager.SetData(m.calendars, m.hiddenCalendars)
	}
	return m, func() tea.Msg {
		return calendarOrderSavedMsg{ids: ids, err: m.app.Calendars.SetOrder(context.Background(), ids)}
	}
}

func (m Model) handleCalendarOrderSaved(msg calendarOrderSavedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, m.toast.Failed(msg.err.Error())
	}
	// Clear only the positions this save confirmed. A newer reorder may
	// have updated m.pendingOrder while this write was in flight. That
	// one stays pending until its own save lands.
	for i, id := range msg.ids {
		if m.pendingOrder[id] == int64(i) {
			delete(m.pendingOrder, id)
		}
	}
	return m, nil
}

func (m Model) handleCalendarImportPreviewRequested(msg CalendarImportPreviewRequestedMsg) (tea.Model, tea.Cmd) {
	transfer, ok := m.calendarManager.Transfer()
	if !ok || transfer.Generation() != msg.Generation {
		return m, nil
	}
	m.syncing = true
	m.syncStatus = "Reading iCal file…"
	path := msg.Path
	return m, func() tea.Msg {
		preview, err := icaltransfer.ParseFile(path)
		return calendarImportPreviewReadyMsg{Generation: msg.Generation, Path: path, Preview: preview, Err: err}
	}
}

func (m Model) handleCalendarImportPreviewReady(msg calendarImportPreviewReadyMsg) (tea.Model, tea.Cmd) {
	transfer, ok := m.calendarManager.Transfer()
	if !ok || transfer.Generation() != msg.Generation {
		return m, nil
	}
	m.syncing = false
	if msg.Err != nil {
		next := transfer.WithError(msg.Err)
		m.calendarManager = m.calendarManager.SetTransfer(next)
		m.syncStatus = "iCal preview failed: " + msg.Err.Error()
		return m, nil
	}
	if msg.Preview.Events+msg.Preview.Todos+msg.Preview.Journals == 0 {
		err := fmt.Errorf("no importable VEVENT, VTODO, or VJOURNAL components")
		if msg.Preview.FreeBusy > 0 {
			err = fmt.Errorf("%w; VFREEBUSY is preview-only", err)
		}
		m.calendarManager = m.calendarManager.SetTransfer(transfer.WithError(err))
		m.syncStatus = "iCal preview failed: " + err.Error()
		return m, nil
	}
	next := transfer.WithPreview(msg.Path, msg.Preview, calendarImportDestinations(m.calendars, msg.Preview)).
		SetSize(m.width, m.height)
	m.calendarManager = m.calendarManager.SetTransfer(next)
	m.syncStatus = ""
	return m, nil
}

func (m Model) handleCalendarImportRequested(msg CalendarImportRequestedMsg) (tea.Model, tea.Cmd) {
	transfer, ok := m.calendarManager.Transfer()
	if !ok || transfer.Generation() != msg.Generation {
		return m, nil
	}
	m.syncing = true
	m.syncStatus = "Importing iCal file…"
	request := msg
	return m, func() tea.Msg {
		ctx := context.Background()
		calendarID := request.CalendarID
		created := false
		if calendarID == 0 {
			cal, err := m.app.Calendars.Create(ctx, request.NewName, request.NewColor, "")
			if err != nil {
				return calendarImportFinishedMsg{Generation: request.Generation, Err: fmt.Errorf("create destination calendar: %w", err)}
			}
			calendarID = cal.ID
			created = true
		}
		cleanup := func(cause error) calendarImportFinishedMsg {
			if !created {
				return calendarImportFinishedMsg{Generation: request.Generation, CalendarID: calendarID, Err: cause}
			}
			if err := m.app.Calendars.Delete(ctx, calendarID); err != nil {
				cause = errors.Join(cause, fmt.Errorf("remove incomplete calendar: %w", err))
			}
			return calendarImportFinishedMsg{Generation: request.Generation, Err: cause}
		}
		if err := icaltransfer.ValidateDestination(ctx, m.app, calendarID, request.Preview); err != nil {
			return cleanup(err)
		}
		summary := icaltransfer.Import(ctx, m.app, calendarID, &request.Preview.Result)
		if summary.Failed > 0 {
			return cleanup(fmt.Errorf("%d component(s) failed to import", summary.Failed))
		}
		return calendarImportFinishedMsg{Generation: request.Generation, CalendarID: calendarID, Summary: summary}
	}
}

func (m Model) handleCalendarImportFinished(msg calendarImportFinishedMsg) (tea.Model, tea.Cmd) {
	transfer, ok := m.calendarManager.Transfer()
	if !ok || transfer.Generation() != msg.Generation {
		return m, nil
	}
	m.syncing = false
	if msg.Err != nil {
		if transfer, ok := m.calendarManager.Transfer(); ok {
			m.calendarManager = m.calendarManager.SetTransfer(transfer.WithError(msg.Err))
		}
		m.syncStatus = "iCal import failed: " + msg.Err.Error()
		return m, nil
	}
	m.calendarManager = m.calendarManager.CompleteTransfer(msg.CalendarID)
	imported := len(msg.Summary.Events) + len(msg.Summary.Todos) + len(msg.Summary.Journals)
	m.syncStatus = fmt.Sprintf("Imported %d item(s) with %d warning(s)", imported, len(msg.Summary.Warnings))
	m.statusToken++
	return m, tea.Batch(m.loadCalendars(), m.loadEvents(), m.expireStatusAfter(10*time.Second, m.statusToken))
}

func (m Model) handleCalendarExportWriteRequested(msg CalendarExportWriteRequestedMsg) (tea.Model, tea.Cmd) {
	transfer, ok := m.calendarManager.Transfer()
	if !ok || transfer.Generation() != msg.Generation {
		return m, nil
	}
	m.syncing = true
	m.syncStatus = "Exporting calendar…"
	request := msg
	return m, func() tea.Msg {
		summary, err := icaltransfer.ExportCalendarFile(
			context.Background(), m.app, request.CalendarID, request.Name, request.Path,
		)
		return calendarExportFinishedMsg{Generation: request.Generation, Path: request.Path, Summary: summary, Err: err}
	}
}

func (m Model) handleCalendarExportFinished(msg calendarExportFinishedMsg) (tea.Model, tea.Cmd) {
	transfer, ok := m.calendarManager.Transfer()
	if !ok || transfer.Generation() != msg.Generation {
		return m, nil
	}
	m.syncing = false
	if msg.Err != nil {
		if transfer, ok := m.calendarManager.Transfer(); ok {
			m.calendarManager = m.calendarManager.SetTransfer(transfer.WithError(msg.Err))
		}
		m.syncStatus = "Calendar export failed: " + msg.Err.Error()
		return m, nil
	}
	m.calendarManager = m.calendarManager.CloseTransfer()
	m.syncStatus = fmt.Sprintf("Exported %d events, %d todos, %d journals to %s",
		msg.Summary.Events, msg.Summary.Todos, msg.Summary.Journals, msg.Path)
	m.statusToken++
	return m, m.expireStatusAfter(10*time.Second, m.statusToken)
}

func (m Model) handleCalendarManagerRequested(msg CalendarManagerRequestedMsg) (tea.Model, tea.Cmd) {
	m.calendarManager = m.calendarManager.SetData(m.calendars, m.hiddenCalendars)
	m.calendarManagerOpen = true
	switch msg.Target {
	case CalendarManagerTargetRoot:
		m.calendarManager = m.calendarManager.CloseDetail()
	case CalendarManagerTargetCalendar:
		m.calendarManager = m.calendarManager.CloseDetail()
		m.calendarManager = m.calendarManager.SelectCalendar(msg.CalendarID)
		if info, ok := m.calendars[msg.CalendarID]; ok {
			params := CalendarDialogParams{
				ID:            msg.CalendarID,
				AccountID:     info.AccountID,
				AccountName:   info.AccountName,
				Name:          info.Name,
				Color:         info.Color,
				Description:   info.Description,
				OwnerEmail:    info.OwnerEmail,
				RemoteLinked:  info.AccountID != 0,
				LastSyncAt:    info.LastSyncAt,
				LastSyncError: info.LastSyncError,
				IsDefault:     info.IsDefault,
				Hidden:        m.hiddenCalendars[msg.CalendarID],
			}
			if params.AccountName == "" {
				if configured, exists := m.accounts[info.AccountID]; exists {
					params.AccountName = configured.DisplayName
				}
			}
			m.calendarManager = m.calendarManager.OpenCalendar(params).SetSize(m.width, m.height)
		}
	case CalendarManagerTargetAccount:
		m.calendarManager = m.calendarManager.CloseDetail()
		if params, ok := m.accountSettingsParams(msg.AccountID); ok {
			m.calendarManager = m.calendarManager.OpenAccount(params).SetSize(m.width, m.height)
		}
	case CalendarManagerTargetLocalCreate:
		m.calendarManager = m.calendarManager.CloseDetail()
		params := CalendarDialogParams{Color: "#a6e3a1", OfferDefault: len(m.calendars) > 0}
		m.calendarManager = m.calendarManager.OpenCalendar(params).SetSize(m.width, m.height)
	case CalendarManagerTargetAccountConnect:
		m.calendarManager = m.calendarManager.CloseDetail()
		m.calendarManager = m.calendarManager.OpenAccountConnection().SetSize(m.width, m.height)
	case CalendarManagerTargetImport:
		m.calendarManager = m.calendarManager.CloseDetail()
		m.calendarTransferGeneration++
		m.calendarManager = m.calendarManager.OpenImport(m.calendarTransferGeneration)
	}
	return m, nil
}

func (m Model) handleCalendarSaved(msg CalendarSavedMsg) (tea.Model, tea.Cmd) {
	// Metadata saves stay blocked while a sync or OAuth completion is
	// writing to the calendars table. Never do this in silence. Surface
	// the block as a form error. The editor and draft then stay intact
	// and the user knows to retry.
	if m.syncing || m.oauthPending {
		return m, func() tea.Msg {
			return calendarMutationDoneMsg{err: errors.New("sync in progress — try again in a moment")}
		}
	}
	// Keep the dialog open until the mutation succeeds so we can
	// show validation errors (e.g. duplicate name) on the form.
	saved := msg
	return m, func() tea.Msg {
		ctx := context.Background()
		var (
			cal calendar.Calendar
			err error
		)
		if saved.ID == 0 {
			cal, err = m.app.Calendars.Create(ctx, saved.Name, saved.Color, saved.Description)
		} else {
			cal, err = m.app.Calendars.Update(ctx, saved.ID, saved.Name, saved.Color, saved.Description)
		}
		if err != nil {
			return calendarMutationDoneMsg{err: err}
		}

		if err := m.app.Calendars.SetOwnerEmail(ctx, cal.ID, saved.OwnerEmail); err != nil {
			return calendarMutationDoneMsg{err: err}
		}

		// MakeDefault only matters on create; edit-mode default moves
		// through the dedicated CalendarSetDefaultRequestedMsg path so
		// the rule stays in one place.
		if saved.ID == 0 && saved.MakeDefault {
			if derr := m.app.Calendars.SetDefault(ctx, cal.ID); derr != nil {
				return calendarMutationDoneMsg{err: derr}
			}
		}

		return calendarMutationDoneMsg{err: nil}
	}
}

func (m Model) handleCalendarKeepLocalRequested(msg CalendarKeepLocalRequestedMsg) (tea.Model, tea.Cmd) {
	// Keep the edit dialog open behind the confirm, mirroring the delete
	// flow: cancelling returns to the editor with the draft intact.
	message := fmt.Sprintf(
		"Keep %q as a local calendar?\n\nSyncing with its account stops; every downloaded event stays on this device.\nRe-adding it later from Manage Calendars creates a separate copy.",
		msg.Name,
	)
	return m.armConfirm(
		pendingAction{
			kind:   pendingActionCalendarKeepLocal,
			target: pendingTarget{calendarID: msg.ID},
		},
		NewConfirmDialogModel(message, "Keep as Local", m.theme),
	), nil
}

func (m Model) handleCalendarSetDefaultRequested(msg CalendarSetDefaultRequestedMsg) (tea.Model, tea.Cmd) {
	id := msg.ID
	return m, func() tea.Msg {
		// keepEditor: the Set as Default button lives on the edit form;
		// success must not pop the form and discard an unsaved draft.
		return calendarMutationDoneMsg{err: m.app.Calendars.SetDefault(context.Background(), id), keepEditor: true}
	}
}

func (m Model) handleCalendarTestRequested(msg CalendarTestRequestedMsg) (tea.Model, tea.Cmd) {
	req := msg
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		meta, err := caldav.VerifyCalendarURL(ctx, req.URL, req.Username, req.Password, req.AuthType, req.AllowInsecure)
		if err != nil {
			return CalendarTestResultMsg{Message: err.Error()}
		}
		message := "Connected"
		if meta.DisplayName != "" {
			message = fmt.Sprintf("Connected · %s", meta.DisplayName)
		}
		return CalendarTestResultMsg{OK: true, Message: message}
	}
}

// armCalendarDeleteCount records that a calendar-delete confirm is in
// flight while CountByCalendar runs. handleCalendarDeleteCount then
// ignores a count that arrives after clearPending (ctrl+c, cancel,
// manager close), so a stale result cannot re-arm a destructive confirm
// over quit or after the user has left the manager.
func (m Model) armCalendarDeleteCount(id, promoteID int64, name string) Model {
	m.pending = pendingAction{
		kind:   pendingActionCalendarDelete,
		target: pendingTarget{calendarID: id, promoteID: promoteID},
		label:  name,
	}
	return m
}

func (m Model) handleCalendarDeleteRequested(msg CalendarDeleteRequestedMsg) (tea.Model, tea.Cmd) {
	// Fetch the event count before the confirm dialog. The user then
	// knows how many events will be deleted alongside the calendar.
	// When the target is the current default, slot in a picker dialog
	// before the destructive confirm. The user then chooses the
	// replacement default. Never promote in silence.
	id, name := msg.ID, msg.Name
	info, known := m.calendars[id]
	if known && info.IsDefault {
		candidates := defaultPromotionCandidates(m.calendars, id)
		if len(candidates) == 0 {
			// Last calendar: service will return ErrLastCalendar; let
			// the normal confirm flow surface the error verbatim.
			m = m.armCalendarDeleteCount(id, 0, name)
			return m, func() tea.Msg {
				count, _ := m.app.Events.CountByCalendar(context.Background(), id)
				return calendarDeleteCountMsg{id: id, name: name, eventCount: count}
			}
		}
		promoteCands := make([]int64, len(candidates))
		labels := make([]string, len(candidates))
		for i, c := range candidates {
			promoteCands[i] = c.id
			labels[i] = c.name
		}
		message := fmt.Sprintf("%q is the default calendar.\n\nChoose a new default before deleting it:", name)
		return m.armChoice(
			pendingAction{
				kind:   pendingActionCalendarPromote,
				target: pendingTarget{calendarID: id, promoteCands: promoteCands},
				label:  name,
			},
			NewChoiceDialogModel(message, m.theme, labels...),
		), nil
	}
	m = m.armCalendarDeleteCount(id, 0, name)
	return m, func() tea.Msg {
		count, _ := m.app.Events.CountByCalendar(context.Background(), id)
		return calendarDeleteCountMsg{id: id, name: name, eventCount: count}
	}
}

func (m Model) handleCalendarDeleteCount(msg calendarDeleteCountMsg) (tea.Model, tea.Cmd) {
	if m.pending.kind != pendingActionCalendarDelete || m.pending.target.calendarID != msg.id {
		return m, nil
	}
	// Keep the edit dialog open behind the confirm. If the user
	// cancels the confirm, they return to the edit dialog. They do
	// not lose their in-progress changes. The confirm dialog takes
	// input priority, so the edit dialog is visible but inert.
	message := fmt.Sprintf("Delete calendar %q?", msg.name)
	if msg.eventCount > 0 {
		if msg.eventCount == 1 {
			message = fmt.Sprintf("Delete calendar %q?\n\n%d event will be deleted", msg.name, msg.eventCount)
		} else {
			message = fmt.Sprintf("Delete calendar %q?\n\n%d events will be deleted", msg.name, msg.eventCount)
		}
	}
	if msg.promoteName != "" {
		message += fmt.Sprintf("\n\n%q will become the default.", msg.promoteName)
	}
	return m.armConfirm(
		pendingAction{
			kind:   pendingActionCalendarDelete,
			target: pendingTarget{calendarID: msg.id, promoteID: msg.promoteID},
		},
		NewConfirmDialogModel(message, "Delete", m.theme).
			Destructive(),
	), nil
}

func (m Model) handleCalendarManagerClosed(msg CalendarManagerClosedMsg) (tea.Model, tea.Cmd) {
	m.calendarManagerOpen = false
	if m.pendingAccountManagementID != 0 {
		m.accountManagementGeneration++
		m.pendingAccountManagementID = 0
		m.syncing = false
		m.syncStatus = ""
	}
	m = m.clearPending()
	return m, nil
}

func (m Model) handleCalendarMutationDone(msg calendarMutationDoneMsg) (tea.Model, tea.Cmd) {
	if m.calendarManagerOpen {
		m.calendarManager, _ = m.calendarManager.Update(msg)
	}
	if msg.err != nil {
		if m.calendarManagerOpen {
			m.calendarManager = m.calendarManager.FormSetError(cdIdxName, msg.err.Error())
			return m, nil
		}
		m.err = msg.err
		return m, nil
	}
	// m.calendarManagerOpen = false (Removed to keep it open)
	return m, tea.Batch(m.loadCalendars(), m.loadEvents())
}
