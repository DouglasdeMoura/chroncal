package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

func moveState(m Model) *calendarMoveState {
	return m.pending.target.move
}

func TestCalendarMoveBeginsWithAccountChoiceForLocalCalendar(t *testing.T) {
	m, cmd := calendarMoveModel().beginCalendarMove(CalendarMoveToAccountRequestedMsg{ID: 1, Name: "Local"})
	if cmd != nil {
		t.Fatal("opening account choice unexpectedly started async work")
	}
	if !m.choiceOpen || m.pending.kind != pendingActionCalendarMoveAccount || moveState(m) == nil {
		t.Fatalf("move account choice not open: open=%v kind=%v pending=%+v", m.choiceOpen, m.pending.kind, moveState(m))
	}
	plain := stripANSI(m.choiceDialog.View())
	if !strings.Contains(plain, "Move \"Local\" to which account?") ||
		!strings.Contains(plain, "Personal") || !strings.Contains(plain, "Work") {
		t.Fatalf("account choice missing expected content:\n%s", plain)
	}
	if got := moveState(m).accounts[0].ID; got != 8 {
		t.Fatalf("first account ID = %d, want display-order account 8", got)
	}
}

func TestCalendarMoveRejectsRemoteSource(t *testing.T) {
	m, _ := calendarMoveModel().beginCalendarMove(CalendarMoveToAccountRequestedMsg{ID: 2, Name: "Remote"})
	if m.choiceOpen || moveState(m) != nil {
		t.Fatal("remote calendar entered Move to Account flow")
	}
}

func TestCalendarMoveAccountChoiceStartsDiscoveryAndCancelIsNonDestructive(t *testing.T) {
	m, _ := calendarMoveModel().beginCalendarMove(CalendarMoveToAccountRequestedMsg{ID: 1, Name: "Local"})
	updated, cmd := m.calendarMoveChoice(m.pending, 0)
	m = updated.(Model)
	if cmd == nil || !m.syncing || moveState(m).account.ID != 8 {
		t.Fatalf("account choice not routed: cmd=%v syncing=%v pending=%+v", cmd != nil, m.syncing, moveState(m))
	}

	cancelled, _ := m.Update(ChoiceDialogResultMsg{Choice: -1})
	cm := cancelled.(Model)
	if moveState(cm) != nil {
		t.Fatalf("cancel left migration state behind: pending=%+v", moveState(cm))
	}
	if _, ok := cm.calendars[1]; !ok {
		t.Fatal("cancel removed the local calendar")
	}
}

func TestCalendarMoveDiscoveryOffersOnlyWritableCollections(t *testing.T) {
	m, _ := calendarMoveModel().beginCalendarMove(CalendarMoveToAccountRequestedMsg{ID: 1, Name: "Local"})
	moveState(m).account = moveState(m).accounts[0]
	m.syncing = true
	state := *moveState(m)
	discovery := account.Discovery{
		Account: state.account,
		Calendars: []account.DiscoveredCalendar{
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/write/", Name: "Writable", Access: caldav.CalendarAccessWrite}, Importable: true},
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/read/", Name: "Read only", Access: caldav.CalendarAccessRead}, Importable: true},
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/freebusy/", Name: "Availability", Access: caldav.CalendarAccessWrite}, Importable: false},
		},
	}
	m, cmd := m.finishCalendarMoveDiscovery(calendarMoveDiscoveryReadyMsg{
		state:     state,
		discovery: discovery,
	})
	if cmd != nil || !m.choiceOpen || m.pending.kind != pendingActionCalendarMoveCollection {
		t.Fatalf("collection choice not opened: cmd=%v open=%v kind=%v", cmd != nil, m.choiceOpen, m.pending.kind)
	}
	if got := len(moveState(m).collections); got != 1 || moveState(m).collections[0].Path != "/write/" {
		t.Fatalf("destination collections = %+v, want only writable collection", moveState(m).collections)
	}
	plain := stripANSI(m.choiceDialog.View())
	if !strings.Contains(plain, "Writable") || strings.Contains(plain, "Read only") || strings.Contains(plain, "Availability") {
		t.Fatalf("collection choice contains wrong rows:\n%s", plain)
	}
}

