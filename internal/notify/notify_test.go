package notify

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"mime"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/alarm"
	"github.com/douglasdemoura/chroncal/internal/config"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
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

func TestFormatNotification_SuppressesGenericEventReminderDescription(t *testing.T) {
	da := alarm.DueAlarm{
		Event: event.Event{
			Title:     "Team Standup",
			StartTime: time.Date(2026, 3, 27, 9, 30, 0, 0, time.Local),
		},
		Alarm: model.Alarm{
			Action:      "DISPLAY",
			Description: "this is an event reminder",
		},
	}

	_, body := FormatNotification(da)

	want := "Fri Mar 27, 09:30"
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
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

func TestFormatNotification_SanitizesControlCharacters(t *testing.T) {
	da := alarm.DueAlarm{
		Event: event.Event{
			Title:     "Bad\x1b]52;c;clip\a",
			Location:  "Room\r\nB",
			StartTime: time.Date(2026, 3, 27, 10, 0, 0, 0, time.Local),
		},
		Alarm: model.Alarm{
			Action:      "DISPLAY",
			Description: "Line\x1b[31m",
		},
	}

	title, body := FormatNotification(da)

	if strings.Contains(title, "\x1b") || strings.Contains(title, "\r") || strings.Contains(title, "\n") {
		t.Fatalf("title contains control characters: %q", title)
	}
	if strings.Contains(body, "\x1b") || strings.Contains(body, "\r") || strings.Contains(body, "\n") {
		t.Fatalf("body contains control characters: %q", body)
	}
	if strings.Contains(title, "]52;c;clip") {
		t.Fatalf("title contains raw OSC payload: %q", title)
	}
}

func TestResolveLocalAudioPath_RequiresExplicitUnsafeOptIn(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "alert.oga")
	if err := os.WriteFile(tmp, []byte("fake audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	notAudio := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notAudio, []byte("not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(dir, "folder.oga")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		uri    string
		policy ExecutionPolicy
		want   bool // true = expect non-empty path
	}{
		{"empty", "", ExecutionPolicy{}, false},
		{"absolute path exists disabled", tmp, ExecutionPolicy{}, false},
		{"absolute path exists enabled", tmp, ExecutionPolicy{AllowUnsafeAudioAttach: true}, true},
		{"absolute path missing", "/nonexistent/sound.wav", ExecutionPolicy{AllowUnsafeAudioAttach: true}, false},
		{"file:// URI exists disabled", "file://" + tmp, ExecutionPolicy{}, false},
		{"file:// URI exists enabled", "file://" + tmp, ExecutionPolicy{AllowUnsafeAudioAttach: true}, true},
		{"file:// URI missing", "file:///nonexistent/sound.wav", ExecutionPolicy{AllowUnsafeAudioAttach: true}, false},
		{"directory rejected", subdir, ExecutionPolicy{AllowUnsafeAudioAttach: true}, false},
		{"non-audio extension rejected", notAudio, ExecutionPolicy{AllowUnsafeAudioAttach: true}, false},
		{"http URI", "http://example.com/sound.wav", ExecutionPolicy{AllowUnsafeAudioAttach: true}, false},
		{"https URI", "https://example.com/sound.wav", ExecutionPolicy{AllowUnsafeAudioAttach: true}, false},
		{"data URI", "data:audio/wav;base64,AAAA", ExecutionPolicy{AllowUnsafeAudioAttach: true}, false},
		{"relative path", "sounds/alert.wav", ExecutionPolicy{AllowUnsafeAudioAttach: true}, false},
		{"file URI relative path rejected", "file://relative.wav", ExecutionPolicy{AllowUnsafeAudioAttach: true}, false},
		{"file URI remote host rejected", "file://example.com/alert.oga", ExecutionPolicy{AllowUnsafeAudioAttach: true}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveLocalAudioPath(tt.uri, tt.policy)
			if tt.want && got == "" {
				t.Errorf("resolveLocalAudioPath(%q) = empty, want non-empty", tt.uri)
			}
			if !tt.want && got != "" {
				t.Errorf("resolveLocalAudioPath(%q) = %q, want empty", tt.uri, got)
			}
		})
	}
}

