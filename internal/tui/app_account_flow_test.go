package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
)

func openCalendarManagerForTest(m *Model, params CalendarDialogParams) {
	m.calendarManager = NewCalendarManagerModel(m.calendars, m.hiddenCalendars, newThemedHelp(m.theme))
	m.calendarManager.theme = m.theme
	m.calendarManager = m.calendarManager.SetSize(m.width, m.height).OpenCalendar(params)
	m.calendarManagerOpen = true
}

func accountManagerOpen(m Model) bool {
	_, ok := m.calendarManager.AccountSettings()
	return m.calendarManagerOpen && ok
}

func TestAppCalendarDiscoveryOpensPickerInsideCalendarDialog(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	openCalendarManagerForTest(&m, CalendarDialogParams{})
	m.calendarManagerOpen = true
	m.syncing = true

	updated, _ := m.Update(accountDiscoveryReadyMsg{discovery: pickerDiscovery()})
	m = updated.(Model)
	if m.syncing || !m.calendarManagerOpen || m.calendarManager.calendarForm.discoveryPicker == nil {
		t.Fatalf("discovery transition: syncing=%v calendarDialog=%v picker=%v",
			m.syncing, m.calendarManagerOpen, m.calendarManager.calendarForm.discoveryPicker != nil)
	}
}

func TestAppNewAccountDiscoveryImportsAllUsableCalendarsImmediately(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	openCalendarManagerForTest(&m, CalendarDialogParams{})
	m.calendarManagerOpen = true
	m.syncing = true

	updated, cmd := m.Update(accountDiscoveryReadyMsg{
		discovery:      pickerDiscovery(),
		createdAccount: true,
	})
	m = updated.(Model)
	if cmd == nil || !m.syncing || m.calendarManagerOpen {
		t.Fatalf("automatic import transition: cmd=%v syncing=%v calendarDialog=%v",
			cmd, m.syncing, m.calendarManagerOpen)
	}
	if !strings.Contains(m.syncStatus, "all calendars") {
		t.Fatalf("automatic import status = %q", m.syncStatus)
	}
}

func TestAppNewAccountImportCompletionClearsDiscoveryPicker(t *testing.T) {
	m := NewModel(nil, "")
	openCalendarManagerForTest(&m, CalendarDialogParams{})
	m.calendarManager = m.calendarManager.ShowDiscovery(pickerDiscovery())
	m.pendingDiscoveryAccountID = 7
	m.pendingDiscoveryCreated = true
	m.syncing = true

	updated, _ := m.Update(accountImportFinishedMsg{created: 2, synced: 2})
	m = updated.(Model)
	if m.syncing || m.pendingDiscoveryCreated || m.pendingDiscoveryAccountID != 0 {
		t.Fatalf("import completion state: syncing=%v pending=%v/%d",
			m.syncing, m.pendingDiscoveryCreated, m.pendingDiscoveryAccountID)
	}
	if m.calendarManager.calendarForm.discoveryPicker != nil {
		t.Fatal("successful automatic import retained the discovery picker")
	}
}

func TestAppNewAccountWithoutUsableCalendarsSchedulesRollback(t *testing.T) {
	m := NewModel(nil, "")
	openCalendarManagerForTest(&m, CalendarDialogParams{})
	m.calendarManager = m.calendarManager.ShowDiscovery(pickerDiscovery())
	m.pendingDiscoveryAccountID = 7
	m.pendingDiscoveryCreated = true
	m.syncing = true

	updated, cmd := m.Update(accountImportFinishedMsg{})
	m = updated.(Model)
	if cmd == nil || !m.syncing || m.pendingDiscoveryCreated || m.pendingDiscoveryAccountID != 0 {
		t.Fatalf("rollback state: cmd=%v syncing=%v pending=%v/%d",
			cmd, m.syncing, m.pendingDiscoveryCreated, m.pendingDiscoveryAccountID)
	}
	if m.syncStatus != "Cancelling calendar discovery…" {
		t.Fatalf("rollback status = %q", m.syncStatus)
	}

	updated, _ = m.Update(calendarDiscoveryDiscardedMsg{})
	m = updated.(Model)
	if m.syncing || m.syncStatus != "Calendar discovery cancelled" {
		t.Fatalf("rollback completion: syncing=%v status=%q", m.syncing, m.syncStatus)
	}
	if m.calendarManager.calendarForm.discoveryPicker != nil {
		t.Fatal("rolled-back automatic import retained the discovery picker")
	}
}

