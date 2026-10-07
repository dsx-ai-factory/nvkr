package factory

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func nativeGitHubTestPlan(t *testing.T, routes map[string]string) *Plan {
	t.Helper()
	original := githubTestPlan(t)
	definition, environment := original.Definition.Clone(), original.Environment.Clone()
	first := definition.Outputs[0]
	first.Name = "cli-amd64"
	second := first
	second.Name = "cli-arm64"
	second.Platform = "linux/arm64"
	definition.Outputs = []authored.DefinitionOutput{first, second}
	environment.RunsOn = ""
	environment.GitHubRunners = routes
	stack, err := catalog.ApplyStack(original.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definition, environment, stack, "v1.2.3 (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath = "definitions/fixture.yaml"
	plan.EnvironmentPath = "definitions/environment.yaml"
	return plan
}

func TestGitHubNativePlatformRoutesAndHelper(t *testing.T) {
	plan := nativeGitHubTestPlan(t, map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm"})
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath])
	for _, tc := range []struct{ platform, runner string }{{"linux/amd64", "ubuntu-24.04"}, {"linux/arm64", "ubuntu-24.04-arm"}} {
		bootstrapID := "build-runner-" + shortStableName(tc.runner)
		bootstrap := document.Jobs[bootstrapID]
		if len(bootstrap.Steps) == 0 || strings.TrimSpace(bootstrap.Steps[0].Run) != githubPlatformCheck(plan, tc.runner) {
			t.Fatalf("missing %s bootstrap preflight", tc.platform)
		}
		if !strings.Contains(joinRuns(bootstrap.Steps), "go build -trimpath") {
			t.Fatalf("missing native %s helper build", tc.platform)
		}
		count := 0
		for _, node := range plan.Graph.Nodes {
			projected, err := plan.GitHubExecutionFor(node)
			if err != nil {
				t.Fatal(err)
			}
			if node.IRNode == "" || projected.Platform != tc.platform {
				continue
			}
			count++
			job := document.Jobs[githubJobID(node.ID)]
			if job.RunsOn != tc.runner || !containsString(job.Needs, bootstrapID) {
				t.Fatalf("misbound %s native job: %+v", node.ID, job)
			}
			if !stepDownloadsArtifact(plan, job.Steps, "nkd-exec-"+shortStableName(tc.runner)) {
				t.Fatalf("job %s downloads a foreign helper", node.ID)
			}
		}
		if count == 0 {
			t.Fatalf("no producer for %s", tc.platform)
		}
	}
	if plan.Builds[0].BuildDigest == plan.Builds[1].BuildDigest {
		t.Fatal("architectures share Build identity")
	}
	changed := nativeGitHubTestPlan(t, map[string]string{"linux/amd64": "ubuntu-22.04", "linux/arm64": "ubuntu-22.04-arm"})
	if plan.EnvironmentDigest == changed.EnvironmentDigest {
		t.Fatal("routes absent from Environment identity")
	}
	for index, build := range plan.Builds {
		if build.BuildDigest != changed.Builds[index].BuildDigest {
			t.Fatal("transport route changed logical Build identity")
		}
	}
	// Bootstrap routing cannot be substituted even if the workflow blob digest is recomputed.
	mutated := *bundle
	mutated.Files = map[string][]byte{}
	for name, raw := range bundle.Files {
		mutated.Files[name] = append([]byte(nil), raw...)
	}
	mutated.Files[GitHubWorkflowPath] = bytes.ReplaceAll(mutated.Files[GitHubWorkflowPath], []byte("Linux/aarch64"), []byte("Linux/x86_64"))
	mutated.WorkflowDigest = pipeline.FileDigest(mutated.Files[GitHubWorkflowPath])
	assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &mutated), e.CodeEngineCapability)
}

func TestGitHubNativePlatformAdmissionFailsBeforeOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		routes map[string]string
	}{{"missing ARM64", map[string]string{"linux/amd64": "ubuntu-24.04"}}, {"implicit foreign runner", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			plan := nativeGitHubTestPlan(t, map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm"})
			plan.Environment.GitHubRunners = tc.routes
			plan.EnvironmentDigest, _ = plan.Environment.Digest()
			bundle, err := RenderGitHub(t.Context(), plan)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if bundle != nil {
				t.Fatal("unsupported route emitted a partial bundle")
			}
		})
	}
	plan := nativeGitHubTestPlan(t, map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm"})
	for _, node := range plan.Graph.Nodes {
		if node.BuildTarget == "cli-arm64" && node.Kind == NodeBuild {
			assertFactoryErrorCode(t, admitGitHubNodePlatform(plan, node, "linux/amd64"), e.CodeEngineCapability)
			if err := admitGitHubNodePlatform(plan, node, "linux/arm64"); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertFactoryErrorCode(t, admitLocalPlanBase(t.Context(), plan), e.CodeEngineCapability)
}

func TestGitHubNativePreflightObservesKernelPlatform(t *testing.T) {
	plan := nativeGitHubTestPlan(t, map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm"})
	for _, tc := range []struct {
		name, machine string
		fail          bool
	}{{"native", "aarch64", false}, {"foreign", "x86_64", true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "uname"), []byte("#!/bin/sh\ncase \"$1\" in -s) printf 'Linux\\n';; -m) printf '"+tc.machine+"\\n';; esac\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", githubPlatformCheck(plan, "ubuntu-24.04-arm"))
			cmd.Env = []string{"PATH=" + dir}
			raw, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("preflight error %v: %s", err, raw)
			}
			if tc.fail {
				var exit *exec.ExitError
				want := e.ExitCode(e.New(e.CodeEngineCapability, "foreign runner"))
				if !errors.As(err, &exit) || exit.ExitCode() != want {
					t.Fatalf("preflight exit = %v, want %d", err, want)
				}
			}
			if tc.fail && !strings.Contains(string(raw), "NKD-E-ENGINE-CAPABILITY") {
				t.Fatalf("uncoded failure: %s", raw)
			}
		})
	}
}

