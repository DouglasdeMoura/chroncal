package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseFreeBusyTime(t *testing.T) {
	tests := []struct {
		name         string
		flag         string
		input        string
		inclusiveEnd bool
		want         time.Time
	}{
		{
			name:  "date only from",
			flag:  "from",
			input: "2026-04-10",
			want:  time.Date(2026, 4, 10, 0, 0, 0, 0, time.Local),
		},
		{
			name:         "date only to is inclusive end-of-day",
			flag:         "to",
			input:        "2026-04-07",
			inclusiveEnd: true,
			// Half-open range must cover all of Apr 7, so the bound is Apr 8
			// 00:00 — matching parseDateRange used by list/export (issue #137).
			want: time.Date(2026, 4, 8, 0, 0, 0, 0, time.Local),
		},
		{
			name:         "rfc3339 to is unchanged",
			flag:         "to",
			input:        "2026-04-10T09:30:00Z",
			inclusiveEnd: true,
			want:         time.Date(2026, 4, 10, 9, 30, 0, 0, time.UTC),
		},
		{
			name:  "rfc3339 from",
			flag:  "from",
			input: "2026-04-10T09:30:00Z",
			want:  time.Date(2026, 4, 10, 9, 30, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFreeBusyTime(tt.flag, tt.input, tt.inclusiveEnd)
			if err != nil {
				t.Fatalf("parseFreeBusyTime: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("parseFreeBusyTime(%q) = %s, want %s", tt.input, got, tt.want)
			}
		})
	}
}

func TestFreeBusyRejectsInvalidFormat(t *testing.T) {
	setupCalendarCLITestEnv(t)

	_, stderr, err := runChroncalCommand(t,
		"freebusy",
		"--from", "2026-04-01",
		"--to", "2026-04-07",
		"--format", "json",
	)
	if err == nil {
		t.Fatalf("expected error for invalid --format value, got none (stderr=%q)", stderr)
	}
	if !strings.Contains(stderr, "format") {
		t.Fatalf("expected error mentioning format, got: %s", stderr)
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
