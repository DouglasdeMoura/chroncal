package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
)

func footerTestManager() CalendarManagerModel {
	cals := map[int64]CalendarInfo{
		1: {Name: "Personal", Color: "#ff0000"},
		2: {Name: "Work", Color: "#00ff00", AccountID: 7, AccountName: "iCloud"},
	}
	return NewCalendarManagerModel(cals, nil, newThemedHelp(activeTheme))
}

// footerStates returns the manager in every state whose footer bindings
// differ, so width sweeps and style checks cover each renderHelp branch.
func footerStates(m CalendarManagerModel) map[string]CalendarManagerModel {
	return map[string]CalendarManagerModel{
		"root/list":      m,
		"root/add":       m.setRootFocus(rootFocusAdd),
		"root/inspector": m.setRootFocus(rootFocusInspector),
		"root/add-menu":  m.openAddMenu(),
		"calendar":       m.OpenCalendar(CalendarDialogParams{ID: 1, Name: "Personal", ManagerEmbedded: true}),
		"account":        m.OpenAccount(AccountSettingsParams{AccountID: 7, DisplayName: "iCloud"}),
		"picker":         m.OpenAccountCalendars(account.Discovery{}),
		"transfer":       m.OpenImport(),
	}
}

// The manager footer must render through the shared themed help model: keys
// and descriptions joined by the themed " · " separator, matching every other
// dialog's footer, in every screen and root-focus state.
func TestCalendarManagerFooterUsesThemedHelp(t *testing.T) {
	for name, state := range footerStates(footerTestManager().SetSize(110, 32)) {
		boxW, _ := state.boxSize()
		innerW := max(boxW-5, 10)
		plain := stripANSI(state.renderHelp(innerW))
		if !strings.Contains(plain, " · ") {
			t.Errorf("%s: footer %q lacks the themed help separator", name, plain)
		}
		if !strings.Contains(plain, "esc") {
			t.Errorf("%s: footer %q lost the esc hint", name, plain)
		}
	}
}

// The footer must stay a single line no wider than the dialog interior at
// every size the manager can be given; an overflowing help line wraps and
// shears the dialog frame (bubbles' short-help keeps an overflowing item when
// the ellipsis lands exactly on the width boundary).
func TestCalendarManagerFooterNeverWraps(t *testing.T) {
	base := footerTestManager()
	for w := 40; w <= 140; w += 5 {
		for h := 14; h <= 40; h += 13 {
			for name, state := range footerStates(base.SetSize(w, h)) {
				boxW, _ := state.boxSize()
				innerW := max(boxW-5, 10)
				view := state.renderHelp(innerW)
				if strings.Contains(view, "\n") {
					t.Fatalf("%s at %dx%d: footer wrapped", name, w, h)
				}
				if got := lipgloss.Width(view); got > innerW {
					t.Fatalf("%s at %dx%d: footer width %d exceeds interior %d", name, w, h, got, innerW)
				}
			}
		}
	}
}

// footerBindingsEqual compares two footer binding slices by the rendered help
// hint (key label + description) — the only attributes the footer displays —
// so the contract tests assert the user-visible contract, not binding identity.
func footerBindingsEqual(a, b []key.Binding) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ah, bh := a[i].Help(), b[i].Help()
		if ah.Key != bh.Key || ah.Desc != bh.Desc {
			return false
		}
	}
	return true
}

