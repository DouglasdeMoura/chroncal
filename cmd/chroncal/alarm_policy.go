package main

import (
	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/notify"
)

type alarmExecutionPolicy struct {
	AllowUnsafeAudioAttach    bool
	AllowUnsafeEmailAttendees bool
}

func bindAlarmExecutionPolicyFlags(cmd *cobra.Command, policy *alarmExecutionPolicy) {
	cmd.Flags().BoolVar(&policy.AllowUnsafeAudioAttach, "allow-unsafe-audio-attach", false, "allow AUDIO alarms to play calendar-supplied local file attachment paths")
	cmd.Flags().BoolVar(&policy.AllowUnsafeEmailAttendees, "allow-unsafe-email-attendees", false, "allow EMAIL alarms to deliver to attendees stored in calendar data")
}

func effectiveAlarmExecutionPolicy(cmd *cobra.Command, flagPolicy alarmExecutionPolicy) alarmExecutionPolicy {
	policy := alarmExecutionPolicy{
		AllowUnsafeAudioAttach:    cfg.Security.AllowUnsafeAlarmAudioAttach,
		AllowUnsafeEmailAttendees: cfg.Security.AllowUnsafeAlarmEmailAttendees,
	}
	if cmd.Flags().Changed("allow-unsafe-audio-attach") {
		policy.AllowUnsafeAudioAttach = flagPolicy.AllowUnsafeAudioAttach
	}
	if cmd.Flags().Changed("allow-unsafe-email-attendees") {
		policy.AllowUnsafeEmailAttendees = flagPolicy.AllowUnsafeEmailAttendees
	}
	return policy
}

func (p alarmExecutionPolicy) notifyPolicy() notify.ExecutionPolicy {
	return notify.ExecutionPolicy{
		AllowUnsafeAudioAttach:    p.AllowUnsafeAudioAttach,
		AllowUnsafeEmailAttendees: p.AllowUnsafeEmailAttendees,
	}
}
