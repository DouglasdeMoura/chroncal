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
