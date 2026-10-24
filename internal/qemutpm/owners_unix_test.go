// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin || linux

package qemutpm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"golang.org/x/sys/unix"
)

func ownerFixture(t *testing.T) (string, configuration) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"adapter", "adapter/launches", "simulator", "simulator/instances", "simulator/instances/instance"} {
		mode := os.FileMode(0700)
		if name == "simulator/instances" || name == "simulator/instances/instance" {
			mode = 0755 // Match NICoSim's source-owned artifact directories.
		}
		if err := os.Mkdir(filepath.Join(root, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := inspectTool(t.Context(), self)
	if err != nil {
		t.Fatal(err)
	}
	cfg := configuration{filepath.Join(root, "adapter"), filepath.Join(root, "simulator", "instances"), selected, selected, TCG}
	if err := writeJSON(filepath.Join(cfg.Directory, "configuration.json"), cfg); err != nil {
		t.Fatal(err)
	}
	return root, cfg
}

func TestSourceGuestDirectoryPermissions(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode os.FileMode
		want bool
	}{
		{"private", 0700, true},
		{"group readable", 0750, true},
		{"NICoSim default", 0755, true},
		{"group writable", 0775, false},
		{"other writable", 0757, false},
		{"owner inaccessible", 0500, false},
		{"setuid", 0755 | os.ModeSetuid, false},
		{"setgid", 0755 | os.ModeSetgid, false},
		{"sticky", 0755 | os.ModeSticky, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, cfg := ownerFixture(t)
			guest := filepath.Join(cfg.Instances, "instance")
			if err := os.Chmod(guest, tc.mode); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(guest)
			if err != nil {
				t.Fatal(err)
			}
			if special := tc.mode & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky); info.Mode()&special != special {
				t.Skip("filesystem did not retain the requested special directory bits")
			}
			err = startOwner(t, root, "success").wait(t)
			if (err == nil) != tc.want {
				t.Fatalf("mode %v: launch error %v", tc.mode, err)
			}
			if !tc.want {
				for _, path := range []string{filepath.Join(guest, "nvkr-tpm"), filepath.Join(root, "qemu.pid"), filepath.Join(root, "tpm.pid")} {
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("rejected guest created state or a child: %s: %v", path, err)
					}
				}
				return
			}
			for _, path := range []string{cfg.Directory, filepath.Join(cfg.Directory, "launches", "instance"), filepath.Join(guest, "nvkr-tpm")} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0700 {
					t.Fatalf("private directory permissions changed: %s: %v %v", path, info, err)
				}
			}
			if err := (&Adapter{cfg}).Close(t.Context()); err != nil {
				t.Fatal("joined launch was not accepted", err)
			}
		})
	}
}

type fakeOwner struct {
	command *exec.Cmd
	network net.Conn
	done    chan error
	joined  bool
	output  bytes.Buffer
}

func startOwner(t *testing.T, root, mode string) *fakeOwner {
	t.Helper()
	return startOwnerWithEnvironment(t, root, append(os.Environ(), "NVKR_TEST_NATIVE_QEMU_MODE="+mode))
}

func startOwnerWithEnvironment(t *testing.T, root string, environment []string) *fakeOwner {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	parent, childFD := os.NewFile(uintptr(fds[0]), "parent"), os.NewFile(uintptr(fds[1]), "child")
	conn, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		_ = childFD.Close()
		t.Fatal(err)
	}
	command := exec.Command(self, "test-owner")
	command.Env = append(environment, "NVKR_TEST_NATIVE_QEMU=1", "NVKR_TEST_NATIVE_QEMU_ROOT="+root)
	command.ExtraFiles = []*os.File{nil, childFD}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	p := &fakeOwner{command: command, network: conn, done: make(chan error, 1)}
	command.Stdout, command.Stderr = &p.output, &p.output
	if err := command.Start(); err != nil {
		_ = childFD.Close()
		_ = conn.Close()
		t.Fatal(err)
	}
	_ = childFD.Close()
	go func() { p.done <- command.Wait() }()
	t.Cleanup(func() {
		_ = conn.Close()
		if !p.joined {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			select {
			case <-p.done:
			case <-time.After(5 * time.Second):
				t.Error("test owner could not be joined")
			}
		}
	})
	return p
}

func (p *fakeOwner) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-p.done:
		p.joined = true
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("test owner did not return")
		return nil
	}
}

