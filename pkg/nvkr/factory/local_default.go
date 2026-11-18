// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"runtime"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// DefaultLocalEnvironmentPath is a logical snapshot entry, never a file read
// from or written to the checkout. Its bytes are frozen with every local Run.
const DefaultLocalEnvironmentPath = ".nvkr/builtin/local-environment-v1.json"

// CaptureCompileDefaultLocal compiles with the versioned build-only local
// policy and retains its exact bytes alongside all other compiler inputs.
// It grants no signing, publication, native VM or qualification capability.
func CaptureCompileDefaultLocal(ctx context.Context, root, definitionPath, generatorVersion string) (*Plan, InputSnapshot, error) {
	if err := snapshotContext(ctx); err != nil {
		return nil, InputSnapshot{}, err
	}
	environment, err := DefaultLocalEnvironment()
	if err != nil {
		return nil, InputSnapshot{}, err
	}
	if definitionPath == DefaultLocalEnvironmentPath {
		return nil, InputSnapshot{}, e.New(e.CodeUsage, "Definition cannot use the reserved built-in Environment path")
	}
	return captureCompileFiles(ctx, root, definitionPath, DefaultLocalEnvironmentPath, generatorVersion, &environment, nil)
}

// DefaultLocalEnvironment describes the built-in policy for this host. The
// compiler and local executor still admit every selected capability separately.
func DefaultLocalEnvironment() (authored.Environment, error) {
	return defaultLocalEnvironment(runtime.GOOS, runtime.GOARCH)
}

func defaultLocalEnvironment(goos, arch string) (authored.Environment, error) {
	if (goos != "linux" && goos != "darwin") || (arch != "amd64" && arch != "arm64") {
		return authored.Environment{}, e.New(e.CodeEngineCapability, "built-in local Environment supports Linux and macOS on amd64 or arm64; select an explicit Environment for another host")
	}
	// Existing maintained local utility pins; the host selects only utility
	// execution, never an output's explicitly declared target platform.
	pin := "957861185dbf29d93668ace41e011d091c6e779953d47f0316ef2cc58e28fe2b"
	if arch == "arm64" {
		pin = "08bcb99a38f85a265f9a93d1d3323e2440eec333380f3ea26ee0dec9264bb41c"
	}
	environment := authored.Environment{
		APIVersion: authored.EnvironmentAPIVersion, Kind: authored.EnvironmentKind,
		Metadata:         authored.Metadata{Name: "builtin-local-v1-" + arch},
		UtilityContainer: "golang@sha256:" + pin, UtilityPlatform: "linux/" + arch,
		Runtime:         authored.Runtime{Runtime: "docker", BuildNetworkPolicy: authored.BuildNetworkOfflineOnly},
		ToolchainAccess: &authored.ToolchainAccess{PublicRoots: []string{"docker.io/library/alpine", "docker.io/library/golang", "docker.io/moby/buildkit"}},
	}
	if err := environment.Validate(); err != nil {
		return authored.Environment{}, err
	}
	return environment, nil
}

// ValidateStarter compiles proposed authored values without writing them. All
// referenced files use the same root-confined reader as captured local Runs.
// Values are descriptions and gain no execution authority through this API.
func ValidateStarter(ctx context.Context, root, destination string, definition authored.Definition, environment authored.Environment) error {
	if destination == DefaultLocalEnvironmentPath {
		return e.New(e.CodeUsage, "starter destination uses the reserved built-in Environment path")
	}
	_, _, err := captureCompileFiles(ctx, root, destination, DefaultLocalEnvironmentPath, "definition-init", &environment, &definition)
	return err
}
