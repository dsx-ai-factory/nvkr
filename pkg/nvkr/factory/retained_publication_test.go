// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Uses the existing native OCI/Docker image fixture, including its exact
// producer records and synthetic scanned inventory. No live scanner claim.
func testRetainedImagePublication(t *testing.T, plan *Plan, artifacts map[string]Artifact, subject string) {
	t.Helper()
	for _, mode := range []string{"exact", "missing inventory", "changed producer", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			var code e.Code
			switch mode {
			case "missing inventory":
				path := artifacts["sbom/image"].Path
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Rename(path+".saved", path); err != nil {
						t.Error(err)
					}
				})
				code = e.CodeProvenanceMissing
			case "changed producer":
				path := artifacts["result/image/image"].Path
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.WriteFile(path, raw, 0o600); err != nil {
						t.Error(err)
					}
				})
				code = e.CodeVerifyFailed
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				code = e.CodeTimeout
			}
			owner, rootDigest, err := CaptureWorkspaceOrdinaryPublication(ctx, plan, ".nvkr/factory")
			if mode != "exact" {
				if owner != nil {
					_ = owner.Close()
					t.Fatal("failed capture returned an owner")
				}
				if rootDigest != "" {
					t.Fatal("failed capture returned a root identity")
				}
				assertFactoryErrorCode(t, err, code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owner.Close() })
			// Change an existing inode after capture, not just its directory entry.
			path := filepath.Join(artifacts["package/image"].Path, "blobs", "sha256", strings.TrimPrefix(subject, "sha256:"))
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("substituted source"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Error(err)
				}
			})
			out := filepath.Join(t.TempDir(), "held")
			if err := owner.WriteLayout(t.Context(), out); err != nil {
				t.Fatal(err)
			}
			inspection, err := delivery.Inspect(t.Context(), out)
			if err != nil {
				t.Fatal(err)
			}
			identity, err := plan.IdentityDigest()
			if err != nil {
				t.Fatal(err)
			}
			metadata := inspection.Root.Release
			if inspection.RootDigest != rootDigest || metadata == nil || metadata.PlanDigest != identity || metadata.InvocationID != "" || inspection.Root.Qualification != delivery.QualificationDiagnostic {
				t.Fatalf("retained metadata changed identity or claimed execution: %+v", metadata)
			}
			outputs, _, err := owner.Artifacts("example.test/local/release@" + inspection.RootDigest)
			if err != nil || len(outputs) != 1 || outputs[0].Digest != subject {
				t.Fatalf("captured outputs = %+v: %v", outputs, err)
			}
			copied := *owner
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			assertFactoryErrorCode(t, copied.WriteLayout(t.Context(), filepath.Join(t.TempDir(), "closed")), e.CodeGateUnmet)
		})
	}
}
