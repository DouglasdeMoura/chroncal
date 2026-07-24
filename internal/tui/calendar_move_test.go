package tui

import (
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/caldav"
)

func calendarMoveModel() Model {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendars = map[int64]CalendarInfo{
		1: {Name: "Local", IsDefault: true},
		2: {Name: "Remote", Synced: true, AccountID: 7},
	}
	m.accounts = map[int64]account.Account{
		7: {ID: 7, DisplayName: "Work", Name: "Work", DisplayOrder: 2},
		8: {ID: 8, DisplayName: "Personal", Name: "Personal", DisplayOrder: 1},
	}
	m.calendarManagerOpen = true
	return m
}

func TestCalendarMoveBeginsWithAccountChoiceForLocalCalendar(t *testing.T) {
	m, cmd := calendarMoveModel().beginCalendarMove(CalendarMoveToAccountRequestedMsg{ID: 1, Name: "Local"})
	if cmd != nil {
		t.Fatal("opening account choice unexpectedly started async work")
	}
	if !m.choiceOpen || m.pendingScopeKind != pendingScopeCalendarMoveAccount || m.pendingCalendarMove == nil {
		t.Fatalf("move account choice not open: open=%v kind=%v pending=%+v", m.choiceOpen, m.pendingScopeKind, m.pendingCalendarMove)
	}
	plain := stripANSI(m.choiceDialog.View())
	if !strings.Contains(plain, "Move \"Local\" to which account?") ||
		!strings.Contains(plain, "Personal") || !strings.Contains(plain, "Work") {
		t.Fatalf("account choice missing expected content:\n%s", plain)
	}
	if got := m.pendingCalendarMove.accounts[0].ID; got != 8 {
		t.Fatalf("first account ID = %d, want display-order account 8", got)
	}
}

func TestCalendarMoveRejectsRemoteSource(t *testing.T) {
	m, _ := calendarMoveModel().beginCalendarMove(CalendarMoveToAccountRequestedMsg{ID: 2, Name: "Remote"})
	if m.choiceOpen || m.pendingCalendarMove != nil {
		t.Fatal("remote calendar entered Move to Account flow")
	}
}

func TestCalendarMoveAccountChoiceStartsDiscoveryAndCancelIsNonDestructive(t *testing.T) {
	m, _ := calendarMoveModel().beginCalendarMove(CalendarMoveToAccountRequestedMsg{ID: 1, Name: "Local"})
	m, cmd, handled := m.handleCalendarMoveChoice(pendingScopeCalendarMoveAccount, 0)
	if !handled || cmd == nil || !m.syncing || m.pendingCalendarMove.account.ID != 8 {
		t.Fatalf("account choice not routed: handled=%v cmd=%v syncing=%v pending=%+v", handled, cmd != nil, m.syncing, m.pendingCalendarMove)
	}

	cancelled, _, handled := m.handleCalendarMoveChoice(pendingScopeCalendarMoveAccount, -1)
	if !handled || cancelled.pendingCalendarMove != nil {
		t.Fatalf("cancel left migration state behind: handled=%v pending=%+v", handled, cancelled.pendingCalendarMove)
	}
	if _, ok := cancelled.calendars[1]; !ok {
		t.Fatal("cancel removed the local calendar")
	}
}

