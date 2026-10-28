// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package execution

import (
	"bufio"
	"errors"
	"io"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/moby/sys/mountinfo"
	"golang.org/x/sys/unix"
)

const (
	procSelfCgroup    = "/proc/self/cgroup"
	procSelfMountinfo = "/proc/self/mountinfo"
	procMeminfo       = "/proc/meminfo"
	maxMountinfoBytes = 4 << 20
)

func currentCgroupV2Location() (string, string, error) {
	membershipRaw, err := readBoundedKernelText(procSelfCgroup, 4096)
	if err != nil {
		return "", "", e.Wrap(e.CodeEngineCapability, err,
			"reading local cgroup-v2 membership")
	}
	mountinfoRaw, err := readBoundedKernelText(procSelfMountinfo, maxMountinfoBytes)
	if err != nil {
		return "", "", e.Wrap(e.CodeEngineCapability, err,
			"reading local mount table")
	}
	directory, root, device, err := resolveCgroupV2Dir(membershipRaw, mountinfoRaw)
	if err != nil {
		return "", "", err
	}
	if err := authenticateCgroupV2Directory(directory, device); err != nil {
		return "", "", err
	}
	return directory, root, nil
}

func parseCgroupV2Path(raw string) (string, error) {
	found := ""
	matched := false
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "0::") {
			continue
		}
		if matched {
			return "", e.New(e.CodeEngineCapability,
				"local runtime reports multiple cgroup-v2 memberships")
		}
		value := strings.TrimPrefix(line, "0::")
		if value == "" || strings.ContainsRune(value, '\x00') {
			return "", e.New(e.CodeEngineCapability,
				"local runtime cgroup-v2 membership is malformed")
		}
		clean := filepath.Clean(filepath.FromSlash("/" + strings.TrimPrefix(value, "/")))
		found = filepath.ToSlash(clean)
		matched = true
	}
	if !matched {
		return "", e.New(e.CodeEngineCapability,
			"local execution observation requires cgroup v2")
	}
	return found, nil
}

type cgroupV2Device struct {
	major uint32
	minor uint32
}

func resolveCgroupV2Dir(membershipRaw, mountinfoRaw string) (string, string, cgroupV2Device, error) {
	membership, err := parseCgroupV2Path(membershipRaw)
	if err != nil {
		return "", "", cgroupV2Device{}, err
	}
	mounts, err := mountinfo.GetMountsFromReader(strings.NewReader(mountinfoRaw),
		mountinfo.FSTypeFilter("cgroup2"))
	if err != nil {
		return "", "", cgroupV2Device{}, e.Wrap(e.CodeEngineCapability, err,
			"parsing local cgroup-v2 mount table")
	}
	var selected string
	var selectedRoot string
	var device cgroupV2Device
	for _, mount := range mounts {
		root, ok := canonicalAbsolutePath(mount.Root)
		if !ok {
			return "", "", cgroupV2Device{}, e.New(e.CodeEngineCapability,
				"local cgroup-v2 mount has invalid root %q", mount.Root)
		}
		mountpoint, ok := canonicalAbsolutePath(mount.Mountpoint)
		if !ok || mount.Major < 0 || mount.Minor < 0 ||
			uint64(mount.Major) > math.MaxUint32 || uint64(mount.Minor) > math.MaxUint32 {
			return "", "", cgroupV2Device{}, e.New(e.CodeEngineCapability,
				"local cgroup-v2 mount has invalid mountpoint or device")
		}
		relative, ok := pathRelativeTo(root, membership)
		if !ok {
			continue
		}
		candidate := filepath.Join(mountpoint, relative)
		if _, ok := pathRelativeTo(mountpoint, candidate); !ok {
			return "", "", cgroupV2Device{}, e.New(e.CodeEngineCapability,
				"local cgroup-v2 membership escapes its mountpoint")
		}
		if selected != "" {
			return "", "", cgroupV2Device{}, e.New(e.CodeEngineCapability,
				"local cgroup-v2 membership maps to multiple mounts")
		}
		selected = candidate
		selectedRoot = mountpoint
		device = cgroupV2Device{major: uint32(mount.Major), minor: uint32(mount.Minor)}
	}
	if selected == "" {
		return "", "", cgroupV2Device{}, e.New(e.CodeEngineCapability,
			"local cgroup-v2 membership has no matching mount")
	}
	return selected, selectedRoot, device, nil
}

