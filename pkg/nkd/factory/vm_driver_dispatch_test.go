// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

type fakeVMAdmission struct {
	err      error
	closeErr error
	mutate   func(*Plan)
	calls    int
	nodeID   string
	callback bool
	closed   bool
}

type overlapDetectingWriter struct {
	active  atomic.Int32
	overlap atomic.Bool
}

func (writer *overlapDetectingWriter) Write(data []byte) (int, error) {
	if writer.active.Add(1) != 1 {
		writer.overlap.Store(true)
	}
	for range 8 {
		runtime.Gosched()
	}
	writer.active.Add(-1)
	return len(data), nil
}

func (admission *fakeVMAdmission) Close() error {
	admission.closed = true
	return admission.closeErr
}

func (admission *fakeVMAdmission) Execute(
	ctx context.Context,
	plan *Plan,
	nodeID string,
	work func(context.Context, *VMRuntime) error,
) error {
	admission.calls++
	admission.nodeID = nodeID
	if admission.err != nil {
		return admission.err
	}
	request, err := localVMRequestForPlan(plan, nodeID)
	if err != nil {
		return err
	}
	if admission.mutate != nil {
		admission.mutate(plan)
	}
	admission.callback = true
	return work(ctx, &VMRuntime{inner: &execution.LocalVMRuntime{}, subject: request.Subject})
}

type fakeVMDriverExecutor struct {
	packerCalls          int
	subject              string
	err                  error
	missing              bool
	missingResult        bool
	artifactOnError      bool
	corruptMaterialClose bool
	closeErr             error
	closed               bool
}

func (executor *fakeVMDriverExecutor) Close() error {
	executor.closed = true
	return executor.closeErr
}

type fakeVMDriverResolver struct {
	executor *fakeVMDriverExecutor
	err      error
	onAdmit  func()
	calls    int
	plan     *Plan
	session  *pipeline.NodeSession
	config   vmDriverRunConfig
}

func (resolver *fakeVMDriverResolver) Admit(
	_ context.Context,
	plan *Plan,
	session *pipeline.NodeSession,
	config vmDriverRunConfig,
) (vmDriverExecutor, error) {
	resolver.calls++
	if resolver.onAdmit != nil {
		resolver.onAdmit()
	}
	resolver.plan = plan
	resolver.session = session
	resolver.config = config
	if resolver.err != nil {
		return nil, resolver.err
	}
	if resolver.executor == nil {
		resolver.executor = new(fakeVMDriverExecutor)
	}
	return resolver.executor, nil
}

func (executor *fakeVMDriverExecutor) RunPacker(
	_ context.Context,
	_ *VMRuntime,
	subject string,
	node ir.Node,
	materials *vmPackerMaterials,
) (*pipeline.PackerArtifact, *pipeline.ArtifactResult, error) {
	executor.packerCalls++
	executor.subject = subject
	if executor.corruptMaterialClose && materials != nil && len(materials.inputs) > 0 &&
		materials.inputs[0].Content != nil {
		_ = materials.inputs[0].Content.OSFile().Close()
	}
	if executor.artifactOnError {
		return &pipeline.PackerArtifact{}, nil, executor.err
	}
	artifact, result, err := executor.result()
	if result != nil {
		result.Component, result.OutputType = node.Component, node.OutputType
		if len(node.Materials) != 0 {
			result.MaterialManifestDigest = materials.manifest.Digest()
		}
	}
	return artifact, result, err
}

type fakeVMArtifactCloser struct {
	err    error
	closed bool
}

func (artifact *fakeVMArtifactCloser) Close() error {
	artifact.closed = true
	return artifact.err
}

func (executor *fakeVMDriverExecutor) result() (*pipeline.PackerArtifact, *pipeline.ArtifactResult, error) {
	if executor.err != nil || executor.missing {
		return nil, nil, executor.err
	}
	if executor.missingResult {
		return &pipeline.PackerArtifact{}, nil, nil
	}
	return &pipeline.PackerArtifact{}, &pipeline.ArtifactResult{}, nil
}

func compiledPackerVMNode(t *testing.T) (*Plan, Node) {
	return compiledPackerVMNodeWithEncryption(t, "none")
}

func compiledPackerVMNodeWithEncryption(t *testing.T, encryption string) (*Plan, Node) {
	t.Helper()
	definition, environment, stack := vmFactoryInputs(t, "linux/amd64", execution.CapabilityPreferred)
	catalogCopy := *stack.Catalog
	catalogCopy.Components = make(map[string]catalog.Component, len(stack.Catalog.Components))
	for name, component := range stack.Catalog.Components {
		catalogCopy.Components[name] = component
	}
	component := catalogCopy.Components["image"]
	disk := *component.Disk
	disk.Encryption = encryption
	component.Disk = &disk
	component.BuildCommand = nil
	component.Packer = &catalog.PackerConfig{
		ProjectPath: "os-image/packer", PackerVersion: "1.14.2", BuildName: "image.qemu.node",
		ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator", MaterialManifestVariable: "material_manifest",
		OutputDirectoryVariable: "output_directory",
	}
	catalogCopy.Components["image"] = component
	stack, err := catalog.ApplyStack(&catalogCopy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definition, environment, stack, vmTestGeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodeBuild && node.IRNode != "" {
			return plan, node
		}
	}
	t.Fatal("compiled Packer VM Plan has no build node")
	return nil, Node{}
}

