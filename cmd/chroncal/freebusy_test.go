package main

import (
	"testing"
	"time"
)

func TestParseFreeBusyTime(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{
			name:  "date only",
			input: "2026-04-10",
			want:  time.Date(2026, 4, 10, 0, 0, 0, 0, time.Local),
		},
		{
			name:  "rfc3339",
			input: "2026-04-10T09:30:00Z",
			want:  time.Date(2026, 4, 10, 9, 30, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFreeBusyTime(tt.input)
			if err != nil {
				t.Fatalf("parseFreeBusyTime: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("parseFreeBusyTime(%q) = %s, want %s", tt.input, got, tt.want)
			}
		})
	}
}

func TestRootCommandRegistersFreeBusy(t *testing.T) {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "freebusy" {
			return
		}
	}
	t.Fatal("root command is missing freebusy")
}
