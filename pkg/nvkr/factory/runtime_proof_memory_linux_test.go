// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package factory

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// These are diagnostic observations only. They never change VM admission,
// cgroup limits, cache policy or the declared execution class.
func nativeRuntimeProofMemory(t *testing.T, phase string, paths ...string) {
	t.Helper()
	read := func(path string) string {
		file, err := os.Open(path)
		if err != nil {
			t.Logf("memory diagnostic %s: %v", path, err)
			return ""
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, 64<<10))
		if err != nil {
			t.Logf("memory diagnostic %s: %v", path, err)
		}
		return string(raw)
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	t.Logf("memory breakdown at %s: Go heap_alloc=%d heap_sys=%d heap_idle=%d heap_released=%d sys=%d bytes", phase,
		memory.HeapAlloc, memory.HeapSys, memory.HeapIdle, memory.HeapReleased, memory.Sys)
	for _, path := range []string{"/proc/self/status", "/proc/meminfo"} {
		t.Logf("%s:\n%s", path, read(path))
	}
	for _, line := range strings.Split(read("/proc/self/cgroup"), "\n") {
		if !strings.HasPrefix(line, "0::/") {
			continue
		}
		root := "/sys/fs/cgroup"
		directory := filepath.Join(root, strings.TrimPrefix(line, "0::"))
		if directory != root && !strings.HasPrefix(directory, root+"/") {
			t.Logf("unavailable cgroup ancestor: %s", line)
			continue
		}
		for {
			for _, name := range []string{"memory.current", "memory.max", "memory.stat", "memory.events"} {
				path := filepath.Join(directory, name)
				t.Logf("%s:\n%s", path, read(path))
			}
			if directory == root {
				break
			}
			directory = filepath.Dir(directory)
		}
	}
	for _, line := range strings.Split(read("/proc/self/mountinfo"), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 6 && (strings.Contains(line, " - tmpfs ") || strings.Contains(line, " - ramfs ")) {
			paths = append(paths, fields[4])
		}
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		var stat unix.Statfs_t
		if err := unix.Statfs(path, &stat); err != nil {
			t.Logf("filesystem %s: %v", path, err)
			continue
		}
		t.Logf("filesystem %s type=%#x block_bytes=%d total_blocks=%d free_blocks=%d available_blocks=%d", path,
			stat.Type, stat.Bsize, stat.Blocks, stat.Bfree, stat.Bavail)
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Logf("descriptor memory diagnostic: %v", err)
		return
	}
	var memfdBytes, unlinkedDiskBytes int64
	for _, entry := range entries {
		path := filepath.Join("/proc/self/fd", entry.Name())
		target, err := os.Readlink(path)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.Size() >= 1<<30 {
			var stat unix.Stat_t
			var filesystem unix.Statfs_t
			if statErr := unix.Stat(path, &stat); statErr == nil {
				if fsErr := unix.Statfs(path, &filesystem); fsErr == nil {
					t.Logf("large retained file: bytes=%d uid=%d mode=%#o links=%d filesystem=%#x", info.Size(), stat.Uid, stat.Mode&0o777, stat.Nlink, filesystem.Type)
				}
			}
		}
		if strings.HasPrefix(target, "/memfd:") {
			memfdBytes += info.Size()
		} else if strings.HasSuffix(target, " (deleted)") {
			unlinkedDiskBytes += info.Size()
		}
	}
	t.Logf("retained descriptor logical sizes: memfd=%d unlinked_non_memfd=%d bytes (descriptor totals, not physical allocation)", memfdBytes, unlinkedDiskBytes)
}
