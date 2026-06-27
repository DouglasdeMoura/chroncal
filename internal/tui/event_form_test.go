package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testEventFormCalendars() map[int64]CalendarInfo {
	return map[int64]CalendarInfo{
		1: {Name: "Work", Color: "#ff0000", OwnerEmail: "work@example.com"},
		2: {Name: "Personal", Color: "#00ff00"},
		3: {Name: "same@example.com", Color: "#0000ff", OwnerEmail: "same@example.com"},
	}
}

func TestEventForm_PlacesCalendarAfterNotes(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	repeatItemIdx := -1
	alarmsItemIdx := -1
	titleItemIdx := -1
	titleSeparatorIdx := -1
	repeatSeparatorIdx := -1
	alarmsSeparatorIdx := -1
	peopleItemIdx := -1
	notesItemIdx := -1
	tagsItemIdx := -1
	calendarItemIdx := -1
	transpItemIdx := -1
	classItemIdx := -1
	sepCount := 0
	for i, item := range m.form.items {
		switch item.Label {
		case "Title":
			titleItemIdx = i
		case "Repeat":
			repeatItemIdx = i
		case "Alarms":
			alarmsItemIdx = i
		case "People":
			peopleItemIdx = i
		case "Notes":
			notesItemIdx = i
		case "Tags":
			tagsItemIdx = i
		case "Calendar":
			calendarItemIdx = i
		case "Show as":
			transpItemIdx = i
		case "Visibility":
			classItemIdx = i
		case "":
			if _, ok := item.Field.(*StaticField); ok {
				switch sepCount {
				case 0:
					titleSeparatorIdx = i
				case 1:
					repeatSeparatorIdx = i
				case 2:
					alarmsSeparatorIdx = i
				}
				sepCount++
			}
		}
	}

	notesIdx := -1
	tagsIdx := -1
	calendarIdx := -1
	transpIdx := -1
	classIdx := -1
	peopleIdx := -1
	for i, key := range m.fieldKeys {
		switch key {
		case efKeyDescription:
			notesIdx = i
		case efKeyTags:
			tagsIdx = i
		case efKeyCalendar:
			calendarIdx = i
		case efKeyTransp:
			transpIdx = i
		case efKeyClass:
			classIdx = i
		case efKeyPeople:
			peopleIdx = i
		}
	}

	assert.NotEqual(t, -1, notesIdx)
	assert.NotEqual(t, -1, tagsIdx)
	assert.NotEqual(t, -1, calendarIdx)
	assert.NotEqual(t, -1, transpIdx)
	assert.NotEqual(t, -1, classIdx)
	assert.NotEqual(t, -1, peopleIdx)
	assert.Equal(t, notesIdx+1, tagsIdx)
	assert.Equal(t, tagsIdx+1, calendarIdx)
	assert.Equal(t, calendarIdx+1, transpIdx)
	assert.Equal(t, transpIdx+1, classIdx)
	assert.NotEqual(t, -1, titleItemIdx)
	assert.NotEqual(t, -1, titleSeparatorIdx)
	assert.NotEqual(t, -1, repeatItemIdx)
	assert.NotEqual(t, -1, repeatSeparatorIdx)
	assert.NotEqual(t, -1, alarmsSeparatorIdx)
	assert.NotEqual(t, -1, peopleItemIdx)
	assert.NotEqual(t, -1, notesItemIdx)
	assert.NotEqual(t, -1, tagsItemIdx)
	assert.NotEqual(t, -1, calendarItemIdx)
	assert.NotEqual(t, -1, transpItemIdx)
	assert.NotEqual(t, -1, classItemIdx)
	assert.Equal(t, titleItemIdx+1, titleSeparatorIdx)
	assert.NotEqual(t, -1, alarmsItemIdx)
	assert.Greater(t, alarmsItemIdx, repeatItemIdx)
	assert.Greater(t, alarmsItemIdx, classItemIdx)
	assert.Equal(t, repeatItemIdx+1, repeatSeparatorIdx)
	assert.Equal(t, repeatSeparatorIdx+1, peopleItemIdx)
	assert.Equal(t, notesItemIdx+1, tagsItemIdx)
	assert.Equal(t, tagsItemIdx+1, calendarItemIdx)
	assert.Equal(t, calendarItemIdx+1, transpItemIdx)
	assert.Equal(t, transpItemIdx+1, classItemIdx)
	assert.Equal(t, classItemIdx+1, alarmsSeparatorIdx)
	assert.Equal(t, alarmsSeparatorIdx+1, alarmsItemIdx)
	assert.Equal(t, len(m.form.items)-1, alarmsItemIdx)
}

