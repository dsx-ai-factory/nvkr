package factory

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func diskMaterialTraceFixture(t *testing.T) (*Plan, *LocalRunResult) {
	return diskMaterialTraceFixtureWithSibling(t, false)
}

func diskMaterialTraceFixtureWithSibling(t *testing.T, sibling bool) (*Plan, *LocalRunResult) {
	return diskMaterialTraceFixtureWithNetwork(t, sibling, false)
}

func diskMaterialTraceFixtureWithNetwork(t *testing.T, sibling, networked bool) (*Plan, *LocalRunResult) {
	return diskMaterialTraceFixtureForPlatform(t, sibling, networked, "linux/amd64")
}

func diskMaterialTraceFixtureForPlatform(t *testing.T, sibling, networked bool, platform string) (*Plan, *LocalRunResult) {
	return diskMaterialTraceFixtureForPlatformAndEncryption(t, sibling, networked, platform, "none")
}

func diskMaterialTraceFixtureForPlatformAndEncryption(t *testing.T, sibling, networked bool, platform, encryption string) (*Plan, *LocalRunResult) {
	return diskMaterialTraceFixtureWithCapture(t, sibling, networked, platform, encryption, "")
}

func diskMaterialTraceFixtureWithCapture(t *testing.T, sibling, networked bool, platform, encryption, capture string, buildProvenance ...bool) (*Plan, *LocalRunResult) {
	t.Helper()
	fixture := newVMPackerMaterialFixtureForPlatform(t, nil, map[string]string{"a-agent": "agent", "z-runtime": "runtime"}, platform)
	base := fixture.plan.CatalogSnapshot
	image := base.Components["image"]
	image.Disk.Encryption = encryption
	if capture != "" {
		packer := *image.Packer
		packer.AcquisitionArchive = capture
		image.Packer = &packer
	}
	base.Components["image"] = image
	agent := base.Components["agent"]
	agent.Prebuilt = nil
	agent.FromSource = true
	agent.BuilderType = catalog.BuilderOCIHermetic
	agent.Repo, agent.Tag = "https://example.test/agent", "v1.0.0"
	agent.Commit, agent.TreeHash = strings.Repeat("a", 40), strings.Repeat("b", 40)
	agent.ToolchainImage = "example.test/agent@sha256:" + strings.Repeat("c", 64)
	agent.BuildCommand, agent.ArtifactPath = []string{"build-agent"}, "out/agent.deb"
	agent.WriteDir = "out"
	agent.Materials = map[string]string{"runtime": "runtime"}
	base.Components["agent"] = agent
	environment := fixture.plan.Environment
	if networked {
		image := base.Components["image"]
		image.BuildNetwork = ir.NetworkDefault
		base.Components["image"] = image
		environment.Runtime.BuildNetworkPolicy = authored.BuildNetworkProducerDeclared
	}
	if len(buildProvenance) != 0 && buildProvenance[0] {
		environment.GitHubARC = "native-kvm-v1"
		environment.DiskEvidence = "build-provenance-v1"
		environment.Publication = &authored.Publication{RepositoryPrefix: "example.test/nkd", Anonymous: true}
		environment.Signing = cosignSigningProfile()
		fixture.plan.Definition.CosignTool = "cosign"
		base.Tools = map[string]catalog.ToolPin{"cosign": {Format: ToolFormatTarGzip, URL: "https://example.test/cosign.tar.gz", Platform: environment.UtilityPlatform, ExecutablePath: "cosign", ArchiveDigest: testCosignArchiveDigest, ExecutableDigest: testCosignExecutableDigest}}
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition := fixture.plan.Definition
	if sibling {
		second := definition.Outputs[0]
		second.Name = "sibling-image"
		definition.Outputs = append(definition.Outputs, second)
	}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	owner := localDiskRunResult(t, plan)
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	_, artifacts := planIndex(plan)
	for _, component := range []string{"runtime", "agent"} {
		var operation Node
		for _, node := range plan.Graph.Nodes {
			if node.BuildTarget == "node-image" && node.IRNode != "" {
				_, instruction, err := plan.producerInstruction(node)
				if err == nil && instruction.Component == component && instruction.Kind != ir.KindFetch {
					operation = node
				}
			}
		}
		_, instruction, err := plan.producerInstruction(operation)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(fixture.payloads[component])
		if err != nil {
			t.Fatal(err)
		}
		payloadPath := artifacts["artifact/node-image/"+component].Path
		if err := os.MkdirAll(filepath.Dir(payloadPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(payloadPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		record := pipeline.ArtifactResult{Component: component, OutputType: instruction.OutputType,
			SHA256: pipeline.FileDigest(raw), Size: int64(len(raw)), AbsPath: artifacts["artifact/node-image/"+component].Path,
			Qualification: pipeline.ArtifactProfileFreeAcquisition, EngineClass: ir.EnginePrebuiltAcquire}
		if instruction.Kind == ir.KindBuild {
			observed, err := observeBuildMaterials(t.Context(), plan, operation)
			if err != nil {
				t.Fatal(err)
			}
			record.Qualification, record.EngineClass, record.BuilderType = pipeline.ArtifactUnqualified, instruction.BuilderType, instruction.BuilderType
			record.Toolchain, record.InputDigest, record.Path = instruction.Toolchain, "sha256:"+strings.Repeat("d", 64), instruction.ArtifactPath
			record.MaterialManifestDigest = observed.manifest.Digest()
			if err := observed.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if err := executeProducer(t.Context(), plan, operation, "docker", 1, io.Discard, io.Discard,
			func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) { return &record, nil }, owner); err != nil {
			t.Fatal(err)
		}
	}
	var operation Node
	for _, node := range plan.Graph.Nodes {
		if node.BuildTarget == "node-image" && node.IRNode == "build-image" {
			operation = node
		}
	}
	_, instruction, err := plan.producerInstruction(operation)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := observeVMPackerMaterials(t.Context(), plan, operation)
	if err != nil {
		t.Fatal(err)
	}
	defer observed.Close()
	payload := testQCOW2HeaderPayload()
	held := &fakeDiskProducerResult{payload: payload, digest: pipeline.FileDigest(payload), size: int64(len(payload))}
	result := pipeline.ArtifactResult{Component: "image", OutputType: "disk-image", Qualification: pipeline.ArtifactUnqualified,
		Toolchain: instruction.Toolchain, BuilderType: ir.BuilderVMImage, EngineClass: ir.BuilderVMImage,
		InputDigest: "sha256:" + strings.Repeat("d", 64), Path: instruction.ArtifactPath,
		SHA256: held.digest, Size: held.size, MaterialManifestDigest: observed.manifest.Digest()}
	if err := owner.recordProducerConsumption(t.Context(), plan, operation, instruction, &result, observed.observedProducerMaterials); err != nil {
		t.Fatal(err)
	}
	if err := populateHeldDiskProducerCompletion(t.Context(), plan, operation.ID, owner,
		func(context.Context, ir.Node) (*diskPayload, *pipeline.ArtifactResult, error) {
			return fakeDiskPayload(held), &result, nil
		}); err != nil {
		t.Fatal(err)
	}
	return plan, owner
}

func TestDiskMaterialTraceCannotReuseEqualBuildSiblingObservations(t *testing.T) {
	plan, owner := diskMaterialTraceFixtureWithSibling(t, true)
	if plan.Builds[0].BuildDigest != plan.Builds[1].BuildDigest {
		t.Fatal("fixture outputs do not share logical Build identity")
	}
	var sibling LifecycleOutputPlan
	var operation Node
	for _, output := range plan.LifecycleOutputs {
		if output.Name == "sibling-image" {
			sibling = output
		}
	}
	for _, node := range plan.Graph.Nodes {
		if node.BuildTarget == sibling.Name && node.IRNode == "build-image" {
			operation = node
		}
	}
	// Give the sibling its own successful held disk completion, with identical
	// bytes and Build Digest. It still lacks its own consumed-material receipts.
	completion := owner.diskCompletions[0]
	original := fakeDiskPayloadState(completion.Payload)
	held := &fakeDiskProducerResult{payload: append([]byte(nil), original.payload...), digest: original.digest, size: original.size}
	if err := populateHeldDiskProducerCompletion(t.Context(), plan, operation.ID, owner,
		func(context.Context, ir.Node) (*diskPayload, *pipeline.ArtifactResult, error) {
			return fakeDiskPayload(held), &completion.Result, nil
		}); err != nil {
		t.Fatal(err)
	}
	owner.Nodes = append(owner.Nodes, LocalNodeResult{ID: operation.ID, Kind: NodeBuild, Status: "ok"})
	if _, err := owner.ObserveDiskLifecyclePayload(t.Context(), plan, sibling); err != nil {
		t.Fatalf("sibling held payload is not independently observable: %v", err)
	}
	if trace, err := owner.observeDiskMaterials(t.Context(), plan, sibling); trace != nil || factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("sibling reused another output's material handoff: %#v/%v", trace, err)
	}
	trace, err := owner.observeDiskMaterials(t.Context(), plan, plan.LifecycleOutputs[0])
	if err != nil {
		t.Fatal(err)
	}
	subject := trace.Description().Subject
	if err := trace.ValidateSubject(sibling.Name, subject.PlanDigest, subject.BuildDigest, subject.Digest); factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("issued trace accepted an identical-byte sibling output: %v", err)
	}
}

func TestDiskMaterialTraceRetainsActualTransitiveConsumption(t *testing.T) {
	plan, owner := diskMaterialTraceFixture(t)
	// Replacing the descriptive result files after successful invocations must
	// neither rewrite nor remove the graph that those invocations consumed.
	_, artifacts := planIndex(plan)
	for _, component := range []string{"agent", "runtime"} {
		if err := os.WriteFile(artifacts["result/node-image/"+component].Path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	trace, err := owner.observeDiskMaterials(t.Context(), plan, plan.LifecycleOutputs[0])
	if err != nil {
		t.Fatal(err)
	}
	description := trace.Description()
	if len(description.Members) != 3 || len(description.Edges) != 3 || description.Coverage != diskMaterialCoverageStagedOutputs {
		t.Fatalf("trace = %#v", description)
	}
	if description.Members[0].Producer != "build-agent" || description.Members[1].Producer != "build-image" ||
		description.Members[2].Producer != "pull-runtime" || description.Edges[0].Consumer != "build-agent" ||
		description.Edges[0].Producer != "pull-runtime" {
		t.Fatalf("unordered or flattened trace = %#v", description)
	}
	for index, want := range []diskMaterialProduction{diskMaterialProductionBuilt, diskMaterialProductionBuilt, diskMaterialProductionAcquired} {
		if got := description.Members[index].Production; got != want {
			t.Fatalf("member %q production = %q, want %q", description.Members[index].Producer, got, want)
		}
	}
	wantEdges := []diskMaterialEdge{
		{Consumer: "build-agent", Binding: "runtime", Producer: "pull-runtime", ManifestDigest: owner.materialConsumptions["node-image/build-agent"].manifestDigest},
		{Consumer: "build-image", Binding: "a-agent", Producer: "build-agent", ManifestDigest: owner.materialConsumptions["node-image/build-image"].manifestDigest},
		{Consumer: "build-image", Binding: "z-runtime", Producer: "pull-runtime", ManifestDigest: owner.materialConsumptions["node-image/build-image"].manifestDigest},
	}
	if !reflect.DeepEqual(description.Edges, wantEdges) {
		t.Fatalf("sorted exact edges = %#v, want %#v", description.Edges, wantEdges)
	}
	if err := trace.ValidateSubject(description.OutputName, description.Subject.PlanDigest, description.Subject.BuildDigest, description.Subject.Digest); err != nil {
		t.Fatal(err)
	}
	description.Members[0].Digest = "changed"
	description.Edges[0].Binding = "changed"
	description.Subject.Components[0] = "changed"
	description.OutputName = "changed"
	if fresh := trace.Description(); fresh.Members[0].Digest == "changed" || fresh.Edges[0].Binding == "changed" || fresh.Subject.Components[0] == "changed" || fresh.OutputName == "changed" {
		t.Fatal("description changed issued observation")
	}
	raw, err := canonical.Marshal(owner)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "materialConsumptions") || strings.Contains(string(raw), "manifestDigest") {
		t.Fatal("private consumption capability escaped local Run serialization")
	}
}

func TestDiskMaterialTraceRejectsIncompleteOrSubstitutedInvocations(t *testing.T) {
	for _, test := range []struct {
		name     string
		mutate   func(*LocalRunResult)
		wantText string
	}{
		{"missing root", func(owner *LocalRunResult) { delete(owner.materialConsumptions, "node-image/build-image") }, "disk root has no exact consumed material manifest"},
		{"missing transitive", func(owner *LocalRunResult) { delete(owner.materialConsumptions, "node-image/pull-runtime") }, "has no exact retained invocation"},
		{"changed acquisition", func(owner *LocalRunResult) {
			receipt := owner.materialConsumptions["node-image/pull-runtime"]
			receipt.digest = "sha256:" + strings.Repeat("f", 64)
			owner.materialConsumptions["node-image/pull-runtime"] = receipt
		}, "has no exact retained invocation"},
		{"changed plan", func(owner *LocalRunResult) {
			receipt := owner.materialConsumptions["node-image/build-agent"]
			receipt.planDigest = "sha256:" + strings.Repeat("f", 64)
			owner.materialConsumptions["node-image/build-agent"] = receipt
		}, "has no exact retained invocation"},
		{"missing input", func(owner *LocalRunResult) {
			receipt := owner.materialConsumptions["node-image/build-agent"]
			receipt.materials = nil
			owner.materialConsumptions["node-image/build-agent"] = receipt
		}, "consumption set differs from its Plan"},
		{"substituted binding", func(owner *LocalRunResult) {
			owner.materialConsumptions["node-image/build-agent"].materials[0].Binding = "other"
		}, "consumed a substituted material"},
		{"substituted component", func(owner *LocalRunResult) {
			owner.materialConsumptions["node-image/build-agent"].materials[0].Component = "other"
		}, "consumed a substituted material"},
		{"substituted kind", func(owner *LocalRunResult) {
			owner.materialConsumptions["node-image/build-agent"].materials[0].Kind = "raw"
		}, "consumed a substituted material"},
		{"substituted architecture", func(owner *LocalRunResult) {
			owner.materialConsumptions["node-image/build-agent"].materials[0].Architecture = "arm64"
		}, "consumed a substituted material"},
		{"root size", func(owner *LocalRunResult) {
			record := owner.materialConsumptions["node-image/build-image"]
			record.size++
			owner.materialConsumptions["node-image/build-image"] = record
		}, "has no exact retained invocation"},
		{"duplicate binding", func(owner *LocalRunResult) {
			receipt := owner.materialConsumptions["node-image/build-image"]
			receipt.materials[1] = receipt.materials[0]
			owner.materialConsumptions["node-image/build-image"] = receipt
		}, "repeats a consumed binding"},
		{"root manifest", func(owner *LocalRunResult) {
			receipt := owner.materialConsumptions["node-image/build-image"]
			receipt.manifestDigest = "sha256:" + strings.Repeat("f", 64)
			owner.materialConsumptions["node-image/build-image"] = receipt
		}, "disk root has no exact consumed material manifest"},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, owner := diskMaterialTraceFixture(t)
			test.mutate(owner)
			trace, err := owner.observeDiskMaterials(t.Context(), plan, plan.LifecycleOutputs[0])
			if trace != nil || factoryErrorCode(err) != e.CodeVerifyFailed || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("trace/error = %#v/%v", trace, err)
			}
		})
	}
}

