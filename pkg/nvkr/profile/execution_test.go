// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package profile

import (
	"context"
	"strings"
	"sync"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func exactDigest(ch byte) string {
	return "sha256:" + strings.Repeat(string(ch), 64)
}

func executionTestProfile() BuilderProfile {
	return BuilderProfile{
		SchemaVersion: SchemaVersion, Identity: "execution-test", Version: "1",
		HostControlClass: OperatorControlled,
		Provides:         []string{ir.BuilderOCIHermetic, ir.BuilderNix},
		Fields: []Field{
			{Name: FieldTargetArch, Value: "amd64", HardFail: true, MeasurementStage: MeasurementHost},
			{Name: FieldRunnerDigest, Value: exactDigest('1'), HardFail: true, MeasurementStage: MeasurementExecution},
			{Name: FieldBootstrapToolchainDigest, Value: exactDigest('2'), HardFail: true, MeasurementStage: MeasurementExecution},
			{Name: FieldRuntimeIdentity, Value: exactDigest('3'), HardFail: true, MeasurementStage: MeasurementExecution},
			{Name: FieldOCISandboxMode, Value: "rootless+network=none", HardFail: true, MeasurementStage: MeasurementExecution},
			{Name: FieldNixSandboxMode, Value: "nix-sandbox+network=none", HardFail: true, MeasurementStage: MeasurementExecution},
			{Name: FieldNixVersion, Value: "2.24.9", HardFail: true, MeasurementStage: MeasurementExecution},
			{Name: FieldNixExecutableDigest, Value: exactDigest('4'), HardFail: true, MeasurementStage: MeasurementExecution},
		},
	}
}

func executionTestNodes() []ir.Node {
	return []ir.Node{
		{
			ID: "build-oci", Kind: ir.KindBuild, Component: "kubelet",
			BuilderType: ir.BuilderOCIHermetic, Toolchain: "registry.example/oci@" + exactDigest('5'),
			Network: ir.NetworkNone, SourceReadOnly: true, WriteDir: "out",
		},
		{
			ID: "build-nix", Kind: ir.KindBuild, Component: "containerd",
			BuilderType: ir.BuilderNix, Toolchain: "registry.example/nix@" + exactDigest('6'),
			FlakeLockDigest: exactDigest('7'), Network: ir.NetworkNone, SourceReadOnly: true, WriteDir: "out",
		},
	}
}

func executionTestObservation() ExecutionObservation {
	return ExecutionObservation{
		RunnerDigest: exactDigest('1'), RunnerOrigin: RunnerBootstrapped,
		BootstrapToolchainDigest: exactDigest('2'),
		RuntimeIdentity:          exactDigest('3'), NixVersion: "2.24.9", NixExecutableDigest: exactDigest('4'),
		Nodes: []NodeExecutionObservation{
			{NodeID: "build-nix", EngineClass: ir.EngineNix, ToolchainDigest: exactDigest('6'), SandboxMode: "nix-sandbox+network=none", NixClosureDigest: exactDigest('7')},
			{NodeID: "build-oci", EngineClass: ir.EngineOCIHermetic, ToolchainDigest: exactDigest('5'), SandboxMode: "rootless+network=none"},
		},
	}
}

func TestVerifyExecutionDoesNotBindAnUnusedBootstrapToolchain(t *testing.T) {
	p := executionTestProfile()
	nodes := executionTestNodes()
	host := hostReceiptFor(t, p)
	observation := executionTestObservation()
	observation.RunnerOrigin = RunnerPreexisting
	observation.BootstrapToolchainDigest = ""

	receipt, err := VerifyExecution(context.Background(), host, p, nodes, observation)
	if err != nil {
		t.Fatalf("preexisting byte-hashed runner must not require an unused bootstrap: %v", err)
	}
	if err := receipt.RequireFor(context.Background(), p, nodes); err != nil {
		t.Fatal(err)
	}

	withoutBootstrapPin := p
	withoutBootstrapPin.Fields = append([]Field(nil), p.Fields...)
	for i := range withoutBootstrapPin.Fields {
		if withoutBootstrapPin.Fields[i].Name == FieldBootstrapToolchainDigest {
			withoutBootstrapPin.Fields = append(withoutBootstrapPin.Fields[:i], withoutBootstrapPin.Fields[i+1:]...)
			break
		}
	}
	host = hostReceiptFor(t, withoutBootstrapPin)
	if _, err := VerifyExecution(context.Background(), host, withoutBootstrapPin, nodes, observation); err != nil {
		t.Fatalf("profile without an unused bootstrap pin must qualify a preexisting runner: %v", err)
	}

	observation.BootstrapToolchainDigest = exactDigest('2')
	if _, err := VerifyExecution(context.Background(), hostReceiptFor(t, p), p, nodes, observation); code(err) != nvkrerr.CodePreflightFailed {
		t.Fatalf("preexisting runner accepted a contradictory bootstrap claim: %v", err)
	}
}

func hostReceiptFor(t *testing.T, p BuilderProfile) HostPreflightReceipt {
	t.Helper()
	receipt, divs, err := p.VerifyHost(context.Background(), map[string]string{FieldTargetArch: "amd64"})
	if err != nil || len(divs) != 0 {
		t.Fatalf("host preflight failed: receipt=%#v divs=%v err=%v", receipt, divs, err)
	}
	return receipt
}

func copyObservation(in ExecutionObservation) ExecutionObservation {
	out := in
	out.Nodes = append([]NodeExecutionObservation(nil), in.Nodes...)
	return out
}

func TestHostPreflightDoesNotConsumeExecutionIdentities(t *testing.T) {
	p := executionTestProfile()

	minimal := map[string]string{FieldTargetArch: "amd64"}
	forged := map[string]string{
		FieldTargetArch: "amd64", FieldRunnerDigest: exactDigest('f'),
		FieldBootstrapToolchainDigest: exactDigest('f'),
		FieldRuntimeIdentity:          exactDigest('f'),
		FieldNixVersion:               "attacker-controlled", FieldNixExecutableDigest: exactDigest('f'),
		FieldOCISandboxMode: "disabled", FieldNixSandboxMode: "disabled", FieldSandboxMode: "disabled",
	}
	minimalReceipt, minimalDivs, err := p.VerifyHost(context.Background(), minimal)
	if err != nil || len(minimalDivs) != 0 {
		t.Fatalf("minimal host facts must pass independently of execution identities: %v %v", err, minimalDivs)
	}
	forgedReceipt, forgedDivs, err := p.VerifyHost(context.Background(), forged)
	if err != nil || len(forgedDivs) != 0 {
		t.Fatalf("execution strings must not be interpreted as host facts: %v %v", err, forgedDivs)
	}
	if minimalReceipt.observationBinding != forgedReceipt.observationBinding {
		t.Fatal("host receipt bound caller-supplied execution identity strings")
	}
}

func TestVerifyExecutionBindsExactProfileNodesAndObservations(t *testing.T) {
	p := executionTestProfile()
	nodes := executionTestNodes()
	host := hostReceiptFor(t, p)
	if err := host.RequireFor(context.Background(), p); err != nil {
		t.Fatalf("valid host receipt did not authorize its exact profile: %v", err)
	}
	if code((HostPreflightReceipt{}).RequireFor(context.Background(), p)) != nvkrerr.CodePreflightFailed {
		t.Fatal("zero-value host receipt must fail closed")
	}
	changedHostProfile := p
	changedHostProfile.Version = "changed-after-host-preflight"
	if code(host.RequireFor(context.Background(), changedHostProfile)) != nvkrerr.CodePreflightFailed {
		t.Fatal("host receipt authorized a changed profile")
	}
	receipt, err := VerifyExecution(context.Background(), host, p, nodes, executionTestObservation())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ProfileDigest() != host.ProfileDigest() || receipt.hostObservationBinding != host.observationBinding {
		t.Fatal("execution receipt did not consume and bind the host stage")
	}
	if receipt.selectionBinding == "" || receipt.ExecutionBinding() == "" {
		t.Fatal("execution receipt lacks exact bindings")
	}
	if err := receipt.RequireFor(context.Background(), p, nodes); err != nil {
		t.Fatalf("valid receipt did not authorize its exact selection: %v", err)
	}
	identities := receipt.NodeIdentities()
	if len(identities) != 2 || identities[0].NodeID != "build-nix" || identities[1].NodeID != "build-oci" {
		t.Fatalf("verified identities are incomplete or nondeterministic: %#v", identities)
	}
	identities[0].EngineClass = "forged"
	if receipt.NodeIdentities()[0].EngineClass != ir.EngineNix {
		t.Fatal("receipt leaked mutable node identity state")
	}

	mutatedNodes := executionTestNodes()
	mutatedNodes[0].Command = []string{"different-artifact-work"}
	if code(receipt.RequireFor(context.Background(), p, mutatedNodes)) != nvkrerr.CodePreflightFailed {
		t.Fatal("receipt authorized a different selected build-node definition")
	}
	mutatedProfile := p
	mutatedProfile.Version = "2"
	if code(receipt.RequireFor(context.Background(), mutatedProfile, nodes)) != nvkrerr.CodePreflightFailed {
		t.Fatal("receipt authorized a different builder profile")
	}
	if code((ExecutionReceipt{}).RequireFor(context.Background(), p, nodes)) != nvkrerr.CodePreflightFailed {
		t.Fatal("zero-value execution receipt must fail closed")
	}
}

func TestVerifyExecutionRejectsIncompleteOrMismatchedIdentity(t *testing.T) {
	p := executionTestProfile()
	nodes := executionTestNodes()
	host := hostReceiptFor(t, p)
	base := executionTestObservation()

	tests := map[string]func(*ExecutionObservation){
		"runner mismatch":        func(o *ExecutionObservation) { o.RunnerDigest = exactDigest('a') },
		"runner malformed":       func(o *ExecutionObservation) { o.RunnerDigest = "sha256:caller-assertion" },
		"bootstrap missing":      func(o *ExecutionObservation) { o.BootstrapToolchainDigest = "" },
		"runtime mismatch":       func(o *ExecutionObservation) { o.RuntimeIdentity = exactDigest('a') },
		"nix version mismatch":   func(o *ExecutionObservation) { o.NixVersion = "2.24.8" },
		"nix executable missing": func(o *ExecutionObservation) { o.NixExecutableDigest = "" },
		"engine mismatch":        func(o *ExecutionObservation) { o.Nodes[0].EngineClass = ir.EngineOCIHermetic },
		"toolchain mismatch":     func(o *ExecutionObservation) { o.Nodes[1].ToolchainDigest = exactDigest('a') },
		"sandbox mismatch":       func(o *ExecutionObservation) { o.Nodes[1].SandboxMode = "network=default" },
		"nix closure mismatch":   func(o *ExecutionObservation) { o.Nodes[0].NixClosureDigest = exactDigest('a') },
		"selected node missing":  func(o *ExecutionObservation) { o.Nodes = o.Nodes[:1] },
		"duplicate observation":  func(o *ExecutionObservation) { o.Nodes = append(o.Nodes, o.Nodes[0]) },
		"unselected observation": func(o *ExecutionObservation) {
			o.Nodes = append(o.Nodes, NodeExecutionObservation{NodeID: "build-attacker", EngineClass: ir.EngineNix, ToolchainDigest: exactDigest('6'), SandboxMode: "nix-sandbox+network=none", NixClosureDigest: exactDigest('7')})
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			observation := copyObservation(base)
			mutate(&observation)
			receipt, err := VerifyExecution(context.Background(), host, p, nodes, observation)
			if code(err) != nvkrerr.CodePreflightFailed {
				t.Fatalf("identity defect must fail execution preflight, got receipt=%#v err=%v", receipt, err)
			}
			if err := receipt.RequireFor(context.Background(), p, nodes); code(err) != nvkrerr.CodePreflightFailed {
				t.Fatalf("failed verification returned an authorizing receipt: %v", err)
			}
		})
	}
}

