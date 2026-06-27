package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/spf13/cobra"

	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

const systemdServiceTmpl = `[Unit]
Description=chroncal service run service
After=default.target

[Service]
Type=oneshot
{{if .SyncInterval}}Environment=CHRONCAL_SYNC_INTERVAL={{.SyncInterval}}
{{end}}{{if .SyncConflictStrategy}}Environment=CHRONCAL_SYNC_CONFLICT_STRATEGY={{.SyncConflictStrategy}}
{{end}}{{if .AllowUnsafeAlarmAudioAttach}}Environment=CHRONCAL_SECURITY_ALLOW_UNSAFE_ALARM_AUDIO_ATTACH={{.AllowUnsafeAlarmAudioAttach}}
{{end}}{{if .AllowUnsafeAlarmEmailAttendees}}Environment=CHRONCAL_SECURITY_ALLOW_UNSAFE_ALARM_EMAIL_ATTENDEES={{.AllowUnsafeAlarmEmailAttendees}}
{{end}}ExecStart={{.BinaryPath}} service run

[Install]
WantedBy=default.target
`

const systemdTimerTmpl = `[Unit]
Description=chroncal alarm timer

[Timer]
OnCalendar=*-*-* *:*:00
Persistent=true

[Install]
WantedBy=timers.target
`

const launchdPlistTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.chroncal.alarm</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.BinaryPath}}</string>
        <string>service</string>
        <string>run</string>
    </array>
    {{if or .SyncInterval .SyncConflictStrategy .AllowUnsafeAlarmAudioAttach .AllowUnsafeAlarmEmailAttendees}}<key>EnvironmentVariables</key>
    <dict>
        {{if .SyncInterval}}<key>CHRONCAL_SYNC_INTERVAL</key>
        <string>{{.SyncInterval}}</string>
        {{end}}{{if .SyncConflictStrategy}}<key>CHRONCAL_SYNC_CONFLICT_STRATEGY</key>
        <string>{{.SyncConflictStrategy}}</string>
        {{end}}{{if .AllowUnsafeAlarmAudioAttach}}<key>CHRONCAL_SECURITY_ALLOW_UNSAFE_ALARM_AUDIO_ATTACH</key>
        <string>{{.AllowUnsafeAlarmAudioAttach}}</string>
        {{end}}{{if .AllowUnsafeAlarmEmailAttendees}}<key>CHRONCAL_SECURITY_ALLOW_UNSAFE_ALARM_EMAIL_ATTENDEES</key>
        <string>{{.AllowUnsafeAlarmEmailAttendees}}</string>
        {{end}}
    </dict>
    {{end}}
    <key>StartInterval</key>
    <integer>60</integer>
    <key>RunAtLoad</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{{.HomeDir}}/Library/Logs/chroncal-alarm.log</string>
    <key>StandardErrorPath</key>
    <string>{{.HomeDir}}/Library/Logs/chroncal-alarm.log</string>
</dict>
</plist>
`

func serviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage alarm notification service",
		Long: `Install or inspect the background service that runs "chroncal service run"
on a schedule.

On Linux this uses user-level systemd units. On macOS it uses a
LaunchAgent. On Windows it registers a per-user Task Scheduler entry.`,
		Example: `  chroncal service install
  chroncal service run
  chroncal service status
  chroncal service uninstall`,
		Args: rejectUnknownSubcommand,
		RunE: groupRunE,
	}
	cmd.AddCommand(serviceInstallCmd(), serviceUninstallCmd(), serviceStatusCmd(), tickCmd())
	return cmd
}

func resolveBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("eval symlinks: %w", err)
	}
	// The path is interpolated into service definitions (unit file, plist,
	// batch wrapper) without per-format escaping; a control character would
	// inject directives. Legal paths never contain them.
	if strings.ContainsAny(resolved, "\n\r") {
		return "", fmt.Errorf("executable path %q contains control characters", resolved)
	}
	return resolved, nil
}

func renderTemplate(tmplStr string, data any) (string, error) {
	t, err := template.New("").Parse(tmplStr)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func serviceInstallCmd() *cobra.Command {
	var syncInterval string
	var flagPolicy alarmExecutionPolicy
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install background alarm and sync service for the current OS",
		Long: `Install the native background service that runs chroncal on a
schedule.

