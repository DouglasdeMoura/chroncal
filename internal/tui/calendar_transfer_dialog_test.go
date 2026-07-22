package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/icaltransfer"
)

func TestCalendarImportDestinationsRequireWritableCompatibleCalendar(t *testing.T) {
	preview := icaltransfer.Preview{Events: 1, Todos: 1}
	calendars := map[int64]CalendarInfo{
		1: {Name: "Local"},
		2: {Name: "Read only", AccountID: 7, RemoteAccess: "read", RemoteComponents: "VEVENT,VTODO"},
		3: {Name: "Events only", AccountID: 7, RemoteAccess: "write", RemoteComponents: "VEVENT"},
		4: {Name: "Team", AccountID: 7, AccountName: "Work", RemoteAccess: "write", RemoteComponents: "VEVENT,VTODO"},
	}

	got := calendarImportDestinations(calendars, preview)
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 4 {
		t.Fatalf("destinations = %+v, want local and compatible Work calendar", got)
	}
}

func TestCalendarImportPreviewDefaultsToNewLocalCalendar(t *testing.T) {
	preview := icaltransfer.Preview{Events: 2, Todos: 1, Journals: 1, Warnings: []string{"unknown component"}}
	m := NewCalendarImportDialogModel(NewTheme(true)).
		WithPreview("/tmp/Family Schedule.ics", preview, []CalendarImportDestination{{ID: 9, Name: "Work · Google"}})

	form, cmd := m.form.Submit()
	m.form = form
	if cmd == nil {
		t.Fatal("import form did not submit")
	}
	request, ok := cmd().(CalendarImportRequestedMsg)
	if !ok {
		t.Fatalf("submit message = %T", cmd())
	}
	if request.CalendarID != 0 || request.NewName != "Family Schedule" || request.NewColor == "" {
		t.Fatalf("new-calendar request = %+v", request)
	}
	if request.Preview.Events != 2 || request.Preview.Todos != 1 || request.Preview.Journals != 1 {
		t.Fatalf("preview lost from request: %+v", request.Preview)
	}
}

func TestCalendarImportExistingDestinationIgnoresNewCalendarFields(t *testing.T) {
	preview := icaltransfer.Preview{Events: 1}
	m := NewCalendarImportDialogModel(NewTheme(true)).
		WithPreview("/tmp/events.ics", preview, []CalendarImportDestination{{ID: 9, Name: "Work · Google"}})
	m.form.Field(1).(*SelectField).SetSelected(1)
	m.form.Field(2).(*TextField).SetValue("")

	form, cmd := m.form.Submit()
	m.form = form
	if cmd == nil {
		t.Fatal("existing-destination import did not submit")
	}
	request := cmd().(CalendarImportRequestedMsg)
	if request.CalendarID != 9 {
		t.Fatalf("calendar ID = %d, want 9", request.CalendarID)
	}
}

func TestCalendarTransferErrorPreservesPreviewAndDestination(t *testing.T) {
	preview := icaltransfer.Preview{Events: 1}
	m := NewCalendarImportDialogModel(NewTheme(true)).
		WithPreview("/tmp/events.ics", preview, []CalendarImportDestination{{ID: 9, Name: "Work"}})
	m.form.Field(1).(*SelectField).SetSelected(1)
	m = m.WithError(errors.New("database busy"))

	if m.path != "/tmp/events.ics" || m.preview.Events != 1 || m.form.Field(1).(*SelectField).Value() != "9" {
		t.Fatalf("error reset import state: path=%q preview=%+v destination=%q", m.path, m.preview, m.form.Field(1).(*SelectField).Value())
	}
}

func TestCalendarManagerAddChoiceRoutesImport(t *testing.T) {
	m := managerRoutingModel()
	m.calendarManager = NewCalendarManagerModel(m.calendars, m.hiddenCalendars, newThemedHelp(m.theme)).SetSize(120, 40)
	m.calendarManagerOpen = true

	updated, _ := m.Update(CalendarManagerAddRequestedMsg{})
	m = updated.(Model)
	if !m.choiceOpen || m.pendingScopeKind != pendingScopeCalendarManagerAdd {
		t.Fatalf("add choices not open: choice=%v scope=%v", m.choiceOpen, m.pendingScopeKind)
	}
	if m.choiceDialog.choices != 3 {
		t.Fatalf("add menu choices = %d, want local calendar, account, and iCal import", m.choiceDialog.choices)
	}
	updated, _ = m.Update(ChoiceDialogResultMsg{Choice: 2})
	m = updated.(Model)
	if !m.calendarManagerOpen || m.calendarManager.Screen() != CalendarManagerScreenTransfer {
		t.Fatalf("import choice did not open transfer screen: open=%v screen=%v", m.calendarManagerOpen, m.calendarManager.Screen())
	}
}

func TestCalendarManagerSelectsImportedDestinationAfterReload(t *testing.T) {
	m := NewCalendarManagerModel(map[int64]CalendarInfo{1: {Name: "Existing"}}, nil, newThemedHelp(NewTheme(true)))
	m = m.CompleteTransfer(9)
	m = m.SetData(map[int64]CalendarInfo{
		1: {Name: "Existing"},
		9: {Name: "Imported"},
	}, nil)
	if id, ok := m.selectedID(); !ok || id != 9 {
		t.Fatalf("selection after reload = %d, %v; want imported calendar 9", id, ok)
	}
}