func TestEventForm_CalendarFieldRendersColorDot(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	if assert.NotNil(t, m.calendarField) {
		m.calendarField.SetSelected(1)
		view := m.calendarField.View()
		assert.Contains(t, view, Glyphs["dot"])
		assert.Contains(t, view, "Work (work@example.com)")
	}
}

func TestEventForm_CalendarFieldOmitsEmailWhenAbsent(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	if assert.NotNil(t, m.calendarField) {
		m.calendarField.SetSelected(0)
		view := m.calendarField.View()
		assert.Contains(t, view, "Personal")
		assert.False(t, strings.Contains(view, "("))
	}
}

func TestEventForm_CalendarFieldShowsOnlyEmailWhenNameMatches(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	if assert.NotNil(t, m.calendarField) {
		m.calendarField.SetSelected(2)
		view := m.calendarField.View()
		assert.Contains(t, view, "same@example.com")
		assert.False(t, strings.Contains(view, "same@example.com ("))
	}
}

func TestEventForm_CalendarFieldFocusBackgroundAppliesOnlyToName(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	if assert.NotNil(t, m.calendarField) {
		m.calendarField.SetSelected(1)
		m.calendarField.Focus()

		view := m.calendarField.View()
		dot := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Render(Glyphs["dot"])
		name := lipgloss.NewStyle().Reverse(true).Render("Work (work@example.com)")

		assert.Contains(t, view, dot+" "+name)
	}
}

func TestEventForm_DefaultsShowBusyAndPublic(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	require.NotNil(t, m.transparencyField)
	require.NotNil(t, m.visibilityField)
	assert.Equal(t, "OPAQUE", m.transparencyField.Value())
	assert.Equal(t, "PUBLIC", m.visibilityField.Value())
}

func TestEventForm_EditHydratesShowAsAndVisibility(t *testing.T) {
	m, _ := NewEventFormModelForEdit(event.Event{
		ID:         7,
		Title:      "Review",
		StartTime:  time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 22, 15, 0, 0, 0, time.UTC),
		CalendarID: 1,
		Transp:     "TRANSPARENT",
		Class:      "PRIVATE",
	}, testEventFormCalendars(), Theme{})

	require.NotNil(t, m.transparencyField)
	require.NotNil(t, m.visibilityField)
	assert.Equal(t, "TRANSPARENT", m.transparencyField.Value())
	assert.Equal(t, "PRIVATE", m.visibilityField.Value())
}

// TestEventForm_EditModeRetainsEditID is a regression test: after building
// the form via NewEventFormModelForEdit, the live form model must carry the
// edited event's ID so the app handler can dispatch an Update. Previously
// the OnSubmit closure captured editID=0 (from the create-mode constructor)
// and sent it through EventFormSaveMsg; the parent then fell into the Create
// branch and created a duplicate event with a fresh UID.
func TestEventForm_EditModeRetainsEditID(t *testing.T) {
	m, _ := NewEventFormModelForEdit(event.Event{
		ID:         42,
		UID:        "original-uid",
		Title:      "Pay the bills!!!",
		StartTime:  time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 22, 15, 0, 0, 0, time.UTC),
		CalendarID: 1,
	}, testEventFormCalendars(), Theme{})

	assert.Equal(t, int64(42), m.editID, "NewEventFormModelForEdit must populate editID on the returned form")

	require.NotNil(t, m.form.onSubmit, "edit form must have an OnSubmit callback")
	cmd := m.form.onSubmit(&m.form)
	require.NotNil(t, cmd)
	// OnSubmit emits a deferred submit marker; the model's Update then runs
	// save() against the live receiver to avoid stale captured state.
	_, ok := cmd().(eventFormSubmitNowMsg)
	require.True(t, ok, "OnSubmit must emit eventFormSubmitNowMsg")
}

