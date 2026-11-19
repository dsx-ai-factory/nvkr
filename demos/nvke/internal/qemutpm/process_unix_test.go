// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin || linux

package qemutpm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
)

func TestMain(m *testing.M) {
	if os.Getenv("NVKR_TEST_NATIVE_QEMU") != "1" {
		os.Exit(m.Run())
	}
	root := os.Getenv("NVKR_TEST_NATIVE_QEMU_ROOT")
	if len(os.Args) > 1 && os.Args[1] == "test-owner-drop" {
		launches, _, err := securepath.OpenDirectory(filepath.Join(root, "adapter", "launches"))
		if err != nil {
			panic(err)
		}
		owner, err := openLaunchOwner(context.Background(), launches, "instance", true)
		if err != nil {
			panic(err)
		}
		if _, err := owner.record(context.Background()); err != nil {
			panic(err)
		}
		// Simulate a supervisor dying after intent but before launching children.
		// The kernel releases its guard; no joined receipt is manufactured.
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "test-owner" {
		cfg, err := load(filepath.Join(root, "adapter", "configuration.json"))
		if err != nil {
			panic(err)
		}
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer cancel()
		args := []string{"-machine", "virt,accel=hvf", "-cpu", "host", "-pidfile", filepath.Join(cfg.Instances, "instance", "qemu.pid")}
		if os.Getenv("NVKR_TEST_NATIVE_QEMU_REAL") == "1" {
			// No guest disk or firmware: create a paused 64 MiB machine solely
			// to exercise the real installed standard TPM-device launch path.
			args = append(args, "-m", "64", "-S", "-display", "none", "-nodefaults", "-nic", "none")
		}
		err = run(ctx, cfg, args, os.Stdout, os.Stderr)
		_ = os.WriteFile(filepath.Join(root, "result"), []byte(fmt.Sprint(err)), 0600)
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	mode := os.Getenv("NVKR_TEST_NATIVE_QEMU_MODE")
	role := "qemu"
	if len(os.Args) > 1 && os.Args[1] == "socket" {
		role = "tpm"
	}
	// Publish the checked PID and force-escalation behavior before either
	// readiness signal. Cancellation may follow the socket/FD4 immediately.
	signal.Ignore(syscall.SIGTERM)
	if err := os.WriteFile(filepath.Join(root, role+".pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		panic(err)
	}
	if role == "tpm" {
		control := ""
		for _, arg := range os.Args {
			if strings.HasPrefix(arg, "type=unixio,path=") {
				control = strings.Split(strings.TrimPrefix(arg, "type=unixio,path="), ",")[0]
			}
			if strings.HasPrefix(arg, "dir=") {
				state := strings.Split(strings.TrimPrefix(arg, "dir="), ",")[0]
				file, err := os.OpenFile(filepath.Join(state, "identity-marker"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err == nil {
					_, _ = file.WriteString("synthetic TPM identity")
					_ = file.Close()
				} else if !errors.Is(err, os.ErrExist) {
					panic(err)
				}
			}
		}
		listener, err := net.Listen("unix", control)
		if err != nil {
			panic(err)
		}
		defer listener.Close()
	} else {
		report, err := json.Marshal(struct {
			PID, PGID int
			Args      []string
		}{os.Getpid(), syscall.Getpgrp(), os.Args[1:]})
		if err != nil {
			panic(err)
		}
		if _, err := os.NewFile(4, "network").Write(report); err != nil {
			panic(err)
		}
	}
	if role == "qemu" && mode == "success" {
		os.Exit(0)
	}
	if role == "qemu" && mode == "qemu-failure" {
		os.Exit(23)
	}
	if role == "tpm" && mode == "tpm-failure" {
		time.Sleep(200 * time.Millisecond)
		os.Exit(24)
	}
	time.Sleep(time.Minute)
	os.Exit(25)
}

func TestExplicitAcceleratorMatchesOnlySelectedSourceOperands(t *testing.T) {
	base := []string{"-name", "owned", "-machine", "virt,accel=hvf", "-cpu", "host", "-drive", "file=selected.qcow2", "-netdev", "dgram,id=net0,local.type=fd,local.str=4"}
	for _, tc := range []struct {
		name        string
		accelerator Accelerator
		args        []string
		accepted    bool
	}{
		{"HVF unchanged", HVF, base, true},
		{"explicit TCG", TCG, base, true},
		{"missing selection", "", base, false},
		{"fallback", "hvf:tcg", base, false},
		{"missing operands", TCG, []string{"-name", "owned"}, false},
		{"foreign machine", TCG, []string{"-machine", "q35,accel=hvf", "-cpu", "host"}, false},
		{"already TCG", TCG, []string{"-machine", "virt,accel=tcg", "-cpu", "max"}, false},
		{"changed CPU", TCG, []string{"-machine", "virt,accel=hvf", "-cpu", "cortex-a72"}, false},
		{"inline machine", TCG, []string{"-machine=virt,accel=hvf", "-cpu", "host"}, false},
		{"inline CPU", TCG, []string{"-machine", "virt,accel=hvf", "-cpu=host"}, false},
		{"duplicate machine", TCG, append(append([]string{}, base...), "-machine", "virt,accel=hvf"), false},
		{"duplicate CPU", TCG, append(append([]string{}, base...), "-cpu", "host"), false},
		{"alternate accelerator", TCG, append(append([]string{}, base...), "-accel", "hvf"), false},
		{"inline accelerator", TCG, append(append([]string{}, base...), "--accel=hvf"), false},
		{"machine alias", TCG, append(append([]string{}, base...), "-M", "virt"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]string(nil), tc.args...)
			got, err := acceleratorArguments(tc.args, tc.accelerator)
			if (err == nil) != tc.accepted || !reflect.DeepEqual(tc.args, before) {
				t.Fatalf("arguments=%v err=%v", got, err)
			}
			if tc.accepted {
				want := append([]string(nil), base...)
				if tc.accelerator == TCG {
					want[3], want[5] = "virt,accel=tcg", "max"
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("non-accelerator operands changed: %v", got)
				}
			}
		})
	}
}

func TestInstanceAdmission(t *testing.T) {
	root := filepath.Join(t.TempDir(), "instances")
	valid := []string{"-pidfile", filepath.Join(root, "instance", "qemu.pid"), "-device", "virtio-net-pci,netdev=net0"}
	for _, tc := range []struct {
		name string
		args []string
		ok   bool
	}{
		{"source invocation", valid, true},
		{"missing owner", nil, false},
		{"outside owner", []string{"-pidfile", filepath.Join(filepath.Dir(root), "foreign", "qemu.pid")}, false},
		{"nested owner", []string{"-pidfile", filepath.Join(root, "one", "two", "qemu.pid")}, false},
		{"duplicate PID file", append(append([]string{}, valid...), "-pidfile", valid[1]), false},
		{"existing TPM backend", append(append([]string{}, valid...), "-tpmdev", "emulator,id=other"), false},
		{"existing TPM device", append(append([]string{}, valid...), "-device", "tpm-tis-device,tpmdev=other"), false},
		{"inline device", append(append([]string{}, valid...), "-device=tpm-tis-device"), false},
		{"config file", append(append([]string{}, valid...), "-readconfig", "file"), false},
		{"daemonization", append(append([]string{}, valid...), "-daemonize"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := instance(tc.args, root)
			if (err == nil) != tc.ok {
				t.Fatal(err)
			}
		})
	}
}

func TestSupervisorPreservesFD4GroupAndJoinsChildren(t *testing.T) {
	for _, mode := range []string{"success", "qemu-failure", "tpm-failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			root, cfg := ownerFixture(t)
			owner := startOwner(t, root, mode)
			_ = owner.network.SetReadDeadline(time.Now().Add(10 * time.Second))
			data := make([]byte, 8192)
			n, err := owner.network.Read(data)
			if err != nil {
				t.Fatal("FD4 was not inherited by QEMU", err)
			}
			var observed struct {
				PID, PGID int
				Args      []string
			}
			if err := json.Unmarshal(data[:n], &observed); err != nil || observed.PID <= 0 || observed.PGID != owner.command.Process.Pid {
				t.Fatal("QEMU escaped vmnet process group", observed, err)
			}
			if !strings.Contains(strings.Join(observed.Args, " "), "tpm-tis-device,tpmdev=nvkrtpm0") {
				t.Fatal("missing standard ARM TPM device", observed.Args)
			}
			if !strings.Contains(strings.Join(observed.Args, " "), "-machine virt,accel=tcg -cpu max") {
				t.Fatal("explicit TCG was not passed to the actual child", observed.Args)
			}
			if mode == "cancel" {
				_ = owner.command.Process.Signal(syscall.SIGTERM)
			}
			select {
			case err := <-owner.done:
				owner.joined = true
				if (err == nil) != (mode == "success") {
					t.Fatal("original outcome was lost", err)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("supervisor did not return after bounded cleanup")
			}
			if err := (&Adapter{cfg}).Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, role := range []string{"qemu", "tpm"} {
				raw, err := os.ReadFile(filepath.Join(root, role+".pid"))
				if err != nil {
					t.Fatal(err)
				}
				pid, err := strconv.Atoi(string(raw))
				if err != nil || pid <= 0 || role == "qemu" && pid != observed.PID {
					t.Fatal("child PID publication does not match readiness", role, pid, err)
				}
				if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
					t.Fatal("child remains live or unjoined", role, pid, err)
				}
			}
		})
	}
}

func TestTPMStartupFailureStillJoins(t *testing.T) {
	root, cfg := ownerFixture(t)
	failed := filepath.Join(root, "failed-tpm")
	if err := os.WriteFile(failed, []byte("#!/bin/sh\nexit 24\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var err error
	cfg.SWTPM, err = inspectTool(t.Context(), failed)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), cfg, []string{"-machine", "virt,accel=hvf", "-cpu", "host", "-pidfile", filepath.Join(cfg.Instances, "instance", "qemu.pid")}, os.Stdout, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "before readiness") {
		t.Fatal("startup failure was hidden", err)
	}
	if err := (&Adapter{cfg}).Close(t.Context()); err != nil {
		t.Fatal("failed startup child was not joined", err)
	}
}

func TestUnjoinedChildRetainsFailure(t *testing.T) {
	// The test deliberately withholds the completion observation, then performs
	// its own Wait. Process death alone must not be accepted as a joined child.
	command := exec.Command("/bin/sleep", "60")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	err := stop(ctx, &child{command: command, done: make(chan struct{})})
	if !errors.Is(err, ErrUnjoined) {
		t.Fatal("missing join was accepted", err)
	}
}
