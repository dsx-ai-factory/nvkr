// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func TestPublishedHelmChartConfigWithoutPayloads(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*ociManifest, *[]byte)
	}{
		{"exact chart version", nil},
		{"missing version", func(_ *ociManifest, config *[]byte) {
			*config = bytes.Replace(*config, []byte(`"version":"1.2.3",`), nil, 1)
		}},
		{"invalid version", func(_ *ociManifest, config *[]byte) {
			*config = bytes.Replace(*config, []byte(`"version":"1.2.3"`), []byte(`"version":"invalid"`), 1)
		}},
		{"case alias", func(_ *ociManifest, config *[]byte) {
			*config = bytes.Replace(*config, []byte(`"version"`), []byte(`"Version"`), 1)
		}},
		{"duplicate version", func(_ *ociManifest, config *[]byte) {
			*config = append([]byte(`{"version":"4.5.6",`), (*config)[1:]...)
		}},
		{"unknown config field", func(_ *ociManifest, config *[]byte) {
			*config = append([]byte(`{"unrecognized":true,`), (*config)[1:]...)
		}},
		{"wrong config profile", func(manifest *ociManifest, _ *[]byte) { manifest.Config.MediaType = MediaEmptyConfig }},
		{"wrong layer profile", func(manifest *ociManifest, _ *[]byte) { manifest.Layers[0].MediaType = MediaBinaryBundleLayer }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, archive := helmMetadataLayoutFixture(t)
			index, manifest := fixtureManifest(t, root)
			config, err := readBlob(t.Context(), root, manifest.Config, 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(fixtureBlobPath(root, digestBytes(archive))); err != nil {
				t.Fatal(err)
			}
			if test.edit != nil {
				test.edit(&manifest, &config)
			}
			writer := layoutWriter{root: root}
			manifest.Config, err = writer.addBlobBytes(config, manifest.Config.MediaType)
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
			if err != nil {
				t.Fatal(err)
			}
			descriptor.ArtifactType = manifest.ArtifactType
			index.Manifests[0] = descriptor
			if _, err := writer.writeIndex(index); err != nil {
				t.Fatal(err)
			}
			got, err := publishedHelmChartConfig(t.Context(), root, PublishedObject{
				DescriptorDigest: descriptor.Digest, Size: descriptor.Size, ArtifactType: descriptor.ArtifactType,
				BlobDigest: digestBytes(archive), Output: &OutputBinding{Format: outputFormatHelm},
			})
			if test.edit == nil {
				if err != nil || got.Name != "demo" || got.Version != "1.2.3" || got.AppVersion != "9.8.7" {
					t.Fatalf("config-only chart projection: %+v, %v", got, err)
				}
				return
			}
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if !reflect.DeepEqual(got, helmChartConfig{}) {
				t.Fatalf("invalid chart returned metadata: %+v", got)
			}
		})
	}
}

func TestHelmPublicationChartMetadataAndPlatform(t *testing.T) {
	for _, test := range []struct {
		name, arch, configVersion string
		fullCode                  e.Code
	}{
		{"amd64", "amd64", "", ""},
		{"arm64", "arm64", "", ""},
		{"legacy neutral platform", "", "", ""},
		{"config archive mismatch", "arm64", "4.5.6", e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, pin := helmPublicationFixture(t, test.arch, test.configVersion)
			reference := "registry.example.test/product/releases@" + pin
			metadata, err := InspectMetadataDescription(t.Context(), root, reference)
			if err != nil {
				t.Fatal(err)
			}
			wantVersion := "1.2.3"
			if test.configVersion != "" {
				wantVersion = test.configVersion
			}
			wantOS := ""
			if test.arch != "" {
				wantOS = "linux"
			}
			if len(metadata.Outputs) != 1 || metadata.Outputs[0].OS != wantOS || metadata.Outputs[0].Arch != test.arch || metadata.Outputs[0].ChartVersion != wantVersion {
				t.Fatalf("metadata does not describe exact chart config/declaration: %+v", metadata.Outputs)
			}
			held, err := OpenPublication(t.Context(), root, pin)
			if test.fullCode != "" {
				assertRegistryCode(t, err, test.fullCode)
				if held != nil {
					_ = held.Close()
					t.Fatal("inconsistent chart metadata issued a publication")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = held.Close() }()
			artifacts, _, err := held.Artifacts(reference)
			if err != nil || len(artifacts) != 1 || artifacts[0].OS != wantOS || artifacts[0].Arch != test.arch || artifacts[0].ChartVersion != "1.2.3" {
				t.Fatalf("full verification projection lost chart metadata: %+v, %v", artifacts, err)
			}
		})
	}
}

