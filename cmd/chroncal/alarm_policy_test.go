package main

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/config"
)

func TestEffectiveAlarmExecutionPolicy_UsesConfigDefaults(t *testing.T) {
	oldCfg := cfg
	cfg = config.Config{
		Security: config.SecurityConfig{
			AllowUnsafeAlarmAudioAttach:    true,
			AllowUnsafeAlarmEmailAttendees: true,
		},
	}
	t.Cleanup(func() { cfg = oldCfg })

	cmd := &cobra.Command{Use: "test"}
	var flags alarmExecutionPolicy
	bindAlarmExecutionPolicyFlags(cmd, &flags)

	got := effectiveAlarmExecutionPolicy(cmd, flags)
	if !got.AllowUnsafeAudioAttach {
		t.Fatal("AllowUnsafeAudioAttach = false, want true")
	}
	if !got.AllowUnsafeEmailAttendees {
		t.Fatal("AllowUnsafeEmailAttendees = false, want true")
	}
}

func TestEffectiveAlarmExecutionPolicy_FlagsOverrideConfig(t *testing.T) {
	oldCfg := cfg
	cfg = config.Config{}
	t.Cleanup(func() { cfg = oldCfg })

	cmd := &cobra.Command{Use: "test"}
	var flags alarmExecutionPolicy
	bindAlarmExecutionPolicyFlags(cmd, &flags)
	if err := cmd.ParseFlags([]string{"--allow-unsafe-audio-attach", "--allow-unsafe-email-attendees"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}

	got := effectiveAlarmExecutionPolicy(cmd, flags)
	if !got.AllowUnsafeAudioAttach {
		t.Fatal("AllowUnsafeAudioAttach = false, want true")
	}
	if !got.AllowUnsafeEmailAttendees {
		t.Fatal("AllowUnsafeEmailAttendees = false, want true")
	}
}