func TestAppOAuthCalendarDiscoveryRecordsIntegratedPurpose(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	openCalendarManagerForTest(&m, CalendarDialogParams{})
	m.calendarManagerOpen = true
	req := CalendarDiscoveryRequestedMsg{
		ServerURL: "https://example.com/caldav", Username: "me@example.com",
		AuthType: "oauth2", OAuthClientID: "client.apps", OAuthClientSecret: "secret",
	}
	updated, cmd := m.Update(req)
	m = updated.(Model)
	if cmd == nil || !m.oauthFlowOpen || !m.oauthPurpose.calendarDiscovery {
		t.Fatalf("OAuth discovery state: flow=%v purpose=%v cmd=%v",
			m.oauthFlowOpen, m.oauthPurpose.calendarDiscovery, cmd)
	}
	if m.oauthPurpose.calendarDiscoveryMsg.Username != "me@example.com" {
		t.Fatalf("OAuth purpose lost discovery request: %+v", m.oauthPurpose.calendarDiscoveryMsg)
	}
	m.oauthFlow.Abort()
}

func TestAppCalendarDiscoveryCancelSchedulesTemporaryAccountCleanup(t *testing.T) {
	m := NewModel(nil, "")
	m.calendarManagerOpen = true
	m.pendingDiscoveryAccountID = 7
	m.pendingDiscoveryCreated = true

	updated, cmd := m.Update(AccountCalendarPickerClosedMsg{})
	m = updated.(Model)
	if cmd == nil || m.calendarManagerOpen || !m.syncing {
		t.Fatalf("cancel state: cmd=%v calendarDialog=%v syncing=%v", cmd, m.calendarManagerOpen, m.syncing)
	}
	if m.pendingDiscoveryAccountID != 0 || m.pendingDiscoveryCreated {
		t.Fatalf("temporary account state not cleared: id=%d created=%v",
			m.pendingDiscoveryAccountID, m.pendingDiscoveryCreated)
	}
}

func TestAppOAuthCalendarDiscoveryCancelReturnsToConnectionStep(t *testing.T) {
	m := NewModel(nil, "")
	m.oauthFlowOpen = true
	m.oauthFlow.state = OAuthFlowWaiting
	m.oauthPurpose.calendarDiscovery = true

	updated, _ := m.Update(oauthFlowDoneMsg{err: context.Canceled})
	m = updated.(Model)
	if m.oauthFlowOpen || !m.calendarManagerOpen {
		t.Fatalf("OAuth cancel state: flow=%v calendarDialog=%v", m.oauthFlowOpen, m.calendarManagerOpen)
	}
}

func TestCalendarDiscoveryAccountNameUsesFriendlyUniqueSuggestion(t *testing.T) {
	accounts := []account.Account{
		{ID: 3, Name: "maildodouglas@gmail.com", DisplayName: "Google", Username: "maildodouglas@gmail.com"},
	}
	if got := calendarDiscoveryAccountName(accounts, "other@gmail.com"); got != "Google (2)" {
		t.Fatalf("second Google account name = %q, want %q", got, "Google (2)")
	}
	if got := calendarDiscoveryAccountName(accounts, "douglas.moura@jaya.tech"); got != "Jaya" {
		t.Fatalf("workspace account name = %q, want %q", got, "Jaya")
	}
}

func TestExistingCalendarDiscoveryAccountMatchesConnectionIdentity(t *testing.T) {
	accounts := []account.Account{
		{ID: 3, DisplayName: "First", ServerURL: "https://example.com/dav", AuthType: "basic", Username: "other"},
		{ID: 7, DisplayName: "Existing", ServerURL: "https://example.com/dav", AuthType: "basic", Username: "me@example.com"},
	}
	got, ok := existingCalendarDiscoveryAccount(
		accounts,
		CalendarDiscoveryRequestedMsg{
			ServerURL: " https://example.com/dav ",
			AuthType:  "BASIC",
			Username:  "me@example.com",
		},
	)
	if !ok || got.ID != 7 {
		t.Fatalf("matched account = %+v, %v; want ID 7", got, ok)
	}
}

