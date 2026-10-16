// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// executePackage admits every declared input before dispatching format-specific I/O.
func executePackage(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact, stdout io.Writer) error {
	output, err := signNodeOutput(node, artifacts, node.PackageKind)
	if err != nil {
		return err
	}
	inputs, err := packageInputs(plan, node, artifacts)
	if err != nil {
		return err
	}
	switch node.PackageKind {
	case PackageImage:
		if node.ImageIndex != nil {
			return executeImageIndexPackage(ctx, plan, node, output, artifacts, stdout)
		}
		return executeImagePackage(ctx, plan, node, output, inputs, artifacts, stdout)
	case PackageBinaryBundle:
		return executeBinaryPackage(ctx, plan, output, inputs)
	case PackageAPT:
		return executeAPTPackage(ctx, plan, node, output, inputs)
	case PackageHelm:
		return executeHelmPackage(ctx, plan, node, output, inputs, artifacts, stdout)
	case PackageOCI:
		return executePublicationPackage(ctx, plan, node, output, inputs, artifacts)
	default:
		return e.New(e.CodeEngineCapability, "factory package kind %q has no adapter", node.PackageKind)
	}
}

func packageInputs(plan *Plan, node Node, artifacts map[string]Artifact) ([]Artifact, error) {
	var inputs []Artifact
	for _, id := range node.Inputs {
		planned := Artifact{}
		for _, candidate := range plan.Graph.Artifacts {
			if candidate.ID == id {
				planned = candidate
				break
			}
		}
		if planned.ID == "" {
			return nil, e.New(e.CodePipelineDrift, "package node %q has unknown input %q", node.ID, id)
		}
		if planned.Kind == string(TierTerminalObservation) || planned.Kind == "producer-result" {
			continue
		}
		artifact, ok := artifacts[id]
		if !ok || artifact.ID != planned.ID || artifact.Kind != planned.Kind || artifact.Name != planned.Name ||
			artifact.BuildTarget != planned.BuildTarget || artifact.Component != planned.Component {
			return nil, e.New(e.CodeReleaseIncomplete, "package node %q has no exact declared input %q", node.ID, id)
		}
		inputs = append(inputs, artifact)
	}
	return inputs, nil
}

func executeBinaryPackage(ctx context.Context, plan *Plan, output Artifact, inputs []Artifact) error {
	files := make([]delivery.BinaryFileInput, 0, len(inputs))
	for _, input := range inputs {
		files = append(files, delivery.BinaryFileInput{Name: input.Name, Path: input.Path,
			InstallPath: "/usr/local/bin/" + input.Name, Mode: 0o755})
	}
	_, err := delivery.BuildBinaryBundle(ctx, output.Path, delivery.BinaryBundleRequest{
		OS: "linux", Arch: packageArch(plan, inputs), Purpose: output.Name, Files: files,
	})
	return err
}

func packageArch(plan *Plan, inputs []Artifact) string {
	for _, input := range inputs {
		for _, buildPlan := range plan.Builds {
			if buildPlan.Name == input.BuildTarget {
				return strings.TrimPrefix(buildPlan.Platform, "linux/")
			}
		}
	}
	return "amd64"
}

func removeFactoryCarrier(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "checking stale factory carrier %s", path)
	}
	if !info.Mode().IsRegular() {
		return e.New(e.CodeImmutableViolation,
			"stale factory carrier %s is not a regular file", path)
	}
	if err := os.Remove(path); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "removing stale factory carrier %s", path)
	}
	return nil
}
