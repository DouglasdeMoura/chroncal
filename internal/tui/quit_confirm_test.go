package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/trash"
)

func pendingQuit(m Model) bool {
	return m.pending.kind == pendingActionQuit
}

// TestCtrlCConvertsNonQuitConfirmToQuit reproduces issue #143. ctrl+c is
// documented as "truly global". When a destructive (non-quit) confirm is
// open it used to fall through and be swallowed. ctrl+c must instead replace
// the open confirm with the quit confirm. It must clear the abandoned
// destructive wait state so it cannot fire later.
func TestCtrlCConvertsNonQuitConfirmToQuit(t *testing.T) {
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

	m := Model{}
	// Simulate an open event-delete confirm: confirm dialog is up but it is
	// NOT the quit confirm.
	m.confirmOpen = true
	m.pending = pendingAction{
		kind:   pendingActionEventDelete,
		target: pendingTarget{ev: event.Event{ID: 7, Title: "Standup"}},
	}

	next, _, handled := m.interceptGlobalKeys(ctrlC)
	if !handled {
		t.Fatalf("ctrl+c not handled while a non-quit confirm is open (issue #143)")
	}
	if !next.confirmOpen || !pendingQuit(next) {
		t.Fatalf("ctrl+c should replace the open confirm with the quit confirm: confirmOpen=%v kind=%v", next.confirmOpen, next.pending.kind)
	}
	if next.pending.target.ev.ID != 0 {
		t.Fatalf("abandoned destructive pending state not cleared: event ID=%d", next.pending.target.ev.ID)
	}

	// A second ctrl+c now force-quits.
	_, cmd, handled := next.interceptGlobalKeys(ctrlC)
	if !handled || cmd == nil {
		t.Fatalf("second ctrl+c should force quit: handled=%v cmd=%v", handled, cmd)
	}
}

// TestCtrlCClearsPurgePendingState guards the trash-purge variant of the same
// bug. The bulk-purge confirm must also be convertible to a quit. Its wait
// entries must not stay armed.
func TestCtrlCClearsPurgePendingState(t *testing.T) {
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

	m := Model{}
	m.confirmOpen = true
	m.pending = pendingAction{
		kind:   pendingActionPurgeEntries,
		label:  "1 item",
		target: pendingTarget{entries: []trash.Entry{{Kind: trash.KindEvent}}},
	}

	next, _, handled := m.interceptGlobalKeys(ctrlC)
	if !handled {
		t.Fatalf("ctrl+c not handled while a purge confirm is open (issue #143)")
	}
	if !pendingQuit(next) {
		t.Fatalf("ctrl+c should open the quit confirm")
	}
	if len(next.pending.target.entries) != 0 || next.pending.label != "" {
		t.Fatalf("abandoned purge pending state not cleared: entries=%d label=%q", len(next.pending.target.entries), next.pending.label)
	}
}

func TestCtrlCClearsAccountCalendarSelectionPendingState(t *testing.T) {
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	m := Model{
		choiceOpen: true,
		pending: pendingAction{
			kind: pendingActionAccountSelectionPromote,
			target: pendingTarget{
				selection:    &accountCalendarSelection{params: account.SelectionParams{SelectedPaths: []string{"/keep/"}}},
				defaultCands: []accountDefaultCandidate{{id: 9, name: "Home"}},
			},
		},
	}

	next, _, handled := m.interceptGlobalKeys(ctrlC)
	if !handled || !pendingQuit(next) {
		t.Fatalf("ctrl+c did not replace account removal with quit: handled=%v quit=%v", handled, pendingQuit(next))
	}
	if next.pending.target.selection != nil || len(next.pending.target.defaultCands) != 0 {
		t.Fatalf("abandoned account selection remains armed: selection=%+v candidates=%+v",
			next.pending.target.selection, next.pending.target.defaultCands)
	}
	if next.choiceOpen || next.pending.kind != pendingActionQuit {
		t.Fatalf("superseded default choice remains open: open=%v kind=%v",
			next.choiceOpen, next.pending.kind)
	}
}

func TestCtrlCClearsAccountRemovalPendingState(t *testing.T) {
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	m := Model{
		confirmOpen: true,
		pending: pendingAction{
			kind:   pendingActionAccountRemove,
			label:  "Personal Google",
			target: pendingTarget{accountID: 7},
		},
	}

	next, _, handled := m.interceptGlobalKeys(ctrlC)
	if !handled || !pendingQuit(next) {
		t.Fatalf("ctrl+c did not replace account removal with quit: handled=%v quit=%v",
			handled, pendingQuit(next))
	}
	if next.pending.target.accountID != 0 || next.pending.label != "" {
		t.Fatalf("abandoned account removal remains armed: id=%d label=%q",
			next.pending.target.accountID, next.pending.label)
	}
}

// TestCtrlCClearsKeepLocalPendingState guards the keep-local variant of the
// ctrl+c supersede. The keep-local confirm arms pendingActionCalendarKeepLocal.
// ctrl+c must drop that ID with the other wait state. A stale ID survives
// the canceled quit confirm. The next confirmed dialog then fires Disconnect
// instead of its own action.
func TestCtrlCClearsKeepLocalPendingState(t *testing.T) {
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

	m := Model{
		confirmOpen: true,
		pending: pendingAction{
			kind:   pendingActionCalendarKeepLocal,
			target: pendingTarget{calendarID: 42},
		},
	}

	next, _, handled := m.interceptGlobalKeys(ctrlC)
	if !handled || !pendingQuit(next) {
		t.Fatalf("ctrl+c did not replace the keep-local confirm with quit: handled=%v quit=%v",
			handled, pendingQuit(next))
	}
	if next.pending.target.calendarID != 0 {
		t.Fatalf("abandoned keep-local ID remains armed: %d", next.pending.target.calendarID)
	}
}

