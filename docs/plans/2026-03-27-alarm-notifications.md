# Alarm Notifications Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make VALARM actions (DISPLAY, AUDIO, EMAIL) actually fire notifications when events are due, with a daemon/timer architecture for scheduling.

**Architecture:** A stateless `tcal alarm check` command computes which alarms are due by comparing `event.start_time + alarm.trigger_value` against the current time, fires platform-appropriate notifications, and records state to prevent duplicates. An optional `tcal alarm daemon` wraps `check` in a loop. OS-native scheduling (systemd timer, launchd) is the primary integration path. A new `alarm_state` table tracks fired/acked/snoozed alarms per RFC 9074.

**Tech Stack:** `gen2brain/beeep` (cross-platform notifications), `net/smtp` (EMAIL action), platform audio commands via `os/exec` (AUDIO action), SQLite for state, Cobra for CLI.

---

## Task 1: RFC 5545 Duration Parser (extract from ical package)

The `addDuration` function in `internal/ical/import.go:556-623` parses RFC 5545 duration strings like `-PT15M` and adds them to a `time.Time`. We need this logic in a shared location so the alarm checker can compute trigger times without importing the ical package.

**Files:**
- Create: `internal/duration/duration.go`
- Create: `internal/duration/duration_test.go`
- Modify: `internal/ical/import.go:556-623` (replace with call to shared package)

**Step 1: Write the failing tests**

```go
// internal/duration/duration_test.go
package duration

import (
	"testing"
	"time"
)

func TestAdd(t *testing.T) {
	base := time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		dur  string
		want time.Time
	}{
		{"15 min before", "-PT15M", time.Date(2026, 4, 1, 13, 45, 0, 0, time.UTC)},
		{"1 hour before", "-PT1H", time.Date(2026, 4, 1, 13, 0, 0, 0, time.UTC)},
		{"1 day before", "-P1D", time.Date(2026, 3, 31, 14, 0, 0, 0, time.UTC)},
		{"1 week before", "-P1W", time.Date(2026, 3, 25, 14, 0, 0, 0, time.UTC)},
		{"30 min after", "PT30M", time.Date(2026, 4, 1, 14, 30, 0, 0, time.UTC)},
		{"1 day 2 hours", "P1DT2H", time.Date(2026, 4, 2, 16, 0, 0, 0, time.UTC)},
		{"positive prefix", "+PT10M", time.Date(2026, 4, 1, 14, 10, 0, 0, time.UTC)},
		{"hours minutes seconds", "-PT1H30M45S", time.Date(2026, 4, 1, 12, 29, 15, 0, time.UTC)},
		{"empty defaults to 1h", "", time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Add(base, tt.dur)
			if !got.Equal(tt.want) {
				t.Errorf("Add(%v, %q) = %v, want %v", base, tt.dur, got, tt.want)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/duration/ -v`
Expected: FAIL (package does not exist)

**Step 3: Write minimal implementation**

Extract the `addDuration` function from `internal/ical/import.go:556-623` into a new package:

```go
// internal/duration/duration.go
package duration

import (
	"strconv"
	"strings"
	"time"
)

// Add parses an RFC 5545 duration string and adds it to a time.
// Format: [+/-]P[nW] or [+/-]P[nD][T[nH][nM][nS]]
// An empty string defaults to +1 hour.
func Add(t time.Time, dur string) time.Time {
	if dur == "" {
		return t.Add(time.Hour)
	}

	s := dur
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	} else if s[0] == '+' {
		s = s[1:]
	}

	if len(s) == 0 || s[0] != 'P' {
		return t.Add(time.Hour)
	}
	s = s[1:]

	var d time.Duration
	var days int

	if i := strings.Index(s, "W"); i >= 0 {
		if w, err := strconv.Atoi(s[:i]); err == nil {
			days = w * 7
		}
		if neg {
			return t.AddDate(0, 0, -days)
		}
		return t.AddDate(0, 0, days)
	}

	if i := strings.Index(s, "D"); i >= 0 {
		if v, err := strconv.Atoi(s[:i]); err == nil {
			days = v
		}
		s = s[i+1:]
	}

	if len(s) > 0 && s[0] == 'T' {
		s = s[1:]
		if i := strings.Index(s, "H"); i >= 0 {
			if v, err := strconv.Atoi(s[:i]); err == nil {
				d += time.Duration(v) * time.Hour
			}
			s = s[i+1:]
		}
		if i := strings.Index(s, "M"); i >= 0 {
			if v, err := strconv.Atoi(s[:i]); err == nil {
				d += time.Duration(v) * time.Minute
			}
			s = s[i+1:]
		}
		if i := strings.Index(s, "S"); i >= 0 {
			if v, err := strconv.Atoi(s[:i]); err == nil {
				d += time.Duration(v) * time.Second
			}
		}
	}

	if neg {
		return t.AddDate(0, 0, -days).Add(-d)
	}
	return t.AddDate(0, 0, days).Add(d)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/duration/ -v`
Expected: PASS (all 9 cases)

**Step 5: Update ical import to use shared package**

In `internal/ical/import.go`, replace the `addDuration` function body:

```go
import "github.com/douglasdemoura/tcal/internal/duration"

// Replace the existing addDuration function:
func addDuration(t time.Time, dur string) time.Time {
	return duration.Add(t, dur)
}
```

**Step 6: Run all tests to verify no regression**

Run: `go test ./... -v`
Expected: All PASS

**Step 7: Commit**

```bash
git add internal/duration/ internal/ical/import.go
git commit -m "refactor: extract RFC 5545 duration parser into shared package"
```

---

## Task 2: Alarm State Database Migration + Queries

