// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

func debianFactoryInputs(t *testing.T) (authored.Definition, authored.Environment, *catalog.Stack) {
	t.Helper()
	routes := []catalog.DebianRoute{}
	for _, distribution := range []string{"debian-13", "ubuntu-24.04"} {
		for _, platform := range []string{"linux/amd64", "linux/arm64"} {
			routes = append(routes, catalog.DebianRoute{Target: target.Debian{Distribution: distribution, Platform: platform},
				ToolchainImage: "example.test/toolchain@sha256:" + strings.Repeat("c", 64), BuildCommand: []string{"make", "deb", "ARCH={{.Arch}}"}, ArtifactPath: "out/agent-{{.Arch}}.deb", WriteDir: "out"})
		}
	}
	component := catalog.Component{Platforms: []string{"linux/amd64", "linux/arm64"}, FromSource: true, BuilderType: catalog.BuilderOCIHermetic, OutputType: catalog.OutputDebianPackage,
		Repo: "https://example.test/source", Tag: "v1.0.0", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), SourceDate: 1, Debian: &catalog.DebianSpec{Routes: routes}}
	stack, err := catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{"agent": component}})
	if err != nil {
		t.Fatal(err)
	}
	definition := authored.Definition{APIVersion: authored.DefinitionAPIVersion, Kind: authored.DefinitionKind, Metadata: authored.Metadata{Name: "deb"}, Catalog: authored.Catalog{Base: "catalog"}, Outputs: []authored.DefinitionOutput{{Name: "agent-output", Format: authored.OutputDebianPackage, Components: []string{"agent"}}}}
	environment := authored.Environment{APIVersion: authored.EnvironmentAPIVersion, Kind: authored.EnvironmentKind, Metadata: authored.Metadata{Name: "local"}, UtilityContainer: fixtureUtilityContainer, UtilityPlatform: "linux/amd64"}
	return definition, environment, stack
}