func TestCalendarTransferMessagesCarryGeneration(t *testing.T) {
	importDialog := NewCalendarImportDialogModel(NewTheme(true), 41)
	importDialog.form.Field(0).(*TextField).SetValue("/tmp/input.ics")
	form, cmd := importDialog.form.Submit()
	importDialog.form = form
	if cmd == nil {
		t.Fatal("preview form emitted no command")
	}
	previewRequest := cmd().(CalendarImportPreviewRequestedMsg)
	if previewRequest.Generation != 41 {
		t.Fatalf("preview generation = %d, want 41", previewRequest.Generation)
	}

	exportDialog := NewCalendarExportDialogModel(7, "Work", NewTheme(true), 42)
	form, cmd = exportDialog.form.Submit()
	exportDialog.form = form
	if cmd == nil {
		t.Fatal("export form emitted no command")
	}
	exportRequest := cmd().(CalendarExportWriteRequestedMsg)
	if exportRequest.Generation != 42 {
		t.Fatalf("export generation = %d, want 42", exportRequest.Generation)
	}
}

func TestCalendarManagerIgnoresStaleTransferCompletions(t *testing.T) {
	m := managerRoutingModel()
	m.calendarManager = NewCalendarManagerModel(m.calendars, m.hiddenCalendars, newThemedHelp(m.theme)).
		SetSize(120, 40).
		OpenImport(8)
	m.calendarManagerOpen = true
	m.calendarTransferGeneration = 8
	m.syncing = true

	updated, _ := m.Update(calendarImportPreviewReadyMsg{
		Generation: 7,
		Path:       "/tmp/stale.ics",
		Preview:    icaltransfer.Preview{Events: 1},
	})
	m = updated.(Model)
	transfer, ok := m.calendarManager.Transfer()
	if !ok || transfer.phase != calendarTransferPath || transfer.path != "" {
		t.Fatalf("stale preview replaced active transfer: ok=%v phase=%v path=%q", ok, transfer.phase, transfer.path)
	}
	if !m.syncing {
		t.Fatal("stale preview cleared the active transfer spinner")
	}

	updated, _ = m.Update(calendarExportFinishedMsg{Generation: 7, Path: "/tmp/stale.ics"})
	m = updated.(Model)
	if _, ok := m.calendarManager.Transfer(); !ok {
		t.Fatal("stale export completion closed the active transfer")
	}

	updated, _ = m.Update(calendarImportFinishedMsg{Generation: 7, CalendarID: 99})
	m = updated.(Model)
	if _, ok := m.calendarManager.Transfer(); !ok {
		t.Fatal("stale import completion replaced the active transfer")
	}
}

func TestCalendarImportRejectsPreviewWithoutImportableComponents(t *testing.T) {
	m := managerRoutingModel()
	m.calendarManager = NewCalendarManagerModel(m.calendars, m.hiddenCalendars, newThemedHelp(m.theme)).
		SetSize(120, 40).
		OpenImport(3)
	m.calendarManagerOpen = true
	m.calendarTransferGeneration = 3
	m.syncing = true

	updated, _ := m.Update(calendarImportPreviewReadyMsg{
		Generation: 3,
		Path:       "/tmp/freebusy.ics",
		Preview:    icaltransfer.Preview{FreeBusy: 1},
	})
	m = updated.(Model)
	transfer, ok := m.calendarManager.Transfer()
	if !ok {
		t.Fatal("empty preview closed the transfer")
	}
	if transfer.phase != calendarTransferPath {
		t.Fatalf("empty preview advanced to destination phase %v", transfer.phase)
	}
	if !strings.Contains(transfer.errText, "no importable") || !strings.Contains(transfer.errText, "VFREEBUSY") {
		t.Fatalf("empty preview error = %q", transfer.errText)
	}
	if m.syncing {
		t.Fatal("completed empty preview left spinner running")
	}
}

func TestCalendarTransferCancelInvalidatesPendingGeneration(t *testing.T) {
	m := managerRoutingModel()
	m.calendarManager = NewCalendarManagerModel(m.calendars, m.hiddenCalendars, newThemedHelp(m.theme)).
		SetSize(120, 40).
		OpenImport(5)
	m.calendarManagerOpen = true
	m.calendarTransferGeneration = 5
	m.syncing = true

	manager, cmd := m.calendarManager.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m.calendarManager = manager
	if cmd == nil {
		t.Fatal("transfer cancel did not notify the app")
	}
	closed, ok := cmd().(CalendarTransferClosedMsg)
	if !ok {
		t.Fatalf("cancel message = %T, want CalendarTransferClosedMsg", cmd())
	}
	updated, _ := m.Update(closed)
	m = updated.(Model)
	if m.calendarTransferGeneration != 6 || m.syncing {
		t.Fatalf("cancel state: generation=%d syncing=%v", m.calendarTransferGeneration, m.syncing)
	}
	if _, ok := m.calendarManager.Transfer(); ok {
		t.Fatal("cancel left transfer open")
	}
}
