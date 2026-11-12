// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func acquiredInternalToolPlan(t *testing.T, platform, url, digest string) *Plan {
	t.Helper()
	base := arcTestPlan(t, platform)
	d := base.Definition.Clone()
	d.InternalTools = []authored.InternalTool{{Name: "inspector", Platform: platform, Format: authored.OutputExecutable, Components: []string{"helper"}, Result: "helper"}}
	env := base.Environment.Clone()
	profile := env.DiskInspectionProfiles[platform]
	profile.HelperTool = "inspector"
	env.DiskInspectionProfiles[platform] = profile
	base.CatalogSnapshot.Components["helper"] = catalog.Component{Platforms: []string{platform}, Tag: "1.0.0", SourceDate: 1,
		OutputType: "host-package", ArtifactPath: "helper", Prebuilt: &catalog.Prebuilt{URL: url,
			Digests: map[string]string{strings.TrimPrefix(platform, "linux/"): digest}, Evidence: &catalog.ExternalEvidence{None: true}}}
	stack, err := catalog.ApplyStack(base.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), d, env, stack, base.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = base.DefinitionPath, base.EnvironmentPath
	return plan
}

func TestInternalToolSBOMOfflineLaunch(t *testing.T) {
	plan := acquiredInternalToolPlan(t, "linux/amd64", "https://example.test/helper", "sha256:"+strings.Repeat("a", 64))
	node := planNode(plan, "internal/inspector/sbom")
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	calls := filepath.Join(root, "calls")
	t.Setenv("NVKR_TEST_RUNTIME_LOG", calls)
	script := "#!/bin/sh\nprintf '%s\n' CALL \"$@\" >> \"$NVKR_TEST_RUNTIME_LOG\"\n" + preparedUtilityRuntimeProtocol(node, "docker") + "exit 0\n"
	if err := os.WriteFile(filepath.Join(root, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFactoryFile(t.Context(), FactoryPlanPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := launchGitHubUtility(t.Context(), plan, node, GitHubStepOptions{}); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--network\nnone", node.Container + "\n/workspace/nvkr-exec", "--internal-tool-sbom", "--plan-digest"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %q in %s", want, raw)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*Node)
	}{
		{"online", func(n *Node) { n.Network = ir.NetworkDefault }},
		{"uncontained", func(n *Node) { n.Container = "" }},
		{"producer", func(n *Node) { n.IRNode = "producer" }},
		{"other output", func(n *Node) { n.Outputs = []string{"foreign"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := node
			tc.change(&changed)
			assertFactoryErrorCode(t, validateInternalToolSBOMNode(plan, changed), e.CodeEngineCapability)
		})
	}
}

func TestInternalToolSBOMHasSeparateDeadlineReserve(t *testing.T) {
	plan := acquiredInternalToolPlan(t, "linux/amd64", "https://example.test/helper", "sha256:"+strings.Repeat("a", 64))
	stages, _, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range stages {
		if stage.DiskOutput == "" {
			continue
		}
		without := stage
		without.Nodes = nil
		for _, n := range stage.Nodes {
			if n.ID != "internal/inspector/sbom" {
				without.Nodes = append(without.Nodes, n)
			}
		}
		before, _, err := githubDiskStageBudget(plan, without)
		if err != nil {
			t.Fatal(err)
		}
		if stage.diskExecutionBudget-before != time.Hour {
			t.Fatal("scanner consumed the shared completion reserve")
		}
		seen := false
		err = executeGitHubDiskNodes(t.Context(), plan, stage, &LocalRunResult{}, GitHubStepOptions{}, func(ctx context.Context, n Node) error {
			if n.ID == "internal/inspector/sbom" {
				seen = true
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > time.Hour {
					t.Fatal("scanner lacks its bounded deadline")
				}
			}
			return nil
		}, func(context.Context) error { return nil })
		if err != nil || !seen {
			t.Fatalf("scanner dispatch: %v, seen=%v", err, seen)
		}
	}
}

// A real source-free acquisition issues the capability. The callback replaces
// only container transport; the separate launch test checks its offline route.
func TestInternalToolDispatcherCompletesOwnedEvidence(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("produced executable ownership requires Linux")
	}
	// The test executable may use cgo (or race instrumentation) on CI. Build
	// an independent static helper instead of inheriting the harness linkage.
	root := t.TempDir()
	source, helper := filepath.Join(root, "main.go"), filepath.Join(root, "helper")
	if err := os.WriteFile(source, []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildmode=exe", "-o", helper, source)
	command.Dir = root
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=-p=2",
		"GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
	var buildLog bytes.Buffer
	command.Stdout, command.Stderr = &buildLog, &buildLog
	if err := ownedcmd.Run(ctx, command); err != nil {
		t.Fatalf("building static acquired helper: %v: %s", err, &buildLog)
	}
	raw, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
	defer server.Close()
	originalTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	plan := acquiredInternalToolPlan(t, "linux/"+runtime.GOARCH, server.URL+"/helper", pipeline.FileDigest(raw))
	identity, err := plan.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	owner := &LocalRunResult{PlanDigest: identity}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	session := newOperationSession(plan, operationPlacement{owner: owner, runtime: plan.Environment.RuntimeBinary(), jobs: 1, producer: (pipeline.Local{}).ExecNode,
		launch: func(ctx context.Context, n Node) error { return ComputeInternalToolSBOM(ctx, plan, n.ID, io.Discard) },
	}, io.Discard, io.Discard)
	tool := plan.InternalTools[0]
	nodes, artifacts := planIndex(plan)
	producer := nodes[artifacts[tool.ArtifactID].Producer]
	if producer.Kind != NodeAcquire {
		t.Fatal("fixture is not source-free")
	}
	if err := session.dispatch(t.Context(), plan, producer); err != nil {
		t.Fatal(err)
	}
	_, err = owner.borrowInternalExecutable(t.Context(), plan, tool.Name)
	assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
	for _, id := range []string{tool.ObservationArtifactID, tool.SBOMArtifactID} {
		if id == tool.SBOMArtifactID {
			launch := session.placement.launch
			session.placement.launch = nil
			assertFactoryErrorCode(t, session.dispatch(t.Context(), plan, nodes[artifacts[id].Producer]), e.CodeEngineCapability)
			if _, err := os.Stat(artifacts[id].Path); !os.IsNotExist(err) {
				t.Fatal("missing container route wrote host SBOM")
			}
			session.placement.launch = launch
		}
		if err := session.dispatch(t.Context(), plan, nodes[artifacts[id].Producer]); err != nil {
			t.Fatal(err)
		}
	}
	borrowed, err := owner.borrowInternalExecutable(t.Context(), plan, tool.Name)
	if err != nil {
		t.Fatal(err)
	}
	if borrowed != owner.internalExecutables[tool.Name] {
		t.Fatal("borrow restored a capability instead of retaining its owner")
	}
	consumer := nodes["node-image/sbom"]
	if len(consumer.InternalTools) != 1 || consumer.InternalTools[0] != tool.Name {
		t.Fatal("inspection lost its exact helper binding")
	}
	assertFactoryErrorCode(t, session.dispatch(t.Context(), plan, nodes[artifacts[tool.SBOMArtifactID].Producer]), e.CodeVerifyFailed)
	recorded, err := readProducerResult(t.Context(), artifacts[tool.ResultArtifactID].Path)
	if err != nil {
		t.Fatal(err)
	}
	recorded.Path = "substituted-recorded-path"
	changed, err := canonical.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFactoryFile(t.Context(), artifacts[tool.ResultArtifactID].Path, changed, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = owner.borrowInternalExecutable(t.Context(), plan, tool.Name)
	assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := borrowed.BindingContext(t.Context()); err == nil {
		t.Fatal("borrow outlived original owner")
	}
}