func TestEventForm_EditDialogFitsSmallTerminal(t *testing.T) {
	m, _ := NewEventFormModelForEdit(event.Event{
		ID:          42,
		Title:       "Very detailed planning session",
		Description: strings.Repeat("Long notes that make the form tall.\n", 30),
		StartTime:   time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 4, 22, 15, 0, 0, 0, time.UTC),
		CalendarID:  1,
		Categories:  "planning, review",
	}, testEventFormCalendars(), Theme{})
	const termH = 15
	m = m.SetSize(120, termH)

	_, bh := m.BoxSize()
	require.LessOrEqual(t, bh, termH, "rendered edit form must fit inside the terminal")
	require.True(t, m.bodyOverflows(), "test precondition: form body should overflow")

	out := m.View()
	assert.Contains(t, out, "Edit Event")
	assert.Contains(t, out, "Save")
	assert.Contains(t, out, "Cancel")
	assert.Contains(t, out, "more")
}

func TestEventForm_MouseWheelScrollSurvivesRender(t *testing.T) {
	m, _ := NewEventFormModelForEdit(event.Event{
		ID:         42,
		Title:      "Very detailed planning session",
		StartTime:  time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 22, 15, 0, 0, 0, time.UTC),
		CalendarID: 1,
	}, testEventFormCalendars(), Theme{})
	m = m.SetSize(120, 15)
	require.True(t, m.bodyOverflows(), "test precondition: form body should overflow")

	for range 30 {
		var cmd tea.Cmd
		m, cmd = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		require.Nil(t, cmd)
	}
	require.True(t, m.body.AtBottom(), "mouse wheel should scroll the body to the bottom")

	out := m.View()
	assert.Contains(t, out, "Alarms", "rendering must preserve the wheel-scrolled viewport")
	assert.Contains(t, out, "↑ more")
}

func TestEventForm_EndsAfterBlocksEmptyAndZeroCount(t *testing.T) {
	for _, count := range []string{"", "0"} {
		m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})
		m.titleField.SetValue("Standup")
		m.repeatField.SetSelected(1) // a recurring preset
		m.endsField.SetSelected(int(endsAfter))
		m.rebuildDialog() // rebuild items so the count field is present
		m.endsCountField.SetValue(count)

		_, valid := m.form.validate()
		assert.Falsef(t, valid, "Ends=After with count %q must block submit", count)
	}
}

func TestEventForm_SaveIncludesShowAsAndVisibility(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})
	m.titleField.SetValue("Planning")
	m.transparencyField.SetSelected(1)
	m.visibilityField.SetSelected(2)

	cmd := m.save(&m.form)
	require.NotNil(t, cmd)
	msg, ok := cmd().(EventFormSaveMsg)
	require.True(t, ok)
	assert.Equal(t, "TRANSPARENT", msg.Transp)
	assert.Equal(t, "CONFIDENTIAL", msg.Class)
}

func TestEventForm_EditNoOpPreservesTimedStartAcrossUTCDateBoundary(t *testing.T) {
	// Event stored 2026-04-16T02:00:00Z in America/New_York. Its UTC date
	// (Apr 16) differs from its display-tz wall-clock date (Apr 15, 22:00 NY).
	// A no-op edit (just save) must not shift the event by a day.
	start := time.Date(2026, 4, 16, 2, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 16, 3, 0, 0, 0, time.UTC)
	m, _ := NewEventFormModelForEdit(event.Event{
		ID:         7,
		UID:        "boundary-uid",
		Title:      "Late meeting",
		StartTime:  start,
		EndTime:    end,
		Timezone:   "America/New_York",
		CalendarID: 1,
	}, testEventFormCalendars(), Theme{})

	cmd := m.save(&m.form)
	require.NotNil(t, cmd)
	msg, ok := cmd().(EventFormSaveMsg)
	require.True(t, ok)
	assert.True(t, msg.StartTime.Equal(start),
		"no-op edit must preserve StartTime; got %s want %s",
		msg.StartTime.Format(time.RFC3339), start.Format(time.RFC3339))
	assert.True(t, msg.EndTime.Equal(end),
		"no-op edit must preserve EndTime; got %s want %s",
		msg.EndTime.Format(time.RFC3339), end.Format(time.RFC3339))
}

