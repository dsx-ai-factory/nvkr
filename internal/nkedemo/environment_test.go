// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestDaemonEnvironmentBindings(t *testing.T) {
	bound := map[string]string{"HOME": "/account", "XDG_RUNTIME_DIR": "/run/user/501"}
	for _, test := range []struct {
		name, raw string
		fail      bool
	}{{"native", "HOME=/account\x00XDG_RUNTIME_DIR=/run/user/501\x00", false}, {"defaults", "HOME=/account\x00SECRET=hidden\x00", false}, {"foreign", "HOME=/foreign\x00", true}, {"duplicate", "HOME=/account\x00HOME=/account\x00", true}, {"oversize", strings.Repeat("x", (1<<20)+1), true}} {
		t.Run(test.name, func(t *testing.T) {
			err := checkDaemonBindings([]byte(test.raw), bound)
			if (err != nil) != test.fail {
				t.Fatalf("error=%v", err)
			}
			if err != nil && strings.Contains(err.Error(), "hidden") {
				t.Fatal("unrelated credential leaked")
			}
		})
	}
}
func TestExactSeedProjection(t *testing.T) {
	block := []byte(fmt.Sprintf("  - id: %q\n    name: \"cpu4-mem6gb-disk60gb\"\n    site_id: %q\n    gpu_count: 0\n    cpu_count: 4\n    memory_gib: 6\n    disk_gib: 60\n", TenantInstanceType, SiteID))
	raw := append(append([]byte("# prefix unchanged\n"), block...), []byte("# suffix unchanged\n")...)
	before := bytes.Clone(raw)
	projected, err := projectSeedFixture(raw, plainSHA(raw))
	if err != nil {
		t.Fatal(err)
	}
	expected := bytes.Replace(bytes.Replace(raw, []byte("mem6gb"), []byte("mem8gb"), 1), []byte("memory_gib: 6"), []byte("memory_gib: 8"), 1)
	if !bytes.Equal(projected, expected) || !bytes.Equal(raw, before) {
		t.Fatal("projection changed nonselected bytes or source")
	}
	for _, test := range []struct {
		name   string
		data   []byte
		digest string
	}{{"changed", raw, strings.Repeat("f", 64)}, {"duplicate", append(bytes.Clone(raw), block...), plainSHA(append(bytes.Clone(raw), block...))}, {"missing", []byte("missing"), plainSHA([]byte("missing"))}, {"oversize", append(bytes.Clone(raw), make([]byte, 65536)...), plainSHA(append(bytes.Clone(raw), make([]byte, 65536)...))}} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := projectSeedFixture(test.data, test.digest); err == nil {
				t.Fatal("unsafe fixture accepted")
			}
		})
	}
}
func TestSimulatorEnvironmentExcludesAmbientAuthority(t *testing.T) {
	t.Setenv("HOME", "/foreign")
	t.Setenv("XDG_RUNTIME_DIR", "/foreign")
	t.Setenv("LD_PRELOAD", "/foreign")
	t.Setenv("NICO_VMS_VM_BACKEND", "mock")
	work := t.TempDir()
	tools := filepath.Join(work, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"qemu-img", "cloud-localds"} {
		if err := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\nexit0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", tools)
	env, err := simulatorEnvironment(t.Context(), work, "/held/disk", work, "172.18.0.1", map[string]string{"HOME": "/admitted", "XDG_RUNTIME_DIR": "/run/user/501"})
	if err != nil {
		t.Fatal(err)
	}
	for key, expected := range map[string]string{"HOME": "/admitted", "XDG_RUNTIME_DIR": "/run/user/501", "NICO_VMS_VM_BACKEND": "libvirt", "NICO_SIM_SEED_PATH": filepath.Join(work, "laptop-seed.yaml"), "NICO_VMS_DIAG_SSH_ENABLED": "true"} {
		if got := environmentValue(env, key); got != expected {
			t.Fatalf("%s=%q", key, got)
		}
	}
	if len(environmentValue(env, "NICO_VMS_DIAG_SSH_PASSWORD")) != 48 || environmentValue(env, "LD_PRELOAD") != "" {
		t.Fatal("unsafe simulator environment")
	}
}
func TestDemoAdmissionHasNoPartialOutput(t *testing.T) {
	for _, budget := range []time.Duration{5 * time.Minute, time.Hour, 2 * time.Hour, 299 * time.Second, 7201 * time.Second, time.Duration(9223372036854775807)} {
		t.Run(budget.String(), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "new")
			var progress bytes.Buffer
			cfg := Config{Out: out, Timeout: budget, Progress: &progress}
			// Exercise the owning admission directly so macOS's earlier platform
			// check cannot hide invalid budgets from this boundary test.
			_, admissionErr := admitDemo(t.Context(), cfg)
			invalid := budget < 5*time.Minute || budget > 2*time.Hour
			if admissionErr == nil || strings.Contains(admissionErr.Error(), "tenant budget") != invalid {
				t.Fatalf("budget validation masked other checks or accepted invalid input: %v", admissionErr)
			}
			err := Run(context.Background(), cfg)
			if nkderr.ExitCode(err) != 17 {
				t.Fatalf("expected coded preflight error, got %v", err)
			}
			if _, statErr := os.Lstat(out); !os.IsNotExist(statErr) {
				t.Fatal("failed admission created output")
			}
			if strings.Contains(progress.String(), "PASS:") {
				t.Fatal("failed admission emitted PASS")
			}
		})
	}
}