func compiledCommandVMNodeWithEncryption(t *testing.T, encryption string) (*Plan, Node) {
	t.Helper()
	definition, environment, stack := vmFactoryInputs(t, "linux/amd64", execution.CapabilityPreferred)
	catalogCopy := *stack.Catalog
	catalogCopy.Components = make(map[string]catalog.Component, len(stack.Catalog.Components))
	for name, component := range stack.Catalog.Components {
		catalogCopy.Components[name] = component
	}
	component := catalogCopy.Components["image"]
	disk := *component.Disk
	disk.Encryption = encryption
	component.Disk = &disk
	catalogCopy.Components["image"] = component
	stack, err := catalog.ApplyStack(&catalogCopy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definition, environment, stack, vmTestGeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodeBuild && node.IRNode != "" {
			return plan, node
		}
	}
	t.Fatal("compiled command VM Plan has no build node")
	return nil, Node{}
}

func TestLocalVMUnsupportedFormsFailBeforeResolverAndWorkspace(t *testing.T) {
	for _, test := range []struct {
		name     string
		plan     func(*testing.T) (*Plan, Node)
		wantText string
	}{
		{name: "plain command", plan: func(t *testing.T) (*Plan, Node) {
			return compiledCommandVMNodeWithEncryption(t, "none")
		}, wantText: "held-output adapter"},
		{name: "encrypted command", plan: func(t *testing.T) (*Plan, Node) {
			return compiledCommandVMNodeWithEncryption(t, "luks2")
		}, wantText: "encryption secret channel"},
		{name: "encrypted Packer", plan: func(t *testing.T) (*Plan, Node) {
			return compiledPackerVMNodeWithEncryption(t, "luks2")
		}, wantText: "encryption secret channel"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			plan, _ := test.plan(t)
			resolver := &fakeVMDriverResolver{executor: new(fakeVMDriverExecutor)}
			options := withLocalDefaults(LocalRunOptions{Workspace: ".nkd/vm-unsupported"})
			options.vmResolver = resolver
			result, err := runLocal(t.Context(), plan, options, productionLocalRunDeps())
			if result != nil || factoryErrorCode(err) != e.CodeEngineCapability ||
				!strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("unsupported VM result/error = %#v/%v", result, err)
			}
			if resolver.calls != 0 {
				t.Fatalf("unsupported VM reached resolver: %#v", resolver)
			}
			if _, statErr := os.Stat(options.Workspace); !os.IsNotExist(statErr) {
				t.Fatalf("unsupported VM mutated workspace: %v", statErr)
			}
		})
	}
}

