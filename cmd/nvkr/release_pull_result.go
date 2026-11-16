// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const releasePullResultSchemaVersion = "nvkr.dev/release-pull-result/v1"

// Payload representations. A directory payload is the complete tree beneath the
// destination; it has no per-file records and no invented directory checksum.
const (
	releasePullPayloadFiles     = "files"
	releasePullPayloadDirectory = "directory"
)

// releasePullResult is a detached presentation of delivery.PulledOutput. It
// copies only explicitly chosen public fields, so private or future delivery
// fields never reach a script. It is diagnostic data, not a signature, policy or
// release-acceptance receipt, and it is never written to disk.
type releasePullResult struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Reference     string                 `json:"reference"`
	Output        releasePullOutput      `json:"output"`
	Destination   string                 `json:"destination"`
	Payload       string                 `json:"payload"`
	Files         []releasePullFile      `json:"files"`
	Scope         releasePullResultScope `json:"scope"`
}

type releasePullOutput struct {
	Name        string               `json:"name"`
	Kind        string               `json:"kind"`
	Digest      string               `json:"digest"`
	BuildDigest string               `json:"buildDigest"`
	Platform    *releasePullPlatform `json:"platform,omitempty"`
	// RequiredPlatforms lists every platform an aggregate image declares.
	RequiredPlatforms []string `json:"requiredPlatforms,omitempty"`
}

type releasePullPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type releasePullFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// releasePullResultScope states what the pull did and did not establish.
type releasePullResultScope struct {
	PayloadIntegrityChecked bool `json:"payloadIntegrityChecked"`
	SignaturesChecked       bool `json:"signaturesChecked"`
	PolicyChecked           bool `json:"policyChecked"`
}

// releasePullPayloads maps each output kind that release pull can materialize to
// its payload shape. OCI layouts and APT repositories are complete directories
// and carry no file records, so none is invented for them. A kind missing here is
// an error rather than a guess: delivery owns the kind list, and a newly
// supported kind must be classified deliberately before it can be presented.
var releasePullPayloads = map[string]string{
	"debian-package": releasePullPayloadFiles,
	"executable":     releasePullPayloadFiles,
	"disk-image":     releasePullPayloadFiles,
	"helm-oci":       releasePullPayloadFiles,
	"bundle-oci":     releasePullPayloadFiles,
	"apt-repository": releasePullPayloadDirectory,
	"oci":            releasePullPayloadDirectory,
}

func releasePullPayloadKind(kind string) (string, error) {
	payload, ok := releasePullPayloads[kind]
	if !ok {
		return "", e.New(e.CodeInternal, "release pull result has no payload representation for output kind %q", kind)
	}
	return payload, nil
}

func newReleasePullResult(reference, out string, result delivery.PulledOutput) (releasePullResult, error) {
	payload, err := releasePullPayloadKind(result.Output.Kind)
	if err != nil {
		return releasePullResult{}, err
	}
	output := releasePullOutput{Name: result.Output.Name, Kind: result.Output.Kind,
		Digest: result.Output.Digest, BuildDigest: result.Output.BuildDigest}
	if result.Output.OS != "" {
		output.Platform = &releasePullPlatform{OS: result.Output.OS, Arch: result.Output.Arch}
	}
	if result.Output.ImageIndex != nil {
		output.RequiredPlatforms = slices.Clone(result.Output.ImageIndex.RequiredPlatforms)
	}
	files := make([]releasePullFile, 0, len(result.Files))
	for _, file := range result.Files {
		files = append(files, releasePullFile{Path: file.Path, SHA256: file.SHA256, Size: file.Size})
	}
	return releasePullResult{SchemaVersion: releasePullResultSchemaVersion, Reference: reference, Output: output,
		Destination: out, Payload: payload, Files: files,
		Scope: releasePullResultScope{PayloadIntegrityChecked: true}}, nil
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