func helmMetadataLayoutFixture(t *testing.T) (string, []byte) {
	t.Helper()
	archive := helmChartArchiveFixture(t, map[string]string{
		"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\nappVersion: 9.8.7\n",
	})
	root := filepath.Join(t.TempDir(), "chart")
	if _, err := BuildHelmChartArtifact(t.Context(), root, "application/vnd.nvkr.helm-bundle.v1", archive); err != nil {
		t.Fatal(err)
	}
	return root, archive
}

func helmPublicationFixture(t *testing.T, arch, configVersion string) (string, string) {
	t.Helper()
	return helmPublicationFixtureWith(t, arch, configVersion, nil)
}

// helmPublicationFixtureWith lets a test adjust the release metadata, such as
// a composite record, before the root is built.
func helmPublicationFixtureWith(t *testing.T, arch, configVersion string, release func(*ReleaseMetadata)) (string, string) {
	t.Helper()
	layout, archive := helmMetadataLayoutFixture(t)
	index, manifest := fixtureManifest(t, layout)
	if configVersion != "" {
		config, err := readBlob(t.Context(), layout, manifest.Config, 8<<20)
		if err != nil {
			t.Fatal(err)
		}
		writer := layoutWriter{root: layout}
		oldConfig, oldManifest := manifest.Config.Digest, index.Manifests[0].Digest
		manifest.Config, err = writer.addBlobBytes(bytes.Replace(config, []byte(`"version":"1.2.3"`), []byte(`"version":"`+configVersion+`"`), 1), MediaHelmConfig)
		if err != nil {
			t.Fatal(err)
		}
		descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
		if err != nil {
			t.Fatal(err)
		}
		descriptor.ArtifactType = manifest.ArtifactType
		index.Manifests[0] = descriptor
		if _, err := writer.writeIndex(index); err != nil {
			t.Fatal(err)
		}
		for _, digest := range []string{oldConfig, oldManifest} {
			if err := os.Remove(fixtureBlobPath(layout, digest)); err != nil {
				t.Fatal(err)
			}
		}
	}
	descriptor := index.Manifests[0]
	build, archiveDigest := digestBytes([]byte("chart build")), digestBytes(archive)
	artifact := Output{Name: "chart", Kind: outputFormatHelm, Digest: descriptor.Digest, BuildDigest: build}
	content, err := ContentDigest([]Output{artifact})
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "chart.tgz")
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	inventory, err := sbom.InspectDependencies(t.Context(), archivePath, archiveDigest, "tarball", sbom.Scanner{})
	if err != nil {
		t.Fatal(err)
	}
	bom, err := sbom.NewSubject(build, "chart", descriptor.Digest)
	if err != nil {
		t.Fatal(err)
	}
	bom, err = sbom.WithDependencies(bom, inventory.Components, inventory.Method, inventory.ScannerDigest)
	if err != nil {
		t.Fatal(err)
	}
	bom, err = sbom.ForSubject(bom, descriptor.Digest, "packaging", archiveDigest)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := bom.Digest()
	if err != nil {
		t.Fatal(err)
	}
	osName := ""
	if arch != "" {
		osName = "linux"
	}
	request := PublicationRequest{
		Release: &ReleaseMetadata{DefinitionDigest: build, EnvironmentDigest: build, PlanDigest: build, ContentSubject: content},
		Objects: []ObjectInput{
			{Name: "chart", Kind: outputFormatHelm, Path: layout, Layout: true, ArtifactType: "application/vnd.nvkr.helm-bundle.v1", SemanticDigest: descriptor.Digest,
				OS: osName, Arch: arch, Output: &OutputBinding{Repository: "chart", Format: outputFormatHelm, BuildDigest: build}},
			{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json",
				LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: raw,
				Evidence: &EvidenceBinding{Outputs: []string{"chart"}, Component: "chart", Stage: "packaging", SubjectKind: "helm-bundle",
					SubjectDigest: descriptor.Digest, InspectionInputDigest: archiveDigest}},
		},
	}
	if release != nil {
		release(request.Release)
	}
	root := filepath.Join(t.TempDir(), "publication")
	result, err := BuildPublicationRoot(t.Context(), root, request)
	if err != nil {
		t.Fatal(err)
	}
	return root, result.RootDigest
}
