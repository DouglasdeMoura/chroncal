package tui

import (
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

func (m Model) dispatchAppMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
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
