// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"bytes"
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	yaml "go.yaml.in/yaml/v3"
)

func generationFixture(t *testing.T) (*factory.Plan, lifecycle.QualificationPolicy) {
	t.Helper()
	plan, err := factory.LoadPlanContext(t.Context(), "../../../.github/nvkr-run/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	policy := workerPolicyFixture()
	policy.Source.Repository = "dsx-ai-factory/nvkr"
	policy.Source.Workflow = factory.GitHubWorkflowPath
	policy.Cosign = lifecycle.QualificationCosignPolicy{Version: "v3.0.2", PlatformDigests: map[string]string{"linux/amd64": "sha256:" + strings.Repeat("a", 64), "linux/arm64": "sha256:" + strings.Repeat("b", 64)}}
	policy.Smoke.Images = map[string]string{"linux/amd64": "ghcr.io/dsx-ai-factory/smoke@sha256:" + strings.Repeat("c", 64), "linux/arm64": "ghcr.io/dsx-ai-factory/smoke@sha256:" + strings.Repeat("d", 64)}
	// Synthetic independent trust pins are test-only. Scope deliberately names
	// the real maintained Plan's exact members; no payload or authority is faked.
	policy.Smoke.Tools = map[string]string{"nvkectl": "nvkectl-version-json", "helm": "helm-version", "kubectl": "kubectl-client-version-json"}
	for _, output := range plan.LifecycleOutputs {
		if output.Kind == lifecycle.OutputOCI {
			policy.Smoke.NonToolOutputs = append(policy.Smoke.NonToolOutputs, lifecycle.QualificationNonToolOutput{Output: output.Name, Kind: output.Kind, Components: append([]string(nil), output.Components...), Reason: "deployment-image"})
		}
	}
	if len(policy.Smoke.NonToolOutputs) != 25 {
		t.Fatal("maintained candidate fixture changed its reviewed OCI scope")
	}
	return plan, policy
}
func generationBundle(t *testing.T) (*GeneratedWorkflows, lifecycle.QualificationPolicy) {
	t.Helper()
	plan, policy := generationFixture(t)
	bundle, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return bundle, policy
}

type qualificationWorkflowDocument struct {
	Name        string                              `yaml:"name"`
	On          map[string]any                      `yaml:"on"`
	Permissions map[string]string                   `yaml:"permissions"`
	Jobs        map[string]qualificationWorkflowJob `yaml:"jobs"`
}
type qualificationWorkflowJob struct {
	If          string                      `yaml:"if"`
	Name        string                      `yaml:"name"`
	Uses        string                      `yaml:"uses"`
	Needs       []string                    `yaml:"needs"`
	Permissions map[string]string           `yaml:"permissions"`
	Secrets     map[string]string           `yaml:"secrets"`
	Env         map[string]string           `yaml:"env"`
	RunsOn      string                      `yaml:"runs-on"`
	Timeout     int                         `yaml:"timeout-minutes"`
	Steps       []qualificationWorkflowStep `yaml:"steps"`
}
type qualificationWorkflowStep struct {
	If              string            `yaml:"if"`
	ContinueOnError bool              `yaml:"continue-on-error"`
	Name            string            `yaml:"name"`
	Uses            string            `yaml:"uses"`
	Run             string            `yaml:"run"`
	Env             map[string]string `yaml:"env"`
	With            map[string]string `yaml:"with"`
}

func decodeQualificationWorkflow(t *testing.T, raw []byte) qualificationWorkflowDocument {
	t.Helper()
	var result qualificationWorkflowDocument
	if err := yaml.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestQualificationGenerationWorkflowContract(t *testing.T) {
	plan, policy := generationFixture(t)
	bundle, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	again, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
	if err != nil || !reflect.DeepEqual(bundle.Files(), again.Files()) {
		t.Fatalf("nondeterministic generation: %v", err)
	}
	t.Logf("rendered qualifier=%d bytes worker=%d bytes", len(bundle.files[policy.QualificationWorkflow]), len(bundle.files[policy.Smoke.Workflow]))
	top := decodeQualificationWorkflow(t, bundle.files[policy.QualificationWorkflow])
	worker := decodeQualificationWorkflow(t, bundle.files[policy.Smoke.Workflow])
	if len(top.Jobs) != 2 || len(worker.Jobs) != 1 {
		t.Fatal("unexpected job set")
	}
	call := top.Jobs["native-smoke"]
	native := worker.Jobs["native"]
	final := top.Jobs["qualify"]
	if call.Name != "native-smoke" || call.Uses != "./"+policy.Smoke.Workflow || native.Name != "native-${{ matrix.arch }}" || native.RunsOn != "${{ matrix.runner }}" {
		t.Fatal("exact authenticated job or same-commit reusable identity changed")
	}
	if !reflect.DeepEqual(final.Needs, []string{"native-smoke"}) || final.Permissions["packages"] != "write" || call.Permissions["packages"] != "read" || native.Permissions["packages"] != "read" {
		t.Fatal("final publication and read-only worker permissions are not separated")
	}
	if call.Secrets["NKE_SOURCE_TOKEN"] != "${{ secrets.NKE_SOURCE_TOKEN }}" || len(call.Secrets) != 1 {
		t.Fatalf("unexpected explicit source secrets: %v", call.Secrets)
	}
	for _, job := range []qualificationWorkflowJob{native, final} {
		if len(job.Env) != 0 || job.Timeout != 30 {
			t.Fatal("ambient job environment or missing timeout")
		}
		scoped := 0
		checkouts := 0
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/checkout@") {
				checkouts++
				if step.With["ref"] != "${{ github.workflow_sha }}" || step.With["persist-credentials"] != "false" {
					t.Fatal("candidate checkout or persisted checkout credential")
				}
			}
			if _, ok := step.Env["NVKR_PIN_SOURCE_TOKEN_0"]; ok {
				scoped++
				if !strings.Contains(step.Run, "./nvkr-exec run ") || !strings.Contains(step.Run, "--policy 'qualification.yaml'") {
					t.Fatal("source credential escaped owner invocation")
				}
				if step.Env["GIT_CONFIG_GLOBAL"] != "/dev/null" || step.Env["GIT_CONFIG_NOSYSTEM"] != "1" {
					t.Fatal("ambient Git config allowed")
				}
			}
		}
		if scoped != 1 || checkouts != 1 {
			t.Fatal("incorrect credential or checkout scope")
		}
	}
	for _, step := range native.Steps {
		for key := range step.Env {
			if key == "NVKR_REGISTRY" || key == "NVKR_REGISTRY_USER" || key == "NVKR_REGISTRY_PASSWORD" {
				t.Fatal("publication credentials reached smoke worker")
			}
		}
	}
	all := string(bundle.files[policy.QualificationWorkflow]) + string(bundle.files[policy.Smoke.Workflow])
	for _, forbidden := range []string{"secrets: inherit", "refs/pull/", "ref: ${{ github.event.workflow_run.head_sha }}", "--run ", "--results ", "@main", "@v4"} {
		if strings.Contains(all, forbidden) {
			t.Fatalf("unexpected generated surface %q", forbidden)
		}
	}
	for _, required := range []string{"test \"$GITHUB_WORKFLOW_SHA\" = \"$GITHUB_SHA\"", "ubuntu-24.04-arm", "sha256sum --check --strict", "https://github.com/sigstore/cosign/releases/download/$COSIGN_VERSION/cosign-linux-$COSIGN_ARCH", "nvkr-smoke-${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.arch }}", "include-hidden-files: true", "if-no-files-found: error"} {
		if !strings.Contains(all, required) {
			t.Fatalf("missing fixed contract %q", required)
		}
	}
	upload := native.Steps[len(native.Steps)-1]
	if upload.With["path"] != ".nvkr/smoke-worker/evidence/worker.json\n.nvkr/smoke-worker/evidence/worker.sig\n.nvkr/smoke-worker/evidence/worker.pem\n.nvkr/smoke-worker/evidence/worker.bundle.json\n" {
		t.Fatal("worker artifact membership changed")
	}
	for name, raw := range bundle.Files() {
		raw[0] = '!'
		if bundle.files[name][0] == '!' {
			t.Fatal("rendered file alias escaped")
		}
	}
}