func TestVerifyExecutionRejectsMissingHostStageAndProfileOwnedPins(t *testing.T) {
	p := executionTestProfile()
	nodes := executionTestNodes()
	observation := executionTestObservation()
	if _, err := VerifyExecution(context.Background(), HostPreflightReceipt{}, p, nodes, observation); code(err) != nvkrerr.CodePreflightFailed {
		t.Fatalf("execution preflight without host receipt must fail closed: %v", err)
	}

	host := hostReceiptFor(t, p)
	for _, field := range []string{FieldRunnerDigest, FieldBootstrapToolchainDigest, FieldRuntimeIdentity, FieldNixVersion, FieldNixExecutableDigest, FieldOCISandboxMode, FieldNixSandboxMode} {
		t.Run(field, func(t *testing.T) {
			missing := p
			missing.Fields = append([]Field(nil), p.Fields...)
			for i := range missing.Fields {
				if missing.Fields[i].Name == field {
					missing.Fields = append(missing.Fields[:i], missing.Fields[i+1:]...)
					break
				}
			}
			missingHost := hostReceiptFor(t, missing)
			if _, err := VerifyExecution(context.Background(), missingHost, missing, nodes, observation); code(err) != nvkrerr.CodePreflightFailed {
				t.Fatalf("missing profile-owned pin %q did not fail closed: %v", field, err)
			}
		})
	}
	other := p
	other.Identity = "other-profile"
	if _, err := VerifyExecution(context.Background(), host, other, nodes, observation); code(err) != nvkrerr.CodePreflightFailed {
		t.Fatalf("host receipt was reusable with a different profile: %v", err)
	}
}

