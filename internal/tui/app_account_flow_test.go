package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
)

func TestAppCalendarDiscoveryOpensPickerInsideCalendarDialog(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{}, m.theme).SetSize(m.width, m.height)
	m.calendarDialogOpen = true
	m.syncing = true

	updated, _ := m.Update(accountDiscoveryReadyMsg{discovery: pickerDiscovery()})
	m = updated.(Model)
	if m.syncing || !m.calendarDialogOpen || m.calendarDialog.discoveryPicker == nil {
		t.Fatalf("discovery transition: syncing=%v calendarDialog=%v picker=%v",
			m.syncing, m.calendarDialogOpen, m.calendarDialog.discoveryPicker != nil)
	}
}

func TestAppOAuthCalendarDiscoveryRecordsIntegratedPurpose(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{}, m.theme).SetSize(m.width, m.height)
	m.calendarDialogOpen = true
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
	m.calendarDialogOpen = true
	m.pendingDiscoveryAccountID = 7
	m.pendingDiscoveryCreated = true

	updated, cmd := m.Update(AccountCalendarPickerClosedMsg{})
	m = updated.(Model)
	if cmd == nil || m.calendarDialogOpen || !m.syncing {
		t.Fatalf("cancel state: cmd=%v calendarDialog=%v syncing=%v", cmd, m.calendarDialogOpen, m.syncing)
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
	if m.oauthFlowOpen || !m.calendarDialogOpen {
		t.Fatalf("OAuth cancel state: flow=%v calendarDialog=%v", m.oauthFlowOpen, m.calendarDialogOpen)
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

func TestAppAdditionalCalendarDiscoveryReturnsToUnsavedEditFormOnCancel(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{
		ID:           11,
		AccountID:    7,
		Name:         "Personal",
		Color:        "#a6e3a1",
		RemoteLinked: true,
	}, m.theme).SetSize(m.width, m.height)
	m.calendarDialogOpen = true
	m.calendarDialog.form.Field(cdIdxName).(*TextField).SetValue("Unsaved rename")

	updated, cmd := m.Update(CalendarDiscoverAdditionalRequestedMsg{CalendarID: 11, AccountID: 7})
	m = updated.(Model)
	if cmd == nil || !m.syncing || !m.discoveryReturnToEdit {
		t.Fatalf("additional discovery start: cmd=%v syncing=%v returnToEdit=%v",
			cmd, m.syncing, m.discoveryReturnToEdit)
	}

	updated, _ = m.Update(accountDiscoveryReadyMsg{
		discovery:        pickerDiscovery(),
		management:       true,
		originCalendarID: 11,
		originAccountID:  7,
	})
	m = updated.(Model)
	if m.calendarDialog.discoveryPicker == nil {
		t.Fatal("additional discovery did not open the picker")
	}
	if !m.calendarDialog.discoveryPicker.manage {
		t.Fatal("additional discovery opened the additive picker instead of account management")
	}

	updated, _ = m.Update(AccountCalendarPickerClosedMsg{})
	m = updated.(Model)
	if !m.calendarDialogOpen || m.calendarDialog.discoveryPicker != nil {
		t.Fatalf("picker cancel: calendarDialog=%v picker=%v",
			m.calendarDialogOpen, m.calendarDialog.discoveryPicker != nil)
	}
	if got := m.calendarDialog.form.Field(cdIdxName).(*TextField).Value(); got != "Unsaved rename" {
		t.Fatalf("unsaved name after picker cancel = %q", got)
	}
}

func TestAppAdditionalCalendarDiscoveryIgnoresResultAfterEditCloses(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{
		ID:           11,
		AccountID:    7,
		Name:         "Personal",
		Color:        "#a6e3a1",
		RemoteLinked: true,
	}, m.theme).SetSize(m.width, m.height)
	m.calendarDialogOpen = true
	m.calendarDialogGeneration = 4

	updated, cmd := m.Update(CalendarDiscoverAdditionalRequestedMsg{CalendarID: 11, AccountID: 7})
	m = updated.(Model)
	if cmd == nil || !m.syncing {
		t.Fatalf("additional discovery start: cmd=%v syncing=%v", cmd, m.syncing)
	}
	updated, _ = m.Update(CalendarDialogClosedMsg{})
	m = updated.(Model)
	updated, _ = m.Update(accountDiscoveryReadyMsg{
		discovery:        pickerDiscovery(),
		management:       true,
		dialogGeneration: 4,
	})
	m = updated.(Model)
	if m.calendarDialogOpen || m.calendarDialog.discoveryPicker != nil || m.syncing {
		t.Fatalf("stale discovery reopened dialog: open=%v picker=%v syncing=%v",
			m.calendarDialogOpen, m.calendarDialog.discoveryPicker != nil, m.syncing)
	}
}