func TestQualificationTriggerExcludesNamedProducerOfSameDefinition(t *testing.T) {
	plan, policy := generationFixture(t)
	qualifier, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	top := decodeQualificationWorkflow(t, qualifier.files[policy.QualificationWorkflow])
	trigger, ok := top.On["workflow_run"].(map[string]any)
	if !ok {
		t.Fatal("qualifier omits the workflow_run trigger")
	}
	names, ok := trigger["workflows"].([]any)
	if !ok || len(names) != 1 || names[0] != "nvkr-run-"+plan.Name {
		t.Fatalf("qualifier changed its sole default producer selection: %v", trigger)
	}
	for _, name := range []string{plan.Name, "other-project"} {
		t.Run(name, func(t *testing.T) {
			named := *plan
			named.Environment.GitHubBundle = name
			var digestErr error
			named.EnvironmentDigest, digestErr = named.Environment.Digest()
			if digestErr != nil {
				t.Fatal(digestErr)
			}
			producer, renderErr := factory.RenderGitHub(t.Context(), &named)
			if renderErr != nil {
				t.Fatal(renderErr)
			}
			workflow := decodeQualificationWorkflow(t, producer.Files[producer.WorkflowPath])
			if workflow.Name != "nvkr-project-"+name || workflow.Name == names[0] {
				t.Fatalf("named producer can trigger the default qualifier: %q", workflow.Name)
			}
		})
	}
}

