package main

import (
	"strings"
	"testing"
)

func TestSystemdServiceTemplateUsesTickAndSyncInterval(t *testing.T) {
	rendered, err := renderTemplate(systemdServiceTmpl, map[string]string{
		"BinaryPath":   "/usr/local/bin/chroncal",
		"SyncInterval": "15m",
	})
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}

	if !strings.Contains(rendered, "ExecStart=/usr/local/bin/chroncal tick") {
		t.Fatalf("systemd service missing tick command:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Environment=CHRONCAL_SYNC_INTERVAL=15m") {
		t.Fatalf("systemd service missing sync interval env:\n%s", rendered)
	}
}

func TestLaunchdTemplateUsesTickAndSyncInterval(t *testing.T) {
	rendered, err := renderTemplate(launchdPlistTmpl, map[string]string{
		"BinaryPath":   "/usr/local/bin/chroncal",
		"HomeDir":      "/Users/tester",
		"SyncInterval": "15m",
	})
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}

	if !strings.Contains(rendered, "<string>tick</string>") {
		t.Fatalf("launchd plist missing tick command:\n%s", rendered)
	}
	if !strings.Contains(rendered, "<key>CHRONCAL_SYNC_INTERVAL</key>") {
		t.Fatalf("launchd plist missing sync interval env:\n%s", rendered)
	}
	if !strings.Contains(rendered, "<integer>60</integer>") {
		t.Fatalf("launchd plist missing one-minute schedule:\n%s", rendered)
	}
}

func TestRootCommandRegistersTick(t *testing.T) {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "tick" {
			return
		}
	}
	t.Fatal("root command is missing tick")
}
