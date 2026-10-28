// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestNativePackerDataUsesCapturedBytesAndStableLogicalNames(t *testing.T) {
	program, digest := writeFakePacker(t)
	root := t.TempDir()
	const relative = "nvkr/toolchain/firmware/code.fd"
	source := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte("pinned firmware\n")
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	toolchain, err := OpenPackerToolchain(t.Context(), program, digest, nil, root,
		[]PackerPluginFile{{Path: relative, SHA256: FileDigest(data)}})
	if err != nil {
		t.Fatal(err)
	}
	defer toolchain.Close()
	if err := os.WriteFile(source, []byte("changed source"), 0o600); err != nil {
		t.Fatal(err)
	}
	variables := []PackerVariable{{Name: "firmware", Value: "/" + relative}, {Name: "variant", Value: "plain"}}
	resolved, err := toolchain.NativeVariables(t.Context(), variables)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := os.ReadFile(resolved[0].Value)
	if err != nil || string(observed) != string(data) || resolved[0].Value == source || variables[0].Value != "/"+relative || resolved[1] != variables[1] {
		t.Fatalf("native file binding changed identity or bytes: %+v, %q, %v", resolved, observed, err)
	}
	for _, row := range []struct {
		value string
		want  e.Code
	}{
		{NativePackerFilePrefix + "missing", e.CodeUnverifiedRef},
		{NativePackerFilePrefix + "../escape", e.CodeInvalidCatalog},
		{NativePackerFilePrefix + "firmware//code.fd", e.CodeInvalidCatalog},
		{NativePackerFilePrefix, e.CodeInvalidCatalog},
	} {
		if _, err := toolchain.NativeVariables(t.Context(), []PackerVariable{{Name: "file", Value: row.value}}); code(err) != row.want {
			t.Fatalf("file %q admitted: %v", row.value, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := toolchain.NativeVariables(ctx, variables); code(err) != e.CodeTimeout {
		t.Fatalf("canceled binding admitted: %v", err)
	}
	if err := toolchain.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(resolved[0].Value); !os.IsNotExist(err) {
		t.Fatal("native data remains after close", err)
	}
	if _, err := toolchain.NativeVariables(t.Context(), variables); code(err) != e.CodeEngineCapability {
		t.Fatalf("closed toolchain resolved data: %v", err)
	}
	if strings.Contains(variables[0].Value, root) {
		t.Fatal("physical source path leaked into the logical variable")
	}
}

func TestNativePackerNodeSharesDriverAndCleansWorkspace(t *testing.T) {
	toolchain, _ := testPackerToolchain(t)
	project := t.TempDir()
	request := testPackerRequest(t, project)
	workspace := t.TempDir()
	directory, err := os.Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	node := ir.Node{Platform: "linux/arm64", ArtifactPath: request.ArtifactPath,
		ExecutionBinding: &execution.Binding{Capabilities: []execution.CapabilityResolution{{Name: execution.CapabilityKVM, Enabled: false}}},
		Packer: &ir.PackerConfig{ProjectPath: ".", BuildName: request.BuildName, PackerVersion: "1.15.4",
			ArchitectureVariable: request.ArchitectureVariable, AcceleratorVariable: request.AcceleratorVariable,
			MaterialManifestVariable: request.MaterialManifestVariable, OutputDirectoryVariable: request.OutputDirectoryVariable},
	}
	for _, variable := range request.Variables {
		node.Packer.Variables = append(node.Packer.Variables, ir.PackerVariable(variable))
	}
	manifest, err := heldfile.OpenContext(t.Context(), request.MaterialManifestPath, heldfile.Options{Label: "native-node-manifest", ExpectedDigest: request.MaterialManifestSHA256, MaxBytes: maxBuildMaterialManifestBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer manifest.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	artifact, err := runNativePackerNode(ctx, toolchain, testMaterializedPackerSource(project), node,
		VMImagePackerRun{Manifest: manifest, Materials: request.Materials, Workspace: workspace}, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Close()
	if artifact.Digest() != FileDigest([]byte("packer-artifact\n")) {
		t.Fatal("wrong native driver output")
	}
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("native workspace remains: %v %v", entries, err)
	}
	if len(readPackerCalls(t, project)) != 2 {
		t.Fatal("native node did not share validate/build sequencing")
	}
	wrong := t.TempDir()
	_, err = runNativePackerNode(ctx, toolchain, testMaterializedPackerSource(project), node,
		VMImagePackerRun{Manifest: manifest, Materials: request.Materials, Workspace: wrong}, directory)
	if code(err) != e.CodeEngineCapability {
		t.Fatalf("wrong workspace projection: %v", err)
	}
}

func TestNativePackerSSHUsesPinnedSystemTool(t *testing.T) {
	requireNativePackerExecution(t)
	path, err := filepath.EvalSymlinks("/usr/bin/ssh-keygen")
	if err != nil {
		t.Skipf("ssh-keygen fixture unavailable: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	program, digest := writeFakePacker(t)
	toolchain, err := OpenPackerToolchain(t.Context(), program, digest,
		[]PackerHostTool{{Name: "ssh-keygen", Path: path, SHA256: FileDigest(raw)}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer toolchain.Close()
	project := t.TempDir()
	request := testPackerRequest(t, project)
	request.PackerVersion = "1.15.4"
	request.SSHPrivateKeyVariable, request.SSHPublicKeyVariable = "ssh_private_key_file", "ssh_public_key_file"
	artifact, err := runPacker(t.Context(), toolchain, testMaterializedPackerSource(project), request)
	if err != nil {
		t.Fatalf("%s native SSH: %v", runtime.GOOS, err)
	}
	if err := artifact.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(request.OutputParent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private SSH/build data remain: %v %v", entries, err)
	}
}

func TestNativePackerNodeRetainsDeclaredAcquisitions(t *testing.T) {
	toolchain, _ := testPackerToolchain(t)
	for _, test := range []struct {
		name string
		want e.Code
	}{
		{name: "valid"},
		{name: "missing", want: e.CodeVerifyFailed},
		{name: "invalid", want: e.CodeVerifyFailed},
		{name: "symlink", want: e.CodeVerifyFailed},
		{name: "budget", want: e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, workspace := t.TempDir(), t.TempDir()
			request := testPackerRequest(t, project)
			directory, err := os.Open(workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			capture := testPackerAcquisitionBytes(t, "arm64")
			if test.name == "invalid" {
				capture = []byte("invalid capture")
			}
			if test.name != "missing" {
				if err := os.WriteFile(filepath.Join(project, "capture.tar"), capture, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.name == "symlink" {
				if err := os.WriteFile(filepath.Join(project, "symlink-acquisitions"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			budget := &execution.ResourceRequest{MemoryMiB: 128, EphemeralStorageMiB: 4}
			if test.name == "budget" {
				budget.MemoryMiB = 64
			}
			node := ir.Node{Platform: "linux/arm64", ArtifactPath: request.ArtifactPath,
				Requirements: &execution.Requirements{Resources: budget}, ExecutionBinding: &execution.Binding{},
				Packer: &ir.PackerConfig{ProjectPath: ".", BuildName: request.BuildName, PackerVersion: "1.15.4",
					ArchitectureVariable: request.ArchitectureVariable, AcceleratorVariable: request.AcceleratorVariable,
					MaterialManifestVariable: request.MaterialManifestVariable, OutputDirectoryVariable: request.OutputDirectoryVariable,
					AcquisitionArchive: "capture.tar"},
			}
			for _, variable := range request.Variables {
				node.Packer.Variables = append(node.Packer.Variables, ir.PackerVariable(variable))
			}
			manifest, err := heldfile.OpenContext(t.Context(), request.MaterialManifestPath, heldfile.Options{
				Label: "native-capture-manifest", ExpectedDigest: request.MaterialManifestSHA256, MaxBytes: maxBuildMaterialManifestBytes,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer manifest.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			artifact, err := runNativePackerNode(ctx, toolchain, testMaterializedPackerSource(project), node,
				VMImagePackerRun{Manifest: manifest, Materials: request.Materials, Workspace: workspace}, directory)
			if code(err) != test.want {
				if artifact != nil {
					_ = artifact.Close()
				}
				t.Fatalf("artifact=%v err=%v; want %s", artifact, err, test.want)
			}
			entries, err := os.ReadDir(workspace)
			if err != nil || len(entries) != 0 {
				t.Fatalf("native capture workspace remains: %v %v", entries, err)
			}
			if test.want != "" {
				if artifact != nil {
					t.Fatal("failed capture returned an artifact")
				}
				if test.name == "budget" {
					if _, err := os.Stat(filepath.Join(project, ".nvkr-test-calls")); !os.IsNotExist(err) {
						t.Fatal("unadmitted capture budget started Packer")
					}
				}
				return
			}
			defer artifact.Close()
			if _, err := artifact.ObserveAcquisitionsContext(t.Context()); err != nil {
				t.Fatalf("same-invocation capture was not retained after cleanup: %v", err)
			}
			if err := artifact.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := artifact.ObserveAcquisitionsContext(t.Context()); code(err) != e.CodeGateUnmet {
				t.Fatalf("closed capture remains usable: %v", err)
			}
		})
	}
}

// Exercise the public session boundary, host admission, prepared source receipt,
// held inputs and output observation together, without a container runtime.
// The controlled Packer executable is not a real image build or VM acceptance.
func TestNativePackerSessionRunsExactNodeWithoutContainerRuntime(t *testing.T) {
	toolchain, _ := testPackerToolchain(t)
	workspace := t.TempDir()
	directory, err := os.Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	root := t.TempDir()
	fetch := sessionSource(t, root)
	request := testPackerRequest(t, root)
	host := execution.HostPlatform(runtime.GOOS + "/" + runtime.GOARCH)
	capability := execution.CapabilityKVM
	if runtime.GOOS == "darwin" {
		capability = execution.CapabilityHVF
	}
	requirements := &execution.Requirements{
		Resources:            &execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 1, EphemeralStorageMiB: 1},
		AllowedHostPlatforms: []execution.HostPlatform{host},
		Capabilities:         []execution.CapabilityRequirement{{Name: capability, Policy: execution.CapabilityForbidden}},
		TimeoutSeconds:       20,
	}
	binding, err := execution.Match(requirements, []execution.Class{{
		Name: "native", RunsOn: "native", HostPlatform: host,
		Resources: execution.ResourceCapacity{CPUMillis: 1000, MemoryMiB: 1, EphemeralStorageMiB: 1}, MaxTimeoutSeconds: 20,
	}}, execution.MatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node := ir.Node{
		ID: "build-node", Kind: ir.KindBuild, Needs: []string{fetch.ID}, Component: "node",
		BuilderType: ir.BuilderVMImage, Toolchain: "sha256:" + repeatHex("c"), ToolchainPlatform: string(host),
		Platform: "linux/arm64", Network: ir.NetworkDefault, Workdir: "/src",
		SourceDest: fetch.Dest, SourceCommit: fetch.Commit, SourceTreeHash: fetch.TreeHash, VendorSpecDigest: fetch.VendorSpecDigest,
		OutputType: "disk-image", ArtifactPath: request.ArtifactPath, Version: "1.0.0",
		Disk: &ir.DiskSpec{Format: "qcow2", Encryption: "none"}, Requirements: requirements, ExecutionBinding: binding,
		Packer: &ir.PackerConfig{
			ProjectPath: ".", BuildName: request.BuildName, PackerVersion: "1.15.4",
			ArchitectureVariable: request.ArchitectureVariable, AcceleratorVariable: request.AcceleratorVariable,
			MaterialManifestVariable: request.MaterialManifestVariable, OutputDirectoryVariable: request.OutputDirectoryVariable,
		},
	}
	selection := VMImagePackerSelection{Subject: "plan/native-node", Node: node, Toolchain: toolchain}
	environment := definition.Environment{
		APIVersion: definition.EnvironmentAPIVersion, Kind: definition.EnvironmentKind, Metadata: definition.Metadata{Name: "native"},
		UtilityContainer: "example.test/utility@sha256:" + repeatHex("a"), UtilityPlatform: "linux/arm64",
		Runtime: definition.Runtime{BuildNetworkPolicy: definition.BuildNetworkProducerDeclared},
	}
	digest, err := environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := AuthorizeVMImagePackerNetwork(environment, digest, []VMImagePackerSelection{selection})
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.AdmitVMImagePacker(t.Context(), []VMImagePackerSelection{selection}, nil, "missing-runtime"); code(err) != e.CodeEngineCapability {
		t.Fatalf("native execution skipped network admission: %v", err)
	}
	executor, err := session.AdmitVMImagePacker(t.Context(), []VMImagePackerSelection{selection}, receipt, "missing-runtime")
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	changed := node
	changed.Version = "1.0.1"
	if _, _, err := executor.Run(t.Context(), selection.Subject, changed, VMImagePackerRun{}); code(err) != e.CodeEngineCapability {
		t.Fatalf("changed native node admitted: %v", err)
	}
	manifest, err := heldfile.OpenContext(t.Context(), request.MaterialManifestPath, heldfile.Options{
		Label: "native-session-manifest", ExpectedDigest: request.MaterialManifestSHA256, MaxBytes: maxBuildMaterialManifestBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manifest.Close()
	diagnostics, err := NewPackerDiagnosticSink(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer diagnostics.Close()
	vmRequest := execution.LocalVMRequest{Subject: selection.Subject, Requirements: requirements, Binding: binding}
	admission, err := execution.AdmitLocalVM(t.Context(), vmRequest, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	err = admission.Execute(t.Context(), vmRequest, func(ctx context.Context, vm *execution.LocalVMRuntime) error {
		artifact, result, err := executor.Run(ctx, selection.Subject, node, VMImagePackerRun{
			SourceRoot: root, Workspace: workspace, Manifest: manifest, Materials: request.Materials, Runtime: vm, Diagnostics: diagnostics,
		})
		if err != nil {
			return err
		}
		defer artifact.Close()
		if result.SHA256 != FileDigest([]byte("packer-artifact\n")) || result.Qualification != ArtifactUnqualified || result.Toolchain != node.Toolchain {
			t.Fatal("native result lost its selected identity or unqualified observation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executor.Run(t.Context(), selection.Subject, node, VMImagePackerRun{
		Manifest: manifest, Runtime: &execution.LocalVMRuntime{}, Diagnostics: diagnostics,
	}); code(err) != e.CodeEngineCapability {
		t.Fatalf("closed native executor reused: %v", err)
	}
}

func TestNativePackerSSHRejectsMissingDependency(t *testing.T) {
	toolchain, _ := testPackerToolchain(t)
	request := testPackerRequest(t, t.TempDir())
	request.SSHPrivateKeyVariable, request.SSHPublicKeyVariable = "ssh_private_key_file", "ssh_public_key_file"
	artifact, err := runPacker(t.Context(), toolchain, testMaterializedPackerSource(t.TempDir()), request)
	if artifact != nil || code(err) != e.CodeEngineCapability {
		t.Fatalf("missing SSH generator: artifact=%v error=%v", artifact, err)
	}
	entries, err := os.ReadDir(request.OutputParent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed SSH admission leaked private workspace: %v %v", entries, err)
	}
}
