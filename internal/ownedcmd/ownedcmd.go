// Package ownedcmd runs short-lived external tools as owned process groups.
//
// Callers retain domain-specific error classification. This package guarantees
// context-first admission, SIGTERM-to-SIGKILL cancellation, and a best-effort
// bounded cleanup of descendants even when the direct child exits first.
package ownedcmd

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

const DefaultTerminationGrace = 250 * time.Millisecond

// ErrUnsupported reports that the host cannot provide owned process-group
// execution. Callers must fail closed rather than silently using a weaker exec.
var ErrUnsupported = errors.New("owned process-group execution is unsupported")

// Output runs name with args and returns stdout. Stderr is retained in the
// returned exec.ExitError exactly as os/exec.Output does.
func Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return run(ctx, false, name, args...)
}

// CombinedOutput runs name with args and returns interleaved stdout and stderr.
func CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return run(ctx, true, name, args...)
}

// Run executes a fully configured command as an owned process group. The caller
// may set Dir, Env, Stdin, Stdout, and Stderr before calling Run; command identity
// and arguments must already be present on cmd. This is the streaming counterpart
// to Output and CombinedOutput.
func Run(ctx context.Context, cmd *exec.Cmd) error {
	if ctx == nil {
		return errors.New("owned command requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if cmd == nil || cmd.Path == "" {
		return errors.New("owned command is not configured")
	}
	if cmd.Cancel == nil {
		return errors.New("owned command must be constructed with exec.CommandContext")
	}
	return runCommand(ctx, cmd)
}

func run(ctx context.Context, combined bool, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("owned command requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if combined {
		cmd.Stdout = &stdout
		cmd.Stderr = &stdout
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}
	err := runCommand(ctx, cmd)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && !combined {
			exitErr.Stderr = append([]byte(nil), stderr.Bytes()...)
		}
		return stdout.Bytes(), err
	}
	return stdout.Bytes(), nil
}

func runCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := configureCommand(cmd, DefaultTerminationGrace); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	pid := cmd.Process.Pid
	waitErr := cmd.Wait()
	cleanupErr := cleanupCommandGroup(pid, DefaultTerminationGrace)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if waitErr != nil {
		return waitErr
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	return nil
}