func TestAdmittedVMDispatchRunsExactPackerProducerInsideCallback(t *testing.T) {
	packerPlan, packerNode := compiledPackerVMNode(t)
	admission := new(fakeVMAdmission)
	executor := new(fakeVMDriverExecutor)
	artifact, description, err := dispatchAdmittedVMProducer(t.Context(), admission, packerPlan, packerNode, executor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if artifact == nil || description == nil {
		t.Fatalf("dispatch returned incomplete artifact/result: %#v/%#v", artifact, description)
	}
	if err := artifact.Close(); err != nil {
		t.Fatal(err)
	}
	if admission.calls != 1 || !admission.callback || admission.nodeID != packerNode.ID || executor.packerCalls != 1 {
		t.Fatalf("dispatch calls = admission %d/%t/%q, Packer %d",
			admission.calls, admission.callback, admission.nodeID, executor.packerCalls)
	}
	request, err := localVMRequestForPlan(packerPlan, packerNode.ID)
	if err != nil {
		t.Fatal(err)
	}
	if executor.subject != request.Subject || executor.subject == packerNode.ID {
		t.Fatalf("executor subject = %q, want exact Plan subject %q", executor.subject, request.Subject)
	}
}

func TestAdmittedVMDispatchRejectsPlanSubstitutionWithUnchangedIRNode(t *testing.T) {
	plan, node := compiledPackerVMNode(t)
	_, before, err := plan.producerInstruction(node)
	if err != nil {
		t.Fatal(err)
	}
	admission := &fakeVMAdmission{mutate: func(candidate *Plan) {
		candidate.GeneratorVersion = "changed-but-valid"
	}}
	executor := new(fakeVMDriverExecutor)
	artifact, description, err := dispatchAdmittedVMProducer(t.Context(), admission, plan, node, executor, nil)
	if artifact != nil || description != nil || factoryErrorCode(err) != e.CodePipelineDrift {
		t.Fatalf("substituted Plan artifact/result/error = %#v/%#v/%v", artifact, description, err)
	}
	_, after, instructionErr := plan.producerInstruction(node)
	if instructionErr != nil {
		t.Fatal(instructionErr)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("test mutation unexpectedly changed the selected IR node")
	}
	if executor.packerCalls != 0 {
		t.Fatal("Plan substitution reached the Packer executor")
	}
}

func TestAdmittedVMDispatchRejectsCommandBeforeAdmission(t *testing.T) {
	plan, node := compiledVMNode(t, execution.CapabilityPreferred)
	admission := new(fakeVMAdmission)
	executor := new(fakeVMDriverExecutor)
	artifact, description, err := dispatchAdmittedVMProducer(t.Context(), admission, plan, node, executor, nil)
	if artifact != nil || description != nil || factoryErrorCode(err) != e.CodeEngineCapability ||
		!strings.Contains(err.Error(), "held-output command adapter") {
		t.Fatalf("command dispatch artifact/result/error = %#v/%#v/%v", artifact, description, err)
	}
	if admission.calls != 0 || executor.packerCalls != 0 {
		t.Fatalf("unsupported command reached admission/executor: %#v/%#v", admission, executor)
	}
}

func TestAdmittedVMDispatchRejectsEncryptedCommandBeforeAdmission(t *testing.T) {
	plan, node := compiledCommandVMNodeWithEncryption(t, "luks2")
	admission := new(fakeVMAdmission)
	executor := new(fakeVMDriverExecutor)
	artifact, description, err := dispatchAdmittedVMProducer(t.Context(), admission, plan, node, executor, nil)
	if artifact != nil || description != nil || factoryErrorCode(err) != e.CodeEngineCapability ||
		!strings.Contains(err.Error(), "encryption secret channel") {
		t.Fatalf("encrypted command artifact/result/error = %#v/%#v/%v", artifact, description, err)
	}
	if admission.calls != 0 || executor.packerCalls != 0 {
		t.Fatalf("encrypted command reached admission/executor: %#v/%#v", admission, executor)
	}
}

func TestAdmittedVMDispatchCleansRejectedArtifactAndMaterialCapabilities(t *testing.T) {
	driverFailure := e.New(e.CodeVerifyFailed, "driver failed")
	for _, test := range []struct {
		name     string
		fixture  func(*testing.T) (*Plan, Node)
		executor *fakeVMDriverExecutor
		wantText string
		wantCode e.Code
	}{
		{
			name: "rejected artifact", fixture: compiledPackerVMNode,
			executor: &fakeVMDriverExecutor{err: driverFailure, artifactOnError: true},
			wantCode: e.CodeVerifyFailed,
		},
		{
			name: "material close failure", fixture: func(t *testing.T) (*Plan, Node) {
				fixture := newVMPackerMaterialFixture(t)
				return fixture.plan, fixture.node
			},
			executor: &fakeVMDriverExecutor{err: driverFailure, corruptMaterialClose: true},
			wantText: "closing VM Packer material capabilities", wantCode: e.CodeVerifyFailed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, node := test.fixture(t)
			artifact, result, err := dispatchAdmittedVMProducer(
				t.Context(), new(fakeVMAdmission), plan, node, test.executor, nil,
			)
			if artifact != nil || result != nil || factoryErrorCode(err) != test.wantCode ||
				!errors.Is(err, driverFailure) || (test.wantText != "" && !strings.Contains(err.Error(), test.wantText)) {
				t.Fatalf("rejected dispatch artifact/result/error = %#v/%#v/%v", artifact, result, err)
			}
		})
	}
}

func TestCloseRejectedVMArtifactJoinsCloseFailure(t *testing.T) {
	primary := e.New(e.CodeVerifyFailed, "driver rejected artifact")
	closeFailure := errors.New("artifact close failed")
	artifact := &fakeVMArtifactCloser{err: closeFailure}
	err := closeRejectedVMArtifact(primary, artifact)
	if !artifact.closed || factoryErrorCode(err) != e.CodeVerifyFailed ||
		!errors.Is(err, primary) || !errors.Is(err, closeFailure) ||
		!strings.Contains(err.Error(), "closing rejected VM driver artifact") {
		t.Fatalf("rejected artifact cleanup = closed:%t error:%v", artifact.closed, err)
	}
}

func TestAdmittedVMDispatchFailsBeforeDriverWithoutAdmission(t *testing.T) {
	plan, node := compiledPackerVMNode(t)
	admissionFailure := errors.New("admission denied")
	admission := &fakeVMAdmission{err: admissionFailure}
	executor := new(fakeVMDriverExecutor)
	artifact, description, err := dispatchAdmittedVMProducer(t.Context(), admission, plan, node, executor, nil)
	if artifact != nil || description != nil || !errors.Is(err, admissionFailure) {
		t.Fatalf("dispatch artifact/error = %#v/%v", artifact, err)
	}
	if admission.callback || executor.packerCalls != 0 {
		t.Fatalf("driver ran before admission: admission=%#v executor=%#v", admission, executor)
	}
}

