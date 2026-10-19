// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// Synthetic documents exercise release membership, not live scanner authority.
func TestDiskPublicationRequiresMaterialContentInventory(t *testing.T) {
	const architecture = "amd64"
	for _, method := range []string{"syft-cyclonedx/v1", "disk-reconciled/v1"} {
		t.Run(method, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, architecture)
			changeDiskFixtureComponentBOM(&request, "agent", func(bom *sbom.BOM) {
				if method == "disk-reconciled/v1" {
					// Keep the wrong-method document structurally valid so the
					// release's material-specific gate rejects its membership.
					bom.Metadata.Component.Type = "operating-system"
				}
				for index, property := range bom.Metadata.Props {
					if property.Name == "nvkr:dependencyInspection" {
						bom.Metadata.Props[index].Value = method
						return
					}
				}
				t.Fatal("fixture lacks material method")
			})
			output := filepath.Join(t.TempDir(), "rejected")
			_, err := BuildPublicationRoot(t.Context(), output, request)
			assertRegistryCode(t, err, e.CodeProvenanceMissing)
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatalf("rejected material evidence exposed output: %v", err)
			}
		})
	}
}

func TestDiskEvidenceScannerPinUsesOneProvenanceGate(t *testing.T) {
	for _, component := range []string{"image", "agent"} {
		t.Run(component, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, "amd64")
			changeDiskFixtureComponentBOM(&request, component, func(bom *sbom.BOM) {
				for index, property := range bom.Metadata.Props {
					if property.Name == "nvkr:dependencyScannerDigest" {
						bom.Metadata.Props = append(bom.Metadata.Props[:index], bom.Metadata.Props[index+1:]...)
						return
					}
				}
				t.Fatal("fixture has no scanner pin")
			})
			_, err := BuildPublicationRoot(t.Context(), filepath.Join(t.TempDir(), "refused"), request)
			assertRegistryCode(t, err, e.CodeProvenanceMissing)
			if !strings.Contains(err.Error(), "exact nvkr:dependencyScannerDigest") {
				t.Fatalf("wrong scanner provenance gate: %v", err)
			}
		})
	}
}
