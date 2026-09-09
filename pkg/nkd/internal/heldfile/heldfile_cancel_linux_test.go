//go:build linux

package heldfile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestHeldTerminationHelper(t *testing.T) {
	if os.Getenv("NKD_HELDFILE_TERMINATION_HELPER") != "1" {
		return
	}
	ready := os.Getenv("NKD_HELDFILE_TERMINATION_READY")
	terminated := os.Getenv("NKD_HELDFILE_TERMINATED")
	signals := make(chan os.Signal, 1)
	if os.Getenv("NKD_HELDFILE_IGNORE_TERM") == "1" {
		signal.Ignore(syscall.SIGTERM)
	} else {
		signal.Notify(signals, syscall.SIGTERM)
		defer signal.Stop(signals)
	}
	if os.Getenv("NKD_HELDFILE_SPAWN_GRANDCHILD") == "1" {
		grandchild := exec.Command(os.Args[0], "-test.run=^TestHeldGrandchildHelper$")
		grandchild.Env = append(os.Environ(),
			"NKD_HELDFILE_GRANDCHILD_HELPER=1",
			"NKD_HELDFILE_GRANDCHILD_PID="+os.Getenv("NKD_HELDFILE_GRANDCHILD_PID"),
		)
		grandchild.Stdout, grandchild.Stderr = os.Stdout, os.Stderr
		if err := grandchild.Start(); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("NKD_HELDFILE_EXIT_AFTER_SPAWN") == "1" {
			if !waitForPath(os.Getenv("NKD_HELDFILE_GRANDCHILD_PID"), 2*time.Second) {
				t.Fatal("grandchild did not become ready")
			}
			return
		}
	}
	if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("NKD_HELDFILE_IGNORE_TERM") == "1" {
		for {
			time.Sleep(time.Second)
		}
	}
	<-signals
	if err := os.WriteFile(terminated, []byte("terminated"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHeldGrandchildHelper(t *testing.T) {
	if os.Getenv("NKD_HELDFILE_GRANDCHILD_HELPER") != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	pidPath := os.Getenv("NKD_HELDFILE_GRANDCHILD_PID")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestHeldNonzeroHelper(t *testing.T) {
	if os.Getenv("NKD_HELDFILE_NONZERO_HELPER") != "1" {
		return
	}
	os.Exit(23)
}

func TestRunContextTerminatesGracefullyThenEscalates(t *testing.T) {
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(executablePath)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := Open(executablePath, Options{
		Label: "termination-helper", Executable: true, MaxBytes: info.Size() + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer executable.Close()

	t.Run("nonzero", func(t *testing.T) {
		err := RunContext(context.Background(), executable,
			[]string{"-test.run=^TestHeldNonzeroHelper$"},
			RunOptions{Env: append(os.Environ(), "NKD_HELDFILE_NONZERO_HELPER=1")})
		if err == nil {
			t.Fatal("held command swallowed a nonzero child exit")
		}
	})

	t.Run("graceful", func(t *testing.T) {
		root := t.TempDir()
		ready := filepath.Join(root, "ready")
		terminated := filepath.Join(root, "terminated")
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- RunContext(ctx, executable,
				[]string{"-test.run=^TestHeldTerminationHelper$"},
				RunOptions{Env: append(os.Environ(),
					"NKD_HELDFILE_TERMINATION_HELPER=1",
					"NKD_HELDFILE_TERMINATION_READY="+ready,
					"NKD_HELDFILE_TERMINATED="+terminated,
				), TerminationGrace: 100 * time.Millisecond})
		}()
		if !waitForPath(ready, 2*time.Second) {
			t.Fatal("held child did not become ready")
		}
		cancel()
		if err := <-result; err == nil {
			t.Fatal("canceled held command succeeded")
		}
		if !waitForPath(terminated, time.Second) {
			t.Fatal("held child was killed without receiving SIGTERM")
		}
	})

	t.Run("escalation", func(t *testing.T) {
		root := t.TempDir()
		ready := filepath.Join(root, "ready")
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- RunContext(ctx, executable,
				[]string{"-test.run=^TestHeldTerminationHelper$"},
				RunOptions{Env: append(os.Environ(),
					"NKD_HELDFILE_TERMINATION_HELPER=1",
					"NKD_HELDFILE_TERMINATION_READY="+ready,
					"NKD_HELDFILE_IGNORE_TERM=1",
				), TerminationGrace: 50 * time.Millisecond})
		}()
		if !waitForPath(ready, 2*time.Second) {
			t.Fatal("stubborn held child did not become ready")
		}
		started := time.Now()
		cancel()
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("stubborn canceled held command succeeded")
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("SIGKILL escalation exceeded its bound: %s", elapsed)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("stubborn held child was never killed")
		}
	})
}

func TestRunContextCancellationReapsDescendantProcessGroup(t *testing.T) {
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(executablePath)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := Open(executablePath, Options{
		Label: "descendant-helper", Executable: true, MaxBytes: info.Size() + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer executable.Close()

	root := t.TempDir()
	ready := filepath.Join(root, "ready")
	pidPath := filepath.Join(root, "grandchild.pid")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- RunContext(ctx, executable,
			[]string{"-test.run=^TestHeldTerminationHelper$"},
			RunOptions{Env: append(os.Environ(),
				"NKD_HELDFILE_TERMINATION_HELPER=1",
				"NKD_HELDFILE_TERMINATION_READY="+ready,
				"NKD_HELDFILE_SPAWN_GRANDCHILD=1",
				"NKD_HELDFILE_GRANDCHILD_PID="+pidPath,
			), TerminationGrace: 100 * time.Millisecond})
	}()
	if !waitForPath(ready, 2*time.Second) || !waitForPath(pidPath, 2*time.Second) {
		cancel()
		t.Fatal("held descendant process did not become ready")
	}
	rawPID, err := os.ReadFile(pidPath)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(rawPID))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	cancel()
	if err := <-result; err == nil {
		t.Fatal("canceled held command with a descendant succeeded")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived held command cancellation", pid)
}

func TestRunContextSuccessfulChildCannotLeaveDescendant(t *testing.T) {
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(executablePath)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := Open(executablePath, Options{
		Label: "successful-descendant-helper", Executable: true, MaxBytes: info.Size() + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer executable.Close()

	pidPath := filepath.Join(t.TempDir(), "grandchild.pid")
	err = RunContext(context.Background(), executable,
		[]string{"-test.run=^TestHeldTerminationHelper$"},
		RunOptions{Env: append(os.Environ(),
			"NKD_HELDFILE_TERMINATION_HELPER=1",
			"NKD_HELDFILE_TERMINATION_READY="+filepath.Join(t.TempDir(), "unused"),
			"NKD_HELDFILE_SPAWN_GRANDCHILD=1",
			"NKD_HELDFILE_EXIT_AFTER_SPAWN=1",
			"NKD_HELDFILE_GRANDCHILD_PID="+pidPath,
		), TerminationGrace: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("successful direct child returned %v", err)
	}
	rawPID, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(rawPID))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived successful held-command exit", pid)
}
