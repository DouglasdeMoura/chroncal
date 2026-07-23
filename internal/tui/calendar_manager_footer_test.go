package tui

import (
	"strings"
	"testing"

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
