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
