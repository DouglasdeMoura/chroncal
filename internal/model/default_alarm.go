package model

import (
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/duration"
)

// DefaultAlarm describes one default alarm. The alarm engine synthesizes
// default alarms at check time for events without alarms. They are never
// stored on the event, so they never sync back to the server.
type DefaultAlarm struct {
	Action       string // DISPLAY or AUDIO
	TriggerValue string // RFC 5545 duration relative to the event start
}

// ParseDefaultAlarmSpec parses one default-alarm spec. The spec is either a
// trigger duration ("-PT15M") or an action-prefixed trigger
// ("AUDIO:-PT5M"). The action prefix is optional; DISPLAY is the default.
// Absolute triggers are not valid defaults: one absolute time cannot remind
// for every event.
func ParseDefaultAlarmSpec(spec string) (DefaultAlarm, error) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return DefaultAlarm{}, fmt.Errorf("empty default alarm spec")
	}

	action := "DISPLAY"
	trigger := s
	if i := strings.Index(s, ":"); i >= 0 {
		action = strings.ToUpper(strings.TrimSpace(s[:i]))
		trigger = strings.TrimSpace(s[i+1:])
		switch action {
		case "DISPLAY", "AUDIO":
			// Fireable actions without extra content. EMAIL needs a
			// recipient per event, so it is not a valid default.
		default:
			return DefaultAlarm{}, fmt.Errorf("invalid default alarm action %q (use DISPLAY or AUDIO)", action)
		}
	}

	if trigger == "" {
		return DefaultAlarm{}, fmt.Errorf("default alarm %q has no trigger", spec)
	}
	if err := duration.Validate(trigger); err != nil {
		return DefaultAlarm{}, fmt.Errorf("invalid default alarm trigger %q: %w", trigger, err)
	}
	return DefaultAlarm{Action: action, TriggerValue: trigger}, nil
}

// FormatDefaultAlarmSpec renders a spec in the action-prefixed form when the
// action is not the DISPLAY default. The output round-trips through
// ParseDefaultAlarmSpec.
func FormatDefaultAlarmSpec(a DefaultAlarm) string {
	if a.Action == "" || a.Action == "DISPLAY" {
		return a.TriggerValue
	}
	return a.Action + ":" + a.TriggerValue
}

// ParseDefaultAlarmList parses the comma-separated spec list that the
// calendars.default_alarms column stores. It drops empty entries and reports
// the invalid ones through the second return value so the caller can log
// them. The calendar CLI uses the strict ParseDefaultAlarmSpec instead and
// never writes invalid entries.
func ParseDefaultAlarmList(raw string) ([]DefaultAlarm, []string) {
	var (
		specs   []DefaultAlarm
		invalid []string
	)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		spec, err := ParseDefaultAlarmSpec(part)
		if err != nil {
			invalid = append(invalid, part)
			continue
		}
		specs = append(specs, spec)
	}
	return specs, invalid
}

// FormatDefaultAlarmList renders the specs as the comma-separated list that
// the calendars.default_alarms column stores. The output round-trips through
// ParseDefaultAlarmList.
func FormatDefaultAlarmList(alarms []DefaultAlarm) string {
	parts := make([]string, 0, len(alarms))
	for _, a := range alarms {
		parts = append(parts, FormatDefaultAlarmSpec(a))
	}
	return strings.Join(parts, ",")
}