// For #547 the pushed-screen footer is owned by the active child: the manager
// defers to each child's HelpBindings instead of a hardcoded copy that drifts
// when a key is rebound. Every pushed screen must defer to its own child.
func TestManagerFooterDefersToActiveChildHelp(t *testing.T) {
	base := footerTestManager()

	cal := base.OpenCalendar(CalendarDialogParams{ID: 1, Name: "Personal", ManagerEmbedded: true})
	if got := cal.helpBindings(); !footerBindingsEqual(got, cal.calendarForm.HelpBindings()) {
		t.Errorf("calendar footer does not defer to calendarForm.HelpBindings(): %#v", got)
	}

	acc := base.OpenAccount(AccountSettingsParams{AccountID: 7, DisplayName: "iCloud"})
	if got := acc.helpBindings(); !footerBindingsEqual(got, acc.accountSettings.HelpBindings()) {
		t.Errorf("account footer does not defer to accountSettings.HelpBindings(): %#v", got)
	}

	picker := base.OpenAccountCalendars(pickerDiscovery())
	if got := picker.helpBindings(); !footerBindingsEqual(got, picker.accountPicker.HelpBindings()) {
		t.Errorf("account-calendars footer does not defer to accountPicker.HelpBindings(): %#v", got)
	}

	transfer := base.OpenImport()
	if got := transfer.helpBindings(); !footerBindingsEqual(got, transfer.transfer.HelpBindings()) {
		t.Errorf("transfer footer does not defer to transfer.HelpBindings(): %#v", got)
	}
}

// A calendar detail with an embedded discovery picker advertises the picker's
// compact help (via the calendar child), so connecting an account never shows
// the form's field hints.
func TestCalendarDetailHelpDelegatesToEmbeddedPicker(t *testing.T) {
	m := footerTestManager().OpenCalendar(CalendarDialogParams{ID: 1, Name: "Personal", ManagerEmbedded: true})
	m = m.ShowDiscovery(pickerDiscovery())
	if m.calendarForm == nil || m.calendarForm.discoveryPicker == nil {
		t.Fatalf("embedded discovery picker not mounted: form=%v", m.calendarForm != nil)
	}
	want := m.calendarForm.discoveryPicker.HelpBindings()
	if got := m.helpBindings(); !footerBindingsEqual(got, want) {
		t.Errorf("calendar+picker footer does not defer to discoveryPicker.HelpBindings(): %#v", got)
	}
}

// The child-owned footer must keep the exact hint text the manager showed
// before #547, so centralizing the dispatch is a no-op for users.
func TestManagerFooterHintsPreserved(t *testing.T) {
	wide := footerTestManager().SetSize(140, 40)
	cases := []struct {
		name  string
		state CalendarManagerModel
		hints []string
	}{
		{"calendar", wide.OpenCalendar(CalendarDialogParams{ID: 1, Name: "Personal", ManagerEmbedded: true}), []string{"next field", "confirm", "back"}},
		{"account", wide.OpenAccount(AccountSettingsParams{AccountID: 7, DisplayName: "iCloud"}), []string{"select", "open", "back"}},
		{"picker", wide.OpenAccountCalendars(pickerDiscovery()), []string{"toggle", "switch", "confirm", "back"}},
		{"transfer", wide.OpenImport(), []string{"next field", "confirm", "back"}},
		{"calendar+picker", wide.OpenCalendar(CalendarDialogParams{ID: 1, Name: "Personal", ManagerEmbedded: true}).ShowDiscovery(pickerDiscovery()), []string{"toggle", "switch", "confirm", "back"}},
	}
	for _, c := range cases {
		boxW, _ := c.state.boxSize()
		innerW := max(boxW-5, 10)
		plain := stripANSI(c.state.renderHelp(innerW))
		for _, h := range c.hints {
			if !strings.Contains(plain, h) {
				t.Errorf("%s: footer %q lost hint %q", c.name, plain, h)
			}
		}
	}
}