func TestEventForm_EnterOnTimeDoesNotOpenDatePicker(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	timeIdx := -1
	for i, item := range m.form.items {
		if item.Label == "Time" {
			timeIdx = i
			break
		}
	}
	require.NotEqual(t, -1, timeIdx)
	m.form.focused = timeIdx

	cmd := m.tryOpenOverlay()
	assert.Nil(t, cmd)
	assert.False(t, m.datePickerOpen)
	assert.False(t, m.timezonePickerOpen)
}

func TestEventForm_EnterOnDateOpensDatePicker(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	dateIdx := -1
	for i, item := range m.form.items {
		if item.Label == "Date" {
			dateIdx = i
			break
		}
	}
	require.NotEqual(t, -1, dateIdx)
	m.form.focused = dateIdx

	cmd := m.tryOpenOverlay()
	require.NotNil(t, cmd)
	assert.True(t, m.datePickerOpen)
	assert.False(t, m.timezonePickerOpen)
}

// fieldIndexByLabel returns the form item index for the field with the given
// inline label, or -1 if none matches.
func fieldIndexByLabel(m EventFormModel, label string) int {
	for i, item := range m.form.items {
		if item.Label == label {
			return i
		}
	}
	return -1
}

// mouseZoneByName returns the recorded clickable zone with the given name.
func mouseZoneByName(mt *mouseTracker, name string) (mouseZone, bool) {
	for _, z := range mt.zones {
		if z.name == name {
			return z, true
		}
	}
	return mouseZone{}, false
}

// clickFormField renders the form, locates the clickable zone for the form
// item at idx, and dispatches a left mouse click at its center through the
// model's Update — exercising the same coordinate-resolution path as a real
// mouse click.
func clickFormField(t *testing.T, m EventFormModel, idx int) EventFormModel {
	t.Helper()
	// Focus the target field and scroll it into the body viewport so its
	// clickable zone is rendered.
	m.form.focused = idx
	m.syncBodyViewport(true)
	_ = m.View() // populates defaultMouseTracker.zones via mouseSweep

	zone, ok := mouseZoneByName(defaultMouseTracker, fieldTarget(idx))
	require.True(t, ok, "form field %d must register a clickable zone", idx)

	bw, bh := m.BoxSize()
	ox := (m.width - bw) / 2
	oy := (m.height - bh) / 2
	clickX := ox + (zone.startX+zone.endX)/2
	clickY := oy + zone.startY

	m, _ = m.Update(tea.MouseClickMsg(tea.Mouse{X: clickX, Y: clickY, Button: tea.MouseLeft}))
	return m
}

// TestEventForm_MouseClickOpensOverlays is a regression test for issue #470:
// the mouse-click branch only special-cased the Date field, so clicking the
// Timezone or Alarms rows merely focused them without opening their overlays.
// All overlay-opener fields must behave identically under mouse and Enter.
func TestEventForm_MouseClickOpensOverlays(t *testing.T) {
	t.Run("timezone", func(t *testing.T) {
		m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})
		m = m.SetSize(120, 40)
		idx := fieldIndexByLabel(m, "Timezone")
		require.NotEqual(t, -1, idx)

		m = clickFormField(t, m, idx)
		assert.True(t, m.timezonePickerOpen, "clicking Timezone should open the timezone picker")
	})

	t.Run("alarms", func(t *testing.T) {
		m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})
		m = m.SetSize(120, 40)
		idx := fieldIndexByLabel(m, "Alarms")
		require.NotEqual(t, -1, idx)

		m = clickFormField(t, m, idx)
		assert.True(t, m.alarmEditorOpen, "clicking Alarms should open the alarm editor")
	})
}

