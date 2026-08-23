package tui

import (
	"context"
	"fmt"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
)

func (m Model) handleAccountReauthReady(msg accountReauthReadyMsg) (tea.Model, tea.Cmd) {
	m.oauthPending = false
	configured, ok := m.accounts[msg.accountID]
	if msg.err == nil && (!ok || configured.ID == 0 || !calendarAuthIsOAuth(configured.AuthType)) {
		msg.err = fmt.Errorf("account is no longer available for OAuth")
	}
	if msg.err != nil {
		if params, ok := m.accountSettingsParams(msg.accountID); ok {
			m.calendarManager = m.calendarManager.OpenAccount(params).SetSize(m.width, m.height)
			m.calendarManagerOpen = true
		}
		m.statusToken++
		m.syncStatus = "Re-authentication failed: " + msg.err.Error()
		return m, m.expireStatusAfter(8*time.Second, m.statusToken)
	}
	if msg.cred.OAuthClientID == "" || msg.cred.OAuthClientSecret == "" {
		m.accountOAuthConfig = NewAccountOAuthConfigDialogModel(
			msg.accountID, msg.name, msg.cred.OAuthClientID, m.theme,
		).SetSize(m.width, m.height)
		m.accountOAuthConfigOpen = true
		m.syncStatus = ""
		return m, nil
	}
	m.oauthPurpose = oauthFlowPurpose{
		accountID: msg.accountID, accountName: msg.name, cred: msg.cred,
	}
	return m.startOAuthFlow(msg.cred.OAuthClientID, msg.cred.OAuthClientSecret)
}

func (m Model) handleAccountReordered(msg AccountReorderedMsg) (tea.Model, tea.Cmd) {
	ids := slices.Clone(msg.IDs)
	m.pendingAccountOrder = make(map[int64]int64, len(ids))
	m.pendingAccountOrderIDs = ids
	for i, accountID := range ids {
		position := int64(i)
		m.pendingAccountOrder[accountID] = position
		for calendarID, info := range m.calendars {
			if info.AccountID == accountID {
				info.AccountOrder = position
				m.calendars[calendarID] = info
			}
		}
	}
	m.sidebar = m.sidebar.SetList(
		m.sidebar.List().SetItemsPreservingCursor(sortedCalendarListItems(m.calendars)),
	)
	if m.accountOrderSaveInFlight {
		return m, nil
	}
	return m, m.beginAccountOrderSave(ids)
}

func (m Model) handleAccountOrderSaved(msg accountOrderSavedMsg) (tea.Model, tea.Cmd) {
	m.accountOrderSaveInFlight = false
	latest := m.pendingAccountOrderIDs
	if len(latest) > 0 && !slices.Equal(msg.ids, latest) {
		saveLatest := m.beginAccountOrderSave(latest)
		if msg.err != nil {
			return m, tea.Batch(m.toast.Failed(msg.err.Error()), saveLatest)
		}
		return m, saveLatest
	}
	for i, id := range msg.ids {
		if m.pendingAccountOrder[id] == int64(i) {
			delete(m.pendingAccountOrder, id)
		}
	}
	m.pendingAccountOrderIDs = nil
	if msg.err != nil {
		return m, tea.Batch(m.toast.Failed(msg.err.Error()), m.loadCalendars())
	}
	return m, nil
}

func (m Model) handleAccountRenameRequested(msg AccountRenameRequestedMsg) (tea.Model, tea.Cmd) {
	if m.syncing || !m.accountRenameOpen {
		return m, nil
	}
	m.accountRenameOpen = false
	m.syncing = true
	m.syncStatus = "Renaming account…"
	request := msg
	return m, func() tea.Msg {
		renamed, err := m.app.Accounts.Rename(context.Background(), request.AccountID, request.Name)
		return accountRenameFinishedMsg{account: renamed, err: err}
	}
}

