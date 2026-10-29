// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func rawDebianInputs(t *testing.T) (authored.Definition, authored.Environment, *catalog.Stack) {
	t.Helper()
	component := catalog.Component{Platforms: []string{"linux/amd64"}, FromSource: true,
		BuilderType: ir.BuilderOCIHermetic, OutputType: "host-package", Repo: "https://example.test/source",
		Tag: "v1.0.0", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), SourceDate: 1,
		ToolchainImage: fixtureUtilityContainer, BuildCommand: []string{"make", "deb"},
		ArtifactPath: "out/agent.deb", WriteDir: "out"}
	stack, err := catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{"agent": component}})
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := fixtureInputs("raw-package", "agent", "")
	definition.Outputs[0].Format, definition.Outputs[0].Components = authored.OutputDebianPackage, []string{"agent"}
	return definition, environment, stack
}

func rawDebianFixture(t *testing.T) (*Plan, string, string, string, pipeline.ArtifactResult) {
	t.Helper()
	t.Chdir(t.TempDir())
	d, env, stack := rawDebianInputs(t)
	plan, err := Compile(t.Context(), d, env, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	workspace := ".nvkr/attempt"
	localized, err := relocateFactoryPlan(plan, workspace)
	if err != nil {
		t.Fatal(err)
	}
	nodes, artifacts := planIndex(localized)
	payload := artifacts[plan.LifecycleOutputs[0].ArtifactID]
	_, producer, err := localized.producerInstruction(nodes[payload.Producer])
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("controlled raw producer bytes; not a real Debian archive")
	if err := os.MkdirAll(filepath.Dir(payload.Path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	result := pipeline.ArtifactResult{Component: producer.Component, OutputType: producer.OutputType,
		Qualification: pipeline.ArtifactUnqualified, Toolchain: producer.Toolchain, BuilderType: producer.BuilderType,
		InputDigest: "sha256:" + strings.Repeat("d", 64), Path: producer.ArtifactPath, SHA256: pipeline.FileDigest(data), Size: int64(len(data))}
	resultPath := artifacts["result/agent/agent"].Path
	writeRawResult(t, resultPath, result)
	return plan, workspace, payload.Path, resultPath, result
}

func writeRawResult(t *testing.T, path string, result pipeline.ArtifactResult) {
	t.Helper()
	raw, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRawDebianPlanUsesOrdinaryProducer(t *testing.T) {
	d, env, stack := rawDebianInputs(t)
	plan, err := Compile(t.Context(), d, env, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if plan.LifecycleOutputs[0].ArtifactID != "artifact/agent/agent" {
		t.Fatal("raw package gained a wrapper")
	}
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodePackage {
			t.Fatal("raw package gained a separate packaging operation")
		}
	}
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.Kind == ir.KindBuild && (node.OutputType != "host-package" || !reflect.DeepEqual(node.Command, []string{"make", "deb"})) {
			t.Fatal("ordinary catalog producer command changed")
		}
	}
	_, artifacts := planIndex(plan)
	if artifacts["sbom/agent"].Kind != "sbom-set" {
		t.Fatal("raw package omitted ordinary source composition")
	}
	if _, err := RenderGitHub(t.Context(), plan); err == nil {
		t.Fatal("raw package admitted hosted execution")
	}
	assertFactoryErrorCode(t, ExecuteNode(t.Context(), plan, "agent/terminal-observe", io.Discard, io.Discard), e.CodeEngineCapability)
}

func TestRawDebianDirectNodeGatePreservesOrdinarySibling(t *testing.T) {
	d, env, stack := rawDebianInputs(t)
	sibling := d.Outputs[0]
	sibling.Name, sibling.Format = "sibling", authored.OutputExecutable
	d.Outputs = append(d.Outputs, sibling)
	plan, err := Compile(t.Context(), d, env, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range plan.Graph.Nodes {
		if node.BuildTarget != "agent" && node.BuildTarget != "sibling" {
			continue
		}
		admitted, err := admitExecutionNode(t.Context(), plan, node.ID)
		if node.BuildTarget == "agent" {
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
		} else if err != nil || admitted.ID != node.ID {
			t.Fatalf("raw sibling blocked ordinary operation %s: %v", node.ID, err)
		}
	}
	if _, err := RenderGitHub(t.Context(), plan); err == nil {
		t.Fatal("mixed Plan bypassed raw hosted-execution gate")
	}
}

func TestRawDebianUnsupportedCapabilitiesBeforeCatalogIO(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*authored.Definition, *authored.Environment)
	}{
		{"inspection", func(d *authored.Definition, _ *authored.Environment) { d.SBOMTool = "absent-scanner" }},
		{"publication", func(_ *authored.Definition, env *authored.Environment) {
			env.Publication = &authored.Publication{RepositoryPrefix: "example.test/team", Anonymous: true}
		}},
		{"hosted", func(_ *authored.Definition, env *authored.Environment) {
			env.GitHubRunners = map[string]string{"linux/amd64": "ubuntu-latest"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, env, _ := rawDebianInputs(t)
			d.Catalog.Base = "missing-catalog"
			tc.mutate(&d, &env)
			plan, err := CompileDocuments(t.Context(), "definition.yaml", d, "environment.yaml", env, "test")
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if plan != nil {
				t.Fatal("unsupported capability returned Plan")
			}
		})
	}
}

func TestRawDebianLocalAdmissionBeforeWorkspaceIO(t *testing.T) {
	t.Chdir(t.TempDir())
	d, env, stack := rawDebianInputs(t)
	plan, err := Compile(t.Context(), d, env, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	options := LocalRunOptions{Workspace: ".nvkr/attempt", DryRun: true, Stdout: io.Discard, Stderr: io.Discard}
	if _, err := RunLocal(t.Context(), plan, options); err != nil {
		t.Fatal("raw package dry run required execution capability", err)
	}
	options.DryRun, options.Runtime = false, "unapproved-runtime"
	assertFactoryErrorCode(t, AdmitLocal(t.Context(), plan, options), e.CodeEngineCapability)
	options.Runtime = env.RuntimeBinary()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		assertFactoryErrorCode(t, AdmitLocal(t.Context(), plan, options), e.CodeEngineCapability)
	} else {
		if err := admitLocalDebianHost(plan, options); err != nil {
			t.Fatal("raw admission invented a mandatory deadline", err)
		}
	}
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 0 {
		t.Fatal("raw package admission wrote a workspace", err)
	}
}

func TestRawDebianExportAuthenticatesExactProducerBytes(t *testing.T) {
	plan, workspace, path, _, recorded := rawDebianFixture(t)
	var destination bytes.Buffer
	result, err := ExportWorkspaceDebianPayload(t.Context(), plan, plan.LifecycleOutputs[0], workspace, &destination)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(destination.Bytes(), want) || result.SHA256 != recorded.SHA256 || result.Size != recorded.Size {
		t.Fatal("raw export changed producer bytes or result", err)
	}
}

func TestRawDebianLocalObservationRetainsSourceComposition(t *testing.T) {
	plan, workspace, _, _, _ := rawDebianFixture(t)
	options := LocalRunOptions{Workspace: workspace, Stdout: io.Discard, Stderr: io.Discard}
	for _, id := range []string{"agent/terminal-observe", "agent/sbom"} {
		if err := executeLocalFactoryNode(t.Context(), plan, planNode(plan, id), options, nil); err != nil {
			t.Fatal(err)
		}
	}
	localized, err := relocateFactoryPlan(plan, workspace)
	if err != nil {
		t.Fatal(err)
	}
	_, artifacts := planIndex(localized)
	if _, err := os.Stat(filepath.Join(artifacts["sbom/agent"].Path, "agent.cdx.json")); err != nil {
		t.Fatal("ordinary source composition was not retained", err)
	}
}

func TestRawDebianExportRejectsMisboundOrChangedBytes(t *testing.T) {
	for _, mode := range []string{"slot", "component", "missing input", "qualified", "digest", "size", "symlink", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			plan, workspace, path, recordPath, recorded := rawDebianFixture(t)
			expected := plan.LifecycleOutputs[0]
			switch mode {
			case "slot":
				expected.Name = "foreign"
			case "component":
				recorded.Component = "foreign"
			case "missing input":
				recorded.InputDigest = ""
			case "qualified":
				recorded.Qualification = pipeline.ArtifactQualifiedExecution
			case "digest":
				recorded.SHA256 = "sha256:" + strings.Repeat("f", 64)
			case "size":
				recorded.Size++
			case "symlink":
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(path)+".original", path); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.Truncate(path, pipeline.MaxBuildMaterialBytes+1); err != nil {
					t.Fatal(err)
				}
			}
			writeRawResult(t, recordPath, recorded)
			var destination bytes.Buffer
			result, err := ExportWorkspaceDebianPayload(t.Context(), plan, expected, workspace, &destination)
			if err == nil || destination.Len() != 0 || result != (pipeline.ArtifactResult{}) {
				t.Fatalf("rejected export wrote bytes or returned a result: %v", err)
			}
			wantCode := e.CodeVerifyFailed
			if mode == "qualified" {
				wantCode = e.CodePreflightFailed
			}
			if mode == "symlink" || mode == "oversize" {
				wantCode = e.CodeEngineCapability
			}
			assertFactoryErrorCode(t, err, wantCode)
		})
	}
}

func TestRawDebianExportCancellationAndWriteFailure(t *testing.T) {
	plan, workspace, _, _, _ := rawDebianFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := ExportWorkspaceDebianPayload(ctx, plan, plan.LifecycleOutputs[0], workspace, io.Discard)
	assertFactoryErrorCode(t, err, e.CodeTimeout)
	_, err = ExportWorkspaceDebianPayload(t.Context(), plan, plan.LifecycleOutputs[0], workspace, rawRejectWriter{})
	assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
}

type rawRejectWriter struct{}

func (rawRejectWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
