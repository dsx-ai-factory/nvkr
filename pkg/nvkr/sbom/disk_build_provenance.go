// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// DiskBuildProvenance records producer evidence, never an installed inventory.
const DiskBuildProvenance = "build-provenance-v1"

// ForDiskBuildProvenance explicitly labels producer-only scope on an exact source-bound
// subject. The factory must authenticate that subject against its live producer.
func ForDiskBuildProvenance(bom *BOM, digest string) (*BOM, error) {
	result, err := ForSubject(bom, digest, "build-output", "")
	if err != nil {
		return nil, err
	}
	result.Metadata.Props = append(result.Metadata.Props,
		Property{Name: "nvkr:diskEvidence", Value: DiskBuildProvenance},
		Property{Name: "nvkr:diskInspection", Value: "not-performed"})
	sort.Slice(result.Metadata.Props, func(i, j int) bool { return result.Metadata.Props[i].Name < result.Metadata.Props[j].Name })
	if err := ValidateDiskBuildProvenance(result); err != nil {
		return nil, err
	}
	return result, nil
}

// ValidateDiskBuildProvenance rejects missing scope and any invented inspection
// claims. Exact byte, source and release membership checks remain with callers.
func ValidateDiskBuildProvenance(bom *BOM) error {
	if err := bom.Validate(); err != nil {
		return err
	}
	if len(bom.Components) != 0 || len(bom.Dependencies) != 0 {
		return e.New(e.CodeVerifyFailed, "disk build provenance cannot claim inspected dependencies")
	}
	props := map[string]string{}
	for _, p := range bom.Metadata.Props {
		props[p.Name] = p.Value
		if strings.HasPrefix(p.Name, "nvkr:dependency") || strings.HasPrefix(p.Name, "nvkr:diskInspect") && p.Name != "nvkr:diskInspection" || p.Name == "nvkr:diskCodecDigest" || p.Name == "nvkr:inspectionInputDigest" {
			return e.New(e.CodeVerifyFailed, "disk build provenance carries an inspection claim")
		}
	}
	// Read the original uninspected token without rewriting signed bytes. New
	// producers emit only not-performed; neither spelling proves inspection.
	uninspected := props["nvkr:diskInspection"] == "not-performed" || props["nvkr:diskInspection"] == "deferred-m3"
	if props["nvkr:diskEvidence"] != DiskBuildProvenance || !uninspected || props["nvkr:evidenceStage"] != "build-output" {
		return e.New(e.CodeProvenanceMissing, "disk build provenance lacks explicit evidence scope")
	}
	return nil
}