func TestUnavailableVMDriverResolverFailsClosed(t *testing.T) {
	resolver := unavailableVMDriverResolver{}
	executor, err := resolver.Admit(t.Context(), nil, nil, vmDriverRunConfig{})
	if executor != nil || factoryErrorCode(err) != e.CodeEngineCapability ||
		!strings.Contains(err.Error(), "Plan-bound realized toolchain") {
		t.Fatalf("production resolver executor/error = %#v/%v", executor, err)
	}
}

func TestLocalDefaultsConnectDirectPackerResolver(t *testing.T) {
	options := withLocalDefaults(LocalRunOptions{})
	if _, ok := options.vmResolver.(localVMPackerResolver); !ok {
		t.Fatalf("default VM resolver = %T, want localVMPackerResolver", options.vmResolver)
	}
}

func TestLocalVMPackerResolverRejectsUnsupportedPlans(t *testing.T) {
	directPlan, _ := compiledPackerVMNode(t)
	commandPlan, _ := compiledCommandVMNodeWithEncryption(t, "none")
	encryptedPlan, _ := compiledPackerVMNodeWithEncryption(t, "luks2")
	ordinaryPlan := buildOnlyPlan(t, "ordinary")
	session, err := pipeline.NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	for _, test := range []struct {
		name    string
		ctx     context.Context
		plan    *Plan
		session *pipeline.NodeSession
		code    e.Code
	}{
		{name: "nil context", plan: directPlan, session: session, code: e.CodeUsage},
		{name: "nil plan", ctx: t.Context(), session: session, code: e.CodeEngineCapability},
		{name: "nil session", ctx: t.Context(), plan: directPlan, code: e.CodeEngineCapability},
		{name: "missing diagnostics", ctx: t.Context(), plan: directPlan, session: session, code: e.CodeEngineCapability},
		{name: "command producer", ctx: t.Context(), plan: commandPlan, session: session, code: e.CodeEngineCapability},
		{name: "encrypted producer", ctx: t.Context(), plan: encryptedPlan, session: session, code: e.CodeEngineCapability},
		{name: "no VM producer", ctx: t.Context(), plan: ordinaryPlan, session: session, code: e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor, err := (localVMPackerResolver{}).Admit(test.ctx, test.plan, test.session, vmDriverRunConfig{Runtime: "docker"})
			if executor != nil || factoryErrorCode(err) != test.code {
				t.Fatalf("resolver executor/error = %#v/%v, want nil/%s", executor, err, test.code)
			}
		})
	}
}

func TestLocalVMPackerExecutorRejectsUnadmittedRoutes(t *testing.T) {
	manifest, err := heldfile.FromBytesContext(t.Context(), []byte("{}\n"), heldfile.Options{Label: "manifest", MaxBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer manifest.Close()
	materials := &vmPackerMaterials{observedProducerMaterials: &observedProducerMaterials{manifest: manifest}}
	runtime := &VMRuntime{inner: &execution.LocalVMRuntime{}}
	for _, test := range []struct {
		name     string
		executor *localVMPackerExecutor
		subject  string
		code     e.Code
	}{
		{name: "nil executor", code: e.CodeEngineCapability},
		{name: "subject not admitted", executor: &localVMPackerExecutor{inner: &pipeline.VMImagePackerExecutor{}, subjects: map[string]struct{}{}}, subject: "plan/node", code: e.CodePipelineDrift},
		{name: "source route missing", executor: &localVMPackerExecutor{inner: &pipeline.VMImagePackerExecutor{}, subjects: map[string]struct{}{"plan/node": {}}, sourceRoots: map[string]string{}}, subject: "plan/node", code: e.CodePipelineDrift},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact, result, err := test.executor.RunPacker(t.Context(), runtime, test.subject, ir.Node{}, materials)
			if artifact != nil || result != nil || factoryErrorCode(err) != test.code {
				t.Fatalf("executor artifact/result/error = %#v/%#v/%v, want nil/nil/%s", artifact, result, err, test.code)
			}
		})
	}
}

