package tui

import (
	"strings"
	"testing"
)

func TestFooter_MonthContextShowsExpectedKeys(t *testing.T) {
	f := NewFooterModel(NewTheme(true))
	out := f.Render(FooterMonthWeekDay, 120, "", "", false)

	want := []string{"MONTH", "move", "open", "new", "today", "help"}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("Render = %q, missing %q", out, w)
		}
	}
}

func TestFooter_AgendaEmptyShowsCreate(t *testing.T) {
	f := NewFooterModel(NewTheme(true))
	out := f.Render(FooterAgendaEmpty, 120, "", "", false)
	if !strings.Contains(out, "create event") {
		t.Errorf("Render = %q, missing 'create event'", out)
	}
}

func TestFooter_EventPopupRSVPConditional(t *testing.T) {
	f := NewFooterModel(NewTheme(true))
	withRSVP := f.Render(FooterEventPopup, 120, "", "", true)
	withoutRSVP := f.Render(FooterEventPopup, 120, "", "", false)
	if !strings.Contains(withRSVP, "RSVP") {
		t.Errorf("with RSVP = %q, missing 'RSVP'", withRSVP)
	}
	if strings.Contains(withoutRSVP, "RSVP") {
		t.Errorf("without RSVP = %q, should not contain 'RSVP'", withoutRSVP)
	}
}

func TestFooter_ToastOverridesRightSide(t *testing.T) {
	f := NewFooterModel(NewTheme(true))
	out := f.Render(FooterMonthWeekDay, 120, "", "TOAST_HERE", false)
	if !strings.Contains(out, "TOAST_HERE") {
		t.Errorf("Render = %q, missing toast override", out)
	}
	if strings.Contains(out, "move") {
		t.Errorf("Render = %q, hint list should be hidden when toast is active", out)
	}
}

func TestFooter_CollapsesBelow40Cols(t *testing.T) {
	f := NewFooterModel(NewTheme(true))
	out := f.Render(FooterEventPopup, 30, "", "", false)
	if !strings.Contains(out, "?") {
		t.Errorf("Render (narrow) = %q, missing ? help escape hatch", out)
	}
	if !strings.Contains(out, "x") {
		t.Errorf("Render (narrow) = %q, missing collapsed top hint 'x'", out)
	}
	// Should NOT include the full hint list under 40 cols.
	if strings.Contains(out, "prev/next") {
		t.Errorf("Render (narrow) = %q, should not include full hints", out)
	}
}

func TestFooter_EllipsisBetween40And60(t *testing.T) {
	// Use the calendar-popup context (longest hint list, ~52 chars) at a
	// width inside the ellipsis band to force truncation even after the
	// label has been dropped.
	f := NewFooterModel(NewTheme(true))
	out := f.Render(FooterCalendarPopup, 42, "syncing", "", false)
	if !strings.HasSuffix(out, "…") {
		t.Errorf("Render (mid width) = %q, expected ellipsis suffix", out)
	}
}

func TestFooter_ZeroWidth(t *testing.T) {
	f := NewFooterModel(NewTheme(true))
	if out := f.Render(FooterMonthWeekDay, 0, "", "", false); out != "" {
		t.Errorf("Render (w=0) = %q, want empty", out)
	}
}

func TestFooter_LabelDroppedInEllipsisBand(t *testing.T) {
	// Between 40 and 60 cols the hint list truncates with an ellipsis.
	// The label is lower-priority than actionable hints and must disappear
	// first so commands survive longer. Test with CALENDARS (the longest
	// label) to catch both short and long names.
	f := NewFooterModel(NewTheme(true))
	out := f.Render(FooterCalendarPopup, 55, "", "", false)
	if strings.Contains(out, "CALENDARS") {
		t.Errorf("Render (mid width) = %q, label should be dropped", out)
	}
}

func TestFooter_LabelPresentAtFullWidth(t *testing.T) {
	f := NewFooterModel(NewTheme(true))
	out := f.Render(FooterCalendarPopup, 120, "", "", false)
	if !strings.Contains(out, "CALENDARS") {
		t.Errorf("Render (full width) = %q, label should be visible", out)
	}
}

func TestFooter_RenderMinimal_OnlyStatusAndHelp(t *testing.T) {
	f := NewFooterModel(NewTheme(true))
	out := f.RenderMinimal(120, "syncing", "", true)
	if !strings.Contains(out, "syncing") {
		t.Errorf("RenderMinimal = %q, missing sync status", out)
	}
	if !strings.Contains(out, "help") {
		t.Errorf("RenderMinimal = %q, missing help escape hatch", out)
	}
	// Must not show contextual hints.
	if strings.Contains(out, "move") || strings.Contains(out, "edit") || strings.Contains(out, "delete") {
		t.Errorf("RenderMinimal = %q, should not show contextual hints", out)
	}
}

func TestFooter_RenderMinimal_ToastTakesPrecedence(t *testing.T) {
	f := NewFooterModel(NewTheme(true))
	out := f.RenderMinimal(120, "", "TOAST", true)
	if !strings.Contains(out, "TOAST") {
		t.Errorf("RenderMinimal = %q, toast should win", out)
	}
	if strings.Contains(out, "? help") {
		t.Errorf("RenderMinimal = %q, ? help should hide when toast is showing", out)
	}
}

func TestFooter_RenderMinimal_HelpDialogHidesHelpHint(t *testing.T) {
	f := NewFooterModel(NewTheme(true))
	out := f.RenderMinimal(120, "", "", false)
	if strings.Contains(out, "help") {
		t.Errorf("RenderMinimal (help dialog open) = %q, ? help is misleading while help is up", out)
	}
}
