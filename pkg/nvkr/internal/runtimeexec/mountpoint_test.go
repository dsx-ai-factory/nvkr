// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runtimeexec

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPrepareNestedMountPointRejectsPackagedSymlinks(t *testing.T) {
	for _, tc := range []struct {
		name, link string
	}{
		{"real directories", ""},
		{"absolute parent symlink", "absolute"},
		{"relative parent symlink", "relative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rootPath, outside := t.TempDir(), t.TempDir()
			if tc.link != "" {
				target := outside
				if tc.link == "relative" {
					var err error
					target, err = filepath.Rel(rootPath, outside)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, filepath.Join(rootPath, "nvkr")); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.Open(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			err = prepareMountPoint(root, "nvkr/input")
			if tc.link == "" {
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(filepath.Join(rootPath, "nvkr", "input"))
				if err != nil || !info.IsDir() {
					t.Fatalf("nested mount point = %v, %v", info, err)
				}
			} else {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
					t.Fatalf("symlink admission = %v", err)
				}
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("outside root changed: %v, %v", entries, err)
			}
		})
	}
}
