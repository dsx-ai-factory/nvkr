// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package target owns concrete artifact target platforms. Target support does not
// imply that an execution host has the capabilities needed to produce an artifact.
package target

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

// ValidatePlatform admits only the concrete platforms supported by the compiler.
// Empty values and wildcards never select a host-derived default.
func ValidatePlatform(platform string) error {
	switch platform {
	case "linux/amd64", "linux/arm64":
		return nil
	default:
		return e.New(e.CodeInvalidCatalog, "target platform %q must be linux/amd64 or linux/arm64", platform)
	}
}

// Arch returns the architecture of a validated platform. Invalid platforms return
// an empty value; callers must admit the platform before selecting pins or tools.
func Arch(platform string) string {
	switch platform {
	case "linux/amd64":
		return "amd64"
	case "linux/arm64":
		return "arm64"
	default:
		return ""
	}
}

// ValidateExecutablePlatform admits acquired native tools independently of the
// Linux source-build and OCI execution domains. It does not authorize execution.
func ValidateExecutablePlatform(platform string) error {
	if platform == "darwin/arm64" {
		return nil
	}
	return ValidatePlatform(platform)
}

// ExecutableArch returns the architecture of an admitted executable target.
func ExecutableArch(platform string) string {
	if platform == "darwin/arm64" {
		return "arm64"
	}
	return Arch(platform)
}

func ValidArch(arch string) bool { return arch == "amd64" || arch == "arm64" }

// Arches returns the supported architecture domain in canonical order.
func Arches() []string { return []string{"amd64", "arm64"} }

// ValidatePlatforms admits a nonempty, canonically ordered finite set of supported
// targets. Membership is authored explicitly; no host or default fills omissions.
func ValidatePlatforms(platforms []string) error {
	if len(platforms) == 0 {
		return e.New(e.CodeInvalidCatalog, "required platform set is empty")
	}
	previous := ""
	for _, platform := range platforms {
		if err := ValidatePlatform(platform); err != nil {
			return err
		}
		if platform <= previous {
			return e.New(e.CodeInvalidCatalog, "required platforms must be sorted and unique")
		}
		previous = platform
	}
	return nil
}
