// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
)

const releasePullResultSchemaVersion = "nvkr.dev/release-pull-result/v1"

// releasePullResult is a detached presentation of delivery.PulledOutput. It
// copies only explicitly chosen public fields, so private or future delivery
// fields never reach a script. It is diagnostic data, not a signature, policy or
// release-acceptance receipt, and it is never written to disk.
type releasePullResult struct {
	SchemaVersion     string                       `json:"schemaVersion"`
	Reference         string                       `json:"reference"`
	Output            releasePullOutput            `json:"output"`
	Destination       string                       `json:"destination"`
	Payload           string                       `json:"payload"`
	Files             []releasePullFile            `json:"files"`
	InspectionDetails releasePullInspectionDetails `json:"inspectionDetails"`
}

type releasePullOutput struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Digest      string `json:"digest"`
	BuildDigest string `json:"buildDigest"`
	// OS and Arch use release browse's flat names for a native artifact.
	OS   string `json:"os,omitempty"`
	Arch string `json:"arch,omitempty"`
	// ImageIndex mirrors release browse: an aggregate image lists every
	// platform it declares under imageIndex.requiredPlatforms.
	ImageIndex *releasePullImageIndex `json:"imageIndex,omitempty"`
}

type releasePullImageIndex struct {
	RequiredPlatforms []string `json:"requiredPlatforms"`
}

type releasePullFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// releasePullInspectionDetails states what the pull did and did not establish,
// using release browse's inspectionDetails.checks type so the two cannot drift.
type releasePullInspectionDetails struct {
	Checks releaseview.InspectionChecks `json:"checks"`
}

// releasePullChecks reports what a pull established. It validates the complete
// release metadata and evidence closure before it downloads the selected payload,
// then binds the payload bytes to that closure. As in browse, registry
// availability and installation compatibility are not established: the reference
// is the caller's locator and nothing is installed or run. It authenticates no
// signature and applies no consumer policy.
func releasePullChecks() releaseview.InspectionChecks {
	return releaseview.InspectionChecks{
		MetadataIntegrity: "checked", PayloadIntegrity: "checked",
		Signatures: "not-checked", ConsumerPolicy: "not-checked",
		RegistryAvailability: "not-checked", InstallationCompatibility: "not-checked",
	}
}

// releasePullPayload validates the shape delivery chose for the pulled output.
// An empty or unknown shape is an error rather than a guess.
func releasePullPayload(result delivery.PulledOutput) (string, error) {
	switch result.Payload {
	case delivery.PulledPayloadFiles, delivery.PulledPayloadDirectory:
		return string(result.Payload), nil
	}
	return "", e.New(e.CodeInternal, "release pull result has no payload representation for output kind %q", result.Output.Kind)
}

func newReleasePullResult(reference, out string, result delivery.PulledOutput) (releasePullResult, error) {
	payload, err := releasePullPayload(result)
	if err != nil {
		return releasePullResult{}, err
	}
	output := releasePullOutput{Name: result.Output.Name, Kind: result.Output.Kind,
		Digest: result.Output.Digest, BuildDigest: result.Output.BuildDigest}
	output.OS, output.Arch = result.Output.OS, result.Output.Arch
	if result.Output.ImageIndex != nil {
		output.ImageIndex = &releasePullImageIndex{RequiredPlatforms: slices.Clone(result.Output.ImageIndex.RequiredPlatforms)}
	}
	files := make([]releasePullFile, 0, len(result.Files))
	for _, file := range result.Files {
		files = append(files, releasePullFile{Path: file.Path, SHA256: file.SHA256, Size: file.Size})
	}
	return releasePullResult{SchemaVersion: releasePullResultSchemaVersion, Reference: reference, Output: output,
		Destination: out, Payload: payload, Files: files,
		InspectionDetails: releasePullInspectionDetails{Checks: releasePullChecks()}}, nil
}

// renderReleasePullJSON returns exactly one JSON value followed by a newline.
func renderReleasePullJSON(reference, out string, result delivery.PulledOutput) ([]byte, error) {
	presented, err := newReleasePullResult(reference, out, result)
	if err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(presented, "", "  ")
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "encoding release pull result")
	}
	return append(raw, '\n'), nil
}
