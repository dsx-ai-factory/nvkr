// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

// Existing OCI writer and transport packer own all fixture grammar.
func nestedOCIFixture(t *testing.T, architectures []string, compressed bool, change func(string)) *generatedInspectionFile {
	t.Helper()
	root := t.TempDir()
	lp, err := layout.Write(root, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	for index, architecture := range architectures {
		configuration, err := empty.Image.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		configuration.OS, configuration.Architecture = "linux", architecture
		configuration.Config.Labels = map[string]string{"fixture": strconv.Itoa(index)}
		image, err := mutate.ConfigFile(empty.Image, configuration)
		if err != nil {
			t.Fatal(err)
		}
		if err := lp.AppendImage(image); err != nil {
			t.Fatal(err)
		}
	}
	if change != nil {
		change(root)
	}
	if err := os.MkdirAll(filepath.Join(root, "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	carrier := filepath.Join(root, "layout.tar")
	if err := transportarchive.Pack(t.Context(), carrier, root, []string{"oci-layout", "index.json", "blobs"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(carrier)
	if err != nil {
		t.Fatal(err)
	}
	if compressed {
		return compressedMaterialBytes(t, raw)
	}
	file, err := createGeneratedInspectionFile(t.Context(), 1<<20, func(destination io.Writer) error { _, err := destination.Write(raw); return err })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestNestedOCIEnumerationPlatformDepthAndDeduplication(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		configuration, err := empty.Image.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		configuration.OS, configuration.Architecture = "linux", arch
		image, err := mutate.ConfigFile(empty.Image, configuration)
		if err != nil {
			t.Fatal(err)
		}
		for _, test := range []struct {
			name  string
			index v1.ImageIndex
			code  e.Code
			count int
		}{
			{"same digest repeated", mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: image}, mutate.IndexAddendum{Add: image}), "", 1},
			{"foreign descriptor", mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: image, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "foreign"}}}), e.CodeVerifyFailed, 0},
			{"same digest later foreign descriptor", mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: image}, mutate.IndexAddendum{Add: image, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "foreign"}}}), e.CodeVerifyFailed, 0},
			{"foreign config", mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: empty.Image}), e.CodeVerifyFailed, 0},
		} {
			t.Run(arch+"/"+test.name, func(t *testing.T) {
				budget := &materialInspectionBudget{remaining: 1 << 20}
				images, err := nestedOCIImages(t.Context(), test.index, "linux/"+arch, budget, 0)
				if test.code != "" {
					assertDependencyCode(t, err, test.code)
				} else if err != nil || len(images) != test.count || budget.images != test.count || budget.members != 2 {
					t.Fatalf("duplicate descriptor rescanned image: %d %+v %v", len(images), budget, err)
				}
			})
		}
		index := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: image})
		for range 9 {
			index = mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: index})
		}
		images, err := nestedOCIImages(t.Context(), index, "linux/"+arch, &materialInspectionBudget{remaining: 1 << 20}, 0)
		assertDependencyCode(t, err, e.CodeEngineCapability)
		if images != nil || !strings.Contains(err.Error(), "bounded depth") {
			t.Fatalf("deep index returned partial images: %v", err)
		}
	}
}

func TestNestedOCIUsesCompleteClosureAndAllPlatforms(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, compressed := range []bool{false, true} {
			for _, test := range []struct {
				name          string
				architectures []string
				change        func(string)
				code          e.Code
				message       string
			}{
				{"two images reach scanner", []string{architecture, architecture}, nil, e.CodeUnverifiedRef, ""},
				{"other platform not dropped", []string{architecture, "unsupported"}, nil, e.CodeVerifyFailed, ""},
				{"empty layout malformed blob directory", nil, nil, e.CodeVerifyFailed, "unexpected directory set"},
				{"well-formed empty layout has no image", nil, func(root string) {
					if err := os.MkdirAll(filepath.Join(root, "blobs", "sha256"), 0o755); err != nil {
						t.Fatal(err)
					}
				}, e.CodeEngineCapability, "no inspectable image"},
				{"multiple roots without index media type", []string{architecture, architecture}, func(root string) {
					raw, err := os.ReadFile(filepath.Join(root, "index.json"))
					if err != nil {
						t.Fatal(err)
					}
					var manifest v1.IndexManifest
					if err := json.Unmarshal(raw, &manifest); err != nil {
						t.Fatal(err)
					}
					manifest.MediaType = ""
					raw, err = json.Marshal(manifest)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, "index.json"), raw, 0o644); err != nil {
						t.Fatal(err)
					}
				}, e.CodeVerifyFailed, ""},
				{"unreferenced blob rejected", []string{architecture}, func(root string) {
					raw := []byte("unreferenced")
					if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(rawDigest(raw), "sha256:")), raw, 0o644); err != nil {
						t.Fatal(err)
					}
				}, e.CodeVerifyFailed, ""},
			} {
				t.Run(architecture+"/"+strconv.FormatBool(compressed)+"/"+test.name, func(t *testing.T) {
					inspection, err := inspectCompressedImages(t.Context(), nestedOCIFixture(t, test.architectures, compressed, test.change), "linux/"+architecture, Scanner{Path: "/missing/selected-syft", Digest: "sha256:" + strings.Repeat("f", 64)}, 1<<20, 0)
					assertDependencyCode(t, err, test.code)
					if test.message != "" && !strings.Contains(err.Error(), test.message) {
						t.Fatalf("wrong rejection boundary: %v", err)
					}
					if inspection != nil {
						t.Fatal("rejected OCI returned partial inventory")
					}
				})
			}
		}
	}
}

