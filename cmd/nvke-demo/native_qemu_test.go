// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"reflect"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/nativeqemu"
)

func TestNativeQEMULauncherPreservesOpaqueArguments(t *testing.T) {
	args := []string{"-machine", "virt,accel=hvf", "-name", "guest name", "-netdev", "dgram,id=net0,local.type=fd,local.str=4"}
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"/private/adapter/" + nativeqemu.LauncherName, append([]string{"native-qemu"}, args...)},
		{"/private/bin/nvke-demo", args},
	} {
		if got := launcherArguments(tc.name, args); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%q: %#v", tc.name, got)
		}
	}
	command, _, err := newCommand(io.Discard, io.Discard, demoActions{}).Find([]string{"native-qemu"})
	if err != nil || !command.Hidden || !command.DisableFlagParsing {
		t.Fatal("launcher must remain hidden and preserve QEMU arguments", err)
	}
}
