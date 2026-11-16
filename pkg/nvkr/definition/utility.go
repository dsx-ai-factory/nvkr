// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"sort"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func (environment Environment) validateUtilityContainers() error {
	if environment.UtilityContainers == nil {
		return nil
	}
	if environment.UtilityContainers[environment.UtilityPlatform] != environment.UtilityContainer {
		return e.New(e.CodeInvalidCatalog, "utilityContainers must include the exact default utilityContainer")
	}
	platforms := make([]string, 0, len(environment.UtilityContainers))
	for platform := range environment.UtilityContainers {
		platforms = append(platforms, platform)
	}
	sort.Strings(platforms)
	for _, platform := range platforms {
		if platform != "linux/amd64" && platform != "linux/arm64" {
			return e.New(e.CodeEngineCapability, "unsupported utility platform %q", platform)
		}
		if !containerPinPattern.MatchString(environment.UtilityContainers[platform]) {
			return e.New(e.CodeUnpinnedSource, "utilityContainers[%s] must be digest-pinned", platform)
		}
	}
	return nil
}

// OutputUtility selects native utility execution when explicitly enabled. The
// legacy Environment retains its single utility platform and canonical bytes.
func (environment Environment) OutputUtility(platform string) (container, executionPlatform string, err error) {
	if environment.UtilityContainers == nil {
		return environment.UtilityContainer, environment.UtilityPlatform, nil
	}
	if platform == "" {
		platform = environment.UtilityPlatform
	}
	container, exists := environment.UtilityContainers[platform]
	if !exists {
		return "", "", e.New(e.CodeEngineCapability, "output utilities have no container for %s", platform)
	}
	return container, platform, nil
}