func canonicalAbsolutePath(value string) (string, bool) {
	if value == "" || strings.ContainsRune(value, '\x00') || !filepath.IsAbs(value) {
		return "", false
	}
	clean := filepath.Clean(value)
	return clean, clean == value
}

func pathRelativeTo(root, target string) (string, bool) {
	relative, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(relative) || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}

func authenticateCgroupV2Directory(directory string, device cgroupV2Device) error {
	var filesystem unix.Statfs_t
	if err := unix.Statfs(directory, &filesystem); err != nil {
		return e.Wrap(e.CodeEngineCapability, err,
			"inspecting resolved cgroup-v2 directory")
	}
	if filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return e.New(e.CodeEngineCapability,
			"resolved local cgroup directory is not cgroup v2")
	}
	var stat unix.Stat_t
	if err := unix.Stat(directory, &stat); err != nil {
		return e.Wrap(e.CodeEngineCapability, err,
			"authenticating resolved cgroup-v2 device")
	}
	if unix.Major(stat.Dev) != device.major || unix.Minor(stat.Dev) != device.minor {
		return e.New(e.CodeEngineCapability,
			"resolved local cgroup directory differs from its mount-table device")
	}
	return nil
}

func effectiveCgroupCPUMillis(cgroupDir, cgroupRoot string, affinityMillis uint64) (uint64, error) {
	if affinityMillis == 0 {
		return 0, e.New(e.CodeEngineCapability,
			"local VM admission has no CPU affinity capacity")
	}
	ancestry, err := cgroupV2Ancestry(cgroupDir, cgroupRoot)
	if err != nil {
		return 0, err
	}
	effective := affinityMillis
	root := filepath.Clean(cgroupRoot)
	for _, directory := range ancestry {
		raw, err := readBoundedKernelText(filepath.Join(directory, "cpu.max"), 128)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				if directory == root {
					continue
				}
				disabled, controllerErr := cgroupControllerDisabled(directory, "cpu")
				if controllerErr != nil {
					return 0, controllerErr
				}
				if disabled {
					continue
				}
			}
			return 0, e.Wrap(e.CodeEngineCapability, err,
				"reading cgroup CPU quota at %s", directory)
		}
		quota, limited, err := parseCPUMax(strings.TrimSpace(raw))
		if err != nil {
			return 0, e.Wrap(e.CodeEngineCapability, err,
				"parsing cgroup CPU quota at %s", directory)
		}
		if limited {
			effective = min(effective, quota)
		}
	}
	if effective == 0 {
		return 0, e.New(e.CodeEngineCapability,
			"local VM admission has no effective CPU capacity")
	}
	return effective, nil
}

func parseCPUMax(raw string) (uint64, bool, error) {
	fields := strings.Fields(raw)
	if len(fields) != 2 {
		return 0, false, e.New(e.CodeEngineCapability,
			"effective cgroup CPU quota is malformed")
	}
	period, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || period == 0 {
		return 0, false, e.New(e.CodeEngineCapability,
			"effective cgroup CPU period is malformed")
	}
	if fields[0] == "max" {
		return 0, false, nil
	}
	quota, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil || quota == 0 {
		return 0, false, e.New(e.CodeEngineCapability,
			"effective cgroup CPU quota is malformed")
	}
	high, low := bits.Mul64(quota, 1000)
	if high >= period {
		return math.MaxUint64, true, nil
	}
	millis, _ := bits.Div64(high, low, period)
	if millis == 0 {
		return 0, false, e.New(e.CodeEngineCapability,
			"effective cgroup CPU quota is below one millicpu")
	}
	return millis, true, nil
}

