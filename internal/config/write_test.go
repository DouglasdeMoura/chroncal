package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// configHome points the config directory at a temporary directory and
// returns the path of the config file that Load reads.
func configHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("CHRONCAL_SECURITY_ALLOW_PLAINTEXT", "")
	os.Unsetenv("CHRONCAL_SECURITY_ALLOW_PLAINTEXT")
	return filepath.Join(dir, "chroncal", "config.toml")
}

func TestEnableAllowPlaintextCreatesTheFile(t *testing.T) {
	path := configHome(t)

	written, err := EnableAllowPlaintext()
	if err != nil {
		t.Fatalf("EnableAllowPlaintext: %v", err)
	}
	if written != path {
		t.Fatalf("written path = %q, want %q", written, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the new config file: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Security.AllowPlaintext {
		t.Fatal("Load did not read security.allow_plaintext = true")
	}
}

func TestEnableAllowPlaintextKeepsTheRestOfTheFile(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no security table",
			in:   "# my settings\ndb = \"/tmp/x.db\"\n\n[ui]\ntheme = \"nord\" # dark\n",
			want: "# my settings\ndb = \"/tmp/x.db\"\n\n[ui]\ntheme = \"nord\" # dark\n\n[security]\nallow_plaintext = true\n",
		},
		{
			name: "security table without the key",
			in:   "[security]\nallow_unsafe_alarm_audio_attach = true\n\n[ui]\ntheme = \"nord\"\n",
			want: "[security]\nallow_plaintext = true\nallow_unsafe_alarm_audio_attach = true\n\n[ui]\ntheme = \"nord\"\n",
		},
		{
			name: "key set to false",
			in:   "[security] # creds\n  allow_plaintext = false # off\n[ui]\ntheme = \"nord\"\n",
			want: "[security] # creds\n  allow_plaintext = true # off\n[ui]\ntheme = \"nord\"\n",
		},
		{
			name: "dotted key at the top level",
			in:   "security.allow_plaintext = false\ndb = \"/tmp/x.db\"\n",
			want: "security.allow_plaintext = true\ndb = \"/tmp/x.db\"\n",
		},
		{
			name: "other dotted security key at the top level",
			in:   "security.allow_unsafe_alarm_audio_attach = true\ndb = \"/tmp/x.db\"\n\n[ui]\ntheme = \"nord\"\n",
			want: "security.allow_unsafe_alarm_audio_attach = true\nsecurity.allow_plaintext = true\ndb = \"/tmp/x.db\"\n\n[ui]\ntheme = \"nord\"\n",
		},
		{
			name: "no trailing newline",
			in:   "db = \"/tmp/x.db\"",
			want: "db = \"/tmp/x.db\"\n\n[security]\nallow_plaintext = true\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := configHome(t)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.in), 0o640); err != nil {
				t.Fatal(err)
			}
			if _, err := EnableAllowPlaintext(); err != nil {
				t.Fatalf("EnableAllowPlaintext: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("file =\n%s\nwant\n%s", got, tc.want)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
				t.Fatalf("mode = %v, want the old mode 0640", info.Mode().Perm())
			}
		})
	}
}

// TestEnableAllowPlaintextRefusesAFileItCannotEditSafely keeps a file that the
// text edit cannot change with certainty. The function must not write a file
// that changes another key or that does not parse.
func TestEnableAllowPlaintextRefusesAFileItCannotEditSafely(t *testing.T) {
	cases := map[string]string{
		"inline table":   "security = { allow_unsafe_alarm_audio_attach = true }\n",
		"malformed file": "[security\nallow_plaintext = false\n",
		"string value":   "[security]\nallow_plaintext = \"no\"\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			path := configHome(t)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := EnableAllowPlaintext(); err == nil {
				t.Fatal("EnableAllowPlaintext accepted a file it cannot edit safely")
			} else if !strings.Contains(err.Error(), "allow_plaintext") {
				t.Fatalf("error %q does not name the key", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != in {
				t.Fatalf("the refused edit changed the file:\n%s", got)
			}
		})
	}
}

// TestCheckAllowPlaintextEnv matches the bool rule of Load: an empty value
// is ignored, a true value is accepted, and a false value turns plaintext
// storage off. Load fails on a value that does not parse as a bool, so the
// error must say that the value is not a bool.
func TestCheckAllowPlaintextEnv(t *testing.T) {
	const off, notBool = "turns plaintext storage off", "is not a bool"
	for _, tc := range []struct {
		value string
		// wantMsg is the reason that a refusal names. An empty value means
		// no refusal.
		wantMsg string
	}{
		{"", ""},
		{"true", ""},
		{"1", ""},
		{"false", off},
		{"0", off},
		{"no", notBool},
		{"maybe", notBool},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv(allowPlaintextEnv, tc.value)
			err := CheckAllowPlaintextEnv()
			if tc.wantMsg == "" {
				if err != nil {
					t.Fatalf("CheckAllowPlaintextEnv(%q) = %v, want nil", tc.value, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) || !strings.Contains(err.Error(), "unset "+allowPlaintextEnv) {
				t.Fatalf("CheckAllowPlaintextEnv(%q) = %v, want %q and the remedy", tc.value, err, tc.wantMsg)
			}
		})
	}
}

// TestEnableAllowPlaintextKeepsADanglingSymlink keeps a config symlink whose
// target is not there. A write through the link path replaces the link with
// a regular file, and the user loses the link to the dotfiles target with
// no warning. The function must refuse and keep the link.
func TestEnableAllowPlaintextKeepsADanglingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need extra rights on Windows")
	}
	path := configHome(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "dotfiles", "chroncal.toml")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := EnableAllowPlaintext(); err == nil {
		t.Fatal("EnableAllowPlaintext wrote through a dangling symlink")
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config path mode = %v, want the symlink kept", info.Mode())
	}
}
