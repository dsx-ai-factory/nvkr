// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package execution

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestParseCgroupV2Path(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want string
		code nvkrerr.Code
	}{
		{name: "root", raw: "0::/\n", want: "/"},
		{name: "nested", raw: "0::/user.slice/build.scope\n", want: "/user.slice/build.scope"},
		{name: "v1 only", raw: "2:cpu:/build\n", code: nvkrerr.CodeEngineCapability},
		{name: "missing path", raw: "0::\n", code: nvkrerr.CodeEngineCapability},
		{name: "duplicate unified membership", raw: "0::/a\n0::/b\n", code: nvkrerr.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCgroupV2Path(test.raw)
			if got != test.want || errorCode(err) != test.code {
				t.Fatalf("path = %q, error = %v; want %q, %s", got, err, test.want, test.code)
			}
			if test.name == "v1 only" && (!strings.Contains(err.Error(), "local execution observation requires cgroup v2") || strings.Contains(err.Error(), "VM")) {
				t.Fatalf("shared cgroup refusal is not context-neutral: %v", err)
			}
		})
	}
}

func TestResolveCgroupV2DirUsesMountRootAndMountpoint(t *testing.T) {
	for _, test := range []struct {
		name       string
		membership string
		mountinfo  string
		wantDir    string
		wantRoot   string
		wantDevice cgroupV2Device
		code       nvkrerr.Code
	}{
		{
			name:       "hierarchy root",
			membership: "0::/user.slice/build.scope\n",
			mountinfo:  "25 1 0:29 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n",
			wantDir:    "/sys/fs/cgroup/user.slice/build.scope",
			wantRoot:   "/sys/fs/cgroup",
			wantDevice: cgroupV2Device{major: 0, minor: 29},
		},
		{
			name:       "mounted subtree root",
			membership: "0::/tenant.slice/build.scope\n",
			mountinfo:  "25 1 0:30 /tenant.slice /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n",
			wantDir:    "/sys/fs/cgroup/build.scope",
			wantRoot:   "/sys/fs/cgroup",
			wantDevice: cgroupV2Device{major: 0, minor: 30},
		},
		{
			name:       "alternate escaped mountpoint",
			membership: "0::/build.scope\n",
			mountinfo:  "25 1 7:11 / /run/cgroup\\040view rw - cgroup2 cgroup2 rw\n",
			wantDir:    "/run/cgroup view/build.scope",
			wantRoot:   "/run/cgroup view",
			wantDevice: cgroupV2Device{major: 7, minor: 11},
		},
		{
			name:       "membership outside mounted subtree",
			membership: "0::/other.slice/build.scope\n",
			mountinfo:  "25 1 0:30 /tenant.slice /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n",
			code:       nvkrerr.CodeEngineCapability,
		},
		{
			name:       "ambiguous mounts",
			membership: "0::/build.scope\n",
			mountinfo: "25 1 0:29 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n" +
				"26 1 0:29 / /run/cgroup rw - cgroup2 cgroup2 rw\n",
			code: nvkrerr.CodeEngineCapability,
		},
		{
			name:       "invalid relative mountpoint",
			membership: "0::/build.scope\n",
			mountinfo:  "25 1 0:29 / sys/fs/cgroup rw - cgroup2 cgroup2 rw\n",
			code:       nvkrerr.CodeEngineCapability,
		},
		{
			name:       "no cgroup2 mount",
			membership: "0::/build.scope\n",
			mountinfo:  "25 1 0:29 / /sys/fs/cgroup rw - tmpfs tmpfs rw\n",
			code:       nvkrerr.CodeEngineCapability,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory, root, device, err := resolveCgroupV2Dir(test.membership, test.mountinfo)
			if directory != test.wantDir || root != test.wantRoot || device != test.wantDevice ||
				errorCode(err) != test.code {
				t.Fatalf("location = %q, root = %q, device = %#v, error = %v; want %q, %q, %#v, %s",
					directory, root, device, err, test.wantDir, test.wantRoot, test.wantDevice, test.code)
			}
		})
	}
}

func TestParseCPUMax(t *testing.T) {
	for _, test := range []struct {
		name    string
		raw     string
		want    uint64
		limited bool
		code    nvkrerr.Code
	}{
		{name: "unlimited", raw: "max 100000"},
		{name: "fractional", raw: "150000 100000", want: 1500, limited: true},
		{name: "small", raw: "100 100000", want: 1, limited: true},
		{name: "overflow saturates", raw: "18446744073709551615 1", want: math.MaxUint64, limited: true},
		{name: "zero quota", raw: "0 100000", code: nvkrerr.CodeEngineCapability},
		{name: "zero period", raw: "100000 0", code: nvkrerr.CodeEngineCapability},
		{name: "malformed", raw: "100000", code: nvkrerr.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, limited, err := parseCPUMax(test.raw)
			if got != test.want || limited != test.limited || errorCode(err) != test.code {
				t.Fatalf("quota = %d, limited = %t, error = %v; want %d, %t, %s",
					got, limited, err, test.want, test.limited, test.code)
			}
		})
	}
}

