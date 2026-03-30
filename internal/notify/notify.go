package notify

import (
	"fmt"
	"net/smtp"
	"os/exec"
	"runtime"
	"strings"

	"github.com/douglasdemoura/tcal/internal/alarm"
	"github.com/gen2brain/beeep"
)

// SMTPConfig holds SMTP connection settings for EMAIL action alarms.
type SMTPConfig struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
	From     string `toml:"from"`
}

// FormatNotification formats a DueAlarm into a title and body suitable for display.
// Title is the event title. Body contains the formatted time and, when present,
// the location. The description is included only if it is non-empty and not "Reminder".
func FormatNotification(da alarm.DueAlarm) (title, body string) {
	title = da.Event.Title

	var parts []string
	if !da.Event.StartTime.IsZero() {
		parts = append(parts, da.Event.StartTime.Local().Format("Mon Jan 2, 15:04"))
	}

	if da.Event.Location != "" {
		parts = append(parts, da.Event.Location)
	}

	desc := da.Alarm.Description
	if desc != "" && desc != "Reminder" {
		parts = append(parts, desc)
	}

	// Fallback for bare todos: show trigger time so the notification isn't empty.
	if len(parts) == 0 && !da.TriggerAt.IsZero() {
		parts = append(parts, da.TriggerAt.Local().Format("Mon Jan 2, 15:04"))
	}

	body = strings.Join(parts, " - ")
	return title, body
}

// Display sends a desktop notification for the given alarm.
func Display(da alarm.DueAlarm) error {
	title, body := FormatNotification(da)
	return beeep.Notify(title, body, "")
}

// Audio sends a desktop notification and plays a system sound.
func Audio(da alarm.DueAlarm) error {
	// Send visual notification first (best-effort).
	_ = Display(da)

	switch runtime.GOOS {
	case "linux":
		if err := exec.Command("paplay", "/usr/share/sounds/freedesktop/stereo/alarm-clock-elapsed.oga").Run(); err == nil {
			return nil
		}
		if err := exec.Command("aplay", "/usr/share/sounds/freedesktop/stereo/alarm-clock-elapsed.oga").Run(); err == nil {
			return nil
		}
		return beeep.Beep(beeep.DefaultFreq, beeep.DefaultDuration)
	case "darwin":
		return exec.Command("afplay", "/System/Library/Sounds/Glass.aiff").Run()
	default: // windows and others
		return beeep.Beep(beeep.DefaultFreq, beeep.DefaultDuration)
	}
}

// Email sends an email notification for an EMAIL action alarm.
// It sends to the alarm's attendees using the provided SMTP configuration.
// Returns an error if no attendees are configured or SMTP is not configured.
func Email(da alarm.DueAlarm, smtpCfg SMTPConfig) error {
	if smtpCfg.Host == "" {
		return fmt.Errorf("SMTP not configured")
	}

	if len(da.Alarm.Attendees) == 0 {
		return fmt.Errorf("no attendees for EMAIL alarm")
	}

	title, body := FormatNotification(da)

	to := make([]string, len(da.Alarm.Attendees))
	for i, att := range da.Alarm.Attendees {
		to[i] = att.Email
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s\r\n",
		smtpCfg.From,
		strings.Join(to, ", "),
		title,
		body,
	)

	addr := fmt.Sprintf("%s:%d", smtpCfg.Host, smtpCfg.Port)

	var auth smtp.Auth
	if smtpCfg.Username != "" {
		auth = smtp.PlainAuth("", smtpCfg.Username, smtpCfg.Password, smtpCfg.Host)
	}

	return smtp.SendMail(addr, auth, smtpCfg.From, to, []byte(msg))
}
