package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"text/template"

	"github.com/spf13/cobra"
)

const systemdServiceTmpl = `[Unit]
Description=chroncal alarm checker
After=default.target

[Service]
Type=oneshot
ExecStart={{.BinaryPath}} alarm check

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
        <string>alarm</string>
        <string>check</string>
    </array>
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
	}
	cmd.AddCommand(serviceInstallCmd(), serviceUninstallCmd(), serviceStatusCmd())
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
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install alarm notification service for the current OS",
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			binPath, err := resolveBinaryPath()
			if err != nil {
				return err
			}

			data := map[string]string{
				"BinaryPath": binPath,
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
			default:
				fmt.Fprintf(w, "No native service integration for %s.\n", runtime.GOOS)
				fmt.Fprintf(w, "Use 'chroncal alarm daemon' to run alarm checks in a loop.\n")
				return nil
			}
		},
	}
	return cmd
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
		Use:   "uninstall",
		Short: "Uninstall alarm notification service",
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()

			switch runtime.GOOS {
			case "linux":
				return uninstallLinuxService(cmd.Context(), w)
			case "darwin":
				return uninstallDarwinService(cmd.Context(), w)
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
			default:
				fmt.Fprintf(w, "No native service integration for %s.\n", runtime.GOOS)
				fmt.Fprintf(w, "Use 'chroncal alarm daemon' to run alarm checks in a loop.\n")
				return nil
			}
		},
	}
	return cmd
}
