package fsutil

import (
	"errors"
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

func TestContractHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := map[string]string{
		"":                                 "",
		"/srv/calendars":                   "/srv/calendars",
		filepath.Join(home, "cal", "work"): "~/cal/work",
		home:                               "~",
		home + "-other":                    home + "-other",
	}
	for in, want := range cases {
		if got := ContractHome(in); got != want {
			t.Errorf("ContractHome(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExpandHomeRejectsOtherUserHome(t *testing.T) {
	if _, err := ExpandHome("~alice/calendars"); !errors.Is(err, ErrOtherUserHome) {
		t.Fatalf("ExpandHome(~alice/...) error = %v, want ErrOtherUserHome", err)
	}
}

func TestContractHomeWithTrailingSeparatorInHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home+string(os.PathSeparator))
	if got := ContractHome(filepath.Join(home, "calendars")); got != "~/calendars" {
		t.Errorf("ContractHome with a trailing separator in HOME = %q, want ~/calendars", got)
	}
}

func TestSameTree(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"/srv/cal", "/srv/cal", true},
		{"/srv/cal", "/srv/cal/work", true},
		{"/srv/cal/work", "/srv/cal", true},
		{"/srv/cal", "/srv/calendar", false},
		{"", "/srv/cal", false},
	}
	for _, tc := range cases {
		if got := SameTree(tc.a, tc.b); got != tc.want {
			t.Errorf("SameTree(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