func TestDiskMaterialTraceRejectsOtherSubjectsAndLifecycle(t *testing.T) {
	plan, owner := diskMaterialTraceFixture(t)
	trace, err := owner.observeDiskMaterials(t.Context(), plan, plan.LifecycleOutputs[0])
	if err != nil {
		t.Fatal(err)
	}
	description := trace.Description()
	subject := description.Subject
	for _, test := range []struct{ name, output, plan, build, disk string }{
		{"output", "other", subject.PlanDigest, subject.BuildDigest, subject.Digest},
		{"plan", description.OutputName, "other", subject.BuildDigest, subject.Digest},
		{"build", description.OutputName, subject.PlanDigest, "other", subject.Digest},
		{"disk", description.OutputName, subject.PlanDigest, subject.BuildDigest, "other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := trace.ValidateSubject(test.output, test.plan, test.build, test.disk); factoryErrorCode(err) != e.CodeVerifyFailed {
				t.Fatalf("error = %v", err)
			}
		})
	}
	for _, unissued := range []*diskMaterialTrace{nil, {}} {
		if err := unissued.ValidateSubject(description.OutputName, subject.PlanDigest, subject.BuildDigest, subject.Digest); factoryErrorCode(err) != e.CodeVerifyFailed {
			t.Fatalf("unissued error = %v", err)
		}
		if !reflect.DeepEqual(unissued.Description(), diskMaterialTraceDescription{}) {
			t.Fatal("unissued trace returned a description")
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := owner.observeDiskMaterials(canceled, plan, plan.LifecycleOutputs[0]); factoryErrorCode(err) != e.CodeTimeout {
		t.Fatalf("canceled error = %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.observeDiskMaterials(t.Context(), plan, plan.LifecycleOutputs[0]); factoryErrorCode(err) != e.CodeGateUnmet {
		t.Fatalf("closed error = %v", err)
	}
}

func TestDiskMaterialTraceDisclosesNetworkedBuilds(t *testing.T) {
	for _, networked := range []bool{false, true} {
		t.Run(map[bool]string{false: "offline", true: "networked"}[networked], func(t *testing.T) {
			plan, owner := diskMaterialTraceFixtureWithNetwork(t, false, networked)
			trace, err := owner.observeDiskMaterials(t.Context(), plan, plan.LifecycleOutputs[0])
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			if networked {
				want = []string{"build-image"}
			}
			if got := trace.Description().UntracedNetworkBuilds; !reflect.DeepEqual(got, want) {
				t.Fatalf("untraced network builds = %v, want %v", got, want)
			}
		})
	}
}

func TestVMDispatchRetainsZeroMaterialConsumption(t *testing.T) {
	t.Chdir(t.TempDir())
	plan, operation := compiledPackerVMNode(t)
	owner := localDiskRunResult(t, plan)
	defer owner.Close()
	artifact, description, err := dispatchAdmittedVMProducer(t.Context(), new(fakeVMAdmission), plan,
		operation, new(tracingVMExecutor), owner)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Close()
	record, ok := owner.materialConsumptions[operation.ID]
	if !ok || description.MaterialManifestDigest != "" || record.manifestDigest != "" || len(record.materials) != 0 {
		t.Fatalf("zero-material handoff = %#v, result %#v", record, description)
	}
	// Dispatch owns recording; factory's existing held-payload test seam supplies
	// framing bytes without pretending a zero-valued PackerArtifact owns them.
	payload := testQCOW2HeaderPayload()
	held := &fakeDiskProducerResult{payload: payload, digest: pipeline.FileDigest(payload), size: int64(len(payload))}
	_, instruction, err := plan.producerInstruction(operation)
	if err != nil {
		t.Fatal(err)
	}
	description.Qualification, description.BuilderType, description.EngineClass = pipeline.ArtifactUnqualified, ir.BuilderVMImage, ir.BuilderVMImage
	description.Toolchain, description.Path, description.InputDigest = instruction.Toolchain, instruction.ArtifactPath, "sha256:"+strings.Repeat("d", 64)
	if err := populateHeldDiskProducerCompletion(t.Context(), plan, operation.ID, owner,
		func(context.Context, ir.Node) (*diskPayload, *pipeline.ArtifactResult, error) {
			return fakeDiskPayload(held), description, nil
		}); err != nil {
		t.Fatal(err)
	}
	trace, err := owner.observeDiskMaterials(t.Context(), plan, plan.LifecycleOutputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := trace.Description(); len(got.Members) != 1 || len(got.Edges) != 0 ||
		got.Coverage != diskMaterialCoverageStagedOutputs || got.Members[0].Production != diskMaterialProductionBuilt {
		t.Fatalf("zero-material trace = %#v", got)
	}
}

func TestRecordProducerConsumptionRejectsWrongManifest(t *testing.T) {
	fixture := newVMPackerMaterialFixture(t)
	owner := localDiskRunResult(t, fixture.plan)
	defer owner.Close()
	observed, err := observeVMPackerMaterials(t.Context(), fixture.plan, fixture.node)
	if err != nil {
		t.Fatal(err)
	}
	defer observed.Close()
	_, instruction, err := fixture.plan.producerInstruction(fixture.node)
	if err != nil {
		t.Fatal(err)
	}
	result := &pipeline.ArtifactResult{Component: instruction.Component, OutputType: instruction.OutputType,
		SHA256: "sha256:" + strings.Repeat("a", 64), Size: 1, MaterialManifestDigest: "sha256:" + strings.Repeat("f", 64)}
	if err := owner.recordProducerConsumption(t.Context(), fixture.plan, fixture.node, instruction, result, observed.observedProducerMaterials); factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("error = %v", err)
	}
	if len(owner.materialConsumptions) != 0 {
		t.Fatal("rejected invocation retained authority")
	}
	// Input descriptions cannot create a trace without the held disk completion.
	owner.materialConsumptions = map[string]producerConsumption{fixture.node.ID: {materials: []material.Observation{}}}
	if trace, err := owner.observeDiskMaterials(t.Context(), fixture.plan, fixture.plan.LifecycleOutputs[0]); trace != nil || factoryErrorCode(err) != e.CodeReleaseIncomplete {
		t.Fatalf("trace/error = %#v/%v", trace, err)
	}
}

func TestRecordProducerConsumptionRejectsUndeclaredMaterials(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*pipeline.ArtifactResult, *observedProducerMaterials)
	}{
		{"manifest identity", func(result *pipeline.ArtifactResult, _ *observedProducerMaterials) {
			result.MaterialManifestDigest = "sha256:" + strings.Repeat("f", 64)
		}},
		{"consumption observation", func(_ *pipeline.ArtifactResult, materials *observedProducerMaterials) {
			materials.observations = []material.Observation{{Binding: "undeclared"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, operation := compiledPackerVMNode(t)
			owner := localDiskRunResult(t, plan)
			defer owner.Close()
			_, instruction, err := plan.producerInstruction(operation)
			if err != nil {
				t.Fatal(err)
			}
			if len(instruction.Materials) != 0 {
				t.Fatal("fixture declares material inputs")
			}
			result := &pipeline.ArtifactResult{Component: instruction.Component, OutputType: instruction.OutputType}
			materials := &observedProducerMaterials{}
			test.mutate(result, materials)
			if err := owner.recordProducerConsumption(t.Context(), plan, operation, instruction, result, materials); factoryErrorCode(err) != e.CodeVerifyFailed || !strings.Contains(err.Error(), "recorded undeclared material inputs") {
				t.Fatalf("record error = %v", err)
			}
			if len(owner.materialConsumptions) != 0 {
				t.Fatal("undeclared inputs retained consumption observations")
			}
		})
	}
}

type tracingVMExecutor struct{ fakeVMDriverExecutor }

func (executor *tracingVMExecutor) RunPacker(ctx context.Context, runtime *VMRuntime, subject string,
	node ir.Node, materials *vmPackerMaterials,
) (*pipeline.PackerArtifact, *pipeline.ArtifactResult, error) {
	artifact, _, err := executor.fakeVMDriverExecutor.RunPacker(ctx, runtime, subject, node, materials)
	if err != nil {
		return artifact, nil, err
	}
	manifestDigest := ""
	if len(node.Materials) != 0 {
		manifestDigest = materials.manifest.Digest()
	}
	return artifact, &pipeline.ArtifactResult{Component: node.Component, OutputType: node.OutputType,
		SHA256: pipeline.FileDigest(testQCOW2HeaderPayload()), Size: int64(len(testQCOW2HeaderPayload())),
		MaterialManifestDigest: manifestDigest}, nil
}

func TestVMDispatchRetainsConsumptionBeforeClosingMaterials(t *testing.T) {
	fixture := newVMPackerMaterialFixture(t)
	relocated, err := relocateFactoryPlan(fixture.plan, ".nkd/trace-workspace")
	if err != nil {
		t.Fatal(err)
	}
	// Identity remains the compiled Plan's identity after workspace relocation.
	identity, err := relocated.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	original, err := fixture.plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if identity != original {
		t.Fatal("relocation changed logical Plan identity")
	}
	_, originals := planIndex(fixture.plan)
	_, relocatedArtifacts := planIndex(relocated)
	for _, component := range fixture.components {
		for _, prefix := range []string{"artifact", "result"} {
			id := prefix + "/node-image/" + component
			raw, err := os.ReadFile(originals[id].Path)
			if err != nil {
				t.Fatal(err)
			}
			mustWriteWorkspaceFixture(t, relocatedArtifacts[id].Path, raw)
		}
	}
	// Dispatch really consumes the relocated paths while retaining the original
	// compiled Plan identity, exactly as the local scheduler does.
	owner := localDiskRunResult(t, fixture.plan)
	defer owner.Close()
	admission := new(fakeVMAdmission)
	executor := new(tracingVMExecutor)
	artifact, _, err := dispatchAdmittedVMProducer(t.Context(), admission, relocated, fixture.node, executor, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Close()
	receipt, ok := owner.materialConsumptions[fixture.node.ID]
	if !ok || receipt.planDigest != identity || len(receipt.materials) != 2 || receipt.materials[0].Binding != "a-agent" {
		t.Fatalf("live dispatch did not retain handoff: %#v", receipt)
	}
	if admission.calls != 1 || executor.packerCalls != 1 {
		t.Fatal("record was not issued by actual dispatch callback")
	}
	if err := owner.recordProducerConsumption(t.Context(), fixture.plan, fixture.node,
		fixture.producer(t, "image"), &pipeline.ArtifactResult{Component: "image", OutputType: "disk-image"}, nil); factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("description without material handoff error = %v", err)
	}
}

func TestProducerConsumptionRejectsDuplicateAndClosedOwner(t *testing.T) {
	plan, owner := diskMaterialTraceFixture(t)
	operation := planNode(plan, "node-image/build-image")
	_, instruction, err := plan.producerInstruction(operation)
	if err != nil {
		t.Fatal(err)
	}
	materials, err := observeVMPackerMaterials(t.Context(), plan, operation)
	if err != nil {
		t.Fatal(err)
	}
	defer materials.Close()
	result := owner.diskCompletions[0].Result
	prior := owner.materialConsumptions[operation.ID]
	prior.materials = append([]material.Observation(nil), prior.materials...)
	if err := owner.recordProducerConsumption(t.Context(), plan, operation, instruction, &result, materials.observedProducerMaterials); factoryErrorCode(err) != e.CodeVerifyFailed || !strings.Contains(err.Error(), "duplicate consumption observations") {
		t.Fatalf("duplicate invocation error = %v", err)
	}
	if !reflect.DeepEqual(prior, owner.materialConsumptions[operation.ID]) {
		t.Fatal("duplicate invocation changed its original consumption observation")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.recordProducerConsumption(t.Context(), plan, operation, instruction, &result, materials.observedProducerMaterials); factoryErrorCode(err) != e.CodeGateUnmet {
		t.Fatalf("closed invocation error = %v", err)
	}
	if len(owner.materialConsumptions) != 0 {
		t.Fatal("closed owner retained observations")
	}
}

func TestRecordProducerConsumptionRejectsInvalidInputs(t *testing.T) {
	for _, test := range []struct {
		name     string
		mutate   func(context.Context, *Plan, *LocalRunResult, *pipeline.ArtifactResult) (context.Context, *Plan, *LocalRunResult, *pipeline.ArtifactResult)
		code     e.Code
		wantText string
	}{
		{"nil context", func(_ context.Context, p *Plan, o *LocalRunResult, r *pipeline.ArtifactResult) (context.Context, *Plan, *LocalRunResult, *pipeline.ArtifactResult) {
			return nil, p, o, r
		}, e.CodeUsage, "recording producer consumption requires a context"},
		{"canceled", func(ctx context.Context, p *Plan, o *LocalRunResult, r *pipeline.ArtifactResult) (context.Context, *Plan, *LocalRunResult, *pipeline.ArtifactResult) {
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			return ctx, p, o, r
		}, e.CodeTimeout, "recording producer consumption"},
		{"nil plan", func(ctx context.Context, _ *Plan, o *LocalRunResult, r *pipeline.ArtifactResult) (context.Context, *Plan, *LocalRunResult, *pipeline.ArtifactResult) {
			return ctx, nil, o, r
		}, e.CodeVerifyFailed, "requires a successful live result"},
		{"nil owner", func(ctx context.Context, p *Plan, _ *LocalRunResult, r *pipeline.ArtifactResult) (context.Context, *Plan, *LocalRunResult, *pipeline.ArtifactResult) {
			return ctx, p, nil, r
		}, e.CodeVerifyFailed, "requires a successful live result"},
		{"nil result", func(ctx context.Context, p *Plan, o *LocalRunResult, _ *pipeline.ArtifactResult) (context.Context, *Plan, *LocalRunResult, *pipeline.ArtifactResult) {
			return ctx, p, o, nil
		}, e.CodeVerifyFailed, "requires a successful live result"},
		{"different plan identity", func(ctx context.Context, p *Plan, o *LocalRunResult, r *pipeline.ArtifactResult) (context.Context, *Plan, *LocalRunResult, *pipeline.ArtifactResult) {
			changed := *p
			changed.GeneratorVersion += "-different"
			return ctx, &changed, o, r
		}, e.CodeVerifyFailed, "changes its Plan or output"},
		{"invalid plan identity", func(ctx context.Context, p *Plan, o *LocalRunResult, r *pipeline.ArtifactResult) (context.Context, *Plan, *LocalRunResult, *pipeline.ArtifactResult) {
			p.SchemaVersion = ""
			return ctx, p, o, r
		}, e.CodeInvalidCatalog, "run plan schema/name is invalid"},
		{"misbound component", func(ctx context.Context, p *Plan, o *LocalRunResult, r *pipeline.ArtifactResult) (context.Context, *Plan, *LocalRunResult, *pipeline.ArtifactResult) {
			r.Component = "other"
			return ctx, p, o, r
		}, e.CodeVerifyFailed, "changes its Plan or output"},
		{"misbound output type", func(ctx context.Context, p *Plan, o *LocalRunResult, r *pipeline.ArtifactResult) (context.Context, *Plan, *LocalRunResult, *pipeline.ArtifactResult) {
			r.OutputType = "raw"
			return ctx, p, o, r
		}, e.CodeVerifyFailed, "changes its Plan or output"},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, node := compiledPackerVMNode(t)
			owner := localDiskRunResult(t, plan)
			defer owner.Close()
			_, instruction, err := plan.producerInstruction(node)
			if err != nil {
				t.Fatal(err)
			}
			result := &pipeline.ArtifactResult{Component: instruction.Component, OutputType: instruction.OutputType}
			ctx, p, o, r := test.mutate(t.Context(), plan, owner, result)
			if err := o.recordProducerConsumption(ctx, p, node, instruction, r, nil); factoryErrorCode(err) != test.code || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("record error = %v, want %s", err, test.code)
			}
			if len(owner.materialConsumptions) != 0 {
				t.Fatal("rejected invocation retained observations")
			}
		})
	}
}