// pendingFieldsOutsideDialog lists Model fields with a "pending" prefix that
// clearPending does not own. Each entry drains through its own lifecycle.
// The `pending` field is the dialog wait state and must be zero after clear.
var pendingFieldsOutsideDialog = map[string]bool{
	"pendingOrder":               true, // calendar order save; the order-saved message drains it
	"pendingAccountOrder":        true, // account order save; the order-saved message drains it
	"pendingAccountOrderIDs":     true, // account order save; the order-saved message drains it
	"pendingOpenEvent":           true, // event view request; the view loader drains it
	"pendingAccountManagementID": true, // account management routing; the manager drains it
	"pendingDiscoveryAccountID":  true, // OAuth discovery target; the flow drains it
	"pendingDiscoveryCreated":    true, // OAuth discovery result; the flow drains it
	"pendingSyncCalendar":        true, // queued sync; syncFinishedMsg drains it
}

// TestClearPendingZerosWholeStruct fills every pendingAction field, calls
// clearPending, and requires the zero value. A new field on pendingAction
// or pendingTarget fails this test until clearPending resets it too.
func TestClearPendingZerosWholeStruct(t *testing.T) {
	m := Model{
		pending: pendingAction{
			kind:  pendingActionEventDelete,
			label: "Standup",
			target: pendingTarget{
				ev:           event.Event{ID: 7, Title: "Standup"},
				save:         EventFormSaveMsg{Title: "Standup"},
				calendarID:   3,
				promoteID:    9,
				promoteCands: []int64{3, 9},
				accountID:    7,
				selection:    &accountCalendarSelection{},
				defaultCands: []accountDefaultCandidate{{id: 9, name: "Home"}},
				entries:      []trash.Entry{{Kind: trash.KindEvent}},
				move:         &calendarMoveState{sourceID: 1},
			},
		},
	}

	m = m.clearPending()

	assertPendingZero(t, m.pending)

	mv := reflect.ValueOf(m)
	mt := mv.Type()
	var stale []string
	for i := 0; i < mt.NumField(); i++ {
		f := mt.Field(i)
		if !strings.HasPrefix(f.Name, "pending") || pendingFieldsOutsideDialog[f.Name] {
			continue
		}
		if !mv.Field(i).IsZero() {
			stale = append(stale, f.Name)
		}
	}
	if len(stale) > 0 {
		t.Fatalf("clearPending left dialog wait state armed: %v", stale)
	}
}

func assertPendingZero(t *testing.T, p pendingAction) {
	t.Helper()
	pv := reflect.ValueOf(p)
	pt := pv.Type()
	var stale []string
	for i := 0; i < pt.NumField(); i++ {
		if !pv.Field(i).IsZero() {
			stale = append(stale, pt.Field(i).Name)
		}
	}
	if len(stale) > 0 {
		t.Fatalf("clearPending left pendingAction fields armed: %v", stale)
	}
}

// TestQuitKeyDeferredToOpenOverlay reproduces issue #406. A press of `q` while a
// read-only/choice overlay owns input must NOT route to the quit confirm. The
// global intercept should leave the keystroke unhandled. The overlay's own
// `q`-to-close binding then runs.
func TestQuitKeyDeferredToOpenOverlay(t *testing.T) {
	qKey := tea.KeyPressMsg{Code: 'q', Text: "q"}

	setters := map[string]func(*Model){
		"viewDialogOpen":      func(m *Model) { m.viewDialogOpen = true },
		"dialogOpen":          func(m *Model) { m.dialogOpen = true },
		"choiceOpen":          func(m *Model) { m.choiceOpen = true },
		"calendarManagerOpen": func(m *Model) { m.calendarManagerOpen = true },
		"trashOpen":           func(m *Model) { m.trashOpen = true },
		"helpDialogOpen":      func(m *Model) { m.helpDialogOpen = true },
	}

	for name, set := range setters {
		t.Run(name, func(t *testing.T) {
			m := Model{keys: defaultAppKeys()}
			set(&m)

			next, _, handled := m.interceptGlobalKeys(qKey)
			if handled {
				t.Fatalf("q was intercepted while %s is open; the overlay should own q (issue #406)", name)
			}
			if pendingQuit(next) || next.confirmOpen {
				t.Fatalf("q opened the quit confirm while %s is open: kind=%v confirmOpen=%v", name, next.pending.kind, next.confirmOpen)
			}
		})
	}
}

// TestQuitKeyStillQuitsFromMainGrid guards the happy path: with no overlay
// open, `q` must still route to the quit confirm.
func TestQuitKeyStillQuitsFromMainGrid(t *testing.T) {
	qKey := tea.KeyPressMsg{Code: 'q', Text: "q"}

	m := Model{keys: defaultAppKeys()}
	next, _, handled := m.interceptGlobalKeys(qKey)
	if !handled {
		t.Fatalf("q not handled from the main grid; expected the quit confirm to open")
	}
	if !pendingQuit(next) || !next.confirmOpen {
		t.Fatalf("q should open the quit confirm from the main grid: kind=%v confirmOpen=%v", next.pending.kind, next.confirmOpen)
	}
}
