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
				{Name: "attestation.json", Kind: ArtifactReleaseCompanion, BuildTarget: "mixed", Path: payload},
				{Name: "recipe", Kind: "file", BuildTarget: "mixed", Path: payload},
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
				want := []struct {
					name string
					mode uint32
				}{{"assets", 0o644}, {"attestation.json", 0o644}, {"recipe", 0o644}, {"tool.tar.gz", 0o755}}
				if len(bundle.Entries) != len(want) {
					t.Fatalf("producer types lost in bundle: %+v", bundle.Entries)
				}
				for i, entry := range bundle.Entries {
					if entry.Name != want[i].name || entry.Mode != want[i].mode || entry.SHA256 != bundle.Entries[0].SHA256 {
						t.Fatalf("producer types or bytes lost in bundle: %+v", bundle.Entries)
					}
				}
				if firstDigest != "" && result.RootDigest != firstDigest {
					t.Fatal("repackaging changed artifact identity")
				}
				firstDigest = result.RootDigest
			}
		})
	}
}
