// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/v1/types"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestImageArchiveParentReplacementCannotRedirectWritesOrCleanup(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "no replace cleanup"}[existing], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "parent")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			parent, _, err := securepath.OpenDirectory(path)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			original := filepath.Join(root, "original")
			if err := os.Rename(path, original); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			sentinel := []byte("replacement directory belongs to someone else")
			if err := os.WriteFile(filepath.Join(path, "sentinel"), sentinel, 0600); err != nil {
				t.Fatal(err)
			}
			if existing {
				if err := os.WriteFile(filepath.Join(original, "image.tar"), []byte("existing archive"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			blob, err := heldfile.FromBytesContext(t.Context(), []byte("transport fixture"), heldfile.Options{Label: "archive parent fixture", MaxBytes: 4096})
			if err != nil {
				t.Fatal(err)
			}
			defer blob.Close()
			_, _, err = writePublishedImageArchive(t.Context(), parent, "image.tar", Descriptor{Digest: blob.Digest(), Size: blob.Size(), MediaType: MediaOCIManifest}, map[string]*heldfile.File{blob.Digest(): blob})
			if existing {
				assertRegistryCode(t, err, e.CodeImmutableViolation)
			} else if err != nil {
				t.Fatal(err)
			}
			files, readErr := os.ReadDir(path)
			if readErr != nil || len(files) != 1 || files[0].Name() != "sentinel" {
				t.Fatalf("export touched replacement directory: %v %v", files, readErr)
			}
			got, readErr := os.ReadFile(filepath.Join(path, "sentinel"))
			if readErr != nil || !bytes.Equal(got, sentinel) {
				t.Fatal("replacement sentinel changed")
			}
			files, readErr = os.ReadDir(original)
			if readErr != nil || len(files) != 1 || files[0].Name() != "image.tar" {
				t.Fatalf("export lost original owner or leaked temporary files: %v %v", files, readErr)
			}
			if existing {
				got, err := os.ReadFile(filepath.Join(original, "image.tar"))
				if err != nil || string(got) != "existing archive" {
					t.Fatal("no replace changed the existing archive")
				}
			}
		})
	}
}

