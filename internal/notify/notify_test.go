package notify

import (
	"os"
	"path/filepath"
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

func TestFormatNotification_ZeroStartTime(t *testing.T) {
	da := alarm.DueAlarm{
		Event: event.Event{
			Title:    "Buy Groceries",
			Location: "Supermarket",
		},
		Alarm: model.Alarm{
			Action:      "DISPLAY",
			Description: "Don't forget milk",
		},
		TriggerAt: time.Date(2026, 3, 27, 9, 0, 0, 0, time.Local),
	}

	title, body := FormatNotification(da)

	if title != "Buy Groceries" {
		t.Errorf("title = %q, want %q", title, "Buy Groceries")
	}
	if strings.Contains(body, "Jan  1") || strings.Contains(body, "0001") {
		t.Errorf("body = %q, should not contain zero-time formatting", body)
	}
	if !strings.Contains(body, "Supermarket") {
		t.Errorf("body = %q, want it to contain location", body)
	}
	if !strings.Contains(body, "Don't forget milk") {
		t.Errorf("body = %q, want it to contain description", body)
	}
}

func TestFormatNotification_ZeroStartTimeNoLocation(t *testing.T) {
	da := alarm.DueAlarm{
		Event: event.Event{
			Title: "Bare Todo",
		},
		Alarm: model.Alarm{
			Action: "DISPLAY",
		},
		TriggerAt: time.Date(2026, 3, 27, 9, 0, 0, 0, time.Local),
	}

	_, body := FormatNotification(da)

	// Bare todo with no start/location/desc falls back to trigger time.
	want := "Fri Mar 27, 09:00"
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestFormatNotification_ZeroStartTimeNoLocationZeroTrigger(t *testing.T) {
	da := alarm.DueAlarm{
		Event: event.Event{
			Title: "Truly Bare Todo",
		},
		Alarm: model.Alarm{
			Action: "DISPLAY",
		},
	}

	_, body := FormatNotification(da)

	if body != "" {
		t.Errorf("body = %q, want empty string when both start and trigger are zero", body)
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

func TestResolveLocalAudioPath(t *testing.T) {
	// Create a temp file to simulate a local audio file.
	tmp := filepath.Join(t.TempDir(), "alert.oga")
	if err := os.WriteFile(tmp, []byte("fake audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		uri  string
		want bool // true = expect non-empty path
	}{
		{"empty", "", false},
		{"absolute path exists", tmp, true},
		{"absolute path missing", "/nonexistent/sound.wav", false},
		{"file:// URI exists", "file://" + tmp, true},
		{"file:// URI missing", "file:///nonexistent/sound.wav", false},
		{"http URI", "http://example.com/sound.wav", false},
		{"https URI", "https://example.com/sound.wav", false},
		{"data URI", "data:audio/wav;base64,AAAA", false},
		{"relative path", "sounds/alert.wav", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveLocalAudioPath(tt.uri)
			if tt.want && got == "" {
				t.Errorf("resolveLocalAudioPath(%q) = empty, want non-empty", tt.uri)
			}
			if !tt.want && got != "" {
				t.Errorf("resolveLocalAudioPath(%q) = %q, want empty", tt.uri, got)
			}
		})
	}
}
