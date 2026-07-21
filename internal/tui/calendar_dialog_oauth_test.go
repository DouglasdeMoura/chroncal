package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// oauthDialogFixture builds an unlinked dialog with Sync on and auth set to
// the given type, driving OnRebuild the same way form_test.go does.
func oauthDialogFixture(t *testing.T, authType string) CalendarDialogModel {
	t.Helper()
	m := NewCalendarDialogModel(CalendarDialogParams{Color: "#a6e3a1"}, Theme{})
	m = m.SetSize(120, 40)
	rebuild := func() {
		if m.form.onRebuild != nil {
			m.form.onRebuild(&m.form)
		}
	}
	m.form.Field(cdIdxSync).(*CheckboxField).Toggle()
	rebuild()
	m.form.Field(calDAVIdxAuth).(*SelectField).SetSelected(authOptionIndex(authType))
	rebuild()
	return m
}

func TestCalendarDialog_OAuthLayoutSwapsRows(t *testing.T) {
	m := oauthDialogFixture(t, "oauth2")

	if got := m.form.ItemCount(); got != calDAVIdxOAuthAllowInsecure+1 {
		t.Fatalf("ItemCount = %d, want %d (oauth layout)", got, calDAVIdxOAuthAllowInsecure+1)
	}
	if _, ok := m.form.Field(calDAVIdxOAuthClientID).(*TextField); !ok {
		t.Errorf("row %d should be the Client ID TextField", calDAVIdxOAuthClientID)
	}
	if _, ok := m.form.Field(calDAVIdxOAuthClientSecret).(*TextField); !ok {
		t.Errorf("row %d should be the Client secret TextField", calDAVIdxOAuthClientSecret)
	}
	if _, ok := m.form.Field(calDAVIdxOAuthAllowInsecure).(*CheckboxField); !ok {
		t.Errorf("row %d should be the HTTP checkbox", calDAVIdxOAuthAllowInsecure)
	}
	view := m.form.View()
	if !strings.Contains(view, "Client ID") || !strings.Contains(view, "Client secret") {
		t.Errorf("oauth layout should render client config labels; got %q", view)
	}
}

func TestCalendarDialog_OAuthLayoutFitsSmallTerminal(t *testing.T) {
	m := oauthDialogFixture(t, "oauth2").SetSize(120, 10)

	_, bh := lipgloss.Size(m.View())
	if bh > 10 {
		t.Fatalf("rendered calendar dialog height = %d, want <= 10", bh)
	}
	if !m.bodyOverflows() {
		t.Fatal("test precondition: calendar form body should overflow")
	}
	out := m.View()
	if !strings.Contains(out, "New calendar") || !strings.Contains(out, "Discover calendars") || !strings.Contains(out, "Cancel") {
		t.Fatalf("title and actions should stay visible in small terminal, got %q", out)
	}
	if !strings.Contains(out, "more") {
		t.Fatalf("scroll hint should render when body overflows, got %q", out)
	}
}

func TestCalendarDialog_MouseWheelScrollSurvivesRender(t *testing.T) {
	m := oauthDialogFixture(t, "oauth2").SetSize(120, 10)
	if !m.bodyOverflows() {
		t.Fatal("test precondition: calendar form body should overflow")
	}

	for range 30 {
		var cmd tea.Cmd
		m, cmd = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		if cmd != nil {
			t.Fatalf("mouse wheel returned unexpected command %T", cmd)
		}
	}
	if !m.body.AtBottom() {
		t.Fatal("mouse wheel should scroll the body to the bottom")
	}

	out := m.View()
	if !strings.Contains(out, "allow plain HTTP") {
		t.Fatalf("rendering must preserve the wheel-scrolled viewport, got %q", out)
	}
	if !strings.Contains(out, "↑ more") {
		t.Fatalf("bottom scroll hint should render after wheel scrolling, got %q", out)
	}
}