func TestAppAccountSettingsRequestOpensCanonicalPanel(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.accounts = map[int64]account.Account{
		7: {
			ID: 7, DisplayName: "Personal Google",
			ServerURL: "https://apidata.googleusercontent.com/caldav/v2/",
			AuthType:  "oauth2", Username: "douglas@example.com",
		},
	}
	m.calendars = map[int64]CalendarInfo{
		2: {Name: "Personal", AccountID: 7},
		3: {Name: "Família", AccountID: 7, LastSyncError: "token expired"},
		9: {Name: "Local"},
	}

	updated, cmd := m.Update(AccountSettingsRequestedMsg{AccountID: 7})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("opening Account settings returned command %T", cmd())
	}
	if !accountManagerOpen(m) {
		t.Fatal("Account settings did not open")
	}
	if !m.calendarManagerOpen || m.syncing || m.oauthPending || m.oauthFlowOpen {
		t.Fatalf("opening settings state: manager=%v syncing=%v oauthPending=%v oauth=%v",
			m.calendarManagerOpen, m.syncing, m.oauthPending, m.oauthFlowOpen)
	}
	if got := m.calendarManager.accountSettings.params; got.AccountID != 7 || got.DisplayName != "Personal Google" ||
		got.Username != "douglas@example.com" || got.Provider != "Google Account" ||
		got.ServerURL != "https://apidata.googleusercontent.com/caldav/v2/" ||
		got.CalendarCount != 2 || got.AttentionCount != 1 || got.AuthType != "oauth2" {
		t.Fatalf("Account settings params = %+v", got)
	}
}

func TestAppAccountSettingsRemoveConfirmsAccountIdentityAndKeepsCalendarsLocal(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.accounts = map[int64]account.Account{
		7: {ID: 7, DisplayName: "Personal Google", AuthType: "oauth2"},
	}
	m.calendars = map[int64]CalendarInfo{
		2: {Name: "Personal", AccountID: 7},
		3: {Name: "Família", AccountID: 7},
	}
	openCalendarManagerForTest(&m, CalendarDialogParams{
		ID: 2, AccountID: 7, AccountName: "Personal Google",
		Name: "Personal", Color: "#a6e3a1", RemoteLinked: true,
	})
	m.calendarManagerOpen = true // Edit Calendar remains underneath Account settings.
	m.calendarManager.calendarForm.form.Field(cdIdxName).(*TextField).SetValue("Unsaved personal rename")
	updated, _ := m.Update(AccountSettingsRequestedMsg{AccountID: 7})
	m = updated.(Model)

	updated, cmd := m.Update(AccountSettingsRemoveRequestedMsg{AccountID: 7})
	m = updated.(Model)
	if cmd != nil || !m.confirmOpen || !accountManagerOpen(m) ||
		m.pendingAccountRemoveID != 7 || m.pendingAccountRemoveName != "Personal Google" {
		t.Fatalf("remove request: cmd=%v confirm=%v settings=%v pending=%d/%q",
			cmd, m.confirmOpen, accountManagerOpen(m),
			m.pendingAccountRemoveID, m.pendingAccountRemoveName)
	}
	plain := m.confirmDialog.form.Field(0).(*StaticField).Value()
	for _, want := range []string{
		"Remove account “Personal Google” from Chroncal?",
		"2 downloaded calendars will be kept as local calendars.",
		"Remote links and stored sign-in will be removed.",
		"Any unsaved calendar edits will be discarded.",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("remove confirmation missing %q:\n%s", want, plain)
		}
	}

	updated, cmd = m.Update(ConfirmDialogResultMsg{Confirmed: false})
	m = updated.(Model)
	if cmd != nil || m.confirmOpen || !accountManagerOpen(m) ||
		m.pendingAccountRemoveID != 0 || m.pendingAccountRemoveName != "" {
		t.Fatalf("cancel removal: cmd=%v confirm=%v settings=%v pending=%d/%q",
			cmd, m.confirmOpen, accountManagerOpen(m),
			m.pendingAccountRemoveID, m.pendingAccountRemoveName)
	}

	updated, _ = m.Update(AccountSettingsRemoveRequestedMsg{AccountID: 7})
	m = updated.(Model)
	updated, cmd = m.Update(ConfirmDialogResultMsg{Confirmed: true})
	m = updated.(Model)
	if cmd == nil || !m.syncing || accountManagerOpen(m) ||
		m.pendingAccountRemoveID != 0 || m.pendingAccountRemoveName != "" {
		t.Fatalf("confirm removal: cmd=%v syncing=%v settings=%v pending=%d/%q",
			cmd == nil, m.syncing, accountManagerOpen(m),
			m.pendingAccountRemoveID, m.pendingAccountRemoveName)
	}

	updated, reload := m.Update(accountRemovalFinishedMsg{accountID: 7, name: "Personal Google"})
	m = updated.(Model)
	if reload == nil || m.syncing || m.calendarManagerOpen ||
		!strings.Contains(m.syncStatus, "downloaded calendars are now local") {
		t.Fatalf("remove completion: reload=%v syncing=%v calendar=%v status=%q",
			reload == nil, m.syncing, m.calendarManagerOpen, m.syncStatus)
	}
}