func TestCgroupV2Ancestry(t *testing.T) {
	root, parent, leaf := cgroupCapacityFixture(t)
	got, err := cgroupV2Ancestry(leaf, root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{leaf, parent, root}; !slices.Equal(got, want) {
		t.Fatalf("ancestry = %q, want %q", got, want)
	}
	if _, err := cgroupV2Ancestry(t.TempDir(), root); errorCode(err) != nvkrerr.CodeEngineCapability {
		t.Fatalf("out-of-root ancestry error = %v, want %s", err, nvkrerr.CodeEngineCapability)
	}
}

func TestEffectiveCgroupCPUIncludesEveryAncestor(t *testing.T) {
	for _, test := range []struct {
		name                        string
		rootMax, parentMax, leafMax string
		want                        uint64
	}{
		{name: "affinity is tightest", rootMax: "max 100000", parentMax: "max 100000", leafMax: "max 100000", want: 4000},
		{name: "parent is tightest", rootMax: "300000 100000", parentMax: "150000 100000", leafMax: "200000 100000", want: 1500},
		{name: "root is tightest", rootMax: "50000 100000", parentMax: "max 100000", leafMax: "max 100000", want: 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, parent, leaf := cgroupCapacityFixture(t)
			writeCgroupValue(t, root, "cpu.max", test.rootMax)
			writeCgroupValue(t, parent, "cpu.max", test.parentMax)
			writeCgroupValue(t, leaf, "cpu.max", test.leafMax)
			got, err := effectiveCgroupCPUMillis(leaf, root, 4000)
			if err != nil || got != test.want {
				t.Fatalf("effective CPU = %d, %v; want %d", got, err, test.want)
			}
		})
	}
}

func TestEffectiveCgroupMemoryIncludesEveryAncestor(t *testing.T) {
	const mib = uint64(1024 * 1024)
	for _, test := range []struct {
		name                  string
		rootMax, rootUsed     uint64
		parentMax, parentUsed uint64
		leafMax, leafUsed     uint64
		want                  uint64
	}{
		{name: "host is tightest", want: 2048},
		{name: "parent is tightest", parentMax: 512 * mib, parentUsed: 128 * mib, want: 384},
		{name: "root is tightest", rootMax: 600 * mib, rootUsed: 200 * mib,
			parentMax: 700 * mib, parentUsed: 100 * mib, leafMax: 800 * mib, leafUsed: 100 * mib, want: 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, parent, leaf := cgroupCapacityFixture(t)
			writeCgroupMemory(t, root, test.rootMax, test.rootUsed)
			writeCgroupMemory(t, parent, test.parentMax, test.parentUsed)
			writeCgroupMemory(t, leaf, test.leafMax, test.leafUsed)
			got, err := effectiveCgroupMemoryMiB(leaf, root, 2048)
			if err != nil || got != test.want {
				t.Fatalf("effective memory = %d, %v; want %d", got, err, test.want)
			}
		})
	}
}

func TestEffectiveCgroupCapacityFailsClosedOnMissingControls(t *testing.T) {
	root, parent, leaf := cgroupCapacityFixture(t)
	writeCgroupValue(t, leaf, "cpu.max", "max 100000")
	if _, err := effectiveCgroupCPUMillis(leaf, root, 4000); errorCode(err) != nvkrerr.CodeEngineCapability {
		t.Fatalf("missing ancestor CPU control error = %v, want %s", err, nvkrerr.CodeEngineCapability)
	}
	writeCgroupMemory(t, leaf, 0, 0)
	writeCgroupValue(t, parent, "memory.max", strconv.FormatUint(1024*1024*1024, 10))
	if _, err := effectiveCgroupMemoryMiB(leaf, root, 2048); errorCode(err) != nvkrerr.CodeEngineCapability {
		t.Fatalf("missing ancestor memory control error = %v, want %s", err, nvkrerr.CodeEngineCapability)
	}
}

func TestEffectiveCgroupCapacityAllowsRootWithoutControllerLimits(t *testing.T) {
	const mib = uint64(1024 * 1024)
	root, parent, leaf := cgroupCapacityFixture(t)
	writeCgroupValue(t, parent, "cpu.max", "150000 100000")
	writeCgroupValue(t, leaf, "cpu.max", "200000 100000")
	writeCgroupMemory(t, parent, 512*mib, 128*mib)
	writeCgroupMemory(t, leaf, 800*mib, 100*mib)

	cpu, err := effectiveCgroupCPUMillis(leaf, root, 4000)
	if err != nil || cpu != 1500 {
		t.Fatalf("effective CPU = %d, %v; want 1500 with no root controller file", cpu, err)
	}
	memory, err := effectiveCgroupMemoryMiB(leaf, root, 2048)
	if err != nil || memory != 384 {
		t.Fatalf("effective memory = %d, %v; want 384 with no root controller file", memory, err)
	}
}

