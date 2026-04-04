package main

import (
	"strings"
	"testing"
)

func TestSystemdServiceTemplateUsesServiceRunAndSyncInterval(t *testing.T) {
	rendered, err := renderTemplate(systemdServiceTmpl, map[string]string{
		"BinaryPath":                     "/usr/local/bin/chroncal",
		"SyncInterval":                   "15m",
		"AllowUnsafeAlarmAudioAttach":    "true",
		"AllowUnsafeAlarmEmailAttendees": "true",
	})
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}

	if !strings.Contains(rendered, "ExecStart=/usr/local/bin/chroncal service run") {
		t.Fatalf("systemd service missing service run command:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Environment=CHRONCAL_SYNC_INTERVAL=15m") {
		t.Fatalf("systemd service missing sync interval env:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Environment=CHRONCAL_SECURITY_ALLOW_UNSAFE_ALARM_AUDIO_ATTACH=true") {
		t.Fatalf("systemd service missing unsafe audio env:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Environment=CHRONCAL_SECURITY_ALLOW_UNSAFE_ALARM_EMAIL_ATTENDEES=true") {
		t.Fatalf("systemd service missing unsafe email env:\n%s", rendered)
	}
}

func TestLaunchdTemplateUsesServiceRunAndSyncInterval(t *testing.T) {
	rendered, err := renderTemplate(launchdPlistTmpl, map[string]string{
		"BinaryPath":                     "/usr/local/bin/chroncal",
		"HomeDir":                        "/Users/tester",
		"SyncInterval":                   "15m",
		"AllowUnsafeAlarmAudioAttach":    "true",
		"AllowUnsafeAlarmEmailAttendees": "true",
	})
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}

	if !strings.Contains(rendered, "<string>service</string>") || !strings.Contains(rendered, "<string>run</string>") {
		t.Fatalf("launchd plist missing service run command:\n%s", rendered)
	}
	if !strings.Contains(rendered, "<key>CHRONCAL_SYNC_INTERVAL</key>") {
		t.Fatalf("launchd plist missing sync interval env:\n%s", rendered)
	}
	if !strings.Contains(rendered, "<key>CHRONCAL_SECURITY_ALLOW_UNSAFE_ALARM_AUDIO_ATTACH</key>") {
		t.Fatalf("launchd plist missing unsafe audio env:\n%s", rendered)
	}
	if !strings.Contains(rendered, "<key>CHRONCAL_SECURITY_ALLOW_UNSAFE_ALARM_EMAIL_ATTENDEES</key>") {
		t.Fatalf("launchd plist missing unsafe email env:\n%s", rendered)
	}
	if !strings.Contains(rendered, "<integer>60</integer>") {
		t.Fatalf("launchd plist missing one-minute schedule:\n%s", rendered)
	}
}

func TestServiceCommandRegistersRun(t *testing.T) {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() != "service" {
			continue
		}
		for _, sub := range cmd.Commands() {
			if sub.Name() == "run" {
				return
			}
		}
	}
	t.Fatal("service command is missing run")
}

func TestServiceRunCommandHasTickAlias(t *testing.T) {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() != "service" {
			continue
		}
		for _, sub := range cmd.Commands() {
			if sub.Name() != "run" {
				continue
			}
			for _, alias := range sub.Aliases {
				if alias == "tick" {
					return
				}
			}
			t.Fatal("service run command is missing tick alias")
		}
	}
	t.Fatal("service command is missing run")
}

func TestRootCommandDoesNotRegisterTopLevelTick(t *testing.T) {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "tick" {
			t.Fatal("root command should not register top-level tick")
		}
	}
}