func TestCalendarDialog_OAuthLayoutSwitchPreservesValues(t *testing.T) {
	m := oauthDialogFixture(t, "basic")
	rebuild := func() {
		if m.form.onRebuild != nil {
			m.form.onRebuild(&m.form)
		}
	}

	m.form.Field(calDAVIdxSecret).(*TextField).SetValue("hunter2")
	rebuild()

	// basic -> oauth2: enter client config.
	m.form.Field(calDAVIdxAuth).(*SelectField).SetSelected(authOptionIndex("oauth2"))
	rebuild()
	m.form.Field(calDAVIdxOAuthClientID).(*TextField).SetValue("cid.apps")
	m.form.Field(calDAVIdxOAuthClientSecret).(*TextField).SetValue("shh")
	rebuild()

	// oauth2 -> basic: the password survives the round trip.
	m.form.Field(calDAVIdxAuth).(*SelectField).SetSelected(authOptionIndex("basic"))
	rebuild()
	if got := m.form.Field(calDAVIdxSecret).(*TextField).Value(); got != "hunter2" {
		t.Errorf("password lost across layout switch: %q", got)
	}

	// basic -> oauth2 again: the client config survives too.
	m.form.Field(calDAVIdxAuth).(*SelectField).SetSelected(authOptionIndex("oauth2"))
	rebuild()
	if got := m.form.Field(calDAVIdxOAuthClientID).(*TextField).Value(); got != "cid.apps" {
		t.Errorf("client ID lost across layout switch: %q", got)
	}
	if got := m.form.Field(calDAVIdxOAuthClientSecret).(*TextField).Value(); got != "shh" {
		t.Errorf("client secret lost across layout switch: %q", got)
	}
}

func TestCalendarDialog_OAuthSubmitCarriesClientConfig(t *testing.T) {
	m := oauthDialogFixture(t, "oauth2")
	rebuild := func() {
		if m.form.onRebuild != nil {
			m.form.onRebuild(&m.form)
		}
	}
	m.form.Field(cdIdxName).(*TextField).SetValue("gmail")
	m.form.Field(calDAVIdxServer).(*TextField).SetValue("https://apidata.googleusercontent.com/caldav/v2/x/events")
	m.form.Field(calDAVIdxUsername).(*TextField).SetValue("x@gmail.com")
	m.form.Field(calDAVIdxOAuthClientID).(*TextField).SetValue("cid.apps")
	m.form.Field(calDAVIdxOAuthClientSecret).(*TextField).SetValue("shh")
	rebuild()

	cmd := m.form.onSubmit(&m.form)
	if cmd == nil {
		t.Fatal("submit returned nil; validation rejected valid input")
	}
	discovery, ok := cmd().(CalendarDiscoveryRequestedMsg)
	if !ok {
		t.Fatalf("expected CalendarDiscoveryRequestedMsg, got %T", cmd())
	}
	if discovery.AuthType != "oauth2" {
		t.Errorf("AuthType = %q, want oauth2", discovery.AuthType)
	}
	if discovery.OAuthClientID != "cid.apps" || discovery.OAuthClientSecret != "shh" {
		t.Errorf("client config not carried: %+v", discovery)
	}
	if discovery.Secret != "" {
		t.Errorf("oauth2 discovery should not carry a secret; got %q", discovery.Secret)
	}
}

func TestCalendarDialog_OAuthSubmitValidatesClientConfig(t *testing.T) {
	m := oauthDialogFixture(t, "oauth2")
	m.form.Field(cdIdxName).(*TextField).SetValue("gmail")
	m.form.Field(calDAVIdxServer).(*TextField).SetValue("https://example.com/dav/")
	m.form.Field(calDAVIdxUsername).(*TextField).SetValue("x@gmail.com")
	// Client ID/secret left empty.

	if cmd := m.form.onSubmit(&m.form); cmd != nil {
		t.Fatalf("submit should be rejected without client config, got msg %T", cmd())
	}
}