func TestRecordProducerConsumptionRejectsChangedBytes(t *testing.T) {
	for _, test := range []struct{ name, wantText string }{
		{"payload", "bytes differ from its recorded digest or size"},
		{"result file", "decoding producer result"},
		{"returned digest", "changed its successful output"},
		{"returned size", "changed its successful output"},
		{"held manifest", "rechecking consumed material manifest"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVMPackerMaterialFixture(t)
			owner := localDiskRunResult(t, fixture.plan)
			defer owner.Close()
			operation := planNode(fixture.plan, "node-image/pull-runtime")
			instruction := fixture.producer(t, "runtime")
			raw, err := os.ReadFile(fixture.results["runtime"])
			if err != nil {
				t.Fatal(err)
			}
			var result pipeline.ArtifactResult
			if err := canonical.UnmarshalExact(raw, &result); err != nil {
				t.Fatal(err)
			}
			var materials *observedProducerMaterials
			switch test.name {
			case "payload":
				if err := os.WriteFile(fixture.payloads["runtime"], []byte("substituted"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "result file":
				if err := os.WriteFile(fixture.results["runtime"], []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "returned digest":
				result.SHA256 = "sha256:" + strings.Repeat("f", 64)
			case "returned size":
				result.Size++
			case "held manifest":
				operation = fixture.node
				instruction = fixture.producer(t, "image")
				observed, err := observeVMPackerMaterials(t.Context(), fixture.plan, operation)
				if err != nil {
					t.Fatal(err)
				}
				materials = observed.observedProducerMaterials
				result.Component, result.OutputType, result.MaterialManifestDigest = instruction.Component, instruction.OutputType, materials.manifest.Digest()
				// Immutable snapshots reject writes; invalidate the underlying handle
				// to prove that the live manifest recheck still fails closed.
				if err := materials.manifest.OSFile().Close(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = observed.Close() })
			}
			if err := owner.recordProducerConsumption(t.Context(), fixture.plan, operation, instruction, &result, materials); factoryErrorCode(err) != e.CodeVerifyFailed || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("record error = %v", err)
			}
			if len(owner.materialConsumptions) != 0 {
				t.Fatal("changed producer bytes retained observations")
			}
		})
	}
}

