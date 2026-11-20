// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
)

func TestRuntimeGuestSeedBytes(t *testing.T) {
	// Real-mode x86: initialize DS/SS/SP, wait ten seconds via BIOS int 15h,
	// write this sector to LBA 8 via int 13h, then request QEMU PC ACPI shutdown.
	// A failed write halts without shutdown, so Packer times out.
	code, err := hex.DecodeString("fa31c08ed88ed0bc007cfbb99800ba8096b486cd15be807cb280b80043cd137207b80020ba0406effaf4ebfd")
	if err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 65536)
	copy(want, code)
	// Disk address packet: one sector at 0000:7c00 -> LBA 8.
	copy(want[0x80:0x90], []byte{0x10, 0, 1, 0, 0, 0x7c, 0, 0, 8, 0, 0, 0, 0, 0, 0, 0})
	want[510], want[511] = 0x55, 0xaa
	got, err := os.ReadFile("testdata/runtime-proof/seed.raw")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("committed guest differs from its complete instruction bytes and address packet")
	}
	checksum, err := os.ReadFile("testdata/runtime-proof/seed.raw.sha256")
	if err != nil {
		t.Fatal(err)
	}
	if string(checksum) != fmt.Sprintf("%x  seed.raw\n", sha256.Sum256(want)) {
		t.Fatal("committed guest checksum differs")
	}
}