func TestAppAdditionalCalendarImportReturnsToUnsavedEditForm(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{
		ID:           11,
		AccountID:    7,
		Name:         "Personal",
		Color:        "#a6e3a1",
		RemoteLinked: true,
	}, m.theme).SetSize(m.width, m.height)
	m.calendarDialog.form.Field(cdIdxName).(*TextField).SetValue("Unsaved rename")
	m.calendarDialog = m.calendarDialog.ShowDiscovery(pickerDiscovery()).SetSize(m.width, m.height)
	m.calendarDialogOpen = true
	m.discoveryReturnToEdit = true

	updated, cmd := m.Update(AccountCalendarsImportRequestedMsg{
		AccountID: 7,
		Paths:     []string{"/holidays/"},
	})
	m = updated.(Model)
	if cmd == nil || m.calendarDialogOpen || !m.syncing {
		t.Fatalf("calendar import start: cmd=%v dialog=%v syncing=%v",
			cmd, m.calendarDialogOpen, m.syncing)
	}

	updated, _ = m.Update(accountImportFinishedMsg{created: 1, synced: 1})
	m = updated.(Model)
	if !m.calendarDialogOpen || m.calendarDialog.discoveryPicker != nil {
		t.Fatalf("calendar import finish: dialog=%v picker=%v",
			m.calendarDialogOpen, m.calendarDialog.discoveryPicker != nil)
	}
	if got := m.calendarDialog.form.Field(cdIdxName).(*TextField).Value(); got != "Unsaved rename" {
		t.Fatalf("unsaved name after calendar import = %q", got)
	}
}

func TestAppAccountActionsMenuReturnsToUnsavedCalendarEdit(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{
		ID:           11,
		AccountID:    7,
		Name:         "Personal",
		Color:        "#a6e3a1",
		RemoteLinked: true,
	}, m.theme).SetSize(m.width, m.height)
	m.calendarDialogOpen = true
	m.calendarDialog.form.Field(cdIdxName).(*TextField).SetValue("Unsaved rename")

	updated, _ := m.Update(CalendarAccountActionsRequestedMsg{})
	m = updated.(Model)
	if !m.calendarAccountMenuOpen {
		t.Fatal("Account action did not open its menu")
	}

	updated, _ = m.Update(CalendarAccountMenuClosedMsg{})
	m = updated.(Model)
	if m.calendarAccountMenuOpen || !m.calendarDialogOpen {
		t.Fatalf("menu close: accountMenu=%v calendarDialog=%v",
			m.calendarAccountMenuOpen, m.calendarDialogOpen)
	}
	if got := m.calendarDialog.form.Field(cdIdxName).(*TextField).Value(); got != "Unsaved rename" {
		t.Fatalf("unsaved name after Account menu close = %q", got)
	}
}

func TestAppAccountMenuSelectionClosesMenuBeforeDispatch(t *testing.T) {
	m := NewModel(nil, "")
	m.calendarAccountMenuOpen = true
	want := CalendarDiscoverAdditionalRequestedMsg{CalendarID: 11, AccountID: 7}

	updated, cmd := m.Update(CalendarAccountMenuSelectedMsg{Message: want})
	m = updated.(Model)
	if m.calendarAccountMenuOpen {
		t.Fatal("Account menu stayed open after selection")
	}
	if cmd == nil {
		t.Fatal("Account menu selection did not dispatch its action")
	}
	if got := cmd(); got != want {
		t.Fatalf("dispatched action = %#v, want %#v", got, want)
	}
}

func accountManagementAppModel(t *testing.T) Model {
	t.Helper()
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{
		ID:             42,
		AccountID:      7,
		Name:           "Personal",
		Color:          "#a6e3a1",
		RemoteLinked:   true,
		RemoteAuthType: "oauth2",
	}, m.theme).SetSize(m.width, m.height)
	m.calendarDialog.form.Field(cdIdxName).(*TextField).SetValue("Unsaved rename")
	m.calendarDialog = m.calendarDialog.ShowCalendarManagement(pickerDiscovery()).SetSize(m.width, m.height)
	m.calendarDialogOpen = true
	m.discoveryReturnToEdit = true
	m.calendars = map[int64]CalendarInfo{
		42: {Name: "Personal", IsDefault: false, AccountID: 7},
		99: {Name: "Local", IsDefault: true},
	}
	return m
}

func TestAppAccountCalendarAddOnlySelectionStartsWithoutConfirmation(t *testing.T) {
	m := accountManagementAppModel(t)
	updated, cmd := m.Update(AccountCalendarsReconcileRequestedMsg{
		AccountID:     7,
		SelectedPaths: []string{"/primary/", "/personal/"},
	})
	m = updated.(Model)

	if cmd == nil || !m.syncing || m.calendarDialogOpen || m.confirmOpen {
		t.Fatalf("add-only selection: cmd=%v syncing=%v dialog=%v confirm=%v",
			cmd, m.syncing, m.calendarDialogOpen, m.confirmOpen)
	}
}