The installed service runs "chroncal service run", which checks alarms every
minute and also runs sync work when the configured sync interval is due.
Without --sync-interval, the [sync] interval from your config is used, falling
back to every 15 minutes when it is unset.`,
		Example: `  chroncal service install
  chroncal service install --sync-interval ""
  chroncal service install --sync-interval 15m`,
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			binPath, err := resolveBinaryPath()
			if err != nil {
				return err
			}
			effectiveSyncInterval := serviceInstallSyncInterval(cmd)
			// The values below are interpolated into service definitions
			// (unit file, plist, batch wrapper). Reject anything that
			// doesn't parse rather than escaping per-format: a config value
			// with a newline must never reach a template.
			if effectiveSyncInterval != "" {
				if _, err := parseCLIDuration("sync-interval", effectiveSyncInterval); err != nil {
					return err
				}
			}
			if s := cfg.Sync.ConflictStrategy; s != "" &&
				s != string(syncPkg.ConflictServerWins) && s != string(syncPkg.ConflictPrompt) {
				return errInvalidInputf("sync.conflict_strategy: invalid value %q (use %q or %q)",
					s, syncPkg.ConflictServerWins, syncPkg.ConflictPrompt)
			}

			data := map[string]string{
				"BinaryPath":                     binPath,
				"SyncInterval":                   effectiveSyncInterval,
				"SyncConflictStrategy":           cfg.Sync.ConflictStrategy,
				"AllowUnsafeAlarmAudioAttach":    "",
				"AllowUnsafeAlarmEmailAttendees": "",
			}
			policy := effectiveAlarmExecutionPolicy(cmd, flagPolicy)
			if policy.AllowUnsafeAudioAttach {
				data["AllowUnsafeAlarmAudioAttach"] = "true"
			}
			if policy.AllowUnsafeEmailAttendees {
				data["AllowUnsafeAlarmEmailAttendees"] = "true"
			}

			switch runtime.GOOS {
			case "linux":
				return installLinuxService(cmd.Context(), w, data)
			case "darwin":
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("get home dir: %w", err)
				}
				data["HomeDir"] = home
				return installDarwinService(cmd.Context(), w, data)
			case "windows":
				return installWindowsService(cmd.Context(), w, data)
			default:
				fmt.Fprintf(w, "No native service integration for %s.\n", runtime.GOOS)
				fmt.Fprintf(w, "Use 'chroncal alarm daemon' to run alarm checks in a loop.\n")
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&syncInterval, "sync-interval", "15m", "how often service run should run sync work (for example 15m); empty disables sync")
	bindAlarmExecutionPolicyFlags(cmd, &flagPolicy)
	return cmd
}

// serviceInstallSyncInterval resolves the sync interval baked into the
// installed service definition. An explicit --sync-interval flag always wins
// (including an explicit empty value, which disables sync). Otherwise the
// configured [sync] interval is used, falling back to the static flag default
// ("15m") when config leaves it unset.
//
// This must be resolved here, at RunE time, rather than as the flag default:
// the command tree is built during package init() — before
// rootCmd.PersistentPreRunE runs config.Load() — so cfg is still the zero
// value when a computed flag default would be evaluated (issue #462).
func serviceInstallSyncInterval(cmd *cobra.Command) string {
	if cmd.Flags().Changed("sync-interval") {
		return cmd.Flag("sync-interval").Value.String()
	}
	if cfg.Sync.Interval != "" {
		return cfg.Sync.Interval
	}
	return cmd.Flag("sync-interval").Value.String()
}

func systemdUserDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "systemd", "user"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

func installLinuxService(ctx context.Context, w interface{ Write([]byte) (int, error) }, data map[string]string) error {
	dir, err := systemdUserDir()
	if err != nil {
		return fmt.Errorf("resolve systemd user dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create systemd dir: %w", err)
	}

	servicePath := filepath.Join(dir, "chroncal-alarm.service")
	timerPath := filepath.Join(dir, "chroncal-alarm.timer")

	serviceContent, err := renderTemplate(systemdServiceTmpl, data)
	if err != nil {
		return fmt.Errorf("render service template: %w", err)
	}

	timerContent, err := renderTemplate(systemdTimerTmpl, data)
	if err != nil {
		return fmt.Errorf("render timer template: %w", err)
	}

	if err := os.WriteFile(servicePath, []byte(serviceContent), 0o644); err != nil {
		return fmt.Errorf("write service file: %w", err)
	}
	fmt.Fprintf(w, "Wrote %s\n", servicePath)

	if err := os.WriteFile(timerPath, []byte(timerContent), 0o644); err != nil {
		return fmt.Errorf("write timer file: %w", err)
	}
	fmt.Fprintf(w, "Wrote %s\n", timerPath)

	if err := exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	fmt.Fprintln(w, "Reloaded systemd user daemon.")

	if err := exec.CommandContext(ctx, "systemctl", "--user", "enable", "--now", "chroncal-alarm.timer").Run(); err != nil {
		return fmt.Errorf("systemctl enable timer: %w", err)
	}
	fmt.Fprintln(w, "Enabled and started chroncal-alarm.timer.")
	return nil
}

func installDarwinService(ctx context.Context, w interface{ Write([]byte) (int, error) }, data map[string]string) error {
	home := data["HomeDir"]
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}

	plistPath := filepath.Join(dir, "com.chroncal.alarm.plist")

	plistContent, err := renderTemplate(launchdPlistTmpl, data)
	if err != nil {
		return fmt.Errorf("render plist template: %w", err)
	}

	if err := os.WriteFile(plistPath, []byte(plistContent), 0o644); err != nil {
		return fmt.Errorf("write plist file: %w", err)
	}
	fmt.Fprintf(w, "Wrote %s\n", plistPath)

	if err := exec.CommandContext(ctx, "launchctl", "load", plistPath).Run(); err != nil {
		return fmt.Errorf("launchctl load: %w", err)
	}
	fmt.Fprintln(w, "Loaded com.chroncal.alarm agent.")
	return nil
}

func serviceUninstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "uninstall",
		Short:   "Uninstall alarm notification service",
		Long:    `Remove the native background service that was installed by "chroncal service install".`,
		Example: `  chroncal service uninstall`,
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()

			switch runtime.GOOS {
			case "linux":
				return uninstallLinuxService(cmd.Context(), w)
			case "darwin":
				return uninstallDarwinService(cmd.Context(), w)
			case "windows":
				return uninstallWindowsService(cmd.Context(), w)
			default:
				fmt.Fprintf(w, "No native service integration for %s.\n", runtime.GOOS)
				return nil
			}
		},
	}
	return cmd
}

func uninstallLinuxService(ctx context.Context, w interface{ Write([]byte) (int, error) }) error {
	dir, err := systemdUserDir()
	if err != nil {
		return fmt.Errorf("resolve systemd user dir: %w", err)
	}
	servicePath := filepath.Join(dir, "chroncal-alarm.service")
	timerPath := filepath.Join(dir, "chroncal-alarm.timer")

	// Stop and disable timer (best-effort).
	_ = exec.CommandContext(ctx, "systemctl", "--user", "disable", "--now", "chroncal-alarm.timer").Run()
	fmt.Fprintln(w, "Disabled chroncal-alarm.timer.")

	if err := os.Remove(timerPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove timer file: %w", err)
	}
	fmt.Fprintf(w, "Removed %s\n", timerPath)

	if err := os.Remove(servicePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove service file: %w", err)
	}
	fmt.Fprintf(w, "Removed %s\n", servicePath)

	_ = exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").Run()
	fmt.Fprintln(w, "Reloaded systemd user daemon.")
	return nil
}

const windowsTaskName = "chroncal-alarm"

// windowsWrapperPath returns the path to the .bat wrapper the Scheduled Task
// executes. We can't pass env vars directly to schtasks, so the wrapper sets
// them before invoking chroncal.
func windowsWrapperPath() (string, error) {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(dir, "chroncal", "chroncal-service.bat"), nil
}

const windowsWrapperTmpl = `@echo off
{{if .SyncInterval}}set CHRONCAL_SYNC_INTERVAL={{.SyncInterval}}
{{end}}{{if .SyncConflictStrategy}}set CHRONCAL_SYNC_CONFLICT_STRATEGY={{.SyncConflictStrategy}}
{{end}}{{if .AllowUnsafeAlarmAudioAttach}}set CHRONCAL_SECURITY_ALLOW_UNSAFE_ALARM_AUDIO_ATTACH={{.AllowUnsafeAlarmAudioAttach}}
{{end}}{{if .AllowUnsafeAlarmEmailAttendees}}set CHRONCAL_SECURITY_ALLOW_UNSAFE_ALARM_EMAIL_ATTENDEES={{.AllowUnsafeAlarmEmailAttendees}}
{{end}}"{{.BinaryPath}}" service run
`

func installWindowsService(ctx context.Context, w interface{ Write([]byte) (int, error) }, data map[string]string) error {
	wrapperPath, err := windowsWrapperPath()
	if err != nil {
		return fmt.Errorf("resolve wrapper path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(wrapperPath), 0o755); err != nil {
		return fmt.Errorf("create wrapper dir: %w", err)
	}

	content, err := renderTemplate(windowsWrapperTmpl, data)
	if err != nil {
		return fmt.Errorf("render wrapper template: %w", err)
	}
	if err := os.WriteFile(wrapperPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write wrapper file: %w", err)
	}
	fmt.Fprintf(w, "Wrote %s\n", wrapperPath)

	// /F overwrites an existing task of the same name. /SC MINUTE /MO 1 fires
	// every minute — matches the systemd timer cadence.
	out, err := exec.CommandContext(ctx, "schtasks",
		"/Create", "/F",
		"/TN", windowsTaskName,
		"/TR", wrapperPath,
		"/SC", "MINUTE",
		"/MO", "1",
	).CombinedOutput()
	if len(out) > 0 {
		fmt.Fprint(w, string(out))
	}
	if err != nil {
		return fmt.Errorf("schtasks create: %w", err)
	}
	fmt.Fprintf(w, "Registered Scheduled Task %q (every minute).\n", windowsTaskName)
	return nil
}

func uninstallWindowsService(ctx context.Context, w interface{ Write([]byte) (int, error) }) error {
	out, err := exec.CommandContext(ctx, "schtasks", "/Delete", "/F", "/TN", windowsTaskName).CombinedOutput()
	if len(out) > 0 {
		fmt.Fprint(w, string(out))
	}
	if err != nil {
		// Best-effort: continue cleanup even if the task didn't exist.
		fmt.Fprintf(w, "schtasks delete: %v\n", err)
	} else {
		fmt.Fprintf(w, "Removed Scheduled Task %q.\n", windowsTaskName)
	}

	wrapperPath, err := windowsWrapperPath()
	if err != nil {
		return fmt.Errorf("resolve wrapper path: %w", err)
	}
	if err := os.Remove(wrapperPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove wrapper file: %w", err)
	}
	fmt.Fprintf(w, "Removed %s\n", wrapperPath)
	return nil
}

func uninstallDarwinService(ctx context.Context, w interface{ Write([]byte) (int, error) }) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}

	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.chroncal.alarm.plist")

	// Unload (best-effort).
	_ = exec.CommandContext(ctx, "launchctl", "unload", plistPath).Run()
	fmt.Fprintln(w, "Unloaded com.chroncal.alarm agent.")

	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist file: %w", err)
	}
	fmt.Fprintf(w, "Removed %s\n", plistPath)
	return nil
}

func serviceStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show alarm service status",
		Long: `Show the native background service status for the current OS.

