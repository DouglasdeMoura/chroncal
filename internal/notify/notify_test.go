package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/tcal/internal/alarm"
	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
)

func TestFormatNotification_Basic(t *testing.T) {
	da := alarm.DueAlarm{
		Event: event.Event{
			Title:     "Team Standup",
			StartTime: time.Date(2026, 3, 27, 9, 30, 0, 0, time.Local),
		},
		Alarm: model.Alarm{
			Action:      "DISPLAY",
			Description: "Reminder",
		},
		TriggerAt: time.Date(2026, 3, 27, 9, 15, 0, 0, time.Local),
	}

	title, body := FormatNotification(da)

	if title != "Team Standup" {
		t.Errorf("title = %q, want %q", title, "Team Standup")
	}

	if !strings.Contains(body, "Fri Mar 27, 09:30") {
		t.Errorf("body = %q, want it to contain formatted time", body)
	}

	if strings.Contains(body, "Reminder") {
		t.Errorf("body = %q, should not contain 'Reminder' description", body)
	}
}

func TestFormatNotification_WithLocation(t *testing.T) {
	da := alarm.DueAlarm{
		Event: event.Event{
			Title:     "Lunch Meeting",
			Location:  "Conference Room B",
			StartTime: time.Date(2026, 3, 27, 12, 0, 0, 0, time.Local),
		},
		Alarm: model.Alarm{
			Action:      "DISPLAY",
			Description: "Reminder",
		},
		TriggerAt: time.Date(2026, 3, 27, 11, 45, 0, 0, time.Local),
	}

	title, body := FormatNotification(da)

	if title != "Lunch Meeting" {
		t.Errorf("title = %q, want %q", title, "Lunch Meeting")
	}

	if !strings.Contains(body, "Conference Room B") {
		t.Errorf("body = %q, want it to contain location", body)
	}

	if !strings.Contains(body, "Fri Mar 27, 12:00") {
		t.Errorf("body = %q, want it to contain formatted time", body)
	}
}

func TestFormatNotification_WithDescription(t *testing.T) {
	da := alarm.DueAlarm{
		Event: event.Event{
			Title:     "Doctor Appointment",
			StartTime: time.Date(2026, 3, 27, 14, 0, 0, 0, time.Local),
		},
		Alarm: model.Alarm{
			Action:      "DISPLAY",
			Description: "Bring insurance card",
		},
		TriggerAt: time.Date(2026, 3, 27, 13, 45, 0, 0, time.Local),
	}

	_, body := FormatNotification(da)

	if !strings.Contains(body, "Bring insurance card") {
		t.Errorf("body = %q, want it to contain description", body)
	}
}

func TestFormatNotification_NoLocation(t *testing.T) {
	da := alarm.DueAlarm{
		Event: event.Event{
			Title:     "Quick Call",
			StartTime: time.Date(2026, 3, 27, 10, 0, 0, 0, time.Local),
		},
		Alarm: model.Alarm{
			Action: "DISPLAY",
		},
		TriggerAt: time.Date(2026, 3, 27, 9, 55, 0, 0, time.Local),
	}

	_, body := FormatNotification(da)

	// Body should just be the time, with no trailing separator
	want := "Fri Mar 27, 10:00"
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}