func (m Model) handleAccountSettingsRequested(msg AccountSettingsRequestedMsg) (tea.Model, tea.Cmd) {
	params, ok := m.accountSettingsParams(msg.AccountID)
	if !ok {
		return m, nil
	}
	m.calendarManager = m.calendarManager.OpenAccount(params).SetSize(m.width, m.height)
	m.calendarManagerOpen = true
	return m, nil
}

func (m Model) handleAccountSettingsManageRequested(msg AccountSettingsManageRequestedMsg) (tea.Model, tea.Cmd) {
	if m.syncing || m.oauthFlowOpen || m.oauthPending ||
		!m.calendarManagerOpen || m.calendarManager.ActiveAccountID() != msg.AccountID {
		return m, nil
	}
	if _, ok := m.accounts[msg.AccountID]; !ok {
		return m, nil
	}
	m.calendarManagerOpen = true
	m.accountManagementGeneration++
	m.pendingAccountManagementID = msg.AccountID
	m.syncing = true
	m.syncStatus = "Discovering calendars…"
	return m, tea.Batch(
		m.syncSpinner.Tick,
		m.discoverAccountCalendars(msg.AccountID, m.accountManagementGeneration),
	)
}

func (m Model) handleAccountSettingsSyncRequested(msg AccountSettingsSyncRequestedMsg) (tea.Model, tea.Cmd) {
	if m.syncing || !m.calendarManagerOpen ||
		m.calendarManager.ActiveAccountID() != msg.AccountID {
		return m, nil
	}
	params, ok := m.accountSettingsParams(msg.AccountID)
	if !ok {
		return m, nil
	}
	m.syncing = true
	m.syncStatus = "Syncing " + textsafe.Display(params.DisplayName) + "…"
	return m, tea.Batch(m.syncSpinner.Tick, m.runSyncAccount(msg.AccountID, params.DisplayName))
}

func (m Model) handleAccountSettingsRenameRequested(msg AccountSettingsRenameRequestedMsg) (tea.Model, tea.Cmd) {
	if m.syncing || !m.calendarManagerOpen ||
		m.calendarManager.ActiveAccountID() != msg.AccountID {
		return m, nil
	}
	configured, ok := m.accounts[msg.AccountID]
	if !ok {
		return m, nil
	}
	m.accountRename = newAccountRenameDialogModel(configured, m.theme).
		SetSize(m.width, m.height)
	m.accountRenameFromSettings = true
	m.accountRenameOpen = true
	return m, nil
}

func (m Model) handleAccountOAuthConfigSubmitted(msg AccountOAuthConfigSubmittedMsg) (tea.Model, tea.Cmd) {
	if m.oauthFlowOpen || m.oauthPending || !m.accountOAuthConfigOpen ||
		m.accountOAuthConfig.accountID != msg.AccountID {
		return m, nil
	}
	configured, ok := m.accounts[msg.AccountID]
	if !ok || !calendarAuthIsOAuth(configured.AuthType) {
		return m, nil
	}
	m.accountOAuthConfigOpen = false
	m.oauthPending = true
	m.syncStatus = "Preparing sign-in…"
	return m, m.prepareAccountReauth(configured, msg.ClientID, msg.ClientSecret)
}

func (m Model) handleAccountOAuthConfigClosed(msg AccountOAuthConfigClosedMsg) (tea.Model, tea.Cmd) {
	if !m.accountOAuthConfigOpen || m.accountOAuthConfig.accountID != msg.AccountID {
		return m, nil
	}
	m.accountOAuthConfigOpen = false
	m.oauthPending = false
	if params, ok := m.accountSettingsParams(msg.AccountID); ok {
		m.calendarManager = m.calendarManager.OpenAccount(params).SetSize(m.width, m.height)
		m.calendarManagerOpen = true
	}
	m.statusToken++
	m.syncStatus = "Authorization cancelled"
	return m, m.expireStatusAfter(6*time.Second, m.statusToken)
}

