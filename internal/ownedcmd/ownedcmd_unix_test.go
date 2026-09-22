//go:build linux || darwin

package ownedcmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandStartFailurePreservesPathError(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(context.Context, string) error
	}{
		{"run", func(ctx context.Context, path string) error { return Run(ctx, exec.CommandContext(ctx, path)) }},
		{"output", func(ctx context.Context, path string) error { _, err := Output(ctx, path); return err }},
		{"combined output", func(ctx context.Context, path string) error { _, err := CombinedOutput(ctx, path); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(t.Context(), filepath.Join(t.TempDir(), "missing-command")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("start failure lost its path error: %v", err)
			}
		})
	}
}

func TestRunStreamsConfiguredCommandOutput(t *testing.T) {
	var output bytes.Buffer
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "printf streamed")
	cmd.Stdout = &output
	if err := Run(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if output.String() != "streamed" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunRejectsCommandWithoutContextOwnership(t *testing.T) {
	if err := Run(context.Background(), exec.Command("sh", "-c", "exit 0")); err == nil {
		t.Fatal("contextless command unexpectedly ran")
	}
}

func TestOutputCleansDescendantsAfterSuccessfulParent(t *testing.T) {
	out, err := Output(context.Background(), "sh", "-c",
		"sleep 30 >/dev/null 2>&1 & printf '%s' \"$!\"")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("child pid %q: %v", out, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("successful parent left descendant pid %d running", pid)
}

func TestCombinedOutputPreservesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := CombinedOutput(ctx, "sh", "-c", "trap '' TERM; sleep 30 & wait")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled command error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("canceled command returned after %s", elapsed)
	}
}

func TestOutputRejectsNilContextBeforeStart(t *testing.T) {
	//nolint:staticcheck // The exported boundary must reject a nil context before starting work.
	if _, err := Output(nil, "must-not-run"); err == nil {
		t.Fatal("nil context unexpectedly started a command")
	}
}