func TestAppAccountCalendarRemovalRequiresDestructiveConfirmation(t *testing.T) {
	m := accountManagementAppModel(t)
	updated, cmd := m.Update(AccountCalendarsReconcileRequestedMsg{
		AccountID:     7,
		SelectedPaths: []string{"/primary/"},
	})
	m = updated.(Model)

	if cmd != nil || !m.confirmOpen || !m.calendarDialogOpen || m.pendingAccountSelection == nil {
		t.Fatalf("removal selection: cmd=%v confirm=%v dialog=%v pending=%v",
			cmd, m.confirmOpen, m.calendarDialogOpen, m.pendingAccountSelection)
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
		!m.calendarDialogOpen || m.calendarDialog.discoveryPicker == nil {
		t.Fatalf("cancel removal: cmd=%v confirm=%v pending=%v dialog=%v picker=%v",
			cmd, m.confirmOpen, m.pendingAccountSelection, m.calendarDialogOpen,
			m.calendarDialog.discoveryPicker != nil)
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
	m.calendarDialog.discoveryPicker.discovery.Calendars[1].Name = "\x1b]8;;https://evil.example\aOwned\x1b]8;;\a"

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

func TestAppAccountCalendarRemovalCompletionHandlesUnderlyingEdit(t *testing.T) {
	t.Run("kept current calendar returns to unsaved edit", func(t *testing.T) {
		m := accountManagementAppModel(t)
		updated, _ := m.Update(accountSelectionFinishedMsg{created: 1, synced: 1})
		m = updated.(Model)
		if !m.calendarDialogOpen || m.calendarDialog.discoveryPicker != nil {
			t.Fatalf("selection finish: dialog=%v picker=%v",
				m.calendarDialogOpen, m.calendarDialog.discoveryPicker != nil)
		}
		if got := m.calendarDialog.form.Field(cdIdxName).(*TextField).Value(); got != "Unsaved rename" {
			t.Fatalf("unsaved name after selection = %q", got)
		}
	})

	t.Run("removed current calendar closes edit", func(t *testing.T) {
		m := accountManagementAppModel(t)
		updated, _ := m.Update(accountSelectionFinishedMsg{
			removed:        1,
			removedCurrent: true,
			accountRemoved: true,
		})
		m = updated.(Model)
		if m.calendarDialogOpen || m.calendarDialog.discoveryPicker != nil || m.discoveryReturnToEdit {
			t.Fatalf("removed-current finish: dialog=%v picker=%v return=%v",
				m.calendarDialogOpen, m.calendarDialog.discoveryPicker != nil, m.discoveryReturnToEdit)
		}
		if !strings.Contains(m.syncStatus, "Removed account") {
			t.Fatalf("removed-account status = %q", m.syncStatus)
		}
	})
}

func TestAppAccountCalendarCompletionRespectsReplacementEdit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		accountID int64
		msg       accountSelectionFinishedMsg
		wantOpen  bool
	}{
		{
			name:     "stale reconcile error preserves unrelated edit",
			msg:      accountSelectionFinishedMsg{err: account.ErrSelectionStale},
			wantOpen: true,
		},
		{
			name: "removed account preserves unrelated edit",
			msg: accountSelectionFinishedMsg{
				removed:        1,
				removedCurrent: true,
				accountRemoved: true,
			},
			wantOpen: true,
		},
		{
			name:      "removed replacement row closes its edit",
			accountID: 7,
			msg: accountSelectionFinishedMsg{
				removed:    1,
				removedIDs: []int64{99},
			},
			wantOpen: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := accountManagementAppModel(t)
			m.calendarDialogGeneration = 4
			tc.msg.dialogGeneration = 4
			m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{
				ID:        99,
				AccountID: tc.accountID,
				Name:      "Replacement",
				Color:     "#a6e3a1",
			}, m.theme).SetSize(m.width, m.height)
			m.calendarDialog.form.Field(cdIdxName).(*TextField).SetValue("New unsaved edit")
			m.calendarDialogGeneration = 5
			m.calendarDialogOpen = true

			updated, _ := m.Update(tc.msg)
			m = updated.(Model)
			if m.calendarDialogOpen != tc.wantOpen {
				t.Fatalf("replacement edit open = %v, want %v", m.calendarDialogOpen, tc.wantOpen)
			}
			if tc.wantOpen {
				if got := m.calendarDialog.form.Field(cdIdxName).(*TextField).Value(); got != "New unsaved edit" {
					t.Fatalf("replacement edit name = %q", got)
				}
			}
		})
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
	if cmd == nil || !m.syncing || m.calendarDialogOpen || m.pendingAccountSelection != nil {
		t.Fatalf("confirmed empty selection: cmd=%v syncing=%v dialog=%v pending=%v",
			cmd, m.syncing, m.calendarDialogOpen, m.pendingAccountSelection)
	}
}
