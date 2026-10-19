// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Hostile metadata is written below the validated constructor boundary. The
// absent, tiny fixture payload proves rejection precedes even opening its inode;
// no large allocation is necessary to test the disk-storage admission invariant.
func TestPublicationRejectsMalformedDiskBeforePayloadCapture(t *testing.T) {
	request, _ := diskPublicationRequest(t, "amd64")
	base := filepath.Join(t.TempDir(), "base")
	if _, err := BuildPublicationRoot(t.Context(), base, request); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name        string
		config      string
		extraLayer  bool
		plainLayer  bool
		disguise    bool
		annotations bool
	}{
		{name: "empty config", config: MediaEmptyConfig},
		{name: "helm config", config: MediaHelmConfig},
		{name: "multiple payloads", extraLayer: true},
		{name: "substituted payload media", plainLayer: true},
		{name: "disguised image output", config: MediaEmptyConfig, disguise: true},
		{name: "disk annotations survive disguised media", config: MediaEmptyConfig, plainLayer: true, disguise: true, annotations: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "publication")
			if err := os.CopyFS(root, os.DirFS(base)); err != nil {
				t.Fatal(err)
			}
			inspection, err := Inspect(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			publication := inspection.Root
			writer := layoutWriter{root: root}
			for i := range publication.Objects {
				object := &publication.Objects[i]
				if object.Output == nil {
					continue
				}
				manifest, err := flatManifest(t.Context(), root, *object)
				if err != nil {
					t.Fatal(err)
				}
				if scenario.config != "" {
					manifest.Config.MediaType = scenario.config
				}
				if scenario.extraLayer {
					manifest.Layers = append(manifest.Layers, manifest.Layers[0])
				}
				if scenario.plainLayer {
					manifest.Layers[0].MediaType = MediaOctetStream
				}
				if scenario.disguise {
					object.Kind, object.ArtifactType = "container-image", MediaOCIManifest
					object.Output.Format, object.Output.Disk = "oci", nil
					object.Output.Components = []string{"image"}
					object.Output.BuildSubjects = object.Output.BuildSubjects[1:]
					manifest.ArtifactType = MediaOCIManifest
					if !scenario.annotations {
						manifest.Annotations = nil
					}
				}
				descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
				if err != nil {
					t.Fatal(err)
				}
				object.DescriptorDigest, object.Size = descriptor.Digest, descriptor.Size
			}
			// Root.Validate must succeed: the storage owner, not a label-only
			// metadata check, must reject the malformed byte profile.
			if err := publication.Validate(); err != nil {
				t.Fatal(err)
			}
			raw, err := canonical.Marshal(publication)
			if err != nil {
				t.Fatal(err)
			}
			config, err := writer.addBlobBytes(raw, MediaPublicationRootConfig)
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := writer.addJSONBlob(ociManifest{SchemaVersion: 2, MediaType: MediaOCIManifest, ArtifactType: MediaPublicationRoot, Config: config, Layers: []Descriptor{}}, MediaOCIManifest)
			if err != nil {
				t.Fatal(err)
			}
			descriptor.ArtifactType = MediaPublicationRoot
			pin, err := writer.writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, ArtifactType: MediaPublicationRoot, Manifests: []Descriptor{descriptor}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(fixtureBlobPath(root, request.Objects[0].Output.Disk.PayloadDigest)); err != nil {
				t.Fatal(err)
			}
			disks, err := publicationDiskPayloads(t.Context(), root, pin)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if disks != nil || !strings.Contains(err.Error(), "unsupported manifest, config or payload contract") {
				t.Fatalf("malformed disk reached payload/inventory work: %v", err)
			}
			held, err := OpenPublication(t.Context(), root, pin)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if held != nil || !strings.Contains(err.Error(), "unsupported manifest, config or payload contract") {
				t.Fatalf("publication reached payload capture: %v", err)
			}
		})
	}
}
