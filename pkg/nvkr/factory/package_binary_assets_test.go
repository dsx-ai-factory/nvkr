// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
)

func TestBinaryPackageTarballModeFollowsDeclaredType(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o755} {
		t.Run(mode.String(), func(t *testing.T) {
			root := t.TempDir()
			payload := filepath.Join(root, "payload")
			if err := os.WriteFile(payload, []byte("opaque producer bytes"), mode); err != nil {
				t.Fatal(err)
			}
			plan := &Plan{Builds: []BuildPlan{{Name: "mixed", Platform: "linux/arm64"}}}
			inputs := []Artifact{
				{Name: "assets", Kind: "tarball", BuildTarget: "mixed", Path: payload},
				{Name: "tool.tar.gz", Kind: "host-package", BuildTarget: "mixed", Path: payload},
			}
			var firstDigest string
			for _, attempt := range []string{"first", "reproduced"} {
				out := Artifact{Name: "mixed", Path: filepath.Join(root, attempt)}
				if err := executeBinaryPackage(t.Context(), plan, out, inputs); err != nil {
					t.Fatal(err)
				}
				bundle, result, err := delivery.InspectBinaryBundle(t.Context(), out.Path)
				if err != nil {
					t.Fatal(err)
				}
				if len(bundle.Entries) != 2 || bundle.Entries[0].Name != "assets" || bundle.Entries[0].Mode != 0o644 || bundle.Entries[1].Name != "tool.tar.gz" || bundle.Entries[1].Mode != 0o755 {
					t.Fatalf("producer types lost in bundle: %+v", bundle.Entries)
				}
				if bundle.Entries[0].SHA256 != bundle.Entries[1].SHA256 {
					t.Fatal("packaging changed identical source bytes")
				}
				if firstDigest != "" && result.RootDigest != firstDigest {
					t.Fatal("repackaging changed artifact identity")
				}
				firstDigest = result.RootDigest
			}
		})
	}
}
