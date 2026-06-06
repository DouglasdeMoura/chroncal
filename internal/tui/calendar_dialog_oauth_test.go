package tui

import (
	"strings"
	"testing"

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
	m.form.Field(cdIdxAuth).(*SelectField).SetSelected(authOptionIndex(authType))
	rebuild()
	return m
}

func TestCalendarDialog_OAuthLayoutSwapsRows(t *testing.T) {
	m := oauthDialogFixture(t, "oauth2")

	if got := m.form.ItemCount(); got != cdIdxOAuthAllowInsecure+1 {
		t.Fatalf("ItemCount = %d, want %d (oauth layout)", got, cdIdxOAuthAllowInsecure+1)
	}
	if _, ok := m.form.Field(cdIdxOAuthClientID).(*TextField); !ok {
		t.Errorf("row %d should be the Client ID TextField", cdIdxOAuthClientID)
	}
	if _, ok := m.form.Field(cdIdxOAuthClientSecret).(*TextField); !ok {
		t.Errorf("row %d should be the Client secret TextField", cdIdxOAuthClientSecret)
	}
	if _, ok := m.form.Field(cdIdxOAuthAllowInsecure).(*CheckboxField); !ok {
		t.Errorf("row %d should be the HTTP checkbox", cdIdxOAuthAllowInsecure)
	}
	view := m.form.View()
	if !strings.Contains(view, "Client ID") || !strings.Contains(view, "Client secret") {
		t.Errorf("oauth layout should render client config labels; got %q", view)
	}
}

