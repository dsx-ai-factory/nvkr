package factory

import (
	"context"
	"strings"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
)

// TestCommittedGraphDigestMatchesEmittedNodesDigest guards the trailing-newline drift: at
// runtime `nkd-exec exec-node --nodes-digest` re-hashes the committed graph file it reads, so
// the digest baked into the workflow MUST equal FileDigest of the exact committed file bytes.
func TestCommittedGraphDigestMatchesEmittedNodesDigest(t *testing.T) {
	plan := buildOnlyPlan(t, "generic")
	for _, tc := range []struct {
		render func() (*GeneratedBundle, error)
		wfPath string
		dir    string
	}{
		{func() (*GeneratedBundle, error) { return RenderGitLab(context.Background(), plan) }, GitLabWorkflowPath, GitLabGraphDir},
		{func() (*GeneratedBundle, error) { return RenderGitHub(context.Background(), plan) }, GitHubWorkflowPath, FactoryGraphDir},
	} {
		b, err := tc.render()
		if err != nil {
			t.Fatal(err)
		}
		wf := string(b.Files[tc.wfPath])
		graphs := 0
		for path, raw := range b.Files {
			if !strings.HasPrefix(path, tc.dir+"/") {
				continue
			}
			graphs++
			if d := pipeline.FileDigest(raw); !strings.Contains(wf, "--nodes-digest "+d) {
				t.Fatalf("graph %s file-digest %s is not the emitted --nodes-digest (trailing-newline drift)", path, d)
			}
		}
		if graphs == 0 {
			t.Fatalf("expected graph files under %s", tc.dir)
		}
	}
}

