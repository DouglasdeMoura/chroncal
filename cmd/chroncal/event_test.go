package main

import "testing"

func TestValidateRRule(t *testing.T) {
	tests := []struct {
		name    string
		rrule   string
		wantErr bool
	}{
		{"empty is ok", "", false},
		{"daily", "FREQ=DAILY", false},
		{"weekly with byday", "FREQ=WEEKLY;BYDAY=MO,WE,FR", false},
		{"monthly with count", "FREQ=MONTHLY;COUNT=12", false},
		{"yearly", "FREQ=YEARLY;BYMONTH=1;BYMONTHDAY=1", false},
		{"secondly", "FREQ=SECONDLY", false},
		{"minutely", "FREQ=MINUTELY", false},
		{"hourly", "FREQ=HOURLY", false},
		{"lowercase freq", "freq=daily", false},
		{"missing FREQ", "BYDAY=MO,WE,FR", true},
		{"garbage", "garbage", true},
		{"invalid freq value", "FREQ=BIWEEKLY", true},
		{"freq empty", "FREQ=", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRRule(tt.rrule)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRRule(%q) error = %v, wantErr %v", tt.rrule, err, tt.wantErr)
			}
		})
	}
}

func TestValidateGeo(t *testing.T) {
	tests := []struct {
		name    string
		geo     string
		wantErr bool
	}{
		{"empty is ok", "", false},
		{"valid coords", "37.386;-122.083", false},
		{"zero zero", "0;0", false},
		{"extremes", "90;180", false},
		{"negative extremes", "-90;-180", false},
		{"missing semicolon", "37.386", true},
		{"non-numeric lat", "abc;-122.083", true},
		{"non-numeric lon", "37.386;xyz", true},
		{"lat too high", "91;0", true},
		{"lat too low", "-91;0", true},
		{"lon too high", "0;181", true},
		{"lon too low", "0;-181", true},
		{"garbage", "not-valid", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGeo(tt.geo)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateGeo(%q) error = %v, wantErr %v", tt.geo, err, tt.wantErr)
			}
		})
	}
}

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"empty is ok", "", false},
		{"https", "https://example.com", false},
		{"http", "http://example.com/path?q=1", false},
		{"ftp", "ftp://files.example.com/doc.pdf", false},
		{"no scheme", "example.com", true},
		{"plain text", "not a url", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestParseRelationFlags(t *testing.T) {
	tests := []struct {
		name      string
		input     []string
		wantTypes []string
		wantUIDs  []string
		wantErr   bool
	}{
		{"bare UID defaults to PARENT", []string{"some-uid"}, []string{"PARENT"}, []string{"some-uid"}, false},
		{"PARENT prefix", []string{"PARENT:parent-uid"}, []string{"PARENT"}, []string{"parent-uid"}, false},
		{"CHILD prefix", []string{"CHILD:child-uid"}, []string{"CHILD"}, []string{"child-uid"}, false},
		{"SIBLING prefix", []string{"SIBLING:sibling-uid"}, []string{"SIBLING"}, []string{"sibling-uid"}, false},
		{"lowercase prefix", []string{"child:uid-123"}, []string{"CHILD"}, []string{"uid-123"}, false},
		{"multiple", []string{"PARENT:a", "CHILD:b"}, []string{"PARENT", "CHILD"}, []string{"a", "b"}, false},
		{"unknown prefix treated as UID", []string{"UNKNOWN:uid"}, []string{"PARENT"}, []string{"UNKNOWN:uid"}, false},
		{"empty UID after prefix", []string{"PARENT:"}, nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRelationFlags(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseRelationFlags(%v) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if len(got) != len(tt.wantTypes) {
				t.Fatalf("got %d relations, want %d", len(got), len(tt.wantTypes))
			}
			for i := range got {
				if got[i].RelType != tt.wantTypes[i] {
					t.Errorf("relation[%d] type: %q, want %q", i, got[i].RelType, tt.wantTypes[i])
				}
				if got[i].RelUID != tt.wantUIDs[i] {
					t.Errorf("relation[%d] uid: %q, want %q", i, got[i].RelUID, tt.wantUIDs[i])
				}
			}
		})
	}
}

func TestParseOrganizerFlag(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantEmail string
		wantName  string
	}{
		{"email only", "alice@example.com", "alice@example.com", ""},
		{"name and email", "Alice <alice@example.com>", "alice@example.com", "Alice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseOrganizerFlag(tt.input)
			if got.Email != tt.wantEmail {
				t.Errorf("email: %q, want %q", got.Email, tt.wantEmail)
			}
			if got.Name != tt.wantName {
				t.Errorf("name: %q, want %q", got.Name, tt.wantName)
			}
			if !got.Organizer {
				t.Error("Organizer should be true")
			}
			if got.Role != "CHAIR" {
				t.Errorf("Role: %q, want CHAIR", got.Role)
			}
			if got.RSVPStatus != "ACCEPTED" {
				t.Errorf("RSVPStatus: %q, want ACCEPTED", got.RSVPStatus)
			}
		})
	}
}

func TestValidateAlarmTrigger(t *testing.T) {
	tests := []struct {
		name    string
		trigger string
		wantErr bool
	}{
		{"15 min before", "-PT15M", false},
		{"1 hour before", "-PT1H", false},
		{"1 day before", "-P1D", false},
		{"complex duration", "-P1DT2H30M", false},
		{"positive duration", "PT15M", false},
		{"absolute datetime", "2026-05-10T14:00:00Z", false},
		{"absolute with offset", "2026-05-10T14:00:00-04:00", false},
		{"empty", "", true},
		{"garbage", "garbage", true},
		{"just P", "P", true},
		{"just minus P", "-P", true},
		{"no P prefix", "T15M", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAlarmTrigger(tt.trigger)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateAlarmTrigger(%q) error = %v, wantErr %v", tt.trigger, err, tt.wantErr)
			}
		})
	}
}
