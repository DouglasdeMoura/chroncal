package tui

import (
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// overlayKind names one modal layer. The stack order in overlayStack is
// the input-routing priority. Last-painted in View is first on the stack.
type overlayKind int

const (
	overlayQuitConfirm overlayKind = iota
	overlayHelp
	overlayPalette
	overlayConfirm
	overlayChoice
	overlayOAuth
	overlayCredentials
	overlayOAuthConfig
	overlayRename
	overlayTrash
	overlayCalendarManager
	overlayForm
	overlayViewDialog
	overlayDialog
)

// overlayLayer is one active modal. passthrough is true when this layer
// lets the message reach a lower layer or the main switch. update runs
// only when passthrough is false.
type overlayLayer struct {
	kind        overlayKind
	passthrough func(tea.Msg) bool
	update      func(*Model, tea.Msg) tea.Cmd
}

func passUnlessInput(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseClickMsg, tea.MouseWheelMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg, tea.PasteMsg:
		return false
	default:
		return true
	}
}

func passUnlessKeyOrClick(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseClickMsg, tea.MouseWheelMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		return false
	default:
		return true
	}
}

func passPaletteHost(msg tea.Msg) bool {
	switch msg.(type) {
	case PaletteSelectedMsg, PaletteClosedMsg,
		tea.BackgroundColorMsg, tea.WindowSizeMsg,
		spinner.TickMsg:
		return true
	default:
		return false
	}
}

func passOAuthConfigHost(msg tea.Msg) bool {
	switch msg.(type) {
	case AccountOAuthConfigSubmittedMsg, AccountOAuthConfigClosedMsg,
		syncStatusExpiredMsg, toastTickMsg, spinner.TickMsg,
		tea.BackgroundColorMsg, tea.WindowSizeMsg:
		return true
	default:
		return false
	}
}

func passCredentialsHost(msg tea.Msg) bool {
	switch msg.(type) {
	case AccountCredentialsUpdateSubmittedMsg, AccountCredentialsUpdateClosedMsg,
		syncStatusExpiredMsg, toastTickMsg, spinner.TickMsg,
		tea.BackgroundColorMsg, tea.WindowSizeMsg:
		return true
	default:
		return false
	}
}

func passRenameHost(msg tea.Msg) bool {
	switch msg.(type) {
	case AccountRenameRequestedMsg, accountRenameCancelledMsg,
		accountRenameFinishedMsg, syncStatusExpiredMsg, toastTickMsg, spinner.TickMsg,
		tea.BackgroundColorMsg, tea.WindowSizeMsg:
		return true
	default:
		return false
	}
}

func passCalendarManagerHost(msg tea.Msg) bool {
	switch msg.(type) {
	case CalendarManagerClosedMsg, CalendarManagerRequestedMsg, CalendarTransferClosedMsg,
		CalendarSavedMsg, CalendarDiscoveryRequestedMsg,
		CalendarDeleteRequestedMsg, CalendarKeepLocalRequestedMsg, CalendarMoveToAccountRequestedMsg, CalendarTestRequestedMsg,
		CalendarVisibilityToggledMsg, CalendarReorderedMsg, AccountReorderedMsg,
		calendarOrderSavedMsg, accountOrderSavedMsg,
		CalendarExportRequestedMsg, CalendarImportPreviewRequestedMsg,
		CalendarImportRequestedMsg, CalendarExportWriteRequestedMsg,
		calendarImportPreviewReadyMsg, calendarImportFinishedMsg, calendarExportFinishedMsg,
		AccountSettingsRequestedMsg, AccountSettingsClosedMsg, AccountSettingsManageRequestedMsg, AccountSettingsSyncRequestedMsg,
		AccountSettingsRenameRequestedMsg, AccountSettingsReauthRequestedMsg,
		AccountSettingsUpdateCredentialsRequestedMsg,
		AccountSettingsRemoveRequestedMsg, AccountRenameRequestedMsg,
		CalendarSetDefaultRequestedMsg,
		AccountCalendarsImportRequestedMsg, AccountCalendarsReconcileRequestedMsg,
		AccountCalendarPickerClosedMsg,
		accountDiscoveryReadyMsg, accountImportFinishedMsg, accountSelectionFinishedMsg,
		calendarDiscoveryDiscardedMsg,
		calendarDeleteCountMsg,
		tea.BackgroundColorMsg, tea.WindowSizeMsg,
		eventsLoadedMsg, calendarsLoadedMsg,
		calendarMutationDoneMsg,
		toastTickMsg, spinner.TickMsg, syncStatusExpiredMsg:
		return true
	default:
		return false
	}
}

func passFormHost(msg tea.Msg) bool {
	switch msg.(type) {
	case EventFormSaveMsg, EventFormClosedMsg,
		tea.BackgroundColorMsg, tea.WindowSizeMsg,
		eventsLoadedMsg, calendarsLoadedMsg,
		eventCreatedMsg, eventUpdatedMsg,
		spinner.TickMsg:
		return true
	default:
		return false
	}
}