func TestEffectiveCgroupMemorySkipsUsageForUnlimitedAncestors(t *testing.T) {
	root, parent, leaf := cgroupCapacityFixture(t)
	writeCgroupValue(t, parent, "memory.max", "max")
	writeCgroupValue(t, leaf, "memory.max", "max")

	memory, err := effectiveCgroupMemoryMiB(leaf, root, 2048)
	if err != nil || memory != 2048 {
		t.Fatalf("effective memory = %d, %v; want host capacity without memory.current", memory, err)
	}
}

func TestEffectiveCgroupCapacityDisabledControllersPreserveAncestors(t *testing.T) {
	for _, controller := range []string{"cpu", "memory"} {
		for _, levels := range []int{1, 2} {
			t.Run(controller+"/"+strconv.Itoa(levels), func(t *testing.T) {
				root, parent, leaf := cgroupCapacityFixture(t)
				writeCgroupValue(t, leaf, "cgroup.controllers", "pids")
				writeCgroupValue(t, parent, "cgroup.subtree_control", "pids")
				limitDirectory := parent
				if levels == 2 {
					writeCgroupValue(t, parent, "cgroup.controllers", "pids")
					writeCgroupValue(t, root, "cgroup.subtree_control", "pids")
					limitDirectory = root
				}
				var got, want uint64
				var err error
				if controller == "cpu" {
					writeCgroupValue(t, limitDirectory, "cpu.max", "150000 100000")
					got, err = effectiveCgroupCPUMillis(leaf, root, 4000)
					want = 1500
				} else {
					writeCgroupMemory(t, limitDirectory, 512*1024*1024, 128*1024*1024)
					got, err = effectiveCgroupMemoryMiB(leaf, root, 2048)
					want = 384
				}
				if err != nil || got != want {
					t.Fatalf("effective %s = %d, %v; want ancestor bound %d", controller, got, err, want)
				}
			})
		}
	}
}

func TestEffectiveCgroupCapacityRejectsUnprovenMissingController(t *testing.T) {
	for _, controller := range []string{"cpu", "memory"} {
		for _, tc := range []struct {
			name          string
			child, parent string
			missingChild  bool
			missingParent bool
		}{
			{name: "child advertises controller", child: controller, parent: "pids"},
			{name: "parent enables controller", child: "pids", parent: controller},
			{name: "child metadata absent", missingChild: true, parent: "pids"},
			{name: "parent metadata absent", child: "pids", missingParent: true},
			{name: "child uses write syntax", child: "+" + controller, parent: "pids"},
			{name: "parent uses write syntax", child: "pids", parent: "-" + controller},
			{name: "child contains NUL", child: "pids\x00", parent: "pids"},
			{name: "parent exceeds bound", child: "pids", parent: strings.Repeat("pids ", 1000)},
		} {
			t.Run(controller+"/"+tc.name, func(t *testing.T) {
				root, parent, leaf := cgroupCapacityFixture(t)
				if !tc.missingChild {
					writeCgroupValue(t, leaf, "cgroup.controllers", tc.child)
				}
				if !tc.missingParent {
					writeCgroupValue(t, parent, "cgroup.subtree_control", tc.parent)
				}
				var err error
				if controller == "cpu" {
					writeCgroupValue(t, parent, "cpu.max", "150000 100000")
					_, err = effectiveCgroupCPUMillis(leaf, root, 4000)
				} else {
					writeCgroupMemory(t, parent, 512*1024*1024, 128*1024*1024)
					_, err = effectiveCgroupMemoryMiB(leaf, root, 2048)
				}
				if errorCode(err) != nvkrerr.CodeEngineCapability {
					t.Fatalf("missing %s control error = %v, want %s", controller, err, nvkrerr.CodeEngineCapability)
				}
			})
		}
	}
}

func TestEffectiveCgroupCapacityNeverSkipsMalformedExistingLimit(t *testing.T) {
	for _, controller := range []string{"cpu", "memory"} {
		t.Run(controller, func(t *testing.T) {
			root, parent, leaf := cgroupCapacityFixture(t)
			writeCgroupValue(t, leaf, "cgroup.controllers", "pids")
			writeCgroupValue(t, parent, "cgroup.subtree_control", "pids")
			writeCgroupValue(t, leaf, controller+".max", "invalid")
			var err error
			if controller == "cpu" {
				_, err = effectiveCgroupCPUMillis(leaf, root, 4000)
			} else {
				writeCgroupValue(t, leaf, "memory.current", "0")
				_, err = effectiveCgroupMemoryMiB(leaf, root, 2048)
			}
			if errorCode(err) != nvkrerr.CodeEngineCapability {
				t.Fatalf("malformed %s limit error = %v, want %s", controller, err, nvkrerr.CodeEngineCapability)
			}
		})
	}
}

func cgroupCapacityFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	leaf := filepath.Join(parent, "leaf")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, parent, leaf
}

func writeCgroupValue(t *testing.T, directory, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCgroupMemory(t *testing.T, directory string, maximum, current uint64) {
	t.Helper()
	maximumText := "max"
	if maximum > 0 {
		maximumText = strconv.FormatUint(maximum, 10)
	}
	writeCgroupValue(t, directory, "memory.max", maximumText)
	writeCgroupValue(t, directory, "memory.current", strconv.FormatUint(current, 10))
}
