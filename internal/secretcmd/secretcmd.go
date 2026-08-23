package secretcmd

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// DefaultTimeout bounds how long a password command may run. A helper that
// blocks must not stop an alarm email or a CalDAV request forever.
const DefaultTimeout = 30 * time.Second

// timeout holds the active bound. A test sets it to a shorter value.
var timeout = DefaultTimeout

// Run executes command through the system shell and returns the secret.
// The secret is the first line of stdout. A trailing CR on that line is
// removed. Later lines are ignored. Helper programs such as pass print
// metadata after the secret.
//
// The command has a deadline of timeout. Stderr is discarded so a helper
// cannot write a secret to the journal.
func Run(command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("password command is empty")
	}
	shell, flag := "sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/c"
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, flag, command)
	// A killed shell can leave a helper child that holds the output pipe.
	// WaitDelay bounds that wait, so the deadline holds even when the
	// shell forked its command instead of replacing itself.
	cmd.WaitDelay = time.Second
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("run password command %q: %w", command, err)
	}
	secret := firstLine(string(out))
	if secret == "" {
		return "", fmt.Errorf("password command %q returned an empty secret", command)
	}
	return secret, nil
}

func firstLine(out string) string {
	line, _, _ := strings.Cut(out, "\n")
	return strings.TrimSuffix(line, "\r")
}
