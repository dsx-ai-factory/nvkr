// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package material

import (
	"bytes"
	"encoding/json"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// MkosiManifest is mkosi's version-one JSON package report. Its package list is
// the installed-content input to the closure gate, not an acquisition manifest
// or an SBOM. The original report bytes must be retained as producer evidence.
type MkosiManifest struct {
	ManifestVersion int                `json:"manifest_version"`
	Config          MkosiConfig        `json:"config"`
	Packages        []InstalledPackage `json:"packages"`
	Extension       map[string]string  `json:"extension"`
}

type MkosiConfig struct {
	Name         string `json:"name"`
	Distribution string `json:"distribution"`
	Architecture string `json:"architecture"`
	OutputFormat string `json:"output_format"`
	Version      string `json:"version,omitempty"`
	Release      string `json:"release,omitempty"`
}

type InstalledPackage struct {
	Type         string `json:"type"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
}

// CheckMkosiClosure requires equality of the entire installed package selection
// with the retained inputs. It rejects missing, surplus, repeated, foreign-arch
// and substituted packages. Matching top-level requested names is insufficient.
// This checks a report; the execution owner must bind that report to the actual
// assembly and its byte-verified package materials before issuing a receipt.
func (inputs PackageInputs) CheckMkosiClosure(raw []byte) error {
	if err := inputs.AdmitAPT(); err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > 8<<20 {
		return e.New(e.CodeVerifyFailed, "mkosi manifest size outside supported bounds")
	}
	if err := canonical.ValidateFoldedJSONDocument(raw); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "validating mkosi manifest JSON")
	}
	var manifest MkosiManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding mkosi manifest")
	}
	arch := map[string]string{"amd64": "x86-64", "arm64": "arm64"}[inputs.Architecture]
	if manifest.ManifestVersion != 1 || manifest.Config.Architecture != arch || len(manifest.Extension) != 0 ||
		(manifest.Config.OutputFormat != "tar" && manifest.Config.OutputFormat != "directory" && manifest.Config.OutputFormat != "oci") {
		return e.New(e.CodeVerifyFailed, "mkosi manifest does not describe the selected root filesystem")
	}
	if len(manifest.Packages) != len(inputs.Packages) {
		return e.New(e.CodeVerifyFailed, "installed package count differs from the retained input closure")
	}
	expected := make(map[string]string, len(inputs.Packages))
	for _, item := range inputs.Packages {
		expected[item.Name+"\x00"+item.Architecture] = item.Version
	}
	for _, item := range manifest.Packages {
		key := item.Name + "\x00" + item.Architecture
		version, found := expected[key]
		if !found || item.Type != "deb" || item.Version != version {
			return e.New(e.CodeVerifyFailed, "installed package differs from the retained input closure")
		}
		delete(expected, key)
	}
	return nil
}
