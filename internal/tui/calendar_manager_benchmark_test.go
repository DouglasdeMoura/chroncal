package tui

import (
	"testing"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
)

var calendarManagerBenchmarkSink CalendarManagerModel

func benchmarkCalendarManager() CalendarManagerModel {
	calendars := map[int64]CalendarInfo{
		1: {Name: "Personal", Color: "#a6e3a1", IsDefault: true},
		2: {
			Name: "Work", Color: "#89b4fa", AccountID: 7,
			AccountName: "Google", RemoteAccess: "write",
		},
	}
	return NewCalendarManagerModel(calendars, nil, help.New()).SetSize(120, 40)
}

func BenchmarkCalendarManagerOpenLocalEditor(b *testing.B) {
	base := benchmarkCalendarManager()
	params := calendarDialogParamsFor(1, base.calendars[1], false)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = base.OpenCalendar(params)
	}
}

func BenchmarkCalendarManagerOpenAccountConnection(b *testing.B) {
	base := benchmarkCalendarManager()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = base.OpenAccountConnection()
	}
}

func BenchmarkCalendarManagerViewLocalEditor(b *testing.B) {
	m := benchmarkCalendarManager()
	m = m.OpenCalendar(calendarDialogParamsFor(1, m.calendars[1], false))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = m.View()
	}
}

func BenchmarkCalendarManagerViewAccountConnection(b *testing.B) {
	m := benchmarkCalendarManager().OpenAccountConnection()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = m.View()
	}
}

func BenchmarkCalendarManagerTypeInLocalEditor(b *testing.B) {
	base := benchmarkCalendarManager()
	base = base.OpenCalendar(calendarDialogParamsFor(1, base.calendars[1], false))
	press := tea.KeyPressMsg{Code: 'x', Text: "x"}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		m := base
		m, _ = m.Update(press)
		calendarManagerBenchmarkSink = m
	}
}

func BenchmarkCalendarManagerTypeInAccountConnection(b *testing.B) {
	base := benchmarkCalendarManager().OpenAccountConnection()
	press := tea.KeyPressMsg{Code: 'x', Text: "x"}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		m := base
		m, _ = m.Update(press)
		calendarManagerBenchmarkSink = m
	}
}

func BenchmarkCalendarManagerResizeAccountConnection(b *testing.B) {
	base := benchmarkCalendarManager().OpenAccountConnection()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = base.SetSize(121, 41)
	}
}

func BenchmarkCalendarManagerTabLocalEditor(b *testing.B) {
	base := benchmarkCalendarManager()
	base = base.OpenCalendar(calendarDialogParamsFor(1, base.calendars[1], false))
	press := tea.KeyPressMsg{Code: tea.KeyTab}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		m := base
		m, _ = m.Update(press)
		calendarManagerBenchmarkSink = m
	}
}

func BenchmarkCalendarManagerTabAccountConnection(b *testing.B) {
	base := benchmarkCalendarManager().OpenAccountConnection()
	press := tea.KeyPressMsg{Code: tea.KeyTab}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		m := base
		m, _ = m.Update(press)
		calendarManagerBenchmarkSink = m
	}
}

func BenchmarkCalendarManagerResizeLocalEditor(b *testing.B) {
	base := benchmarkCalendarManager()
	base = base.OpenCalendar(calendarDialogParamsFor(1, base.calendars[1], false))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = base.SetSize(121, 41)
	}
}

func BenchmarkCalendarManagerViewRoot(b *testing.B) {
	m := benchmarkCalendarManager()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = m.View()
	}
}
