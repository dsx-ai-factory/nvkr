package factory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

type vmPackerMaterialFixture struct {
	plan       *Plan
	node       Node
	payloads   map[string]string
	results    map[string]string
	components []string
}

func newVMPackerMaterialFixture(t *testing.T) vmPackerMaterialFixture {
	return newVMPackerMaterialFixtureWithContract(t, nil, map[string]string{"a-agent": "agent", "z-runtime": "runtime"})
}

func newVMPackerMaterialFixtureWithContract(
	t *testing.T,
	projection *material.Projection,
	bindings map[string]string,
) vmPackerMaterialFixture {
	t.Helper()
	t.Chdir(t.TempDir())
	definition, environment, stack := vmFactoryInputs(t, "linux/amd64", execution.CapabilityPreferred)
	catalogCopy := *stack.Catalog
	catalogCopy.Components = make(map[string]catalog.Component, len(stack.Catalog.Components)+2)
	for name, component := range stack.Catalog.Components {
		catalogCopy.Components[name] = component
	}
	image := catalogCopy.Components["image"]
	image.BuildCommand = nil
	image.Packer = &catalog.PackerConfig{
		ProjectPath: "os-image/packer", PackerVersion: "1.14.2", BuildName: "image.qemu.node",
		ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator",
		MaterialManifestVariable: "material_manifest", OutputDirectoryVariable: "output_directory",
		MaterialManifest: material.CloneProjection(projection),
	}
	image.Materials = bindings
	catalogCopy.Components["image"] = image

	rawByComponent := map[string][]byte{
		"agent":   []byte("agent package\n"),
		"runtime": []byte("runtime package\n"),
	}
	for _, component := range []string{"agent", "runtime"} {
		catalogCopy.Components[component] = catalog.Component{
			Platforms: []string{"linux/amd64"}, OutputType: "host-package", Tag: "v1.0.0", SourceDate: 1,
			Prebuilt: &catalog.Prebuilt{
				URL:      "https://example.test/" + component + "-{{.Arch}}.deb",
				Digests:  map[string]string{"amd64": pipeline.FileDigest(rawByComponent[component])},
				Evidence: &catalog.ExternalEvidence{None: true},
			},
		}
	}
	stack, err := catalog.ApplyStack(&catalogCopy)
	if err != nil {
		t.Fatal(err)
	}
	definition.Outputs[0].Components = []string{"agent", "image", "runtime"}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	fixture := vmPackerMaterialFixture{
		plan: plan, payloads: make(map[string]string), results: make(map[string]string),
		components: []string{"agent", "runtime"},
	}
	for _, candidate := range plan.Graph.Nodes {
		if candidate.Kind == NodeBuild && candidate.IRNode != "" {
			_, instruction, lookupErr := plan.producerInstruction(candidate)
			if lookupErr == nil && instruction.Component == "image" {
				fixture.node = candidate
				break
			}
		}
	}
	if fixture.node.ID == "" {
		t.Fatal("compiled fixture has no VM root node")
	}
	_, artifacts := planIndex(plan)
	for _, component := range fixture.components {
		payload := artifacts["artifact/node-image/"+component]
		record := artifacts["result/node-image/"+component]
		if err := os.MkdirAll(filepath.Dir(payload.Path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(payload.Path, rawByComponent[component], 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(record.Path), 0o700); err != nil {
			t.Fatal(err)
		}
		producer := fixture.producer(t, component)
		observed := pipeline.ArtifactResult{
			Component: component, OutputType: producer.OutputType,
			Qualification: pipeline.ArtifactProfileFreeAcquisition,
			EngineClass:   ir.EnginePrebuiltAcquire,
			SHA256:        producer.Digest, Size: int64(len(rawByComponent[component])),
		}
		raw, marshalErr := canonical.Marshal(observed)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := os.WriteFile(record.Path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		fixture.payloads[component] = payload.Path
		fixture.results[component] = record.Path
	}
	return fixture
}

func TestObserveVMPackerMaterialsEmitsNKEProducerContract(t *testing.T) {
	projection := &material.Projection{
		SchemaVersion: "nke.dev/os-image-materials/v1",
		Kinds: map[string]string{
			"image-inputs":  "archive",
			"installer-iso": "disk-image",
		},
	}
	fixture := newVMPackerMaterialFixtureWithContract(t, projection, map[string]string{
		"image-inputs": "agent", "installer-iso": "runtime",
	})
	observed, err := observeVMPackerMaterials(t.Context(), fixture.plan, fixture.node)
	if err != nil {
		t.Fatal(err)
	}
	defer observed.Close()
	raw, err := observed.manifest.ReadAllContext(t.Context(), maxProducerManifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	var manifest material.Manifest
	if err := canonical.UnmarshalExact(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != "nke.dev/os-image-materials/v1" || len(manifest.Materials) != 2 {
		t.Fatalf("NKE material manifest = %#v", manifest)
	}
	wantKinds := map[string]string{"image-inputs": "archive", "installer-iso": "disk-image"}
	for _, entry := range manifest.Materials {
		if entry.Kind != wantKinds[entry.Binding] || entry.Architecture != "amd64" ||
			entry.Path != "materials/"+entry.Binding || !canonical.IsDigest(entry.Digest) {
			t.Fatalf("NKE material entry = %#v", entry)
		}
	}
}

func (fixture vmPackerMaterialFixture) producer(t *testing.T, component string) ir.Node {
	t.Helper()
	for _, node := range fixture.plan.Builds[0].Graph.Nodes {
		if node.Component == component && (node.Kind == ir.KindBuild || node.Kind == ir.KindPull) {
			return node
		}
	}
	t.Fatalf("fixture has no producer for %q", component)
	return ir.Node{}
}

func (fixture vmPackerMaterialFixture) rootInstruction(t *testing.T) *ir.Node {
	t.Helper()
	for index := range fixture.plan.Builds[0].Graph.Nodes {
		if fixture.plan.Builds[0].Graph.Nodes[index].ID == fixture.node.IRNode {
			return &fixture.plan.Builds[0].Graph.Nodes[index]
		}
	}
	t.Fatal("fixture has no root instruction")
	return nil
}

func TestObserveVMPackerMaterialsBindsExactPlanClosure(t *testing.T) {
	fixture := newVMPackerMaterialFixture(t)
	observed, err := observeVMPackerMaterials(t.Context(), fixture.plan, fixture.node)
	if err != nil {
		t.Fatal(err)
	}
	defer observed.Close()
	if len(observed.inputs) != 2 || observed.manifest == nil {
		t.Fatalf("observed material closure = %#v", observed)
	}
	raw, err := observed.manifest.ReadAllContext(t.Context(), maxProducerManifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	var manifest material.Manifest
	if err := canonical.UnmarshalExact(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != material.ManifestSchemaVersion || len(manifest.Materials) != 2 {
		t.Fatalf("material manifest = %#v", manifest)
	}
	for _, entry := range manifest.Materials {
		if entry.Architecture != "amd64" {
			t.Fatalf("material architecture = %q, want amd64", entry.Architecture)
		}
	}
}

func TestObserveVMPackerMaterialsRejectsPlanAndByteSubstitution(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *vmPackerMaterialFixture)
		code   e.Code
	}{
		{name: "substituted producer", code: e.CodePipelineDrift, mutate: func(t *testing.T, fixture *vmPackerMaterialFixture) {
			fixture.rootInstruction(t).Materials[0].Producer = "pull-runtime"
		}},
		{name: "missing payload edge", code: e.CodePipelineDrift, mutate: func(_ *testing.T, fixture *vmPackerMaterialFixture) {
			fixture.node.Inputs = removeString(fixture.node.Inputs, "artifact/node-image/agent")
		}},
		{name: "missing result edge", code: e.CodePipelineDrift, mutate: func(_ *testing.T, fixture *vmPackerMaterialFixture) {
			fixture.node.Inputs = removeString(fixture.node.Inputs, "result/node-image/agent")
		}},
		{name: "missing artifact", code: e.CodePipelineDrift, mutate: func(_ *testing.T, fixture *vmPackerMaterialFixture) {
			fixture.plan.Graph.Artifacts = removeArtifact(fixture.plan.Graph.Artifacts, "artifact/node-image/agent")
		}},
		{name: "duplicate artifact", code: e.CodePipelineDrift, mutate: func(_ *testing.T, fixture *vmPackerMaterialFixture) {
			fixture.plan.Graph.Artifacts = append(fixture.plan.Graph.Artifacts, fixture.plan.Graph.Artifacts[0])
		}},
		{name: "wrong payload producer", code: e.CodePipelineDrift, mutate: mutateArtifact("artifact/node-image/agent", func(artifact *Artifact) { artifact.Producer = "node-image/pull-runtime" })},
		{name: "wrong output kind", code: e.CodePipelineDrift, mutate: mutateArtifact("artifact/node-image/agent", func(artifact *Artifact) { artifact.Kind = "raw" })},
		{name: "wrong component", code: e.CodePipelineDrift, mutate: mutateArtifact("artifact/node-image/agent", func(artifact *Artifact) { artifact.Component = "runtime" })},
		{name: "wrong build target", code: e.CodePipelineDrift, mutate: mutateArtifact("artifact/node-image/agent", func(artifact *Artifact) { artifact.BuildTarget = "other" })},
		{name: "changed payload", code: e.CodeVerifyFailed, mutate: func(t *testing.T, fixture *vmPackerMaterialFixture) {
			if err := os.WriteFile(fixture.payloads["agent"], []byte("changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink payload", code: e.CodeVerifyFailed, mutate: func(t *testing.T, fixture *vmPackerMaterialFixture) {
			payload := fixture.payloads["agent"]
			if err := os.Remove(payload); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(fixture.payloads["runtime"], payload); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "directory payload", code: e.CodeVerifyFailed, mutate: func(t *testing.T, fixture *vmPackerMaterialFixture) {
			payload := fixture.payloads["agent"]
			if err := os.Remove(payload); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(payload, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unsupported architecture", code: e.CodeEngineCapability, mutate: func(t *testing.T, fixture *vmPackerMaterialFixture) {
			fixture.rootInstruction(t).Platform = "linux/riscv64"
		}},
		{name: "producer architecture mismatch", code: e.CodePipelineDrift, mutate: func(t *testing.T, fixture *vmPackerMaterialFixture) {
			for index := range fixture.plan.Builds[0].Graph.Nodes {
				if fixture.plan.Builds[0].Graph.Nodes[index].Component == "agent" {
					fixture.plan.Builds[0].Graph.Nodes[index].Platform = "linux/arm64"
					return
				}
			}
			t.Fatal("fixture has no agent producer")
		}},
		{name: "projection omits observed binding", code: e.CodeInvalidCatalog, mutate: func(t *testing.T, fixture *vmPackerMaterialFixture) {
			fixture.rootInstruction(t).Packer.MaterialManifest = &material.Projection{
				SchemaVersion: "nke.dev/os-image-materials/v1",
				Kinds:         map[string]string{"a-agent": "archive"},
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVMPackerMaterialFixture(t)
			test.mutate(t, &fixture)
			observed, err := observeVMPackerMaterials(t.Context(), fixture.plan, fixture.node)
			if observed != nil || factoryErrorCode(err) != test.code {
				t.Fatalf("observation/error = %#v/%v, want %s", observed, err, test.code)
			}
		})
	}
}

func TestObserveVMPackerMaterialsEnforcesBoundsAndCleansLateFailure(t *testing.T) {
	for _, test := range []struct {
		name   string
		limits producerMaterialLimits
		code   e.Code
	}{
		{name: "file count", limits: producerMaterialLimits{files: 1, perFile: 1024, total: 2048, manifest: 4096}, code: e.CodeEngineCapability},
		{name: "per file bytes", limits: producerMaterialLimits{files: 2, perFile: 2, total: 2048, manifest: 4096}, code: e.CodeUnverifiedRef},
		{name: "total bytes", limits: producerMaterialLimits{files: 2, perFile: 1024, total: 20, manifest: 4096}, code: e.CodeEngineCapability},
		{name: "manifest bytes", limits: producerMaterialLimits{files: 2, perFile: 1024, total: 2048, manifest: 1}, code: e.CodeInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVMPackerMaterialFixture(t)
			observed, err := observeVMPackerMaterialsWithLimits(t.Context(), fixture.plan, fixture.node, test.limits)
			if observed != nil || factoryErrorCode(err) != test.code {
				t.Fatalf("observation/error = %#v/%v, want %s", observed, err, test.code)
			}
		})
	}

	fixture := newVMPackerMaterialFixture(t)
	before := openDescriptorCount(t)
	if err := os.WriteFile(fixture.payloads["runtime"], []byte("changed late\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	observed, err := observeVMPackerMaterials(t.Context(), fixture.plan, fixture.node)
	if observed != nil || factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("late failure observation/error = %#v/%v", observed, err)
	}
	if after := openDescriptorCount(t); after != before {
		t.Fatalf("late material failure leaked held descriptors: before=%d after=%d", before, after)
	}
}

func removeString(values []string, remove string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != remove {
			result = append(result, value)
		}
	}
	return result
}

func removeArtifact(values []Artifact, remove string) []Artifact {
	result := make([]Artifact, 0, len(values))
	for _, value := range values {
		if value.ID != remove {
			result = append(result, value)
		}
	}
	return result
}

func mutateArtifact(id string, mutate func(*Artifact)) func(*testing.T, *vmPackerMaterialFixture) {
	return func(t *testing.T, fixture *vmPackerMaterialFixture) {
		t.Helper()
		for index := range fixture.plan.Graph.Artifacts {
			if fixture.plan.Graph.Artifacts[index].ID == id {
				mutate(&fixture.plan.Graph.Artifacts[index])
				return
			}
		}
		t.Fatalf("fixture has no artifact %q", id)
	}
}

func openDescriptorCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("descriptor cleanup check requires /dev/fd: %v", err)
	}
	return len(entries)
}