func TestEventForm_EnterOnAllDayDoesNotOpenTimezonePicker(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	allDayIdx := -1
	for i, item := range m.form.items {
		if item.Label == "All day" {
			allDayIdx = i
			break
		}
	}
	require.NotEqual(t, -1, allDayIdx)
	m.form.focused = allDayIdx

	cmd := m.tryOpenOverlay()
	assert.Nil(t, cmd)
	assert.False(t, m.datePickerOpen)
	assert.False(t, m.timezonePickerOpen)
}

func TestEventForm_EnterOnAllDayDisablesTimeImmediately(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	allDayIdx := -1
	for i, item := range m.form.items {
		if item.Label == "All day" {
			allDayIdx = i
			break
		}
	}
	require.NotEqual(t, -1, allDayIdx)
	require.True(t, m.timeField.IsFocusable())

	m.form.focused = allDayIdx
	m, _ = m.Update(keyPressMsg("enter"))

	assert.True(t, m.allDayField.Checked())
	assert.False(t, m.timeField.IsFocusable())
}

// TestEventForm_EditPreservesAttendeeMetadata guards against issue #109:
// editing an event must not flatten attendee Role/RSVPStatus/CUType/CN back to
// defaults. A CHAIR who has ACCEPTED must remain a CHAIR who has ACCEPTED after
// an unrelated title edit, and unchanged attendees must keep their display name.
func TestEventForm_EditPreservesAttendeeMetadata(t *testing.T) {
	m, _ := NewEventFormModelForEdit(event.Event{
		ID:         7,
		CalendarID: 1,
		Title:      "Standup",
		Attendees: []model.Attendee{
			{
				Email:      "chair@example.com",
				Name:       "Chair Person",
				Role:       "CHAIR",
				RSVPStatus: "ACCEPTED",
				CUType:     "INDIVIDUAL",
			},
			{
				Email:      "room@example.com",
				Name:       "Big Room",
				Role:       "OPT-PARTICIPANT",
				RSVPStatus: "TENTATIVE",
				CUType:     "ROOM",
			},
		},
	}, testEventFormCalendars(), Theme{})

	m.titleField.SetValue("Standup (renamed)")

	cmd := m.save(&m.form)
	require.NotNil(t, cmd)
	msg, ok := cmd().(EventFormSaveMsg)
	require.True(t, ok)

	require.Len(t, msg.Attendees, 2)
	byEmail := map[string]model.Attendee{}
	for _, a := range msg.Attendees {
		byEmail[a.Email] = a
	}

	chair := byEmail["chair@example.com"]
	assert.Equal(t, "CHAIR", chair.Role)
	assert.Equal(t, "ACCEPTED", chair.RSVPStatus)
	assert.Equal(t, "Chair Person", chair.Name)

	room := byEmail["room@example.com"]
	assert.Equal(t, "OPT-PARTICIPANT", room.Role)
	assert.Equal(t, "TENTATIVE", room.RSVPStatus)
	assert.Equal(t, "ROOM", room.CUType)
	assert.Equal(t, "Big Room", room.Name)
}

// TestEventForm_EditTimedMultiDayMidnightEndPreservesEnd guards against issue
// #208: editing a timed multi-day event whose end instant is exactly midnight
// (e.g. Apr 1 09:00 -> Apr 3 00:00, occupying Apr 1 and Apr 2) and saving with
// no changes must not shift the end back a full day. multiDayEndDate stores the
// last *included* day (Apr 2); the save path must re-add the exclusive midnight
// day so the saved end is still Apr 3 00:00, not Apr 2 00:00.
func TestEventForm_EditTimedMultiDayMidnightEndPreservesEnd(t *testing.T) {
	origLocal := time.Local
	time.Local = time.UTC
	defer func() { time.Local = origLocal }()

	start := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)

	m, _ := NewEventFormModelForEdit(event.Event{
		ID:         11,
		CalendarID: 1,
		Title:      "Conference",
		StartTime:  start,
		EndTime:    end,
		Timezone:   "UTC",
	}, testEventFormCalendars(), Theme{})

	cmd := m.save(&m.form)
	require.NotNil(t, cmd)
	msg, ok := cmd().(EventFormSaveMsg)
	require.True(t, ok)

	assert.Equal(t, start, msg.StartTime)
	assert.Truef(t, end.Equal(msg.EndTime),
		"no-op edit shifted end: want %s, got %s", end, msg.EndTime)
}

