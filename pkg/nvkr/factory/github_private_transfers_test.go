// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

func TestGitHubPrivateBootstrapOwnsAndDiscardsItsCaches(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 is required to exercise the hosted bootstrap owner")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash is required to exercise the hosted bootstrap owner")
	}
	for _, test := range []struct {
		name string
		code e.Code
	}{
		{name: "success"},
		{name: "build failure", code: e.CodeBuildFailed},
		{name: "cancellation", code: e.CodeTimeout},
		{name: "missing runner temporary directory", code: e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, runnerTemp, ambient := t.TempDir(), t.TempDir(), t.TempDir()
			marker := filepath.Join(ambient, "keep")
			if err := os.WriteFile(marker, []byte("ambient cache"), 0o600); err != nil {
				t.Fatal(err)
			}
			// Isolated Python must not import workspace or PYTHONPATH modules.
			if err := os.WriteFile(filepath.Join(root, "tempfile.py"), []byte("raise RuntimeError('workspace import')\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			fakeGo := "#!/bin/sh\nexec " + shellQuote(python) + " -I - \"$@\" <<'PY'\n" + `import json, os, pathlib, signal, sys, time
for key in ("NVKR_CREDENTIAL", "NVKR_CI_STORAGE_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "GOPROXY"):
    assert key not in os.environ
assert os.environ["GOTOOLCHAIN"] == "local" and os.environ["GOENV"] == "off"
assert os.environ["CGO_ENABLED"] == "0"
build, modules = pathlib.Path(os.environ["GOCACHE"]), pathlib.Path(os.environ["GOMODCACHE"])
assert build.parent == modules.parent
assert build.parent.stat().st_mode & 0o077 == 0
for directory in (build, modules / "read-only-module"):
    directory.mkdir(parents=True)
    (directory / "cached").write_text("disposable")
    directory.chmod(0o500)
pathlib.Path("bootstrap-observation.json").write_text(json.dumps({"pid": os.getpid(), "cache": str(build.parent), "args": sys.argv[1:]}))
pathlib.Path(".nvkr/ci-bootstrap/nvkr-exec").write_text("compiled helper")
mode = ` + strconv.Quote(test.name) + `
if mode == "cancellation":
    os.kill(os.getppid(), signal.SIGTERM)
    time.sleep(30)
sys.exit(7 if mode == "build failure" else 0)
PY
`
			if err := os.WriteFile(filepath.Join(root, "go"), []byte(fakeGo), 0o700); err != nil {
				t.Fatal(err)
			}
			selectedTemp := runnerTemp
			if test.code == e.CodeEngineCapability {
				selectedTemp = ""
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, bash, "-c", githubCIBootstrapBuildCommand("-X main.version=fixture"))
			command.Dir = root
			command.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"),
				"RUNNER_TEMP="+selectedTemp, "GOCACHE="+ambient, "GOMODCACHE="+ambient,
				"PYTHONPATH="+root, "NVKR_CREDENTIAL=must-not-inherit", "NVKR_CI_STORAGE_TOKEN=must-not-inherit",
				"ACTIONS_ID_TOKEN_REQUEST_TOKEN=must-not-inherit", "GOPROXY=must-not-inherit")
			output, runErr := command.CombinedOutput()
			if test.code == "" {
				if runErr != nil {
					t.Fatalf("bootstrap failed: %v\n%s", runErr, output)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(runErr, &exit) || exit.ExitCode() != e.ExitCode(e.New(test.code, "")) || !bytes.Contains(output, []byte(test.code)) {
					t.Fatalf("bootstrap failure lost its code %s: %v\n%s", test.code, runErr, output)
				}
			}
			if bytes.Contains(output, []byte("must-not-inherit")) {
				t.Fatal("bootstrap diagnostic exposed an inherited credential")
			}
			entries, err := os.ReadDir(runnerTemp)
			if err != nil || len(entries) != 0 {
				t.Fatalf("bootstrap left disposable caches behind: %v / %v", entries, err)
			}
			if contents, err := os.ReadFile(marker); err != nil || string(contents) != "ambient cache" {
				t.Fatal("bootstrap changed an ambient cache")
			}
			if test.code == e.CodeEngineCapability {
				if _, err := os.Stat(filepath.Join(root, ".nvkr")); !os.IsNotExist(err) {
					t.Fatal("missing capability reached workspace setup")
				}
				return
			}
			var observed struct {
				PID   int      `json:"pid"`
				Cache string   `json:"cache"`
				Args  []string `json:"args"`
			}
			raw, err := os.ReadFile(filepath.Join(root, "bootstrap-observation.json"))
			if err != nil || json.Unmarshal(raw, &observed) != nil {
				t.Fatalf("missing bootstrap process observation: %v", err)
			}
			if filepath.Dir(observed.Cache) != runnerTemp || !strings.HasPrefix(filepath.Base(observed.Cache), "nvkr-ci-bootstrap-") ||
				strings.Join(observed.Args, "|") != "build|-trimpath|-ldflags|-X main.version=fixture|-o|.nvkr/ci-bootstrap/nvkr-exec|./cmd/nvkr" {
				t.Fatal("bootstrap escaped its owned caches or fixed build arguments")
			}
			process, err := os.FindProcess(observed.PID)
			if err != nil {
				t.Fatal(err)
			}
			if err := process.Signal(syscall.Signal(0)); err == nil {
				t.Fatal("bootstrap compiler survived cache cleanup")
			}
			if contents, err := os.ReadFile(filepath.Join(root, ".nvkr/ci-bootstrap/nvkr-exec")); err != nil || string(contents) != "compiled helper" {
				t.Fatal("cache cleanup removed the compiled helper")
			}
		})
	}
}

func TestGitHubPrivateTransfersExposeOnlyLocatorsToActions(t *testing.T) {
	for _, fixture := range []struct {
		name string
		plan func(*testing.T) *Plan
	}{
		{"production", githubTestPlan}, {"development", githubDevelopmentTestPlan},
		{"signed development", githubDevelopmentPublicationTestPlan},
		{"runtime Plan", githubRuntimePlanFixture},
		{"ARC ARM", func(t *testing.T) *Plan { return arcTestPlan(t, "linux/arm64") }},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			plan := fixture.plan(t)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			actions := testActionRefs(t, plan)
			documents, err := githubCIActionDocuments(plan, bundle.Files[bundle.WorkflowPath], bundle.Files)
			if err != nil {
				t.Fatal(err)
			}
			uploads, downloads, builds := 0, 0, 0
			for name, document := range documents {
				if name != bundle.WorkflowPath && githubYAMLContains(document, "${{ secrets.") {
					t.Fatalf("composite %s reaches a caller-only secrets context", name)
				}
				if err := githubCIWalkSteps(document, func(step *yamlv3.Node) error {
					uses := githubYAMLValue(step, "uses")
					with := githubYAMLField(step, "with")
					switch uses {
					case actions.Upload:
						uploads++
						if name != githubCITransferPath(plan, "upload")+"/action.yml" || githubYAMLValue(with, "path") != "${{ steps.store.outputs.locator_path }}" || githubYAMLValue(step, "if") != githubCIIndexCondition {
							t.Fatalf("provider upload in %s can expose bytes or fall back when private storage refuses", name)
						}
					case actions.Download:
						t.Fatalf("unbounded provider download action remains in %s", name)
					case actions.SetupGo:
						if name == githubCITransferPath(plan, "bootstrap-download")+"/action.yml" {
							builds++
							if githubYAMLValue(with, "cache") != "false" || githubYAMLValue(with, "go-version-file") != ".go-version" {
								t.Fatal("private helper bootstrap does not provision the pinned toolchain")
							}
						}
					}

					return nil
				}); err != nil {
					t.Fatal(err)
				}
				// Only the two fixed transfer shell commands receive storage
				// credentials; producer/source/build commands retain their scopes.
				var visit func(*yamlv3.Node)
				visit = func(node *yamlv3.Node) {
					if node == nil {
						return
					}
					if strings.Contains(githubYAMLValue(node, "run"), " factory github ci-fetch-locators ") {
						downloads++
						if name != githubCITransferPath(plan, "download")+"/action.yml" && name != githubCITransferPath(plan, "bootstrap-download")+"/action.yml" {
							t.Fatalf("provider payload download remains in %s", name)
						}
						env := githubYAMLField(node, "env")
						if githubYAMLValue(env, "NVKR_CI_INDEX") != "${{ steps.directory.outputs.path }}" {
							t.Fatal("locator download does not use a fresh directory")
						}
						if githubYAMLValue(env, "NVKR_CI_REPOSITORY_TOKEN") != "${{ github.token }}" || githubYAMLField(env, "NVKR_CI_STORAGE_TOKEN") != nil {
							t.Fatal("locator acquisition must use only the repository credential")
						}
					}
					if env := githubYAMLField(node, "env"); env != nil && githubYAMLField(env, "NVKR_CI_STORAGE_TOKEN") != nil {
						run := githubYAMLValue(node, "run")
						if (!strings.Contains(run, " factory github ci-upload ") && !strings.Contains(run, " factory github ci-download ")) || (githubYAMLValue(env, "NVKR_CI_STORAGE_TOKEN") != githubCICredential(githubCIStorageInput, false) && githubYAMLValue(env, "NVKR_CI_STORAGE_TOKEN") != githubCICredential(githubCIReadInput, false)) {
							t.Fatalf("storage token escapes fixed transfer commands in %s", name)
						}
					}
					for _, child := range node.Content {
						visit(child)
					}
				}
				visit(document)
			}
			if uploads != 1 || downloads != 2 || builds != 1 {
				t.Fatalf("closed transport template membership changed: uploads=%d downloads=%d bootstraps=%d", uploads, downloads, builds)
			}
			actual := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath])
			basePermissions, err := githubCIBasePermissions(plan)
			if err != nil {
				t.Fatal(err)
			}
			for id, job := range actual.Jobs {
				transfer, err := githubCIJobTransfers(plan, githubTestYAMLField(t, documents[bundle.WorkflowPath].Content[0], "jobs", id), documents, map[string]bool{})
				if err != nil {
					t.Fatal(err)
				}
				if !maps.Equal(job.Permissions, githubCITransferPermissions(basePermissions[id], transfer)) {
					t.Fatalf("job %s elevated repository token permissions for transport", id)
				}
			}
			if bytes.Contains(bundle.Files[bundle.WorkflowPath], []byte("|| github.token")) {
				t.Fatal("private transport falls back to repository credentials")
			}
			if err := ValidateGitHubBundle(t.Context(), plan, bundle); err != nil {
				t.Fatal("private projection changed logical graph authority", err)
			}
		})
	}
}

func TestGitHubPrivateUploadsPreserveCancellationAndFailureOrdering(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := githubCIActionDocuments(plan, bundle.Files[bundle.WorkflowPath], bundle.Files)
	if err != nil {
		t.Fatal(err)
	}
	template := documents[githubCITransferPath(plan, "upload")+"/action.yml"].Content[0]
	if githubYAMLValue(githubTestYAMLField(t, template, "inputs", githubCIAlwaysInput), "default") != "false" {
		t.Fatal("ordinary private uploads must remain cancellable by default")
	}
	steps := githubTestYAMLField(t, template, "runs", "steps")
	for _, stage := range []struct{ id, condition string }{
		{"directory", githubCIUploadCondition},
		{"store", githubCIStoreCondition},
		{"index", githubCIIndexCondition},
	} {
		step := githubTestYAMLStep(t, steps, "id", stage.id)
		if githubYAMLValue(step, "if") != stage.condition {
			t.Fatalf("private upload %s lost cancellation or predecessor-success admission", stage.id)
		}
	}
	ordinary, always := 0, 0
	for name, document := range documents {
		if name == githubCITransferPath(plan, "upload")+"/action.yml" {
			continue
		}
		if err := githubCIWalkSteps(document, func(step *yamlv3.Node) error {
			if githubCITransferMode(plan, githubYAMLValue(step, "uses")) != "upload" {
				return nil
			}
			flag := githubYAMLValue(githubYAMLField(step, "with"), githubCIAlwaysInput)
			if githubYAMLValue(step, "if") == githubAlwaysCondition {
				always++
				if flag != "true" {
					t.Fatalf("always-run upload in %s can be skipped during cancellation", name)
				}
			} else {
				ordinary++
				if flag != "" {
					t.Fatalf("ordinary upload in %s became uncancellable", name)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if ordinary == 0 || always == 0 {
		t.Fatal("fixture must exercise ordinary and always-run private uploads")
	}
	_, bootstrapMember := githubTestBootstrap(t, bundle)
	for _, mutation := range []struct {
		name, member string
		edit         func(*yamlv3.Node)
	}{
		{"ordinary upload made uncancellable", bootstrapMember, func(root *yamlv3.Node) {
			steps := githubTestYAMLField(t, root, "runs", "steps")
			step := githubTestYAMLStep(t, steps, "uses", "./"+githubCITransferPath(plan, "upload"))
			githubYAMLSet(githubYAMLField(step, "with"), githubCIAlwaysInput, githubYAMLString("true"))
		}},
		{"missing always binding", bundle.WorkflowPath, func(root *yamlv3.Node) {
			steps := githubTestYAMLField(t, root, "jobs", githubFinalizerJobID, "steps")
			step := githubTestYAMLStep(t, steps, "id", "upload-run-record")
			githubYAMLRemove(githubYAMLField(step, "with"), githubCIAlwaysInput)
		}},
		{"arbitrary always expression", bundle.WorkflowPath, func(root *yamlv3.Node) {
			steps := githubTestYAMLField(t, root, "jobs", githubFinalizerJobID, "steps")
			step := githubTestYAMLStep(t, steps, "id", "upload-run-record")
			githubYAMLSet(githubYAMLField(step, "with"), githubCIAlwaysInput, githubYAMLString("${{ github.event.inputs.always }}"))
		}},
		{"lost directory cancellation", githubCITransferPath(plan, "upload") + "/action.yml", func(root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "directory")
			githubYAMLRemove(step, "if")
		}},
		{"store after directory failure", githubCITransferPath(plan, "upload") + "/action.yml", func(root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "store")
			githubYAMLSet(step, "if", githubYAMLString(githubAlwaysCondition))
		}},
		{"locator after storage failure", githubCITransferPath(plan, "upload") + "/action.yml", func(root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "index")
			githubYAMLSet(step, "if", githubYAMLString(githubAlwaysCondition))
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := mutateGitHubBundleYAML(t, bundle, mutation.member, mutation.edit)
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), e.CodeEngineCapability)
		})
	}
}

func TestGitHubPrivateTransfersRejectPayloadFallbackAndCredentialTampering(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, _ := githubTestBootstrap(t, bundle)
	for _, test := range []struct {
		name, member string
		mutate       func(*yamlv3.Node)
	}{
		{"upload original bytes", githubCITransferPath(plan, "upload") + "/action.yml", func(root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "index")
			githubTestYAMLField(t, step, "with", "path").Value = "${{ inputs.path }}"
		}},
		{"public fallback", githubCITransferPath(plan, "upload") + "/action.yml", func(root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "store")
			githubTestYAMLField(t, step, "run").Value += " || true"
		}},
		{"cache bootstrap", githubCITransferPath(plan, "bootstrap-download") + "/action.yml", func(root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "uses", plan.Actions["setup-go"].Uses())
			githubTestYAMLField(t, step, "with", "cache").Value = "true"
		}},
		{"missing locator context", githubCITransferPath(plan, "download") + "/action.yml", func(root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "name", "Download only private locators")
			githubYAMLRemove(step, "env")
		}},
		{"wildcard bootstrap locator discovery", githubCITransferPath(plan, "bootstrap-download") + "/action.yml", func(root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "name", "Download only private locators")
			githubTestYAMLField(t, step, "env", "NVKR_CI_PATTERN").Value = "*"
		}},
		{"credential scope", bundle.WorkflowPath, func(root *yamlv3.Node) {
			job := githubTestYAMLField(t, root, "jobs", bootstrap)
			step := githubTestYAMLStep(t, githubTestYAMLField(t, job, "steps"), "name", "Prepare NVKR runner (ubuntu-24.04)")
			githubTestYAMLField(t, step, "with", githubCIStorageInput).Value = "${{ secrets.UNRELATED }}"
		}},
		{"elevated package scope", bundle.WorkflowPath, func(root *yamlv3.Node) {
			githubYAMLSet(githubTestYAMLField(t, root, "jobs", bootstrap), "permissions", githubYAMLMap("contents", githubYAMLString("read"), "packages", githubYAMLString("write")))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := mutateGitHubBundleYAML(t, bundle, test.member, test.mutate)
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), e.CodeEngineCapability)
		})
	}
	for _, mode := range []string{"upload", "download", "bootstrap-download"} {
		t.Run("missing "+mode, func(t *testing.T) {
			changed := *bundle
			changed.Files = make(map[string][]byte, len(bundle.Files))
			for name, raw := range bundle.Files {
				if name != githubCITransferPath(plan, mode)+"/action.yml" {
					changed.Files[name] = raw
				}
			}
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &changed), e.CodeEngineCapability)
		})
	}
}

func TestGitHubPrivateTransferProjectionRemainsStableAcrossSourcePins(t *testing.T) {
	before := githubRuntimePlanFixture(t)
	after := recompileGitHubStabilityPlan(t, before, func(in *githubStabilityInputs) {
		for name, component := range in.catalog.Components {
			if component.FromSource {
				prior := component.Commit
				component.Commit = nextGitHubStabilityPin(t, component.Commit)
				component.TreeHash = nextGitHubStabilityPin(t, component.TreeHash)
				component.SourceDate++
				for key, value := range component.VersionStamp {
					component.VersionStamp[key] = strings.ReplaceAll(value, prior, component.Commit)
				}
				in.catalog.Components[name] = component
			}
		}
	})
	first, err := RenderGitHub(t.Context(), before)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderGitHub(t.Context(), after)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range first.Files {
		if name != first.PlanPath && name != githubActionDataPathFor(before) && !bytes.Equal(raw, second.Files[name]) {
			t.Fatalf("source pin update changed private transport implementation %s", name)
		}
	}
}
