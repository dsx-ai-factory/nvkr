// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"slices"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/target"
)

func (output DefinitionOutput) validatePlatforms() error {
	if output.Platforms == nil {
		return target.ValidatePlatform(output.Platform)
	}
	if output.Platform != "" || output.Format != OutputOCI {
		return e.New(e.CodeInvalidCatalog, "output %q platforms require OCI format and exclude platform", output.Name)
	}
	return target.ValidatePlatforms(output.Platforms)
}

// TargetPlatforms returns a defensive view of the explicit target selection.
// This description does not admit an otherwise invalid Definition.
func (output DefinitionOutput) TargetPlatforms() []string {
	if len(output.Platforms) > 0 {
		return slices.Clone(output.Platforms)
	}
	return []string{output.Platform}
}

// SupportsPlatform checks whether the declared payload covers a chart's target.
// Complete Definition validation still owns admission of the selected set.
func (output DefinitionOutput) SupportsPlatform(platform string) bool {
	if len(output.Platforms) > 0 {
		return slices.Contains(output.Platforms, platform)
	}
	return output.Platform == platform
}
