package tui

import (
	"testing"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func TestAppReadOnlyCalendarBlocksEventMutations(t *testing.T) {
	newReadOnlyModel := func() Model {
		m := NewModel(nil, "")
		m.calendars = map[int64]CalendarInfo{
			7: {Name: "Holidays", RemoteAccess: "read"},
		}
		return m
	}

	t.Run("edit", func(t *testing.T) {
		m := newReadOnlyModel()
		updated, cmd := m.Update(EventEditMsg{Event: event.Event{CalendarID: 7, Title: "Holiday"}})
		m = updated.(Model)
		if cmd == nil || m.formOpen || m.toast.state != ToastFailed {
			t.Fatalf("read-only edit: formOpen=%v toast=%v cmd=%v", m.formOpen, m.toast.state, cmd)
		}
	})

	t.Run("delete", func(t *testing.T) {
		m := newReadOnlyModel()
		updated, cmd := m.Update(EventDeleteMsg{Event: event.Event{CalendarID: 7, Title: "Holiday"}})
		m = updated.(Model)
		if cmd == nil || m.confirmOpen || m.choiceOpen || m.toast.state != ToastFailed {
			t.Fatalf("read-only delete: confirm=%v choice=%v toast=%v cmd=%v", m.confirmOpen, m.choiceOpen, m.toast.state, cmd)
		}
	})

	t.Run("save", func(t *testing.T) {
		m := newReadOnlyModel()
		m.formOpen = true
		updated, cmd := m.Update(EventFormSaveMsg{CalendarID: 7, Title: "Holiday"})
		m = updated.(Model)
		if cmd == nil || !m.formOpen || m.toast.state != ToastFailed {
			t.Fatalf("read-only save: formOpen=%v toast=%v cmd=%v", m.formOpen, m.toast.state, cmd)
		}
	})
}
