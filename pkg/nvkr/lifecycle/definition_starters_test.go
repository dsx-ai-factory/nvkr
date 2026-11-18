// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// Synthetic pins describe compilation fixtures, never externally built artifacts.
func starterCatalog(t *testing.T, payload string, disk bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "catalog"), 0o700); err != nil {
		t.Fatal(err)
	}
	component := map[string]any{"source": "fixture", "fromSource": true, "platforms": []string{"linux/amd64"}, "outputType": payload, "buildCommand": []string{"make", "artifact"}, "artifactPath": "out/artifact", "writeDir": "out"}
	environment, err := factory.DefaultLocalEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if disk {
		delete(component, "writeDir")
		component["builderType"] = catalog.BuilderVMImage
		component["disk"] = catalog.DiskSpec{Format: "qcow2", Encryption: "none"}
		component["artifactPath"] = "out/artifact.qcow2"
		component["buildRequirements"] = execution.Requirements{Resources: &execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 1024, EphemeralStorageMiB: 4096}, AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64}, Capabilities: []execution.CapabilityRequirement{{Name: execution.CapabilityKVM, Policy: execution.CapabilityRequired}}, TimeoutSeconds: 3600}
		environment.ExecutionClasses = []execution.Class{{Name: "explicit-kvm", RunsOn: "local", HostPlatform: execution.HostPlatformLinuxAMD64, Capabilities: []execution.CapabilityName{execution.CapabilityKVM}, Resources: execution.ResourceCapacity{CPUMillis: 1000, MemoryMiB: 1024, EphemeralStorageMiB: 4096}, MaxTimeoutSeconds: 3600}}
	}
	registry := map[string]any{"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry", "metadata": map[string]string{"name": "starter-fixture"}, "sources": map[string]any{"fixture": map[string]any{"repo": "https://example.invalid/fixture", "constraint": "1.x", "toolchainImage": "golang@sha256:" + strings.Repeat("a", 64), "toolchainImagesByArch": map[string]string{"amd64": "golang@sha256:" + strings.Repeat("a", 64)}}}, "components": map[string]any{"artifact": component}}
	writeCanonicalTestFile(t, filepath.Join(root, "catalog/registry.yaml"), registry)
	lock := catalog.PinLock{APIVersion: "nvkr.dev/lock/v1", Kind: "PinLock", Pins: map[string]catalog.Pin{"fixture": {Tag: "v1.0.0", Commit: strings.Repeat("b", 40), TreeHash: strings.Repeat("c", 40), SourceDateEpoch: 1}}}
	writeCanonicalTestFile(t, filepath.Join(root, "catalog/pins.lock.yaml"), lock)
	environmentPath := filepath.Join(root, "environment.json")
	writeCanonicalTestFile(t, environmentPath, environment)
	return root, environmentPath
}

func TestDefinitionStarterFormatsCompileFromDestination(t *testing.T) {
	for _, tc := range []struct{ format, payload string }{
		{definition.OutputExecutable, "host-package"}, {definition.OutputOCI, "oci-image"}, {definition.OutputAPT, "host-package"},
		{definition.OutputHelm, "tarball"}, {definition.OutputBundle, "tarball"}, {definition.OutputDebianPackage, "host-package"}, {definition.OutputDiskImage, "disk-image"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			root, environmentPath := starterCatalog(t, tc.payload, tc.format == definition.OutputDiskImage)
			if err := os.Mkdir(filepath.Join(root, "defs"), 0700); err != nil {
				t.Fatal(err)
			}
			request := DefinitionInit{Root: root, Destination: "defs/starter.json", Name: "starter", Catalog: filepath.Join(root, "catalog"), Platform: "linux/amd64", Components: []string{"artifact"}, Format: tc.format}
			if tc.format == definition.OutputAPT {
				request.APTDistribution = "stable"
				request.APTComponent = "main"
			}
			if tc.format == definition.OutputDiskImage {
				request.Environment = environmentPath
			}
			// CWD is deliberately unrelated to both input root and destination.
			t.Chdir(t.TempDir())
			got, err := InitDefinition(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if got.Catalog.Base != "../catalog" || got.Outputs[0].Format != tc.format {
				t.Fatalf("wrong starter: %+v", got)
			}
			writeCanonicalTestFile(t, filepath.Join(root, request.Destination), got)
			plan, _, err := factory.CaptureCompileFiles(t.Context(), root, request.Destination, "environment.json", "test")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(plan.Definition, got) {
				t.Fatal("compiler changed starter")
			}
			if tc.format != definition.OutputDiskImage {
				prepared, err := PrepareDefaultLocal(t.Context(), root, request.Destination, "test", "attempt")
				if err != nil {
					t.Fatal(err)
				}
				if prepared.plan.Environment.Publication != nil || prepared.plan.Environment.Signing != nil || prepared.Description().EnvironmentDigest == "" {
					t.Fatal("default policy not bound to Run")
				}
				var snapshot factory.InputSnapshot
				if err := canonical.UnmarshalExact([]byte(prepared.inputs), &snapshot); err != nil {
					t.Fatal(err)
				}
				replay, err := factory.CompileSnapshot(t.Context(), snapshot, "test")
				if err != nil {
					t.Fatal(err)
				}
				digest, err := replay.Digest()
				if err != nil || digest != prepared.Description().PlanDigest {
					t.Fatalf("Run replay: %v", err)
				}
			}
		})
	}
}

