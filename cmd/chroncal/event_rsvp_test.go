package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/model"
)

func TestParseRsvpStatus(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "ACCEPTED", want: "ACCEPTED"},
		{in: "yes", want: "ACCEPTED"},
		{in: "Y", want: "ACCEPTED"},
		{in: "declined", want: "DECLINED"},
		{in: "no", want: "DECLINED"},
		{in: "n", want: "DECLINED"},
		{in: "TENTATIVE", want: "TENTATIVE"},
		{in: "maybe", want: "TENTATIVE"},
		{in: " m ", want: "TENTATIVE"},
		{in: "", wantErr: "--status is required"},
		{in: "CONFIRMED", wantErr: "--status must be ACCEPTED, DECLINED, or TENTATIVE"},
	}
	for _, tt := range tests {
		got, err := parseRsvpStatus(tt.in)
		if tt.wantErr != "" {
			if err == nil {
				t.Fatalf("parseRsvpStatus(%q) err = nil, want %q", tt.in, tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("parseRsvpStatus(%q) err = %q, want %q", tt.in, err.Error(), tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseRsvpStatus(%q) err = %v", tt.in, err)
		}
		if got != tt.want {
			t.Fatalf("parseRsvpStatus(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFindUserRsvpAttendee(t *testing.T) {
	attendees := []model.Attendee{
		{Email: "org@example.com", Organizer: true},
		{Email: "me@example.com", Organizer: false},
		{Email: "alice@example.com", Organizer: false},
	}

	idx, err := findUserRsvpAttendee(attendees, "ME@example.com")
	if err != nil {
		t.Fatalf("findUserRsvpAttendee: %v", err)
	}
	if idx != 1 {
		t.Fatalf("findUserRsvpAttendee index = %d, want 1", idx)
	}

	if _, err := findUserRsvpAttendee(attendees, ""); err == nil || !strings.Contains(err.Error(), "no owner email") {
		t.Fatalf("empty owner email err = %v, want no owner email", err)
	}
	if _, err := findUserRsvpAttendee(attendees, "org@example.com"); err == nil || !strings.Contains(err.Error(), "organizer") {
		t.Fatalf("organizer-only err = %v, want organizer", err)
	}
	if _, err := findUserRsvpAttendee(attendees, "nobody@example.com"); err == nil || !strings.Contains(err.Error(), "not an invited attendee") {
		t.Fatalf("missing attendee err = %v, want not invited", err)
	}

	both := []model.Attendee{
		{Email: "me@example.com", Organizer: true},
		{Email: "me@example.com", Organizer: false},
	}
	idx, err = findUserRsvpAttendee(both, "me@example.com")
	if err != nil {
		t.Fatalf("organizer plus attendee: %v", err)
	}
	if idx != 1 {
		t.Fatalf("organizer plus attendee index = %d, want 1", idx)
	}
}

func TestEventRsvpUpdatesOwnerPartstat(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work", "--email", "me@example.com"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	addOut, _, err := runChroncalCommand(t,
		"event", "add", "Sync",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "09:00",
		"--duration", "30m",
		"--attendee", "Me <me@example.com>",
		"--attendee", "Alice <alice@example.com>",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event add: %v", err)
	}
	var created jsonEvent
	if jerr := json.Unmarshal([]byte(addOut), &created); jerr != nil {
		t.Fatalf("decode event add json: %v\n%s", jerr, addOut)
	}

	rsvpOut, _, err := runChroncalCommand(t,
		"event", "rsvp", strconv.FormatInt(created.ID, 10),
		"--status", "yes",
	)
	if err != nil {
		t.Fatalf("event rsvp: %v", err)
	}
	if strings.TrimSpace(rsvpOut) != "RSVP updated to ACCEPTED." {
		t.Fatalf("event rsvp output = %q, want RSVP updated to ACCEPTED.", rsvpOut)
	}

	getOut, _, err := runChroncalCommand(t,
		"event", "get", strconv.FormatInt(created.ID, 10),
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event get: %v", err)
	}
	var got jsonEvent
	if jerr := json.Unmarshal([]byte(getOut), &got); jerr != nil {
		t.Fatalf("decode event get json: %v\n%s", jerr, getOut)
	}
	owner, alice := attendeeByEmail(got.Attendees, "me@example.com"), attendeeByEmail(got.Attendees, "alice@example.com")
	if owner == nil {
		t.Fatalf("owner attendee missing: %s", getOut)
	}
	if owner.RSVPStatus != "ACCEPTED" {
		t.Fatalf("owner rsvp_status = %q, want ACCEPTED", owner.RSVPStatus)
	}
	if alice == nil || alice.RSVPStatus != "NEEDS-ACTION" {
		t.Fatalf("alice rsvp_status = %#v, want NEEDS-ACTION", alice)
	}

	rsvpJSON, _, err := runChroncalCommand(t, "event", "rsvp", created.UID, "--status", "maybe", "--output", "json")
	if err != nil {
		t.Fatalf("event rsvp by uid: %v", err)
	}
	var printed jsonEvent
	if jerr := json.Unmarshal([]byte(rsvpJSON), &printed); jerr != nil {
		t.Fatalf("decode event rsvp json: %v\n%s", jerr, rsvpJSON)
	}
	printedOwner := attendeeByEmail(printed.Attendees, "me@example.com")
	if printedOwner == nil || printedOwner.RSVPStatus != "TENTATIVE" {
		t.Fatalf("rsvp json owner rsvp_status = %#v, want TENTATIVE\n%s", printedOwner, rsvpJSON)
	}

	getOut, _, err = runChroncalCommand(t, "event", "get", created.UID, "--output", "json")
	if err != nil {
		t.Fatalf("event get after maybe: %v", err)
	}
	if jerr := json.Unmarshal([]byte(getOut), &got); jerr != nil {
		t.Fatalf("decode event get after maybe: %v\n%s", jerr, getOut)
	}
	owner = attendeeByEmail(got.Attendees, "me@example.com")
	if owner == nil || owner.RSVPStatus != "TENTATIVE" {
		t.Fatalf("owner rsvp_status after maybe = %#v, want TENTATIVE", owner)
	}
}

func TestEventRsvpRefusesInvalidCases(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work", "--email", "me@example.com"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t, "calendar", "create", "NoEmail"); err != nil {
		t.Fatalf("calendar create NoEmail: %v", err)
	}

	invited, _, err := runChroncalCommand(t,
		"event", "add", "Invited",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "09:00",
		"--attendee", "Me <me@example.com>",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event add invited: %v", err)
	}
	organizerOnly, _, err := runChroncalCommand(t,
		"event", "add", "Hosted",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "10:00",
		"--organizer", "Me <me@example.com>",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event add hosted: %v", err)
	}
	otherGuest, _, err := runChroncalCommand(t,
		"event", "add", "Other",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "11:00",
		"--attendee", "Alice <alice@example.com>",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event add other: %v", err)
	}
	noEmail, _, err := runChroncalCommand(t,
		"event", "add", "Local",
		"--calendar", "NoEmail",
		"--date", "2026-04-21",
		"--time", "12:00",
		"--attendee", "Me <me@example.com>",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event add no email: %v", err)
	}

	invitedID := mustJSONEvent(t, invited).ID
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing status",
			args:    []string{"event", "rsvp", strconv.FormatInt(invitedID, 10)},
			wantErr: "--status is required",
		},
		{
			name:    "invalid status",
			args:    []string{"event", "rsvp", strconv.FormatInt(invitedID, 10), "--status", "CONFIRMED"},
			wantErr: "--status must be ACCEPTED, DECLINED, or TENTATIVE",
		},
		{
			name:    "organizer only",
			args:    []string{"event", "rsvp", strconv.FormatInt(mustJSONEvent(t, organizerOnly).ID, 10), "--status", "ACCEPTED"},
			wantErr: "you are the organizer of this event",
		},
		{
			name:    "not invited",
			args:    []string{"event", "rsvp", strconv.FormatInt(mustJSONEvent(t, otherGuest).ID, 10), "--status", "ACCEPTED"},
			wantErr: "you are not an invited attendee of this event",
		},
		{
			name:    "no owner email",
			args:    []string{"event", "rsvp", strconv.FormatInt(mustJSONEvent(t, noEmail).ID, 10), "--status", "ACCEPTED"},
			wantErr: "calendar has no owner email; set it with calendar update --email",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runChroncalCommand(t, tt.args...)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
			if strings.Contains(err.Error(), "update attendees:") {
				t.Fatalf("error = %q, should not leak wrap prefix", err.Error())
			}
		})
	}
}

func mustJSONEvent(t *testing.T, stdout string) jsonEvent {
	t.Helper()
	var ev jsonEvent
	if err := json.Unmarshal([]byte(stdout), &ev); err != nil {
		t.Fatalf("decode event json: %v\n%s", err, stdout)
	}
	return ev
}

func attendeeByEmail(attendees []jsonAttendee, email string) *jsonAttendee {
	for i := range attendees {
		if strings.EqualFold(attendees[i].Email, email) {
			return &attendees[i]
		}
	}
	return nil
}