func TestEmail_RequiresSMTPConfig(t *testing.T) {
	da := alarm.DueAlarm{
		Event: event.Event{Title: "Test Event"},
		Alarm: model.Alarm{
			Action: "EMAIL",
			Attendees: []model.AlarmAttendee{
				{Email: "user@example.com"},
			},
		},
	}

	err := Email(da, config.SMTPConfig{
		Port: 587,
		From: "sender@example.com",
	}, ExecutionPolicy{AllowUnsafeEmailAttendees: true})
	if err == nil {
		t.Fatal("Email err = nil, want SMTP configuration error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "smtp not configured") {
		t.Fatalf("Email err = %q, want SMTP configuration message", err)
	}
}

func TestEmail_RequiresExplicitUnsafeOptInForStoredAttendees(t *testing.T) {
	da := alarm.DueAlarm{
		Event: event.Event{Title: "Test Event"},
		Alarm: model.Alarm{
			Action: "EMAIL",
			Attendees: []model.AlarmAttendee{
				{Email: "user@example.com"},
			},
		},
	}

	err := Email(da, config.SMTPConfig{
		Host: "smtp.example.com",
		Port: 587,
		From: "sender@example.com",
	}, ExecutionPolicy{})
	if err == nil {
		t.Fatal("Email err = nil, want unsafe opt-in error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unsafe") {
		t.Fatalf("Email err = %q, want unsafe opt-in message", err)
	}
}

// TestUseImplicitTLS verifies the connection-mode selection logic.
// Port 465 and TLSMode "implicit" both trigger implicit-TLS (SMTPS); an
// explicit TLSMode always overrides port-based detection.
func TestUseImplicitTLS(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.SMTPConfig
		want bool
	}{
		{"port 465 auto", config.SMTPConfig{Port: 465}, true},
		{"port 587 auto", config.SMTPConfig{Port: 587}, false},
		{"port 25 auto", config.SMTPConfig{Port: 25}, false},
		{"tls=implicit explicit", config.SMTPConfig{Port: 587, TLSMode: "implicit"}, true},
		{"tls=starttls explicit", config.SMTPConfig{Port: 587, TLSMode: "starttls"}, false},
		{"tls=none explicit", config.SMTPConfig{Port: 587, TLSMode: "none"}, false},
		{"port 465 tls=starttls explicit overrides", config.SMTPConfig{Port: 465, TLSMode: "starttls"}, false},
		{"port 465 tls=implicit redundant", config.SMTPConfig{Port: 465, TLSMode: "implicit"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := useImplicitTLS(tt.cfg)
			if got != tt.want {
				t.Errorf("useImplicitTLS(%+v) = %v, want %v", tt.cfg, got, tt.want)
			}
		})
	}
}

// TestEmail_ImplicitTLS_SMTPSServer starts a local TLS SMTP server and verifies
// that Email can deliver a message using implicit TLS (SMTPS / port 465).
// This test exercises the code path that smtp.SendMail cannot handle.
func TestEmail_ImplicitTLS_SMTPSServer(t *testing.T) {
	cert := generateTestCert(t)
	port, serverDone := startMinimalSMTPSServer(t, cert)

	// Override dialTLS to skip certificate verification for the self-signed test cert.
	origDial := dialTLS
	dialTLS = func(addr, _ string) (net.Conn, error) {
		dialer := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
		return dialer.DialContext(context.Background(), "tcp", addr)
	}
	t.Cleanup(func() { dialTLS = origDial })

	da := alarm.DueAlarm{
		Event: event.Event{Title: "SMTPS Test"},
		Alarm: model.Alarm{
			Action:    "EMAIL",
			Attendees: []model.AlarmAttendee{{Email: "user@example.com"}},
		},
	}

	err := Email(da, config.SMTPConfig{
		Host:    "127.0.0.1",
		Port:    port,
		From:    "sender@example.com",
		TLSMode: "implicit",
	}, ExecutionPolicy{AllowUnsafeEmailAttendees: true})
	if err != nil {
		t.Fatalf("Email (implicit TLS) error: %v", err)
	}

	select {
	case serverErr := <-serverDone:
		if serverErr != nil {
			t.Fatalf("SMTPS server error: %v", serverErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for SMTPS server to finish")
	}
}

func TestBuildEmailMessage_MIMEHeaders(t *testing.T) {
	tests := []struct {
		name    string
		from    string
		to      []string
		subject string
		body    string
	}{
		{
			name:    "ascii",
			from:    "sender@example.com",
			to:      []string{"user@example.com"},
			subject: "Reminder",
			body:    "Meeting at noon",
		},
		{
			name:    "non-ascii",
			from:    "sender@example.com",
			to:      []string{"a@example.com", "b@example.com"},
			subject: "Reunião com café",
			body:    "Não esqueça do açúcar — naïve façade ☕",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := string(buildEmailMessage(tt.from, tt.to, tt.subject, tt.body))

			head, rest, found := strings.Cut(msg, "\r\n\r\n")
			if !found {
				t.Fatalf("message has no header/body separator:\n%q", msg)
			}

			if !strings.Contains(head, "MIME-Version: 1.0") {
				t.Errorf("message headers missing MIME-Version:\n%q", head)
			}
			if !strings.Contains(head, "Content-Type: text/plain; charset=utf-8") {
				t.Errorf("message headers missing UTF-8 Content-Type:\n%q", head)
			}
			// The Subject header may be RFC 2047 encoded; decode before comparing.
			if got := decodeSubjectHeader(t, head); got != tt.subject {
				t.Errorf("decoded Subject = %q, want %q", got, tt.subject)
			}
			if !strings.Contains(rest, tt.body) {
				t.Errorf("message body = %q, want it to contain %q", rest, tt.body)
			}
		})
	}
}

// TestBuildEmailMessage_SubjectHeaderEncoding asserts the Subject header is
// RFC 2047 encoded for non-ASCII titles: the raw header line must stay ASCII so
// non-SMTPUTF8 servers/clients render it correctly, and decoding it must yield
// the original subject.
func TestBuildEmailMessage_SubjectHeaderEncoding(t *testing.T) {
	subject := "Reunião com café ☕"
	msg := string(buildEmailMessage("sender@example.com", []string{"user@example.com"}, subject, "body"))

	head, _, found := strings.Cut(msg, "\r\n\r\n")
	if !found {
		t.Fatalf("message has no header/body separator:\n%q", msg)
	}

	var subjectLine string
	for _, line := range strings.Split(head, "\r\n") {
		if v, ok := strings.CutPrefix(line, "Subject: "); ok {
			subjectLine = v
			break
		}
	}
	if subjectLine == "" {
		t.Fatalf("no Subject header found:\n%q", head)
	}

	// The raw Subject header must be pure ASCII (RFC 5322 / 2047).
	for _, r := range subjectLine {
		if r > 127 {
			t.Fatalf("Subject header contains non-ASCII rune %q: %q", r, subjectLine)
		}
	}

	// And it must decode back to the original subject.
	if got := decodeSubjectHeader(t, head); got != subject {
		t.Errorf("decoded Subject = %q, want %q", got, subject)
	}
}

// decodeSubjectHeader extracts the Subject header from a message header block
// and returns its RFC 2047-decoded value.
func decodeSubjectHeader(t *testing.T, head string) string {
	t.Helper()
	for _, line := range strings.Split(head, "\r\n") {
		if v, ok := strings.CutPrefix(line, "Subject: "); ok {
			decoded, err := new(mime.WordDecoder).DecodeHeader(v)
			if err != nil {
				t.Fatalf("decoding Subject header %q: %v", v, err)
			}
			return decoded
		}
	}
	t.Fatalf("no Subject header found:\n%q", head)
	return ""
}

// generateTestCert creates a self-signed ECDSA certificate for 127.0.0.1/localhost.
func generateTestCert(t *testing.T) tls.Certificate {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// startMinimalSMTPSServer starts a TLS SMTP listener on a random local port.
// It accepts exactly one connection, runs a minimal SMTP dialog, and signals
// completion (with any error) on the returned channel.
func startMinimalSMTPSServer(t *testing.T, cert tls.Certificate) (port int, done <-chan error) {
	t.Helper()
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan error, 1)
	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			ch <- err
			return
		}
		defer conn.Close()
		ch <- runMinimalSMTPDialog(conn)
	}()
	return ln.Addr().(*net.TCPAddr).Port, ch
}

