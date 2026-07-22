package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
)

// managerRoutingModel is the shared fixture for calendar-manager routing
// tests: one local default calendar plus one remote calendar under a Google
// account with a recorded sync error (so attention count is non-zero).
func managerRoutingModel() Model {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendars = map[int64]CalendarInfo{
		1: {Name: "On device", Color: "#a6e3a1", IsDefault: true},
		2: {Name: "Primary", Color: "#89b4fa", AccountID: 7, AccountName: "Personal Google",
			OwnerEmail: "me@example.com", LastSyncError: "token expired"},
	}
	m.accounts = map[int64]account.Account{
		7: {ID: 7, DisplayName: "Personal Google",
			ServerURL: "https://apidata.googleusercontent.com/caldav/v2/",
			AuthType:  "oauth2", Username: "douglas@example.com"},
	}
	return m
}

// TestManageCalendarsRequestOpensManagerRoot locks in the primary cutover:
// the unified CalendarManagerRequestedMsg with the Root target opens the flat
// manager as the sole top-level management overlay at its root list screen.
func TestManageCalendarsRequestOpensManagerRoot(t *testing.T) {
	m := managerRoutingModel()
	updated, cmd := m.Update(CalendarManagerRequestedMsg{Target: CalendarManagerTargetRoot})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("root open returned command %T", cmd())
	}
	if !m.calendarManagerOpen {
		t.Fatal("calendarManagerOpen not set")
	}
	if m.calendarManager.Screen() != CalendarManagerScreenList {
		t.Fatalf("manager screen = %v, want root list", m.calendarManager.Screen())
	}
	if got := m.calendarManager.list.items; len(got) != 2 {
		t.Fatalf("root items = %v, want 2 calendars", got)
	}
}

// TestManageCalendarsShortcutAndPaletteEmitRootRequest guards the entry points
// (the C key and the palette command) produce the unified root request rather
// than the legacy list-dialog request.
func TestManageCalendarsShortcutAndPaletteEmitRootRequest(t *testing.T) {
	m := managerRoutingModel()
	m.keys = defaultAppKeys()
	updated, cmd := m.Update(keyPress("shift+c"))
	if cmd == nil {
		t.Fatal("C key produced no command")
	}
	if msg := cmd(); msg.(CalendarManagerRequestedMsg).Target != CalendarManagerTargetRoot {
		t.Fatalf("C key emitted %T %+v, want root request", msg, msg)
	}
	m = updated.(Model)
	_ = m

	cmds := buildPaletteCommands(managerRoutingModel())
	var manage *PaletteCommand
	for i := range cmds {
		if cmds[i].ID == "calendar.manage" {
			manage = &cmds[i]
		}
	}
	if manage == nil {
		t.Fatal("palette missing calendar.manage command")
	}
	if msg := manage.Action(); msg.(CalendarManagerRequestedMsg).Target != CalendarManagerTargetRoot {
		t.Fatalf("palette manage emitted %T %+v, want root request", msg, msg)
	}
}

// TestSidebarCalendarRequestOpensManagerCalendarDetail verifies a calendar
// target opens the manager's calendar detail with the full canonical params
// (name, color, account linkage, sync health) sourced from the app cache.
func TestSidebarCalendarRequestOpensManagerCalendarDetail(t *testing.T) {
	m := managerRoutingModel()
	updated, cmd := m.Update(CalendarManagerRequestedMsg{
		Target: CalendarManagerTargetCalendar, CalendarID: 2,
	})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("calendar open returned command %T", cmd())
	}
	if !m.calendarManagerOpen || m.calendarManager.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("manager not at calendar detail: open=%v screen=%v",
			m.calendarManagerOpen, m.calendarManager.Screen())
	}
	form, ok := m.calendarManager.CalendarForm()
	if !ok {
		t.Fatal("no active calendar form")
	}
	if form.id != 2 || form.name != "Primary" || !form.linked {
		t.Fatalf("calendar detail params = id %d name %q linked %v", form.id, form.name, form.linked)
	}
	draft := form.Draft()
	if draft.AccountID != 7 || draft.AccountName != "Personal Google" ||
		draft.OwnerEmail != "me@example.com" || draft.LastSyncError != "token expired" ||
		!draft.ManagerEmbedded {
		t.Fatalf("calendar detail draft missing canonical fields: %+v", draft)
	}
}