func TestAppAccountRemovalCompletionPreservesUnrelatedCalendarEdit(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.syncing = true
	openCalendarManagerForTest(&m, CalendarDialogParams{
		ID: 9, AccountID: 8, AccountName: "Work",
		Name: "Work", Color: "#89b4fa", RemoteLinked: true,
	})
	m.calendarManager.calendarForm.form.Field(cdIdxName).(*TextField).SetValue("Unsaved work rename")
	m.calendarManagerOpen = true
	m.calendarManager = m.calendarManager.OpenAccount(AccountSettingsParams{
		AccountID: 8, DisplayName: "Work", Username: "work@example.com",
	})
	m.calendarManagerOpen = true

	updated, cmd := m.Update(accountRemovalFinishedMsg{
		accountID: 7, name: "Personal Google",
	})
	m = updated.(Model)
	if cmd == nil || m.syncing || !m.calendarManagerOpen || !accountManagerOpen(m) {
		t.Fatalf("unrelated completion: cmd=%v syncing=%v calendar=%v settings=%v",
			cmd == nil, m.syncing, m.calendarManagerOpen, accountManagerOpen(m))
	}
	if got := m.calendarManager.calendarForm.form.Field(cdIdxName).(*TextField).Value(); got != "Unsaved work rename" {
		t.Fatalf("unrelated edit changed to %q", got)
	}

	m.syncing = true
	updated, _ = m.Update(accountRemovalFinishedMsg{
		accountID: 7, name: "Personal Google", err: errors.New("keyring locked"),
	})
	m = updated.(Model)
	if !accountManagerOpen(m) || m.calendarManager.accountSettings.params.AccountID != 8 {
		t.Fatalf("failed stale removal replaced newer account settings: open=%v account=%d",
			accountManagerOpen(m), m.calendarManager.accountSettings.params.AccountID)
	}
}

func TestAppAccountSettingsManageUsesAccountOnlyDiscovery(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.accounts = map[int64]account.Account{
		7: {ID: 7, DisplayName: "Personal Google", AuthType: "oauth2", Username: "douglas@example.com"},
	}
	m.calendars = map[int64]CalendarInfo{
		2: {Name: "Personal", AccountID: 7},
		3: {Name: "Família", AccountID: 7},
	}
	updated, _ := m.Update(AccountSettingsRequestedMsg{AccountID: 7})
	m = updated.(Model)

	updated, cmd := m.Update(AccountSettingsManageRequestedMsg{AccountID: 7})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("Manage did not start account discovery")
	}
	if !m.syncing || accountManagerOpen(m) || m.accountCalendarManagerOpen || m.calendarManagerOpen {
		t.Fatalf("Manage start: syncing=%v settings=%v manager=%v calendarDialog=%v",
			m.syncing, accountManagerOpen(m), m.accountCalendarManagerOpen, m.calendarManagerOpen)
	}
	generation := m.accountManagementGeneration
	if generation == 0 || m.pendingAccountManagementID != 7 {
		t.Fatalf("management identity: generation=%d account=%d", generation, m.pendingAccountManagementID)
	}

	// Lifecycle completions must bypass whichever overlay happens to be open.
	updated, _ = m.Update(AccountSettingsRequestedMsg{AccountID: 7})
	m = updated.(Model)
	if !accountManagerOpen(m) {
		t.Fatal("precondition: Account settings did not reopen during discovery")
	}

	discovery := pickerDiscovery()
	discovery.Account.ID = 7
	updated, _ = m.Update(accountManagementDiscoveryReadyMsg{
		discovery: discovery, accountID: 7, generation: generation,
	})
	m = updated.(Model)
	if m.syncing || !m.accountCalendarManagerOpen || m.calendarManagerOpen {
		t.Fatalf("Manage completion: syncing=%v manager=%v calendarDialog=%v",
			m.syncing, m.accountCalendarManagerOpen, m.calendarManagerOpen)
	}
	if !m.accountCalendarManager.manage || m.accountCalendarManager.discovery.Account.ID != 7 {
		t.Fatalf("management picker = %+v", m.accountCalendarManager)
	}

	updated, _ = m.Update(calendarsLoadedMsg{
		calendars: m.calendars,
		accounts: map[int64]account.Account{
			7: {ID: 7, DisplayName: "Reloaded Google", AuthType: "oauth2"},
		},
	})
	m = updated.(Model)
	if got := m.accounts[7].DisplayName; got != "Reloaded Google" {
		t.Fatalf("manager swallowed calendar reload: account name = %q", got)
	}

	updated, cmd = m.Update(AccountCalendarsReconcileRequestedMsg{
		AccountID: 7, SelectedPaths: []string{"/personal/"},
	})
	m = updated.(Model)
	if cmd == nil || !m.syncing || m.accountCalendarManagerOpen || m.calendarManagerOpen {
		t.Fatalf("Manage save: cmd=%v syncing=%v manager=%v calendarDialog=%v",
			cmd == nil, m.syncing, m.accountCalendarManagerOpen, m.calendarManagerOpen)
	}
	updated, _ = m.Update(accountSelectionFinishedMsg{accountManagement: true})
	m = updated.(Model)
	if m.accountCalendarManagerOpen || m.calendarManagerOpen {
		t.Fatalf("Manage completion reopened unrelated dialog: manager=%v calendarDialog=%v",
			m.accountCalendarManagerOpen, m.calendarManagerOpen)
	}
}

