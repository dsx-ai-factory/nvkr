// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin || linux

package qemutpm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// ErrUnjoined requires retaining the runtime and its network ownership.
var ErrUnjoined = errors.New("native QEMU children could not be joined")

// Run is the hidden command's only execution boundary. Its configuration is
// process-local and private, and QEMU remains the owner of its PID/QMP files.
func Run(ctx context.Context, args []string) error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return e.New(e.CodeEngineCapability, "native QEMU TPM adapter requires Darwin ARM64")
	}
	cfg, err := load(os.Getenv(ConfigEnvironment))
	if err == nil {
		err = run(ctx, cfg, args, os.Stdout, os.Stderr)
	}
	if err != nil {
		return e.Wrap(e.CodePreflightFailed, err, "native QEMU software TPM")
	}
	return nil
}

func run(ctx context.Context, cfg configuration, args []string, stdout, stderr *os.File) (result error) {
	if ctx == nil {
		return errors.New("active QEMU context required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	args, err := acceleratorArguments(args, cfg.Accelerator)
	if err != nil {
		return err
	}
	for _, selected := range []tool{cfg.QEMU, cfg.SWTPM} {
		actual, err := inspectTool(ctx, selected.Path)
		if err != nil || actual != selected {
			return errors.Join(err, errors.New("installed QEMU/TPM executable changed after admission"))
		}
	}
	id, err := instance(args, cfg.Instances)
	if err != nil {
		return err
	}
	root, _, err := securepath.OpenDirectory(cfg.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := privateDirectory(root); err != nil {
		return err
	}
	instances, _, err := securepath.OpenDirectory(cfg.Instances)
	if err != nil {
		return err
	}
	defer instances.Close()
	guest, _, err := securepath.OpenChild(instances, id, true)
	if err != nil {
		return err
	}
	defer guest.Close()
	if err := protectedGuestDirectory(guest); err != nil {
		return err
	}
	launches, _, err := securepath.OpenChild(root, "launches", true)
	if err != nil {
		return err
	}
	defer launches.Close()
	if err := privateDirectory(launches); err != nil {
		return err
	}
	owner, err := openLaunchOwner(ctx, launches, id, true)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, owner.close(errors.Is(result, ErrUnjoined))) }()
	previous, err := owner.previous(ctx)
	if err != nil {
		return err
	}
	state, err := owner.state(ctx, instances, guest, id, previous)
	if err != nil {
		return err
	}
	launch, err := owner.record(ctx)
	if err != nil {
		return err
	}
	defer launch.Close()
	var qemu, tpm *child
	var sockets, socket string
	defer func() {
		budget, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
		defer cancel()
		stopErr := errors.Join(stop(budget, qemu), stop(budget, tpm))
		result = errors.Join(result, stopErr)
		if stopErr != nil {
			return
		}
		if socket != "" {
			if err := errors.Join(removeSocket(socket), os.Remove(sockets)); err != nil {
				result = errors.Join(result, err)
				return
			}
		}
		if _, err := owner.state(budget, instances, guest, id, state); err != nil {
			result = errors.Join(result, err)
			return
		}
		result = errors.Join(result, writeJoined(budget, launch, state))
	}()
	// Only the source-owned NICo lifecycle decides to launch this instance again.
	// Every earlier supervisor joined; the protected TPM directory is unchanged.
	tpmStatePath := filepath.Join(cfg.Instances, id, "nvkr-tpm")
	sockets, err = os.MkdirTemp("/tmp", "nvkr-tpm-")
	if err != nil {
		return err
	}
	socket = filepath.Join(sockets, "control")
	tpmArgs := []string{"socket", "--tpm2", "--tpmstate", "dir=" + tpmStatePath + ",mode=0600,lock", "--ctrl", "type=unixio,path=" + socket + ",mode=0600,terminate"}
	tpm, err = start(ctx, cfg.SWTPM.Path, tpmArgs, nil, stdout, stderr)
	if err != nil {
		return err
	}
	ready, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = waitSocket(ready, socket, tpm)
	cancel()
	if err != nil {
		return err
	}
	// vmnet-client's command ABI passes its datagram socket at FD4. ExtraFiles
	// explicitly retains that descriptor; neither child creates another PGID.
	network := os.NewFile(4, "vmnet-network")
	if network == nil {
		return errors.New("vmnet network descriptor is missing")
	}
	defer network.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(4, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return errors.Join(err, errors.New("vmnet FD4 must be a socket"))
	}
	if kind, err := unix.GetsockoptInt(4, unix.SOL_SOCKET, unix.SO_TYPE); err != nil || kind != unix.SOCK_DGRAM {
		return errors.Join(err, errors.New("vmnet FD4 must be a datagram socket"))
	}
	qemuArgs := append(append([]string(nil), args...), "-chardev", "socket,id=nvkrtpm,path="+socket, "-tpmdev", "emulator,id=nvkrtpm0,chardev=nvkrtpm", "-device", "tpm-tis-device,tpmdev=nvkrtpm0")
	qemu, err = start(ctx, cfg.QEMU.Path, qemuArgs, []*os.File{nil, network}, stdout, stderr)
	if err != nil {
		return err
	}
	select {
	case <-qemu.done:
		return qemu.err
	case <-tpm.done:
		// Control-disconnect termination can finish before Wait observes QEMU's
		// normal exit. Give that same child a bounded chance to report it.
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-qemu.done:
			return qemu.err
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.Join(tpm.err, errors.New("software TPM exited while QEMU was running"))
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Match only the two fixed operands issued by the selected NICoSim Darwin
// backend (platformArgs); QEMU owns the remaining argument grammar. TCG is
// explicitly requested and changes no firmware, disk, network or TPM operand.
func acceleratorArguments(args []string, accelerator Accelerator) ([]string, error) {
	if !accelerator.valid() {
		return nil, errors.New("explicit hvf or tcg QEMU accelerator required")
	}
	selected := append([]string(nil), args...)
	machine, cpu := false, false
	for i, arg := range args {
		flag := strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-")
		switch flag {
		case "machine":
			if arg != "-machine" || machine || i+1 >= len(args) || args[i+1] != "virt,accel=hvf" {
				return nil, errors.New("QEMU machine differs from the selected NICoSim Darwin contract")
			}
			machine = true
			if accelerator == TCG {
				selected[i+1] = "virt,accel=tcg"
			}
		case "cpu":
			if arg != "-cpu" || cpu || i+1 >= len(args) || args[i+1] != "host" {
				return nil, errors.New("QEMU CPU differs from the selected NICoSim Darwin contract")
			}
			cpu = true
			if accelerator == TCG {
				selected[i+1] = "max"
			}
		case "M", "accel", "enable-kvm":
			return nil, errors.New("QEMU accelerator conflicts with the explicit native selection")
		}
	}
	if !machine || !cpu {
		return nil, errors.New("selected NICoSim machine and CPU operands required")
	}
	return selected, nil
}

// Recognize only the source-owned PID-file operand needed to constrain state;
// QEMU owns all other argument validation. No QEMU configuration is interpreted.
func instance(args []string, root string) (string, error) {
	if len(args) > 128 || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errors.New("bounded QEMU invocation and absolute instance root required")
	}
	pid := ""
	for i, arg := range args {
		if len(arg) > 16<<10 || strings.ContainsRune(arg, 0) {
			return "", errors.New("invalid QEMU argument")
		}
		flag := strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-")
		switch flag {
		case "tpmdev", "readconfig", "daemonize":
			return "", errors.New("QEMU TPM adapter rejects conflicting device/configuration or daemonization")
		case "device", "chardev":
			if arg != "-"+flag || i+1 >= len(args) || strings.Contains(args[i+1], "nvkrtpm") || flag == "device" && strings.Contains(args[i+1], "tpm") {
				return "", errors.New("QEMU TPM device or identifier conflicts")
			}
		case "pidfile":
			if arg != "-pidfile" || i+1 >= len(args) || pid != "" {
				return "", errors.New("require exactly one source-owned PID file")
			}
			pid = args[i+1]
		}
	}
	relative, err := filepath.Rel(root, pid)
	parts := strings.Split(relative, string(filepath.Separator))
	if err != nil || !filepath.IsAbs(pid) || filepath.Clean(pid) != pid || len(parts) != 2 || parts[0] == ".." || parts[0] == "." || parts[1] != "qemu.pid" || strings.ContainsAny(parts[0], ",\x00\r\n") {
		return "", errors.New("QEMU PID file must be in one exact owned instance directory")
	}
	return parts[0], nil
}

type child struct {
	command *exec.Cmd
	done    chan struct{}
	err     error
}

func start(ctx context.Context, path string, args []string, files []*os.File, stdout, stderr *os.File) (*child, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := exec.Command(path, args...)
	command.ExtraFiles, command.Stdout, command.Stderr = files, stdout, stderr
	// Inherit vmnet-client's group. Killing only the wrapper must never make
	// its supervised QEMU or TPM children escape the simulator's delete owner.
	if err := command.Start(); err != nil {
		return nil, err
	}
	c := &child{command: command, done: make(chan struct{})}
	go func() { c.err = command.Wait(); close(c.done) }()
	return c, nil
}

func stop(ctx context.Context, c *child) error {
	if c == nil {
		return nil
	}
	select {
	case <-c.done:
		return nil
	default:
	}
	_ = c.command.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-c.done:
		return nil
	case <-timer.C:
		_ = c.command.Process.Kill()
	case <-ctx.Done():
		_ = c.command.Process.Kill()
	}
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return errors.Join(ErrUnjoined, ctx.Err())
	}
}

func waitSocket(ctx context.Context, path string, c *child) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return errors.Join(c.err, errors.New("software TPM exited before readiness"))
		case <-ticker.C:
		}
	}
}

func privateDirectory(file *os.File) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	if stat.Uid != uint32(os.Getuid()) || stat.Mode&07777 != 0700 {
		return errors.New("require a current-user-owned private QEMU adapter directory")
	}
	return nil
}

// NICoSim creates guest directories with 0755 beneath the private runtime.
// Their TPM state and adapter journals remain independently private (0700).
func protectedGuestDirectory(file *os.File) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	if stat.Uid != uint32(os.Getuid()) || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0700 != 0700 || stat.Mode&07022 != 0 {
		return errors.New("QEMU instance directory must be owned by the current user, owner-accessible and without special bits or group/other write permissions")
	}
	return nil
}

func protectedOwner(file *os.File) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	if stat.Uid != 0 && stat.Uid != uint32(os.Getuid()) || stat.Mode&0022 != 0 || stat.Mode&06000 != 0 {
		return errors.New("native QEMU file must be protected and owned by root/current user")
	}
	return nil
}

func removeSocket(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