func effectiveCgroupMemoryMiB(cgroupDir, cgroupRoot string, hostAvailable uint64) (uint64, error) {
	if hostAvailable == 0 {
		return 0, e.New(e.CodeEngineCapability,
			"local VM admission has no host-available memory")
	}
	ancestry, err := cgroupV2Ancestry(cgroupDir, cgroupRoot)
	if err != nil {
		return 0, err
	}
	effective := hostAvailable
	root := filepath.Clean(cgroupRoot)
	for _, directory := range ancestry {
		maximumRaw, err := readBoundedKernelText(filepath.Join(directory, "memory.max"), 128)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				if directory == root {
					continue
				}
				disabled, controllerErr := cgroupControllerDisabled(directory, "memory")
				if controllerErr != nil {
					return 0, controllerErr
				}
				if disabled {
					continue
				}
			}
			return 0, e.Wrap(e.CodeEngineCapability, err,
				"reading cgroup memory limit at %s", directory)
		}
		maximum := strings.TrimSpace(maximumRaw)
		if maximum == "max" {
			continue
		}
		currentRaw, err := readBoundedKernelText(filepath.Join(directory, "memory.current"), 128)
		if err != nil {
			return 0, e.Wrap(e.CodeEngineCapability, err,
				"reading cgroup memory use at %s", directory)
		}
		current, err := strconv.ParseUint(strings.TrimSpace(currentRaw), 10, 64)
		if err != nil {
			return 0, e.New(e.CodeEngineCapability,
				"cgroup memory use at %s is malformed", directory)
		}
		limit, err := strconv.ParseUint(maximum, 10, 64)
		if err != nil || limit == 0 {
			return 0, e.New(e.CodeEngineCapability,
				"cgroup memory limit at %s is malformed", directory)
		}
		if current >= limit {
			return 0, e.New(e.CodeEngineCapability,
				"local VM admission has no memory headroom at cgroup %s", directory)
		}
		effective = min(effective, (limit-current)/(1024*1024))
	}
	if effective == 0 {
		return 0, e.New(e.CodeEngineCapability,
			"local VM admission has less than one MiB of effective memory")
	}
	return effective, nil
}

// A controller's interface files exist only where its parent enables it in
// cgroup.subtree_control. A missing local limit is therefore not unlimited:
// require both kernel lists to confirm that this controller is disabled here,
// while the caller continues checking all ancestor limits. This does not relax
// the separate container verifier's requirement for enforced local ceilings.
func cgroupControllerDisabled(directory, controller string) (bool, error) {
	for _, path := range []string{
		filepath.Join(directory, "cgroup.controllers"),
		filepath.Join(filepath.Dir(directory), "cgroup.subtree_control"),
	} {
		raw, err := readBoundedKernelText(path, 4096)
		if err != nil {
			return false, e.Wrap(e.CodeEngineCapability, err,
				"reading cgroup controller availability at %s", path)
		}
		for _, field := range strings.Fields(raw) {
			// The read ABI contains names; +/- prefixes are write operations.
			if strings.ContainsRune(field, '\x00') || strings.HasPrefix(field, "+") || strings.HasPrefix(field, "-") {
				return false, e.New(e.CodeEngineCapability,
					"cgroup controller availability at %s is malformed", path)
			}
			if field == controller {
				return false, nil
			}
		}
	}
	return true, nil
}

func cgroupV2Ancestry(cgroupDir, cgroupRoot string) ([]string, error) {
	root := filepath.Clean(cgroupRoot)
	leaf := filepath.Clean(cgroupDir)
	relative, err := filepath.Rel(root, leaf)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return nil, e.New(e.CodeEngineCapability,
			"process cgroup %s is outside cgroup-v2 root %s", leaf, root)
	}
	ancestry := make([]string, 0, strings.Count(relative, string(filepath.Separator))+1)
	for directory := leaf; ; directory = filepath.Dir(directory) {
		ancestry = append(ancestry, directory)
		if directory == root {
			break
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return nil, e.New(e.CodeEngineCapability,
				"process cgroup %s has no cgroup-v2 root %s ancestor", leaf, root)
		}
	}
	return ancestry, nil
}

func readHostAvailableMemoryMiB() (uint64, error) {
	file, err := os.Open(procMeminfo)
	if err != nil {
		return 0, e.Wrap(e.CodeEngineCapability, err,
			"reading host available memory")
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, 64<<10))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 || fields[0] != "MemAvailable:" || fields[2] != "kB" {
			continue
		}
		kib, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || kib < 1024 {
			return 0, e.New(e.CodeEngineCapability,
				"host available memory is malformed or below one MiB")
		}
		return kib / 1024, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, e.Wrap(e.CodeEngineCapability, err,
			"reading host available memory")
	}
	return 0, e.New(e.CodeEngineCapability,
		"host available memory is absent from /proc/meminfo")
}

func readBoundedKernelText(path string, maximum int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return "", err
	}
	if int64(len(raw)) > maximum {
		return "", e.New(e.CodeEngineCapability,
			"kernel control file %s exceeds %d bytes", path, maximum)
	}
	return string(raw), nil
}