func (m Model) handleAccountSettingsReauthRequested(msg AccountSettingsReauthRequestedMsg) (tea.Model, tea.Cmd) {
	if m.syncing || m.oauthFlowOpen || m.oauthPending ||
		!m.calendarManagerOpen || m.calendarManager.ActiveAccountID() != msg.AccountID {
		return m, nil
	}
	configured, ok := m.accounts[msg.AccountID]
	if !ok || !calendarAuthIsOAuth(configured.AuthType) {
		return m, nil
	}
	m.calendarManagerOpen = false
	m.oauthPending = true
	m.syncStatus = "Preparing sign-in…"
	return m, m.prepareAccountReauth(configured, "", "")
}

func (m Model) handleAccountSettingsUpdateCredentialsRequested(msg AccountSettingsUpdateCredentialsRequestedMsg) (tea.Model, tea.Cmd) {
	if m.syncing || m.oauthFlowOpen || m.oauthPending ||
		!m.calendarManagerOpen || m.calendarManager.ActiveAccountID() != msg.AccountID {
		return m, nil
	}
	configured, ok := m.accounts[msg.AccountID]
	if !ok || !accountAuthIsBasicOrBearer(configured.AuthType) {
		return m, nil
	}
	m.calendarManagerOpen = false
	m.accountCredentials = NewAccountCredentialsDialogModel(
		configured.ID, configured.DisplayName, configured.AuthType, configured.Username, m.theme,
	).SetSize(m.width, m.height)
	m.accountCredentialsOpen = true
	return m, nil
}

func (m Model) handleAccountCredentialsUpdateSubmitted(msg AccountCredentialsUpdateSubmittedMsg) (tea.Model, tea.Cmd) {
	if m.syncing || m.oauthFlowOpen || m.oauthPending || !m.accountCredentialsOpen ||
		m.accountCredentials.accountID != msg.AccountID {
		return m, nil
	}
	configured, ok := m.accounts[msg.AccountID]
	if !ok || !accountAuthIsBasicOrBearer(configured.AuthType) {
		return m, nil
	}
	m.accountCredentialsOpen = false
	m.syncing = true
	m.syncStatus = "Updating credentials…"
	return m, tea.Batch(
		m.syncSpinner.Tick,
		m.updateAccountCredentials(configured, msg.Secret),
	)
}

func (m Model) handleAccountCredentialsUpdateClosed(msg AccountCredentialsUpdateClosedMsg) (tea.Model, tea.Cmd) {
	if !m.accountCredentialsOpen || m.accountCredentials.accountID != msg.AccountID {
		return m, nil
	}
	m.accountCredentialsOpen = false
	m.statusToken++
	m.syncStatus = "Credential update cancelled"
	m = m.reopenAccountSettings(msg.AccountID)
	return m, m.expireStatusAfter(6*time.Second, m.statusToken)
}

func (m Model) handleAccountSettingsRemoveRequested(msg AccountSettingsRemoveRequestedMsg) (tea.Model, tea.Cmd) {
	if m.syncing || !m.calendarManagerOpen ||
		m.calendarManager.ActiveAccountID() != msg.AccountID {
		return m, nil
	}
	params, ok := m.accountSettingsParams(msg.AccountID)
	if !ok {
		return m, nil
	}
	name := textsafe.Display(params.DisplayName)
	message := fmt.Sprintf(
		"Remove account “%s” from Chroncal?\n\n%d downloaded %s will be kept as local calendars.\nRemote links and stored sign-in will be removed.",
		name,
		params.CalendarCount,
		pluralize(params.CalendarCount, "calendar", "calendars"),
	)
	if draft := m.calendarManager.LocalDraft(); m.calendarManagerOpen &&
		draft != nil && draft.AccountID == msg.AccountID {
		message += "\nAny unsaved calendar edits will be discarded."
	}
	return m.armConfirm(
		pendingAction{
			kind:   pendingActionAccountRemove,
			target: pendingTarget{accountID: msg.AccountID},
			label:  name,
		},
		NewConfirmDialogModel(message, "Remove Account", m.theme).
			Destructive(),
	), nil
}

func (m Model) handleAccountSettingsClosed(msg AccountSettingsClosedMsg) (tea.Model, tea.Cmd) {
	if m.syncing || m.oauthPending {
		return m, nil
	}
	if m.calendarManagerOpen {
		m.calendarManager = m.calendarManager.CloseAccount()
	}
	return m, nil
}

