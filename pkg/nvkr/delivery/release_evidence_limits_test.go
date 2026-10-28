// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func TestReleaseEvidenceIndependentDescriptorBounds(t *testing.T) {
	for _, test := range []struct {
		name     string
		bundle   bool
		size     int64
		admitted bool
	}{
		{name: "document at inventory ceiling", size: sbom.MaxDocumentBytes, admitted: true},
		{name: "document one byte over inventory ceiling", size: sbom.MaxDocumentBytes + 1},
		{name: "bundle at transport ceiling exceeds document ceiling", bundle: true, size: sbom.MaxBundleBytes, admitted: true},
		{name: "bundle one byte over transport ceiling", bundle: true, size: sbom.MaxBundleBytes + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, _ := releasePublicationFixture(t)
			inspection, err := Inspect(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			index := -1
			for i := range inspection.Root.Objects {
				if inspection.Root.Objects[i].Evidence != nil {
					index = i
					break
				}
			}
			if index < 0 {
				t.Fatal("fixture lacks bound release evidence")
			}
			object := &inspection.Root.Objects[index]
			manifest, err := flatManifest(t.Context(), root, *object)
			if err != nil {
				t.Fatal(err)
			}
			writer := layoutWriter{root: root, plain: true}
			if test.bundle {
				object.ArtifactType = sbom.BundleMediaType
			}
			// Exercise delivery's choice of byte ceiling with a descriptor only.
			// At the ceiling it must reach the small payload's size mismatch;
			// above the ceiling it must reject before reading that payload.
			// Exact-size decoding belongs to sbom's attestation bound tests.
			manifest.ArtifactType = object.ArtifactType
			manifest.Layers[0].MediaType = object.ArtifactType
			manifest.Layers[0].Size = test.size
			descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
			if err != nil {
				t.Fatal(err)
			}
			object.DescriptorDigest, object.Size = descriptor.Digest, descriptor.Size
			err = verifyReleaseEvidence(t.Context(), root, inspection.Root)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			want := "descriptor is malformed or exceeds its byte bound"
			if test.admitted {
				want = "bytes do not match descriptor"
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("evidence descriptor reached the wrong guard: %v", err)
			}
		})
	}
}
