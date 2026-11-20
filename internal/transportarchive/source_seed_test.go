// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package transportarchive

import (
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSourceSeedCarrierIncludesOnlyGitObjectsAndShallow(t *testing.T) {
	for _, scenario := range []string{"full", "shallow", "missing git", "missing objects", "git symlink", "object symlink", "shallow symlink", "objects symlink", "git file", "shallow directory"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"source/.git/objects/pack", "source/.git/hooks", "source/.git/info", "source/.git/refs"} {
				if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"input", ".git/objects/pack/data", ".git/config", ".git/index", ".git/HEAD", ".git/hooks/pre-commit", ".git/info/attributes", ".git/refs/main"} {
				if err := os.WriteFile(filepath.Join(root, "source", name), []byte(name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			want := e.CodeReleaseIncomplete
			switch scenario {
			case "full":
				want = ""
			case "shallow":
				want = ""
				if err := os.WriteFile(filepath.Join(root, "source/.git/shallow"), []byte("boundary"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing git", "git symlink", "git file":
				if err := os.Rename(filepath.Join(root, "source/.git"), filepath.Join(root, "saved-git")); err != nil {
					t.Fatal(err)
				}
				if scenario == "git file" {
					if err := os.WriteFile(filepath.Join(root, "source/.git"), []byte("gitdir: ../saved-git"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "git symlink" {
					if err := os.Symlink("../saved-git", filepath.Join(root, "source/.git")); err != nil {
						t.Fatal(err)
					}
				}
			case "shallow directory":
				if err := os.Mkdir(filepath.Join(root, "source/.git/shallow"), 0700); err != nil {
					t.Fatal(err)
				}
			case "missing objects", "objects symlink":
				if err := os.Rename(filepath.Join(root, "source/.git/objects"), filepath.Join(root, "saved-objects")); err != nil {
					t.Fatal(err)
				}
				if scenario == "objects symlink" {
					if err := os.Symlink("../../saved-objects", filepath.Join(root, "source/.git/objects")); err != nil {
						t.Fatal(err)
					}
				}
			case "object symlink", "shallow symlink":
				name := "objects/link"
				if scenario == "shallow symlink" {
					name = "shallow"
				}
				if err := os.Symlink("objects", filepath.Join(root, "source/.git", name)); err != nil {
					t.Fatal(err)
				}
			}
			archive := filepath.Join(root, "carrier.tar")
			err := PackSourceSeed(t.Context(), archive, root, "source")
			if transportCode(err) != want {
				t.Fatalf("pack=%v, want %s", err, want)
			}
			if want != "" {
				if _, err := os.Stat(archive); !os.IsNotExist(err) {
					t.Fatalf("refused seed published a carrier: %v", err)
				}
				return
			}
			destination := t.TempDir()
			if err := Unpack(t.Context(), archive, destination, []string{"source"}); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"input", ".git/objects/pack/data"} {
				got, err := os.ReadFile(filepath.Join(destination, "source", name))
				if err != nil || string(got) != name {
					t.Fatalf("restored %s=%q, %v", name, got, err)
				}
			}
			for _, name := range []string{"config", "index", "HEAD", "hooks", "info", "refs"} {
				if _, err := os.Lstat(filepath.Join(destination, "source/.git", name)); !os.IsNotExist(err) {
					t.Fatalf("administrative metadata %s crossed source transport: %v", name, err)
				}
			}
			got, err := os.ReadFile(filepath.Join(destination, "source/.git/shallow"))
			if scenario == "shallow" && (err != nil || string(got) != "boundary") || scenario == "full" && !os.IsNotExist(err) {
				t.Fatalf("restored shallow=%q, %v", got, err)
			}
		})
	}
}
