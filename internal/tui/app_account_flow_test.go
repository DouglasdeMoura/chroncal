package tui

import (
	"context"
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

	updated, _ = m.Update(accountDiscoveryReadyMsg{discovery: pickerDiscovery()})
	m = updated.(Model)
	if m.calendarDialog.discoveryPicker == nil {
		t.Fatal("additional discovery did not open the picker")
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
