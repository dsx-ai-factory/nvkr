// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestNativeEnvironmentExcludesForeignTargets(t *testing.T) {
	for key, value := range map[string]string{"DOCKER_CONTEXT": "foreign", "DOCKER_HOST": "tcp://foreign", "KUBECONFIG": "foreign", "GIT_CONFIG_COUNT": "1", "PYTHONPATH": "foreign", "HOME": "foreign", "CAPI_VERSION": "foreign"} {
		t.Setenv(key, value)
	}
	env := NativeEnvironment()
	for _, key := range []string{"DOCKER_CONTEXT", "KUBECONFIG", "GIT_CONFIG_COUNT", "PYTHONPATH", "HOME", "CAPI_VERSION"} {
		if environmentValue(env, key) != "" {
			t.Fatalf("inherited %s", key)
		}
	}
	if environmentValue(env, "DOCKER_HOST") != "unix:///var/run/docker.sock" || environmentValue(env, "KIND_EXPERIMENTAL_PROVIDER") != "docker" {
		t.Fatal("foreign Docker target")
	}
	updated := SetEnv(env, map[string]string{"HOME": "private", "PATH": "/private/tools"})
	if environmentValue(updated, "HOME") != "private" || environmentValue(env, "HOME") != "" {
		t.Fatal("environment mutation")
	}
	if !slices.IsSorted(updated) {
		t.Fatal("environment not sorted")
	}
}

func TestRegularProducerMembershipRejectsSubstitution(t *testing.T) {
	for _, name := range []string{"exact", "different", "duplicate", "symlink", "packagesSymlink"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			layout := filepath.Join(base, "packages", "tool")
			if err := os.MkdirAll(layout, 0o700); err != nil {
				t.Fatal(err)
			}
			digest := "sha256:" + strings.Repeat("a", 64)
			digests := []string{digest}
			if name == "different" {
				digests[0] = "sha256:" + strings.Repeat("b", 64)
			}
			if name == "duplicate" {
				digests = append(digests, digest)
			}
			manifests := make([]map[string]string, 0)
			for _, pin := range digests {
				manifests = append(manifests, map[string]string{"digest": pin})
			}
			raw, _ := json.Marshal(map[string]any{"manifests": manifests})
			if err := os.WriteFile(filepath.Join(layout, "index.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if name == "symlink" {
				if err := os.Symlink(filepath.Join(layout, "index.json"), filepath.Join(layout, "foreign")); err != nil {
					t.Fatal(err)
				}
			}
			if name == "packagesSymlink" {
				moved := filepath.Join(base, "external")
				if err := os.Rename(filepath.Join(base, "packages"), moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, filepath.Join(base, "packages")); err != nil {
					t.Fatal(err)
				}
			}
			_, err := diagnosticIndexDigest(t.Context(), layout, digest)
			if (err == nil) != (name == "exact") {
				t.Fatalf("index acceptance:%v", err)
			}
		})
	}
}

func TestPrivateWriteNeverReplaces(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "value")
	if err := privateWrite(path, []byte("accepted")); err != nil {
		t.Fatal(err)
	}
	if err := privateWrite(path, []byte("foreign")); err == nil {
		t.Fatal("replaced file")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "accepted" {
		t.Fatal("accepted bytes changed")
	}
}

// Observe actual copied bytes, not a count of context polls or copy chunks.
// This uses the same synchronous fault-injection pattern as transportarchive.
type privateCopyContext struct {
	context.Context
	observe func()
}

func (ctx privateCopyContext) Err() error {
	ctx.observe()
	return ctx.Context.Err()
}

func TestPrivateCopyPreservesFileContract(t *testing.T) {
	for _, scenario := range []string{"same", "occupied", "symlink", "canceled", "grown", "truncated", "canceled partial"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
			payload := strings.Repeat("private-copy-data", 1<<17)
			writeTenantFixture(t, source, []byte(payload), 0644)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "occupied":
				writeTenantFixture(t, destination, []byte("existing"), 0640)
			case "symlink":
				link := source + "-link"
				if err := os.Symlink(source, link); err != nil {
					t.Fatal(err)
				}
				source = link
			case "canceled":
				cancel()
			}
			injected := false
			observed := privateCopyContext{Context: ctx, observe: func() {
				if injected || scenario != "grown" && scenario != "truncated" && scenario != "canceled partial" {
					return
				}
				info, err := os.Stat(destination)
				if os.IsNotExist(err) || err == nil && info.Size() == 0 {
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				injected = true
				switch scenario {
				case "grown":
					err = os.Truncate(source, int64(len(payload))+1)
				case "truncated":
					err = os.Truncate(source, info.Size())
				case "canceled partial":
					cancel()
				}
				if err != nil {
					t.Fatal(err)
				}
			}}
			err := copyPrivate(observed, source, destination, 0755)
			if (err == nil) != (scenario == "same") {
				t.Fatalf("private copy %s: %v", scenario, err)
			}
			if err != nil {
				want := nvkrerr.CodePreflightFailed
				if errors.Is(err, context.Canceled) {
					want = nvkrerr.CodeTimeout
				}
				var coded *nvkrerr.Error
				if !errors.As(nativeSessionError(err, "private copy"), &coded) || coded.Code != want {
					t.Fatal("copy changed the existing caller error mapping", err)
				}
			}
			if scenario == "grown" || scenario == "truncated" || scenario == "canceled partial" {
				if !injected {
					t.Fatal("copy did not exercise the injected failure")
				}
			}
			if scenario == "symlink" {
				if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
					t.Fatal("rejected source created output", statErr)
				}
				return
			}
			data, readErr := os.ReadFile(destination)
			info, statErr := os.Stat(destination)
			if readErr != nil || statErr != nil {
				t.Fatal("copy lost its destination", readErr, statErr)
			}
			switch scenario {
			case "same":
				if string(data) != payload || info.Mode().Perm() != 0755 {
					t.Fatal("copy changed successful bytes or mode")
				}
			case "occupied":
				if string(data) != "existing" || info.Mode().Perm() != 0640 {
					t.Fatal("copy replaced existing bytes or mode")
				}
			default:
				if info.Mode().Perm() != 0600 || len(data) > len(payload) {
					t.Fatal("failed copy exposed or extended partial bytes")
				}
			}
			if scenario == "canceled" || scenario == "canceled partial" {
				if !errors.Is(err, context.Canceled) || scenario == "canceled" && len(data) != 0 || scenario == "canceled partial" && (len(data) == 0 || len(data) >= len(payload)) {
					t.Fatal("copy lost cancellation or changed partial-output behavior", err)
				}
			}
		})
	}
}
