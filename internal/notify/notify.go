package notify

import (
	"context"
	"fmt"
	"mime"
	"net/mail"
	"net/smtp"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/alarm"
	"github.com/douglasdemoura/chroncal/internal/config"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
	"github.com/gen2brain/beeep"
)

// FormatNotification formats a DueAlarm into a title and body suitable for display.
// Title is the event title. Body contains the formatted time and, when present,
// the location. The description is included only if it is non-empty and not "Reminder".
func FormatNotification(da alarm.DueAlarm) (title, body string) {
	title = textsafe.Display(da.Event.Title)

	var parts []string
	if !da.Event.StartTime.IsZero() {
		parts = append(parts, da.Event.StartTime.Local().Format("Mon Jan 2, 15:04"))
	}

	if da.Event.Location != "" {
		parts = append(parts, textsafe.Display(da.Event.Location))
	}

	desc := da.Alarm.Description
	if desc != "" && desc != "Reminder" {
		parts = append(parts, textsafe.Display(desc))
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

// Audio sends a desktop notification and plays a sound. If the alarm has an
// ATTACH URI pointing to a local audio file, that file is played. Otherwise
// a platform-specific system sound is used as a fallback.
func Audio(da alarm.DueAlarm) error {
	// Send visual notification first (best-effort).
	_ = Display(da)

	// Try the alarm's ATTACH URI (local files only, no HTTP).
	if path := resolveLocalAudioPath(da.Alarm.AttachURI); path != "" {
		if err := playAudio(path); err == nil {
			return nil
		}
	}

	// Fall back to platform system sounds.
	return playSystemSound()
}

// resolveLocalAudioPath returns an absolute file path if the URI points to a
// local audio file that exists, or "" if it should be skipped.
func resolveLocalAudioPath(uri string) string {
	if uri == "" {
		return ""
	}
	var path string
	if p, ok := strings.CutPrefix(uri, "file://"); ok {
		path = p
	} else if strings.HasPrefix(uri, "/") {
		path = uri
	} else {
		return "" // HTTP, data:, or other unsupported scheme.
	}
	if _, err := os.Stat(path); err != nil {
		return "" // File doesn't exist.
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	if !isLikelyAudioFile(path) {
		return ""
	}
	return path
}

const (
	linuxSystemSound  = "/usr/share/sounds/freedesktop/stereo/alarm-clock-elapsed.oga"
	darwinSystemSound = "/System/Library/Sounds/Glass.aiff"
	audioPlaybackTimeout = 5 * time.Second
)

// playAudio plays an audio file using the platform's native player.
func playAudio(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), audioPlaybackTimeout)
	defer cancel()
	switch runtime.GOOS {
	case "linux":
		if err := exec.CommandContext(ctx, "paplay", path).Run(); err == nil {
			return nil
		}
		return exec.CommandContext(ctx, "aplay", path).Run()
	case "darwin":
		return exec.CommandContext(ctx, "afplay", path).Run()
	default:
		return fmt.Errorf("unsupported platform for audio playback")
	}
}

func isLikelyAudioFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return false
	}
	if mt := mime.TypeByExtension(ext); strings.HasPrefix(mt, "audio/") {
		return true
	}
	switch ext {
	case ".ogg", ".oga", ".wav", ".mp3", ".aiff", ".aif", ".flac", ".m4a", ".aac", ".au", ".snd":
		return true
	default:
		return false
	}
}

func playSystemSound() error {
	switch runtime.GOOS {
	case "linux":
		if err := playAudio(linuxSystemSound); err == nil {
			return nil
		}
		return beeep.Beep(beeep.DefaultFreq, beeep.DefaultDuration)
	case "darwin":
		return playAudio(darwinSystemSound)
	default:
		return beeep.Beep(beeep.DefaultFreq, beeep.DefaultDuration)
	}
}

// Email sends an email notification for an EMAIL action alarm.
// It sends to the alarm's attendees using the provided SMTP configuration.
// Returns an error if no attendees are configured or SMTP is not configured.
func Email(da alarm.DueAlarm, smtpCfg config.SMTPConfig) error {
	if !smtpCfg.EnableAlarmActions {
		return fmt.Errorf("EMAIL alarm actions are disabled")
	}
	if smtpCfg.Host == "" {
		return fmt.Errorf("SMTP not configured")
	}

	if len(da.Alarm.Attendees) == 0 {
		return fmt.Errorf("no attendees for EMAIL alarm")
	}

	title, body := FormatNotification(da)

	to := make([]string, 0, len(da.Alarm.Attendees))
	for _, att := range da.Alarm.Attendees {
		addr := textsafe.Display(att.Email)
		if addr == "" {
			continue
		}
		if _, err := mail.ParseAddress(addr); err != nil {
			continue
		}
		to = append(to, addr)
	}
	if len(to) == 0 {
		return fmt.Errorf("no valid attendees for EMAIL alarm")
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s\r\n",
		textsafe.Display(smtpCfg.From),
		strings.Join(to, ", "),
		textsafe.Display(title),
		textsafe.Display(body),
	)

	addr := fmt.Sprintf("%s:%d", smtpCfg.Host, smtpCfg.Port)

	var auth smtp.Auth
	if smtpCfg.Username != "" {
		auth = smtp.PlainAuth("", smtpCfg.Username, smtpCfg.Password, smtpCfg.Host)
	}

	return smtp.SendMail(addr, auth, smtpCfg.From, to, []byte(msg))
}