Add an `alarm_state` table to track which alarms have fired, been dismissed, or snoozed. This follows RFC 9074 (VALARM Extensions) semantics.

**Files:**
- Create: `db/migrations/009_alarm_state.sql`
- Create: `db/queries/alarm_state.sql`
- Regenerate: `internal/storage/*.sql.go` (via `sqlc generate`)

**Step 1: Write the migration**

```sql
-- db/migrations/009_alarm_state.sql

-- +goose Up
CREATE TABLE alarm_state (
    id         INTEGER PRIMARY KEY,
    alarm_id   INTEGER NOT NULL REFERENCES event_alarms(id) ON DELETE CASCADE,
    event_id   INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    trigger_at TEXT NOT NULL,
    fired_at   TEXT,
    acked_at   TEXT,
    snoozed_to TEXT
);
CREATE UNIQUE INDEX idx_alarm_state_unique ON alarm_state(alarm_id, trigger_at);
CREATE INDEX idx_alarm_state_event_id ON alarm_state(event_id);

-- +goose Down
DROP INDEX IF EXISTS idx_alarm_state_event_id;
DROP INDEX IF EXISTS idx_alarm_state_unique;
DROP TABLE IF EXISTS alarm_state;
```

**Step 2: Write the query file**

```sql
-- db/queries/alarm_state.sql

-- name: GetAlarmState :one
SELECT * FROM alarm_state WHERE alarm_id = ? AND trigger_at = ?;

-- name: CreateAlarmState :one
INSERT INTO alarm_state (alarm_id, event_id, trigger_at, fired_at)
VALUES (?, ?, ?, ?) RETURNING *;

-- name: AcknowledgeAlarmState :exec
UPDATE alarm_state SET acked_at = ? WHERE id = ?;

-- name: SnoozeAlarmState :exec
UPDATE alarm_state SET snoozed_to = ? WHERE id = ?;

-- name: ListPendingAlarmStates :many
SELECT * FROM alarm_state WHERE acked_at IS NULL AND fired_at IS NOT NULL ORDER BY trigger_at;

-- name: ListAlarmStatesByEventID :many
SELECT * FROM alarm_state WHERE event_id = ? ORDER BY trigger_at;

-- name: DeleteAlarmStatesByEventID :exec
DELETE FROM alarm_state WHERE event_id = ?;
```

**Step 3: Regenerate sqlc**

Run: `sqlc generate`
Expected: Generates `internal/storage/alarm_state.sql.go` with type-safe methods

**Step 4: Verify build**

Run: `go build ./...`
Expected: Clean build

**Step 5: Verify migration runs**

Run: `go test ./internal/storage/ -v`
Expected: PASS (existing tests use in-memory DB which runs all migrations)

**Step 6: Commit**

```bash
git add db/migrations/009_alarm_state.sql db/queries/alarm_state.sql internal/storage/
git commit -m "feat: add alarm_state table for tracking fired/acked/snoozed alarms"
```

---

## Task 3: Alarm Checker Service

The core engine: query events with alarms, compute trigger times, determine which are due, return them. No notification logic here -- just the pure computation.

**Files:**
- Create: `internal/alarm/service.go`
- Create: `internal/alarm/service_test.go`

**Step 1: Write the failing tests**

```go
// internal/alarm/service_test.go
package alarm

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
	"github.com/douglasdemoura/tcal/internal/testutil"
)

func newTestServices(t *testing.T) (*Service, *event.Service) {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	evtSvc := event.NewService(db, q)
	alarmSvc := NewService(db, q, evtSvc)
	return alarmSvc, evtSvc
}

func TestCheck_FiresDueAlarm(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Create event starting in 10 minutes with a 15-min-before alarm
	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Meeting",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "15 min reminder"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Check: alarm trigger is 15 min before start = 5 min ago = should fire
	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("got %d due alarms, want 1", len(due))
	}
	if due[0].Event.Title != "Meeting" {
		t.Errorf("event title = %q, want %q", due[0].Event.Title, "Meeting")
	}
	if due[0].Alarm.Action != "DISPLAY" {
		t.Errorf("alarm action = %q, want %q", due[0].Alarm.Action, "DISPLAY")
	}
}

func TestCheck_SkipsAlreadyFired(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Meeting",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// First check fires
	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("first check: got %d, want 1", len(due))
	}

	// Mark as fired
	err = svc.MarkFired(ctx, due[0])
	if err != nil {
		t.Fatal(err)
	}

	// Second check should skip it
	due, err = svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("second check: got %d, want 0", len(due))
	}
}

func TestCheck_SkipsFutureAlarm(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event in 2 hours with 15-min alarm = trigger is 1h45m from now
	start := time.Now().Add(2 * time.Hour)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Later Meeting",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	})
	if err != nil {
		t.Fatal(err)
	}

	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("got %d due alarms, want 0", len(due))
	}
}

func TestCheck_SkipsStaleAlarm(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event was 2 days ago -- alarm is stale beyond the 24h threshold
	start := time.Now().Add(-48 * time.Hour)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Old Meeting",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	})
	if err != nil {
		t.Fatal(err)
	}

	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("got %d due alarms, want 0 (stale)", len(due))
	}
}

func TestCheck_RelatedEnd(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event ends in 10 minutes, alarm is 15 min before END
	start := time.Now().Add(-50 * time.Minute)
	end := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Ending Soon",
		StartTime:  start,
		EndTime:    end,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M", Related: "END"},
	})
	if err != nil {
		t.Fatal(err)
	}

	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("got %d due alarms, want 1", len(due))
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/alarm/ -v`
Expected: FAIL (package does not exist)

**Step 3: Write the implementation**

