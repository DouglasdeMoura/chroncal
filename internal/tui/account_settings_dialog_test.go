package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestAccountSettingsDialogOAuthActionsAreAccountScoped(t *testing.T) {
	m := NewAccountSettingsDialogModel(AccountSettingsParams{
		AccountID:      7,
		DisplayName:    "Personal Google",
		Provider:       "Google Account",
		ServerURL:      "https://apidata.googleusercontent.com/caldav/v2/",
		Username:       "douglas@example.com",
		CalendarCount:  3,
		AttentionCount: 1,
		AuthType:       "oauth2",
	}, NewTheme(true))

	wantLabels := []string{"Manage Calendars…", "Sync Now", "Rename Account…", "Sign In Again…", "Remove Account…", "Done"}
	if len(m.actions) != len(wantLabels) {
		t.Fatalf("action count = %d, want %d", len(m.actions), len(wantLabels))
	}
	for i, want := range wantLabels {
		if m.actions[i].label != want {
			t.Fatalf("actions[%d].label = %q, want %q", i, m.actions[i].label, want)
		}
		if gotDanger := m.actions[i].variant == ButtonDanger; gotDanger != (want == "Remove Account…") {
			t.Fatalf("actions[%d] danger = %v for %q", i, gotDanger, want)
		}
	}
	if m.selected != 0 {
		t.Fatalf("initial selection = %d, want Manage Calendars", m.selected)
	}

	assertAccountID := func(i int, wantID int64) {
		t.Helper()
		switch msg := m.actions[i].onPress().(type) {
		case AccountSettingsManageRequestedMsg:
			if msg.AccountID != wantID {
				t.Fatalf("Manage AccountID = %d, want %d", msg.AccountID, wantID)
			}
		case AccountSettingsSyncRequestedMsg:
			if msg.AccountID != wantID {
				t.Fatalf("Sync = %+v", msg)
			}
		case AccountSettingsRenameRequestedMsg:
			if msg.AccountID != wantID {
				t.Fatalf("Rename = %+v", msg)
			}
		case AccountSettingsReauthRequestedMsg:
			if msg.AccountID != wantID {
				t.Fatalf("Reauth AccountID = %d, want %d", msg.AccountID, wantID)
			}
		case AccountSettingsRemoveRequestedMsg:
			if msg.AccountID != wantID {
				t.Fatalf("Remove = %+v", msg)
			}
		case AccountSettingsClosedMsg:
			if i != 5 {
				t.Fatalf("unexpected Done at action %d", i)
			}
		default:
			t.Fatalf("actions[%d] message type = %T", i, msg)
		}
	}
	for i := range m.actions {
		assertAccountID(i, 7)
	}
}

func TestAccountSettingsDialogNonOAuthOmitsSignIn(t *testing.T) {
	m := NewAccountSettingsDialogModel(AccountSettingsParams{
		AccountID:     9,
		DisplayName:   "Work",
		Provider:      "CalDAV Account",
		ServerURL:     "https://cal.example.com/dav/",
		Username:      "alice@example.com",
		CalendarCount: 2,
		AuthType:      "basic",
	}, NewTheme(true))

	labels := make([]string, len(m.actions))
	for i := range m.actions {
		labels[i] = m.actions[i].label
	}
	got := strings.Join(labels, "|")
	if strings.Contains(got, "Sign In Again") {
		t.Fatalf("basic-auth actions contain sign-in: %q", got)
	}
	want := "Manage Calendars…|Sync Now|Rename Account…|Update Credentials…|Remove Account…|Done"
	if got != want {
		t.Fatalf("basic-auth actions = %q, want %q", got, want)
	}
	// The new action must be account-scoped so it cannot mutate a sibling.
	for _, a := range m.actions {
		if a.label == "Update Credentials…" {
			if msg, ok := a.onPress().(AccountSettingsUpdateCredentialsRequestedMsg); !ok || msg.AccountID != 9 {
				t.Fatalf("Update Credentials action = %#v, want AccountSettingsUpdateCredentialsRequestedMsg{9}", a.onPress())
			}
			return
		}
	}
	t.Fatalf("basic-auth actions missing Update Credentials: %q", got)
}