func TestDefinitionStarterRefusesUnsupportedCombination(t *testing.T) {
	root, _ := starterCatalog(t, "host-package", false)
	for _, tc := range []struct {
		name   string
		change func(*DefinitionInit)
		code   e.Code
	}{
		{"missing destination", func(r *DefinitionInit) { r.Destination = "" }, e.CodeUsage},
		{"outside root", func(r *DefinitionInit) { r.Destination = "../outside.yaml" }, e.CodeUsage},
		{"outside catalog", func(r *DefinitionInit) { r.Catalog = t.TempDir() }, e.CodeUsage},
		{"unknown format", func(r *DefinitionInit) { r.Format = "unknown" }, e.CodeInvalidCatalog},
		{"image from package", func(r *DefinitionInit) { r.Format = definition.OutputOCI }, e.CodeInvalidCatalog},
		{"chart from package", func(r *DefinitionInit) { r.Format = definition.OutputHelm }, e.CodeInvalidCatalog},
		{"APT settings absent", func(r *DefinitionInit) { r.Format = definition.OutputAPT }, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := DefinitionInit{Root: root, Destination: "starter.yaml", Name: "starter", Catalog: filepath.Join(root, "catalog"), Platform: "linux/amd64", Components: []string{"artifact"}}
			tc.change(&request)
			got, err := InitDefinition(t.Context(), request)
			assertCode(t, err, tc.code)
			if !reflect.DeepEqual(got, Definition{}) {
				t.Fatal("partial starter returned")
			}
		})
	}
}

func TestDefinitionStarterDefaultRefusesVMAndEscapingDestination(t *testing.T) {
	root, _ := starterCatalog(t, "disk-image", true)
	request := DefinitionInit{Root: root, Destination: "starter.yaml", Name: "starter", Catalog: filepath.Join(root, "catalog"), Platform: "linux/amd64", Components: []string{"artifact"}, Format: definition.OutputDiskImage}
	_, err := InitDefinition(t.Context(), request)
	assertCode(t, err, e.CodeEngineCapability)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	request.Destination = "escape/starter.yaml"
	_, err = InitDefinition(t.Context(), request)
	assertCode(t, err, e.CodeUsage)
	request.Destination = "starter.yaml"
	if err := os.Symlink(filepath.Join(outside, "missing.yaml"), filepath.Join(root, request.Destination)); err != nil {
		t.Fatal(err)
	}
	_, err = InitDefinition(t.Context(), request)
	assertCode(t, err, e.CodeUsage)
}