```go
// internal/alarm/service.go
package alarm

import (
	"context"
	"database/sql"
	"time"

	"github.com/douglasdemoura/tcal/internal/duration"
	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
	"github.com/douglasdemoura/tcal/internal/storage"
)

// StaleThreshold is the maximum age of an unfired alarm before it is skipped.
const StaleThreshold = 24 * time.Hour

// DueAlarm represents an alarm that should fire now.
type DueAlarm struct {
	Event     event.Event
	Alarm     model.Alarm
	TriggerAt time.Time
}

type Service struct {
	db     *sql.DB
	q      *storage.Queries
	events *event.Service
}

func NewService(db *sql.DB, q *storage.Queries, events *event.Service) *Service {
	return &Service{db: db, q: q, events: events}
}

// Check finds all alarms that are due at the given time.
// An alarm is due when:
//   - trigger_at <= now (the alarm time has passed)
//   - trigger_at > now - StaleThreshold (not too old)
//   - no alarm_state row exists with fired_at set for this alarm+trigger
func (s *Service) Check(ctx context.Context, now time.Time) ([]DueAlarm, error) {
	// Query events with alarms in a generous window around now.
	// We look from (now - StaleThreshold) to (now + StaleThreshold) for start times,
	// then filter precisely by computed trigger time.
	windowStart := now.Add(-StaleThreshold - 24*time.Hour)
	windowEnd := now.Add(StaleThreshold + 24*time.Hour)

	events, err := s.events.ListByDateRange(ctx, windowStart, windowEnd)
	if err != nil {
		return nil, err
	}

	var due []DueAlarm
	for _, evt := range events {
		alarms, err := s.events.ListAlarms(ctx, evt.ID)
		if err != nil {
			continue
		}
		for _, a := range alarms {
			triggerAt := computeTriggerTime(evt, a)

			// Must be in the past (due) but not stale
			if triggerAt.After(now) {
				continue
			}
			if now.Sub(triggerAt) > StaleThreshold {
				continue
			}

			// Check if already fired
			triggerKey := triggerAt.UTC().Format(time.RFC3339)
			_, err := s.q.GetAlarmState(ctx, storage.GetAlarmStateParams{
				AlarmID:   a.ID,
				TriggerAt: triggerKey,
			})
			if err == nil {
				// Already has a state row -- skip
				continue
			}

			due = append(due, DueAlarm{
				Event:     evt,
				Alarm:     a,
				TriggerAt: triggerAt,
			})
		}
	}
	return due, nil
}

// MarkFired records that an alarm has been fired.
func (s *Service) MarkFired(ctx context.Context, da DueAlarm) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.q.CreateAlarmState(ctx, storage.CreateAlarmStateParams{
		AlarmID:   da.Alarm.ID,
		EventID:   da.Event.ID,
		TriggerAt: da.TriggerAt.UTC().Format(time.RFC3339),
		FiredAt:   sql.NullString{String: now, Valid: true},
	})
	return err
}

// Dismiss acknowledges a fired alarm so it won't show as pending.
func (s *Service) Dismiss(ctx context.Context, stateID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.q.AcknowledgeAlarmState(ctx, storage.AcknowledgeAlarmStateParams{
		AckedAt: sql.NullString{String: now, Valid: true},
		ID:      stateID,
	})
}

// Snooze reschedules a fired alarm to fire again after the given duration.
func (s *Service) Snooze(ctx context.Context, stateID int64, until time.Time) error {
	return s.q.SnoozeAlarmState(ctx, storage.SnoozeAlarmStateParams{
		SnoozedTo: sql.NullString{String: until.UTC().Format(time.RFC3339), Valid: true},
		ID:        stateID,
	})
}

// ListPending returns all fired-but-not-acknowledged alarms.
func (s *Service) ListPending(ctx context.Context) ([]storage.AlarmState, error) {
	return s.q.ListPendingAlarmStates(ctx)
}

func computeTriggerTime(evt event.Event, a model.Alarm) time.Time {
	anchor := evt.StartTime
	if a.Related == "END" {
		anchor = evt.EndTime
	}
	return duration.Add(anchor, a.TriggerValue)
}
```

Note: The `CreateAlarmStateParams` struct will be generated by sqlc. The `fired_at`, `acked_at`, and `snoozed_to` columns are nullable TEXT, so sqlc may generate them as `sql.NullString`. Adjust the field names to match what sqlc generates -- check `internal/storage/alarm_state.sql.go` after running `sqlc generate` in Task 2.

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/alarm/ -v`
Expected: All 5 tests PASS

**Step 5: Commit**

```bash
git add internal/alarm/
git commit -m "feat: add alarm checker service with due/stale/fired logic"
```

---

## Task 4: DISPLAY Notification Backend

Send desktop notifications using `gen2brain/beeep`. This is the most common alarm action.

**Files:**
- Create: `internal/notify/notify.go`
- Create: `internal/notify/notify_test.go`

**Step 1: Add dependency**

Run: `go get github.com/gen2brain/beeep`

**Step 2: Write the tests**

Testing actual notifications is impractical in CI, so we test the interface and formatting logic, not the desktop call itself.

```go
// internal/notify/notify_test.go
package notify

import (
	"testing"
	"time"

	"github.com/douglasdemoura/tcal/internal/alarm"
	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
)

func TestFormatNotification(t *testing.T) {
	da := alarm.DueAlarm{
		Event: event.Event{
			Title:     "Team Standup",
			Location:  "Room 3A",
			StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.Local),
		},
		Alarm: model.Alarm{
			Action:      "DISPLAY",
			Description: "Meeting reminder",
		},
	}

	title, body := FormatNotification(da)

	if title != "Team Standup" {
		t.Errorf("title = %q, want %q", title, "Team Standup")
	}
	// Body should contain the time and location
	if body == "" {
		t.Error("body is empty")
	}
}
```

**Step 3: Write the implementation**

```go
// internal/notify/notify.go
package notify

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/gen2brain/beeep"

	"github.com/douglasdemoura/tcal/internal/alarm"
)

