package secretcmd

import (
	"runtime"
	"testing"
	"time"
)

func TestRun_KeepsFirstLine(t *testing.T) {
	command := "printf 'from-cmd\\nmetadata\\n'"
	if runtime.GOOS == "windows" {
		command = "echo from-cmd"
	}
	got, err := Run(command)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got != "from-cmd" {
		t.Errorf("Run() = %q, want %q", got, "from-cmd")
	}
}

func TestRun_DiscardsStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX redirection")
	}
	got, err := Run("printf 'from-cmd\\n'; printf 'noise\\n' >&2")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got != "from-cmd" {
		t.Errorf("Run() = %q, want %q", got, "from-cmd")
	}
}

func TestRun_CommandError(t *testing.T) {
	command := "exit 3"
	if runtime.GOOS == "windows" {
		command = "exit /b 3"
	}
	if _, err := Run(command); err == nil {
		t.Fatal("Run() error = nil, want an error for a failing command")
	}
}

func TestRun_MissingCommand(t *testing.T) {
	if _, err := Run("chroncal-definitely-not-a-real-binary-1234"); err == nil {
		t.Fatal("Run() error = nil, want an error for a missing command")
	}
}

func TestRun_EmptyCommand(t *testing.T) {
	if _, err := Run("   "); err == nil {
		t.Fatal("Run() error = nil, want an error for an empty command")
	}
}

func TestRun_EmptySecret(t *testing.T) {
	command := "printf '\\n'"
	if runtime.GOOS == "windows" {
		command = "echo."
	}
	if _, err := Run(command); err == nil {
		t.Fatal("Run() error = nil, want an error for an empty secret")
	}
}

// TestRunTimeout checks that a helper which blocks cannot hold the caller
// forever. The test shortens timeout, so it stays fast and deterministic.
func TestRunTimeout(t *testing.T) {
	oldTimeout := timeout
	timeout = 200 * time.Millisecond
	t.Cleanup(func() { timeout = oldTimeout })

	// The background job forces the shell to fork instead of replace
	// itself, so the killed shell leaves a helper child behind. That child
	// holds the output pipe, which is the exact shape the deadline must
	// survive (it hung the ubuntu CI runner once). The Windows variant has
	// no pipe-holding grandchild; it exercises the plain deadline only.
	command := "sleep 60 & wait"
	if runtime.GOOS == "windows" {
		command = "ping -n 60 127.0.0.1"
	}

	start := time.Now()
	_, err := Run(command)
	if err == nil {
		t.Fatal("Run error = nil, want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run took %v, want a return before the command ends", elapsed)
	}
}