func (m Model) handleCalendarDiscoveryRequested(msg CalendarDiscoveryRequestedMsg) (tea.Model, tea.Cmd) {
	if m.syncing || m.oauthFlowOpen {
		return m, nil
	}
	if calendarAuthIsOAuth(msg.AuthType) {
		m.oauthPurpose = oauthFlowPurpose{
			calendarDiscovery:    true,
			calendarDiscoveryMsg: msg,
		}
		return m.startOAuthFlow(msg.OAuthClientID, msg.OAuthClientSecret)
	}
	cred := auth.Credential{Username: msg.Username}
	if msg.AuthType == "bearer" {
		cred.AccessToken = msg.Secret
	} else {
		cred.Password = msg.Secret
	}
	m.syncing = true
	m.syncStatus = "Adding account…"
	return m, tea.Batch(m.syncSpinner.Tick, m.connectAndDiscoverCalendar(msg, cred))
}

func (m Model) handleAccountDiscoveryReady(msg accountDiscoveryReadyMsg) (tea.Model, tea.Cmd) {
	m.syncing = false
	if msg.err != nil {
		m.calendarManagerOpen = true
		m.statusToken++
		m.syncStatus = "Couldn’t add account: " + msg.err.Error()
		m.calendarManager = m.calendarManager.WithTestStatus(lipgloss.NewStyle().Foreground(m.theme.Error), "✗ "+msg.err.Error())
		return m, m.expireStatusAfter(10*time.Second, m.statusToken)
	}
	m.pendingDiscoveryAccountID = msg.discovery.Account.ID
	m.pendingDiscoveryCreated = msg.createdAccount
	m.calendarManager = m.calendarManager.ShowDiscovery(msg.discovery).SetSize(m.width, m.height)
	if msg.createdAccount {
		paths := make([]string, 0, len(msg.discovery.Calendars))
		for _, remote := range msg.discovery.Calendars {
			if remote.Importable && !remote.Missing {
				paths = append(paths, remote.Path)
			}
		}
		m.calendarManagerOpen = false
		m.syncing = true
		m.syncStatus = "Importing and syncing all calendars…"
		return m, tea.Batch(
			m.syncSpinner.Tick,
			m.importAndSyncAccountCalendars(paths),
		)
	}
	m.calendarManagerOpen = true
	m.syncStatus = ""
	return m, m.loadCalendars()
}

func (m Model) handleAccountCalendarPickerClosed(msg AccountCalendarPickerClosedMsg) (tea.Model, tea.Cmd) {
	m.statusToken++
	if m.calendarManager.ManagingAccountCalendars() {
		m.calendarManager = m.calendarManager.HideDiscovery()
		m.calendarManagerOpen = true
		m.syncStatus = "Calendar discovery cancelled"
		return m, m.expireStatusAfter(6*time.Second, m.statusToken)
	}
	m.calendarManagerOpen = false
	if m.pendingDiscoveryCreated && m.pendingDiscoveryAccountID != 0 {
		accountID := m.pendingDiscoveryAccountID
		m.pendingDiscoveryAccountID = 0
		m.pendingDiscoveryCreated = false
		m.syncing = true
		m.syncStatus = "Cancelling calendar discovery…"
		return m, tea.Batch(m.syncSpinner.Tick, m.discardDiscoveryAccount(accountID))
	}
	m.pendingDiscoveryAccountID = 0
	m.pendingDiscoveryCreated = false
	m.syncStatus = "Calendar discovery cancelled"
	return m, m.expireStatusAfter(6*time.Second, m.statusToken)
}