// FormatNotification returns a title and body for a due alarm.
func FormatNotification(da alarm.DueAlarm) (title, body string) {
	title = da.Event.Title

	var parts []string
	parts = append(parts, da.Event.StartTime.Local().Format("Mon Jan 2, 15:04"))
	if da.Event.Location != "" {
		parts = append(parts, da.Event.Location)
	}
	if da.Alarm.Description != "" && da.Alarm.Description != "Reminder" {
		parts = append(parts, da.Alarm.Description)
	}
	body = strings.Join(parts, " - ")
	return
}

// Display sends a desktop notification.
func Display(da alarm.DueAlarm) error {
	title, body := FormatNotification(da)
	return beeep.Notify(title, body, "")
}

// Audio sends a desktop notification and plays a sound.
func Audio(da alarm.DueAlarm) error {
	// Send visual notification too
	if err := Display(da); err != nil {
		return err
	}
	return playSystemSound()
}

// playSystemSound plays a default alert sound using platform commands.
func playSystemSound() error {
	switch runtime.GOOS {
	case "linux":
		// Try paplay (PulseAudio/PipeWire), then aplay (ALSA)
		for _, cmd := range []string{"paplay", "aplay"} {
			if path, err := exec.LookPath(cmd); err == nil {
				// Use freedesktop sound theme
				sound := "/usr/share/sounds/freedesktop/stereo/alarm-clock-elapsed.oga"
				return exec.Command(path, sound).Run()
			}
		}
		// Fallback to beeep's beep
		return beeep.Beep(beeep.DefaultFreq, beeep.DefaultDuration)
	case "darwin":
		return exec.Command("afplay", "/System/Library/Sounds/Glass.aiff").Run()
	case "windows":
		return beeep.Beep(beeep.DefaultFreq, beeep.DefaultDuration)
	default:
		return beeep.Beep(beeep.DefaultFreq, beeep.DefaultDuration)
	}
}

// Email sends an email alarm. Requires SMTP configuration.
func Email(da alarm.DueAlarm, smtpCfg SMTPConfig) error {
	if smtpCfg.Host == "" {
		return fmt.Errorf("SMTP not configured: set [smtp] in config.toml")
	}
	title, body := FormatNotification(da)
	return sendMail(smtpCfg, title, body, da.Alarm.Attendees)
}

// SMTPConfig holds email sending configuration.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}
```

**Step 4: Write the email sender**

```go
// Add to internal/notify/notify.go (or a separate email.go if preferred)

import (
	"fmt"
	"net/smtp"

	"github.com/douglasdemoura/tcal/internal/model"
)

func sendMail(cfg SMTPConfig, subject, body string, attendees []model.AlarmAttendee) error {
	if len(attendees) == 0 {
		return fmt.Errorf("EMAIL alarm has no attendees")
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)

	for _, att := range attendees {
		msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
			cfg.From, att.Email, subject, body)
		if err := smtp.SendMail(addr, auth, cfg.From, []string{att.Email}, []byte(msg)); err != nil {
			return fmt.Errorf("send to %s: %w", att.Email, err)
		}
	}
	return nil
}
```

**Step 5: Run tests**

Run: `go test ./internal/notify/ -v`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/notify/ go.mod go.sum
git commit -m "feat: add notification backends (DISPLAY, AUDIO, EMAIL)"
```

---

## Task 5: SMTP Config Support

Extend the config system to support SMTP settings for EMAIL alarms.

**Files:**
- Modify: `internal/config/config.go:11-13`
- Modify: `internal/config/config_test.go` (if exists, else create)

**Step 1: Write the failing test**

```go
// internal/config/config_test.go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_SMTPFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[smtp]
host = "smtp.example.com"
port = 587
username = "user"
password = "pass"
from = "cal@example.com"
`), 0o644)

	cfg := LoadFile(path)
	if cfg.SMTP.Host != "smtp.example.com" {
		t.Errorf("SMTP.Host = %q, want %q", cfg.SMTP.Host, "smtp.example.com")
	}
	if cfg.SMTP.Port != 587 {
		t.Errorf("SMTP.Port = %d, want %d", cfg.SMTP.Port, 587)
	}
	if cfg.SMTP.From != "cal@example.com" {
		t.Errorf("SMTP.From = %q, want %q", cfg.SMTP.From, "cal@example.com")
	}
}

