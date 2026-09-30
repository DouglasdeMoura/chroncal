package model

import "testing"

func TestParseDefaultAlarmSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    DefaultAlarm
		wantErr bool
	}{
		{name: "bare duration implies display", spec: "-PT15M", want: DefaultAlarm{Action: "DISPLAY", TriggerValue: "-PT15M"}},
		{name: "display prefix", spec: "DISPLAY:-PT30M", want: DefaultAlarm{Action: "DISPLAY", TriggerValue: "-PT30M"}},
		{name: "audio prefix", spec: "AUDIO:-PT5M", want: DefaultAlarm{Action: "AUDIO", TriggerValue: "-PT5M"}},
		{name: "lowercase action", spec: "audio:-PT5M", want: DefaultAlarm{Action: "AUDIO", TriggerValue: "-PT5M"}},
		{name: "spaces trimmed", spec: "  -PT15M  ", want: DefaultAlarm{Action: "DISPLAY", TriggerValue: "-PT15M"}},
		{name: "zero trigger fires at start", spec: "PT0S", want: DefaultAlarm{Action: "DISPLAY", TriggerValue: "PT0S"}},
		{name: "after-start trigger", spec: "PT15M", want: DefaultAlarm{Action: "DISPLAY", TriggerValue: "PT15M"}},
		{name: "day lead", spec: "-P1D", want: DefaultAlarm{Action: "DISPLAY", TriggerValue: "-P1D"}},
		{name: "email rejected", spec: "EMAIL:-PT15M", wantErr: true},
		{name: "unknown action rejected", spec: "BEEP:-PT15M", wantErr: true},
		{name: "absolute trigger rejected", spec: "2026-04-01T10:00:00Z", wantErr: true},
		{name: "malformed duration rejected", spec: "-15m", wantErr: true},
		{name: "empty rejected", spec: "", wantErr: true},
		{name: "blank rejected", spec: "   ", wantErr: true},
		{name: "action without trigger rejected", spec: "AUDIO:", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDefaultAlarmSpec(tt.spec)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseDefaultAlarmSpec(%q) = %+v, want error", tt.spec, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDefaultAlarmSpec(%q) error: %v", tt.spec, err)
			}
			if got != tt.want {
				t.Fatalf("ParseDefaultAlarmSpec(%q) = %+v, want %+v", tt.spec, got, tt.want)
			}
		})
	}
}

func TestDefaultAlarmSpecRoundTrip(t *testing.T) {
	// Format normalizes DISPLAY to the bare form; both parse back the same.
	for _, tt := range []struct{ spec, want string }{
		{spec: "-PT15M", want: "-PT15M"},
		{spec: "AUDIO:-PT5M", want: "AUDIO:-PT5M"},
		{spec: "DISPLAY:-PT30M", want: "-PT30M"},
		{spec: "-P1W", want: "-P1W"},
	} {
		parsed, err := ParseDefaultAlarmSpec(tt.spec)
		if err != nil {
			t.Fatalf("ParseDefaultAlarmSpec(%q) error: %v", tt.spec, err)
		}
		if got := FormatDefaultAlarmSpec(parsed); got != tt.want {
			t.Fatalf("FormatDefaultAlarmSpec(%+v) = %q, want %q", parsed, got, tt.want)
		}
	}
}

func TestParseDefaultAlarmList(t *testing.T) {
	specs, invalid := ParseDefaultAlarmList("-PT15M, AUDIO:-PT5M ,,bad,-PT30M")
	if len(specs) != 3 {
		t.Fatalf("got %d specs, want 3: %+v", len(specs), specs)
	}
	if specs[0].TriggerValue != "-PT15M" || specs[1].Action != "AUDIO" || specs[2].TriggerValue != "-PT30M" {
		t.Fatalf("unexpected specs: %+v", specs)
	}
	if len(invalid) != 1 || invalid[0] != "bad" {
		t.Fatalf("unexpected invalid list: %v", invalid)
	}
	if specs, invalid := ParseDefaultAlarmList(""); len(specs) != 0 || len(invalid) != 0 {
		t.Fatalf("empty list should parse to nothing, got %+v / %v", specs, invalid)
	}
	if got := FormatDefaultAlarmList(specs); got != "-PT15M,AUDIO:-PT5M,-PT30M" {
		t.Fatalf("FormatDefaultAlarmList = %q", got)
	}
}