func TestValidateExecutionPlanRejectsMissingPinsWithoutIssuingAuthority(t *testing.T) {
	p := executionTestProfile()
	nodes := executionTestNodes()
	if err := ValidateExecutionPlan(context.Background(), p, nodes); err != nil {
		t.Fatalf("complete execution plan was rejected: %v", err)
	}
	for i := range p.Fields {
		if p.Fields[i].Name == FieldRuntimeIdentity {
			p.Fields = append(p.Fields[:i], p.Fields[i+1:]...)
			break
		}
	}
	if code(ValidateExecutionPlan(context.Background(), p, nodes)) != nvkrerr.CodePreflightFailed {
		t.Fatal("execution plan without a runtime identity was accepted")
	}
	p = executionTestProfile()
	nodes = executionTestNodes()
	nodes[0].WriteDir = ""
	if code(ValidateExecutionPlan(context.Background(), p, nodes)) != nvkrerr.CodePreflightFailed {
		t.Fatal("execution plan without a private output directory was accepted")
	}
}

func TestQualifiedExecutionRefusesDarwinPayloadBeforeReceipt(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "darwin/arm64"} {
		t.Run(platform, func(t *testing.T) {
			p, nodes := executionTestProfile(), executionTestNodes()
			nodes[0].Platform = platform
			host := hostReceiptFor(t, p)
			declarationErr := ValidateExecutionPlan(t.Context(), p, nodes)
			receipt, err := VerifyExecution(t.Context(), host, p, nodes, executionTestObservation())
			if platform == "darwin/arm64" {
				if code(declarationErr) != nvkrerr.CodeEngineCapability || code(err) != nvkrerr.CodeEngineCapability {
					t.Fatalf("Darwin qualified admission: declaration=%v receipt=%v", declarationErr, err)
				}
				if receipt.RequireFor(t.Context(), p, nodes) == nil {
					t.Fatal("rejected payload received usable authority")
				}
			} else if declarationErr != nil || err != nil || receipt.RequireFor(t.Context(), p, nodes) != nil {
				t.Fatalf("Linux qualified control rejected: declaration=%v receipt=%v", declarationErr, err)
			}
		})
	}
}