func TestNestedOCIValidationErrorPreservesTimeout(t *testing.T) {
	for _, test := range []struct {
		name     string
		canceled bool
		inner    error
		code     e.Code
	}{
		{"canceled context", true, errors.New("controlled transport failure"), e.CodeTimeout},
		{"inner coded timeout", false, e.New(e.CodeTimeout, "controlled inner timeout"), e.CodeTimeout},
		{"ordinary validation error", false, errors.New("controlled transport failure"), e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.canceled {
				cancel()
			}
			err := nestedOCIValidationError(ctx, test.inner, "controlled OCI validation")
			assertDependencyCode(t, err, test.code)
			if test.canceled && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost actual cancellation cause: %v", err)
			}
			if !errors.Is(err, test.inner) {
				t.Fatalf("lost primary validation cause: %v", err)
			}
			if !test.canceled && test.code == e.CodeTimeout && err != test.inner {
				t.Fatalf("coded inner timeout was rewritten: %v", err)
			}
		})
	}
}

func TestNestedOCIScanCancellationPreservesTimeout(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel bool
		code   e.Code
	}{
		{"canceled transport verification", true, e.CodeVerifyFailed},
		{"canceled scanner admission", true, e.CodeUnverifiedRef},
		{"ordinary transport verification", false, e.CodeVerifyFailed},
		{"ordinary scanner admission", false, e.CodeUnverifiedRef},
		{"canceled inner timeout", true, e.CodeTimeout},
		{"ordinary inner timeout", false, e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			subject := nestedOCIFixture(t, []string{"arm64"}, false, nil)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			primary := e.New(test.code, "controlled scan callback failure")
			calls := 0
			inspection, err := inspectNestedOCILayoutScan(ctx, subject, subject, "linux/arm64", Scanner{}, &materialInspectionBudget{remaining: 1 << 20},
				func(context.Context, string, v1.Image) (DependencyInventory, error) {
					calls++
					if test.cancel {
						cancel()
					}
					return DependencyInventory{}, primary
				})
			if calls != 1 || inspection != nil {
				t.Fatalf("scan boundary not reached or partial inventory returned: calls=%d error=%v", calls, err)
			}
			if test.cancel && test.code != e.CodeTimeout {
				assertDependencyCode(t, err, e.CodeTimeout)
				if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "scanning nested OCI image canceled") {
					t.Fatalf("wrong cancellation boundary: %v", err)
				}
			} else if err != primary {
				t.Fatalf("ordinary scanner failure changed: %v", err)
			}
		})
	}
}

func TestNestedOCIClosureIsRevalidatedAfterCompletedScanOperation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate bool
		images int
	}{
		{"unchanged closure", false, 1}, {"changed config after scan", true, 1}, {"changed config across two image scans", true, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			architectures := []string{"arm64"}
			if test.images == 2 {
				architectures = append(architectures, "arm64")
			}
			subject := nestedOCIFixture(t, architectures, false, nil)
			calls := 0
			inspection, err := inspectNestedOCILayoutScan(t.Context(), subject, subject, "linux/arm64", Scanner{}, &materialInspectionBudget{remaining: 1 << 20},
				func(ctx context.Context, root string, image v1.Image) (DependencyInventory, error) {
					calls++
					if test.mutate {
						digest, err := image.ConfigName()
						if err != nil {
							return DependencyInventory{}, err
						}
						if err := os.WriteFile(filepath.Join(root, "blobs", digest.Algorithm, digest.Hex), []byte("substituted"), 0o644); err != nil {
							return DependencyInventory{}, err
						}
					}
					return DependencyInventory{Components: []Component{{Type: "library", Name: "controlled", Version: "1"}}}, nil
				})
			if calls != test.images {
				t.Fatalf("scan did not run: %d %v", calls, err)
			}
			if test.mutate {
				assertDependencyCode(t, err, e.CodeVerifyFailed)
				if inspection != nil {
					t.Fatal("mutated closure returned inventory")
				}
			} else if err != nil || inspection == nil || len(inspection.components) != 1 {
				t.Fatalf("unchanged controlled scan rejected: %v", err)
			}
		})
	}
}