func TestDeployCLIOutputsUseIndependentNativeBuilds(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	definition, err := authored.LoadDefinition(t.Context(), "definitions/nke-deploy/definition.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Outputs) != 8 {
		t.Fatalf("required outputs = %d, want eight", len(definition.Outputs))
	}
	base, err := catalog.LoadContext(t.Context(), catalog.Dir("definitions/nke-v1/catalog"))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := authored.LoadEnvironment(t.Context(), "definitions/nke-deploy/environment-github.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "nkectl-linux-" + arch
		var selected authored.DefinitionOutput
		for _, output := range definition.Outputs {
			if output.Name == name {
				selected = output
			}
		}
		if selected.Catalog == nil || selected.Catalog.Base != "../nke-v1/catalog" || selected.Platform != "linux/"+arch || selected.Format != authored.OutputExecutable {
			t.Fatalf("wrong CLI output: %+v", selected)
		}
		build, err := compileOutputBuild(selected, environment, stack)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, node := range build.Graph.Nodes {
			if node.Kind != ir.KindBuild {
				continue
			}
			found = true
			if node.Component != "nkectl" || node.Platform != "linux/"+arch || node.SourceCommit != "ed9fb09b04fd40bf331d25df2222206ffaca593e" {
				t.Fatalf("wrong CLI instruction: %+v", node)
			}
			if !strings.Contains(strings.Join(node.Command, " "), "-mod=vendor") {
				t.Fatal("CLI stopped building offline")
			}
			env := map[string]string{}
			for _, value := range node.Env {
				env[value.Name] = value.Value
			}
			if env["GOARCH"] != arch || env["GOOS"] != "linux" || env["CGO_ENABLED"] != "0" {
				t.Fatalf("wrong native CLI environment: %v", env)
			}
		}
		if !found {
			t.Fatal("CLI output has no producer")
		}
	}
}

func TestGitHubRoutesAdmittedBeforeCatalogRead(t *testing.T) {
	definition, environment := fixtureInputs("native", "cli", "")
	definition.Catalog.Base = filepath.Join(t.TempDir(), "missing-catalog")
	definition.Outputs[0].Platform = "linux/arm64"
	environment.GitHubRunners = map[string]string{"linux/amd64": "ubuntu-24.04"}
	plan, err := CompileDocuments(t.Context(), "definition.yaml", definition, "environment.yaml", environment, "v1.2.3 (deadbeef)")
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	if plan != nil {
		t.Fatal("missing platform route admitted a Plan")
	}
}

func TestExecuteNodeRejectsForeignNativeHelper(t *testing.T) {
	plan := nativeGitHubTestPlan(t, map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm"})
	foreign := "linux/arm64"
	if runtime.GOOS+"/"+runtime.GOARCH == foreign {
		foreign = "linux/amd64"
	}
	t.Chdir(t.TempDir())
	for _, node := range plan.Graph.Nodes {
		execution, err := plan.GitHubExecutionFor(node)
		if err != nil {
			t.Fatal(err)
		}
		if node.Kind == NodeBuild && execution.Platform == foreign {
			assertFactoryErrorCode(t, ExecuteNode(t.Context(), plan, node.ID, nil, nil), e.CodeEngineCapability)
			entries, err := os.ReadDir(".")
			if err != nil || len(entries) != 0 {
				t.Fatalf("foreign helper created output: %v %v", entries, err)
			}
			return
		}
	}
	t.Fatal("no foreign-platform producer")
}

func TestGitHubFinalizerUsesUtilityPlatform(t *testing.T) {
	plan := nativeGitHubTestPlan(t, map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-22.04-arm"})
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	job := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath]).Jobs["nkd-lifecycle-run"]
	if job.RunsOn != "ubuntu-24.04" || !stepDownloadsArtifact(plan, job.Steps, "nkd-exec-"+shortStableName("ubuntu-24.04")) {
		t.Fatal("finalizer did not use the exact utility runner/helper")
	}
}
