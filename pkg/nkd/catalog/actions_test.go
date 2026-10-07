// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestActionSourcesUseCatalogLockAndSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	registry := strings.Replace(lockTestRegistry, "s: {repo: https://github.com/example/source}", "checkout: {kind: github-action, repo: https://github.com/actions/checkout, constraint: 7.x}", 1)
	if err := os.WriteFile(filepath.Join(dir, RegistryFile), []byte(registry), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := LoadLockContext(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	lock.Pins["checkout"] = lockPin("v7.0.1", "a", 1)
	if err := WriteLockContext(ctx, dir, lock); err != nil {
		t.Fatal(err)
	}
	before, err := LoadContext(ctx, Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got := before.Actions["checkout"]; got.Repo != "actions/checkout" || got.Version != "v7.0.1" || got.SHA != strings.Repeat("a", 40) {
		t.Fatalf("resolved action = %+v", got)
	}
	oldDigest, err := before.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	lock, err = LoadLockContext(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	lock.Pins["checkout"] = lockPin("v7.0.2", "b", 2)
	if err := WriteLockContext(ctx, dir, lock); err != nil {
		t.Fatal(err)
	}
	after, err := LoadContext(ctx, Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	newDigest, err := after.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	if oldDigest == newDigest {
		t.Fatal("action bump did not change catalog identity")
	}
	lock, err = LoadLockContext(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	lock.Pins["checkout"] = lockPin("v7", "b", 2)
	if err := WriteLockContext(ctx, dir, lock); codeOf(err) != e.CodeInvalidCatalog {
		t.Fatalf("moving tag write: %v", err)
	}
}

func TestActionSourceAdmissionBeforePinIO(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source Source
		tag    string
	}{
		{"unknown kind", Source{Kind: "action", Repo: "https://github.com/actions/checkout"}, "v7.0.1"},
		{"wrong host", Source{Kind: SourceGitHubAction, Repo: "https://example.com/actions/checkout"}, "v7.0.1"},
		{"build settings", Source{Kind: SourceGitHubAction, Repo: "https://github.com/actions/checkout", VendorCommand: []string{"go", "mod", "vendor"}}, "v7.0.1"},
		{"moving tag", Source{Kind: SourceGitHubAction, Repo: "https://github.com/actions/checkout"}, "v7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Any accidental subprocess would fail rather than contact a remote.
			t.Setenv("PATH", t.TempDir())
			_, err := ResolvePin(context.Background(), tc.source, tc.tag)
			if codeOf(err) != e.CodeInvalidCatalog {
				t.Fatalf("admission: %v", err)
			}
		})
	}
}