func TestSourceOwnedSameInstanceRelaunchPreservesTPM(t *testing.T) {
	root, cfg := ownerFixture(t)
	marker := filepath.Join(cfg.Instances, "instance", "nvkr-tpm", "identity-marker")
	var before []byte
	for n := 0; n < 2; n++ {
		if err := startOwner(t, root, "success").wait(t); err != nil {
			t.Fatal("same instance could not finish its launch", n, err)
		}
		current, err := os.ReadFile(marker)
		if err != nil || len(current) == 0 || n > 0 && !bytes.Equal(before, current) {
			t.Fatal("TPM identity marker changed on relaunch", err)
		}
		before = current
		if err := (&Adapter{cfg}).Close(t.Context()); err != nil {
			t.Fatal("joined launch was not accepted", err)
		}
	}
	rows, err := os.ReadDir(filepath.Join(cfg.Directory, "launches", "instance"))
	if err != nil || len(rows) != 3 { // one guard and two independent launch audits
		t.Fatal("relaunch overwrote its predecessor audit", rows, err)
	}
	// NVKE's reconcileAbsent removes its instance directory after Delete;
	// deferred adapter cleanup must still authenticate the retained journals.
	if err := os.RemoveAll(filepath.Join(cfg.Instances, "instance")); err != nil {
		t.Fatal(err)
	}
	if err := (&Adapter{cfg}).Close(t.Context()); err != nil {
		t.Fatal("source-owned instance deletion prevented joined cleanup", err)
	}
	if err := startOwner(t, root, "success").wait(t); err == nil {
		t.Fatal("missing instance state was accepted for relaunch")
	}
}

func TestCloseRejectsMissingJournalAfterOwnedGuestDeletion(t *testing.T) {
	_, cfg := ownerFixture(t)
	receipt := joinedOwnerFixture(t, cfg)
	if err := os.Remove(receipt); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(cfg.Instances, "instance")); err != nil {
		t.Fatal(err)
	}
	if err := (&Adapter{cfg}).Close(t.Context()); !errors.Is(err, ErrUnjoined) {
		t.Fatal("missing instance directory excused an unjoined audit", err)
	}
}

func TestConcurrentInstanceLaunchAndCloseFailUntilJoined(t *testing.T) {
	root, cfg := ownerFixture(t)
	first := startOwner(t, root, "cancel")
	_ = first.network.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := first.network.Read(make([]byte, 8192)); err != nil {
		t.Fatal("first fake QEMU did not start", err)
	}
	if err := startOwner(t, root, "success").wait(t); err == nil {
		t.Fatal("concurrent same-instance launch was admitted")
	}
	if err := (&Adapter{cfg}).Close(t.Context()); !errors.Is(err, ErrUnjoined) {
		t.Fatal("cleanup accepted an active owner", err)
	}
	_ = first.command.Process.Signal(syscall.SIGTERM)
	if err := first.wait(t); err == nil {
		t.Fatal("cancellation lost its original failure")
	}
	if err := (&Adapter{cfg}).Close(t.Context()); err != nil {
		t.Fatal("canceled children did not join", err)
	}
}

// Construct an already joined local audit using the same descriptor/state
// owner, without launching any process. Tampering tests never touch real TPMs.
func joinedOwnerFixture(t *testing.T, cfg configuration) string {
	t.Helper()
	launches, _, err := securepath.OpenDirectory(filepath.Join(cfg.Directory, "launches"))
	if err != nil {
		t.Fatal(err)
	}
	defer launches.Close()
	owner, err := openLaunchOwner(t.Context(), launches, "instance", true)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.close(false)
	instances, _, err := securepath.OpenDirectory(cfg.Instances)
	if err != nil {
		t.Fatal(err)
	}
	defer instances.Close()
	guest, _, err := securepath.OpenChild(instances, "instance", true)
	if err != nil {
		t.Fatal(err)
	}
	defer guest.Close()
	state, err := owner.state(t.Context(), instances, guest, "instance", instanceState{})
	if err != nil {
		t.Fatal(err)
	}
	record, err := owner.record(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer record.Close()
	if err := writeJoined(t.Context(), record, state); err != nil {
		t.Fatal(err)
	}
	rows, err := os.ReadDir(filepath.Join(cfg.Directory, "launches", "instance"))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.IsDir() {
			return filepath.Join(cfg.Directory, "launches", "instance", row.Name(), "joined")
		}
	}
	t.Fatal("missing test launch audit")
	return ""
}

