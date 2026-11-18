// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"runtime"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// DefaultLocalEnvironmentPath is the logical entry seeded by built-in
// preparation. Explicit Environment selection still reads a file at this path;
// the path is descriptive and never proves policy origin.
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
	plan, inputs, err := captureCompileFiles(ctx, root, definitionPath, DefaultLocalEnvironmentPath, generatorVersion, &environment, nil)
	if err != nil {
		return nil, InputSnapshot{}, err
	}
	if err := AdmitLocalPlan(ctx, plan); err != nil {
		return nil, InputSnapshot{}, err
	}
	return plan, inputs, nil
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
	// One maintained multi-platform utility index; the host selects only utility
	// execution, never an output's explicitly declared target platform.
	pin := "957861185dbf29d93668ace41e011d091c6e779953d47f0316ef2cc58e28fe2b"
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

// CompileStarter compiles proposed authored values without writing them. All
// referenced Definition/catalog files use the root-confined captured reader.
// The separately selected Environment is already decoded descriptive input.
// Values are descriptions and gain no execution authority through this API.
func CompileStarter(ctx context.Context, root, destination string, definition authored.Definition, environment authored.Environment) (*Plan, error) {
	if destination == DefaultLocalEnvironmentPath {
		return nil, e.New(e.CodeUsage, "starter destination uses the reserved built-in Environment path")
	}
	plan, _, err := captureCompileFiles(ctx, root, destination, DefaultLocalEnvironmentPath, "definition-init", &environment, &definition)
	return plan, err
}
