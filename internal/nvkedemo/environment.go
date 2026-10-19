// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"golang.org/x/sys/unix"
)

func currentOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}
func privateHeader(path string) error {
	file, info, err := securepath.OpenRegular(path)
	if err != nil {
		return err
	}
	defer file.Close()
	var stat unix.Stat_t
	if err = unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	if !currentOwner(info) || stat.Nlink != 1 || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > 256 {
		return errors.New("diagnostic header is not a bounded private owned file")
	}
	return nil
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
func admitLibvirtEnvironment(ctx context.Context, runner Runner) (map[string]string, error) {
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		return nil, err
	}
	home := account.HomeDir
	info, err := os.Lstat(home)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(home) || !info.IsDir() || !currentOwner(info) {
		return nil, errors.New("require native account regular owned home")
	}
	runtime := filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	info, err = os.Lstat(runtime)
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

type seedProjection struct {
	SourceDigest  string `json:"sourceDigest"`
	RuntimeDigest string `json:"runtimeDigest"`
	InstanceType  string `json:"instanceType"`
	VCPUs         int    `json:"vcpus"`
	MemoryGiB     int    `json:"memoryGiB"`
	DiskGiB       int    `json:"diskGiB"`
}

// Both native hosts use the same tenant type. NVIDIA OVN's node pod requests
// three CPUs and 4352 MiB; Kubernetes reservations and other pods need headroom.
// Keep those source-owned requests and reservations intact.
const (
	tenantDemoVCPUs     = 6
	tenantDemoMemoryGiB = 12
)

func projectSeed(ctx context.Context, work string) (seedProjection, error) {
	var result seedProjection
	if err := requireContext(ctx); err != nil {
		return result, err
	}
	file, info, err := securepath.OpenRegular(filepath.Join(work, "internal/seed/default-seed.yaml"))
	if err != nil {
		return result, err
	}
	defer file.Close()
	if !currentOwner(info) || info.Size() > 65536 {
		return result, errors.New("source seed is not a bounded regular owned file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return result, err
	}
	projected, err := projectSeedBytes(raw)
	if err != nil {
		return result, err
	}
	if err = privateWrite(filepath.Join(work, "laptop-seed.yaml"), projected); err != nil {
		return result, err
	}
	return seedProjection{"sha256:" + SeedDigest, "sha256:" + plainSHA(projected), TenantInstanceType, tenantDemoVCPUs, tenantDemoMemoryGiB, 60}, nil
}
func projectSeedBytes(raw []byte) ([]byte, error) { return projectSeedFixture(raw, SeedDigest) }
func projectSeedFixture(raw []byte, digest string) ([]byte, error) {
	block := []byte(fmt.Sprintf("  - id: %q\n    name: \"cpu4-mem6gb-disk60gb\"\n    site_id: %q\n    gpu_count: 0\n    cpu_count: 4\n    memory_gib: 6\n    disk_gib: 60\n", TenantInstanceType, SiteID))
	// This is an exact-byte fixture projection, not a YAML parser. NICoSim owns
	// decoding; accepted source assets and their identity remain untouched.
	if len(raw) > 65536 || plainSHA(raw) != digest || bytes.Count(raw, block) != 1 {
		return nil, errors.New("source simulator seed differs from inspected laptop interface")
	}
	replacement := bytes.ReplaceAll(block, []byte("cpu4"), fmt.Appendf(nil, "cpu%d", tenantDemoVCPUs))
	replacement = bytes.ReplaceAll(replacement, []byte("cpu_count: 4"), fmt.Appendf(nil, "cpu_count: %d", tenantDemoVCPUs))
	replacement = bytes.ReplaceAll(replacement, []byte("mem6gb"), fmt.Appendf(nil, "mem%dgb", tenantDemoMemoryGiB))
	replacement = bytes.ReplaceAll(replacement, []byte("memory_gib: 6"), fmt.Appendf(nil, "memory_gib: %d", tenantDemoMemoryGiB))
	return bytes.Replace(raw, block, replacement, 1), nil
}
func simulatorEnvironment(ctx context.Context, work, disk, guardWork, gateway string, binding map[string]string) ([]string, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	var password [24]byte
	if _, err := rand.Read(password[:]); err != nil {
		return nil, err
	}
	temporary := filepath.Join(work, "tmp")
	if err := os.Mkdir(temporary, 0o700); err != nil {
		return nil, err
	}
	env := NativeEnvironment()
	qemu, err := commandPath("qemu-img", env)
	if err != nil {
		return nil, err
	}
	cloud, err := commandPath("cloud-localds", env)
	if err != nil {
		return nil, err
	}
	values := map[string]string{
		"TMPDIR": temporary, "NICO_SIM_LISTEN_ADDR": fmt.Sprintf("%s:%d", gateway, simulatorAPIPort), "NICO_SIM_DB_PATH": filepath.Join(work, "var/nico-sim.db"), "NICO_SIM_SEED_PATH": filepath.Join(work, "laptop-seed.yaml"), "NICO_SIM_FILES_DIR": filepath.Join(work, "files"),
		"NICO_VMS_VM_BACKEND": "libvirt", "NICO_VMS_LIBVIRT_URI": "qemu:///session", "NICO_VMS_LIBVIRT_NETWORK": "", "NICO_VMS_BRIDGE_NAME": BridgeName, "NICO_VMS_BRIDGE_CIDR": GuestCIDR, "NICO_VMS_BASE_IMAGE": disk,
		"NICO_VMS_BGP_ENABLED": "true", "NICO_VMS_BGP_VIP_CIDRS": VIPCIDR, "NICO_VMS_BGP_LISTEN_PORT": "10179", "NICO_VMS_CONSOLE_SSH_PORT": "-1", "NICO_VMS_CLEANUP_ORPHANED_VMS": "false", "NICO_CONTAINERS_ENABLED": "false",
		"NICO_VMS_READINESS_TIMEOUT": "30m", "NICO_VMS_DIAG_SSH_ENABLED": "true", "NICO_VMS_DIAG_SSH_USER": "nvkr-demo", "NICO_VMS_DIAG_SSH_PASSWORD": hex.EncodeToString(password[:]),
		"NICO_VMS_IMAGE_CACHE_DIR": filepath.Join(work, "cache"), "NICO_VMS_DHCP_HOSTS_DIR": filepath.Join(guardWork, "dhcp-hosts"), "NICO_VMS_DHCP_LEASE_FILE": filepath.Join(guardWork, "leases"), "NICO_VMS_PROMTAIL_TARGETS_FILE": filepath.Join(work, "promtail.json"), "NICO_SIM_LOG_FORMAT": "json",
		"NICO_VMS_QEMU_IMG_PATH": qemu, "NICO_VMS_CLOUD_LOCALDS_PATH": cloud, "NICO_VMS_VM_VCPUS": "2", "NICO_VMS_VM_MEMORY_MIB": "4096", "NICO_VMS_VM_DISK_GIB": "30",
	}
	for key, value := range binding {
		values[key] = value
	}
	return SetEnv(env, values), nil
}
