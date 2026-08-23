package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/tui/oklch"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(clockTickMsg); ok {
		if msg.token != m.clockTickToken {
			return m, nil
		}
		m.clockTickScheduled = false
		m = m.refreshToday(time.Now())
		return m.scheduleClockTick()
	}
	switch msg := msg.(type) {
	case syncFinishedMsg:
		return m.finishSync(msg)

	case oauthCredentialStoredMsg:
		return m.finishOAuthCredentialStore(msg)

	case accountCredentialStoredMsg:
		return m.finishAccountCredentialStore(msg)

	case accountReauthReadyMsg:
		return m.handleAccountReauthReady(msg)

	case accountManagementDiscoveryReadyMsg:
		return m.finishAccountManagementDiscovery(msg)

	case calendarMoveDiscoveryReadyMsg:
		return m.finishCalendarMoveDiscovery(msg)

	case calendarMoveFinishedMsg:
		return m.finishCalendarMove(msg)

	case accountRenameFinishedMsg:
		return m.finishAccountRename(msg)

	case accountRemovalFinishedMsg:
		return m.finishAccountRemoval(msg)
	}

	// Global key bindings override any open dialog: ctrl+c / q always route
	// through the quit guard, and ? opens the help dialog. The quit confirm
	// itself is exempt so its y/n/esc keys keep working, and ? is a no-op
	// while the help dialog is already up (it handles its own close keys).
	if kp, ok := msg.(tea.KeyPressMsg); ok {
		if newM, cmd, handled := m.interceptGlobalKeys(kp); handled {
			return newM, cmd
		}
	}

	if next, cmd, handled := m.captureOverlayInput(msg); handled {
		return next, cmd
	}

	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		return m.handleBackgroundColor(msg)

	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg)

	case eventsLoadedMsg:
		return m.handleEventsLoaded(msg)

	case calendarsLoadedMsg:
		return m.handleCalendarsLoaded(msg)

	case CalendarMonthChangedMsg:
		return m, m.loadEvents()

	case WeekChangedMsg:
		return m, m.loadEvents()

	case DayChangedMsg:
		return m, m.loadEvents()

	case AgendaCursorChangedMsg:
		m.agenda = m.agenda.ResetWindow(msg.Day)
		return m, m.loadEvents()

	case AgendaReloadMsg:
		return m, m.loadEventsIncremental()

	case AgendaEmptyDaysToggledMsg:
		m.saveUIState()
		return m, nil

	case CalendarDaySelectedMsg:
		return m.handleCalendarDaySelected(msg)

	case EventCreateMsg:
		form, cmd := NewEventFormModel(msg.Day, eventFormCalendars(m.calendars), m.theme)
		return m.openEventForm(form, cmd)

	case EventEditMsg:
		return m.handleEventEdit(msg)

	case eventEditLoadedMsg:
		return m.handleEventEditLoaded(msg)

	case EventViewRequestedMsg:
		return m.handleEventViewRequested(msg)

	case eventViewLoadedMsg:
		return m.handleEventViewLoaded(msg)

	case EventViewClosedMsg:
		m.viewDialogOpen = false
		return m, nil

	case EventDuplicateMsg:
		return m.handleEventDuplicate(msg)

	case EventFormSaveMsg:
		return m.handleEventFormSave(msg)

	case EventFormClosedMsg:
		return m.handleEventFormClosed(msg)

	case PaletteSelectedMsg:
		return m.handlePaletteSelected(msg)

	case PaletteClosedMsg:
		m.paletteOpen = false
		return m, nil

	case SwitchViewMsg:
		return m.switchToView(msg.Mode)

	case GoToTodayMsg:
		return m.goToToday()

	case eventUpdateAfterScopeMsg:
		return m.handleEventUpdateAfterScope(msg)

	case ToggleSidebarMsg:
		return m.toggleSidebar()

	case ToggleWeekNumbersMsg:
		return m.toggleWeekNumbers()

	case ToggleWeekStartMsg:
		return m.toggleWeekStart()

	case eventCreatedMsg:
		return m.handleEventCreated(msg)

	case eventUpdatedMsg:
		return m.handleEventUpdated(msg)

	case EventRSVPMsg:
		return m.handleEventRSVP(msg)

	case eventRSVPUpdatedMsg:
		return m.handleEventRSVPUpdated(msg)

	case DialogDayChangedMsg:
		return m.handleDialogDayChanged(msg)

	case EventDialogClosedMsg:
		m.dialogOpen = false
		return m, nil

	case EventDeleteMsg:
		return m.handleEventDelete(msg)

	case SidebarFocusEscapedMsg:
		m.sidebar = m.sidebar.Blur()
		m.focus = focusCalendar
		return m, nil

	case MiniMonthDateSelectedMsg:
		m = m.navigateMainTo(msg.Date)
		return m, m.loadEvents()

	case CalendarVisibilityToggledMsg:
		return m.handleCalendarVisibilityToggled(msg)

	case CalendarReorderedMsg:
		return m.handleCalendarReordered(msg)

	case AccountReorderedMsg:
		return m.handleAccountReordered(msg)

	case accountOrderSavedMsg:
		return m.handleAccountOrderSaved(msg)

	case calendarOrderSavedMsg:
		return m.handleCalendarOrderSaved(msg)

	case AccountRenameRequestedMsg:
		return m.handleAccountRenameRequested(msg)

	case accountRenameCancelledMsg:
		m.accountRenameOpen = false
		m.accountRenameFromSettings = false
		return m, nil

	case AccountSettingsRequestedMsg:
		return m.handleAccountSettingsRequested(msg)

	case AccountSettingsManageRequestedMsg:
		return m.handleAccountSettingsManageRequested(msg)

	case AccountSettingsSyncRequestedMsg:
		return m.handleAccountSettingsSyncRequested(msg)

	case AccountSettingsRenameRequestedMsg:
		return m.handleAccountSettingsRenameRequested(msg)

	case AccountOAuthConfigSubmittedMsg:
		return m.handleAccountOAuthConfigSubmitted(msg)

	case AccountOAuthConfigClosedMsg:
		return m.handleAccountOAuthConfigClosed(msg)

	case AccountSettingsReauthRequestedMsg:
		return m.handleAccountSettingsReauthRequested(msg)

	case AccountSettingsUpdateCredentialsRequestedMsg:
		return m.handleAccountSettingsUpdateCredentialsRequested(msg)

	case AccountCredentialsUpdateSubmittedMsg:
		return m.handleAccountCredentialsUpdateSubmitted(msg)

	case AccountCredentialsUpdateClosedMsg:
		return m.handleAccountCredentialsUpdateClosed(msg)

	case AccountSettingsRemoveRequestedMsg:
		return m.handleAccountSettingsRemoveRequested(msg)

	case AccountSettingsClosedMsg:
		return m.handleAccountSettingsClosed(msg)

	case CalendarDiscoveryRequestedMsg:
		return m.handleCalendarDiscoveryRequested(msg)

	case accountDiscoveryReadyMsg:
		return m.handleAccountDiscoveryReady(msg)

	case AccountCalendarPickerClosedMsg:
		return m.handleAccountCalendarPickerClosed(msg)

	case AccountCalendarsImportRequestedMsg:
		return m.handleAccountCalendarsImportRequested(msg)

	case AccountCalendarsReconcileRequestedMsg:
		return m.handleAccountCalendarsReconcileRequested(msg)

	case calendarDiscoveryDiscardedMsg:
		return m.handleCalendarDiscoveryDiscarded(msg)

	case accountImportFinishedMsg:
		return m.handleAccountImportFinished(msg)

	case accountSelectionFinishedMsg:
		return m.handleAccountSelectionFinished(msg)

	case CalendarExportRequestedMsg:
		m.calendarTransferGeneration++
		m.calendarManager = m.calendarManager.OpenExport(msg.ID, msg.Name, m.calendarTransferGeneration)
		m.calendarManagerOpen = true
		return m, nil

	case CalendarTransferClosedMsg:
		m.calendarTransferGeneration++
		m.syncing = false
		m.calendarManager = m.calendarManager.CloseTransfer()
		return m, nil

	case CalendarImportPreviewRequestedMsg:
		return m.handleCalendarImportPreviewRequested(msg)

	case calendarImportPreviewReadyMsg:
		return m.handleCalendarImportPreviewReady(msg)

	case CalendarImportRequestedMsg:
		return m.handleCalendarImportRequested(msg)

	case calendarImportFinishedMsg:
		return m.handleCalendarImportFinished(msg)

	case CalendarExportWriteRequestedMsg:
		return m.handleCalendarExportWriteRequested(msg)

	case calendarExportFinishedMsg:
		return m.handleCalendarExportFinished(msg)

	case CalendarManagerRequestedMsg:
		return m.handleCalendarManagerRequested(msg)

	case CalendarSavedMsg:
		return m.handleCalendarSaved(msg)

	case oauthFlowStartedMsg:
		var cmd tea.Cmd
		m.oauthFlow, cmd = m.oauthFlow.Update(msg)
		return m, cmd

	case oauthFlowDoneMsg:
		return m.handleOauthFlowDone(msg)

	case oauthFlowClosedMsg:
		return m.handleOauthFlowClosed(msg)

	case CalendarMoveToAccountRequestedMsg:
		return m.beginCalendarMove(msg)

	case CalendarKeepLocalRequestedMsg:
		return m.handleCalendarKeepLocalRequested(msg)

	case CalendarSetDefaultRequestedMsg:
		return m.handleCalendarSetDefaultRequested(msg)

	case CalendarTestRequestedMsg:
		return m.handleCalendarTestRequested(msg)

	case CalendarDeleteRequestedMsg:
		return m.handleCalendarDeleteRequested(msg)

	case calendarDeleteCountMsg:
		return m.handleCalendarDeleteCount(msg)

	case CalendarManagerClosedMsg:
		return m.handleCalendarManagerClosed(msg)

	case HelpDialogRequestedMsg:
		m.helpDialog = NewHelpDialogModel(m.theme).SetSize(m.width, m.height)
		m.helpDialogOpen = true
		return m, nil

	case TrashViewRequestedMsg:
		return m.handleTrashViewRequested(msg)

	case HelpDialogClosedMsg:
		m.helpDialogOpen = false
		return m, nil

	case SyncAllRequestedMsg:
		return m.handleSyncAllRequested(msg)

	case syncAllPlannedMsg:
		return m.handleSyncAllPlanned(msg)

	case syncCalendarFinishedMsg:
		return m.handleSyncCalendarFinished(msg)

	case SyncCalendarRequestedMsg:
		return m.handleSyncCalendarRequested(msg)

	case spinner.TickMsg:
		return m.handleTick(msg)

	case opportunisticPushFinishedMsg:
		return m.handleOpportunisticPushFinished(msg)

	case syncStatusExpiredMsg:
		if msg.token == m.statusToken && !m.syncing {
			m.syncStatus = ""
		}
		return m, nil

	case toastTickMsg:
		m.toast.Update(msg)
		return m, nil

	case calendarMutationDoneMsg:
		return m.handleCalendarMutationDone(msg)

	case ChoiceDialogResultMsg:
		return m.handleChoiceDialogResult(msg)

	case ConfirmDialogResultMsg:
		return m.handleConfirmDialogResult(msg)

	case eventDeletedMsg:
		return m.handleEventDeleted(msg)

	case deferredPushMsg:
		return m.handleDeferredPush(msg)

	case eventRestoredMsg:
		return m.handleEventRestored(msg)

	case TrashDialogClosedMsg:
		m.trashOpen = false
		return m, nil

	case trashLoadedMsg:
		return m.handleTrashLoaded(msg)

	case TrashReloadMsg:
		return m, m.loadTrash()

	case TrashRestoreRequestedMsg:
		return m.handleTrashRestoreRequested(msg)

	case TrashPurgeRequestedMsg:
		return m.handleTrashPurgeRequested(msg)

	case trashActionDoneMsg:
		return m.handleTrashActionDone(msg)

	case tea.MouseWheelMsg:
		return m.handleMouseWheel(msg)

	case tea.MouseClickMsg:
		return m.handleMouseClick(msg)

	case tea.KeyPressMsg:
		return m.handleKeyPress(msg)
	}

	return m, nil
}