func (m Model) handleAccountCalendarsImportRequested(msg AccountCalendarsImportRequestedMsg) (tea.Model, tea.Cmd) {
	if m.calendarManager.DiscoveryPicker() == nil ||
		msg.AccountID != m.calendarManager.DiscoveryPicker().discovery.Account.ID {
		return m, m.toast.Failed("calendar selection no longer matches the open account")
	}
	m.calendarManagerOpen = false
	m.syncing = true
	m.syncStatus = "Importing and syncing selected calendars…"
	return m, tea.Batch(m.syncSpinner.Tick, m.importAndSyncAccountCalendars(msg.Paths))
}

func (m Model) handleAccountCalendarsReconcileRequested(msg AccountCalendarsReconcileRequestedMsg) (tea.Model, tea.Cmd) {
	selection, candidates, err := m.prepareAccountCalendarSelection(msg)
	if err != nil {
		return m, m.toast.Failed(err.Error())
	}
	if len(selection.removed) == 0 {
		m.calendarManagerOpen = true
		m.syncing = true
		m.syncStatus = "Adding and syncing selected calendars…"
		return m, tea.Batch(
			m.syncSpinner.Tick,
			m.reconcileAndSyncAccountCalendars(selection),
		)
	}
	if len(candidates) > 0 {
		labels := make([]string, len(candidates))
		for i, candidate := range candidates {
			labels[i] = textsafe.Display(candidate.name)
		}
		return m.armChoice(
			pendingAction{
				kind: pendingActionAccountSelectionPromote,
				target: pendingTarget{
					selection:    selection,
					defaultCands: candidates,
				},
			},
			NewChoiceDialogModel(
				"The default calendar is being removed.\n\nChoose a new default before saving these changes:",
				m.theme,
				labels...,
			),
		), nil
	}
	m = m.showAccountCalendarRemovalConfirmation(selection)
	return m, nil
}

func (m Model) handleCalendarDiscoveryDiscarded(msg calendarDiscoveryDiscardedMsg) (tea.Model, tea.Cmd) {
	m.syncing = false
	m.calendarManager = m.calendarManager.HideDiscovery()
	m.statusToken++
	if msg.err != nil {
		m.syncStatus = "Calendar discovery cancelled; cleanup failed: " + msg.err.Error()
	} else {
		m.syncStatus = "Calendar discovery cancelled"
	}
	return m, tea.Batch(
		m.loadCalendars(),
		m.expireStatusAfter(8*time.Second, m.statusToken),
	)
}

func (m Model) handleAccountImportFinished(msg accountImportFinishedMsg) (tea.Model, tea.Cmd) {
	m.syncing = false
	m.statusToken++
	switch {
	case msg.err != nil:
		m.calendarManagerOpen = m.calendarManager.DiscoveryPicker() != nil
		m.syncStatus = "Calendar import failed: " + msg.err.Error()
	case msg.created == 0 && msg.existing == 0:
		m.syncStatus = "No calendars selected"
		if m.pendingDiscoveryCreated && m.pendingDiscoveryAccountID != 0 {
			accountID := m.pendingDiscoveryAccountID
			m.pendingDiscoveryAccountID = 0
			m.pendingDiscoveryCreated = false
			m.syncing = true
			m.syncStatus = "Cancelling calendar discovery…"
			return m, tea.Batch(m.syncSpinner.Tick, m.discardDiscoveryAccount(accountID))
		}
		m.pendingDiscoveryAccountID = 0
		m.pendingDiscoveryCreated = false
	case msg.syncErr != nil:
		m.pendingDiscoveryAccountID = 0
		m.pendingDiscoveryCreated = false
		m.syncStatus = fmt.Sprintf("Imported %d calendar(s); first sync failed: %v", msg.created, msg.syncErr)
	case msg.created == 0:
		m.pendingDiscoveryAccountID = 0
		m.pendingDiscoveryCreated = false
		m.syncStatus = fmt.Sprintf("%d selected calendar(s) already imported", msg.existing)
	default:
		m.pendingDiscoveryAccountID = 0
		m.pendingDiscoveryCreated = false
		m.syncStatus = fmt.Sprintf("Imported and synced %d calendar(s)", msg.synced)
	}
	if msg.err == nil {
		m.calendarManager = m.calendarManager.HideDiscovery()
		m.syncStatus = appendImportWarnings(m.syncStatus, msg.warnings)
	}
	return m, tea.Batch(
		m.loadCalendars(),
		m.loadEvents(),
		m.expireStatusAfter(10*time.Second, m.statusToken),
	)
}