// The explicit status function defeats GitHub's implicit success() dependency
// gate. The exact allowlist excludes skipped, canceled and unknown worker states;
// repeating every source guard keeps failure diagnostics inside the trusted trigger.
func TestQualificationGenerationFinalReportsWorkerFailure(t *testing.T) {
	bundle, _ := generationBundle(t)
	top := decodeQualificationWorkflow(t, bundle.files[bundle.manifest.QualificationWorkflow])
	source := "github.event_name == 'workflow_run' && github.event.workflow_run.conclusion == 'success' && github.repository == 'dsx-ai-factory/nvkr' && github.event.workflow_run.head_branch == 'main' && github.event.workflow_run.repository.full_name == github.repository && github.event.workflow_run.head_repository.full_name == github.repository"
	for _, tc := range []struct {
		job  string
		want string
	}{
		{"native-smoke", "${{ " + source + " }}"},
		{"qualify", "${{ !cancelled() && (needs.native-smoke.result == 'success' || needs.native-smoke.result == 'failure') && " + source + " }}"}, //nolint:misspell // GitHub status function uses this exact spelling.
	} {
		t.Run(tc.job, func(t *testing.T) {
			if got := top.Jobs[tc.job].If; got != tc.want {
				t.Fatalf("trusted source and worker-result admission changed:\nwant %s\n got %s", tc.want, got)
			}
		})
	}
	worker := decodeQualificationWorkflow(t, bundle.files[bundle.manifest.SmokeWorkflow])
	for _, step := range worker.Jobs["native"].Steps {
		if step.If != "" || step.ContinueOnError {
			t.Fatalf("worker step %q no longer stops on failure", step.Name)
		}
	}
}

func TestQualificationGenerationAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*factory.Plan, *lifecycle.QualificationPolicy, *string)
		code e.Code
	}{
		{"valid", func(*factory.Plan, *lifecycle.QualificationPolicy, *string) {}, ""},
		{"missing version", func(_ *factory.Plan, p *lifecycle.QualificationPolicy, _ *string) { p.Cosign.Version = "" }, e.CodeUnpinnedInput},
		{"mutable version", func(_ *factory.Plan, p *lifecycle.QualificationPolicy, _ *string) { p.Cosign.Version = "latest" }, e.CodeUnpinnedInput},
		{"missing selected architecture", func(_ *factory.Plan, p *lifecycle.QualificationPolicy, _ *string) {
			delete(p.Smoke.Images, "linux/arm64")
		}, e.CodeReleaseIncomplete},
		{"missing tool", func(_ *factory.Plan, p *lifecycle.QualificationPolicy, _ *string) { delete(p.Smoke.Tools, "kubectl") }, e.CodeGateUnmet},
		{"extra tool", func(_ *factory.Plan, p *lifecycle.QualificationPolicy, _ *string) {
			p.Smoke.Tools["unselected"] = "helm-version"
		}, e.CodeReleaseIncomplete},
		{"missing image exemption", func(_ *factory.Plan, p *lifecycle.QualificationPolicy, _ *string) {
			p.Smoke.NonToolOutputs = p.Smoke.NonToolOutputs[1:]
		}, e.CodeGateUnmet},
		{"wrong image membership", func(_ *factory.Plan, p *lifecycle.QualificationPolicy, _ *string) {
			p.Smoke.NonToolOutputs[0].Components = []string{"unselected"}
		}, e.CodeGateUnmet},
		{"extra image exemption", func(_ *factory.Plan, p *lifecycle.QualificationPolicy, _ *string) {
			p.Smoke.NonToolOutputs = append(p.Smoke.NonToolOutputs, lifecycle.QualificationNonToolOutput{Output: "unselected", Kind: lifecycle.OutputOCI, Components: []string{"image"}, Reason: "deployment-image"})
		}, e.CodeReleaseIncomplete},
		{"producer path", func(_ *factory.Plan, p *lifecycle.QualificationPolicy, _ *string) {
			p.Source.Workflow = ".github/workflows/other.yml"
		}, e.CodeEngineCapability},
		{"other namespace", func(_ *factory.Plan, p *lifecycle.QualificationPolicy, _ *string) { p.Source.Repository = "other/nvkr" }, e.CodeEngineCapability},
		{"policy traversal", func(_ *factory.Plan, _ *lifecycle.QualificationPolicy, p *string) { *p = "../policy.yaml" }, e.CodeUsage},
		{"policy absolute", func(_ *factory.Plan, _ *lifecycle.QualificationPolicy, p *string) { *p = "/policy.yaml" }, e.CodeUsage},
		{"policy overlap", func(_ *factory.Plan, p *lifecycle.QualificationPolicy, path *string) { *path = p.Smoke.Workflow }, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, policy := generationFixture(t)
			name := "qualification.yaml"
			tc.edit(plan, &policy, &name)
			bundle, err := RenderWorkflows(t.Context(), plan, policy, name)
			if tc.code == "" {
				if err != nil || bundle == nil {
					t.Fatal(err)
				}
			} else {
				requireCode(t, err, tc.code)
				if bundle != nil {
					t.Fatal("invalid generation emitted bytes")
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := GenerateFiles(ctx, "missing", "missing", "missing", "invalid", "github", "missing", false)
	requireCode(t, err, e.CodeTimeout)
}

func generationOperatorToolsFixture(t *testing.T, publicationMode string) (*factory.Plan, lifecycle.QualificationPolicy) {
	t.Helper()
	maintained, policy := generationFixture(t)
	var selected factory.BuildPlan
	for _, build := range maintained.Builds {
		if build.Name == "operator-tools" {
			selected = build
		}
	}
	if selected.CatalogSnapshot == nil {
		t.Fatal("missing maintained operator-tools catalog")
	}
	inputs := maintained.Definition.Clone()
	for _, output := range inputs.Outputs {
		if output.Name == "operator-tools" {
			output.Catalog = nil
			inputs.Outputs = []lifecycle.DefinitionOutput{output}
			break
		}
	}
	base := *selected.CatalogSnapshot
	base.Tools = maps.Clone(base.Tools)
	for name, pin := range maintained.CatalogSnapshot.Tools {
		base.Tools[name] = pin
	}
	base.Actions = maps.Clone(maintained.Actions)
	stack, err := catalog.ApplyStack(&base)
	if err != nil {
		t.Fatal(err)
	}
	environment := maintained.Environment.Clone()
	environment.Publication.Mode = publicationMode
	// This selected fixture only acquires prebuilt tools, so no source fetch credential is applicable.
	environment.SourceAccess = nil
	plan, err := factory.Compile(t.Context(), inputs, environment, stack, maintained.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	policy.Smoke.Tools = map[string]string{"helm": "helm-version", "kubectl": "kubectl-client-version-json"}
	policy.Smoke.NonToolOutputs = nil
	delete(policy.Smoke.Images, "linux/arm64")
	delete(policy.Cosign.PlatformDigests, "linux/arm64")
	return plan, policy
}

func TestQualificationGenerationRequiresPerOutputHandoff(t *testing.T) {
	for _, tc := range []struct {
		name, publicationMode string
		code                  e.Code
	}{
		{"legacy aggregate", "", e.CodeEngineCapability},
		{"per-output publication", "per-output-v1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Compile each supported producer mode from its authored inputs. The
			// legacy Plan is valid for production; only qualification is unsupported.
			plan, policy := generationOperatorToolsFixture(t, tc.publicationMode)
			if err := plan.Validate(); err != nil {
				t.Fatalf("producer Plan is invalid: %v", err)
			}
			bundle, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
			if tc.code != "" {
				requireCode(t, err, tc.code)
				if bundle != nil {
					t.Fatal("unsupported qualification handoff emitted workflow bytes")
				}
				return
			}
			if err != nil || bundle == nil || len(bundle.Files()) != 2 {
				t.Fatalf("supported per-output Plan did not emit the workflow pair: %v", err)
			}
		})
	}
}

func TestQualificationGenerationSelectsExactWorkerPlatforms(t *testing.T) {
	plan, policy := generationOperatorToolsFixture(t, "per-output-v1")
	bundle, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	worker := string(bundle.files[policy.Smoke.Workflow])
	if !strings.Contains(worker, "- arch: amd64") || strings.Contains(worker, "- arch: arm64") || strings.Contains(worker, "ubuntu-24.04-arm") {
		t.Fatal("worker matrix is not the exact single-platform scope")
	}
	// An otherwise pinned extra architecture cannot silently become an unused job.
	policy.Cosign.PlatformDigests["linux/arm64"] = "sha256:" + strings.Repeat("b", 64)
	policy.Smoke.Images["linux/arm64"] = "ghcr.io/dsx-ai-factory/smoke@sha256:" + strings.Repeat("d", 64)
	rejected, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
	requireCode(t, err, e.CodeReleaseIncomplete)
	if rejected != nil {
		t.Fatal("incompatible policy emitted workflow bytes")
	}
}

func TestQualificationGeneratedYAMLLints(t *testing.T) {
	bundle, _ := generationBundle(t)
	root := t.TempDir()
	if err := WriteWorkflows(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
	names := qualificationNames(bundle.files)
	for _, tool := range []string{"actionlint", "yamllint"} {
		t.Run(tool, func(t *testing.T) {
			binary, err := exec.LookPath(tool)
			if err != nil {
				t.Skip(tool + " is unavailable")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			args := append([]string{}, names...)
			if tool == "actionlint" {
				args = append([]string{"-color"}, args...)
			} else {
				args = append([]string{"-d", "{extends: default, rules: {line-length: disable, document-start: disable, truthy: disable}}"}, args...)
			}
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Dir = root
			raw, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s: %v\n%s", tool, err, raw)
			}
		})
	}
}

func TestQualificationSourceCredentialContext(t *testing.T) {
	plan, _ := generationFixture(t)
	auth, err := qualificationSourceAuthentication(plan)
	if err != nil {
		t.Fatal(err)
	}
	// Ambient credentials must never fill an unrelated repository, including a
	// different repository on the same origin and a textual prefix collision.
	for _, tc := range []struct {
		url     string
		allowed bool
	}{
		{"https://github.com/dsx-ai-factory/nvke", true},
		{"https://github.com/dsx-ai-factory/nvke-other", false},
		{"https://github.com/dsx-ai-factory/other", false},
		{"https://other.example/dsx-ai-factory/nvke", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "git", "credential", "fill")
			cmd.Dir = t.TempDir()
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "LC_ALL=C"}
			for key, value := range auth.environment {
				if strings.HasPrefix(key, "NVKR_PIN_SOURCE_TOKEN_") {
					value = "synthetic-scoped-token"
				}
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			cmd.Stdin = strings.NewReader("url=" + tc.url + "\n\n")
			raw, err := cmd.CombinedOutput()
			if tc.allowed {
				if err != nil || !bytes.Contains(raw, []byte("password=synthetic-scoped-token")) {
					t.Fatalf("selected credential context failed: %v", err)
				}
			} else if err == nil || bytes.Contains(raw, []byte("synthetic-scoped-token")) {
				t.Fatal("credential escaped selected repository context")
			}
		})
	}
	// The pure factory projection must be byte-for-byte identical to the worker's
	// environment, with no candidate environment input or reconstruction.
	expected, err := factory.GitHubCatalogSourceEnvironment(plan)
	if err != nil || !reflect.DeepEqual(expected, auth.environment) {
		t.Fatal("factory source environment drift")
	}
}

func writeQualificationTestFile(t *testing.T, root, name string, raw []byte) {
	t.Helper()
	full := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Producer namespaces are valid ordinary generation inputs, while qualification
// currently owns only the default producer pair. Reject before either owner writes.
func TestQualificationGenerationNamedProducerRefusesWithoutOutput(t *testing.T) {
	plan, policy := generationFixture(t)
	plan.Environment = plan.Environment.Clone()
	plan.Environment.GitHubBundle = "nightly"
	var err error
	plan.EnvironmentDigest, err = plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("named producer Plan must remain valid: %v", err)
	}
	bundle, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
	requireCode(t, err, e.CodeEngineCapability)
	if bundle != nil {
		t.Fatal("unsupported named qualification emitted a bundle")
	}
	inputs := t.TempDir()
	environment := plan.Environment.Clone()
	environmentPath := filepath.Join(inputs, "environment.yaml")
	for name, value := range map[string]any{"environment.yaml": environment, "qualification.yaml": policy} {
		raw, err := yaml.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(inputs, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(inputs)
	for _, check := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "check"}[check], func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "uncreated")
			digest, err := GenerateFiles(t.Context(), "unavailable-definition.yaml", environmentPath, "qualification.yaml", plan.GeneratorVersion, "github", output, check)
			requireCode(t, err, e.CodeEngineCapability)
			if digest != "" {
				t.Fatal("unsupported qualification returned a Plan digest", digest)
			}
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatal("unsupported qualification created output or ownership manifests", err)
			}
		})
	}
}