func TestAppAccountSettingsRenameOpensAccountScopedDialog(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.accounts = map[int64]account.Account{
		7: {ID: 7, DisplayName: "Personal Google", AuthType: "oauth2", Username: "douglas@example.com"},
	}
	openCalendarManagerForTest(&m, CalendarDialogParams{
		ID: 2, AccountID: 7, AccountName: "Personal Google",
		Name: "Personal", Color: "#a6e3a1", RemoteLinked: true,
	})
	m.calendarManager.calendarForm.form.Field(cdIdxName).(*TextField).SetValue("Unsaved calendar title")
	m.calendarManagerOpen = true
	updated, _ := m.Update(AccountSettingsRequestedMsg{AccountID: 7})
	m = updated.(Model)

	updated, cmd := m.Update(AccountSettingsRenameRequestedMsg{AccountID: 7})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("Rename open returned command %T", cmd())
	}
	if !accountManagerOpen(m) || !m.accountRenameOpen {
		t.Fatalf("Rename open: settings=%v rename=%v", accountManagerOpen(m), m.accountRenameOpen)
	}

	updated, _ = m.Update(accountRenameCancelledMsg{})
	m = updated.(Model)
	if m.accountRenameOpen || !accountManagerOpen(m) {
		t.Fatalf("Rename cancel: settings=%v rename=%v", accountManagerOpen(m), m.accountRenameOpen)
	}

	updated, _ = m.Update(AccountSettingsRenameRequestedMsg{AccountID: 7})
	m = updated.(Model)
	m.accountRename.form.Field(0).(*TextField).SetValue("Attempted Google")
	updated, cmd = m.Update(AccountRenameRequestedMsg{AccountID: 7, Name: "Attempted Google"})
	m = updated.(Model)
	if cmd == nil || !m.syncing || m.accountRenameOpen {
		t.Fatalf("Rename submit: cmd=%v syncing=%v rename=%v", cmd == nil, m.syncing, m.accountRenameOpen)
	}
	updated, _ = m.Update(accountRenameFinishedMsg{err: errors.New("name conflict")})
	m = updated.(Model)
	if !m.accountRenameOpen || m.accountRename.form.Field(0).(*TextField).Value() != "Attempted Google" {
		t.Fatalf("Rename failure lost form: open=%v value=%q",
			m.accountRenameOpen, m.accountRename.form.Field(0).(*TextField).Value())
	}
	if got := m.accountRename.form.Error(); got != "name conflict" {
		t.Fatalf("Rename field error = %q, want name conflict", got)
	}
	m.syncStatus = "Account rename failed"
	m.statusToken = 9
	updated, _ = m.Update(syncStatusExpiredMsg{token: 9})
	m = updated.(Model)
	if m.syncStatus != "" {
		t.Fatalf("Rename dialog swallowed status expiry: %q", m.syncStatus)
	}
	updated, _ = m.Update(accountRenameCancelledMsg{})
	m = updated.(Model)

	updated, _ = m.Update(AccountSettingsRenameRequestedMsg{AccountID: 7})
	m = updated.(Model)
	updated, cmd = m.Update(AccountRenameRequestedMsg{AccountID: 7, Name: "Renamed Google"})
	m = updated.(Model)
	if cmd == nil || !m.syncing || m.accountRenameOpen {
		t.Fatalf("Rename retry: cmd=%v syncing=%v rename=%v", cmd == nil, m.syncing, m.accountRenameOpen)
	}
	updated, _ = m.Update(AccountSettingsClosedMsg{})
	m = updated.(Model)
	m.paletteOpen = true
	renamed := account.Account{
		ID: 7, DisplayName: "Renamed Google", AuthType: "oauth2", Username: "douglas@example.com",
	}
	updated, _ = m.Update(accountRenameFinishedMsg{account: renamed})
	m = updated.(Model)
	if got := m.accounts[7].DisplayName; m.syncing || got != "Renamed Google" {
		t.Fatalf("Rename completion behind palette: syncing=%v name=%q",
			m.syncing, m.accounts[7].DisplayName)
	}
	if got := m.calendarManager.calendarForm.form.Field(cdIdxName).(*TextField).Value(); got != "Unsaved calendar title" {
		t.Fatalf("Rename completion changed calendar draft to %q", got)
	}
	if view := stripANSI(m.calendarManager.calendarForm.View()); !strings.Contains(view, "Account: Renamed Google") {
		t.Fatalf("Rename completion left stale calendar account context:\n%s", view)
	}
}

