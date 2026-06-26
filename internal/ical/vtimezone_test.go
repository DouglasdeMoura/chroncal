package ical

import "testing"

func TestParseUTCOffset(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{name: "positive hhmm", in: "+0530", want: 5*3600 + 30*60},
		{name: "negative hhmm", in: "-0800", want: -(8 * 3600)},
		{name: "zero", in: "+0000", want: 0},
		{name: "positive with seconds", in: "+005258", want: 52*60 + 58},
		{name: "negative with seconds", in: "-005258", want: -(52*60 + 58)},
		{name: "negative offset with seconds", in: "-103730", want: -(10*3600 + 37*60 + 30)},
		{name: "too short", in: "+05", wantErr: true},
		{name: "bad minutes", in: "+05XY", wantErr: true},
		{name: "bad seconds", in: "+0052ZZ", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseUTCOffset(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseUTCOffset(%q) = %d, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseUTCOffset(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("parseUTCOffset(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
