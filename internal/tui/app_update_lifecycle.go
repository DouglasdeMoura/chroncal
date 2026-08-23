package tui

import tea "charm.land/bubbletea/v2"

// dispatchLifecycleMsg handles async finish messages that must run even
// when an overlay is open. The overlay stack must not swallow them.
func (m Model) dispatchLifecycleMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case syncFinishedMsg:
		next, cmd := m.finishSync(msg)
		return next, cmd, true
	case oauthCredentialStoredMsg:
		next, cmd := m.finishOAuthCredentialStore(msg)
		return next, cmd, true
	case accountCredentialStoredMsg:
		next, cmd := m.finishAccountCredentialStore(msg)
		return next, cmd, true
	case accountReauthReadyMsg:
		next, cmd := m.handleAccountReauthReady(msg)
		return next, cmd, true
	case accountManagementDiscoveryReadyMsg:
		next, cmd := m.finishAccountManagementDiscovery(msg)
		return next, cmd, true
	case calendarMoveDiscoveryReadyMsg:
		next, cmd := m.finishCalendarMoveDiscovery(msg)
		return next, cmd, true
	case calendarMoveFinishedMsg:
		next, cmd := m.finishCalendarMove(msg)
		return next, cmd, true
	case accountRenameFinishedMsg:
		next, cmd := m.finishAccountRename(msg)
		return next, cmd, true
	case accountRemovalFinishedMsg:
		next, cmd := m.finishAccountRemoval(msg)
		return next, cmd, true
	default:
		return m, nil, false
	}
}