func (m Model) handleAccountSelectionFinished(msg accountSelectionFinishedMsg) (tea.Model, tea.Cmd) {
	m.syncing = false
	m.statusToken++
	if msg.err != nil {
		if msg.accountManagement {
			m.calendarManagerOpen = m.calendarManager.DiscoveryPicker() != nil
		} else {
			m.calendarManagerOpen = m.calendarManager.DiscoveryPicker() != nil
		}
		m.syncStatus = "Calendar changes failed: " + msg.err.Error()
		return m, m.expireStatusAfter(10*time.Second, m.statusToken)
	}

	m.pendingDiscoveryAccountID = 0
	m.pendingDiscoveryCreated = false
	if msg.accountManagement {
		m.calendarManager = m.calendarManager.HideDiscovery()
		m.calendarManagerOpen = !msg.accountRemoved
	} else {
		m.calendarManager = m.calendarManager.HideDiscovery()
		m.calendarManagerOpen = !msg.removedCurrent && !msg.accountRemoved
	}
	if draft := m.calendarManager.LocalDraft(); draft != nil &&
		slices.Contains(msg.removedIDs, draft.ID) {
		m.calendarManagerOpen = false
	}
	switch {
	case msg.accountRemoved:
		m.syncStatus = fmt.Sprintf("Removed account and %d calendar(s) from Chroncal", msg.removed)
	case msg.syncErr != nil:
		m.syncStatus = fmt.Sprintf(
			"Updated calendars: added %d, removed %d; first sync failed: %v",
			msg.created, msg.removed, msg.syncErr,
		)
	default:
		m.syncStatus = fmt.Sprintf(
			"Updated calendars: added %d, removed %d",
			msg.created, msg.removed,
		)
	}
	m.syncStatus = appendImportWarnings(m.syncStatus, msg.warnings)
	return m, tea.Batch(
		m.loadCalendars(),
		m.loadEvents(),
		m.expireStatusAfter(10*time.Second, m.statusToken),
	)
}

func (m Model) handleOauthFlowDone(msg oauthFlowDoneMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.oauthFlow, cmd = m.oauthFlow.Update(msg)
	switch m.oauthFlow.State() {
	case OAuthFlowDone:
		m.oauthFlowOpen = false
		if m.oauthPurpose.calendarDiscovery {
			m.syncing = true
			m.syncStatus = "Authorized; discovering calendars…"
			return m, tea.Batch(m.syncSpinner.Tick, m.finishOAuthCalendarDiscovery(msg.result))
		}
		m.oauthPending = true
		return m, m.finishOAuthReauth(msg.result)
	case OAuthFlowCancelled:
		m.oauthFlowOpen = false
		m.statusToken++
		if m.oauthPurpose.calendarDiscovery {
			m.calendarManagerOpen = true
			m.syncStatus = "Authorization cancelled"
			return m, m.expireStatusAfter(6*time.Second, m.statusToken)
		}
		m = m.reopenAccountSettings(m.oauthPurpose.accountID)
		m.syncStatus = "Authorization cancelled"
		return m, m.expireStatusAfter(6*time.Second, m.statusToken)
	default:
		// Failed: the modal stays up showing the error; esc closes it.
		// Abort so the chained Waiting-phase context cancel func is
		// invoked now rather than leaking until process exit (Wait's
		// deferred Close already released the listener).
		m.oauthFlow.Abort()
	}
	return m, cmd
}

func (m Model) handleOauthFlowClosed(msg oauthFlowClosedMsg) (tea.Model, tea.Cmd) {
	m.oauthFlowOpen = false
	if m.oauthPurpose.calendarDiscovery {
		m.calendarManagerOpen = true
	} else {
		m = m.reopenAccountSettings(m.oauthPurpose.accountID)
	}
	return m, nil
}
