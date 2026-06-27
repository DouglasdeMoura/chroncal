package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/config"
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

func TestServiceInstallDefaultsSyncIntervalTo15Minutes(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	cfg = config.Config{}

	cmd := serviceInstallCmd()
	flag := cmd.Flags().Lookup("sync-interval")
	if flag == nil {
		t.Fatal("service install missing sync-interval flag")
	}
	if got := flag.DefValue; got != "15m" {
		t.Fatalf("sync-interval default = %q, want 15m", got)
	}
	if got := flag.Value.String(); got != "15m" {
		t.Fatalf("sync-interval value = %q, want 15m", got)
	}
}

// registeredServiceInstallCmd returns the install subcommand as it was wired
// into rootCmd at package init(), i.e. the production code path — not a fresh
// serviceInstallCmd() built after cfg has been populated.
func registeredServiceInstallCmd(t *testing.T) *cobra.Command {
	t.Helper()
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() != "service" {
			continue
		}
		for _, sub := range cmd.Commands() {
			if sub.Name() == "install" {
				return sub
			}
		}
	}
	t.Fatal("service install command not registered on rootCmd")
	return nil
}

// TestServiceInstallRespectsConfiguredSyncIntervalAtRunTime exercises the
// init-time-registered command (as production does) to prove the configured
// [sync] interval is honored even though the flag tree is built before
// config.Load() runs. Regression test for issue #462.
func TestServiceInstallRespectsConfiguredSyncIntervalAtRunTime(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	cfg = config.Config{Sync: config.SyncConfig{Interval: "30m"}}

	install := registeredServiceInstallCmd(t)
	if got := serviceInstallSyncInterval(install); got != "30m" {
		t.Fatalf("effective sync interval = %q, want 30m (config must win when --sync-interval unset)", got)
	}
}

func TestServiceInstallSyncIntervalFlagOverridesConfig(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	cfg = config.Config{Sync: config.SyncConfig{Interval: "30m"}}

	cmd := serviceInstallCmd()
	if err := cmd.Flags().Set("sync-interval", "5m"); err != nil {
		t.Fatalf("set sync-interval: %v", err)
	}
	if got := serviceInstallSyncInterval(cmd); got != "5m" {
		t.Fatalf("effective sync interval = %q, want 5m (explicit flag must win)", got)
	}
}

func TestServiceInstallSyncIntervalFlagCanDisableSync(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	cfg = config.Config{Sync: config.SyncConfig{Interval: "30m"}}

	cmd := serviceInstallCmd()
	if err := cmd.Flags().Set("sync-interval", ""); err != nil {
		t.Fatalf("set sync-interval: %v", err)
	}
	if got := serviceInstallSyncInterval(cmd); got != "" {
		t.Fatalf("effective sync interval = %q, want empty (explicit --sync-interval \"\" disables sync)", got)
	}
}

func TestServiceInstallSyncIntervalFallsBackToStaticDefault(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	cfg = config.Config{}

	cmd := serviceInstallCmd()
	if got := serviceInstallSyncInterval(cmd); got != "15m" {
		t.Fatalf("effective sync interval = %q, want 15m (static default with empty config)", got)
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