func TestAccountSettingsDialogRendersQuietIdentityAndHealth(t *testing.T) {
	m := NewAccountSettingsDialogModel(AccountSettingsParams{
		AccountID:      7,
		DisplayName:    "Personal Google",
		Provider:       "Google Account",
		ServerURL:      "https://apidata.googleusercontent.com/caldav/v2/",
		Username:       "douglas@example.com",
		CalendarCount:  3,
		AttentionCount: 1,
		AuthType:       "oauth2",
	}, NewTheme(true)).SetSize(80, 30)

	view := stripANSI(m.View())
	for _, want := range []string{"Personal Google", "Provider: Google Account", "Server: https://apidata.googleusercontent.com/caldav/v2/", "Identity: douglas@example.com", "Calendars: 3", "Needs attention · 1 calendar", "Manage Calendars…", "Sync Now", "Remove Account…", "Done"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestAccountSettingsDialogEscapeCloses(t *testing.T) {
	m := NewAccountSettingsDialogModel(AccountSettingsParams{AccountID: 7}, NewTheme(true))
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Escape returned nil command")
	}
	if _, ok := cmd().(AccountSettingsClosedMsg); !ok {
		t.Fatalf("Escape message = %T, want AccountSettingsClosedMsg", cmd())
	}
}

func TestAccountOAuthConfigDialogSubmitsAccountScopedConfig(t *testing.T) {
	m := NewAccountOAuthConfigDialogModel(7, "Personal Google", "stored-client", NewTheme(true)).
		SetSize(100, 35)
	if got := stripANSI(m.View()); !strings.Contains(got, "Personal Google") ||
		!strings.Contains(got, "Client ID") || !strings.Contains(got, "Client secret") {
		t.Fatalf("OAuth config view missing account context or fields:\n%s", got)
	}
	m.form.Field(0).(*TextField).SetValue("new-client")
	m.form.Field(1).(*TextField).SetValue("new-secret")
	form, cmd := m.form.Submit()
	m.form = form
	if cmd == nil {
		t.Fatal("OAuth config form did not submit")
	}
	msg, ok := cmd().(AccountOAuthConfigSubmittedMsg)
	if !ok || msg.AccountID != 7 || msg.ClientID != "new-client" || msg.ClientSecret != "new-secret" {
		t.Fatalf("OAuth config submission = %#v", cmd())
	}
}

func TestAccountSettingsDialogBearerOffersUpdateCredentials(t *testing.T) {
	m := NewAccountSettingsDialogModel(AccountSettingsParams{
		AccountID:   11,
		DisplayName: "API Token Account",
		AuthType:    "bearer",
	}, NewTheme(true))

	labels := make([]string, len(m.actions))
	for i := range m.actions {
		labels[i] = m.actions[i].label
	}
	got := strings.Join(labels, "|")
	want := "Manage Calendars…|Sync Now|Rename Account…|Update Credentials…|Remove Account…|Done"
	if got != want {
		t.Fatalf("bearer actions = %q, want %q", got, want)
	}
	for _, a := range m.actions {
		if a.label == "Update Credentials…" {
			if msg, ok := a.onPress().(AccountSettingsUpdateCredentialsRequestedMsg); !ok || msg.AccountID != 11 {
				t.Fatalf("Update Credentials action = %#v, want AccountSettingsUpdateCredentialsRequestedMsg{11}", a.onPress())
			}
			return
		}
	}
	t.Fatalf("bearer actions missing Update Credentials: %q", got)
}

func TestAccountSettingsDialogOAuthOmitsUpdateCredentials(t *testing.T) {
	m := NewAccountSettingsDialogModel(AccountSettingsParams{
		AccountID: 7, DisplayName: "Personal Google", AuthType: "oauth2",
	}, NewTheme(true))
	for _, a := range m.actions {
		if a.label == "Update Credentials…" {
			t.Fatalf("OAuth account must keep Sign In Again, not Update Credentials")
		}
		if a.label == "Sign In Again…" {
			return
		}
	}
	t.Fatalf("OAuth actions missing Sign In Again")
}

func TestAccountCredentialsDialogBasicCollectsPassword(t *testing.T) {
	m := NewAccountCredentialsDialogModel(7, "Work", "basic", "alice@example.com", NewTheme(true)).
		SetSize(80, 30)
	view := stripANSI(m.View())
	if !strings.Contains(view, "Password") || !strings.Contains(view, "New password for Work") {
		t.Fatalf("basic dialog missing Password field/context:\n%s", view)
	}
	m.form.Field(0).(*TextField).SetValue("hunter2")
	form, cmd := m.form.Submit()
	m.form = form
	if cmd == nil {
		t.Fatal("basic credential form did not submit")
	}
	msg, ok := cmd().(AccountCredentialsUpdateSubmittedMsg)
	if !ok || msg.AccountID != 7 || msg.Secret != "hunter2" {
		t.Fatalf("basic submission = %#v, want account 7 secret hunter2", cmd())
	}
}

func TestAccountCredentialsDialogBearerCollectsToken(t *testing.T) {
	m := NewAccountCredentialsDialogModel(11, "API Token Account", "bearer", "svc", NewTheme(true)).
		SetSize(80, 30)
	view := stripANSI(m.View())
	if !strings.Contains(view, "Token") || !strings.Contains(view, "New token for API Token Account") {
		t.Fatalf("bearer dialog missing Token field/context:\n%s", view)
	}
	m.form.Field(0).(*TextField).SetValue("abc123")
	form, cmd := m.form.Submit()
	m.form = form
	if cmd == nil {
		t.Fatal("bearer credential form did not submit")
	}
	msg, ok := cmd().(AccountCredentialsUpdateSubmittedMsg)
	if !ok || msg.AccountID != 11 || msg.Secret != "abc123" {
		t.Fatalf("bearer submission = %#v, want account 11 secret abc123", cmd())
	}
}

func TestAccountCredentialsDialogEscapeCancels(t *testing.T) {
	m := NewAccountCredentialsDialogModel(7, "Work", "basic", "alice@example.com", NewTheme(true))
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Escape returned nil command")
	}
	if _, ok := cmd().(AccountCredentialsUpdateClosedMsg); !ok {
		t.Fatalf("Escape message = %T, want AccountCredentialsUpdateClosedMsg", cmd())
	}
}
