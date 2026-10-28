// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func namedGitHubTestPlan(t *testing.T, name string) *Plan {
	t.Helper()
	plan := githubTestPlan(t)
	plan.Environment.GitHubBundle = name
	digest, err := plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.EnvironmentDigest = digest
	return plan
}

func TestNamedGitHubWorkflowNamesDoNotCollideWithDefault(t *testing.T) {
	defaultPlan := githubTestPlan(t)
	defaultBundle, err := RenderGitHub(t.Context(), defaultPlan)
	if err != nil {
		t.Fatal(err)
	}
	defaultName := decodeGitHubWorkflow(t, defaultBundle.Files[defaultBundle.WorkflowPath]).Name
	if defaultName != "nvkr-run-"+defaultPlan.Name {
		t.Fatal("default workflow display name changed")
	}
	names := map[string]bool{defaultName: true}
	for _, name := range []string{defaultPlan.Name, "other-project"} {
		t.Run(name, func(t *testing.T) {
			plan := *defaultPlan
			plan.Environment.GitHubBundle = name
			plan.EnvironmentDigest, err = plan.Environment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			bundle, renderErr := RenderGitHub(t.Context(), &plan)
			if renderErr != nil {
				t.Fatal(renderErr)
			}
			workflowName := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath]).Name
			if workflowName != "nvkr-project-"+name || names[workflowName] {
				t.Fatalf("named producer shares a display/concurrency identity: %q", workflowName)
			}
			names[workflowName] = true
			if bundle.WorkflowPath != githubBundleLayout(name).workflowPath {
				t.Fatal("display naming changed the named workflow path")
			}
			changed := mutateGitHubBundleFile(t, bundle, bundle.WorkflowPath, func(raw []byte) []byte {
				return bytes.Replace(raw, []byte("name: "+workflowName+"\n"), []byte("name: "+defaultName+"\n"), 1)
			})
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), &plan, changed), e.CodeInvalidCatalog)
		})
	}
}

func TestNamedGitHubBundleChangesPlanIdentityWithoutReplacingSigningPolicy(t *testing.T) {
	plan := signedTestPlan(t, cosignSigningProfile(), ghcrTarget())
	before, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	identity, issuer := plan.Environment.Signing.Identity, plan.Environment.Signing.Issuer
	build := plan.Builds[0].BuildDigest
	plan.Environment.GitHubBundle = "nightly"
	plan.EnvironmentDigest, err = plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.PlanDigest == before || plan.Builds[0].BuildDigest != build ||
		plan.Environment.Signing.Identity != identity || plan.Environment.Signing.Issuer != issuer {
		t.Fatal("namespace changed Build identity/signing policy or failed to bind Plan identity")
	}
}

func TestNamedGitHubBundlesCoexistRegenerateAndRetireIndependently(t *testing.T) {
	root := t.TempDir()
	bundles := map[string]*GeneratedBundle{}
	for _, name := range []string{"", "nvke-nightly", "other-project", "development"} {
		plan := namedGitHubTestPlan(t, name)
		if name == "development" {
			plan = githubDevelopmentTestPlan(t)
		}
		bundle, err := RenderGitHub(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		layout := githubLayoutFor(plan)
		if bundle.WorkflowPath != layout.workflowPath || bundle.PlanPath != layout.planPath || bundle.ManifestPath != layout.manifestPath {
			t.Fatal("bundle paths disagree with selected Environment namespace")
		}
		workflow := string(bundle.Files[bundle.WorkflowPath])
		if name != "" && strings.Contains(workflow, "--plan "+FactoryPlanPath) {
			t.Fatal("named workflow executes the default Plan")
		}
		if !strings.Contains(workflow, "--plan "+bundle.PlanPath) {
			t.Fatal("workflow does not execute its generated Plan")
		}
		again, err := RenderGitHub(t.Context(), plan)
		if err != nil || !bytes.Equal(bundle.Files[bundle.WorkflowPath], again.Files[again.WorkflowPath]) ||
			!bytes.Equal(bundle.Files[bundle.PlanPath], again.Files[again.PlanPath]) {
			t.Fatalf("same namespace did not regenerate exactly: %v", err)
		}
		if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
		bundles[name] = bundle
	}
	for _, bundle := range bundles {
		if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"nvke-nightly", "development"} {
		selected := bundles[name]
		prior := *selected
		prior.Files = make(map[string][]byte, len(selected.Files)+1)
		for name, raw := range selected.Files {
			prior.Files[name] = raw
		}
		obsolete := filepath.ToSlash(filepath.Join(filepath.Dir(selected.ManifestPath), "retired", "old.json"))
		prior.Files[obsolete] = []byte("old graph\n")
		if err := WriteGeneratedBundle(t.Context(), root, &prior); err != nil {
			t.Fatal(err)
		}
		if err := WriteGeneratedBundle(t.Context(), root, selected); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, obsolete)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("own obsolete record survived retirement: %v", err)
		}
		for _, bundle := range bundles {
			if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
				t.Fatalf("retirement disturbed a coexisting namespace: %v", err)
			}
		}
	}
	nightly := bundles["nvke-nightly"]
	if err := os.WriteFile(filepath.Join(root, nightly.WorkflowPath), []byte("hand edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, nightly), e.CodePipelineDrift)
	for name, bundle := range bundles {
		if name != "nvke-nightly" {
			if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
				t.Fatalf("other namespace inherited nightly drift: %v", err)
			}
		}
	}
}

