package factory

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func diskMaterialTraceFixture(t *testing.T) (*Plan, *LocalRunResult) {
	return diskMaterialTraceFixtureWithSibling(t, false)
}

func diskMaterialTraceFixtureWithSibling(t *testing.T, sibling bool) (*Plan, *LocalRunResult) {
	t.Helper()
	fixture := newVMPackerMaterialFixture(t)
	base := fixture.plan.CatalogSnapshot
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
	plan, err := Compile(t.Context(), definition, fixture.plan.Environment, stack, "test")
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
		if err := executeProducerOwned(t.Context(), plan, operation, "docker", 1, io.Discard, io.Discard,
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

func TestDiskMaterialTraceCannotReuseEqualBuildSiblingReceipts(t *testing.T) {
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
	if trace, err := owner.ObserveDiskMaterials(t.Context(), plan, sibling); trace != nil || factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("sibling reused another output's material handoff: %#v/%v", trace, err)
	}
	trace, err := owner.ObserveDiskMaterials(t.Context(), plan, plan.LifecycleOutputs[0])
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
	trace, err := owner.ObserveDiskMaterials(t.Context(), plan, plan.LifecycleOutputs[0])
	if err != nil {
		t.Fatal(err)
	}
	description := trace.Description()
	if len(description.Members) != 3 || len(description.Edges) != 3 || description.Coverage != DiskMaterialCoverageStagedOutputs {
		t.Fatalf("trace = %#v", description)
	}
	if description.Members[0].Producer != "build-agent" || description.Members[1].Producer != "build-image" ||
		description.Members[2].Producer != "pull-runtime" || description.Edges[0].Consumer != "build-agent" ||
		description.Edges[0].Producer != "pull-runtime" {
		t.Fatalf("unordered or flattened trace = %#v", description)
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
		name   string
		mutate func(*LocalRunResult)
	}{
		{"missing root", func(owner *LocalRunResult) { delete(owner.materialConsumptions, "node-image/build-image") }},
		{"missing transitive", func(owner *LocalRunResult) { delete(owner.materialConsumptions, "node-image/pull-runtime") }},
		{"changed acquisition", func(owner *LocalRunResult) {
			receipt := owner.materialConsumptions["node-image/pull-runtime"]
			receipt.digest = "sha256:" + strings.Repeat("f", 64)
			owner.materialConsumptions["node-image/pull-runtime"] = receipt
		}},
		{"changed plan", func(owner *LocalRunResult) {
			receipt := owner.materialConsumptions["node-image/build-agent"]
			receipt.planDigest = "sha256:" + strings.Repeat("f", 64)
			owner.materialConsumptions["node-image/build-agent"] = receipt
		}},
		{"missing input", func(owner *LocalRunResult) {
			receipt := owner.materialConsumptions["node-image/build-agent"]
			receipt.materials = nil
			owner.materialConsumptions["node-image/build-agent"] = receipt
		}},
		{"substituted binding", func(owner *LocalRunResult) {
			owner.materialConsumptions["node-image/build-agent"].materials[0].Binding = "other"
		}},
		{"duplicate binding", func(owner *LocalRunResult) {
			receipt := owner.materialConsumptions["node-image/build-image"]
			receipt.materials[1] = receipt.materials[0]
			owner.materialConsumptions["node-image/build-image"] = receipt
		}},
		{"root manifest", func(owner *LocalRunResult) {
			receipt := owner.materialConsumptions["node-image/build-image"]
			receipt.manifestDigest = "sha256:" + strings.Repeat("f", 64)
			owner.materialConsumptions["node-image/build-image"] = receipt
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, owner := diskMaterialTraceFixture(t)
			test.mutate(owner)
			trace, err := owner.ObserveDiskMaterials(t.Context(), plan, plan.LifecycleOutputs[0])
			if trace != nil || factoryErrorCode(err) != e.CodeVerifyFailed {
				t.Fatalf("trace/error = %#v/%v", trace, err)
			}
		})
	}
}

func TestDiskMaterialTraceRejectsOtherSubjectsAndLifecycle(t *testing.T) {
	plan, owner := diskMaterialTraceFixture(t)
	trace, err := owner.ObserveDiskMaterials(t.Context(), plan, plan.LifecycleOutputs[0])
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
	for _, unissued := range []*DiskMaterialTrace{nil, {}} {
		if err := unissued.ValidateSubject(description.OutputName, subject.PlanDigest, subject.BuildDigest, subject.Digest); factoryErrorCode(err) != e.CodeVerifyFailed {
			t.Fatalf("unissued error = %v", err)
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := owner.ObserveDiskMaterials(canceled, plan, plan.LifecycleOutputs[0]); factoryErrorCode(err) != e.CodeTimeout {
		t.Fatalf("canceled error = %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ObserveDiskMaterials(t.Context(), plan, plan.LifecycleOutputs[0]); factoryErrorCode(err) != e.CodeGateUnmet {
		t.Fatalf("closed error = %v", err)
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
	if trace, err := owner.ObserveDiskMaterials(t.Context(), fixture.plan, fixture.plan.LifecycleOutputs[0]); trace != nil || factoryErrorCode(err) != e.CodeReleaseIncomplete {
		t.Fatalf("trace/error = %#v/%v", trace, err)
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
	return artifact, &pipeline.ArtifactResult{Component: node.Component, OutputType: node.OutputType,
		SHA256: pipeline.FileDigest(testQCOW2HeaderPayload()), Size: int64(len(testQCOW2HeaderPayload())),
		MaterialManifestDigest: materials.manifest.Digest()}, nil
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
	artifact, _, err := dispatchAdmittedVMProducerOwned(t.Context(), admission, relocated, fixture.node, executor, owner)
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
	if err := owner.recordProducerConsumption(t.Context(), plan, operation, instruction, &result, materials.observedProducerMaterials); factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("duplicate invocation error = %v", err)
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
