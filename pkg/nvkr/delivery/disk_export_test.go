// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDiskArtifactWriteLayout(t *testing.T) {
	for _, mode := range []string{"exact", "moved-source", "changed-inode", "closed", "canceled", "existing"} {
		t.Run(mode, func(t *testing.T) {
			root, payload, manifest := diskFixture(t, "linux/arm64")
			owner, err := OpenDiskArtifact(t.Context(), root, manifest, "linux/arm64", int64(len(payload)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owner.Close() })
			parent := t.TempDir()
			out := filepath.Join(parent, "export")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "moved-source":
				if err := os.Rename(root, root+".moved"); err != nil {
					t.Fatal(err)
				}
			case "changed-inode":
				path := filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(owner.Inspection().PayloadDigest, "sha256:"))
				if err := os.WriteFile(path, bytes.Repeat([]byte("x"), len(payload)), 0o600); err != nil {
					t.Fatal(err)
				}
			case "closed":
				_ = owner.Close()
			case "canceled":
				cancel()
			case "existing":
				if err := os.Mkdir(out, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			err = owner.WriteLayout(ctx, out)
			if mode != "exact" && mode != "moved-source" {
				code := e.CodeVerifyFailed
				switch mode {
				case "closed":
					code = e.CodeGateUnmet
				case "canceled":
					code = e.CodeTimeout
				case "existing":
					code = e.CodeImmutableViolation
				}
				assertRegistryCode(t, err, code)
				entries, readErr := os.ReadDir(parent)
				if readErr != nil {
					t.Fatal(readErr)
				}
				want := 0
				if mode == "existing" {
					want = 1
				}
				if len(entries) != want {
					t.Fatal("failed export leaked output or stage", entries)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			copyOwner, err := OpenDiskArtifact(t.Context(), out, manifest, "linux/arm64", int64(len(payload)))
			if err != nil {
				t.Fatal(err)
			}
			defer copyOwner.Close()
			if copyOwner.Inspection() != owner.Inspection() {
				t.Fatal("export changed transport identities")
			}
			var actual bytes.Buffer
			if _, err := copyOwner.WriteToContext(t.Context(), &actual); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actual.Bytes(), payload) {
				t.Fatal("export changed payload bytes")
			}
		})
	}
}