func accountManagementAppModel(t *testing.T) Model {
	t.Helper()
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.accountCalendarManager = NewAccountCalendarManagerModel(pickerDiscovery(), m.theme).
		SetSize(m.width, m.height)
	m.accountCalendarManagerOpen = true
	m.accounts = map[int64]account.Account{
		7: {ID: 7, DisplayName: "Personal Google", AuthType: "oauth2"},
	}
	m.calendars = map[int64]CalendarInfo{
		42: {Name: "Personal", IsDefault: false, AccountID: 7},
		99: {Name: "Local", IsDefault: true},
	}
	return m
}
func TestAppAccountReorderUpdatesSidebarAndSurvivesRacingReload(t *testing.T) {
	m := accountManagementAppModel(t)
	m.accountCalendarManagerOpen = false
	m.calendars[100] = CalendarInfo{Name: "Work", AccountID: 9, AccountName: "Work"}
	m.calendars[42] = CalendarInfo{Name: "Personal", AccountID: 7, AccountName: "Personal"}
	m.sidebar = m.sidebar.SetList(m.sidebar.List().SetItems(sortedCalendarListItems(m.calendars)))

	updated, cmd := m.Update(AccountReorderedMsg{IDs: []int64{9, 7}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("account reorder did not schedule persistence")
	}
	if m.calendars[100].AccountOrder != 0 || m.calendars[42].AccountOrder != 1 {
		t.Fatalf("optimistic account order = work:%d personal:%d",
			m.calendars[100].AccountOrder, m.calendars[42].AccountOrder)
	}

	stale := map[int64]CalendarInfo{
		42:  {Name: "Personal", AccountID: 7, AccountName: "Personal", AccountOrder: 0},
		99:  {Name: "Local"},
		100: {Name: "Work", AccountID: 9, AccountName: "Work", AccountOrder: 1},
	}
	updated, _ = m.Update(calendarsLoadedMsg{calendars: stale})
	m = updated.(Model)
	if m.calendars[100].AccountOrder != 0 || m.calendars[42].AccountOrder != 1 {
		t.Fatalf("racing reload reverted account order = %+v", m.calendars)
	}

	updated, _ = m.Update(accountOrderSavedMsg{ids: []int64{9, 7}})
	m = updated.(Model)
	if len(m.pendingAccountOrder) != 0 {
		t.Fatalf("confirmed account order remains pending: %+v", m.pendingAccountOrder)
	}
}
func TestAppAccountReorderSerializesLatestOrder(t *testing.T) {
	m := accountManagementAppModel(t)
	m.accountCalendarManagerOpen = false
	m.calendars[100] = CalendarInfo{Name: "Work", AccountID: 9, AccountName: "Work"}

	updated, firstSave := m.Update(AccountReorderedMsg{IDs: []int64{9, 7}})
	m = updated.(Model)
	if firstSave == nil {
		t.Fatal("first account reorder did not start a save")
	}
	updated, secondSave := m.Update(AccountReorderedMsg{IDs: []int64{7, 9}})
	m = updated.(Model)
	if secondSave != nil {
		t.Fatal("second account reorder started concurrently instead of being coalesced")
	}

	updated, latestSave := m.Update(accountOrderSavedMsg{ids: []int64{9, 7}})
	m = updated.(Model)
	if latestSave == nil || !m.accountOrderSaveInFlight {
		t.Fatalf("older completion did not start latest save: command=%v inFlight=%v",
			latestSave, m.accountOrderSaveInFlight)
	}
	updated, finalCmd := m.Update(accountOrderSavedMsg{ids: []int64{7, 9}})
	m = updated.(Model)
	if finalCmd != nil || m.accountOrderSaveInFlight || len(m.pendingAccountOrder) != 0 {
		t.Fatalf("latest completion state: command=%v inFlight=%v pending=%+v",
			finalCmd, m.accountOrderSaveInFlight, m.pendingAccountOrder)
	}
}

func TestAppAccountReorderFailureRollsBackOptimisticOrder(t *testing.T) {
	m := accountManagementAppModel(t)
	m.accountCalendarManagerOpen = false
	m.calendars[100] = CalendarInfo{Name: "Work", AccountID: 9, AccountName: "Work"}

	updated, _ := m.Update(AccountReorderedMsg{IDs: []int64{9, 7}})
	m = updated.(Model)
	updated, rollback := m.Update(accountOrderSavedMsg{
		ids: []int64{9, 7},
		err: errors.New("database busy"),
	})
	m = updated.(Model)
	if rollback == nil {
		t.Fatal("failed account reorder did not schedule a database reload")
	}
	if m.accountOrderSaveInFlight || len(m.pendingAccountOrder) != 0 {
		t.Fatalf("failed account order remains optimistic: inFlight=%v pending=%+v",
			m.accountOrderSaveInFlight, m.pendingAccountOrder)
	}
}

func TestAppAccountCalendarAddOnlySelectionStartsWithoutConfirmation(t *testing.T) {
	m := accountManagementAppModel(t)
	updated, cmd := m.Update(AccountCalendarsReconcileRequestedMsg{
		AccountID:     7,
		SelectedPaths: []string{"/primary/", "/personal/"},
	})
	m = updated.(Model)

	if cmd == nil || !m.syncing || m.accountCalendarManagerOpen || m.confirmOpen {
		t.Fatalf("add-only selection: cmd=%v syncing=%v manager=%v confirm=%v",
			cmd, m.syncing, m.accountCalendarManagerOpen, m.confirmOpen)
	}
}

func TestAppAccountCalendarRemovalRequiresDestructiveConfirmation(t *testing.T) {
	m := accountManagementAppModel(t)
	openCalendarManagerForTest(&m, CalendarDialogParams{
		ID: 42, AccountID: 7, AccountName: "Personal Google",
		Name: "Personal", Color: "#a6e3a1", RemoteLinked: true,
	})
	m.calendarManager.calendarForm.form.Field(cdIdxName).(*TextField).SetValue("Unsaved rename")
	m.calendarManagerOpen = true
	updated, cmd := m.Update(AccountCalendarsReconcileRequestedMsg{
		AccountID:     7,
		SelectedPaths: []string{"/primary/"},
	})
	m = updated.(Model)

	if cmd != nil || !m.confirmOpen || !m.accountCalendarManagerOpen || m.pendingAccountSelection == nil {
		t.Fatalf("removal selection: cmd=%v confirm=%v manager=%v pending=%v",
			cmd, m.confirmOpen, m.accountCalendarManagerOpen, m.pendingAccountSelection)
	}
	plain := stripANSI(m.confirmDialog.View())
	for _, want := range []string{
		"Remove “Personal” from Chroncal?",
		"Nothing will be deleted from the server.",
		"changes not yet uploaded",
		"unsaved calendar edits",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("removal confirmation missing %q:\n%s", want, plain)
		}
	}

	updated, cmd = m.Update(ConfirmDialogResultMsg{Confirmed: false})
	m = updated.(Model)
	if cmd != nil || m.confirmOpen || m.pendingAccountSelection != nil ||
		!m.accountCalendarManagerOpen || !m.calendarManagerOpen {
		t.Fatalf("cancel removal: cmd=%v confirm=%v pending=%v manager=%v calendar=%v",
			cmd, m.confirmOpen, m.pendingAccountSelection,
			m.accountCalendarManagerOpen, m.calendarManagerOpen)
	}

	updated, _ = m.Update(AccountCalendarsReconcileRequestedMsg{
		AccountID: 7, SelectedPaths: []string{"/primary/"},
	})
	m = updated.(Model)
	updated, cmd = m.Update(ConfirmDialogResultMsg{Confirmed: true})
	m = updated.(Model)
	if cmd == nil || !m.syncing || m.accountCalendarManagerOpen || !m.calendarManagerOpen {
		t.Fatalf("confirmed removal: cmd=%v syncing=%v manager=%v calendar=%v",
			cmd == nil, m.syncing, m.accountCalendarManagerOpen, m.calendarManagerOpen)
	}
	updated, _ = m.Update(accountSelectionFinishedMsg{
		accountManagement: true,
		removed:           1,
		removedIDs:        []int64{42},
	})
	m = updated.(Model)
	if m.calendarManagerOpen {
		t.Fatal("removing the edited calendar left its stale editor open")
	}
}

