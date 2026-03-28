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
