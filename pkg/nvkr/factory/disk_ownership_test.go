// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func TestDiskOwnershipUsesHeldWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		cancel     bool
		code       e.Code
	}{
		{"existing", "output/nested/result", false, ""},
		{"missing", "output/absent", false, ""},
		{"directory", "output", false, ""},
		{"symlink", "link/result", false, e.CodeVerifyFailed},
		{"escape", "../outside", false, e.CodeVerifyFailed},
		{"unclean", "output/../output", false, e.CodeVerifyFailed},
		{"canceled", "output", true, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "output/nested"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "output/nested/result"), []byte("evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("output", filepath.Join(root, "link")); err != nil {
				t.Fatal(err)
			}
			workspace, _, err := securepath.OpenDirectory(root)
			if err != nil {
				t.Fatal(err)
			}
			defer workspace.Close()
			owner, err := captureDiskOwnership(t.Context(), workspace)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			err = owner.returnPath(ctx, workspace, tc.path)
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDiskOwnershipReturnsOnlySelectedFiles(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to exercise ownership handback")
	}
	for _, linked := range []bool{false, true} {
		name := "regular"
		if linked {
			name = "hardlink"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "output"), 0o700); err != nil {
				t.Fatal(err)
			}
			selected, private := filepath.Join(root, "output/result"), filepath.Join(root, "private")
			for _, path := range []string{selected, private} {
				if err := os.WriteFile(path, []byte("held"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if linked {
				if err := os.Link(selected, filepath.Join(root, "shared")); err != nil {
					t.Fatal(err)
				}
			}
			workspace, _, err := securepath.OpenDirectory(root)
			if err != nil {
				t.Fatal(err)
			}
			defer workspace.Close()
			err = (diskOwnership{65534, 65534}).returnPath(t.Context(), workspace, "output/result")
			if linked {
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
			} else if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{selected, private} {
				var stat unix.Stat_t
				if err := unix.Stat(path, &stat); err != nil {
					t.Fatal(err)
				}
				want := uint32(0)
				if path == selected && !linked {
					want = 65534
				}
				if stat.Uid != want || stat.Gid != want || stat.Mode&0o777 != 0o600 {
					t.Fatalf("ownership/mode changed unexpectedly: %s %+v", path, stat)
				}
			}
		})
	}
}
