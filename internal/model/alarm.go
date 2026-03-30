package model

import (
	"sort"
	"strings"
)

type Alarm struct {
	ID           int64
	EventID      int64
	UID          string // globally unique per RFC 9074
	Action       string // DISPLAY, EMAIL, AUDIO
	TriggerValue string // e.g. "-PT15M" or absolute RFC 3339
	Description  string
	Summary      string // RFC 5545 SUMMARY (required for EMAIL action)
	Repeat       int    // number of additional repetitions
	Duration     string // repeat interval (RFC 5545 duration, e.g. PT5M)
	Related      string // trigger anchor: START or END
	Attendees    []AlarmAttendee
}

// ContentEqual returns true if two alarms have identical content (all fields
// except ID, EventID, and UID). Used by ReplaceAlarms to match incoming
// alarms against existing ones for merge-based updates.
func (a Alarm) ContentEqual(b Alarm) bool {
	if !strings.EqualFold(a.Action, b.Action) {
		return false
	}
	if !strings.EqualFold(a.TriggerValue, b.TriggerValue) {
		return false
	}
	if !strings.EqualFold(a.Related, b.Related) {
		return false
	}
	if a.Description != b.Description || a.Summary != b.Summary {
		return false
	}
	if a.Repeat != b.Repeat || a.Duration != b.Duration {
		return false
	}
	return attendeesEqual(a.Attendees, b.Attendees)
}

func attendeesEqual(a, b []AlarmAttendee) bool {
	if len(a) != len(b) {
		return false
	}
	ae := sortedEmails(a)
	be := sortedEmails(b)
	for i := range ae {
		if ae[i] != be[i] {
			return false
		}
	}
	return true
}

func sortedEmails(atts []AlarmAttendee) []string {
	emails := make([]string, len(atts))
	for i, a := range atts {
		emails[i] = strings.ToLower(a.Email)
	}
	sort.Strings(emails)
	return emails
}

type AlarmAttendee struct {
	ID    int64
	Email string
	Name  string
}
