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
	m.pendingQuit = false
	m.pendingDelete = event.Event{ID: 7, Title: "Standup"}

	next, _, handled := m.interceptGlobalKeys(ctrlC)
	if !handled {
		t.Fatalf("ctrl+c not handled while a non-quit confirm is open (issue #143)")
	}
	if !next.confirmOpen || !next.pendingQuit {
		t.Fatalf("ctrl+c should replace the open confirm with the quit confirm: confirmOpen=%v pendingQuit=%v", next.confirmOpen, next.pendingQuit)
	}
	if next.pendingDelete.ID != 0 {
		t.Fatalf("abandoned destructive pending state not cleared: pendingDelete.ID=%d", next.pendingDelete.ID)
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
	m.pendingQuit = false
	m.pendingPurgeEntries = []trash.Entry{{Kind: trash.KindEvent}}
	m.pendingPurgeTitle = "1 item"

	next, _, handled := m.interceptGlobalKeys(ctrlC)
	if !handled {
		t.Fatalf("ctrl+c not handled while a purge confirm is open (issue #143)")
	}
	if !next.pendingQuit {
		t.Fatalf("ctrl+c should open the quit confirm")
	}
	if len(next.pendingPurgeEntries) != 0 || next.pendingPurgeTitle != "" {
		t.Fatalf("abandoned purge pending state not cleared: entries=%d title=%q", len(next.pendingPurgeEntries), next.pendingPurgeTitle)
	}
}

func TestCtrlCClearsAccountCalendarSelectionPendingState(t *testing.T) {
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	m := Model{
		choiceOpen:       true,
		pendingScopeKind: pendingScopeAccountSelectionPromote,
		pendingAccountSelection: &accountCalendarSelection{
			params: account.SelectionParams{SelectedPaths: []string{"/keep/"}},
		},
		pendingAccountDefaultCandidates: []accountDefaultCandidate{{id: 9, name: "Home"}},
	}

	next, _, handled := m.interceptGlobalKeys(ctrlC)
	if !handled || !next.pendingQuit {
		t.Fatalf("ctrl+c did not replace account removal with quit: handled=%v quit=%v", handled, next.pendingQuit)
	}
	if next.pendingAccountSelection != nil || len(next.pendingAccountDefaultCandidates) != 0 {
		t.Fatalf("abandoned account selection remains armed: selection=%+v candidates=%+v",
			next.pendingAccountSelection, next.pendingAccountDefaultCandidates)
	}
	if next.choiceOpen || next.pendingScopeKind != pendingScopeNone {
		t.Fatalf("superseded default choice remains open: open=%v kind=%v",
			next.choiceOpen, next.pendingScopeKind)
	}
}

func TestCtrlCClearsAccountRemovalPendingState(t *testing.T) {
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	m := Model{
		confirmOpen:              true,
		pendingAccountRemoveID:   7,
		pendingAccountRemoveName: "Personal Google",
	}

	next, _, handled := m.interceptGlobalKeys(ctrlC)
	if !handled || !next.pendingQuit {
		t.Fatalf("ctrl+c did not replace account removal with quit: handled=%v quit=%v",
			handled, next.pendingQuit)
	}
	if next.pendingAccountRemoveID != 0 || next.pendingAccountRemoveName != "" {
		t.Fatalf("abandoned account removal remains armed: id=%d name=%q",
			next.pendingAccountRemoveID, next.pendingAccountRemoveName)
	}
}

// TestCtrlCClearsKeepLocalPendingState guards the keep-local variant of the
// ctrl+c supersede. The keep-local confirm arms pendingCalendarKeepLocal.
// ctrl+c must drop that ID with the other wait state. A stale ID survives
// the canceled quit confirm. The next confirmed dialog then fires Disconnect
// instead of its own action (handleConfirmDialogResult consumes the field
// before the delete fallback).
func TestCtrlCClearsKeepLocalPendingState(t *testing.T) {
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

	m := Model{
		confirmOpen:              true,
		pendingCalendarKeepLocal: 42,
	}

	next, _, handled := m.interceptGlobalKeys(ctrlC)
	if !handled || !next.pendingQuit {
		t.Fatalf("ctrl+c did not replace the keep-local confirm with quit: handled=%v quit=%v",
			handled, next.pendingQuit)
	}
	if next.pendingCalendarKeepLocal != 0 {
		t.Fatalf("abandoned keep-local ID remains armed: %d", next.pendingCalendarKeepLocal)
	}
}