func TestCalendarMoveCollectionChoiceStartsAtomicMigration(t *testing.T) {
	m, _ := calendarMoveModel().beginCalendarMove(CalendarMoveToAccountRequestedMsg{ID: 1, Name: "Local"})
	moveState(m).account = moveState(m).accounts[0]
	moveState(m).discovery = account.Discovery{Account: moveState(m).account}
	moveState(m).collections = []account.DiscoveredCalendar{{
		RemoteCalendar: caldav.RemoteCalendar{Path: "/write/", Name: "Writable", Access: caldav.CalendarAccessWrite},
		Importable:     true,
	}}
	m.pending.kind = pendingActionCalendarMoveCollection
	updated, cmd := m.calendarMoveChoice(m.pending, 0)
	m = updated.(Model)
	if cmd == nil || !m.syncing {
		t.Fatalf("collection choice did not start migration: cmd=%v syncing=%v", cmd != nil, m.syncing)
	}
	if _, ok := m.calendars[1]; !ok {
		t.Fatal("calendar was retired before migration command succeeded")
	}
}

func TestCalendarMoveFailureKeepsManagerAndSource(t *testing.T) {
	m, _ := calendarMoveModel().beginCalendarMove(CalendarMoveToAccountRequestedMsg{ID: 1, Name: "Local"})
	moveState(m).account = moveState(m).accounts[0]
	m.syncing = true
	m, _ = m.finishCalendarMove(calendarMoveFinishedMsg{
		sourceID: 1, account: moveState(m).account, err: errTestReauth,
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
	m = m.clearPending()
	if moveState(m) != nil || m.pending.kind != pendingActionNone || m.choiceOpen {
		t.Fatalf("global cleanup left move state armed: pending=%+v kind=%v choice=%v", moveState(m), m.pending.kind, m.choiceOpen)
	}

	m.syncing = true // an unrelated sync started after the move was cancelled
	m, _ = m.finishCalendarMoveDiscovery(calendarMoveDiscoveryReadyMsg{
		state: calendarMoveState{sourceID: 1, account: account.Account{ID: 8}},
	})
	if !m.syncing {
		t.Fatal("stale discovery result cleared unrelated sync state")
	}
	m, _ = m.finishCalendarMove(calendarMoveFinishedMsg{sourceID: 1, account: account.Account{ID: 8}})
	if !m.syncing {
		t.Fatal("stale migration result cleared unrelated sync state")
	}
}

func TestCalendarMoveCtrlCDuringDiscoveryClearsSyncing(t *testing.T) {
	m, _ := calendarMoveModel().beginCalendarMove(CalendarMoveToAccountRequestedMsg{ID: 1, Name: "Local"})
	updated, cmd := m.calendarMoveChoice(m.pending, 0)
	m = updated.(Model)
	if cmd == nil || !m.syncing {
		t.Fatalf("discovery not started: cmd=%v syncing=%v", cmd != nil, m.syncing)
	}
	state := *moveState(m)

	next, _, handled := m.interceptGlobalKeys(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("ctrl+c not handled during calendar-move discovery")
	}
	m = next
	if m.syncing {
		t.Fatal("ctrl+c during discovery left syncing true")
	}
	if m.pending.isCalendarMove() {
		t.Fatal("ctrl+c left calendar move pending")
	}

	m, _ = m.finishCalendarMoveDiscovery(calendarMoveDiscoveryReadyMsg{
		state: state,
		discovery: account.Discovery{Account: state.account, Calendars: []account.DiscoveredCalendar{{
			RemoteCalendar: caldav.RemoteCalendar{Path: "/write/", Name: "Writable", Access: caldav.CalendarAccessWrite},
			Importable:     true,
		}}},
	})
	if m.syncing {
		t.Fatal("stale discovery after ctrl+c re-armed syncing")
	}
	if m.choiceOpen {
		t.Fatal("stale discovery opened a collection choice after ctrl+c")
	}
}
