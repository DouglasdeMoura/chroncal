// Package secretcmd runs a user-supplied command and reads one secret from
// its standard output. It lets a user keep a password in a password manager
// instead of the config file or the OS keyring.
//
// The runner never writes the secret to a log, to an error message, or to a
// terminal. Only the command string is persistent.
package secretcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Timeout is the deadline for one command. A hung command must not wedge a
// sync pass or an alarm delivery.
const Timeout = 30 * time.Second

// waitDelay is the grace period after the deadline. The runner then kills
// the process group and stops the read of the pipes.
const waitDelay = time.Second

// ErrEmptyCommand reports an empty command string.
var ErrEmptyCommand = errors.New("the password command is empty")

// ErrEmptyOutput reports a command that wrote no usable first line.
var ErrEmptyOutput = errors.New("the password command returned no secret")

// Run executes command through the system shell and returns the secret.
//
// The secret is the first line of the standard output. The runner discards
// every later line, because a password manager such as pass prints metadata
// after the password. The runner also discards the standard error, so a
// noisy command cannot corrupt the TUI display.
//
// Run applies a 30-second deadline on top of the deadline of ctx. It returns
// an error for an empty command, for a non-zero exit status, for a timeout,
// and for an empty first line. No error message holds the command output.
func Run(ctx context.Context, command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", ErrEmptyCommand
	}
	runCtx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	name, args := shellInvocation(runtime.GOOS, command)
	cmd := exec.CommandContext(runCtx, name, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// A nil Stderr sends the standard error to the null device. Keep it nil.
	// The TUI owns the terminal, and a write to the standard error prints
	// over the display.
	cmd.Stderr = nil
	// WaitDelay bounds the wait after the deadline. Without it, a grandchild
	// process that holds the pipe open blocks Wait forever.
	cmd.WaitDelay = waitDelay

	err := cmd.Run()
	if err != nil {
		if runCtx.Err() != nil {
			return "", fmt.Errorf("the password command did not finish in %s", Timeout)
		}
		// The error text holds the exit status only. The command output
		// stays out of it.
		return "", fmt.Errorf("the password command failed: %w", err)
	}

	secret := firstLine(stdout.String())
	if strings.TrimSpace(secret) == "" {
		return "", ErrEmptyOutput
	}
	return secret, nil
}

// firstLine returns the first line of out without the line terminator. A
// carriage return at the end of the line is also removed, so a command that
// writes CRLF gives the same secret as a command that writes LF.
func firstLine(out string) string {
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = out[:i]
	}
	return strings.TrimSuffix(out, "\r")
}

// shellInvocation returns the shell program and its arguments for one goos.
// The command runs through a shell on purpose. A user writes a pipeline or a
// path with arguments, for example "pass show caldav_password".
func shellInvocation(goos, command string) (string, []string) {
	if goos == "windows" {
		return "cmd", []string{"/c", command}
	}
	return "sh", []string{"-c", command}
}