// activeChild is the single screen→child mapping shared by the footer and the
// inspector. For every pushed screen both must read the same child, and at the
// root list it must be nil so the selection inspector renders. This locks the
// two dispatches in lockstep so a newly added screen cannot drift between them.
func TestActiveChildDrivesFooterAndInspector(t *testing.T) {
	wide := footerTestManager().SetSize(140, 40)

	if wide.activeChild() != nil {
		t.Errorf("root list has an active child %T, want nil", wide.activeChild())
	}

	pushed := []struct {
		name     string
		state    CalendarManagerModel
		wantType string
	}{
		{"calendar", wide.OpenCalendar(CalendarDialogParams{ID: 1, Name: "Personal", ManagerEmbedded: true}), "*tui.CalendarDialogModel"},
		{"account", wide.OpenAccount(AccountSettingsParams{AccountID: 7, DisplayName: "iCloud"}), "*tui.AccountSettingsDialogModel"},
		{"picker", wide.OpenAccountCalendars(pickerDiscovery()), "*tui.AccountCalendarPickerModel"},
		{"transfer", wide.OpenImport(), "*tui.CalendarTransferDialogModel"},
	}
	for _, c := range pushed {
		child := c.state.activeChild()
		if child == nil {
			t.Errorf("%s: activeChild is nil for a pushed screen", c.name)
			continue
		}
		// The single dispatch maps each screen to its owning child.
		if got := fmt.Sprintf("%T", child); got != c.wantType {
			t.Errorf("%s: activeChild type %s, want %s", c.name, got, c.wantType)
		}
		// The footer defers to that same child (deterministic: bindings, not render).
		if got := c.state.helpBindings(); !footerBindingsEqual(got, child.HelpBindings()) {
			t.Errorf("%s: footer does not defer to activeChild().HelpBindings()", c.name)
		}
		// The inspector is rendered by that same child, not the root fallback.
		// Compare with ANSI stripped: the child's view carries a blinking
		// text-cursor color phase that varies between render calls, but the
		// visible text is identical — so stripping isolates the dispatch
		// contract (activeInspectorLines routed to activeChild, not
		// selectionInspectorLines) without flaking on cursor phase.
		w, h := c.state.inspectorPaneSize()
		got := stripANSI(strings.Join(c.state.activeInspectorLines(w, h), "\n"))
		want := stripANSI(child.InspectorView(w, h))
		if got != want {
			t.Errorf("%s: inspector does not defer to activeChild().InspectorView()", c.name)
		}
	}
}

// Direct screen-switch smoke: opening and closing each pushed screen flips the
// active child and footer, and the embedded discovery picker hands the footer
// off and back without leaving the calendar screen.
func TestManagerScreenSwitchSmoke(t *testing.T) {
	m := footerTestManager().SetSize(140, 40)
	if m.activeChild() != nil {
		t.Fatalf("initial active child %T, want nil", m.activeChild())
	}

	// Push the calendar detail: footer switches to the form's field hints.
	m = m.OpenCalendar(CalendarDialogParams{ID: 1, Name: "Personal", ManagerEmbedded: true})
	if m.Screen() != CalendarManagerScreenCalendar || m.activeChild() == nil {
		t.Fatalf("OpenCalendar: screen=%v child=%v", m.Screen(), m.activeChild())
	}
	if got := stripANSI(m.renderHelp(120)); !strings.Contains(got, "next field") || !strings.Contains(got, "back") {
		t.Errorf("calendar footer %q lost field/back hints", got)
	}

	// Embed the discovery picker: footer switches to the picker's toggle hint.
	m = m.ShowDiscovery(pickerDiscovery())
	if got := stripANSI(m.renderHelp(120)); !strings.Contains(got, "toggle") {
		t.Errorf("embedded-picker footer %q lost toggle hint", got)
	}

	// Drop the picker: footer returns to the form hints.
	m = m.HideDiscovery()
	if got := stripANSI(m.renderHelp(120)); !strings.Contains(got, "next field") {
		t.Errorf("post-picker footer %q lost field hint", got)
	}

	// Push transfer (export) on top of the retained calendar detail.
	m = m.OpenExport(1, "Personal")
	if m.Screen() != CalendarManagerScreenTransfer || m.transfer == nil {
		t.Fatalf("OpenExport: screen=%v transfer=%v", m.Screen(), m.transfer != nil)
	}
	if got := stripANSI(m.renderHelp(120)); !strings.Contains(got, "next field") {
		t.Errorf("transfer footer %q lost field hint", got)
	}

	// Close transfer: returns to the calendar detail underneath.
	m = m.CloseTransfer()
	if m.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("CloseTransfer: screen=%v, want calendar", m.Screen())
	}
}
