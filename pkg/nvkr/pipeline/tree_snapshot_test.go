// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOCISnapshotRejectsInvalidLayout(t *testing.T) {
	root := t.TempDir()
	invalid := filepath.Join(root, "invalid.oci")
	valid := filepath.Join(root, "valid.oci")
	writeInvalidRegularOCITree(t, invalid)
	digest := writeMinimalOCILayout(t, valid)
	snapshot, err := snapshotRegularTreeContext(t.Context(), invalid)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyOCILayoutSource(t.Context(), snapshot, digest); err == nil {
		t.Fatal("invalid captured OCI bytes passed verification")
	}
}

func TestOCISnapshotRetainsBytesAfterPathReplacement(t *testing.T) {
	live := filepath.Join(t.TempDir(), "transport.oci")
	digest := writeMinimalOCILayout(t, live)
	snapshot, err := snapshotRegularTreeContext(t.Context(), live)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(live, live+".valid"); err != nil {
		t.Fatal(err)
	}
	writeInvalidRegularOCITree(t, live)
	if err := verifyOCILayoutSource(t.Context(), snapshot, digest); err != nil {
		t.Fatalf("verification reopened the replaced path instead of captured bytes: %v", err)
	}
}

func writeInvalidRegularOCITree(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "blobs", "sha256"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"oci-layout": `{"imageLayoutVersion":"1.0.0"}`,
		"index.json": `{"schemaVersion":2,"manifests":[]}`,
		"attacker":   "malicious",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
