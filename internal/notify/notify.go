package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
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

type ExecutionPolicy struct {
	AllowUnsafeAudioAttach    bool
	AllowUnsafeEmailAttendees bool
}

// FormatNotification formats a DueAlarm into a title and body suitable for display.
// Title is the event title. Body contains the formatted time and, when present,
// the location. The description is included only when it is not generic reminder boilerplate.
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
	if !isGenericAlarmDescription(desc) {
		parts = append(parts, textsafe.Display(desc))
	}

	// Fallback for bare todos: show trigger time so the notification isn't empty.
	if len(parts) == 0 && !da.TriggerAt.IsZero() {
		parts = append(parts, da.TriggerAt.Local().Format("Mon Jan 2, 15:04"))
	}

	body = strings.Join(parts, " - ")
	return title, body
}

func isGenericAlarmDescription(desc string) bool {
	normalized := strings.TrimSpace(desc)
	normalized = strings.TrimSuffix(normalized, ".")
	switch strings.ToLower(normalized) {
	case "", "reminder", "this is an event reminder":
		return true
	default:
		return false
	}
}

// Display sends a desktop notification for the given alarm.
func Display(da alarm.DueAlarm) error {
	title, body := FormatNotification(da)
	return beeep.Notify(title, body, "")
}

// Audio sends a desktop notification and plays a sound. Calendar-provided
// local paths are only honored when the execution policy explicitly allows it.
func Audio(da alarm.DueAlarm, policy ExecutionPolicy) error {
	// Send visual notification first (best-effort).
	_ = Display(da)

	if path := resolveLocalAudioPath(da.Alarm.AttachURI, policy); path != "" {
		if err := playAudio(path); err == nil {
			return nil
		}
	}

	// Fall back to platform system sounds.
	return playSystemSound()
}

// resolveLocalAudioPath returns an absolute file path if the URI points to a
// local audio file that exists and the execution policy explicitly allows it.
func resolveLocalAudioPath(uri string, policy ExecutionPolicy) string {
	if !policy.AllowUnsafeAudioAttach || uri == "" {
		return ""
	}
	var path string
	if p, ok := strings.CutPrefix(uri, "file://"); ok {
		u, err := url.Parse("file://" + p)
		if err != nil {
			return ""
		}
		if u.Host != "" && u.Host != "localhost" {
			return ""
		}
		path = u.Path
	} else if strings.HasPrefix(uri, "/") {
		path = uri
	} else {
		return "" // HTTP, data:, or other unsupported scheme.
	}
	if !filepath.IsAbs(path) {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "" // File doesn't exist or isn't a regular file.
	}
	if !isLikelyAudioFile(path) {
		return ""
	}
	return path
}

const (
	linuxSystemSound     = "/usr/share/sounds/freedesktop/stereo/alarm-clock-elapsed.oga"
	darwinSystemSound    = "/System/Library/Sounds/Glass.aiff"
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

// smtpsPort is the IANA-assigned port for SMTPS (implicit-TLS submission).
const smtpsPort = 465

// dialTLS opens a TLS connection for SMTPS (implicit TLS). Tests may replace
// this to inject a custom *tls.Config, e.g. one with InsecureSkipVerify set.
var dialTLS = func(addr, serverName string) (net.Conn, error) {
	return tls.Dial("tcp", addr, &tls.Config{ServerName: serverName})
}

// useImplicitTLS reports whether smtpCfg requires implicit TLS (SMTPS).
// An explicit TLSMode always wins; when TLSMode is empty the port decides.
func useImplicitTLS(smtpCfg config.SMTPConfig) bool {
	switch smtpCfg.TLSMode {
	case "implicit":
		return true
	case "starttls", "none":
		return false
	default: // "" or unrecognised: auto-detect by port
		return smtpCfg.Port == smtpsPort
	}
}

// sendMailImplicitTLS delivers a message over an already-TLS-wrapped connection
// (SMTPS). It dials with tls.Dial so the TLS handshake happens before any SMTP
// traffic, which is what port-465 servers expect.
func sendMailImplicitTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := dialTLS(addr, host)
	if err != nil {
		return fmt.Errorf("dial SMTPS %s: %w", addr, err)
	}
	defer conn.Close()

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("smtp RCPT TO <%s>: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp end DATA: %w", err)
	}
	return c.Quit()
}

// Email sends an email notification for an EMAIL action alarm.
// It sends to the alarm's attendees using the provided SMTP configuration.
// Returns an error if no attendees are configured or SMTP is not configured.
func Email(da alarm.DueAlarm, smtpCfg config.SMTPConfig, policy ExecutionPolicy) error {
	if smtpCfg.Host == "" {
		return fmt.Errorf("SMTP not configured")
	}

	if len(da.Alarm.Attendees) == 0 {
		return fmt.Errorf("no attendees for EMAIL alarm")
	}
	if !policy.AllowUnsafeEmailAttendees {
		return fmt.Errorf("unsafe EMAIL alarm attendees are disabled; pass an explicit allow flag or config option")
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

	if useImplicitTLS(smtpCfg) {
		return sendMailImplicitTLS(addr, smtpCfg.Host, auth, smtpCfg.From, to, []byte(msg))
	}
	return smtp.SendMail(addr, auth, smtpCfg.From, to, []byte(msg))
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