func TestExportPublishedNativeImagePreservesMedia(t *testing.T) {
	for _, media := range []types.MediaType{types.OCIManifestSchema1, types.DockerManifestSchema2} {
		t.Run(string(media), func(t *testing.T) {
			root, _ := metadataImageProfileFixture(t, media, nil)
			source := imageFixture(t)
			if media == types.DockerManifestSchema2 {
				source = nativeDockerLayerFixture(t, types.DockerLayer)
			}
			_, manifest := fixtureManifest(t, source)
			for _, layer := range manifest.Layers {
				raw, err := readBlob(t.Context(), source, layer, maxDeliveryFileBytes)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := (layoutWriter{root: root}).addBlobBytes(raw, layer.MediaType); err != nil {
					t.Fatal(err)
				}
			}
			inspection, err := Inspect(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			var object PublishedObject
			for _, candidate := range inspection.Root.Objects {
				if candidate.Name == "worker" {
					object = candidate
				}
			}
			archive := filepath.Join(t.TempDir(), "native.tar")
			result, err := ExportPublishedImage(t.Context(), root, "worker", "registry.example.test/worker@"+object.DescriptorDigest, "", archive)
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(archive)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			extracted := t.TempDir()
			if err := extractImageArchive(t.Context(), file, extracted); err != nil {
				t.Fatal(err)
			}
			got, err := InspectContainerImage(t.Context(), extracted, "linux/amd64")
			if err != nil || got.MediaType != string(media) || got.ManifestDigest != object.DescriptorDigest || result.ManifestDigest != got.ManifestDigest {
				t.Fatalf("native media changed: %+v %+v %v", result, got, err)
			}
		})
	}
}

func TestPublishedChartGetPreservesArchive(t *testing.T) {
	data := helmChartArchiveFixture(t, map[string]string{"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n"})
	layout := filepath.Join(t.TempDir(), "chart")
	artifact, err := BuildHelmChartArtifact(t.Context(), layout, "application/vnd.nvkr.helm-bundle.v1", data)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "publication")
	manifest, err := PayloadDescriptor(t.Context(), layout)
	if err != nil {
		t.Fatal(err)
	}
	build := digestBytes([]byte("chart build"))
	content, err := ContentDigest([]Output{{Name: "controller-chart", Kind: outputFormatHelm, Digest: manifest.Digest, BuildDigest: build}})
	if err != nil {
		t.Fatal(err)
	}
	bom, err := sbom.NewSubject(build, "controller-chart", manifest.Digest)
	if err != nil {
		t.Fatal(err)
	}
	chartArchive := filepath.Join(t.TempDir(), "chart.tgz")
	if err := os.WriteFile(chartArchive, data, 0600); err != nil {
		t.Fatal(err)
	}
	inventory, err := sbom.InspectDependencies(t.Context(), chartArchive, digestBytes(data), "tarball", sbom.Scanner{})
	if err != nil {
		t.Fatal(err)
	}
	bom, err = sbom.WithDependencies(bom, inventory.Components, inventory.Method, inventory.ScannerDigest)
	if err != nil {
		t.Fatal(err)
	}
	bom, err = sbom.ForSubject(bom, manifest.Digest, "packaging", digestBytes(data))
	if err != nil {
		t.Fatal(err)
	}
	document, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := bom.Digest()
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildPublicationRoot(t.Context(), root, PublicationRequest{
		Release: &ReleaseMetadata{DefinitionDigest: build, EnvironmentDigest: build, PlanDigest: build, ContentSubject: content},
		Objects: []ObjectInput{
			{Name: "controller-chart", Kind: outputFormatHelm, Path: layout, Layout: true, ArtifactType: "application/vnd.nvkr.helm-bundle.v1", SemanticDigest: artifact.SemanticDigest, BlobDigest: artifact.RootDigest, Output: &OutputBinding{Repository: "controller-chart", Format: outputFormatHelm, BuildDigest: build}},
			{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: document, Evidence: &EvidenceBinding{Outputs: []string{"controller-chart"}, Component: "controller-chart", Stage: "packaging", SubjectKind: "helm-bundle", SubjectDigest: manifest.Digest, InspectionInputDigest: digestBytes(data)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "chart.tgz")
	if _, err := ExtractObject(t.Context(), root, "controller-chart", out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("chart extraction changed bytes: %v", err)
	}
}

func TestExportPublishedImagePreservesNativeBytes(t *testing.T) {
	for _, platform := range imageIndexPlatforms {
		t.Run(platform, func(t *testing.T) {
			root, _, index := registryImageIndexFixture(t)
			var child ImageIndexChild
			for _, candidate := range index.Children {
				if candidate.Platform == platform {
					child = candidate
				}
			}
			reference := "registry.example.test/qualification/cwe@" + child.ManifestDigest
			archive := filepath.Join(t.TempDir(), "image.tar")
			result, err := ExportPublishedImage(t.Context(), root, "cwe", reference, platform, archive)
			if err != nil {
				t.Fatal(err)
			}
			if result.ManifestDigest != child.ManifestDigest || result.Reference != reference || result.Platform != platform {
				t.Fatalf("substituted result: %+v", result)
			}
			file, err := os.Open(archive)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			extracted := t.TempDir()
			if err := extractImageArchive(t.Context(), file, extracted); err != nil {
				t.Fatal(err)
			}
			image, err := InspectContainerImage(t.Context(), extracted, platform)
			if err != nil || image.ManifestDigest != child.ManifestDigest {
				t.Fatalf("image identity changed: %+v %v", image, err)
			}
			raw, err := os.ReadFile(filepath.Join(extracted, "index.json"))
			if err != nil {
				t.Fatal(err)
			}
			var outer ociIndex
			if err := json.Unmarshal(raw, &outer); err != nil {
				t.Fatal(err)
			}
			if len(outer.Manifests) != 1 || outer.Manifests[0].Annotations[ocispec.AnnotationRefName] != reference || outer.Manifests[0].Digest != child.ManifestDigest {
				t.Fatal("import reference not bound to selected native digest")
			}
			manifest, err := flatManifest(t.Context(), extracted, PublishedObject{DescriptorDigest: child.ManifestDigest, Size: child.Size})
			if err != nil {
				t.Fatal(err)
			}
			for _, descriptor := range append([]Descriptor{{Digest: child.ManifestDigest}, manifest.Config}, manifest.Layers...) {
				path := filepath.Join("blobs", "sha256", strings.TrimPrefix(descriptor.Digest, "sha256:"))
				want, err := os.ReadFile(filepath.Join(root, path))
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(filepath.Join(extracted, path))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(want, got) {
					t.Fatal("export changed manifest/config/layer bytes")
				}
			}
			archiveBytes, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			if digestBytes(archiveBytes) != result.ArchiveDigest || int64(len(archiveBytes)) != result.Size {
				t.Fatal("archive observation mismatch")
			}
			_, err = ExportPublishedImage(t.Context(), root, "cwe", reference, platform, archive)
			assertRegistryCode(t, err, e.CodeImmutableViolation)
		})
	}
}

func TestExportPublishedImageRejectsSubstitutionBeforeOutput(t *testing.T) {
	for _, scenario := range []string{"wrong digest", "tag", "missing platform", "unsupported platform", "wrong object", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			root, _, index := registryImageIndexFixture(t)
			reference, platform, object := "registry.example.test/cwe@"+index.Children[0].ManifestDigest, index.Children[0].Platform, "cwe"
			ctx := t.Context()
			code := e.CodeVerifyFailed
			switch scenario {
			case "wrong digest":
				reference = "registry.example.test/cwe@sha256:" + strings.Repeat("a", 64)
			case "tag":
				reference = "registry.example.test/cwe:latest"
				code = e.CodeUsage
			case "missing platform":
				platform = ""
				code = e.CodeUsage
			case "unsupported platform":
				platform = "linux/riscv64"
				code = e.CodeEngineCapability
			case "wrong object":
				object = "missing"
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				code = e.CodeTimeout
			}
			directory := t.TempDir()
			result, err := ExportPublishedImage(ctx, root, object, reference, platform, filepath.Join(directory, "image.tar"))
			assertRegistryCode(t, err, code)
			if result != (PublishedImageExport{}) {
				t.Fatal("failed export returned identity")
			}
			files, err := os.ReadDir(directory)
			if err != nil || len(files) != 0 {
				t.Fatalf("failed export exposed output: %v %v", files, err)
			}
		})
	}
}

func TestExportPublishedImageRejectsDuplicateNameAcrossKindsBeforeOutput(t *testing.T) {
	root, _, index := registryImageIndexFixture(t)
	inspection, err := Inspect(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	rewriteDiskPublicationRoot(t, root, inspection, func(publication *PublicationRoot) {
		for i := range publication.Objects {
			object := &publication.Objects[i]
			if object.Evidence != nil {
				// Preserve this distinct SBOM descriptor and its different Kind so
				// the rejection exercises release-name membership, not duplicate
				// descriptors or an identical name/Kind pair.
				object.Name = "cwe"
				sortObjects(publication.Objects)
				return
			}
		}
		t.Fatal("aggregate fixture has no evidence object to duplicate the output name")
	})
	directory := t.TempDir()
	child := index.Children[0]
	result, err := ExportPublishedImage(t.Context(), root, "cwe",
		"registry.example.test/cwe@"+child.ManifestDigest, child.Platform,
		filepath.Join(directory, "image.tar"))
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	if !strings.Contains(err.Error(), "duplicate release object name") {
		t.Fatalf("duplicate name bypassed release membership admission: %v", err)
	}
	if result != (PublishedImageExport{}) {
		t.Fatal("rejected duplicate name returned an image description")
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 0 {
		t.Fatalf("duplicate name exposed an archive or temporary output: %v %v", files, err)
	}
}