func TestCalendarMoveDiscoveryOffersOnlyWritableCollections(t *testing.T) {
	m, _ := calendarMoveModel().beginCalendarMove(CalendarMoveToAccountRequestedMsg{ID: 1, Name: "Local"})
	m.pendingCalendarMove.account = m.pendingCalendarMove.accounts[0]
	m.syncing = true
	discovery := account.Discovery{
		Account: m.pendingCalendarMove.account,
		Calendars: []account.DiscoveredCalendar{
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/write/", Name: "Writable", Access: caldav.CalendarAccessWrite}, Importable: true},
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/read/", Name: "Read only", Access: caldav.CalendarAccessRead}, Importable: true},
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/freebusy/", Name: "Availability", Access: caldav.CalendarAccessWrite}, Importable: false},
		},
	}
	m, cmd := m.finishCalendarMoveDiscovery(calendarMoveDiscoveryReadyMsg{
		sourceID: 1, accountID: m.pendingCalendarMove.account.ID, discovery: discovery,
	})
	if cmd != nil || !m.choiceOpen || m.pendingScopeKind != pendingScopeCalendarMoveCollection {
		t.Fatalf("collection choice not opened: cmd=%v open=%v kind=%v", cmd != nil, m.choiceOpen, m.pendingScopeKind)
	}
	if got := len(m.pendingCalendarMove.collections); got != 1 || m.pendingCalendarMove.collections[0].Path != "/write/" {
		t.Fatalf("destination collections = %+v, want only writable collection", m.pendingCalendarMove.collections)
	}
	plain := stripANSI(m.choiceDialog.View())
	if !strings.Contains(plain, "Writable") || strings.Contains(plain, "Read only") || strings.Contains(plain, "Availability") {
		t.Fatalf("collection choice contains wrong rows:\n%s", plain)
	}
}

func TestCalendarMoveCollectionChoiceStartsAtomicMigration(t *testing.T) {
	m, _ := calendarMoveModel().beginCalendarMove(CalendarMoveToAccountRequestedMsg{ID: 1, Name: "Local"})
	m.pendingCalendarMove.account = m.pendingCalendarMove.accounts[0]
	m.pendingCalendarMove.discovery = account.Discovery{Account: m.pendingCalendarMove.account}
	m.pendingCalendarMove.collections = []account.DiscoveredCalendar{{
		RemoteCalendar: caldav.RemoteCalendar{Path: "/write/", Name: "Writable", Access: caldav.CalendarAccessWrite},
		Importable:     true,
	}}
	m, cmd, handled := m.handleCalendarMoveChoice(pendingScopeCalendarMoveCollection, 0)
	if !handled || cmd == nil || !m.syncing {
		t.Fatalf("collection choice did not start migration: handled=%v cmd=%v syncing=%v", handled, cmd != nil, m.syncing)
	}
	if _, ok := m.calendars[1]; !ok {
		t.Fatal("calendar was retired before migration command succeeded")
	}
}

func TestCalendarMoveFailureKeepsManagerAndSource(t *testing.T) {
	m, _ := calendarMoveModel().beginCalendarMove(CalendarMoveToAccountRequestedMsg{ID: 1, Name: "Local"})
	m.pendingCalendarMove.account = m.pendingCalendarMove.accounts[0]
	m.syncing = true
	m, _ = m.finishCalendarMove(calendarMoveFinishedMsg{
		sourceID: 1, account: m.pendingCalendarMove.account, err: errTestReauth,
	})
	if !m.calendarManagerOpen || m.syncing {
		t.Fatalf("failed move changed host state: manager=%v syncing=%v", m.calendarManagerOpen, m.syncing)
	}
	if _, ok := m.calendars[1]; !ok {
		t.Fatal("failed move removed source calendar")
	}
	if !strings.Contains(m.syncStatus, "Couldn't move calendar") {
		t.Fatalf("failure status = %q", m.syncStatus)
	}
}

func TestCalendarMoveGlobalPendingCleanupAndStaleMessages(t *testing.T) {
	m, _ := calendarMoveModel().beginCalendarMove(CalendarMoveToAccountRequestedMsg{ID: 1, Name: "Local"})
	m = m.clearConfirmPending()
	if m.pendingCalendarMove != nil || m.pendingScopeKind != pendingScopeNone || m.choiceOpen {
		t.Fatalf("global cleanup left move state armed: pending=%+v kind=%v choice=%v", m.pendingCalendarMove, m.pendingScopeKind, m.choiceOpen)
	}

	m.syncing = true // an unrelated sync started after the move was cancelled
	m, _ = m.finishCalendarMoveDiscovery(calendarMoveDiscoveryReadyMsg{sourceID: 1, accountID: 8})
	if !m.syncing {
		t.Fatal("stale discovery result cleared unrelated sync state")
	}
	m, _ = m.finishCalendarMove(calendarMoveFinishedMsg{sourceID: 1, account: account.Account{ID: 8}})
	if !m.syncing {
		t.Fatal("stale migration result cleared unrelated sync state")
	}
}
