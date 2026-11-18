// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package rootfs

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDiskLayerFilesystem(t *testing.T) {
	raw := []byte("runtime")
	layer, err := captureDiskLayer(t.Context(), bytes.NewReader(raw), fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), int64(len(raw)), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer layer.Close()
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(int(layer.file.Fd()), &filesystem); err != nil {
		t.Fatal(err)
	}
	if filesystem.Type == unix.TMPFS_MAGIC || filesystem.Type == unix.RAMFS_MAGIC {
		t.Fatal("retained layer in memory")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(layer.file.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Nlink != 0 || stat.Uid != uint32(os.Geteuid()) {
		t.Fatalf("retained layer ownership/links: uid=%d links=%d", stat.Uid, stat.Nlink)
	}
}