func TestLoad_SMTPFromEnv(t *testing.T) {
	t.Setenv("TCAL_SMTP_HOST", "env.example.com")
	t.Setenv("TCAL_SMTP_PORT", "465")
	t.Setenv("TCAL_SMTP_FROM", "env@example.com")

	cfg := Load()
	if cfg.SMTP.Host != "env.example.com" {
		t.Errorf("SMTP.Host = %q, want %q", cfg.SMTP.Host, "env.example.com")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -v`
Expected: FAIL (SMTP field does not exist)

**Step 3: Add SMTP to config struct**

Modify `internal/config/config.go`:

```go
type Config struct {
	DB   string     `toml:"db"`
	SMTP SMTPConfig `toml:"smtp"`
}

type SMTPConfig struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
	From     string `toml:"from"`
}
```

Add a `LoadFile` function for testability and add env var overrides for SMTP:

```go
func LoadFile(path string) Config {
	var cfg Config
	toml.DecodeFile(path, &cfg)
	applyEnv(&cfg)
	return cfg
}

func Load() Config {
	var cfg Config
	if path, err := configFilePath(); err == nil {
		toml.DecodeFile(path, &cfg)
	}
	applyEnv(&cfg)
	return cfg
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("TCAL_DB"); v != "" {
		cfg.DB = v
	}
	if v := os.Getenv("TCAL_SMTP_HOST"); v != "" {
		cfg.SMTP.Host = v
	}
	if v := os.Getenv("TCAL_SMTP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.SMTP.Port = p
		}
	}
	if v := os.Getenv("TCAL_SMTP_USERNAME"); v != "" {
		cfg.SMTP.Username = v
	}
	if v := os.Getenv("TCAL_SMTP_PASSWORD"); v != "" {
		cfg.SMTP.Password = v
	}
	if v := os.Getenv("TCAL_SMTP_FROM"); v != "" {
		cfg.SMTP.From = v
	}
}
```

Add `"strconv"` to the imports.

**Step 4: Run tests**

Run: `go test ./internal/config/ -v`
Expected: PASS

**Step 5: Run all tests for regression**

Run: `go test ./...`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/config/
git commit -m "feat: add SMTP configuration for EMAIL alarm action"
```

---

## Task 6: CLI `alarm` Command Group

Add the `alarm` top-level command with `check`, `list`, `dismiss`, `snooze`, and `daemon` subcommands.

**Files:**
- Create: `cmd/tcal/alarm.go`
- Modify: `cmd/tcal/main.go:64` (register command)
- Modify: `internal/app/app.go:16-21` (add Alarms service)

**Step 1: Wire the alarm service into the App**

Modify `internal/app/app.go`:

```go
import (
	// ... existing imports ...
	"github.com/douglasdemoura/tcal/internal/alarm"
)

type App struct {
	DB        *sql.DB
	Calendars *calendar.Service
	Events    *event.Service
	Todos     *todo.Service
	Alarms    *alarm.Service
}

func New(dbPath string) (*App, error) {
	db, queries, err := storage.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}

	eventSvc := event.NewService(db, queries)
	return &App{
		DB:        db,
		Calendars: calendar.NewService(queries),
		Events:    eventSvc,
		Todos:     todo.NewService(db, queries),
		Alarms:    alarm.NewService(db, queries, eventSvc),
	}, nil
}
```

**Step 2: Create the alarm CLI commands**

```go
// cmd/tcal/alarm.go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/tcal/internal/notify"
)

func alarmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alarm",
		Short: "Manage alarm notifications",
	}
	cmd.AddCommand(alarmCheckCmd(), alarmListCmd(), alarmDismissCmd(), alarmSnoozeCmd(), alarmDaemonCmd())
	return cmd
}

func alarmCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check for due alarms and fire notifications",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			due, err := a.Alarms.Check(ctx, time.Now())
			if err != nil {
				return fmt.Errorf("check alarms: %w", err)
			}

			w := cmd.OutOrStdout()
			for _, da := range due {
				switch da.Alarm.Action {
				case "AUDIO":
					if err := notify.Audio(da); err != nil {
						fmt.Fprintf(os.Stderr, "audio alarm failed: %v\n", err)
						// Fall back to display
						notify.Display(da)
					}
				case "EMAIL":
					smtpCfg := notify.SMTPConfig{
						Host:     cfg.SMTP.Host,
						Port:     cfg.SMTP.Port,
						Username: cfg.SMTP.Username,
						Password: cfg.SMTP.Password,
						From:     cfg.SMTP.From,
					}
					if err := notify.Email(da, smtpCfg); err != nil {
						fmt.Fprintf(os.Stderr, "email alarm failed: %v\n", err)
						// Fall back to display
						notify.Display(da)
					}
				default: // DISPLAY
					if err := notify.Display(da); err != nil {
						fmt.Fprintf(os.Stderr, "display alarm failed: %v\n", err)
					}
				}

				if err := a.Alarms.MarkFired(ctx, da); err != nil {
					fmt.Fprintf(os.Stderr, "mark fired: %v\n", err)
				}

				if jsonOut {
					printJSON(w, map[string]any{
						"event":      da.Event.Title,
						"action":     da.Alarm.Action,
						"trigger_at": da.TriggerAt.Local().Format(time.RFC3339),
					})
				} else {
					fmt.Fprintf(w, "  Fired: %s (%s at %s)\n",
						da.Event.Title,
						da.Alarm.Action,
						da.TriggerAt.Local().Format("15:04"))
				}
			}

			if !jsonOut && len(due) == 0 {
				fmt.Fprintln(w, "No alarms due.")
			}
			return nil
		},
	}
	return cmd
}

func alarmListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List fired but unacknowledged alarms",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			pending, err := a.Alarms.ListPending(ctx)
			if err != nil {
				return fmt.Errorf("list pending: %w", err)
			}

			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, pending)
			}
			if len(pending) == 0 {
				fmt.Fprintln(w, "No pending alarms.")
				return nil
			}
			for _, p := range pending {
				fmt.Fprintf(w, "  [%d] event=%d trigger=%s fired=%s\n",
					p.ID, p.EventID, p.TriggerAt, p.FiredAt.String)
			}
			return nil
		},
	}
	return cmd
}

func alarmDismissCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dismiss <state-id>",
		Short: "Dismiss a fired alarm",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid state ID: %w", err)
			}

			if err := a.Alarms.Dismiss(context.Background(), id); err != nil {
				return fmt.Errorf("dismiss: %w", err)
			}

			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, map[string]any{"dismissed": true, "id": id})
			}
			fmt.Fprintf(w, "Dismissed alarm state %d.\n", id)
			return nil
		},
	}
	return cmd
}

func alarmSnoozeCmd() *cobra.Command {
	var durStr string
	cmd := &cobra.Command{
		Use:   "snooze <state-id>",
		Short: "Snooze a fired alarm",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid state ID: %w", err)
			}

			dur, err := time.ParseDuration(durStr)
			if err != nil {
				return fmt.Errorf("parse duration: %w", err)
			}

			until := time.Now().Add(dur)
			if err := a.Alarms.Snooze(context.Background(), id, until); err != nil {
				return fmt.Errorf("snooze: %w", err)
			}

			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, map[string]any{"snoozed": true, "id": id, "until": until.Local().Format(time.RFC3339)})
			}
			fmt.Fprintf(w, "Snoozed alarm state %d until %s.\n", id, until.Local().Format("15:04"))
			return nil
		},
	}
	cmd.Flags().StringVar(&durStr, "for", "15m", "snooze duration (e.g. 5m, 15m, 1h)")
	return cmd
}

func alarmDaemonCmd() *cobra.Command {
	var intervalStr string
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run alarm checker in a loop",
		RunE: func(cmd *cobra.Command, args []string) error {
			interval, err := time.ParseDuration(intervalStr)
			if err != nil {
				return fmt.Errorf("parse interval: %w", err)
			}

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Alarm daemon started (checking every %s). Press Ctrl+C to stop.\n", interval)

			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			// Run immediately on start, then on each tick
			runCheck := func() {
				a, err := initApp()
				if err != nil {
					fmt.Fprintf(os.Stderr, "init app: %v\n", err)
					return
				}
				defer a.Close()

				due, err := a.Alarms.Check(ctx, time.Now())
				if err != nil {
					fmt.Fprintf(os.Stderr, "check: %v\n", err)
					return
				}
				for _, da := range due {
					switch da.Alarm.Action {
					case "AUDIO":
						if err := notify.Audio(da); err != nil {
							notify.Display(da)
						}
					case "EMAIL":
						smtpCfg := notify.SMTPConfig{
							Host:     cfg.SMTP.Host,
							Port:     cfg.SMTP.Port,
							Username: cfg.SMTP.Username,
							Password: cfg.SMTP.Password,
							From:     cfg.SMTP.From,
						}
						if err := notify.Email(da, smtpCfg); err != nil {
							notify.Display(da)
						}
					default:
						notify.Display(da)
					}
					a.Alarms.MarkFired(ctx, da)
					fmt.Fprintf(w, "  [%s] Fired: %s\n", time.Now().Format("15:04:05"), da.Event.Title)
				}
			}

			runCheck()
			for {
				select {
				case <-ctx.Done():
					fmt.Fprintln(w, "Alarm daemon stopped.")
					return nil
				case <-ticker.C:
					runCheck()
				}
			}
		},
	}
	cmd.Flags().StringVar(&intervalStr, "interval", "30s", "check interval (e.g. 30s, 1m)")
	return cmd
}
```