func TestPlannedPackerSourceRootUsesExplicitSourceArtifact(t *testing.T) {
	plan, node := compiledPackerVMNode(t)
	build, instruction, err := plan.producerInstruction(node)
	if err != nil {
		t.Fatal(err)
	}
	_, artifacts := planIndex(plan)
	resultID := "result/" + node.BuildTarget + "/" + instruction.Component
	result := artifacts[resultID]
	result.Path = ".nkd/unrelated/layout/result.json"
	artifacts[resultID] = result
	root, err := plannedPackerSourceRoot(build, node, instruction, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.ToSlash(filepath.Join(".nkd", "factory", node.BuildTarget, "src"))
	if filepath.ToSlash(root) != want {
		t.Fatalf("source root = %q, want explicit Plan route %q", root, want)
	}
	for id, artifact := range artifacts {
		if !strings.HasPrefix(id, "source/"+node.BuildTarget+"/") {
			continue
		}
		artifact.Path = ".nkd/substituted-source"
		artifacts[id] = artifact
	}
	if _, err := plannedPackerSourceRoot(build, node, instruction, artifacts); factoryErrorCode(err) != e.CodePipelineDrift {
		t.Fatalf("substituted source route error = %v, want %s", err, e.CodePipelineDrift)
	}
}

func TestAdmittedVMDispatchRejectsIncompleteBoundary(t *testing.T) {
	plan, node := compiledPackerVMNode(t)
	for _, test := range []struct {
		name      string
		ctx       context.Context
		admission vmAdmission
		executor  vmDriverExecutor
		code      e.Code
		wantText  string
	}{
		{name: "nil context", admission: new(fakeVMAdmission), executor: new(fakeVMDriverExecutor), code: e.CodeUsage},
		{name: "nil admission", ctx: t.Context(), executor: new(fakeVMDriverExecutor), code: e.CodeEngineCapability},
		{name: "nil executor", ctx: t.Context(), admission: new(fakeVMAdmission), code: e.CodeEngineCapability},
		{name: "missing artifact", ctx: t.Context(), admission: new(fakeVMAdmission), executor: &fakeVMDriverExecutor{missing: true}, code: e.CodeVerifyFailed},
		{name: "missing result with present artifact", ctx: t.Context(), admission: new(fakeVMAdmission), executor: &fakeVMDriverExecutor{missingResult: true}, code: e.CodeVerifyFailed, wantText: "no descriptive result"},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := localDiskRunResult(t, plan)
			defer owner.Close()
			artifact, description, err := dispatchAdmittedVMProducer(test.ctx, test.admission, plan, node, test.executor, owner)
			if artifact != nil || description != nil || factoryErrorCode(err) != test.code ||
				(test.wantText != "" && !strings.Contains(err.Error(), test.wantText)) {
				t.Fatalf("dispatch artifact/error = %#v/%v, want %s", artifact, err, test.code)
			}
			if len(owner.materialConsumptions) != 0 {
				t.Fatal("rejected dispatch retained consumption observations")
			}
		})
	}
}

func TestAdmittedVMDispatchRejectsNilPlanBeforeAdmission(t *testing.T) {
	admission := new(fakeVMAdmission)
	artifact, description, err := dispatchAdmittedVMProducer(
		t.Context(), admission, nil, Node{}, new(fakeVMDriverExecutor), nil,
	)
	if artifact != nil || description != nil || factoryErrorCode(err) != e.CodeUsage {
		t.Fatalf("nil Plan artifact/result/error = %#v/%#v/%v", artifact, description, err)
	}
	if admission.calls != 0 {
		t.Fatalf("nil Plan reached admission: %#v", admission)
	}
}

func TestLocalRequirementGateAdmitsOnlyVMRequirements(t *testing.T) {
	vmPlan, _ := compiledVMNode(t, execution.CapabilityPreferred)
	if err := rejectUnsupportedLocalPlanRequirements(vmPlan, "local test"); err != nil {
		t.Fatalf("VM requirements rejected before dedicated admission: %v", err)
	}

	ordinary := buildOnlyPlan(t, "ordinary-requirements")
	requirements := testExecutionRequirements()
	var component, instructionID string
	for index := range ordinary.Builds[0].Graph.Nodes {
		node := &ordinary.Builds[0].Graph.Nodes[index]
		if node.Kind == ir.KindBuild {
			node.Requirements = execution.Clone(requirements)
			component = node.Component
			instructionID = node.ID
			break
		}
	}
	for index := range ordinary.Graph.Nodes {
		node := &ordinary.Graph.Nodes[index]
		if node.BuildTarget == ordinary.Builds[0].Name && node.IRNode == instructionID {
			node.Requirements = execution.Clone(requirements)
			break
		}
	}
	for index := range ordinary.Builds[0].ResolvedComponents {
		if ordinary.Builds[0].ResolvedComponents[index].Name == component {
			ordinary.Builds[0].ResolvedComponents[index].BuildRequirements = execution.Clone(requirements)
		}
	}
	if err := rejectUnsupportedLocalPlanRequirements(ordinary, "local test"); factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatalf("ordinary requirements error = %v, want %s", err, e.CodeEngineCapability)
	}
}