func (m Model) handleBackgroundColor(msg tea.BackgroundColorMsg) (tea.Model, tea.Cmd) {
	m.theme = LoadTheme(m.themeName, msg.IsDark())
	// System theme opts into a terminal-adaptive Selected color.
	// Derive it from the actual terminal background reported over
	// OSC 11 by a shift of OKLCh lightness ±8 %. That guarantees a
	// visible-but-subtle highlight on any terminal theme. The
	// static Dracula Selection hexes in system.toml are the
	// fallback when OSC 11 does not answer (e.g. tmux without
	// passthrough).
	if m.themeName == "system" && msg.Color != nil {
		delta := 0.08
		if !msg.IsDark() {
			delta = -0.08
		}
		m.theme.Selected = oklch.ShiftLightness(msg.Color, delta)
		// Secondary buttons need more weight than a list-row fill —
		// at ±0.08 the pill barely separates from the terminal bg on
		// low-contrast themes. Use ±0.18 so the button reads as a
		// tappable surface, while list-row Selected stays subtle.
		btnDelta := 0.18
		if !msg.IsDark() {
			btnDelta = -0.18
		}
		m.theme.ButtonBg = oklch.ShiftLightness(msg.Color, btnDelta)
	}
	SetActiveTheme(m.theme)
	// Month/week/day views use the selected-color as a vibrant
	// BORDER stroke around the cursor cell; Primary (the brand
	// accent) always stands out against the cell background. The
	// neutral theme.Selected is reserved for list-row FILLS
	// (trash, event list, palette, …) where a muted highlight is
	// what you want.
	m.calendar = m.calendar.SetSelectedColor(m.theme.Primary)
	m.week = m.week.SetSelectedColor(m.theme.Primary)
	m.day = m.day.SetSelectedColor(m.theme.Primary)
	m.agenda = m.agenda.SetTheme(m.theme)
	m.sidebar = m.sidebar.SetTheme(m.theme)
	m.toast.SetTheme(m.theme)
	m.oauthFlow = m.oauthFlow.SetTheme(m.theme)
	m.footer.SetTheme(m.theme)
	m.calendarManager = m.calendarManager.SetTheme(m.theme)
	if m.trashOpen {
		m.trash = m.trash.SetSelectedColor(m.theme.Selected)
	}
	return m, nil
}

