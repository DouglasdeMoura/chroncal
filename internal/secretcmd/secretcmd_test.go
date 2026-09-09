package secretcmd

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the test uses a POSIX shell")
	}
}

func TestRunReturnsFirstStdoutLine(t *testing.T) {
	skipOnWindows(t)
	got, err := Run(t.Context(), "printf 'hunter2\\nlogin: alice\\nurl: example.com\\n'")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got != "hunter2" {
		t.Fatalf("Run = %q, want %q", got, "hunter2")
	}
}

func TestRunAcceptsOutputWithNoTrailingNewline(t *testing.T) {
	skipOnWindows(t)
	got, err := Run(t.Context(), "printf 'hunter2'")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got != "hunter2" {
		t.Fatalf("Run = %q, want %q", got, "hunter2")
	}
}

func TestRunStripsCarriageReturn(t *testing.T) {
	skipOnWindows(t)
	got, err := Run(t.Context(), "printf 'hunter2\\r\\n'")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got != "hunter2" {
		t.Fatalf("Run = %q, want %q", got, "hunter2")
	}
}

func TestRunRejectsAnEmptyCommand(t *testing.T) {
	if _, err := Run(t.Context(), "   "); !errors.Is(err, ErrEmptyCommand) {
		t.Fatalf("Run error = %v, want ErrEmptyCommand", err)
	}
}

func TestRunRejectsEmptyOutput(t *testing.T) {
	skipOnWindows(t)
	if _, err := Run(t.Context(), "true"); !errors.Is(err, ErrEmptyOutput) {
		t.Fatalf("Run error = %v, want ErrEmptyOutput", err)
	}
}

func TestRunRejectsABlankFirstLine(t *testing.T) {
	skipOnWindows(t)
	if _, err := Run(t.Context(), "printf '   \\nhunter2\\n'"); !errors.Is(err, ErrEmptyOutput) {
		t.Fatalf("Run error = %v, want ErrEmptyOutput", err)
	}
}

func TestRunReportsANonZeroExit(t *testing.T) {
	skipOnWindows(t)
	_, err := Run(t.Context(), "exit 3")
	if err == nil {
		t.Fatal("Run returned no error for a non-zero exit")
	}
	if !strings.Contains(err.Error(), "the password command failed") {
		t.Fatalf("Run error = %q, want a command-failed message", err)
	}
}

// The error must not carry the command output. A command that prints the
// secret and then fails must not leak the secret through the error text.
func TestRunKeepsTheOutputOutOfTheError(t *testing.T) {
	skipOnWindows(t)
	_, err := Run(t.Context(), "echo hunter2; echo oops >&2; exit 1")
	if err == nil {
		t.Fatal("Run returned no error for a non-zero exit")
	}
	if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "oops") {
		t.Fatalf("Run error leaks output: %q", err)
	}
}

func TestRunStopsAtTheDeadline(t *testing.T) {
	skipOnWindows(t)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Run(ctx, "sleep 30")
	if err == nil {
		t.Fatal("Run returned no error for a slow command")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run took %s, want a fast stop", elapsed)
	}
	if !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("Run error = %q, want a timeout message", err)
	}
}

func TestShellInvocation(t *testing.T) {
	name, args := shellInvocation("linux", "pass show caldav")
	if name != "sh" || len(args) != 2 || args[0] != "-c" || args[1] != "pass show caldav" {
		t.Fatalf("shellInvocation(linux) = %q %q", name, args)
	}
	name, args = shellInvocation("windows", "pass show caldav")
	if name != "cmd" || len(args) != 2 || args[0] != "/c" || args[1] != "pass show caldav" {
		t.Fatalf("shellInvocation(windows) = %q %q", name, args)
	}
}
