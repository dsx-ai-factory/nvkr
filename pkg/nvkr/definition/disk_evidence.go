// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"maps"
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// DiskEvidenceFor is the sole resolution of an output's authored evidence mode.
// Empty retains strict inspection. This description grants no inspection authority.
func (environment Environment) DiskEvidenceFor(output string) string {
	if mode, ok := environment.DiskEvidenceOverrides[output]; ok {
		return mode
	}
	return environment.DiskEvidence
}

func (environment Environment) validateDiskEvidence() error {
	valid := func(mode string) bool {
		return mode == "" || mode == "build-provenance-v1" || mode == "installed-packages-v1"
	}
	if !valid(environment.DiskEvidence) ||
		((environment.DiskEvidence != "" || len(environment.DiskEvidenceOverrides) != 0) &&
			(environment.GitHubARC != "native-kvm-v1" || environment.Publication == nil)) {
		return e.New(e.CodeEngineCapability, "disk evidence mode requires the explicit native ARC publication route")
	}
	for _, name := range slices.Sorted(maps.Keys(environment.DiskEvidenceOverrides)) {
		if name == "" || !valid(environment.DiskEvidenceOverrides[name]) {
			return e.New(e.CodeEngineCapability, "unsupported disk evidence override for %q", name)
		}
	}
	return nil
}

// ValidateDiskEvidenceOutputs rejects misspelled and non-disk overrides against
// the complete resolved Definition, before graph compilation or execution.
func (environment Environment) ValidateDiskEvidenceOutputs(definition Definition) error {
	for _, name := range slices.Sorted(maps.Keys(environment.DiskEvidenceOverrides)) {
		found := false
		for _, output := range definition.Outputs {
			if output.Name == name && output.Format == "disk-image" {
				found = true
			}
		}
		if !found {
			return e.New(e.CodeInvalidCatalog, "disk evidence override %q is not a selected disk output", name)
		}
	}
	return nil
}