**Step 3: Register the alarm command**

Modify `cmd/tcal/main.go:64`:

```go
rootCmd.AddCommand(eventCmd(), calendarCmd(), todoCmd(), icalCmd(), alarmCmd())
```

**Step 4: Verify build**

Run: `go build ./...`
Expected: Clean build

**Step 5: Verify help**

Run: `./tcal alarm --help`
Expected: Shows check, list, dismiss, snooze, daemon subcommands

Run: `./tcal alarm check --help`
Expected: Shows check command help

**Step 6: Run all tests**

Run: `go test ./...`
Expected: All PASS

**Step 7: Commit**

```bash
git add cmd/tcal/alarm.go cmd/tcal/main.go internal/app/app.go
git commit -m "feat: add alarm CLI commands (check, list, dismiss, snooze, daemon)"
```

---

## Task 7: Systemd Service Integration

Provide `tcal service install` and `tcal service uninstall` for systemd user timers (Linux) and launchd (macOS).

**Files:**
- Create: `cmd/tcal/service.go`
- Modify: `cmd/tcal/main.go:64` (register command)

**Step 1: Write the service command**

```go
// cmd/tcal/service.go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"text/template"

	"github.com/spf13/cobra"
)

func serviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage alarm notification service",
	}
	cmd.AddCommand(serviceInstallCmd(), serviceUninstallCmd(), serviceStatusCmd())
	return cmd
}

const systemdService = `[Unit]
Description=tcal alarm checker

[Service]
Type=oneshot
ExecStart={{.Binary}} alarm check
`

const systemdTimer = `[Unit]
Description=Check tcal alarms every minute

[Timer]
OnCalendar=*-*-* *:*:00
Persistent=true

[Install]
WantedBy=timers.target
`

const launchdPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.tcal.alarm</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.Binary}}</string>
    <string>alarm</string>
    <string>check</string>
  </array>
  <key>StartInterval</key>
  <integer>60</integer>
  <key>StandardOutPath</key>
  <string>{{.LogDir}}/tcal-alarm.log</string>
  <key>StandardErrorPath</key>
  <string>{{.LogDir}}/tcal-alarm.err</string>
</dict>
</plist>
`

func serviceInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install alarm checker as a system service",
		RunE: func(cmd *cobra.Command, args []string) error {
			binary, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolve binary path: %w", err)
			}
			binary, err = filepath.EvalSymlinks(binary)
			if err != nil {
				return fmt.Errorf("resolve symlinks: %w", err)
			}

			w := cmd.OutOrStdout()

			switch runtime.GOOS {
			case "linux":
				return installSystemd(w, binary)
			case "darwin":
				return installLaunchd(w, binary)
			default:
				fmt.Fprintf(w, "Automatic service install not supported on %s.\n", runtime.GOOS)
				fmt.Fprintf(w, "Use: tcal alarm daemon --interval 30s\n")
				return nil
			}
		},
	}
	return cmd
}

func serviceUninstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall the alarm checker service",
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			switch runtime.GOOS {
			case "linux":
				return uninstallSystemd(w)
			case "darwin":
				return uninstallLaunchd(w)
			default:
				fmt.Fprintf(w, "No service to uninstall on %s.\n", runtime.GOOS)
				return nil
			}
		},
	}
	return cmd
}

func serviceStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check alarm service status",
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			switch runtime.GOOS {
			case "linux":
				out, err := exec.Command("systemctl", "--user", "status", "tcal-alarm.timer").CombinedOutput()
				fmt.Fprint(w, string(out))
				if err != nil {
					return fmt.Errorf("timer not active (run: tcal service install)")
				}
				return nil
			case "darwin":
				out, err := exec.Command("launchctl", "list", "com.tcal.alarm").CombinedOutput()
				fmt.Fprint(w, string(out))
				if err != nil {
					return fmt.Errorf("agent not loaded (run: tcal service install)")
				}
				return nil
			default:
				fmt.Fprintf(w, "Service status not supported on %s.\n", runtime.GOOS)
				return nil
			}
		},
	}
	return cmd
}

type serviceData struct {
	Binary string
	LogDir string
}

func installSystemd(w *os.File, binary string) error {
	dir := filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user")
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		dir = filepath.Join(xdg, "systemd", "user")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data := serviceData{Binary: binary}

	// Write service file
	svcPath := filepath.Join(dir, "tcal-alarm.service")
	if err := writeTemplate(svcPath, systemdService, data); err != nil {
		return err
	}

	// Write timer file
	timerPath := filepath.Join(dir, "tcal-alarm.timer")
	if err := writeTemplate(timerPath, systemdTimer, data); err != nil {
		return err
	}

	// Enable and start
	exec.Command("systemctl", "--user", "daemon-reload").Run()
	if err := exec.Command("systemctl", "--user", "enable", "--now", "tcal-alarm.timer").Run(); err != nil {
		return fmt.Errorf("enable timer: %w", err)
	}

	fmt.Fprintf(w, "Installed and started tcal-alarm.timer\n")
	fmt.Fprintf(w, "  Service: %s\n", svcPath)
	fmt.Fprintf(w, "  Timer:   %s\n", timerPath)
	fmt.Fprintf(w, "  Status:  tcal service status\n")
	fmt.Fprintf(w, "  Logs:    journalctl --user -u tcal-alarm -f\n")
	return nil
}

func uninstallSystemd(w *os.File) error {
	exec.Command("systemctl", "--user", "disable", "--now", "tcal-alarm.timer").Run()
	exec.Command("systemctl", "--user", "daemon-reload").Run()

	dir := filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user")
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		dir = filepath.Join(xdg, "systemd", "user")
	}
	os.Remove(filepath.Join(dir, "tcal-alarm.service"))
	os.Remove(filepath.Join(dir, "tcal-alarm.timer"))

	fmt.Fprintln(w, "Uninstalled tcal-alarm.timer")
	return nil
}

func installLaunchd(w *os.File, binary string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	agentDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return err
	}

	logDir := filepath.Join(home, "Library", "Logs")
	os.MkdirAll(logDir, 0o755)

	plistPath := filepath.Join(agentDir, "com.tcal.alarm.plist")
	data := serviceData{Binary: binary, LogDir: logDir}
	if err := writeTemplate(plistPath, launchdPlist, data); err != nil {
		return err
	}

	if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
		return fmt.Errorf("load agent: %w", err)
	}

	fmt.Fprintf(w, "Installed and loaded com.tcal.alarm\n")
	fmt.Fprintf(w, "  Plist:  %s\n", plistPath)
	fmt.Fprintf(w, "  Status: tcal service status\n")
	fmt.Fprintf(w, "  Logs:   tail -f %s/tcal-alarm.log\n", logDir)
	return nil
}

func uninstallLaunchd(w *os.File) error {
	home, _ := os.UserHomeDir()
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.tcal.alarm.plist")
	exec.Command("launchctl", "unload", plistPath).Run()
	os.Remove(plistPath)
	fmt.Fprintln(w, "Uninstalled com.tcal.alarm")
	return nil
}

func writeTemplate(path, tmpl string, data any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	t := template.Must(template.New("").Parse(tmpl))
	return t.Execute(f, data)
}
```

**Step 2: Register the service command**

Modify `cmd/tcal/main.go:64`:

```go
rootCmd.AddCommand(eventCmd(), calendarCmd(), todoCmd(), icalCmd(), alarmCmd(), serviceCmd())
```

Also update the root command Long description to include the new commands:

```go
Long: `tcal is a terminal calendar backed by SQLite with iCal import/export.

Launch the TUI by running tcal with no arguments, or use subcommands
for scriptable access to all calendar operations.

Resource groups:
  event      Manage events (list, get, add, update, delete)
  todo       Manage todos (list, get, add, update, delete)
  calendar   Manage calendars (list, get, create, update, delete)
  ical       Import and export iCal (.ics) files
  alarm      Check, dismiss, snooze alarms
  service    Install/uninstall alarm notification service`,
```

**Step 3: Verify build and help**

Run: `go build ./... && ./tcal service --help`
Expected: Shows install, uninstall, status subcommands

**Step 4: Commit**

```bash
git add cmd/tcal/service.go cmd/tcal/main.go
git commit -m "feat: add service install/uninstall for systemd and launchd"
```

---

## Task 8: Integration Test -- Full Alarm Lifecycle

End-to-end test: create event with alarm, check for due alarms, mark fired, dismiss.

**Files:**
- Create: `internal/alarm/integration_test.go`

**Step 1: Write the integration test**

```go
// internal/alarm/integration_test.go
package alarm

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
	"github.com/douglasdemoura/tcal/internal/testutil"
)