// overlayStack returns active modal layers, top-most first. The order
// matches View paint order (last painted owns input).
func (m Model) overlayStack() []overlayLayer {
	if !m.anyOverlayOpen() {
		return nil
	}
	layers := make([]overlayLayer, 0, 8)
	if m.confirmOpen && m.pending.kind == pendingActionQuit {
		layers = append(layers, overlayLayer{
			kind:        overlayQuitConfirm,
			passthrough: passUnlessInput,
			update: func(m *Model, msg tea.Msg) tea.Cmd {
				var cmd tea.Cmd
				m.confirmDialog, cmd = m.confirmDialog.Update(msg)
				return cmd
			},
		})
	}
	if m.helpDialogOpen {
		layers = append(layers, overlayLayer{
			kind:        overlayHelp,
			passthrough: passUnlessInput,
			update: func(m *Model, msg tea.Msg) tea.Cmd {
				var cmd tea.Cmd
				m.helpDialog, cmd = m.helpDialog.Update(msg)
				return cmd
			},
		})
	}
	if m.paletteOpen {
		layers = append(layers, overlayLayer{
			kind:        overlayPalette,
			passthrough: passPaletteHost,
			update: func(m *Model, msg tea.Msg) tea.Cmd {
				var cmd tea.Cmd
				m.palette, cmd = m.palette.Update(msg)
				return cmd
			},
		})
	}
	if m.confirmOpen && m.pending.kind != pendingActionQuit {
		layers = append(layers, overlayLayer{
			kind:        overlayConfirm,
			passthrough: passUnlessInput,
			update: func(m *Model, msg tea.Msg) tea.Cmd {
				var cmd tea.Cmd
				m.confirmDialog, cmd = m.confirmDialog.Update(msg)
				return cmd
			},
		})
	}
	if m.choiceOpen {
		layers = append(layers, overlayLayer{
			kind:        overlayChoice,
			passthrough: passUnlessInput,
			update: func(m *Model, msg tea.Msg) tea.Cmd {
				var cmd tea.Cmd
				m.choiceDialog, cmd = m.choiceDialog.Update(msg)
				return cmd
			},
		})
	}
	if m.oauthFlowOpen {
		layers = append(layers, overlayLayer{
			kind:        overlayOAuth,
			passthrough: passUnlessKeyOrClick,
			update: func(m *Model, msg tea.Msg) tea.Cmd {
				var cmd tea.Cmd
				m.oauthFlow, cmd = m.oauthFlow.Update(msg)
				return cmd
			},
		})
	}
	if m.accountCredentialsOpen {
		layers = append(layers, overlayLayer{
			kind:        overlayCredentials,
			passthrough: passCredentialsHost,
			update: func(m *Model, msg tea.Msg) tea.Cmd {
				var cmd tea.Cmd
				m.accountCredentials, cmd = m.accountCredentials.Update(msg)
				return cmd
			},
		})
	}
	if m.accountOAuthConfigOpen {
		layers = append(layers, overlayLayer{
			kind:        overlayOAuthConfig,
			passthrough: passOAuthConfigHost,
			update: func(m *Model, msg tea.Msg) tea.Cmd {
				var cmd tea.Cmd
				m.accountOAuthConfig, cmd = m.accountOAuthConfig.Update(msg)
				return cmd
			},
		})
	}
	if m.accountRenameOpen {
		layers = append(layers, overlayLayer{
			kind:        overlayRename,
			passthrough: passRenameHost,
			update: func(m *Model, msg tea.Msg) tea.Cmd {
				var cmd tea.Cmd
				m.accountRename, cmd = m.accountRename.Update(msg)
				return cmd
			},
		})
	}
	if m.trashOpen {
		layers = append(layers, overlayLayer{
			kind:        overlayTrash,
			passthrough: passUnlessInput,
			update: func(m *Model, msg tea.Msg) tea.Cmd {
				var cmd tea.Cmd
				m.trash, cmd = m.trash.Update(msg)
				return cmd
			},
		})
	}
	if m.calendarManagerOpen {
		layers = append(layers, overlayLayer{
			kind:        overlayCalendarManager,
			passthrough: passCalendarManagerHost,
			update: func(m *Model, msg tea.Msg) tea.Cmd {
				var cmd tea.Cmd
				m.calendarManager, cmd = m.calendarManager.Update(msg)
				return cmd
			},
		})
	}
	if m.formOpen {
		layers = append(layers, overlayLayer{
			kind:        overlayForm,
			passthrough: passFormHost,
			update: func(m *Model, msg tea.Msg) tea.Cmd {
				var cmd tea.Cmd
				m.form, cmd = m.form.Update(msg)
				return cmd
			},
		})
	}
	if m.viewDialogOpen {
		layers = append(layers, overlayLayer{
			kind:        overlayViewDialog,
			passthrough: passUnlessInput,
			update: func(m *Model, msg tea.Msg) tea.Cmd {
				var cmd tea.Cmd
				m.viewDialog, cmd = m.viewDialog.Update(msg)
				return cmd
			},
		})
	}
	if m.dialogOpen {
		layers = append(layers, overlayLayer{
			kind:        overlayDialog,
			passthrough: passUnlessInput,
			update: func(m *Model, msg tea.Msg) tea.Cmd {
				var cmd tea.Cmd
				m.dialog, cmd = m.dialog.Update(msg)
				return cmd
			},
		})
	}
	return layers
}

// routeOverlay consults the top overlay only. A passthrough message
// goes to the main switch, not to a lower overlay. That matches the
// previous host allowlists: CalendarSavedMsg and ConfirmDialogResultMsg
// must reach the app, not the manager underneath a confirm.
func (m Model) routeOverlay(msg tea.Msg) (Model, tea.Cmd, bool) {
	layers := m.overlayStack()
	if len(layers) == 0 {
		return m, nil, false
	}
	top := layers[0]
	if top.passthrough(msg) {
		return m, nil, false
	}
	cmd := top.update(&m, msg)
	return m, cmd, true
}