// TestEventForm_RepeatChangeAddsEndsFieldToLiveForm guards issue #496: changing
// Repeat from None to a preset on the LIVE form (via Update, as a real key press
// would) must rebuild the rendered form so the inline "Ends" selector appears.
// The old OnRebuild closure ran syncFromForm against a stale value-captured copy,
// so the live form never grew and form-created recurrences were endless.
func TestEventForm_RepeatChangeAddsEndsFieldToLiveForm(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 7, 5, 0, 0, 0, 0, time.Local),
		map[int64]CalendarInfo{1: {}}, NewTheme(true))

	repeatIdx := -1
	for i, item := range m.form.items {
		if item.Label == "Repeat" {
			repeatIdx = i
			break
		}
	}
	if repeatIdx < 0 {
		t.Fatal("Repeat field not found in form")
	}
	m.form.focused = repeatIdx

	before := len(m.form.items)

	// Drive a real "→" key press through the live model, advancing Repeat
	// from None to the first preset.
	m, _ = m.Update(keyMsg("right"))

	if m.repeatField.Selected() == 0 {
		t.Fatal("precondition failed: Repeat selection did not advance off None")
	}

	after := len(m.form.items)
	if after <= before {
		t.Errorf("live form item count did not grow after Repeat change: before=%d after=%d (Ends field never reached live form)", before, after)
	}

	hasEnds := false
	for _, item := range m.form.items {
		if item.Label == "Ends" {
			hasEnds = true
			break
		}
	}
	if !hasEnds {
		t.Error("live form is missing the 'Ends' field after enabling recurrence")
	}
}

// TestEventFormForEdit_MultiDayEndUsesDisplayTimezone guards issue #499: the
// pre-filled multi-day end date must be computed in the event's display
// timezone (the loc used to anchor m.day), not machine-local. Otherwise an
// event whose tz != machine-local with an end near midnight gets an off-by-one
// end day, so a no-op edit shifts the event end forward a day on save.
func TestEventFormForEdit_MultiDayEndUsesDisplayTimezone(t *testing.T) {
	// Force machine-local to UTC so the scenario is deterministic regardless
	// of where the test runs.
	origLocal := time.Local
	time.Local = time.UTC
	defer func() { time.Local = origLocal }()

	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	// In New York the event spans two calendar days (Jun 13 20:00 → Jun 14
	// 01:00). In UTC both instants fall on Jun 14, so a machine-local (UTC)
	// computation would wrongly see a single day.
	ev := event.Event{
		ID:         7,
		CalendarID: 1,
		Title:      "Overnight",
		Timezone:   "America/New_York",
		StartTime:  time.Date(2026, 6, 13, 20, 0, 0, 0, loc),
		EndTime:    time.Date(2026, 6, 14, 1, 0, 0, 0, loc),
	}

	m, _ := NewEventFormModelForEdit(ev, map[int64]CalendarInfo{1: {}}, NewTheme(true))

	if !m.rangeHasEnd {
		t.Fatal("expected multi-day range to be detected in the display timezone")
	}
	want := time.Date(2026, 6, 14, 0, 0, 0, 0, loc)
	if m.rangeEndDate.Year() != want.Year() ||
		m.rangeEndDate.Month() != want.Month() ||
		m.rangeEndDate.Day() != want.Day() {
		t.Errorf("rangeEndDate = %v, want display-tz day %v", m.rangeEndDate, want)
	}
}