func TestAlarmLifecycle(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	evtSvc := event.NewService(db, q)
	svc := NewService(db, q, evtSvc)
	ctx := context.Background()

	// 1. Create event with alarm due now
	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Standup",
		StartTime:  start,
		EndTime:    start.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "Time for standup"},
		{Action: "AUDIO", TriggerValue: "-PT5M"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2. Check -- only the -PT15M alarm should be due (5 min ago)
	//    The -PT5M alarm is still 5 min in the future
	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("check: got %d due, want 1", len(due))
	}
	if due[0].Alarm.Description != "Time for standup" {
		t.Errorf("description = %q, want %q", due[0].Alarm.Description, "Time for standup")
	}

	// 3. Mark fired
	err = svc.MarkFired(ctx, due[0])
	if err != nil {
		t.Fatal(err)
	}

	// 4. Check again -- nothing due
	due, err = svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("after mark: got %d due, want 0", len(due))
	}

	// 5. List pending (fired but not acked)
	pending, err := svc.ListPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending: got %d, want 1", len(pending))
	}

	// 6. Dismiss
	err = svc.Dismiss(ctx, pending[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	// 7. Pending should be empty
	pending, err = svc.ListPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("after dismiss: got %d, want 0", len(pending))
	}
}

func TestAlarmLifecycle_Snooze(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	evtSvc := event.NewService(db, q)
	svc := NewService(db, q, evtSvc)
	ctx := context.Background()

	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Snoozeable",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	})
	if err != nil {
		t.Fatal(err)
	}

	due, _ := svc.Check(ctx, time.Now())
	if len(due) != 1 {
		t.Fatalf("check: got %d, want 1", len(due))
	}

	svc.MarkFired(ctx, due[0])

	pending, _ := svc.ListPending(ctx)
	if len(pending) != 1 {
		t.Fatalf("pending: got %d, want 1", len(pending))
	}

	// Snooze for 10 minutes
	until := time.Now().Add(10 * time.Minute)
	err = svc.Snooze(ctx, pending[0].ID, until)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the snooze was recorded (the pending alarm should have snoozed_to set)
	pending, _ = svc.ListPending(ctx)
	if len(pending) != 1 {
		t.Fatalf("after snooze pending: got %d, want 1", len(pending))
	}
	if !pending[0].SnoozedTo.Valid {
		t.Error("snoozed_to should be set")
	}
}
```

**Step 2: Run tests**

Run: `go test ./internal/alarm/ -v`
Expected: All PASS

**Step 3: Run full test suite**

Run: `go test ./...`
Expected: All PASS

**Step 4: Commit**

```bash
git add internal/alarm/integration_test.go
git commit -m "test: add alarm lifecycle integration tests"
```

---

## Task 9: Manual Smoke Test

Verify the full flow end-to-end on a real system.

**Step 1: Create an event with an alarm due soon**

```bash
# Create event starting in 2 minutes with a 3-minute-before alarm
./tcal event add "Smoke Test" \
  --date $(date +%Y-%m-%d) \
  --time $(date -d "+2 min" +%H:%M) \
  --duration 30m \
  --alarm -PT3M
```

**Step 2: Verify alarm check fires**

```bash
./tcal alarm check
```

Expected: "Fired: Smoke Test (DISPLAY at HH:MM)" and a desktop notification appears.

**Step 3: Verify idempotency**

```bash
./tcal alarm check
```

Expected: "No alarms due." (already fired)

**Step 4: Test pending/dismiss flow**

```bash
./tcal alarm list         # Shows the fired alarm
./tcal alarm dismiss 1    # Dismiss it
./tcal alarm list         # Empty
```

**Step 5: Test daemon mode**

```bash
./tcal alarm daemon --interval 5s
# Wait ~10 seconds, then Ctrl+C
```

Expected: Daemon starts, checks twice, exits cleanly on SIGINT.

**Step 6: Test service install (Linux)**

```bash
./tcal service install
./tcal service status
./tcal service uninstall
```

**Step 7: Clean up**

```bash
./tcal event delete 1
```

---

## Implementation Notes

### What this plan does NOT cover (future work)

1. **Recurring event alarm expansion** -- The checker queries by `start_time` date range, which works for non-recurring events. For recurring events with RRULE, the checker would need to expand occurrences to compute trigger times for future instances. This is a significant feature on its own.

2. **Todo alarms** -- The current plan only handles event alarms. The `todo_alarms` table has the same structure, so extending the checker to todos is straightforward but out of scope.

3. **Snooze re-firing** -- The snooze command records `snoozed_to` but the checker doesn't yet re-fire snoozed alarms when the snooze time arrives. This requires the checker to also query `alarm_state` rows where `snoozed_to <= now AND acked_at IS NULL`.

4. **Custom notification command** -- Like calcurse's `notification.command`, allowing users to specify an arbitrary shell command instead of using beeep. This is a good config extension.

### Key design decisions

- **No self-daemonization** -- `tcal alarm daemon` runs in the foreground. Use systemd/launchd for backgrounding.
- **Stale threshold = 24h** -- Alarms older than 24 hours are silently skipped to avoid notification floods after a long offline period.
- **EMAIL falls back to DISPLAY** -- If SMTP is not configured or sending fails, EMAIL alarms fall back to desktop notifications rather than silently failing.
- **State is per alarm+trigger_at** -- The unique constraint on `(alarm_id, trigger_at)` prevents duplicate firing for the same alarm at the same time, which is important when the checker runs on a short interval.
