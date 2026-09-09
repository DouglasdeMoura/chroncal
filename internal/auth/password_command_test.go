package auth

import (
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
)

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the test uses a POSIX shell")
	}
}

func TestResolvePasswordReturnsTheStoredPassword(t *testing.T) {
	cred := Credential{Username: "alice", Password: "secret123"}
	got, err := cred.ResolvePassword(t.Context())
	if err != nil {
		t.Fatalf("ResolvePassword() error = %v", err)
	}
	if got != "secret123" {
		t.Errorf("ResolvePassword() = %q, want %q", got, "secret123")
	}
}

func TestResolvePasswordRunsTheCommand(t *testing.T) {
	skipOnWindows(t)
	cred := Credential{Username: "alice", PasswordCommand: `printf 'from-command\nlogin: alice\n'`}
	got, err := cred.ResolvePassword(t.Context())
	if err != nil {
		t.Fatalf("ResolvePassword() error = %v", err)
	}
	if got != "from-command" {
		t.Errorf("ResolvePassword() = %q, want %q", got, "from-command")
	}
}

func TestResolvePasswordRejectsBothSources(t *testing.T) {
	cred := Credential{Password: "secret123", PasswordCommand: "true"}
	if _, err := cred.ResolvePassword(t.Context()); !errors.Is(err, ErrPasswordSourceConflict) {
		t.Fatalf("ResolvePassword() error = %v, want ErrPasswordSourceConflict", err)
	}
	if err := cred.ValidatePasswordSources(); !errors.Is(err, ErrPasswordSourceConflict) {
		t.Fatalf("ValidatePasswordSources() error = %v, want ErrPasswordSourceConflict", err)
	}
}

func TestResolvePasswordReportsACommandFailure(t *testing.T) {
	skipOnWindows(t)
	cred := Credential{PasswordCommand: "echo leaked-secret; exit 1"}
	_, err := cred.ResolvePassword(t.Context())
	if err == nil {
		t.Fatal("ResolvePassword() returned no error for a failed command")
	}
	if strings.Contains(err.Error(), "leaked-secret") {
		t.Fatalf("ResolvePassword() error leaks the output: %q", err)
	}
}

// The credential store keeps the command. It must never keep the resolved
// secret, so the marshalled JSON holds password_cmd and no password.
func TestCredentialJSONKeepsTheCommandOnly(t *testing.T) {
	skipOnWindows(t)
	cred := Credential{AccountID: 1, Username: "alice", PasswordCommand: "echo secret123"}
	if _, err := cred.ResolvePassword(t.Context()); err != nil {
		t.Fatalf("ResolvePassword() error = %v", err)
	}
	data, err := json.Marshal(cred)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"password_cmd":"echo secret123"`) {
		t.Errorf("credential JSON = %s, want a password_cmd field", text)
	}
	if strings.Contains(text, `"password"`) {
		t.Errorf("credential JSON = %s, want no password field", text)
	}
}

// Every credential store refuses a credential that holds both sources. The
// check lives in Set, so no write path can store the conflicting pair.
func TestPlaintextStoreSetRejectsBothPasswordSources(t *testing.T) {
	store := &PlaintextFileStore{dir: t.TempDir(), namespace: "test", warn: io.Discard}
	err := store.Set(Credential{AccountID: 1, Password: "secret123", PasswordCommand: "true"})
	if !errors.Is(err, ErrPasswordSourceConflict) {
		t.Fatalf("Set() error = %v, want ErrPasswordSourceConflict", err)
	}
}

func TestHasPasswordCommandIgnoresBlankSpace(t *testing.T) {
	if (Credential{PasswordCommand: "   "}).HasPasswordCommand() {
		t.Error("HasPasswordCommand() = true for a blank command, want false")
	}
	if !(Credential{PasswordCommand: "true"}).HasPasswordCommand() {
		t.Error("HasPasswordCommand() = false for a real command, want true")
	}
}