func TestNamedGitHubBundleRejectsCrossNamespaceOwnershipBeforeIO(t *testing.T) {
	plan := namedGitHubTestPlan(t, "nightly")
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workflow", "plan", "manifest", "member", "unsafe"} {
		t.Run(name, func(t *testing.T) {
			changed := *bundle
			changed.Files = make(map[string][]byte, len(bundle.Files)+1)
			for path, raw := range bundle.Files {
				changed.Files[path] = raw
			}
			switch name {
			case "workflow":
				changed.WorkflowPath = GitHubWorkflowPath
			case "plan":
				changed.PlanPath = FactoryPlanPath
			case "manifest":
				changed.ManifestPath = GeneratedManifestPath
			case "member":
				changed.Files[".github/nvkr-run-other/old.json"] = []byte("other owner")
			case "unsafe":
				changed.WorkflowPath = ".github/workflows/nvkr-run-../escape.yml"
			}
			root := t.TempDir()
			assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), root, &changed), e.CodeInvalidCatalog)
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid namespace caused output: %v %v", entries, err)
			}
		})
	}
	changed := *bundle
	changed.WorkflowPath = GitHubWorkflowPath
	assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &changed), e.CodePipelineDrift)
}

func TestNamedGitHubBundleCannotRetireAnotherNamespace(t *testing.T) {
	bundle, err := RenderGitHub(t.Context(), namedGitHubTestPlan(t, "nightly"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := RenderGitHub(t.Context(), namedGitHubTestPlan(t, "other"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, current := range []*GeneratedBundle{bundle, other} {
		if err := WriteGeneratedBundle(t.Context(), root, current); err != nil {
			t.Fatal(err)
		}
	}
	manifest, _, err := ManifestFor(bundle)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Members = append(manifest.Members, GeneratedMember{Path: other.WorkflowPath,
		SHA256: other.WorkflowDigest, Mode: 0o644})
	// The foreign member sorts last because the existing named workflow ends
	// with nightly.yml and the injected workflow ends with other.yml.
	raw, err := canonical.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, bundle.ManifestPath), append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), root, bundle), e.CodePipelineDrift)
	if err := CheckGeneratedBundle(t.Context(), root, other); err != nil {
		t.Fatalf("hostile manifest changed another namespace: %v", err)
	}
}

func TestGitHubBundleNameRejectedBeforeCatalogResolution(t *testing.T) {
	plan := githubTestPlan(t)
	plan.Environment.GitHubBundle = "../../escape"
	_, err := CompileDocuments(t.Context(), "/does-not-exist/definition.yaml", plan.Definition,
		"/does-not-exist/environment.yaml", plan.Environment, plan.GeneratorVersion)
	assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
	if !strings.Contains(err.Error(), "githubBundle") {
		t.Fatalf("invalid namespace reached catalog resolution: %v", err)
	}
	_, err = RenderGitHub(t.Context(), plan)
	assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
}

func TestNamedGitHubUtilityLaunchUsesOnlyItsAdmittedPlan(t *testing.T) {
	plan, node, calls := githubRunnerRuntimeFixture(t, "exit 0\n")
	plan.Environment.GitHubBundle = "nightly"
	digest, err := plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.EnvironmentDigest = digest
	layout := githubBundleLayout(plan.Environment.GitHubBundle)
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(layout.planPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.planPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RunGitHubNode(t.Context(), plan, node.ID, GitHubStepOptions{}); err != nil {
		t.Fatal(err)
	}
	launch, err := os.ReadFile(calls)
	if err != nil || !strings.Contains(string(launch), "--plan\n"+layout.planPath+"\n") ||
		strings.Contains(string(launch), "--plan\n"+FactoryPlanPath+"\n") {
		t.Fatalf("named utility did not preserve Plan path: %s %v", launch, err)
	}
}
