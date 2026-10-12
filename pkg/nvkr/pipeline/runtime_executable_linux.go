// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
)

const maxQualifiedRuntimeBytes int64 = 1 << 30

var dockerRuntimeEnvironment = []string{
	"HOME=/dev/null",
	"PATH=/nonexistent",
	"LANG=C",
	"LC_ALL=C",
	"TZ=UTC",
	// Procfs owns this directory and permits only numeric descriptor entries.
	// config.json and contexts cannot exist here. Docker treats their absence as
	// empty configuration, unlike /dev/null/config.json (ENOTDIR and a warning).
	"DOCKER_CONFIG=/proc/self/fd",
	"DOCKER_HOST=" + qualifiedRuntimeEndpoint,
}

var podmanRuntimeEnvironmentBase = []string{
	"PATH=/nonexistent",
	"LANG=C",
	"LC_ALL=C",
	"TZ=UTC",
	"REGISTRY_AUTH_FILE=/nonexistent",
	"XDG_CONFIG_HOME=/nonexistent",
}

func resolvedPodmanRuntimeEnvironment() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "resolving Podman user home")
	}
	paths := []struct {
		name     string
		value    string
		fallback string
	}{
		{name: "HOME", value: home},
		{name: "XDG_RUNTIME_DIR", value: os.Getenv("XDG_RUNTIME_DIR"), fallback: fmt.Sprintf("/run/user/%d", os.Getuid())},
		{name: "XDG_DATA_HOME", value: os.Getenv("XDG_DATA_HOME"), fallback: filepath.Join(home, ".local", "share")},
	}
	environment := append([]string(nil), podmanRuntimeEnvironmentBase...)
	for _, item := range paths {
		value := item.value
		if value == "" {
			value = item.fallback
		}
		if !safetext.Valid(value, 4096, false) || !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return nil, e.New(e.CodeEngineCapability,
				"Podman %s must be an absolute clean path", item.name)
		}
		environment = append(environment, item.name+"="+value)
	}
	return environment, nil
}

// snapshotRuntimeExecutable copies the resolved runtime inode into the shared
// sealed-held-file boundary. There is no snapshot pathname to rename or replace;
// qualified observation and execution inherit this descriptor directly.
func snapshotRuntimeExecutable(runtimePath string) (*heldRuntimeExecutable, error) {
	return snapshotRuntimeExecutableContext(context.Background(), runtimePath)
}

func snapshotRuntimeExecutableContext(ctx context.Context, runtimePath string) (*heldRuntimeExecutable, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "holding a container runtime requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "holding container runtime %s", runtimePath)
	}
	name := filepath.Base(runtimePath)
	environment := dockerRuntimeEnvironment
	if name == "podman" {
		podmanEnvironment, err := resolvedPodmanRuntimeEnvironment()
		if err != nil {
			return nil, err
		}
		environment = podmanEnvironment
	}
	resolved, err := filepath.EvalSymlinks(runtimePath)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "resolving container runtime %s", runtimePath)
	}
	capability, err := heldfile.OpenContext(ctx, resolved, heldfile.Options{
		Label: "qualified-runtime", Executable: true, MaxBytes: maxQualifiedRuntimeBytes,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "holding container runtime %s", runtimePath)
		}
		return nil, e.Wrap(e.CodeEngineCapability, err, "holding container runtime %s", runtimePath)
	}
	held := &heldRuntimeExecutable{
		capability: capability, name: name, endpoint: qualifiedRuntimeEndpoint,
		environment: append([]string(nil), environment...),
	}
	if name == "podman" {
		configurationRoot, err := os.MkdirTemp("", "nvkr-podman-config-")
		if err != nil {
			_ = held.close()
			return nil, e.Wrap(e.CodeEngineCapability, err, "creating private empty Podman configuration")
		}
		held.configurationRoot = configurationRoot
		for index, item := range held.environment {
			if item == "XDG_CONFIG_HOME=/nonexistent" {
				held.environment[index] = "XDG_CONFIG_HOME=" + configurationRoot
			}
		}
	}
	return held, nil
}