// TestSidebarAccountRequestOpensManagerAccountDetail verifies an account
// target opens the manager's account detail carrying the canonical provider,
// server, username, calendar count, and attention count.
func TestSidebarAccountRequestOpensManagerAccountDetail(t *testing.T) {
	m := managerRoutingModel()
	updated, cmd := m.Update(CalendarManagerRequestedMsg{
		Target: CalendarManagerTargetAccount, AccountID: 7,
	})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("account open returned command %T", cmd())
	}
	if m.calendarManager.Screen() != CalendarManagerScreenAccount {
		t.Fatalf("manager screen = %v, want account detail", m.calendarManager.Screen())
	}
	panel, ok := m.calendarManager.AccountSettings()
	if !ok {
		t.Fatal("no active account panel")
	}
	p := panel.Params()
	if p.AccountID != 7 || p.DisplayName != "Personal Google" ||
		p.Provider != "Google Account" ||
		p.ServerURL != "https://apidata.googleusercontent.com/caldav/v2/" ||
		p.Username != "douglas@example.com" ||
		p.CalendarCount != 1 || p.AttentionCount != 1 || p.AuthType != "oauth2" {
		t.Fatalf("account params not canonical: %+v", p)
	}
}

// TestAddAccountOpensAccountConnectInsideManager locks the Add Account route:
// AccountAddRequestedMsg opens the manager with the CalDAV connection form as
// its calendar-detail screen.
func TestAddAccountOpensAccountConnectInsideManager(t *testing.T) {
	m := managerRoutingModel()
	updated, cmd := m.Update(AccountAddRequestedMsg{})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("add account returned command %T", cmd())
	}
	if !m.calendarManagerOpen || m.calendarManager.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("manager not showing connection form: open=%v screen=%v",
			m.calendarManagerOpen, m.calendarManager.Screen())
	}
	form, ok := m.calendarManager.CalendarForm()
	if !ok || !form.accountConnection {
		t.Fatalf("connection form not active: ok=%v accountConnection=%v", ok, form.accountConnection)
	}
}

// TestNewLocalCalendarRequestOpensCreateFormInsideManager locks the local-create
// route: a LocalCreate target opens a blank create form hosted by the manager.
func TestNewLocalCalendarRequestOpensCreateFormInsideManager(t *testing.T) {
	m := managerRoutingModel()
	updated, cmd := m.Update(CalendarManagerRequestedMsg{Target: CalendarManagerTargetLocalCreate})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("local create returned command %T", cmd())
	}
	if m.calendarManager.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("manager screen = %v, want calendar detail", m.calendarManager.Screen())
	}
	form, ok := m.calendarManager.CalendarForm()
	if !ok || form.id != 0 {
		t.Fatalf("create form not active: ok=%v id=%d", ok, form.id)
	}
}

// TestManagerMutationRefreshesDataWithoutClosing verifies a successful
// calendar mutation pops the detail back to the root and the manager stays
// open, with the root list refreshed from the reloaded calendars.
func TestManagerMutationRefreshesDataWithoutClosing(t *testing.T) {
	m := managerRoutingModel()
	// Open the manager at calendar detail.
	updated, _ := m.Update(CalendarManagerRequestedMsg{
		Target: CalendarManagerTargetCalendar, CalendarID: 2,
	})
	m = updated.(Model)

	// Successful mutation: the detail pops to root; the manager stays open.
	updated, cmd := m.Update(calendarMutationDoneMsg{err: nil})
	m = updated.(Model)
	if !m.calendarManagerOpen {
		t.Fatal("manager closed after successful mutation")
	}
	if m.calendarManager.Screen() != CalendarManagerScreenList {
		t.Fatalf("manager screen = %v, want root after mutation", m.calendarManager.Screen())
	}
	if cmd == nil {
		t.Fatal("successful mutation did not schedule a reload")
	}

	// The reload refreshes the root data; renaming calendar 2 surfaces there.
	updated, _ = m.Update(calendarsLoadedMsg{calendars: map[int64]CalendarInfo{
		1: {Name: "On device", IsDefault: true},
		2: {Name: "Primary Renamed", AccountID: 7, AccountName: "Personal Google"},
	}})
	m = updated.(Model)
	if got := managerCalendarLine(t, m.calendarManager, 2); !strings.Contains(got, "Primary Renamed") {
		t.Fatalf("root not refreshed after reload: row=%q", got)
	}
}

// TestManagerMutationErrorKeepsChildOpen verifies a failed mutation leaves the
// originating calendar detail open and surfaces the error on its Name field.
func TestManagerMutationErrorKeepsChildOpen(t *testing.T) {
	m := managerRoutingModel()
	updated, _ := m.Update(CalendarManagerRequestedMsg{
		Target: CalendarManagerTargetCalendar, CalendarID: 2,
	})
	m = updated.(Model)

	updated, cmd := m.Update(calendarMutationDoneMsg{err: errors.New("name taken")})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("failed mutation returned command %T", cmd())
	}
	if m.calendarManager.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("manager screen = %v, want calendar detail preserved on error", m.calendarManager.Screen())
	}
	form, ok := m.calendarManager.CalendarForm()
	if !ok {
		t.Fatal("calendar detail dropped after failed mutation")
	}
	if got := form.form.error; got != "name taken" {
		t.Fatalf("form error = %q, want %q", got, "name taken")
	}
}