func TestNestedMaterialCumulativeAdmission(t *testing.T) {
	for _, test := range []struct {
		name            string
		remaining       int64
		members, images int
		code            e.Code
	}{
		{"shared expansion exhaustion", 1024, 0, 0, e.CodeEngineCapability},
		{"shared image count", 1 << 20, 0, 256, e.CodeEngineCapability},
		{"shared member count", 1 << 20, maxDiskInstalledPackages, 0, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspection, err := inspectNestedImagesBudget(t.Context(), nestedOCIFixture(t, []string{"amd64"}, true, nil), "linux/amd64", Scanner{}, 1<<20, 0, &materialInspectionBudget{remaining: test.remaining, members: test.members, images: test.images})
			assertDependencyCode(t, err, test.code)
			if inspection != nil {
				t.Fatal("budget failure returned inventory")
			}
		})
	}
	subject := compressedMaterialFixture(t, map[string]string{"docs": "ordinary"})
	budget := &materialInspectionBudget{remaining: 2048}
	if inspection, err := inspectNestedImagesBudget(t.Context(), subject, "linux/amd64", Scanner{}, 1<<20, 0, budget); err != nil || inspection != nil {
		t.Fatalf("first ordinary expansion: %v", err)
	}
	inspection, err := inspectNestedImagesBudget(t.Context(), subject, "linux/amd64", Scanner{}, 1<<20, 0, budget)
	if err != nil || inspection != nil || budget.remaining != 2048 || budget.members != 0 || budget.images != 0 {
		t.Fatalf("ordinary sibling consumed an image budget: %+v %v", budget, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	inspection, err = inspectCompressedImages(ctx, subject, "linux/amd64", Scanner{}, 1<<20, 0)
	assertDependencyCode(t, err, e.CodeTimeout)
	if inspection != nil {
		t.Fatal("canceled operation returned inventory")
	}
	inspection, err = inspectNestedImagesBudget(t.Context(), compressedDockerFixture(t), "linux/amd64", Scanner{}, 1<<20, 0, &materialInspectionBudget{remaining: 1 << 20, images: 256})
	assertDependencyCode(t, err, e.CodeEngineCapability)
	if inspection != nil || !strings.Contains(err.Error(), "cumulative image budget") {
		t.Fatalf("Docker image bypassed shared image count: %v", err)
	}
}

// Actual held child execution is Linux-only. This proves all projections are
// scanned, not real Syft discovery or a native full-disk join.
func TestNestedOCIInspectsEveryImage(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held scanner execution requires Linux")
	}
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			counter := filepath.Join(t.TempDir(), "scans")
			script := []byte("#!/bin/sh\nprintf x >> '" + counter + "'\nprintf '%s' '{\"schema\":{\"version\":\"16.1.10\"},\"artifacts\":[{\"name\":\"observed\",\"version\":\"1\",\"type\":\"deb\",\"metadata\":{\"architecture\":\"all\"}}]}'\n")
			scannerPath := filepath.Join(t.TempDir(), "scanner")
			if err := os.WriteFile(scannerPath, script, 0o755); err != nil {
				t.Fatal(err)
			}
			subject := nestedOCIFixture(t, []string{architecture, architecture}, true, nil)
			inspection, err := inspectCompressedImages(t.Context(), subject, "linux/"+architecture, Scanner{Path: scannerPath, Digest: rawDigest(script)}, 1<<20, 0)
			if err != nil || inspection == nil {
				t.Fatalf("scan failed: %v", err)
			}
			calls, err := os.ReadFile(counter)
			if err != nil || !bytes.Equal(calls, []byte("xx")) {
				t.Fatalf("not every image scanned: %q %v", calls, err)
			}
			if len(inspection.Components()) != 2 || inspection.subject != subject.Digest() {
				t.Fatal("components or original carrier binding lost")
			}
			for _, component := range inspection.Components() {
				class, err := diskComponentClass(component)
				if err != nil || class != diskClassContainer {
					t.Fatalf("class=%q error=%v", class, err)
				}
			}
		})
	}
}