func (m Model) handleWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	iw, ih := m.innerDims()
	m.calendar = m.calendar.SetSize(iw, ih)
	m.week = m.week.SetSize(iw, ih)
	m.day = m.day.SetSize(iw, ih)
	m.agenda = m.agenda.SetSize(iw, ih)
	m.trash = m.trash.SetSize(m.width, m.height)
	m.dialog = m.dialog.SetSize(m.width, m.height)
	m.viewDialog = m.viewDialog.SetSize(m.width, m.height)
	m.confirmDialog = m.confirmDialog.SetSize(m.width, m.height)
	m.choiceDialog = m.choiceDialog.SetSize(m.width, m.height)
	m.calendarManager = m.calendarManager.SetSize(m.width, m.height)
	m.accountRename = m.accountRename.SetSize(m.width, m.height)
	m.accountOAuthConfig = m.accountOAuthConfig.SetSize(m.width, m.height)
	m.accountCredentials = m.accountCredentials.SetSize(m.width, m.height)
	m.form = m.form.SetSize(m.width, m.height)
	m.palette = m.palette.SetSize(m.width, m.height)
	m.helpDialog = m.helpDialog.SetSize(m.width, m.height)
	m.oauthFlow = m.oauthFlow.SetSize(m.width, m.height)
	m.ready = true
	return m, nil
}