// TestCalendarManagerClosedMsgTearsDownOverlay verifies the manager's close
// message clears the single top-level management flag.
func TestCalendarManagerClosedMsgTearsDownOverlay(t *testing.T) {
	m := managerRoutingModel()
	updated, cmd := m.Update(CalendarManagerClosedMsg{})
	m = updated.(Model)
	if cmd != nil || m.calendarManagerOpen {
		t.Fatalf("closed msg did not tear down: cmd=%v open=%v", cmd, m.calendarManagerOpen)
	}
}

// TestPaletteRetainsOnlyUnifiedCalendarManagement guards the single
// Calendars entry point and rejects obsolete direct account/local actions.
func TestPaletteRetainsOnlyUnifiedCalendarManagement(t *testing.T) {
	cmds := buildPaletteCommands(managerRoutingModel())
	count := 0
	for _, command := range cmds {
		if command.ID == "calendar.manage" {
			count++
		}
		if command.ID == "calendar.new" || strings.HasPrefix(command.ID, "account.") {
			t.Errorf("palette exposes obsolete calendar command %q", command.ID)
		}
	}
	if count != 1 {
		t.Fatalf("calendar.manage count = %d, want 1", count)
	}
}

// TestManagerOpenDefersQuitKeyToOverlay verifies that while the manager is
// open, the global q does not open the quit confirm — the manager owns q to
// close itself (issue #406 parity with the other read-only overlays).
func TestManagerOpenDefersQuitKeyToOverlay(t *testing.T) {
	m := managerRoutingModel()
	m.keys = defaultAppKeys()
	m.calendarManagerOpen = true
	_, _, handled := m.interceptGlobalKeys(keyPress("q"))
	if handled {
		t.Fatal("q was intercepted while manager open; the manager should own q")
	}
}

// keyPress builds a KeyPressMsg for a literal key string, mirroring how the
// app's key bindings are matched.
func keyPress(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: s}
}

func TestDirectAccountCloseReturnsToCalendarList(t *testing.T) {
	m := managerRoutingModel()
	updated, _ := m.Update(CalendarManagerRequestedMsg{Target: CalendarManagerTargetAccount, AccountID: 7})
	m = updated.(Model)

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("direct account close emitted no close command")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if !m.calendarManagerOpen || m.calendarManager.Screen() != CalendarManagerScreenList {
		t.Fatalf("account close did not restore manager list: open=%v screen=%v", m.calendarManagerOpen, m.calendarManager.Screen())
	}
}

func TestAddAccountCancelReturnsToCalendarList(t *testing.T) {
	m := managerRoutingModel()
	updated, _ := m.Update(AccountAddRequestedMsg{})
	m = updated.(Model)

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("account connection cancel emitted no close command")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if !m.calendarManagerOpen || m.calendarManager.Screen() != CalendarManagerScreenList {
		t.Fatalf("account cancel did not restore manager list: open=%v screen=%v", m.calendarManagerOpen, m.calendarManager.Screen())
	}
}

func TestCalendarManagerVisibilityUpdatesSidebarState(t *testing.T) {
	m := managerRoutingModel()
	m.sidebar = m.sidebar.SetList(NewCalendarListModel([]CalendarListItem{
		{ID: 1, Name: "On device"},
		{ID: 2, Name: "Primary", AccountID: 7, AccountName: "Personal Google"},
	}, nil).SetSize(40, 20))

	updated, _ := m.Update(CalendarVisibilityToggledMsg{ID: 2, Hidden: true})
	m = updated.(Model)
	if !m.hiddenCalendars[2] || !m.sidebar.List().HiddenSet()[2] {
		t.Fatalf("hidden state diverged: app=%v sidebar=%v", m.hiddenCalendars, m.sidebar.List().HiddenSet())
	}
	if view := stripANSI(m.sidebar.List().View()); !strings.Contains(view, Glyphs["checkbox.off"]+" ● Primary") {
		t.Fatalf("sidebar did not render hidden calendar marker:\n%s", view)
	}

	updated, _ = m.Update(CalendarVisibilityToggledMsg{ID: 2, Hidden: false})
	m = updated.(Model)
	if m.hiddenCalendars[2] || m.sidebar.List().HiddenSet()[2] {
		t.Fatalf("visible state diverged: app=%v sidebar=%v", m.hiddenCalendars, m.sidebar.List().HiddenSet())
	}
	if view := stripANSI(m.sidebar.List().View()); !strings.Contains(view, Glyphs["checkbox.on"]+" ● Primary") {
		t.Fatalf("sidebar did not render visible calendar marker:\n%s", view)
	}
}