func TestStandaloneLocalAdmissionPreflightsVMWithoutWorkspace(t *testing.T) {
	t.Chdir(t.TempDir())
	plan, _ := compiledVMNode(t, execution.CapabilityPreferred)
	workspace := ".nkd/vm"
	if runtime.GOOS == "linux" {
		root := t.TempDir()
		runtimePath := filepath.Join(root, "runtime")
		if err := os.WriteFile(runtimePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := AdmitLocal(t.Context(), plan, LocalRunOptions{Workspace: workspace,
			Runtime: runtimePath, Go: runtimePath, RunnerSource: root})
		if err != nil {
			t.Fatalf("standalone VM preflight failed: %v", err)
		}
	} else if err := AdmitLocal(t.Context(), plan, LocalRunOptions{Workspace: workspace}); factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatalf("non-Linux VM preflight error = %v, want %s", err, e.CodeEngineCapability)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("standalone VM preflight touched workspace: %v", err)
	}
	if err := AdmitLocal(t.Context(), plan, LocalRunOptions{Workspace: workspace, DryRun: true}); err != nil {
		t.Fatalf("static VM dry-run admission failed: %v", err)
	}
}

func TestRunLocalUnavailableVMExecutorFailsBeforeWorkspaceOrWork(t *testing.T) {
	t.Chdir(t.TempDir())
	plan, _ := compiledPackerVMNode(t)
	workspace := ".nkd/vm-unavailable"
	diagnosticRoot, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, err := pipeline.NewPackerDiagnosticSink(t.Context(), diagnosticRoot)
	_ = diagnosticRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := diagnostics.Close(); err != nil {
			t.Errorf("closing Packer diagnostic sink: %v", err)
		}
	})
	result, err := RunLocal(t.Context(), plan, LocalRunOptions{
		Workspace: workspace, Diagnostics: diagnostics,
	})
	if result != nil || factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatalf("local VM result/error = %#v/%v", result, err)
	}
	if _, statErr := os.Stat(workspace); !os.IsNotExist(statErr) {
		t.Fatalf("unavailable VM executor touched workspace: %v", statErr)
	}
}

func TestRunLocalVMDryRunRemainsStaticAndSideEffectFree(t *testing.T) {
	t.Chdir(t.TempDir())
	plan, _ := compiledVMNode(t, execution.CapabilityPreferred)
	workspace := ".nkd/vm-dry-run"
	result, err := RunLocal(t.Context(), plan, LocalRunOptions{Workspace: workspace, DryRun: true})
	if err != nil || result == nil || len(result.Nodes) == 0 {
		t.Fatalf("VM dry-run result/error = %#v/%v", result, err)
	}
	if _, statErr := os.Stat(workspace); !os.IsNotExist(statErr) {
		t.Fatalf("VM dry-run touched workspace: %v", statErr)
	}
}

func TestLocalVMAdmissionPrecedesRuntimePreparationAndNodeWork(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	plan, vmNode := compiledPackerVMNode(t)
	options := withLocalDefaults(LocalRunOptions{Workspace: ".nkd/vm-order", Jobs: 1})
	var events []string
	resolver := &fakeVMDriverResolver{executor: new(fakeVMDriverExecutor)}
	resolver.onAdmit = func() {
		if _, err := os.Stat(options.Workspace); !os.IsNotExist(err) {
			t.Fatalf("resolver ran after workspace mutation: %v", err)
		}
		events = append(events, "driver-admission")
	}
	options.vmResolver = resolver
	deps := localRunDeps{
		makeDisk: admittedLocalDiskProducer,
		admitVMBuilds: func(_ context.Context, admitted *Plan, workspace string) (map[string]vmAdmission, error) {
			events = append(events, "vm-admission")
			if admitted.workspace != workspace || workspace != options.Workspace {
				t.Fatalf("admitted workspace = %q/%q", admitted.workspace, workspace)
			}
			return map[string]vmAdmission{vmNode.ID: new(fakeVMAdmission)}, nil
		},
		admitRunners: func(context.Context, *Plan, LocalRunOptions, []string) error {
			events = append(events, "runner-admission")
			return nil
		},
		prepareRunner: func(context.Context, *Plan, LocalRunOptions, []string) (map[string]string, error) {
			events = append(events, "runner-preparation")
			return map[string]string{}, nil
		},
		executeNode: func(context.Context, *Plan, Node, LocalRunOptions, map[string]string) error {
			events = append(events, "node-work")
			return nil
		},
	}
	result, err := runLocal(t.Context(), plan, options, deps)
	if result != nil || factoryErrorCode(err) != e.CodeVerifyFailed || !strings.Contains(err.Error(), "invalid held disk payload identity") {
		t.Fatalf("local VM result/error = %#v/%v", result, err)
	}
	wantPrefix := []string{"driver-admission", "vm-admission", "runner-admission", "runner-preparation", "node-work"}
	if len(events) < len(wantPrefix) {
		t.Fatalf("local VM events = %v", events)
	}
	for index, want := range wantPrefix {
		if events[index] != want {
			t.Fatalf("local VM event[%d] = %q, want %q; all events %v", index, events[index], want, events)
		}
	}
	if _, err := os.Stat(filepath.Join(options.Workspace, "run.json")); !os.IsNotExist(err) {
		t.Fatalf("failed local VM unexpectedly wrote run record: %v", err)
	}
	if resolver.calls != 1 || resolver.session == nil || resolver.plan == nil ||
		resolver.config.Runtime != options.Runtime || !resolver.executor.closed {
		t.Fatalf("VM resolver lifecycle = %#v", resolver)
	}
}