func TestDefinitionStarterAdmissionPrecedesReads(t *testing.T) {
	for _, tc := range []struct {
		name, platform, format string
		canceled               bool
		code                   e.Code
	}{
		{"canceled", "linux/amd64", "executable", true, e.CodeTimeout},
		{"unsupported platform", "linux/riscv64", "executable", false, e.CodeInvalidCatalog},
		{"unsupported format", "linux/amd64", "unknown", false, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			_, err := InitDefinition(ctx, DefinitionInit{Root: filepath.Join(t.TempDir(), "absent"), Destination: "starter.yaml", Name: "starter", Catalog: "absent", Platform: tc.platform, Format: tc.format, Components: []string{"artifact"}})
			assertCode(t, err, tc.code)
		})
	}
}

func TestDefaultLocalExecutionAdmissionAndDryRun(t *testing.T) {
	root, _ := starterCatalog(t, "host-package", false)
	d := Definition{APIVersion: DefinitionAPIVersion, Kind: DefinitionKind, Metadata: Metadata{Name: "default-test"}, Catalog: Catalog{Base: "catalog"}, Outputs: []DefinitionOutput{{Name: "output", Format: OutputExecutable, Platform: "linux/amd64", Components: []string{"artifact"}}}}
	writeCanonicalTestFile(t, filepath.Join(root, "definition.json"), d)
	prepared, err := PrepareDefaultLocal(t.Context(), root, "definition.json", "test", "attempt")
	if err != nil {
		t.Fatal(err)
	}
	attempts := filepath.Join(root, "attempts")
	_, err = ExecuteLocal(t.Context(), prepared, LocalOptions{Publish: true, AttemptsRoot: attempts})
	assertCode(t, err, e.CodeEngineCapability)
	if _, err := os.Stat(attempts); !os.IsNotExist(err) {
		t.Fatalf("publication refusal created storage: %v", err)
	}
	called := false
	_, err = executeLocalWithDependencies(t.Context(), prepared, LocalOptions{DryRun: true, AttemptsRoot: attempts}, localExecutionDependencies{
		admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil },
		runFactory: func(_ context.Context, _ *factory.Plan, options factory.LocalRunOptions) (localExecutionResult, error) {
			called = true
			if !options.DryRun || options.CompileInputs == "" {
				t.Fatal("default inputs not handed to dry-run owner")
			}
			return nil, nil
		},
		writeRecordState: writeRunRecordState, beginDiskCommit: beginDiskAttemptTransaction,
	})
	if err != nil || !called {
		t.Fatalf("dry run failed: %v", err)
	}
	if _, err := os.Stat(attempts); !os.IsNotExist(err) {
		t.Fatalf("dry run created storage: %v", err)
	}
}

func TestDefinitionStarterDocumentedLayeredExamples(t *testing.T) {
	root := initRepoRoot(t)
	for _, tc := range []struct{ format, component string }{
		{definition.OutputOCI, "pause"}, {definition.OutputAPT, "kubectl"}, {definition.OutputHelm, "gpu-runtime-chart"}, {definition.OutputBundle, "gpu-runtime-chart"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			request := DefinitionInit{Root: root, Destination: "starter.yaml", Name: "starter", Catalog: filepath.Join(root, "testdata/examples/layered-catalog"), Platform: "linux/amd64", Format: tc.format, Components: []string{tc.component}}
			if tc.format == definition.OutputAPT {
				request.APTDistribution = "stable"
				request.APTComponent = "main"
			}
			if _, err := InitDefinition(t.Context(), request); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDefinitionStarterAcceptsPhysicalPathsUnderSymlinkedRoot(t *testing.T) {
	root, _ := starterCatalog(t, "host-package", false)
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	got, err := InitDefinition(t.Context(), DefinitionInit{Root: alias, Destination: filepath.Join(physical, "starter.yaml"), Name: "starter", Catalog: filepath.Join(physical, "catalog"), Platform: "linux/amd64", Components: []string{"artifact"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Catalog.Base != "catalog" {
		t.Fatalf("unexpected relative catalog: %s", got.Catalog.Base)
	}
	t.Chdir(alias)
	got, err = InitDefinition(t.Context(), DefinitionInit{Root: physical, Destination: "starter.yaml", Name: "starter", Catalog: "catalog", Platform: "linux/amd64", Components: []string{"artifact"}})
	if err != nil || got.Catalog.Base != "catalog" {
		t.Fatalf("logical cwd under physical root: %+v, %v", got, err)
	}
}
