// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package containerruntime owns container dialect admission and selection.
// Selection describes an executable; it does not authenticate an engine or its privileges.
package containerruntime

import (
	"path/filepath"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Default applies the shared Docker default without changing explicit selections.
// Callers must validate the result before executing it.
func Default(binary string) string {
	if binary == "" {
		return "docker"
	}
	return binary
}

// Validate admits only the existing Docker and Podman CLI dialects. An empty
// executable is not admitted; defaults belong at the selection boundary.
func Validate(binary string) error {
	switch filepath.Base(binary) {
	case "docker", "podman":
		return nil
	default:
		return e.New(e.CodeEngineCapability, "unsupported container runtime %q (docker|podman)", binary)
	}
}

// Select binds an optional executable override to the Environment's dialect.
// Environment values are names, while an override may be a path to that CLI.
// No filesystem or engine observation is performed here.
func Select(environment, override string) (string, error) {
	selected := Default(environment)
	if err := Validate(selected); err != nil {
		return "", err
	}
	if filepath.Base(selected) != selected {
		return "", e.New(e.CodeEngineCapability, "Environment container runtime must be docker or podman, got %q", environment)
	}
	if override == "" {
		return selected, nil
	}
	if err := Validate(override); err != nil {
		return "", err
	}
	if filepath.Base(override) != selected {
		return "", e.New(e.CodeEngineCapability, "container runtime %q conflicts with Environment runtime %q; omit --runtime to follow the Environment", override, selected)
	}
	return override, nil
}