func TestAppAccountCalendarDefaultRemovalOffersKeptAndNewReplacements(t *testing.T) {
	m := accountManagementAppModel(t)
	info := m.calendars[42]
	info.IsDefault = true
	m.calendars[42] = info
	info = m.calendars[99]
	info.IsDefault = false
	m.calendars[99] = info

	updated, cmd := m.Update(AccountCalendarsReconcileRequestedMsg{
		AccountID:     7,
		SelectedPaths: []string{"/primary/"},
	})
	m = updated.(Model)
	if cmd != nil || !m.choiceOpen || m.confirmOpen || len(m.pendingAccountDefaultCandidates) != 2 {
		t.Fatalf("default removal: cmd=%v choice=%v confirm=%v candidates=%+v",
			cmd, m.choiceOpen, m.confirmOpen, m.pendingAccountDefaultCandidates)
	}
	plain := stripANSI(m.choiceDialog.View())
	for _, want := range []string{"Choose a new default", "Local", "Primary"} {
		if !strings.Contains(plain, want) {
			t.Errorf("default replacement dialog missing %q:\n%s", want, plain)
		}
	}

	newPathChoice := -1
	for idx, candidate := range m.pendingAccountDefaultCandidates {
		if candidate.path == "/primary/" {
			newPathChoice = idx
			break
		}
	}
	if newPathChoice < 0 {
		t.Fatalf("newly selected calendar is not a default candidate: %+v", m.pendingAccountDefaultCandidates)
	}
	updated, _ = m.Update(ChoiceDialogResultMsg{Choice: newPathChoice})
	m = updated.(Model)
	if m.choiceOpen || !m.confirmOpen || m.pendingAccountSelection == nil ||
		m.pendingAccountSelection.params.NewDefaultPath != "/primary/" {
		t.Fatalf("replacement choice: choice=%v confirm=%v pending=%+v",
			m.choiceOpen, m.confirmOpen, m.pendingAccountSelection)
	}
}