func TestLocalVMResolverAndScheduledNodesShareLockedWriters(t *testing.T) {
	t.Chdir(t.TempDir())
	plan, vmNode := compiledPackerVMNode(t)
	underlying := new(overlapDetectingWriter)
	options := withLocalDefaults(LocalRunOptions{
		Workspace: ".nkd/vm-writers", Jobs: 2, Stdout: underlying, Stderr: underlying,
	})
	resolver := &fakeVMDriverResolver{executor: new(fakeVMDriverExecutor)}
	options.vmResolver = resolver
	deps := localRunDeps{
		makeDisk: admittedLocalDiskProducer,
		admitVMBuilds: func(context.Context, *Plan, string) (map[string]vmAdmission, error) {
			return map[string]vmAdmission{vmNode.ID: new(fakeVMAdmission)}, nil
		},
		admitRunners: func(context.Context, *Plan, LocalRunOptions, []string) error { return nil },
		prepareRunner: func(context.Context, *Plan, LocalRunOptions, []string) (map[string]string, error) {
			return map[string]string{}, nil
		},
		executeNode: func(_ context.Context, _ *Plan, _ Node, scheduled LocalRunOptions, _ map[string]string) error {
			resolverWriter, resolverOK := resolver.config.Stdout.(lockedLocalWriter)
			scheduledWriter, scheduledOK := scheduled.Stdout.(lockedLocalWriter)
			if !resolverOK || !scheduledOK || resolverWriter.mu != scheduledWriter.mu {
				return errors.New("resolver and scheduler do not share one writer lock")
			}
			writers := []io.Writer{resolver.config.Stdout, resolver.config.Stderr, scheduled.Stdout, scheduled.Stderr}
			var writes sync.WaitGroup
			for index := range 32 {
				writer := writers[index%len(writers)]
				writes.Go(func() {
					for range 32 {
						_, _ = writer.Write([]byte("parallel VM output\n"))
					}
				})
			}
			writes.Wait()
			return nil
		},
	}
	result, err := runLocal(t.Context(), plan, options, deps)
	if result != nil || factoryErrorCode(err) != e.CodeVerifyFailed || !strings.Contains(err.Error(), "invalid held disk payload identity") {
		t.Fatalf("parallel writer result/error = %#v/%v", result, err)
	}
	if underlying.overlap.Load() {
		t.Fatal("resolver and scheduled output overlapped beneath the shared writer lock")
	}
}

func TestLocalVMResolverFailureStopsBeforeWorkspaceAndClosesSession(t *testing.T) {
	t.Chdir(t.TempDir())
	plan, _ := compiledPackerVMNode(t)
	denied := errors.New("driver closure denied")
	resolver := &fakeVMDriverResolver{err: denied}
	options := withLocalDefaults(LocalRunOptions{Workspace: ".nkd/vm-resolver-denied"})
	options.vmResolver = resolver
	result, err := runLocal(t.Context(), plan, options, productionLocalRunDeps())
	if result != nil || !errors.Is(err, denied) {
		t.Fatalf("resolver failure result/error = %#v/%v", result, err)
	}
	if resolver.calls != 1 || resolver.session == nil {
		t.Fatalf("resolver admission = %#v", resolver)
	}
	if _, statErr := os.Stat(options.Workspace); !os.IsNotExist(statErr) {
		t.Fatalf("resolver failure mutated workspace: %v", statErr)
	}
	_, sessionErr := resolver.session.ExecNode(ir.Node{}, pipeline.RunOptions{
		Ctx: t.Context(), Launch: true, SourceRoot: t.TempDir(),
	})
	if factoryErrorCode(sessionErr) != e.CodeEngineCapability || !strings.Contains(sessionErr.Error(), "closed") {
		t.Fatalf("resolver failure left NodeSession open: %v", sessionErr)
	}
}

func TestLocalVMInvalidWorkspaceFailsBeforeResolver(t *testing.T) {
	plan, _ := compiledPackerVMNode(t)
	resolver := &fakeVMDriverResolver{executor: new(fakeVMDriverExecutor)}
	options := withLocalDefaults(LocalRunOptions{Workspace: "../unsafe"})
	options.vmResolver = resolver
	result, err := runLocal(t.Context(), plan, options, productionLocalRunDeps())
	if result != nil || factoryErrorCode(err) != e.CodeUsage {
		t.Fatalf("unsafe workspace result/error = %#v/%v", result, err)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver ran for unsafe workspace: %#v", resolver)
	}
}

