// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package bundlearchive

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestExtractAdmitsBeforeCreatingOutput(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		kind       byte
		fail       bool
	}{
		{"regular", "nested/file", tar.TypeReg, false},
		{"traversal", "../escape", tar.TypeReg, true},
		{"symlink", "link", tar.TypeSymlink, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out")
			h := tar.Header{Name: tc.path, Typeflag: tc.kind, Mode: 0o644}
			if tc.kind == tar.TypeReg {
				h.Size = 3
			} else {
				h.Linkname = "outside"
			}
			raw := archiveFixture(t, h)
			err := Extract(t.Context(), raw, out)
			if tc.fail {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
					t.Fatalf("error = %v", err)
				}
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("refusal created output")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(out, tc.path))
			if err != nil || string(got) != "xxx" {
				t.Fatalf("payload %q: %v", got, err)
			}
			if err := Extract(t.Context(), raw, out); err == nil {
				t.Fatal("existing destination overwritten")
			}
		})
	}
}
