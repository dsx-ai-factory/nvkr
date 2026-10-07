// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func TestSourceVendorPodmanNetworkHelperSelection(t *testing.T) {
	for _, test := range []struct {
		name, raw, helper string
		code              e.Code
	}{
		{"rootful unchanged", `{"host":{"security":{"rootless":false}}}`, "", ""},
		{"remote unchanged", `{"host":{"serviceIsRemote":true}}`, "", ""},
		{"pasta", `{"host":{"security":{"rootless":true},"rootlessNetworkCmd":"pasta"}}`, "pasta", ""},
		{"slirp", `{"host":{"security":{"rootless":true},"rootlessNetworkCmd":"slirp4netns"}}`, "slirp4netns", ""},
		{"missing privilege", `{"host":{}}`, "", e.CodeEngineCapability},
		{"missing helper", `{"host":{"security":{"rootless":true}}}`, "", e.CodeEngineCapability},
		{"unknown helper", `{"host":{"security":{"rootless":true},"rootlessNetworkCmd":"custom"}}`, "", e.CodeEngineCapability},
		{"duplicate privilege", `{"host":{"security":{"rootless":true,"rootless":false}}}`, "", e.CodeEngineCapability},
		{"malformed", `{`, "", e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := sourceVendorPodmanNetworkHelper([]byte(test.raw))
			if got != test.helper || code(err) != test.code {
				t.Fatalf("helper=%q, error=%v", got, err)
			}
		})
	}
}

func TestSourceVendorSystemHelperProtection(t *testing.T) {
	for _, test := range []struct {
		name      string
		uid, mode uint32
		directory bool
		code      e.Code
	}{
		{"protected directory", 0, unix.S_IFDIR | 0o755, true, ""},
		{"protected executable", 0, unix.S_IFREG | 0o755, false, ""},
		{"foreign owner", 502, unix.S_IFREG | 0o755, false, e.CodeEngineCapability},
		{"group writable", 0, unix.S_IFDIR | 0o775, true, e.CodeEngineCapability},
		{"world writable", 0, unix.S_IFREG | 0o757, false, e.CodeEngineCapability},
		{"not executable", 0, unix.S_IFREG | 0o644, false, e.CodeEngineCapability},
		{"not directory", 0, unix.S_IFREG | 0o755, true, e.CodeEngineCapability},
		{"not regular", 0, unix.S_IFDIR | 0o755, false, e.CodeEngineCapability},
		{"symlink", 0, unix.S_IFLNK | 0o755, false, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := sourceVendorSystemHelperProtection(unix.Stat_t{Uid: test.uid, Mode: test.mode}, test.directory)
			if code(err) != test.code {
				t.Fatalf("wrong protection admission: %v", err)
			}
		})
	}
}

func TestSourceVendorSystemHelperRejectsActualUnsafeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(path, []byte("not executed"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o777); err != nil {
		t.Fatal(err)
	}
	file, _, err := securepath.OpenRegular(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := sourceVendorSystemHelperFile(file, false); code(err) != e.CodeEngineCapability {
		t.Fatalf("actual writable/foreign-owned helper accepted: %v", err)
	}
}

func TestSourceVendorPodmanHelpersCancelBeforeRuntimeWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	held := &heldRuntimeExecutable{name: "podman", environment: []string{"PATH=/nonexistent"}}
	if err := admitSourceVendorPodmanHelpers(ctx, held); code(err) != e.CodeTimeout {
		t.Fatalf("canceled admission reached runtime work: %v", err)
	}
	if !slices.Equal(held.environment, []string{"PATH=/nonexistent"}) {
		t.Fatal("rejected helper admission changed the closed runtime environment")
	}
}