func (m Model) handleChoiceDialogResult(msg ChoiceDialogResultMsg) (tea.Model, tea.Cmd) {
	m.choiceOpen = false
	kind := m.pendingScopeKind
	m.pendingScopeKind = pendingScopeNone
	if msg.Choice < 0 {
		m.pendingEditSave = EventFormSaveMsg{}
		m.pendingDelete = event.Event{}
		m.viewReturnEvent = event.Event{}
		if kind == pendingScopeCalendarPromote {
			m.pendingCalendarDelete = 0
			m.pendingCalendarDeleteName = ""
			m.pendingCalendarPromote = 0
			m.pendingCalendarPromoteName = ""
			m.pendingCalendarPromoteCands = nil
		}
		if kind == pendingScopeAccountSelectionPromote {
			m.pendingAccountSelection = nil
			m.pendingAccountDefaultCandidates = nil
		}
		if kind == pendingScopeCalendarMoveAccount || kind == pendingScopeCalendarMoveCollection {
			m.pendingCalendarMove = nil
		}
		return m, nil
	}
	if next, cmd, handled := m.handleCalendarMoveChoice(kind, msg.Choice); handled {
		return next, cmd
	}
	if kind == pendingScopeAccountSelectionPromote {
		if msg.Choice >= len(m.pendingAccountDefaultCandidates) ||
			m.pendingAccountSelection == nil {
			m.pendingAccountSelection = nil
			m.pendingAccountDefaultCandidates = nil
			return m, nil
		}
		candidate := m.pendingAccountDefaultCandidates[msg.Choice]
		if candidate.id != 0 {
			m.pendingAccountSelection.params.NewDefaultID = candidate.id
		} else {
			m.pendingAccountSelection.params.NewDefaultPath = candidate.path
		}
		selection := m.pendingAccountSelection
		m.pendingAccountDefaultCandidates = nil
		m = m.showAccountCalendarRemovalConfirmation(selection)
		return m, nil
	}
	if kind == pendingScopeCalendarPromote {
		if msg.Choice < 0 || msg.Choice >= len(m.pendingCalendarPromoteCands) {
			m.pendingCalendarDelete = 0
			m.pendingCalendarDeleteName = ""
			m.pendingCalendarPromote = 0
			m.pendingCalendarPromoteName = ""
			m.pendingCalendarPromoteCands = nil
			return m, nil
		}
		promoteID := m.pendingCalendarPromoteCands[msg.Choice]
		m.pendingCalendarPromote = promoteID
		if info, ok := m.calendars[promoteID]; ok {
			m.pendingCalendarPromoteName = info.Name
		}
		id, name := m.pendingCalendarDelete, m.pendingCalendarDeleteName
		m.pendingCalendarPromoteCands = nil
		return m, func() tea.Msg {
			count, _ := m.app.Events.CountByCalendar(context.Background(), id)
			return calendarDeleteCountMsg{id: id, name: name, eventCount: count}
		}
	}
	if kind == pendingScopeEdit {
		save := m.pendingEditSave
		m.pendingEditSave = EventFormSaveMsg{}
		editID := m.form.editID
		choice := msg.Choice
		return m, func() tea.Msg {
			return m.dispatchEditScope(editID, choice, save)
		}
	}
	ev := m.pendingDelete
	return m, func() tea.Msg {
		switch msg.Choice {
		case 0: // This event
			meta, err := m.app.Events.DeleteInstanceWithUndo(context.Background(), ev.UID, ev.StartTime)
			return eventDeletedMsg{
				calendarID: ev.CalendarID,
				meta:       meta,
				title:      ev.Title,
				err:        err,
			}
		case 1: // This and following
			meta, err := m.app.Events.DeleteFromInstanceWithUndo(context.Background(), ev.UID, ev.StartTime)
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
}

func (m Model) handleConfirmDialogResult(msg ConfirmDialogResultMsg) (tea.Model, tea.Cmd) {
	m.confirmOpen = false
	if m.pendingQuit {
		m.pendingQuit = false
		if msg.Confirmed {
			m.oauthFlow.Abort() // release any in-flight OAuth listener
			return m, tea.Quit
		}
		return m, nil
	}
	if !msg.Confirmed {
		m.pendingCalendarDelete = 0
		m.pendingCalendarDeleteName = ""
		m.pendingCalendarKeepLocal = 0
		m.pendingCalendarPromote = 0
		m.pendingCalendarPromoteName = ""
		m.pendingPurgeEntries = nil
		m.pendingPurgeTitle = ""
		m.pendingAccountSelection = nil
		m.pendingAccountDefaultCandidates = nil
		m.pendingAccountRemoveID = 0
		m.pendingAccountRemoveName = ""
		return m, nil
	}
	if m.pendingAccountRemoveID != 0 {
		accountID := m.pendingAccountRemoveID
		name := m.pendingAccountRemoveName
		m.pendingAccountRemoveID = 0
		m.pendingAccountRemoveName = ""
		m.calendarManagerOpen = false
		m.syncing = true
		m.syncStatus = "Removing account…"
		return m, tea.Batch(m.syncSpinner.Tick, m.removeAccount(accountID, name))
	}
	if m.pendingAccountSelection != nil {
		selection := m.pendingAccountSelection
		m.pendingAccountSelection = nil
		m.pendingAccountDefaultCandidates = nil
		m.syncing = true
		m.syncStatus = "Applying calendar changes…"
		return m, tea.Batch(
			m.syncSpinner.Tick,
			m.reconcileAndSyncAccountCalendars(selection),
		)
	}
	if len(m.pendingPurgeEntries) > 0 {
		entries := m.pendingPurgeEntries
		title := m.pendingPurgeTitle
		m.pendingPurgeEntries = nil
		m.pendingPurgeTitle = ""
		return m, func() tea.Msg {
			for _, e := range entries {
				if err := m.app.Trash.Purge(context.Background(), e); err != nil {
					return trashActionDoneMsg{action: "purged", title: title, err: err}
				}
			}
			return trashActionDoneMsg{action: "purged", title: title, err: nil}
		}
	}
	if m.pendingCalendarKeepLocal != 0 {
		id := m.pendingCalendarKeepLocal
		m.pendingCalendarKeepLocal = 0
		return m, func() tea.Msg {
			ctx := context.Background()
			cal, err := m.app.Calendars.Get(ctx, id)
			if err != nil {
				return calendarMutationDoneMsg{err: err}
			}
			credStore, _ := m.openCredentialStore()
			return calendarMutationDoneMsg{err: m.app.Calendars.Disconnect(ctx, cal, credStore)}
		}
	}
	if m.pendingCalendarDelete != 0 {
		id := m.pendingCalendarDelete
		newDefaultID := m.pendingCalendarPromote
		m.pendingCalendarDelete = 0
		m.pendingCalendarDeleteName = ""
		m.pendingCalendarPromote = 0
		m.pendingCalendarPromoteName = ""
		// Delete confirmed: close the edit dialog too.
		m.calendarManagerOpen = false
		return m, func() tea.Msg {
			credStore, _ := m.openCredentialStore()
			err := m.app.Calendars.DeleteWithRemoteCleanup(context.Background(), id, newDefaultID, credStore)
			return calendarMutationDoneMsg{err: err}
		}
	}
	ev := m.pendingDelete
	if ev.ID == 0 {
		// No branch above matched: nothing destructive waits behind this
		// confirm. Do not delete event 0. That call always fails and shows
		// a spurious error toast.
		return m, nil
	}
	return m, func() tea.Msg {
		meta, err := m.app.Events.DeleteWithUndo(context.Background(), ev.ID)
		return eventDeletedMsg{
			calendarID: ev.CalendarID,
			meta:       meta,
			title:      ev.Title,
			err:        err,
		}
	}
}
