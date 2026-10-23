// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func writeCanonicalRunFixture(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := canonical.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeTenantFixture(t, path, raw, 0600)
}

// Compile authored fixture files through the real capture and Run preparation
// boundaries. Sources and observations are synthetic; no build is executed.
func capturedArtifactRunFixture(t *testing.T, root string, outputs []lifecycle.DefinitionOutput, observed map[string]lifecycle.Artifact) (*factory.Plan, lifecycle.Run) {
	t.Helper()
	inputsRoot := privateTestDir(t)
	components := map[string]any{
		"tool": map[string]any{"source": "fixture", "platforms": []string{"linux/arm64"}, "fromSource": true,
			"outputType": "host-package", "buildCommand": []string{"make", "tool"}, "artifactPath": "out/tool", "writeDir": "out"},
		"image": map[string]any{"source": "fixture", "platforms": []string{"linux/arm64"}, "fromSource": true,
			"outputType": "oci-image", "buildCommand": []string{"make", "image"}, "artifactPath": "out/image", "writeDir": "out"},
		"disk": map[string]any{"source": "fixture", "platforms": []string{"linux/arm64"}, "fromSource": true,
			"builderType": catalog.BuilderVMImage, "outputType": "disk-image", "disk": catalog.DiskSpec{Format: "qcow2", Encryption: "none"},
			"buildCommand": []string{"make", "disk"}, "artifactPath": "out/disk.qcow2",
			"buildRequirements": execution.Requirements{
				Resources:            &execution.ResourceRequest{CPUMillis: 4000, MemoryMiB: 8192, EphemeralStorageMiB: 32768},
				AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxARM64},
				Capabilities:         []execution.CapabilityRequirement{{Name: execution.CapabilityKVM, Policy: execution.CapabilityRequired}},
				TimeoutSeconds:       7200,
			}},
	}
	if err := os.Mkdir(filepath.Join(inputsRoot, "catalog"), 0700); err != nil {
		t.Fatal(err)
	}
	writeCanonicalRunFixture(t, filepath.Join(inputsRoot, "catalog", "registry.yaml"), map[string]any{
		"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry", "components": components,
		"sources": map[string]catalog.Source{"fixture": {Repo: "https://example.invalid/fixture",
			ToolchainImagesByArch: map[string]string{"arm64": "example.invalid/compiler@sha256:" + strings.Repeat("c", 64)}}},
	})
	writeCanonicalRunFixture(t, filepath.Join(inputsRoot, "catalog", "pins.lock.yaml"), catalog.PinLock{
		APIVersion: "nvkr.dev/lock/v1", Kind: "PinLock", Pins: map[string]catalog.Pin{
			"fixture": {Tag: "v1.0.0", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), SourceDateEpoch: 1},
		},
	})
	writeCanonicalRunFixture(t, filepath.Join(inputsRoot, "definition.json"), lifecycle.Definition{
		APIVersion: lifecycle.DefinitionAPIVersion, Kind: lifecycle.DefinitionKind, Metadata: lifecycle.Metadata{Name: "fixture"},
		Catalog: lifecycle.Catalog{Base: "catalog"}, Outputs: outputs,
	})
	writeCanonicalRunFixture(t, filepath.Join(inputsRoot, "environment.json"), lifecycle.Environment{
		APIVersion: lifecycle.EnvironmentAPIVersion, Kind: lifecycle.EnvironmentKind, Metadata: lifecycle.Metadata{Name: "fixture"},
		UtilityContainer: "example.invalid/compiler@sha256:" + strings.Repeat("c", 64), UtilityPlatform: "linux/arm64",
		ExecutionClasses: []execution.Class{{Name: "arm64-kvm", RunsOn: "linux-arm64-cpu16", HostPlatform: execution.HostPlatformLinuxARM64,
			Resources:    execution.ResourceCapacity{CPUMillis: 8000, MemoryMiB: 16384, EphemeralStorageMiB: 65536},
			Capabilities: []execution.CapabilityName{execution.CapabilityKVM}, MaxTimeoutSeconds: 10800}},
	})
	plan, inputs, err := factory.CaptureCompileFiles(t.Context(), inputsRoot, "definition.json", "environment.json", "demo-fixture")
	if err != nil {
		t.Fatal(err)
	}
	return plan, writeCapturedRunFixture(t, root, plan, inputs, observed)
}

func writeCapturedRunFixture(t *testing.T, root string, plan *factory.Plan, inputs factory.InputSnapshot, observed map[string]lifecycle.Artifact) lifecycle.Run {
	t.Helper()
	run, err := lifecycle.PrepareRun(plan.Definition, plan.Environment, plan, filepath.Base(root))
	if err != nil {
		t.Fatal(err)
	}
	run.Status, run.Lane = lifecycle.RunComplete, factory.TierLocalExecution.String()
	for _, expected := range run.Expected {
		artifact := observed[expected.Name]
		artifact.Name, artifact.Kind, artifact.BuildDigest, artifact.Components = expected.Name, expected.Kind, expected.BuildDigest, expected.Components
		if artifact.Digest == "" {
			artifact.Digest = "sha256:" + strings.Repeat("1", 64)
		}
		if artifact.Disk != nil {
			disk := *artifact.Disk
			disk.PlanDigest = run.PlanDigest
			artifact.Disk = &disk
		}
		run.Observed = append(run.Observed, artifact)
	}
	run.ContentSubject, err = delivery.ContentDigest(run.Observed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".runner"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"lifecycle.json": run, ".runner/plan.json": plan, "compile-inputs.json": inputs} {
		writeCanonicalRunFixture(t, filepath.Join(root, name), value)
	}
	return *run
}