func TestDebianFactoryPlanFreezesExactTargetAndRawArchive(t *testing.T) {
	d, env, stack := debianFactoryInputs(t)
	plan, err := Compile(t.Context(), d, env, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	build := plan.Builds[0]
	if build.Platform != "linux/amd64" || build.Debian == nil || build.Debian.Distribution != "ubuntu-24.04" || plan.Definition.Outputs[0].Platform != "" {
		t.Fatal("target was not resolved separately from authored intent")
	}
	if plan.LifecycleOutputs[0].ArtifactID != "artifact/agent-output/agent" {
		t.Fatal("source archive was replaced by a wrapper")
	}
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodePackage || node.Kind == NodeSBOM {
			t.Fatal("compiled-only archive acquired unjoined packaging or SBOM work")
		}
	}
	for _, artifact := range plan.Graph.Artifacts {
		if artifact.ID == plan.LifecycleOutputs[0].ArtifactID && (!strings.HasSuffix(artifact.Path, "agent-amd64.deb") || artifact.Kind != authored.OutputDebianPackage || artifact.MediaType != "application/vnd.debian.binary-package") {
			t.Fatal("source archive projection differs")
		}
	}
	explicit := d.Clone()
	explicit.Outputs[0].Platform = "linux/amd64"
	explicit.Outputs[0].Debian = &authored.DebianOutput{Distribution: "ubuntu-24.04"}
	other, err := Compile(t.Context(), explicit, env, stack, "test")
	if err != nil || other.Builds[0].BuildDigest != build.BuildDigest {
		t.Fatal("explicit defaults changed Build identity", err)
	}
	for _, value := range []target.Debian{{Distribution: "debian-13", Platform: "linux/amd64"}, {Distribution: "ubuntu-24.04", Platform: "linux/arm64"}} {
		request := d.Clone()
		request.Outputs[0].Platform = value.Platform
		request.Outputs[0].Debian = &authored.DebianOutput{Distribution: value.Distribution}
		changed, err := Compile(t.Context(), request, env, stack, "test")
		if err != nil || changed.Builds[0].BuildDigest == build.BuildDigest {
			t.Fatal("target did not change identity", err)
		}
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPlanContext(t.Context(), path)
	if err != nil || loaded.Builds[0].Debian == nil || *loaded.Builds[0].Debian != *build.Debian {
		t.Fatal("exact Plan roundtrip lost target", err)
	}
}

func TestDebianFactoryRederivesAuthoredTargetAndRejectsExecution(t *testing.T) {
	d, env, stack := debianFactoryInputs(t)
	plan, err := Compile(t.Context(), d, env, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*Plan){
		func(p *Plan) { p.Builds[0].Debian = nil },
		func(p *Plan) { p.Builds[0].Debian.Distribution = "debian-13" },
		func(p *Plan) { p.Builds[0].Platform = "linux/arm64" },
	} {
		copy := cloneDebianTestPlan(t, plan)
		edit(copy)
		if err := copy.Validate(); factoryErrorCode(err) != e.CodeUnverifiedRef {
			t.Fatalf("mutated target admission = %v", err)
		}
	}
	if err := AdmitLocalPlan(t.Context(), plan); factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatalf("local admission = %v", err)
	}
	if bundle, err := RenderGitHub(t.Context(), plan); factoryErrorCode(err) != e.CodeEngineCapability || bundle != nil {
		t.Fatalf("hosted admission = %v", err)
	}
	for _, node := range plan.Graph.Nodes {
		if err := ExecuteNode(t.Context(), plan, node.ID, io.Discard, io.Discard); factoryErrorCode(err) != e.CodeEngineCapability {
			t.Fatalf("node %s admission = %v", node.ID, err)
		}
	}
	for _, edit := range []func(*authored.Definition, *authored.Environment){
		func(d *authored.Definition, env *authored.Environment) {
			env.Publication = &authored.Publication{RepositoryPrefix: "example.test/release", Anonymous: true}
		},
		func(d *authored.Definition, env *authored.Environment) {
			d.SBOMTool = "syft"
		},
	} {
		request, settings := d.Clone(), env.Clone()
		edit(&request, &settings)
		compiled, err := CompileDocuments(t.Context(), "/absent/definition.yaml", request, "/absent/environment.yaml", settings, "test")
		if factoryErrorCode(err) != e.CodeEngineCapability || compiled != nil {
			t.Fatalf("unjoined evidence lane reached catalog I/O: %v", err)
		}
	}
}

func cloneDebianTestPlan(t *testing.T, input *Plan) *Plan {
	t.Helper()
	raw, err := canonical.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var copy Plan
	if err := canonical.UnmarshalExact(raw, &copy); err != nil {
		t.Fatal(err)
	}
	return &copy
}

func TestDebianDryRunAdmitsForeignPlatformWithoutHostWork(t *testing.T) {
	d, env, stack := debianFactoryInputs(t)
	env.Runtime.Runtime = "docker"
	d.SBOMTool = "syft"
	pin := "sha256:" + strings.Repeat("c", 64)
	stack.Catalog.Tools = map[string]catalog.ToolPin{"syft": {Format: "raw", URL: "https://example.test/syft", Platform: "linux/amd64", ExecutablePath: "syft", ArchiveDigest: pin, ExecutableDigest: pin}}
	var err error
	stack, err = catalog.ApplyStack(stack.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	env.DiskInspectionProfiles = map[string]authored.DiskInspectionProfile{"linux/amd64": {Image: "example.test/inspect@" + pin, InspectorDigest: pin, CodecDigest: pin, GuestfishDigest: pin, ScannerDigest: pin, PackageReaderDigest: pin, Resources: execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 9000, EphemeralStorageMiB: 4096}, TimeoutSeconds: 30, MaxFilesystemBytes: 1 << 20, ScratchStorage: "private-volume"}}
	plan, err := Compile(t.Context(), d, env, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := AdmitLocalPlan(t.Context(), plan); err != nil {
		t.Fatal("static admission probed execution host", err)
	}
	root := t.TempDir()
	t.Chdir(root)
	result, err := RunLocal(t.Context(), plan, LocalRunOptions{DryRun: true, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result == nil {
		t.Fatal("static dry run required a native execution host/deadline", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("dry run wrote workspace", err)
	}
}