// runMinimalSMTPDialog handles one SMTP session: greeting → EHLO → MAIL FROM →
// RCPT TO(s) → DATA → body → QUIT. No AUTH is required.
func runMinimalSMTPDialog(conn net.Conn) error {
	rdr := bufio.NewReader(conn)

	send := func(line string) error {
		_, err := fmt.Fprintf(conn, "%s\r\n", line)
		return err
	}
	recv := func() (string, error) {
		line, err := rdr.ReadString('\n')
		return strings.TrimRight(line, "\r\n"), err
	}

	if err := send("220 localhost ESMTP test"); err != nil {
		return err
	}

	// EHLO / HELO
	line, err := recv()
	if err != nil {
		return err
	}
	upper := strings.ToUpper(line)
	if !strings.HasPrefix(upper, "EHLO") && !strings.HasPrefix(upper, "HELO") {
		return fmt.Errorf("expected EHLO/HELO, got %q", line)
	}
	if err := send("250 OK"); err != nil {
		return err
	}

	// MAIL FROM
	line, err = recv()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.ToUpper(line), "MAIL FROM:") {
		return fmt.Errorf("expected MAIL FROM, got %q", line)
	}
	if err := send("250 OK"); err != nil {
		return err
	}

	// One or more RCPT TO, then DATA
	for {
		line, err = recv()
		if err != nil {
			return err
		}
		up := strings.ToUpper(line)
		if strings.HasPrefix(up, "RCPT TO:") {
			if err := send("250 OK"); err != nil {
				return err
			}
		} else if up == "DATA" {
			break
		} else {
			return fmt.Errorf("expected RCPT TO or DATA, got %q", line)
		}
	}

	if err := send("354 Start mail input; end with <CRLF>.<CRLF>"); err != nil {
		return err
	}

	// Read body until lone "."
	for {
		line, err = recv()
		if err != nil {
			return err
		}
		if line == "." {
			break
		}
	}
	if err := send("250 OK"); err != nil {
		return err
	}

	// QUIT
	line, err = recv()
	if err != nil {
		return err
	}
	if strings.ToUpper(line) != "QUIT" {
		return fmt.Errorf("expected QUIT, got %q", line)
	}
	return send("221 Bye")
}
