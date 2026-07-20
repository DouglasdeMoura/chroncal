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
		Username:       "douglas@example.com",
		CalendarCount:  3,
		AttentionCount: 1,
		AuthType:       "oauth2",
	}, NewTheme(true))

	wantLabels := []string{"Manage Calendars…", "Rename Account…", "Sign In Again…", "Remove Account…", "Done"}
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
		case AccountSettingsRenameRequestedMsg:
			if msg.AccountID != wantID || msg.DisplayName != "Personal Google" {
				t.Fatalf("Rename = %+v", msg)
			}
		case AccountSettingsReauthRequestedMsg:
			if msg.AccountID != wantID {
				t.Fatalf("Reauth AccountID = %d, want %d", msg.AccountID, wantID)
			}
		case AccountSettingsRemoveRequestedMsg:
			if msg.AccountID != wantID || msg.CalendarCount != 3 {
				t.Fatalf("Remove = %+v", msg)
			}
		case AccountSettingsClosedMsg:
			if i != 4 {
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
		t.Fatalf("non-OAuth actions contain sign-in: %q", got)
	}
	if got != "Manage Calendars…|Rename Account…|Remove Account…|Done" {
		t.Fatalf("non-OAuth actions = %q", got)
	}
}

func TestAccountSettingsDialogRendersQuietIdentityAndHealth(t *testing.T) {
	m := NewAccountSettingsDialogModel(AccountSettingsParams{
		AccountID:      7,
		DisplayName:    "Personal Google",
		Provider:       "Google Account",
		Username:       "douglas@example.com",
		CalendarCount:  3,
		AttentionCount: 1,
		AuthType:       "oauth2",
	}, NewTheme(true)).SetSize(80, 30)

	view := stripANSI(m.View())
	for _, want := range []string{"Personal Google", "Google Account", "douglas@example.com", "3 calendars", "Needs attention · 1 calendar", "Manage Calendars…", "Remove Account…", "Done"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "https://") {
		t.Fatalf("view exposes technical server URL:\n%s", view)
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