func TestExecutionFieldClassificationAndRepinAreFailClosed(t *testing.T) {
	p := executionTestProfile()
	for i := range p.Fields {
		if p.Fields[i].Name == FieldRunnerDigest {
			p.Fields[i].MeasurementStage = MeasurementHost
		}
	}
	if code(p.Validate()) != nvkrerr.CodeInvalidCatalog {
		t.Fatal("known execution identity was accepted as a host fact")
	}

	p = executionTestProfile()
	p.Fields = append(p.Fields, Field{Name: "builder.futureIdentity", Value: "x", HardFail: true, MeasurementStage: MeasurementExecution})
	if code(p.Validate()) != nvkrerr.CodeInvalidCatalog {
		t.Fatal("unimplemented execution identity validator was accepted")
	}

	p = Embedded("amd64")
	p.Repin(FieldRuntimeIdentity, exactDigest('8'), false)
	for _, field := range p.Fields {
		if field.Name == FieldRuntimeIdentity && (!field.HardFail || field.MeasurementStage != MeasurementExecution) {
			t.Fatal("Repin permitted an execution identity to become advisory or host-measured")
		}
	}
}

func TestExecutionPreflightContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := executionTestProfile()
	if _, _, err := p.VerifyHost(ctx, map[string]string{FieldTargetArch: "amd64"}); code(err) != nvkrerr.CodeTimeout {
		t.Fatalf("canceled host stage must return timeout: %v", err)
	}
	host := hostReceiptFor(t, p)
	if _, err := VerifyExecution(ctx, host, p, executionTestNodes(), executionTestObservation()); code(err) != nvkrerr.CodeTimeout {
		t.Fatalf("canceled execution stage must return timeout: %v", err)
	}
}

func TestExecutionPreflightConcurrentUse(t *testing.T) {
	p := executionTestProfile()
	nodes := executionTestNodes()
	observation := executionTestObservation()
	const workers = 24
	const iterations = 25
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				host, _, err := p.VerifyHost(context.Background(), map[string]string{FieldTargetArch: "amd64"})
				if err != nil {
					errCh <- err
					return
				}
				receipt, err := VerifyExecution(context.Background(), host, p, nodes, observation)
				if err != nil {
					errCh <- err
					return
				}
				if err := receipt.RequireFor(context.Background(), p, nodes); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent execution preflight failed: %v", err)
	}
}
