// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
)

// Keep legacy tool IDs for the default utility platform. Native output scanners
// on another platform have independent acquisitions and paths in the same Plan.
func utilityToolName(environment authored.Environment, name, platform string) string {
	if name != "" && environment.UtilityContainers != nil && platform != environment.UtilityPlatform {
		return name + "/" + platform
	}
	return name
}

func sbomToolName(plan *Plan, node Node) string {
	return utilityToolName(plan.Environment, plan.Definition.SBOMTool, node.Platform)
}

func packageUtility(plan Plan, name string) (string, string, error) {
	for _, output := range plan.Definition.Outputs {
		if name == output.Name || name == outputPublicationPackage(output.Name) {
			return plan.Environment.OutputUtility(output.Platform)
		}
	}
	return plan.Environment.OutputUtility("")
}

func nativeUtilityRunner(environment authored.Environment, platform, fallback string) string {
	if environment.UtilityContainers != nil && environment.RunsOn == "" {
		switch platform {
		case "linux/amd64":
			return DefaultRunsOn
		case "linux/arm64":
			return "ubuntu-24.04-arm"
		}
	}
	return fallback
}
