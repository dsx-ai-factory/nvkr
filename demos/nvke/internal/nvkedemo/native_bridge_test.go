// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestNativeBridgeUsesObservedPrivateNetwork(t *testing.T) {
	for _, row := range []struct {
		name, start, end, mask string
		valid                  bool
	}{
		{"Apple default", "192.168.64.1", "192.168.64.254", "255.255.255.0", true},
		{"different shared network", "10.24.8.1", "10.24.8.254", "255.255.255.0", true},
		{"foreign end", "192.168.64.1", "192.168.65.254", "255.255.255.0", false},
		{"public gateway", "8.8.8.1", "8.8.8.254", "255.255.255.0", false},
		{"network gateway", "192.168.64.0", "192.168.64.254", "255.255.255.0", false},
		{"broadcast end", "192.168.64.1", "192.168.64.255", "255.255.255.0", false},
		{"invalid mask", "192.168.64.1", "192.168.64.254", "255.0.255.0", false},
		{"IPv6", "fd00::1", "fd00::fe", "255.255.255.0", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			raw := []byte(`{"vmnet_start_address":"` + row.start + `","vmnet_end_address":"` + row.end + `","vmnet_subnet_mask":"` + row.mask + `"}`)
			got, err := decodeNativeBridge(raw)
			if (err == nil) != row.valid {
				t.Fatalf("bridge=%+v error=%v", got, err)
			}
			if row.valid && (got.gateway.String() != row.start || got.last.String() != row.end || !got.dhcp) {
				t.Fatal("observed network changed")
			}
		})
	}
}

func TestNativeBridgeActualLifecycle(t *testing.T) {
	tools := os.Getenv("NVKR_TEST_VMNET_TOOLS")
	if tools == "" {
		t.Skip("opt-in actual native vmnet interface lifecycle")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("Apple vmnet requires Mac")
	}
	tools, err := filepath.Abs(tools)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	work := t.TempDir()
	if err := os.Chmod(work, 0700); err != nil {
		t.Fatal(err)
	}
	owner, err := startNativeBridge(ctx, Runner{Work: work, Env: NativeEnvironment()}, "darwin", tools)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := owner.Stop(cleanup); err != nil {
			t.Error(err)
		}
	})
	if !owner.config.gateway.IsPrivate() || !owner.config.prefix.Contains(owner.config.last) {
		t.Fatal("unobserved bridge")
	}
	if err := owner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if exited, err := owner.process.Exited(); !exited || err != nil {
		t.Fatalf("helper not joined: %v %v", exited, err)
	}
	select {
	case <-owner.done:
	default:
		t.Fatal("network drain remains running")
	}
}