func TestAppAccountCalendarDefaultCandidatesSanitizeRemoteNames(t *testing.T) {
	m := accountManagementAppModel(t)
	personal := m.calendars[42]
	personal.IsDefault = true
	m.calendars[42] = personal
	local := m.calendars[99]
	local.IsDefault = false
	m.calendars[99] = local
	m.accountCalendarManager.discovery.Calendars[1].Name = "\x1b]8;;https://evil.example\aOwned\x1b]8;;\a"

	updated, cmd := m.Update(AccountCalendarsReconcileRequestedMsg{
		AccountID:     7,
		SelectedPaths: []string{"/primary/", "/holidays/"},
	})
	m = updated.(Model)
	if cmd != nil || !m.choiceOpen {
		t.Fatalf("unsafe default candidate: cmd=%v choice=%v", cmd, m.choiceOpen)
	}
	if view := m.choiceDialog.View(); strings.Contains(view, "\x1b]8;;") || strings.Contains(view, "\a") {
		t.Fatalf("default candidate emitted terminal controls: %q", view)
	}
}

func TestAppRemovingEveryAccountCalendarAlsoRemovesAccount(t *testing.T) {
	m := accountManagementAppModel(t)
	updated, cmd := m.Update(AccountCalendarsReconcileRequestedMsg{AccountID: 7})
	m = updated.(Model)
	if cmd != nil || !m.confirmOpen || m.pendingAccountSelection == nil {
		t.Fatalf("empty selection: cmd=%v confirm=%v pending=%v",
			cmd, m.confirmOpen, m.pendingAccountSelection)
	}
	plain := strings.Join(strings.Fields(stripANSI(m.confirmDialog.View())), " ")
	for _, want := range []string{"stored", "sign-in", "Remove Account"} {
		if !strings.Contains(plain, want) {
			t.Errorf("empty-account confirmation missing %q:\n%s", want, plain)
		}
	}

	updated, cmd = m.Update(ConfirmDialogResultMsg{Confirmed: true})
	m = updated.(Model)
	if cmd == nil || !m.syncing || m.accountCalendarManagerOpen || m.pendingAccountSelection != nil {
		t.Fatalf("confirmed empty selection: cmd=%v syncing=%v manager=%v pending=%v",
			cmd, m.syncing, m.accountCalendarManagerOpen, m.pendingAccountSelection)
	}
}