func TestCalendarDialog_OAuthLayoutSwitchPreservesValues(t *testing.T) {
	m := oauthDialogFixture(t, "basic")
	rebuild := func() {
		if m.form.onRebuild != nil {
			m.form.onRebuild(&m.form)
		}
	}

	m.form.Field(cdIdxPassword).(*TextField).SetValue("hunter2")
	rebuild()

	// basic -> oauth2: enter client config.
	m.form.Field(cdIdxAuth).(*SelectField).SetSelected(authOptionIndex("oauth2"))
	rebuild()
	m.form.Field(cdIdxOAuthClientID).(*TextField).SetValue("cid.apps")
	m.form.Field(cdIdxOAuthClientSecret).(*TextField).SetValue("shh")
	rebuild()

	// oauth2 -> basic: the password survives the round trip.
	m.form.Field(cdIdxAuth).(*SelectField).SetSelected(authOptionIndex("basic"))
	rebuild()
	if got := m.form.Field(cdIdxPassword).(*TextField).Value(); got != "hunter2" {
		t.Errorf("password lost across layout switch: %q", got)
	}

	// basic -> oauth2 again: the client config survives too.
	m.form.Field(cdIdxAuth).(*SelectField).SetSelected(authOptionIndex("oauth2"))
	rebuild()
	if got := m.form.Field(cdIdxOAuthClientID).(*TextField).Value(); got != "cid.apps" {
		t.Errorf("client ID lost across layout switch: %q", got)
	}
	if got := m.form.Field(cdIdxOAuthClientSecret).(*TextField).Value(); got != "shh" {
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
	m.form.Field(cdIdxRemoteURL).(*TextField).SetValue("https://apidata.googleusercontent.com/caldav/v2/x/events")
	m.form.Field(cdIdxUsername).(*TextField).SetValue("x@gmail.com")
	m.form.Field(cdIdxOAuthClientID).(*TextField).SetValue("cid.apps")
	m.form.Field(cdIdxOAuthClientSecret).(*TextField).SetValue("shh")
	rebuild()

	cmd := m.form.onSubmit(&m.form)
	if cmd == nil {
		t.Fatal("submit returned nil; validation rejected valid input")
	}
	saved, ok := cmd().(CalendarSavedMsg)
	if !ok {
		t.Fatalf("expected CalendarSavedMsg, got %T", cmd())
	}
	if saved.AuthType != "oauth2" {
		t.Errorf("AuthType = %q, want oauth2", saved.AuthType)
	}
	if saved.OAuthClientID != "cid.apps" || saved.OAuthClientSecret != "shh" {
		t.Errorf("client config not carried: %+v", saved)
	}
	if saved.Password != "" {
		t.Errorf("oauth2 save should not carry a password; got %q", saved.Password)
	}
}

func TestCalendarDialog_OAuthSubmitValidatesClientConfig(t *testing.T) {
	m := oauthDialogFixture(t, "oauth2")
	m.form.Field(cdIdxName).(*TextField).SetValue("gmail")
	m.form.Field(cdIdxRemoteURL).(*TextField).SetValue("https://example.com/dav/")
	m.form.Field(cdIdxUsername).(*TextField).SetValue("x@gmail.com")
	// Client ID/secret left empty.

	if cmd := m.form.onSubmit(&m.form); cmd != nil {
		t.Fatalf("submit should be rejected without client config, got msg %T", cmd())
	}
}

func TestCalendarDialog_ReauthButtonOnLinkedOAuth(t *testing.T) {
	m := NewCalendarDialogModel(CalendarDialogParams{
		ID:             10,
		Name:           "gmail",
		Color:          "#9FE1E7",
		RemoteURL:      "https://apidata.googleusercontent.com/caldav/v2/x/events",
		RemoteLinked:   true,
		RemoteAuthType: "oauth2",
		RemoteUsername: "x@gmail.com",
	}, Theme{}).SetSize(120, 40)

	view := m.View()
	if !strings.Contains(view, "Re-authenticate") {
		t.Errorf("linked oauth2 dialog should offer Re-authenticate; got %q", view)
	}

	// The button must emit the request msg with empty config (use stored).
	var found bool
	for _, b := range m.form.actionButtons {
		if b.Label == "Re-authenticate" {
			found = true
			msg, ok := b.OnPress().(CalendarReauthRequestedMsg)
			if !ok {
				t.Fatalf("expected CalendarReauthRequestedMsg, got %T", b.OnPress())
			}
			if msg.ID != 10 || msg.Name != "gmail" {
				t.Errorf("msg = %+v, want ID 10 / gmail", msg)
			}
			if msg.ClientID != "" || msg.ClientSecret != "" {
				t.Errorf("non-fallback reauth should carry empty config; got %+v", msg)
			}
		}
	}
	if !found {
		t.Fatal("Re-authenticate button not registered")
	}
}

func TestCalendarDialog_NoReauthButtonOnBasicLinked(t *testing.T) {
	m := NewCalendarDialogModel(CalendarDialogParams{
		ID:             2,
		Name:           "GMX",
		RemoteLinked:   true,
		RemoteAuthType: "basic",
	}, Theme{}).SetSize(120, 40)
	if strings.Contains(m.View(), "Re-authenticate") {
		t.Error("basic-auth linked dialog should not offer Re-authenticate")
	}
}

func TestCalendarDialog_NeedOAuthConfigFallback(t *testing.T) {
	m := NewCalendarDialogModel(CalendarDialogParams{
		ID:                   10,
		Name:                 "gmail",
		RemoteLinked:         true,
		RemoteAuthType:       "oauth2",
		NeedOAuthConfig:      true,
		OAuthClientIDPrefill: "stored-cid.apps",
	}, Theme{}).SetSize(120, 40)

	view := m.View()
	if !strings.Contains(view, "OAuth client config") {
		t.Errorf("fallback dialog should explain why config entry is needed; got %q", view)
	}

	// Find the editable fields, type a secret, and check the button reads
	// both at press time.
	var idField, secretField *TextField
	for i := 0; i < m.form.ItemCount(); i++ {
		if tf, ok := m.form.Field(i).(*TextField); ok && i > cdIdxEmail {
			if idField == nil {
				idField = tf
			} else {
				secretField = tf
			}
		}
	}
	if idField == nil || secretField == nil {
		t.Fatal("fallback dialog should append Client ID and secret fields")
	}
	if got := idField.Value(); got != "stored-cid.apps" {
		t.Errorf("Client ID prefill = %q, want stored-cid.apps", got)
	}
	secretField.SetValue("typed-secret")

	for _, b := range m.form.actionButtons {
		if b.Label == "Re-authenticate" {
			msg := b.OnPress().(CalendarReauthRequestedMsg)
			if msg.ClientID != "stored-cid.apps" || msg.ClientSecret != "typed-secret" {
				t.Errorf("fallback button should read fields at press time; got %+v", msg)
			}
			return
		}
	}
	t.Fatal("Re-authenticate button not registered in fallback mode")
}

func TestCalendarDialog_ReLinkHintPointsAtButtonForOAuth(t *testing.T) {
	lines := syncHealthDialogLines(CalendarDialogParams{
		Name:           "gmail",
		RemoteLinked:   true,
		RemoteAuthType: "oauth2",
		LastSyncError:  `token refresh failed (400): {"error": "invalid_grant"}`,
	}, Theme{})
	if len(lines) != 2 {
		t.Fatalf("expected error + hint lines, got %+v", lines)
	}
	if !strings.Contains(lines[1].text, "Re-authenticate") {
		t.Errorf("oauth hint should point at the in-app button; got %+v", lines[1])
	}
	if strings.Contains(lines[1].text, "chroncal calendar update") {
		t.Errorf("oauth hint should not point at the CLI anymore; got %+v", lines[1])
	}
}

// TestCalendarDialog_LinkedOAuthErrorFitsWidth reproduces the "crumbled"
// edit dialog: a linked Google calendar with a long CalDAV URL, a sync
// error, and all three action buttons (Set as Default + Re-authenticate +
// Disconnect). Every rendered line must fit the dialog's content width —
// no mid-word URL wraps, no button row spilling onto a ragged second line.
func TestCalendarDialog_LinkedOAuthErrorFitsWidth(t *testing.T) {
	m := NewCalendarDialogModel(CalendarDialogParams{
		ID:             12,
		Name:           "douglas.demoura@familywellhealth.com",
		Color:          "#FD7941",
		Description:    "Shared family schedule",
		OwnerEmail:     "douglas.demoura@familywellhealth.com",
		RemoteURL:      "https://apidata.googleusercontent.com/caldav/v2/douglas.demoura@familywellhealth.com/events/",
		RemoteLinked:   true,
		RemoteAuthType: "oauth2",
		RemoteUsername: "douglas.demoura@familywellhealth.com",
		LastSyncError:  `oauth token refresh: token refresh failed (400): {"error": "invalid_grant"}`,
		IsDefault:      false, // keeps the Set as Default button in play
	}, Theme{}).SetSize(120, 40)

	cw := m.dialog.ContentWidth()
	if cw <= 0 {
		t.Fatal("ContentWidth not set")
	}
	for i, l := range strings.Split(m.form.View(), "\n") {
		if w := lipgloss.Width(l); w > cw {
			t.Errorf("form line %d is %d cols, exceeds content width %d: %q", i, w, cw, l)
		}
	}
}

// TestCalendarDialog_OverflowButtonRowLayout pins the two-row button
// degradation: leading actions spread across their own row (first left,
// last flush right), one blank line, then Save/Cancel right-aligned to the
// same edge.
func TestCalendarDialog_OverflowButtonRowLayout(t *testing.T) {
	m := NewCalendarDialogModel(CalendarDialogParams{
		ID:             12,
		Name:           "douglas.demoura@familywellhealth.com",
		RemoteURL:      "https://apidata.googleusercontent.com/caldav/v2/x/events/",
		RemoteLinked:   true,
		RemoteAuthType: "oauth2",
	}, Theme{}).SetSize(120, 40)

	lines := strings.Split(m.form.View(), "\n")
	actionRow, saveRow := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "Disconnect") {
			actionRow = i
		}
		if strings.Contains(l, "Save") && strings.Contains(l, "Cancel") {
			saveRow = i
		}
	}
	if actionRow < 0 || saveRow < 0 {
		t.Fatalf("missing rows: actionRow=%d saveRow=%d\n%s", actionRow, saveRow, m.form.View())
	}
	if !strings.Contains(lines[actionRow], "Re-authenticate") || !strings.Contains(lines[actionRow], "Set as Default") {
		t.Errorf("all three actions should share one row; got %q", lines[actionRow])
	}
	if saveRow != actionRow+2 || strings.TrimSpace(lines[actionRow+1]) != "" {
		t.Errorf("want one blank line between actions and Save/Cancel; rows %d and %d", actionRow, saveRow)
	}
	// Disconnect's row is justified to the full form width, so its right
	// edge matches the right-aligned Save/Cancel row.
	if aw, sw := lipgloss.Width(strings.TrimRight(lines[actionRow], " ")), lipgloss.Width(strings.TrimRight(lines[saveRow], " ")); aw != sw {
		t.Errorf("Disconnect right edge (%d) should match Save/Cancel right edge (%d)", aw, sw)
	}
}
