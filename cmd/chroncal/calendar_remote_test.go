package main

import (
	"testing"
)

func TestReadBasicPasswordUsesEnvVar(t *testing.T) {
	t.Setenv("CHRONCAL_PASSWORD", "lab-secret")
	got, err := readBasicPassword()
	if err != nil {
		t.Fatalf("readBasicPassword: %v", err)
	}
	if got != "lab-secret" {
		t.Fatalf("password = %q, want lab-secret", got)
	}
}

func TestReadBasicPasswordRequiresEnvWhenNonInteractive(t *testing.T) {
	t.Setenv("CHRONCAL_PASSWORD", "")
	if _, err := readBasicPassword(); err == nil {
		t.Fatal("expected error when stdin is not a terminal and env is unset")
	}
}

// The flag wins over every other source. The env command is next. The env
// password comes last before the interactive prompt.
func TestReadBasicSecretSourceOrder(t *testing.T) {
	tests := []struct {
		name        string
		flag        string
		envCommand  string
		envPassword string
		want        basicSecret
	}{
		{
			name:        "the flag wins over the env command",
			flag:        "flag-command",
			envCommand:  "env-command",
			envPassword: "",
			want:        basicSecret{Command: "flag-command"},
		},
		{
			name:        "the env command wins over the env password",
			flag:        "",
			envCommand:  "env-command",
			envPassword: "",
			want:        basicSecret{Command: "env-command"},
		},
		{
			name:        "the env password applies when no command exists",
			flag:        "",
			envCommand:  "",
			envPassword: "lab-secret",
			want:        basicSecret{Password: "lab-secret"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CHRONCAL_PASSWORD_CMD", tc.envCommand)
			t.Setenv("CHRONCAL_PASSWORD", tc.envPassword)
			got, err := readBasicSecret(tc.flag)
			if err != nil {
				t.Fatalf("readBasicSecret: %v", err)
			}
			if got != tc.want {
				t.Fatalf("readBasicSecret = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReadBasicSecretRejectsACommandWithAnEnvPassword(t *testing.T) {
	t.Setenv("CHRONCAL_PASSWORD", "lab-secret")

	t.Setenv("CHRONCAL_PASSWORD_CMD", "")
	if _, err := readBasicSecret("flag-command"); err == nil {
		t.Fatal("the flag command plus CHRONCAL_PASSWORD should fail")
	}

	t.Setenv("CHRONCAL_PASSWORD_CMD", "env-command")
	if _, err := readBasicSecret(""); err == nil {
		t.Fatal("CHRONCAL_PASSWORD_CMD plus CHRONCAL_PASSWORD should fail")
	}
}

func TestBuildCalendarCredentialCarriesThePasswordCommand(t *testing.T) {
	t.Setenv("CHRONCAL_PASSWORD", "")
	t.Setenv("CHRONCAL_PASSWORD_CMD", "")
	cred, err := buildCalendarCredential(t.Context(), calendarRemoteFlags{
		Username: "alice", AuthType: "basic", PasswordCommand: "pass show caldav",
	})
	if err != nil {
		t.Fatalf("buildCalendarCredential: %v", err)
	}
	if cred.PasswordCommand != "pass show caldav" {
		t.Fatalf("PasswordCommand = %q, want %q", cred.PasswordCommand, "pass show caldav")
	}
	if cred.Password != "" {
		t.Fatalf("Password = %q, want empty", cred.Password)
	}
}
