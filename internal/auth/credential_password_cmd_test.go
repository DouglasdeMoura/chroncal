package auth

import (
	"errors"
	"runtime"
	"testing"
)

func TestCredential_ResolvePassword_Literal(t *testing.T) {
	got, err := (Credential{Password: "secret123"}).ResolvePassword()
	if err != nil {
		t.Fatalf("ResolvePassword() error = %v", err)
	}
	if got != "secret123" {
		t.Errorf("ResolvePassword() = %q, want %q", got, "secret123")
	}
}

func TestCredential_ResolvePassword_CommandKeepsFirstLine(t *testing.T) {
	command := "printf 'from-cmd\\nmetadata\\n'"
	if runtime.GOOS == "windows" {
		command = "echo from-cmd"
	}
	got, err := (Credential{PasswordCommand: command}).ResolvePassword()
	if err != nil {
		t.Fatalf("ResolvePassword() error = %v", err)
	}
	if got != "from-cmd" {
		t.Errorf("ResolvePassword() = %q, want %q", got, "from-cmd")
	}
}

func TestCredential_ResolvePassword_BothSetIsError(t *testing.T) {
	_, err := (Credential{Password: "literal", PasswordCommand: "echo from-cmd"}).ResolvePassword()
	if err == nil {
		t.Fatal("ResolvePassword() error = nil, want an error when both password sources are set")
	}
	if !errors.Is(err, errPasswordConflict) {
		t.Errorf("ResolvePassword() error = %v, want the mutual-exclusion error", err)
	}
}

func TestCredential_ResolvePassword_CommandError(t *testing.T) {
	command := "exit 3"
	if runtime.GOOS == "windows" {
		command = "exit /b 3"
	}
	if _, err := (Credential{PasswordCommand: command}).ResolvePassword(); err == nil {
		t.Fatal("ResolvePassword() error = nil, want an error for a failing password command")
	}
}
