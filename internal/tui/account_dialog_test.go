package tui

import (
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/caldav"
)

func rebuildAccountDialog(m *AccountDialogModel) {
	if m.form.onRebuild != nil {
		m.form.onRebuild(&m.form)
	}
}

func TestAccountDialogOAuthSubmitCarriesConnectionSettings(t *testing.T) {
	m := NewAccountDialogModel(Theme{}).SetSize(120, 40)
	m.form.Field(accountIdxName).(*TextField).SetValue("Google")
	m.form.Field(accountIdxServer).(*TextField).SetValue("https://apidata.googleusercontent.com/caldav/v2/")
	m.form.Field(accountIdxUsername).(*TextField).SetValue("me@example.com")
	m.form.Field(accountIdxAuth).(*SelectField).SetSelected(authOptionIndex("oauth2"))
	rebuildAccountDialog(&m)
	m.form.Field(accountIdxOAuthClientID).(*TextField).SetValue("client.apps")
	m.form.Field(accountIdxOAuthClientSecret).(*TextField).SetValue("secret")

	cmd := m.form.onSubmit(&m.form)
	if cmd == nil {
		t.Fatal("valid OAuth account form was rejected")
	}
	msg, ok := cmd().(AccountConnectRequestedMsg)
	if !ok {
		t.Fatalf("message = %T, want AccountConnectRequestedMsg", cmd())
	}
	if msg.Name != "Google" || msg.AuthType != "oauth2" || msg.OAuthClientID != "client.apps" || msg.OAuthClientSecret != "secret" {
		t.Fatalf("connect message = %+v", msg)
	}
}

func TestAccountDialogSwitchingAuthPreservesSecrets(t *testing.T) {
	m := NewAccountDialogModel(Theme{})
	m.form.Field(accountIdxSecret).(*TextField).SetValue("password")
	m.form.Field(accountIdxAuth).(*SelectField).SetSelected(authOptionIndex("oauth2"))
	rebuildAccountDialog(&m)
	m.form.Field(accountIdxOAuthClientID).(*TextField).SetValue("client.apps")
	m.form.Field(accountIdxOAuthClientSecret).(*TextField).SetValue("secret")
	m.form.Field(accountIdxAuth).(*SelectField).SetSelected(authOptionIndex("basic"))
	rebuildAccountDialog(&m)
	if got := m.form.Field(accountIdxSecret).(*TextField).Value(); got != "password" {
		t.Fatalf("password lost after auth switch: %q", got)
	}
	m.form.Field(accountIdxAuth).(*SelectField).SetSelected(authOptionIndex("oauth2"))
	rebuildAccountDialog(&m)
	if got := m.form.Field(accountIdxOAuthClientID).(*TextField).Value(); got != "client.apps" {
		t.Fatalf("client ID lost after auth switch: %q", got)
	}
}

func pickerDiscovery() account.Discovery {
	return account.Discovery{
		Account: account.Account{ID: 7, DisplayName: "Google"},
		Calendars: []account.DiscoveredCalendar{
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/primary/", Name: "Primary", Access: caldav.CalendarAccessWrite, SupportedComponentSet: []string{"VEVENT"}}, Importable: true},
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/holidays/", Name: "Holidays in Brazil", Access: caldav.CalendarAccessRead, SupportedComponentSet: []string{"VEVENT"}}, Importable: true},
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/tasks/", Name: "Tasks", SupportedComponentSet: []string{"VTODO"}}, Importable: false},
		},
	}
}

func TestAccountCalendarPickerSelectsImportableCollections(t *testing.T) {
	m := NewAccountCalendarPickerModel(pickerDiscovery(), Theme{}).SetSize(160, 60)
	if !m.selected["/primary/"] || !m.selected["/holidays/"] || m.selected["/tasks/"] {
		t.Fatalf("initial selections = %v", m.selected)
	}
	if out := m.View(); !strings.Contains(out, "Holidays in Brazil") || !strings.Contains(out, "read-only") || !strings.Contains(out, "unsupported") {
		t.Fatalf("picker view missing collection metadata: %q", out)
	}

	m.shell = m.shell.SetSelected(1)
	m = m.toggleCurrent()
	if m.selected["/holidays/"] {
		t.Fatal("space toggle should deselect the current importable calendar")
	}
	cmd := m.importSelected()
	msg := cmd().(AccountCalendarsImportRequestedMsg)
	if len(msg.Paths) != 1 || msg.Paths[0] != "/primary/" || msg.AccountID != 7 {
		t.Fatalf("import message = %+v", msg)
	}
}

func TestAccountCalendarPickerCannotSelectUnsupportedCollection(t *testing.T) {
	m := NewAccountCalendarPickerModel(pickerDiscovery(), Theme{})
	m.shell = m.shell.SetSelected(2)
	m = m.toggleCurrent()
	if m.selected["/tasks/"] {
		t.Fatal("unsupported VTODO collection must remain unselected")
	}
}
