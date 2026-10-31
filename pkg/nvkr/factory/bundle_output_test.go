// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func bundleTestPlan(t *testing.T, publish bool) *Plan {
	t.Helper()
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	component := base.Components["containerd"]
	component.OutputType = "tarball"
	component.ArtifactPath = "bin/bundle.tar.gz"
	component.BuildCommand = []string{"render", "--format", "argo"}
	base.Components["containerd"] = component
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := fixtureInputs("bundles", "rendered", "ubuntu-24.04")
	definition.Outputs[0].Format = authored.OutputBundle
	definition.Outputs[0].Components = []string{"containerd"}
	if publish {
		environment.Publication = &authored.Publication{RepositoryPrefix: "localhost:5000/nvkr", Anonymous: true, PlainHTTP: true}
	}
	plan, err := Compile(t.Context(), definition, environment, stack, "v0.0.0 (test)")
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = "examples/bundle/definition.yaml", "examples/bundle/environment.yaml"
	return plan
}

func bundleTestArchive(t *testing.T, content string) []byte {
	t.Helper()
	var result bytes.Buffer
	gz := gzip.NewWriter(&result)
	tarWriter := tar.NewWriter(gz)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "bundle/rendered.yaml", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func bundleTestWorkspace(t *testing.T, publish bool) (*Plan, map[string]Artifact, []byte) {
	t.Helper()
	plan := bundleTestPlan(t, publish)
	_, artifacts := planIndex(plan)
	workspace := t.TempDir()
	for id, artifact := range artifacts {
		artifact.Path = filepath.Join(workspace, artifact.Path)
		artifacts[id] = artifact
	}
	archive := bundleTestArchive(t, "apiVersion: argoproj.io/v1alpha1\nkind: Application\n")
	input := artifacts["artifact/rendered/containerd"]
	if err := os.MkdirAll(filepath.Dir(input.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input.Path, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	var instruction ir.Node
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.Kind == ir.KindBuild {
			instruction = node
		}
	}
	result := pipeline.ArtifactResult{Component: input.Component, OutputType: input.Kind, SHA256: pipeline.FileDigest(archive), Size: int64(len(archive)),
		Toolchain: instruction.Toolchain, BuilderType: instruction.BuilderType, Path: instruction.ArtifactPath}
	raw, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := artifacts["result/rendered/containerd"].Path
	if err := os.MkdirAll(filepath.Dir(resultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return plan, artifacts, archive
}

func TestBundleCompileAndGenerateExactDataflow(t *testing.T) {
	for _, publish := range []bool{false, true} {
		t.Run(map[bool]string{false: "build only", true: "publication"}[publish], func(t *testing.T) {
			plan := bundleTestPlan(t, publish)
			packageNode := planNode(plan, "package/rendered")
			for _, expected := range []string{"artifact/rendered/containerd", "result/rendered/containerd", "observation/rendered"} {
				if !containsString(packageNode.Inputs, expected) {
					t.Fatalf("package omits %s", expected)
				}
			}
			sbomNode := planNode(plan, "rendered/sbom")
			if !containsString(sbomNode.Inputs, "artifact/rendered/containerd") || plan.Definition.SBOMTool != "" {
				t.Fatal("bundle inventory requires exact bytes without a scanner")
			}
			first, err := Render(t.Context(), plan, EngineGitHub)
			if err != nil {
				t.Fatal(err)
			}
			second, err := Render(t.Context(), plan, EngineGitHub)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(first, second) {
				t.Fatal("bundle workflow generation is not deterministic")
			}
			if err := ValidateGitHubBundle(t.Context(), plan, first); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBundlePackageRequiresExactProducer(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, *Plan, map[string]Artifact)
		code   e.Code
	}{
		{name: "exact"},
		{name: "missing result", change: func(_ *testing.T, _ *Plan, a map[string]Artifact) { delete(a, "result/rendered/containerd") }, code: e.CodeReleaseIncomplete},
		{name: "sibling result", change: func(_ *testing.T, _ *Plan, a map[string]Artifact) {
			r := a["result/rendered/containerd"]
			r.BuildTarget = "sibling"
			a[r.ID] = r
		}, code: e.CodeReleaseIncomplete},
		{name: "changed bytes", change: func(t *testing.T, _ *Plan, a map[string]Artifact) {
			if err := os.WriteFile(a["artifact/rendered/containerd"].Path, bundleTestArchive(t, "changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeVerifyFailed},
		{name: "acquired producer", change: func(_ *testing.T, p *Plan, _ map[string]Artifact) {
			for i := range p.Graph.Nodes {
				if p.Graph.Nodes[i].Kind == NodeBuild {
					p.Graph.Nodes[i].Kind = NodeAcquire
				}
			}
		}, code: e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, artifacts, archive := bundleTestWorkspace(t, false)
			if test.change != nil {
				test.change(t, plan, artifacts)
			}
			err := executePackage(t.Context(), plan, planNode(plan, "package/rendered"), artifacts, io.Discard)
			if test.code != "" {
				assertFactoryErrorCode(t, err, test.code)
				if _, err := os.Lstat(artifacts["package/rendered"].Path); !os.IsNotExist(err) {
					t.Fatal("failed bundle package created output")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			descriptor, raw, err := delivery.ReadArtifact(t.Context(), artifacts["package/rendered"].Path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, archive) {
				t.Fatal("bundle packaging changed producer bytes")
			}
			observed, err := ObserveLifecyclePayload(t.Context(), plan, plan.LifecycleOutputs[0], artifacts)
			if err != nil {
				t.Fatal(err)
			}
			if observed.Digest != descriptor.Digest || observed.SemanticDigest != pipeline.FileDigest(archive) || observed.Digest == observed.SemanticDigest {
				t.Fatalf("bundle identity conflated: %+v", observed)
			}
		})
	}
}

func TestBundlePublicationCarriesBothEvidenceStages(t *testing.T) {
	plan, artifacts, archive := bundleTestWorkspace(t, true)
	for _, nodeID := range []string{"rendered/sbom", "package/rendered", "package/release"} {
		node := planNode(plan, nodeID)
		var err error
		if node.Kind == NodeSBOM {
			err = executeSBOM(t.Context(), plan, node, artifacts, io.Discard)
		} else {
			err = executePackage(t.Context(), plan, node, artifacts, io.Discard)
		}
		if err != nil {
			t.Fatalf("%s: %v", nodeID, err)
		}
	}
	inspected, err := delivery.Inspect(t.Context(), artifacts["package/release"].Path)
	if err != nil {
		t.Fatal(err)
	}
	stages := map[string]bool{}
	for _, object := range inspected.Root.Objects {
		if object.Output != nil {
			if object.Output.Format != authored.OutputBundle || object.BlobDigest != pipeline.FileDigest(archive) {
				t.Fatalf("wrong bundle binding: %+v", object)
			}
		}
		if object.Evidence != nil {
			stages[object.Evidence.Stage+"/"+object.Evidence.SubjectKind] = true
		}
	}
	if !stages["source-build/source-archive"] || !stages["packaging/bundle"] || len(stages) != 2 {
		t.Fatalf("evidence stages = %v", stages)
	}
}