func TestDiskMaterialTraceRejectsInvalidOwnerSchemaAndTier(t *testing.T) {
	for _, name := range []string{"schema", "tier"} {
		t.Run(name, func(t *testing.T) {
			plan, owner := diskMaterialTraceFixture(t)
			if name == "schema" {
				owner.SchemaVersion = "invalid"
			} else {
				owner.Qualification = TierStaticUnqualified
			}
			if _, err := owner.observeDiskMaterials(t.Context(), plan, plan.LifecycleOutputs[0]); factoryErrorCode(err) != e.CodeVerifyFailed {
				t.Fatalf("invalid owner error = %v", err)
			}
		})
	}
}

func TestLocalProducerRecordsOnlyDiskOutputInvocations(t *testing.T) {
	for _, test := range []struct {
		name            string
		disk, build, vm bool
	}{
		{name: "disk acquisition", disk: true, vm: true},
		{name: "disk build", disk: true, build: true, vm: true},
		{name: "non-disk sibling acquisition", vm: true},
		{name: "no VM owner", build: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var plan *Plan
			var operation Node
			if test.vm {
				fixture := newVMPackerMaterialFixture(t)
				plan = fixture.plan
				if test.build {
					base := plan.CatalogSnapshot
					component := base.Components["runtime"]
					component.Prebuilt = nil
					component.FromSource = true
					component.BuilderType = catalog.BuilderOCIHermetic
					component.Repo, component.Tag = "https://example.test/runtime", "v1.0.0"
					component.Commit, component.TreeHash = strings.Repeat("a", 40), strings.Repeat("b", 40)
					component.ToolchainImage = "example.test/runtime@sha256:" + strings.Repeat("c", 64)
					component.BuildCommand, component.ArtifactPath, component.WriteDir = []string{"build-runtime"}, "out/runtime.deb", "out"
					base.Components["runtime"] = component
					stack, err := catalog.ApplyStack(base)
					if err != nil {
						t.Fatal(err)
					}
					plan, err = Compile(t.Context(), plan.Definition, plan.Environment, stack, "test")
					if err != nil {
						t.Fatal(err)
					}
				}
				if !test.disk {
					definition := plan.Definition
					definition.Outputs = append(definition.Outputs, authored.DefinitionOutput{
						Name: "runtime-copy", Platform: "linux/amd64", Format: authored.OutputExecutable,
						Components: []string{"runtime"},
					})
					stack, err := catalog.ApplyStack(plan.CatalogSnapshot)
					if err != nil {
						t.Fatal(err)
					}
					plan, err = Compile(t.Context(), definition, plan.Environment, stack, "test")
					if err != nil {
						t.Fatal(err)
					}
				}
				id := "node-image/pull-runtime"
				if test.build {
					id = "node-image/build-runtime"
				} else if !test.disk {
					id = "runtime-copy/pull-runtime"
				}
				operation = planNode(plan, id)
			} else {
				plan = githubTestPlan(t)
				for _, node := range plan.Graph.Nodes {
					if node.Kind == NodeBuild && node.IRNode != "" {
						operation = node
						break
					}
				}
			}
			options := withLocalDefaults(LocalRunOptions{Workspace: ".nkd/record-filter", Jobs: len(plan.Graph.Nodes),
				vmResolver: &fakeVMDriverResolver{executor: &fakeVMDriverExecutor{}}})
			run := func(instruction ir.Node, call pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
				if call.BuildMaterials != nil {
					return nil, e.New(e.CodeInternal, "fixture unexpectedly has material inputs")
				}
				relocated, err := relocateFactoryPlan(plan, options.Workspace)
				if err != nil {
					return nil, err
				}
				_, artifacts := planIndex(relocated)
				path := artifacts["artifact/"+operation.BuildTarget+"/"+instruction.Component].Path
				payload := []byte("runtime package\n")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					return nil, err
				}
				if err := os.WriteFile(path, payload, 0o600); err != nil {
					return nil, err
				}
				result := &pipeline.ArtifactResult{Component: instruction.Component, OutputType: instruction.OutputType,
					SHA256: pipeline.FileDigest(payload), Size: int64(len(payload)), AbsPath: path,
					Qualification: pipeline.ArtifactProfileFreeAcquisition, EngineClass: ir.EnginePrebuiltAcquire}
				if instruction.Kind == ir.KindBuild {
					result.Qualification, result.BuilderType, result.EngineClass = pipeline.ArtifactUnqualified, instruction.BuilderType, instruction.BuilderType
					result.Toolchain, result.Path, result.InputDigest = instruction.Toolchain, instruction.ArtifactPath, "sha256:"+strings.Repeat("d", 64)
				}
				return result, nil
			}
			// Exercise automatic scheduler ownership, not a manually supplied owner.
			// Stop before the VM root: this partial-run contract is not a real bake
			// or evidence of a complete, successfully observed NKE material trace.
			stop := e.New(e.CodeBuildFailed, "stop after selected producer observation")
			var owner *LocalRunResult
			called := false
			deps := localRunDeps{
				admitVMBuilds: func(context.Context, *Plan, string) (map[string]vmAdmission, error) {
					return nil, nil
				},
				admitRunners: func(context.Context, *Plan, LocalRunOptions, []string) error { return nil },
				prepareRunner: func(context.Context, *Plan, LocalRunOptions, []string) (map[string]string, error) {
					return nil, nil
				},
				makeDisk: admittedLocalDiskProducer,
				executeNode: func(ctx context.Context, scheduledPlan *Plan, node Node, scheduled LocalRunOptions, _ map[string]string) error {
					if node.ID != operation.ID {
						return nil
					}
					called = true
					owner = scheduled.materialOwner
					if (owner != nil) != test.vm || scheduled.producers == nil {
						return e.New(e.CodeInternal, "scheduler did not thread the expected owner/session")
					}
					if err := executeLocalProducer(ctx, scheduledPlan, node, scheduled, run); err != nil {
						return err
					}
					if owner != nil {
						want := 0
						if test.disk {
							want = 1
						}
						if len(owner.materialConsumptions) != want {
							return e.New(e.CodeInternal, "retained %d invocations, want %d", len(owner.materialConsumptions), want)
						}
						if test.disk {
							if _, recorded := owner.materialConsumptions[node.ID]; !recorded {
								return e.New(e.CodeInternal, "selected disk producer has no consumption observation")
							}
							trace, err := owner.observeDiskMaterials(ctx, scheduledPlan, scheduledPlan.LifecycleOutputs[0])
							if trace != nil || factoryErrorCode(err) != e.CodeVerifyFailed {
								return e.New(e.CodeInternal, "partial run did not reject the absent disk producer outcome: %v", err)
							}
						}
					}
					return stop
				},
			}
			result, err := runLocal(t.Context(), plan, options, deps)
			if !called || result != nil || !errors.Is(err, stop) {
				t.Fatalf("partial run = %#v, %v; selected producer called = %t", result, err, called)
			}
			if owner != nil && (!owner.diskClosed || len(owner.materialConsumptions) != 0) {
				t.Fatal("failed partial run did not close and discard its material observations")
			}
		})
	}
}