func TestLocalVMIncompleteAdmissionMapFailsClosed(t *testing.T) {
	t.Chdir(t.TempDir())
	plan, _ := compiledPackerVMNode(t)
	options := withLocalDefaults(LocalRunOptions{Workspace: ".nkd/vm-incomplete-admission", Jobs: 1})
	options.vmResolver = &fakeVMDriverResolver{executor: new(fakeVMDriverExecutor)}
	deps := localRunDeps{
		makeDisk: admittedLocalDiskProducer,
		admitVMBuilds: func(context.Context, *Plan, string) (map[string]vmAdmission, error) {
			return map[string]vmAdmission{}, nil
		},
		admitRunners: func(context.Context, *Plan, LocalRunOptions, []string) error { return nil },
		prepareRunner: func(context.Context, *Plan, LocalRunOptions, []string) (map[string]string, error) {
			return map[string]string{}, nil
		},
		executeNode: func(context.Context, *Plan, Node, LocalRunOptions, map[string]string) error { return nil },
	}
	result, err := runLocal(t.Context(), plan, options, deps)
	if result != nil || factoryErrorCode(err) != e.CodeEngineCapability ||
		!strings.Contains(err.Error(), "no pre-execution runtime admission") {
		t.Fatalf("incomplete admission result/error = %#v/%v", result, err)
	}
}

func TestLocalVMAdmissionFailureStopsBeforeRuntimeAndNodeWork(t *testing.T) {
	t.Chdir(t.TempDir())
	plan, _ := compiledPackerVMNode(t)
	options := withLocalDefaults(LocalRunOptions{Workspace: ".nkd/vm-denied"})
	options.vmResolver = &fakeVMDriverResolver{executor: new(fakeVMDriverExecutor)}
	denied := errors.New("VM admission denied")
	runtimeTouched := false
	deps := localRunDeps{
		makeDisk: admittedLocalDiskProducer,
		admitVMBuilds: func(context.Context, *Plan, string) (map[string]vmAdmission, error) {
			return nil, denied
		},
		admitRunners: func(context.Context, *Plan, LocalRunOptions, []string) error {
			runtimeTouched = true
			return nil
		},
		prepareRunner: func(context.Context, *Plan, LocalRunOptions, []string) (map[string]string, error) {
			runtimeTouched = true
			return nil, nil
		},
		executeNode: func(context.Context, *Plan, Node, LocalRunOptions, map[string]string) error {
			runtimeTouched = true
			return nil
		},
	}
	result, err := runLocal(t.Context(), plan, options, deps)
	if result != nil || !errors.Is(err, denied) {
		t.Fatalf("local VM result/error = %#v/%v", result, err)
	}
	if runtimeTouched {
		t.Fatal("runtime or node work started after VM admission failed")
	}
}

func TestLocalVMCleanupFailureIsReturned(t *testing.T) {
	for _, test := range []struct {
		name           string
		admissionClose error
		executorClose  error
	}{
		{name: "runtime admission", admissionClose: errors.New("admission close failed")},
		{name: "driver executor", executorClose: errors.New("executor close failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			plan, vmNode := compiledPackerVMNode(t)
			options := withLocalDefaults(LocalRunOptions{Workspace: ".nkd/vm-close", Jobs: 1})
			executor := &fakeVMDriverExecutor{closeErr: test.executorClose}
			resolver := &fakeVMDriverResolver{executor: executor}
			options.vmResolver = resolver
			runnerFailure := errors.New("runner admission failed")
			admission := &fakeVMAdmission{closeErr: test.admissionClose}
			wantCloseFailure := test.admissionClose
			if wantCloseFailure == nil {
				wantCloseFailure = test.executorClose
			}
			deps := localRunDeps{
				makeDisk: admittedLocalDiskProducer,
				admitVMBuilds: func(context.Context, *Plan, string) (map[string]vmAdmission, error) {
					return map[string]vmAdmission{vmNode.ID: admission}, nil
				},
				admitRunners: func(context.Context, *Plan, LocalRunOptions, []string) error { return runnerFailure },
				prepareRunner: func(context.Context, *Plan, LocalRunOptions, []string) (map[string]string, error) {
					return map[string]string{}, nil
				},
				executeNode: func(context.Context, *Plan, Node, LocalRunOptions, map[string]string) error { return nil },
			}
			result, err := runLocal(t.Context(), plan, options, deps)
			if result != nil || factoryErrorCode(err) != e.CodeInternal ||
				!errors.Is(err, runnerFailure) || !errors.Is(err, wantCloseFailure) {
				t.Fatalf("cleanup result/error = %#v/%v", result, err)
			}
			if !admission.closed || !executor.closed {
				t.Fatalf("cleanup state = admission:%t executor:%t", admission.closed, executor.closed)
			}
		})
	}
}
