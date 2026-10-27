// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func currentOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}

// checkDaemonBindings interprets only the kernel's NUL-separated environ ABI;
// it never logs or retains unrelated credentials from another process.
func checkDaemonBindings(raw []byte, bound map[string]string) error {
	if len(raw) > 1<<20 {
		return errors.New("session daemon environment exceeds bound")
	}
	seen := map[string]bool{}
	for _, entry := range bytes.Split(raw, []byte{0}) {
		key, value, ok := strings.Cut(string(entry), "=")
		expected, selected := bound[key]
		if !ok || !selected {
			continue
		}
		if seen[key] || value != expected {
			return errors.New("session daemon uses different or duplicated account/runtime paths")
		}
		seen[key] = true
	}
	return nil
}
func nativeAccountHome(ctx context.Context) (string, error) {
	if err := requireContext(ctx); err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "resolve native account home")
	}
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "resolve native account")
	}
	home := account.HomeDir
	info, err := os.Lstat(home)
	if err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "inspect native account home")
	}
	if !filepath.IsAbs(home) || !info.IsDir() || !currentOwner(info) {
		return "", nvkrerr.New(nvkrerr.CodeEngineCapability, "require native account regular owned home")
	}
	if err := requireContext(ctx); err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "resolve native account home")
	}
	return home, nil
}

func admitLibvirtEnvironment(ctx context.Context, runner Runner) (map[string]string, error) {
	home, err := nativeAccountHome(ctx)
	if err != nil {
		return nil, err
	}
	runtime := filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	info, err := os.Lstat(runtime)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || !currentOwner(info) || info.Mode().Perm() != 0o700 {
		return nil, errors.New("require current user's private native libvirt runtime directory")
	}
	bound := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_CACHE_HOME": filepath.Join(home, ".cache"), "XDG_RUNTIME_DIR": runtime}
	runner.Work = ""
	raw, err := runner.Capture(ctx, []string{"ps", "-u", strconv.Itoa(os.Getuid()), "-o", "pid=,comm="}, "session-daemon-inventory", 30*time.Second)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || (fields[1] != "libvirtd" && fields[1] != "virtqemud") {
			continue
		}
		pid, parseErr := strconv.Atoi(fields[0])
		if parseErr != nil || pid <= 0 {
			return nil, errors.New("invalid daemon process observation")
		}
		file, stat, readErr := securepath.OpenRegular(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
		if readErr != nil {
			return nil, readErr
		}
		if !currentOwner(stat) {
			_ = file.Close()
			return nil, errors.New("session daemon owner changed")
		}
		data, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		_ = file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if readErr = checkDaemonBindings(data, bound); readErr != nil {
			return nil, readErr
		}
	}
	return bound, nil
}

// Both native hosts share the default tenant type. Native deployment may author
// a different memory budget; CNI requests, limits and Kubernetes reservations
// remain source-owned.
const (
	tenantDemoVCPUs     = 6
	tenantDemoMemoryGiB = 12
)
