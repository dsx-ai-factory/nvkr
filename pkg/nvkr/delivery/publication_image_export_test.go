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
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func heldNativeImageFixture(t *testing.T, media types.MediaType) (string, string, string) {
	t.Helper()
	root, pin := metadataImageProfileFixture(t, media, nil)
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
	object, err := publishedImageOutput(inspection, "worker")
	if err != nil {
		t.Fatal(err)
	}
	return root, pin, "registry.example.test/worker@" + object.DescriptorDigest
}

func TestPublicationImageExportUsesHeldClosureAfterSourceReplacement(t *testing.T) {
	for _, scenario := range []string{"native OCI", "native Docker", "aggregate amd64", "aggregate arm64"} {
		t.Run(scenario, func(t *testing.T) {
			var source, pin, reference string
			output, platform, media := "worker", "linux/amd64", MediaOCIManifest
			if strings.HasPrefix(scenario, "aggregate") {
				var result Result
				var index ImageIndexDescription
				source, result, index = registryImageIndexFixture(t)
				pin, output = result.RootDigest, "cwe"
				if scenario == "aggregate arm64" {
					platform = "linux/arm64"
				}
				for _, child := range index.Children {
					if child.Platform == platform {
						reference = "registry.example.test/cwe@" + child.ManifestDigest
					}
				}
			} else {
				nativeMedia := types.OCIManifestSchema1
				if scenario == "native Docker" {
					nativeMedia, media = types.DockerManifestSchema2, MediaDockerManifest
				}
				source, pin, reference = heldNativeImageFixture(t, nativeMedia)
			}
			publication, err := OpenPublication(t.Context(), source, pin)
			if err != nil {
				t.Fatal(err)
			}
			defer publication.Close()
			expectedPath := filepath.Join(t.TempDir(), "expected.tar")
			if _, err := ExportPublishedImage(t.Context(), source, output, reference, platform, expectedPath); err != nil {
				t.Fatal(err)
			}
			expected, err := os.ReadFile(expectedPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(source, source+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(source, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "index.json"), []byte("substituted"), 0o600); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "held.tar")
			result, err := publication.ExportImage(t.Context(), output, reference, platform, archive)
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(archive)
			if err != nil || !bytes.Equal(got, expected) {
				t.Fatalf("held export changed exact image/archive bytes: %v", err)
			}
			if result.ArchiveDigest != digestBytes(got) || result.Size != int64(len(got)) || result.Reference != reference || result.Platform != platform {
				t.Fatal("archive description differs from exported bytes", result)
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
			if err != nil || image.MediaType != media || image.ManifestDigest != result.ManifestDigest {
				t.Fatalf("native media/identity changed: %+v %v", image, err)
			}
			// Export lends file capabilities; the publication retains every output
			// and evidence blob and can still reproduce its complete checked root.
			restored := filepath.Join(t.TempDir(), "publication")
			if err := publication.WriteLayout(t.Context(), restored); err != nil {
				t.Fatal(err)
			}
			inspection, err := Inspect(t.Context(), restored)
			if err != nil || inspection.RootDigest != pin {
				t.Fatalf("image export closed or changed publication files: %v", err)
			}
		})
	}
}

func TestPublicationImageExportRejectsInvalidRequestsWithoutOutput(t *testing.T) {
	root, result, index := registryImageIndexFixture(t)
	for _, scenario := range []string{"wrong digest", "other child digest", "tag", "missing platform", "unsupported platform", "wrong output", "closed", "nil", "unopened", "canceled", "trailing separator", "no replace"} {
		t.Run(scenario, func(t *testing.T) {
			publication, err := OpenPublication(t.Context(), root, result.RootDigest)
			if err != nil {
				t.Fatal(err)
			}
			defer publication.Close()
			directory := t.TempDir()
			out := filepath.Join(directory, "image.tar")
			ctx, output, platform, reference := t.Context(), "cwe", index.Children[0].Platform, "registry.example.test/cwe@"+index.Children[0].ManifestDigest
			code := e.CodeVerifyFailed
			switch scenario {
			case "wrong digest":
				reference = "registry.example.test/cwe@sha256:" + strings.Repeat("a", 64)
			case "other child digest":
				reference = "registry.example.test/cwe@" + index.Children[1].ManifestDigest
			case "tag":
				reference, code = "registry.example.test/cwe:latest", e.CodeUsage
			case "missing platform":
				platform, code = "", e.CodeUsage
			case "unsupported platform":
				platform, code = "linux/riscv64", e.CodeEngineCapability
			case "wrong output":
				output = "missing"
			case "closed":
				_ = publication.Close()
				code = e.CodeGateUnmet
			case "nil":
				publication, code = nil, e.CodeGateUnmet
			case "unopened":
				publication, code = &Publication{}, e.CodeGateUnmet
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				code = e.CodeTimeout
			case "trailing separator":
				out, code = out+string(filepath.Separator), e.CodeUsage
			case "no replace":
				if err := os.WriteFile(out, []byte("existing archive"), 0o600); err != nil {
					t.Fatal(err)
				}
				code = e.CodeImmutableViolation
			}
			got, err := publication.ExportImage(ctx, output, reference, platform, out)
			assertRegistryCode(t, err, code)
			if got != (PublishedImageExport{}) {
				t.Fatal("failed export returned image authority", got)
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "no replace" {
				before, err := os.ReadFile(out)
				if err != nil || string(before) != "existing archive" || len(entries) != 1 {
					t.Fatalf("failed export replaced archive or leaked stage: %v", err)
				}
			} else if len(entries) != 0 {
				t.Fatal("failed export exposed output or stage", entries)
			}
		})
	}
}

func TestPublicationImageExportRetainsCompleteAggregateValidation(t *testing.T) {
	root, result, index := registryImageIndexFixture(t)
	publication, err := OpenPublication(t.Context(), root, result.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer publication.Close()
	// Fault the held unselected sibling while the original layout stays valid.
	// Export must reject the complete captured aggregate without reopening paths
	// to recover that sibling or validating only the requested native image.
	sibling := index.Children[1]
	held, err := publication.heldBlob(Descriptor{Digest: sibling.ManifestDigest, Size: sibling.Size}, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "image.tar")
	_, err = publication.ExportImage(t.Context(), "cwe", "registry.example.test/cwe@"+index.Children[0].ManifestDigest, index.Children[0].Platform, out)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("failed sibling validation exposed output: %v", err)
	}
}

func TestPublicationNativeImageExportBindsPlatformAndPreservesDefault(t *testing.T) {
	root, pin, reference := heldNativeImageFixture(t, types.OCIManifestSchema1)
	publication, err := OpenPublication(t.Context(), root, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer publication.Close()
	for _, test := range []struct {
		name, platform string
		code           e.Code
	}{
		{name: "default from captured native config"},
		{name: "explicit matching platform", platform: "linux/amd64"},
		{name: "wrong native platform", platform: "linux/arm64", code: e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "image.tar")
			result, err := publication.ExportImage(t.Context(), "worker", reference, test.platform, out)
			if test.code != "" {
				assertRegistryCode(t, err, test.code)
				if _, err := os.Lstat(out); !os.IsNotExist(err) {
					t.Fatal("wrong platform exposed output", err)
				}
				return
			}
			if err != nil || result.Platform != "linux/amd64" {
				t.Fatalf("captured native platform=%q error=%v", result.Platform, err)
			}
		})
	}
}

func TestPublicationImageExportConcurrentClose(t *testing.T) {
	root, result, index := registryImageIndexFixture(t)
	for attempt := 0; attempt < 4; attempt++ {
		publication, err := OpenPublication(t.Context(), root, result.RootDigest)
		if err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(t.TempDir(), "image.tar")
		done := make(chan error, 1)
		go func() {
			_, err := publication.ExportImage(t.Context(), "cwe", "registry.example.test/cwe@"+index.Children[0].ManifestDigest, index.Children[0].Platform, out)
			done <- err
		}()
		if err := publication.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			assertRegistryCode(t, err, e.CodeGateUnmet)
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatal("close race exposed partial output", err)
			}
		} else {
			file, err := os.Open(out)
			if err != nil {
				t.Fatal(err)
			}
			extracted := t.TempDir()
			extractErr := extractImageArchive(t.Context(), file, extracted)
			closeErr := file.Close()
			if extractErr != nil || closeErr != nil {
				t.Fatal(extractErr, closeErr)
			}
			image, err := InspectContainerImage(t.Context(), extracted, index.Children[0].Platform)
			if err != nil || image.ManifestDigest != index.Children[0].ManifestDigest {
				t.Fatalf("close race corrupted image: %v", err)
			}
		}
	}
}