On Linux this proxies "systemctl --user status chroncal-alarm.timer".
On macOS it proxies "launchctl list com.chroncal.alarm".
On Windows it proxies "schtasks /Query /TN chroncal-alarm /V /FO LIST".`,
		Example: `  chroncal service status`,
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()

			switch runtime.GOOS {
			case "linux":
				out, err := exec.CommandContext(cmd.Context(), "systemctl", "--user", "status", "chroncal-alarm.timer").CombinedOutput()
				if len(out) > 0 {
					fmt.Fprint(w, string(out))
				}
				if err != nil {
					// systemctl returns non-zero for inactive services; only
					// report as an error if there was no output at all.
					if len(out) == 0 {
						return fmt.Errorf("systemctl status: %w", err)
					}
				}
				return nil
			case "darwin":
				out, err := exec.CommandContext(cmd.Context(), "launchctl", "list", "com.chroncal.alarm").CombinedOutput()
				if len(out) > 0 {
					fmt.Fprint(w, string(out))
				}
				if err != nil {
					if len(out) == 0 {
						return fmt.Errorf("launchctl list: %w", err)
					}
				}
				return nil
			case "windows":
				out, err := exec.CommandContext(cmd.Context(), "schtasks", "/Query", "/TN", windowsTaskName, "/V", "/FO", "LIST").CombinedOutput()
				if len(out) > 0 {
					fmt.Fprint(w, string(out))
				}
				if err != nil {
					if len(out) == 0 {
						return fmt.Errorf("schtasks query: %w", err)
					}
				}
				return nil
			default:
				fmt.Fprintf(w, "No native service integration for %s.\n", runtime.GOOS)
				fmt.Fprintf(w, "Use 'chroncal alarm daemon' to run alarm checks in a loop.\n")
				return nil
			}
		},
	}
	return cmd
}
