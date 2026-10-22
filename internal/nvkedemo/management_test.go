// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