// pendingFieldsOutsideConfirmClear lists Model fields with a "pending"
// prefix that clearConfirmPending does not own. Each entry drains through
// its own lifecycle. Every other "pending" field holds wait state for a
// confirm or choice dialog, so clearConfirmPending must reset it.
var pendingFieldsOutsideConfirmClear = map[string]bool{
	"pendingOrder":               true, // calendar order save; the order-saved message drains it
	"pendingAccountOrder":        true, // account order save; the order-saved message drains it
	"pendingAccountOrderIDs":     true, // account order save; the order-saved message drains it
	"pendingOpenEvent":           true, // event view request; the view loader drains it
	"pendingEditSave":            true, // recurring edit save; the form drains it
	"pendingAccountManagementID": true, // account management routing; the manager drains it
	"pendingDiscoveryAccountID":  true, // OAuth discovery target; the flow drains it
	"pendingDiscoveryCreated":    true, // OAuth discovery result; the flow drains it
	"pendingSyncCalendar":        true, // queued sync; syncFinishedMsg drains it
	"pendingQuit":                true, // quit confirm marker; the quit result drains it
	"pendingCalendarMove":        true, // move choice state; the choice dialog drains it
	"pendingScopeKind":           true, // choice scope; the reset writes pendingScopeNone, not zero
}

// TestClearConfirmPendingResetsAllConfirmWaitFields arms every owned
// "pending" field, then requires the zero value after the call. Extend the
// arm literal below when you add a confirm wait field. The reflective check
// then fails until clearConfirmPending resets the new field too.
func TestClearConfirmPendingResetsAllConfirmWaitFields(t *testing.T) {
	m := Model{
		pendingDelete:                   event.Event{ID: 7, Title: "Standup"},
		pendingPurgeEntries:             []trash.Entry{{Kind: trash.KindEvent}},
		pendingPurgeTitle:               "1 item",
		pendingCalendarDelete:           3,
		pendingCalendarDeleteName:       "Work",
		pendingCalendarKeepLocal:        42,
		pendingCalendarPromote:          9,
		pendingCalendarPromoteName:      "Home",
		pendingCalendarPromoteCands:     []int64{3, 9},
		pendingAccountSelection:         &accountCalendarSelection{},
		pendingAccountDefaultCandidates: []accountDefaultCandidate{{id: 9, name: "Home"}},
		pendingAccountRemoveID:          7,
		pendingAccountRemoveName:        "Personal Google",
	}

	m = m.clearConfirmPending()

	mv := reflect.ValueOf(m)
	mt := mv.Type()
	var stale []string
	for i := 0; i < mt.NumField(); i++ {
		f := mt.Field(i)
		if !strings.HasPrefix(f.Name, "pending") || pendingFieldsOutsideConfirmClear[f.Name] {
			continue
		}
		if !mv.Field(i).IsZero() {
			stale = append(stale, f.Name)
		}
	}
	if len(stale) > 0 {
		t.Fatalf("clearConfirmPending left confirm wait state armed: %v", stale)
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
			if next.pendingQuit || next.confirmOpen {
				t.Fatalf("q opened the quit confirm while %s is open: pendingQuit=%v confirmOpen=%v", name, next.pendingQuit, next.confirmOpen)
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
	if !next.pendingQuit || !next.confirmOpen {
		t.Fatalf("q should open the quit confirm from the main grid: pendingQuit=%v confirmOpen=%v", next.pendingQuit, next.confirmOpen)
	}
}
