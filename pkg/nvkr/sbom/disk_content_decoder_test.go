// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/diskdiagnostic"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// These are private decoder projections, not minted pipeline observations or
// proof that the sandbox/helper/receipt issuer executed successfully.
func TestBoundDiskContentDecoder(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, name := range []string{"valid", "subject", "size", "kind", "platform", "inspector", "codec", "guestfish", "scanner", "package-reader", "filesystem-bound", "image", "unknown-field", "duplicate-field", "trailing-document", "malformed", "empty", "unsorted-files", "duplicate-files", "unsafe-path", "missing-file-digest", "unknown-file-kind", "missing-class"} {
			t.Run(architecture+"/"+name, func(t *testing.T) {
				fixture := contentFixture("disk-image", architecture, "b")
				report, binding, image := fixture.report, fixture.report, fixture.runtimeDigest
				want := e.CodeVerifyFailed
				switch name {
				case "valid":
					want = ""
				case "subject":
					report.Subject = "sha256:" + strings.Repeat("c", 64)
				case "size":
					report.Size++
				case "kind":
					report.Kind = "tarball"
				case "platform":
					report.Platform = "linux/other"
				case "inspector":
					report.Inspector = ""
				case "codec":
					report.Codec = ""
				case "guestfish":
					report.Guestfish = ""
				case "scanner":
					report.Scanner = ""
				case "package-reader":
					report.PackageReader = ""
				case "filesystem-bound":
					report.MaxFilesystemBytes++
				case "image":
					image = "not-a-digest"
				case "empty":
					report.Components, report.Files = nil, nil
					want = e.CodeProvenanceMissing
				case "unsorted-files":
					report.Files = append(report.Files, DiskFile{Path: "a", Kind: "regular", Digest: image, Size: 1})
					want = e.CodeEnvelopeDecode
				case "duplicate-files":
					report.Files = append(report.Files, report.Files[0])
					want = e.CodeEnvelopeDecode
				case "unsafe-path":
					report.Files[0].Path = "../escape"
					want = e.CodeEnvelopeDecode
				case "missing-file-digest":
					report.Files[0].Digest = ""
					want = e.CodeEnvelopeDecode
				case "unknown-file-kind":
					report.Files[0].Kind = "socket"
					want = e.CodeEngineCapability
				case "missing-class":
					report.Components[0].Properties = report.Components[0].Properties[1:]
					want = e.CodeProvenanceMissing
				}
				raw, err := json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				switch name {
				case "unknown-field":
					raw = append(append([]byte{}, raw[:len(raw)-1]...), []byte(",\"unknown\":true}")...)
					want = e.CodeEnvelopeDecode
				case "duplicate-field":
					raw = append(append([]byte{}, raw[:len(raw)-1]...), []byte(",\"size\":4096}")...)
					want = e.CodeEnvelopeDecode
				case "trailing-document":
					raw = append(raw, []byte(" {}")...)
					want = e.CodeEnvelopeDecode
				case "malformed":
					raw = []byte("{")
					want = e.CodeEnvelopeDecode
				}
				diagnostics := diskdiagnostic.NewCollector()
				inspection, err := decodeBoundDiskContentReport(raw, image, binding, diagnostics)
				if diagnostics.DeprecationWarning() != "" {
					t.Fatal("current or rejected report emitted a deprecation warning")
				}
				if want != "" {
					assertDependencyCode(t, err, want)
					if inspection != nil {
						t.Fatal("rejected report returned content authority")
					}
					return
				}
				if err != nil || inspection == nil {
					t.Fatalf("valid decoder failed: %v", err)
				}
				components, files := inspection.Components(), inspection.Files()
				components[0].Properties[0].Value = "changed"
				files[0].Path = "changed"
				again, err := json.Marshal(inspection.report)
				if err != nil || !bytes.Equal(raw, again) {
					t.Fatal("detached descriptions mutated decoded observation")
				}
			})
		}
	}
}

func TestBoundDiskContentDecoderProjectsLegacyProperties(t *testing.T) {
	for _, spelling := range []string{"legacy", "duplicate", "mixed"} {
		t.Run(spelling, func(t *testing.T) {
			fixture := contentFixture("disk-image", "arm64", "b")
			report := fixture.report
			report.Components[0].Properties = append(report.Components[0].Properties, Property{Name: diskContainedInProperty, Value: "cache/packages.tar@" + fixture.runtimeDigest})
			for i := range report.Components[0].Properties {
				report.Components[0].Properties[i].Name = strings.Replace(report.Components[0].Properties[i].Name, "nvkr:", "nkd:", 1)
			}
			slices.SortFunc(report.Components[0].Properties, func(a, b Property) int { return strings.Compare(a.Name, b.Name) })
			if spelling != "legacy" {
				property := report.Components[0].Properties[0]
				if spelling == "mixed" {
					property.Name = diskContentClassProperty
				}
				report.Components[0].Properties = append(report.Components[0].Properties, property)
			}
			raw, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			original := bytes.Clone(raw)
			diagnostics := diskdiagnostic.NewCollector()
			inspection, err := decodeBoundDiskContentReport(raw, fixture.runtimeDigest, fixture.report, diagnostics)
			if got := diagnostics.DeprecationWarning() != ""; got != (spelling == "legacy") {
				t.Fatal("deprecation warning must describe only an accepted legacy report")
			}
			if !bytes.Equal(raw, original) {
				t.Fatal("projection rewrote original report bytes")
			}
			if spelling != "legacy" {
				assertDependencyCode(t, err, e.CodeEnvelopeDecode)
				if inspection != nil {
					t.Fatal("ambiguous report minted an inspection")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, property := range inspection.Components()[0].Properties {
				if !strings.HasPrefix(property.Name, "nvkr:") {
					t.Fatal("legacy report escaped projection", property.Name)
				}
			}
		})
	}
}

func TestBoundDiskContentDecoderRejectsUnsortedProperties(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, sibling := range []bool{false, true} {
			t.Run(fmt.Sprintf("legacy=%t/sibling=%t", legacy, sibling), func(t *testing.T) {
				fixture := contentFixture("disk-image", "arm64", "b")
				report := fixture.report
				if legacy {
					for i := range report.Components[0].Properties {
						report.Components[0].Properties[i].Name = strings.Replace(report.Components[0].Properties[i].Name, "nvkr:", "nkd:", 1)
					}
				}
				index := 0
				if sibling {
					other := cloneComponent(report.Components[0])
					for i := range other.Properties {
						other.Properties[i].Name = strings.Replace(other.Properties[i].Name, "nkd:", "nvkr:", 1)
					}
					report.Components = append(report.Components, other)
					index = len(report.Components) - 1
				}
				slices.Reverse(report.Components[index].Properties)
				raw, err := json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				diagnostics := diskdiagnostic.NewCollector()
				inspection, err := decodeBoundDiskContentReport(raw, fixture.runtimeDigest, fixture.report, diagnostics)
				assertDependencyCode(t, err, e.CodeInvalidCatalog)
				if inspection != nil || diagnostics.DeprecationWarning() != "" {
					t.Fatal("invalid property ordering minted an observation or deprecation notice")
				}
			})
		}
	}
}
