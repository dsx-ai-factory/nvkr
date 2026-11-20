// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func selectedMetadataFixture(t *testing.T) (string, string) {
	t.Helper()
	return selectedMetadataFixtureWithSources(t, false)
}

func selectedMetadataFixtureWithSources(t *testing.T, fromSource bool) (string, string) {
	t.Helper()
	disk, _ := diskPublicationRequest(t, "amd64")
	executable := releasePublicationRequestFixture(t, "linux", "amd64")
	disk.Objects = append(disk.Objects, executable.Objects...)
	if fromSource {
		for index := range disk.Objects {
			object := &disk.Objects[index]
			if object.Evidence == nil {
				continue
			}
			var bom sbom.BOM
			if err := canonical.UnmarshalExact(bytes.TrimSuffix(object.Bytes, []byte{'\n'}), &bom); err != nil {
				t.Fatal(err)
			}
			bom.Metadata.Component.Properties = append(bom.Metadata.Component.Properties,
				sbom.Property{Name: "nvkr:fromSource", Value: "true"},
				sbom.Property{Name: "nvkr:repo", Value: "https://example.test/" + object.Evidence.Outputs[0] + ".git"},
				sbom.Property{Name: "nvkr:commit", Value: strings.Repeat("a", 40)})
			sort.Slice(bom.Metadata.Component.Properties, func(i, j int) bool {
				return bom.Metadata.Component.Properties[i].Name < bom.Metadata.Component.Properties[j].Name
			})
			var err error
			object.Bytes, err = bom.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			object.SemanticDigest, err = bom.Digest()
			if err != nil {
				t.Fatal(err)
			}
			object.Name = "sbom-" + strings.TrimPrefix(object.SemanticDigest, "sha256:")
		}
	}

	var outputs []Output
	for _, object := range disk.Objects {
		if object.Output == nil {
			continue
		}
		descriptor, err := PayloadDescriptor(t.Context(), object.Path)
		if err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, Output{Name: object.Name, Kind: object.Output.Format, Digest: descriptor.Digest,
			BuildDigest: object.Output.BuildDigest, Components: object.Output.Components, Disk: object.Output.Disk})
	}
	var err error
	disk.Release.ContentSubject, err = ContentDigest(outputs)
	if err != nil {
		t.Fatal(err)
	}
	prepared := filepath.Join(t.TempDir(), "prepared")
	if _, err := BuildPublicationRoot(t.Context(), prepared, disk); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "attested")
	result, err := AttestPreparedEvidence(t.Context(), prepared, root, func(_ context.Context, subject string, document []byte) ([]byte, error) {
		return diskFixtureBundle(t, subject, document), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return root, result.RootDigest
}

func TestSelectedMetadataPreservesOriginalRootAndExcludesUnselectedClosure(t *testing.T) {
	for _, names := range [][]string{{"node-image"}, {"tools"}, {"node-image", "tools"}} {
		t.Run(names[0], func(t *testing.T) {
			full, pin := selectedMetadataFixture(t)
			out := filepath.Join(t.TempDir(), "selected")
			result, err := CopySelectedMetadataWithExpectedRoot(t.Context(), full, out, pin, names)
			if err != nil || result.RootDigest != pin {
				t.Fatalf("copy = %+v, %v", result, err)
			}
			inspection, err := InspectSelectedMetadata(t.Context(), out, pin, names)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join(full, "index.json"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(out, "index.json"))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("original index changed: %v", err)
			}
			fullInspection, err := inspectMetadata(t.Context(), full, true)
			if err != nil || !reflect.DeepEqual(inspection.Root, fullInspection.Root) {
				t.Fatalf("original release changed: %v", err)
			}
			selection, err := newMetadataSelection(names)
			if err != nil {
				t.Fatal(err)
			}
			wantVisits := 0
			for _, object := range inspection.Root.Objects {
				if !selection.includes(object) {
					if _, err := os.Lstat(fixtureBlobPath(out, object.DescriptorDigest)); !os.IsNotExist(err) {
						t.Fatalf("unselected %s control copied: %v", object.Name, err)
					}
				}
				if object.Output != nil {
					manifest, err := flatManifest(t.Context(), full, object)
					if err != nil {
						t.Fatal(err)
					}
					for _, layer := range manifest.Layers {
						if _, err := os.Lstat(fixtureBlobPath(out, layer.Digest)); !os.IsNotExist(err) {
							t.Fatalf("output payload copied: %v", err)
						}
					}
				} else if selection.includes(object) {
					wantVisits++
				}
			}
			visits := 0
			err = VisitSelectedMetadataEvidence(t.Context(), out, pin, names, func(_ context.Context, _ string, _ []byte) error {
				visits++
				return nil
			})
			if err != nil || visits != wantVisits || visits == 0 {
				t.Fatalf("evidence visits = %d, want %d: %v", visits, wantVisits, err)
			}
			if len(names) == 1 {
				if _, err := InspectMetadata(t.Context(), out); err == nil {
					t.Fatal("sparse metadata passed complete metadata inspection")
				}
			}
		})
	}
}

func TestSelectedMetadataRequiresSharedEvidenceOwnedByUnselectedOutput(t *testing.T) {
	fixture := packagePullFixture(t, "apt-repository", "valid")
	inspection, err := Inspect(t.Context(), fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "selected")
	if _, err := CopySelectedMetadataWithExpectedRoot(t.Context(), fixture.root, out, inspection.RootDigest, []string{"tools"}); err != nil {
		t.Fatal(err)
	}
	for _, object := range inspection.Root.Objects {
		if object.Evidence == nil {
			continue
		}
		owner, err := evidenceOwner(inspection.Root.Objects, object.Evidence)
		if err != nil || owner.Name != "package" {
			t.Fatalf("fixture needs unselected attachment owner: %+v, %v", owner, err)
		}
		if _, err := os.Lstat(fixtureBlobPath(out, owner.DescriptorDigest)); !os.IsNotExist(err) {
			t.Fatalf("unselected attachment owner copied: %v", err)
		}
		manifest, err := flatManifest(t.Context(), out, object)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(fixtureBlobPath(out, manifest.Layers[0].Digest)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := InspectSelectedMetadata(t.Context(), out, inspection.RootDigest, []string{"tools"}); err == nil {
		t.Fatal("missing shared evidence accepted")
	}
}

func TestSelectedMetadataRejectsInvalidSelectionBeforeCommit(t *testing.T) {
	full, pin := selectedMetadataFixture(t)
	for _, tc := range []struct {
		name  string
		names []string
		pin   string
		code  e.Code
	}{
		{"empty", nil, pin, e.CodeUsage},
		{"duplicate", []string{"tools", "tools"}, pin, e.CodeUsage},
		{"unsorted", []string{"tools", "node-image"}, pin, e.CodeUsage},
		{"unknown", []string{"other"}, pin, e.CodeVerifyFailed},
		{"wildcard", []string{"*"}, pin, e.CodeUsage},
		{"missing root", []string{"tools"}, "", e.CodeUsage},
		{"wrong root", []string{"tools"}, digestBytes([]byte("other")), e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "selected")
			_, err := CopySelectedMetadataWithExpectedRoot(t.Context(), full, out, tc.pin, tc.names)
			assertRegistryCode(t, err, tc.code)
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatalf("failed copy committed destination: %v", err)
			}
		})
	}
}
