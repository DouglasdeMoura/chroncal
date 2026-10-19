package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"~", home},
		{"~/calendars", filepath.Join(home, "calendars")},
		{"~/calendars/../cal", filepath.Join(home, "cal")},
		{"/abs/dir/", "/abs/dir"},
	}
	for _, tc := range cases {
		got, err := ExpandHome(tc.in)
		if err != nil {
			t.Fatalf("ExpandHome(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ExpandHome(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExpandHomeRelativeIsAbsolute(t *testing.T) {
	got, err := ExpandHome("rel/dir")
	if err != nil {
		t.Fatalf("ExpandHome error: %v", err)
	}
	wd, _ := os.Getwd()
	if want := filepath.Join(wd, "rel", "dir"); got != want {
		t.Errorf("ExpandHome(rel/dir) = %q, want %q", got, want)
	}
}