func TestRelaunchRejectsIncompleteOrSubstitutedOwnerAndState(t *testing.T) {
	for _, mode := range []string{"missing join", "older join missing", "malformed join", "unjoined", "guard replaced", "guard symlink", "guard permissions", "state missing", "state replaced", "state symlink", "state permissions", "guest replaced", "guest permissions"} {
		t.Run(mode, func(t *testing.T) {
			root, cfg := ownerFixture(t)
			receipt := joinedOwnerFixture(t, cfg)
			guard := filepath.Join(cfg.Directory, "launches", "instance", "guard")
			state := filepath.Join(cfg.Instances, "instance", "nvkr-tpm")
			var err error
			switch mode {
			case "missing join":
				err = os.Remove(receipt)
			case "older join missing":
				data, readErr := os.ReadFile(receipt)
				var prior termination
				if readErr != nil || json.Unmarshal(data, &prior) != nil {
					t.Fatal("invalid fixture", readErr)
				}
				next := filepath.Join(filepath.Dir(filepath.Dir(receipt)), "launch-second")
				if err = os.Mkdir(next, 0700); err == nil {
					err = writeJSON(filepath.Join(next, "joined"), prior)
				}
				if err == nil {
					err = os.Remove(receipt)
				}
			case "malformed join":
				err = os.WriteFile(receipt, []byte(`{"Joined":true} trailing`), 0600)
			case "unjoined":
				err = os.WriteFile(receipt, []byte(`{"Joined":false}`), 0600)
			case "guard replaced", "guard symlink":
				original := filepath.Join(root, "guard-original")
				if err = os.Rename(guard, original); err == nil {
					if mode == "guard replaced" {
						err = os.WriteFile(guard, nil, 0600)
					} else {
						err = os.Symlink(original, guard)
					}
				}
			case "guard permissions":
				err = os.Chmod(guard, 0644)
			case "state missing", "state replaced", "state symlink":
				if err = os.Rename(state, state+"-original"); err == nil {
					switch mode {
					case "state replaced":
						err = os.Mkdir(state, 0700)
					case "state symlink":
						err = os.Symlink(state+"-original", state)
					}
				}
			case "state permissions":
				err = os.Chmod(state, 0755)
			case "guest permissions":
				err = os.Chmod(filepath.Dir(state), 0775)
			case "guest replaced":
				guest := filepath.Dir(state)
				if err = os.Rename(guest, guest+"-original"); err == nil {
					err = os.MkdirAll(state, 0700)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := startOwner(t, root, "success").wait(t); err == nil {
				t.Fatal("changed or incomplete predecessor admitted another launch")
			}
			if err := (&Adapter{cfg}).Close(t.Context()); !errors.Is(err, ErrUnjoined) {
				t.Fatal("cleanup accepted incomplete or changed ownership", err)
			}
		})
	}
}

func TestKernelReleasedGuardDoesNotAuthorizeAbandonedLaunch(t *testing.T) {
	root, cfg := ownerFixture(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(self, "test-owner-drop")
	command.Env = append(os.Environ(), "NVKR_TEST_NATIVE_QEMU=1", "NVKR_TEST_NATIVE_QEMU_ROOT="+root)
	if err := command.Run(); err != nil {
		t.Fatal("test owner did not exit", err)
	}
	launches, _, err := securepath.OpenDirectory(filepath.Join(cfg.Directory, "launches"))
	if err != nil {
		t.Fatal(err)
	}
	defer launches.Close()
	owner, err := openLaunchOwner(t.Context(), launches, "instance", true)
	if err != nil {
		t.Fatal("kernel did not release exited owner's lock", err)
	}
	if _, err := owner.previous(t.Context()); !errors.Is(err, ErrUnjoined) {
		t.Fatal("process exit was mistaken for a joined launch", err)
	}
	if err := owner.close(false); err != nil {
		t.Fatal(err)
	}
	if err := startOwner(t, root, "success").wait(t); err == nil {
		t.Fatal("abandoned predecessor admitted relaunch")
	}
	if err := (&Adapter{cfg}).Close(t.Context()); !errors.Is(err, ErrUnjoined) {
		t.Fatal("cleanup accepted an abandoned launch", err)
	}
}

func TestOwnerCancellationAndRetainedUnjoinedGuard(t *testing.T) {
	_, cfg := ownerFixture(t)
	joinedOwnerFixture(t, cfg)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := (&Adapter{cfg}).Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cleanup ignored cancellation", err)
	}
	launches, _, err := securepath.OpenDirectory(filepath.Join(cfg.Directory, "launches"))
	if err != nil {
		t.Fatal(err)
	}
	defer launches.Close()
	owner, err := openLaunchOwner(t.Context(), launches, "instance", true)
	if err != nil {
		t.Fatal(err)
	}
	// The hidden process keeps this descriptor until process exit after an
	// unjoined result; this test explicitly releases only its own simulated hold.
	defer unix.Close(owner.guard)
	if err := owner.close(true); err != nil {
		t.Fatal(err)
	}
	if err := (&Adapter{cfg}).Close(t.Context()); !errors.Is(err, ErrUnjoined) {
		t.Fatal("unjoined owner released its guard", err)
	}
}