// buildOnlyPlan compiles a plan from a delivery profile with NO routes/targets — the build-only
// mode — optionally overriding the runner via the delivery's runsOn.
func buildOnlyPlan(t *testing.T, deliveryRunsOn string) *Plan {
	t.Helper()
	base, err := catalog.LoadContext(context.Background(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition := CompileSpec{APIVersion: CompileSpecAPIVersion, Kind: CompileSpecKind,
		Metadata: Metadata{Name: "buildonly"}, Catalog: CatalogStack{Base: "testdata/catalogs/kubernetes-layered"},
		UtilityContainer: "golang@sha256:fe5d57d3b718e7a4986bae156c2d73f44973bfd313073aed08a4de6692bb6161",
		UtilityPlatform:  "linux/amd64",
		Recipes: []RecipeTarget{{Name: "worker", Criteria: criteria.Criteria{
			Category: "kubernetes", Role: "worker", K8sVersion: "1.34.0", OS: "ubuntu", Arch: "amd64",
		}}},
		Packages: []PackageSpec{{Name: "tools", Kind: PackageBinaryBundle, Inputs: []string{"worker/*"}}},
	}
	delivery := ExecutionSettings{APIVersion: ExecutionSettingsAPIVersion, Kind: ExecutionSettingsKind,
		Metadata: Metadata{Name: "build-only"}, RunsOn: deliveryRunsOn}
	plan, err := Compile(context.Background(), definition, delivery, stack, "v0.0.0 (test)")
	if err != nil {
		t.Fatalf("a build-only delivery (no routes) must compile: %v", err)
	}
	plan.DefinitionPath = "examples/factory.yaml"
	plan.EnvironmentPath = "examples/environment.yaml"
	return plan
}

// TestBuildOnlyDeliveryPublishesNothing is the core guarantee of the build-only mode: no publish
// node in the graph, and no publish/credential/registry surface in the generated pipeline.
func TestBuildOnlyDeliveryPublishesNothing(t *testing.T) {
	plan := buildOnlyPlan(t, "")
	for _, n := range plan.Graph.Nodes {
		if n.Kind == NodePublish {
			t.Fatalf("build-only delivery must emit no publish node; found %q", n.ID)
		}
	}
	for _, engine := range []struct {
		name   string
		render func() (*GeneratedBundle, error)
		path   string
	}{
		{"gitlab", func() (*GeneratedBundle, error) { return RenderGitLab(context.Background(), plan) }, GitLabWorkflowPath},
		{"github", func() (*GeneratedBundle, error) { return RenderGitHub(context.Background(), plan) }, GitHubWorkflowPath},
	} {
		b, err := engine.render()
		if err != nil {
			t.Fatalf("%s build-only render: %v", engine.name, err)
		}
		wf := string(b.Files[engine.path])
		for _, forbidden := range []string{"NKD_CREDENTIAL", "publish/", "docker login", "CI_REGISTRY"} {
			if strings.Contains(wf, forbidden) {
				t.Fatalf("%s build-only pipeline must not contain %q:\n%s", engine.name, forbidden, wf)
			}
		}
	}
}

// TestCommittedCanonicalMembersHaveNoTrailingNewline guards every canonical bundle member
// (plan, graphs, compositions) that is read back at runtime with canonical.UnmarshalExact,
// which rejects a trailing newline. Only the workflow (.yml) may end with one.
func TestCommittedCanonicalMembersHaveNoTrailingNewline(t *testing.T) {
	plan := buildOnlyPlan(t, "generic")
	b, err := RenderGitLab(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for path, raw := range b.Files {
		if strings.HasSuffix(path, ".yml") {
			continue
		}
		if strings.HasSuffix(string(raw), "\n") {
			t.Fatalf("canonical member %s ends with a newline; UnmarshalExact will reject it at runtime", path)
		}
	}
}

// TestToolAcquireNodeIsNotIRProjected guards the routing bug: a tool-download acquire node has
// no RecipeTarget/IRNode, so it must run via the glue `factory execute-node` path, not the IR
// exec-node branch (which would reference a nonexistent graphs/.json).
func TestToolAcquireNodeIsNotIRProjected(t *testing.T) {
	if irProjectedNode(Node{ID: "tool/aicr", Kind: NodeAcquire}) {
		t.Fatal("a tool acquire node (no RecipeTarget/IRNode) must not be IR-projected")
	}
	if !irProjectedNode(Node{ID: "gpu/pull-x", Kind: NodeAcquire, RecipeTarget: "gpu", IRNode: "pull-x"}) {
		t.Fatal("a recipe-projected acquire node must be IR-projected")
	}
	if irProjectedNode(Node{ID: "package/tools", Kind: NodePackage}) {
		t.Fatal("a package (glue) node must not be IR-projected")
	}
}

// TestDeliveryRuntimeConfigRendersOnBothEngines proves the delivery runtime environment config
// (image, dind services, job variables, setup) is engine-AGNOSTIC: both emitters render it, each
// in its own syntax. It must not be GitLab-only.
func TestDeliveryRuntimeConfigRendersOnBothEngines(t *testing.T) {
	plan := buildOnlyPlan(t, "generic")
	plan.ExecutionSettings.Runtime.RuntimeImage = "docker:27@sha256:" + strings.Repeat("a", 64)
	plan.ExecutionSettings.Runtime.Services = []string{"docker:dind@sha256:" + strings.Repeat("b", 64)}
	plan.ExecutionSettings.Runtime.Variables = map[string]string{"DOCKER_HOST": "tcp://docker:2375"}
	plan.ExecutionSettings.Runtime.Setup = []string{"docker login -u user -p pass registry.example"}
	digest, err := plan.ExecutionSettings.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.ExecutionSettingsDigest = digest

	gl, err := RenderGitLab(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	glwf := string(gl.Files[GitLabWorkflowPath])
	for _, want := range []string{"image: 'docker:27@sha256:", "services: ['docker:dind@sha256:",
		"DOCKER_HOST: 'tcp://docker:2375'", "before_script:", "docker login -u user -p pass registry.example"} {
		if !strings.Contains(glwf, want) {
			t.Fatalf("gitlab runtime config missing %q", want)
		}
	}
	gh, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	ghwf := string(gh.Files[GitHubWorkflowPath])
	for _, want := range []string{"container: 'docker:27@sha256:", "image: 'docker:dind@sha256:",
		"DOCKER_HOST: 'tcp://docker:2375'", "- name: Configure build environment", "docker login -u user -p pass registry.example"} {
		if !strings.Contains(ghwf, want) {
			t.Fatalf("github runtime config missing %q", want)
		}
	}
}

// TestDeliveryRunsOnOverridesCompileSpecRunner proves the runner is environment config: a
// delivery runsOn overrides the definition's default on every emitted node.
func TestDeliveryRunsOnOverridesCompileSpecRunner(t *testing.T) {
	plan := buildOnlyPlan(t, "generic")
	if len(plan.Graph.Nodes) == 0 {
		t.Fatal("expected a non-empty graph")
	}
	for _, n := range plan.Graph.Nodes {
		if n.RunsOn != "generic" {
			t.Fatalf("delivery runsOn must override the definition default; node %q RunsOn=%q", n.ID, n.RunsOn)
		}
	}
}
