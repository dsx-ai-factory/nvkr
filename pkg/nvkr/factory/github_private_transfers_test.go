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
		{name: "download cancellation", code: e.CodeTimeout},
		{name: "interrupt", code: e.CodeTimeout},
		{name: "missing runner temporary directory", code: e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, runnerTemp, ambient := t.TempDir(), t.TempDir(), t.TempDir()
			marker := filepath.Join(ambient, "keep")
			if err := os.WriteFile(marker, []byte("ambient cache"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"go.mod", "go.sum"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("unchanged source manifest"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			fakeGo := "#!/bin/sh\nexec " + shellQuote(python) + " -I - \"$@\" <<'PY'\n" + `import json, os, pathlib, signal, sys, time
for key in ("NVKR_CREDENTIAL", "NVKR_CI_STORAGE_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_TOKEN"):
    assert key not in os.environ
assert os.environ["GOTOOLCHAIN"] == "local" and os.environ["GOENV"] == "off"
assert os.environ["CGO_ENABLED"] == "0" and os.environ["GOWORK"] == "off"
build, modules = pathlib.Path(os.environ["GOCACHE"]), pathlib.Path(os.environ["GOMODCACHE"])
assert build.parent == modules.parent
assert build.parent.stat().st_mode & 0o077 == 0
modfile = pathlib.Path(next(value.removeprefix("-modfile=") for value in sys.argv if value.startswith("-modfile=")))
assert modfile.parent == build.parent / "inputs"
assert modfile.read_text() == modfile.with_suffix(".sum").read_text() == "unchanged source manifest"
mode = ` + strconv.Quote(test.name) + `
phase = "download" if sys.argv[1:3] == ["mod", "download"] else "build"
if phase == "download" and mode != "download cancellation":
    assert "GOPROXY" not in os.environ and "GOSUMDB" not in os.environ
    print(json.dumps({"Path": "example.com/module", "Version": "v1.0.0"}))
    sys.exit(0)
if phase == "build":
    assert os.environ["GOPROXY"] == "off" and os.environ["GOSUMDB"] == "off"
    for directory in (build, modules / "read-only-module"):
        directory.mkdir(parents=True)
        (directory / "cached").write_text("disposable")
        directory.chmod(0o500)
    pathlib.Path(".nvkr/ci-bootstrap/nvkr-exec").write_text("compiled helper")
pathlib.Path("bootstrap-observation.json").write_text(json.dumps({"pid": os.getpid(), "cache": str(build.parent), "args": sys.argv[1:]}))
if mode in ("cancellation", "download cancellation", "interrupt"):
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    os.kill(int(pathlib.Path("step.pid").read_text()), signal.SIGINT if mode == "interrupt" else signal.SIGTERM)
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        if not build.parent.exists(): pathlib.Path("cleanup-before-stop").write_text("violation")
        time.sleep(0.01)
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
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			script := githubCIBootstrapBuildCommand("-X main.version=fixture", githubRunnerRelease{})
			script = "printf '%s' \"$$\" > step.pid\n" + strings.ReplaceAll(strings.Replace(script, "900s", "60s", 1), "--kill-after=5s", "--kill-after=0.2s")
			command := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", script)
			command.Dir = root
			command.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"),
				"RUNNER_TEMP="+selectedTemp, "GOCACHE="+ambient, "GOMODCACHE="+ambient,
				"NVKR_CREDENTIAL=must-not-inherit", "NVKR_CI_STORAGE_TOKEN=must-not-inherit",
				"ACTIONS_ID_TOKEN_REQUEST_TOKEN=must-not-inherit", "GOPROXY=must-not-inherit")
			t.Cleanup(func() {
				var observed struct {
					PID int `json:"pid"`
				}
				if raw, err := os.ReadFile(filepath.Join(root, "bootstrap-observation.json")); err == nil && json.Unmarshal(raw, &observed) == nil && observed.PID > 0 {
					if process, err := os.FindProcess(observed.PID); err == nil {
						_ = process.Kill()
					}
				}
			})
			output, runErr := command.CombinedOutput()
			if _, err := os.Stat(filepath.Join(root, "cleanup-before-stop")); !os.IsNotExist(err) {
				t.Fatal("cache cleanup raced a live Go process")
			}
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
			resolvedRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			wantArgs := "build|-mod=readonly|-modfile=" + filepath.Join(observed.Cache, "inputs", "go.mod") + "|-trimpath|-ldflags|-X main.version=fixture|-o|" + filepath.Join(resolvedRoot, ".nvkr/ci-bootstrap/nvkr-exec") + "|./cmd/nvkr"
			if test.name == "download cancellation" {
				wantArgs = "mod|download|-modfile=" + filepath.Join(observed.Cache, "inputs", "go.mod")
			}
			if filepath.Dir(observed.Cache) != runnerTemp || !strings.HasPrefix(filepath.Base(observed.Cache), "nvkr-ci-bootstrap-") || strings.Join(observed.Args, "|") != wantArgs {
				t.Fatal("bootstrap escaped its owned caches or fixed build arguments")
			}
			process, err := os.FindProcess(observed.PID)
			if err != nil {
				t.Fatal(err)
			}
			if err := process.Signal(syscall.Signal(0)); err == nil {
				t.Fatal("bootstrap compiler survived cache cleanup")
			}
			if test.name == "download cancellation" {
				if _, err := os.Stat(filepath.Join(root, ".nvkr/ci-bootstrap/nvkr-exec")); !os.IsNotExist(err) {
					t.Fatal("canceled download reached compiler")
				}
				return
			}
			if contents, err := os.ReadFile(filepath.Join(root, ".nvkr/ci-bootstrap/nvkr-exec")); err != nil || string(contents) != "compiled helper" {
				t.Fatal("cache cleanup removed the compiled helper")
			}
		})
	}
}

func TestGitHubPrivateBootstrapRetriesDownloads(t *testing.T) {
	for _, test := range []struct {
		name             string
		failures, builds int
		code             e.Code
	}{
		{"success", 0, 1, ""},
		{"recovery", 2, 1, ""},
		{"exhaustion", 3, 0, e.CodeBuildFailed},
		{"download diagnostic", 3, 0, e.CodeBuildFailed},
		{"compiler failure", 0, 1, e.CodeBuildFailed},
		{"download changes go.mod", 0, 0, e.CodeBuildFailed},
		{"download changes go.sum", 0, 0, e.CodeBuildFailed},
		{"compiler changes go.mod", 0, 1, e.CodeBuildFailed},
		{"compiler changes go.sum", 0, 1, e.CodeBuildFailed},
		{"download changes both go.mod", 0, 0, e.CodeBuildFailed},
		{"compiler changes both go.sum", 0, 1, e.CodeBuildFailed},
		{"download consumes stdin changes go.mod", 0, 0, e.CodeBuildFailed},
		{"compiler consumes stdin changes go.mod", 0, 1, e.CodeBuildFailed},
		{"compiler killed", 0, 1, e.CodeBuildFailed},
		{"compiler exit 124", 0, 1, e.CodeBuildFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, cache := t.TempDir(), t.TempDir()
			for _, name := range []string{"go.mod", "go.sum"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("fixed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			mutation := ""
			if strings.Contains(test.name, "changes") {
				target := `"$modfile"`
				if strings.HasSuffix(test.name, "go.sum") {
					target = `"${modfile%.mod}.sum"`
				}
				mutation = "printf changed > " + target + "\n"
				if strings.Contains(test.name, "both") {
					mutation += "printf changed > " + shellQuote(test.name[strings.LastIndex(test.name, " ")+1:]) + "\n"
				}
				if strings.Contains(test.name, "stdin") {
					mutation = "cat > stdin-observed\n" + mutation
				}
			}
			downloadMutation, buildMutation := "", ""
			if strings.HasPrefix(test.name, "download") {
				downloadMutation = mutation
			} else {
				buildMutation = mutation
			}
			buildExit := "exit 0"
			switch test.name {
			case "compiler failure":
				buildExit = "exit 7"
			case "compiler killed":
				buildExit = "kill -KILL $$"
			case "compiler exit 124":
				buildExit = "exit 124"
			}
			diagnostic := ""
			if test.name == "download diagnostic" {
				diagnostic = "printf '%s\\n' 'module download failed: https://proxy.example/module?token=synthetic' >&2\n"
			}
			fake := `#!/bin/sh
set -eu
for argument do
  case "$argument" in -modfile=*) modfile=${argument#-modfile=} ;; esac
done
if [ "$1" = mod ]; then
  ` + downloadMutation + diagnostic + `
  echo download >> calls
  count=$(wc -l < calls)
  [ "$count" -gt ` + strconv.Itoa(test.failures) + ` ]
else
  echo build >> calls
  ` + buildMutation + buildExit + `
fi
`
			if err := os.WriteFile(filepath.Join(root, "go"), []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", "-c", githubCIBootstrapBuildCommand("-X main.version=fixture", githubRunnerRelease{}))
			command.Dir = root
			command.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "RUNNER_TEMP="+cache)
			output, err := command.CombinedOutput()
			if test.code == "" {
				if err != nil {
					t.Fatalf("bootstrap: %v %s", err, output)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != e.ExitCode(e.New(test.code, "")) || !bytes.Contains(output, []byte(test.code)) {
					t.Fatalf("lost coded failure: %v %s", err, output)
				}
			}
			if test.name == "download diagnostic" && !bytes.Contains(output, []byte("token=synthetic")) {
				t.Fatal("download diagnostic logging changed")
			}
			if strings.Contains(test.name, "stdin") {
				if raw, err := os.ReadFile(filepath.Join(root, "stdin-observed")); err != nil || len(raw) != 0 {
					t.Fatalf("Go inherited script stdin: %v %q", err, raw)
				}
			}
			raw, err := os.ReadFile(filepath.Join(root, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(raw), "download") != min(test.failures+1, 3) || strings.Count(string(raw), "build") != test.builds {
				t.Fatalf("unexpected attempts: %s", raw)
			}
		})
	}
}

func TestGitHubPrivateBootstrapCapabilities(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash required")
	}
	for _, test := range []struct {
		name, missing string
		published     bool
	}{
		{"missing Go", "go", false},
		{"missing timeout", "timeout", false},
		{"published requires Python", "python3", true},
		{"development needs no Python", "python3", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, cache := t.TempDir(), t.TempDir()
			for _, name := range []string{"bash", "env", "timeout", "mktemp", "chmod", "rm", "mkdir", "cp", "cmp", "wc", "cat", "sleep"} {
				if name == test.missing {
					continue
				}
				path, err := exec.LookPath(name)
				if err != nil {
					t.Skipf("fixture needs %s", name)
				}
				if err := os.Symlink(path, filepath.Join(root, name)); err != nil {
					t.Fatal(err)
				}
			}
			if test.missing != "go" {
				if err := os.WriteFile(filepath.Join(root, "go"), []byte("#!/bin/sh\nprintf called >> "+shellQuote(filepath.Join(root, "called"))+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"go.mod", "go.sum"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("fixed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			release := githubRunnerRelease{}
			if test.published {
				release = testRunnerRelease
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, bash, "-c", githubCIBootstrapBuildCommand("-X main.version=fixture", release))
			command.Dir = root
			command.Env = []string{"PATH=" + root, "RUNNER_TEMP=" + cache, "HOME=" + root}
			output, runErr := command.CombinedOutput()
			if test.name == "development needs no Python" {
				if runErr != nil {
					t.Fatalf("development acquired Python requirement: %v %s", runErr, output)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(runErr, &exit) || exit.ExitCode() != e.ExitCode(e.New(e.CodeEngineCapability, "")) || !bytes.Contains(output, []byte(e.CodeEngineCapability)) {
					t.Fatalf("lost capability code: %v %s", runErr, output)
				}
				if _, err := os.Stat(filepath.Join(root, "called")); !os.IsNotExist(err) {
					t.Fatal("missing capability reached Go")
				}
			}
			if entries, err := os.ReadDir(cache); err != nil || len(entries) != 0 {
				t.Fatalf("cache leaked: %v %v", entries, err)
			}
		})
	}
}

func TestGitHubPrivateBootstrapRejectsInvalidManifests(t *testing.T) {
	for _, name := range []string{"go.mod", "go.sum"} {
		for _, kind := range []string{"symlink", "directory", "oversized"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				root, cache := t.TempDir(), t.TempDir()
				for _, file := range []string{"go.mod", "go.sum"} {
					if err := os.WriteFile(filepath.Join(root, file), []byte("fixed"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				target := filepath.Join(root, name)
				switch kind {
				case "symlink", "directory":
					if err := os.Remove(target); err != nil {
						t.Fatal(err)
					}
					if kind == "symlink" {
						if err := os.Symlink("go", target); err != nil {
							t.Fatal(err)
						}
					} else if err := os.Mkdir(target, 0700); err != nil {
						t.Fatal(err)
					}
				case "oversized":
					if err := os.Truncate(target, 4*1024*1024+1); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(root, "go"), []byte("#!/bin/sh\nprintf called > called\n"), 0700); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, "bash", "-c", githubCIBootstrapBuildCommand("-X main.version=fixture", githubRunnerRelease{}))
				command.Dir = root
				command.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "RUNNER_TEMP="+cache)
				output, runErr := command.CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(runErr, &exit) || exit.ExitCode() != e.ExitCode(e.New(e.CodeBuildFailed, "")) || !bytes.Contains(output, []byte(e.CodeBuildFailed)) {
					t.Fatalf("invalid manifest lost code: %v %s", runErr, output)
				}
				if _, err := os.Stat(filepath.Join(root, "called")); !os.IsNotExist(err) {
					t.Fatal("invalid manifest reached Go")
				}
				if entries, err := os.ReadDir(cache); err != nil || len(entries) != 0 {
					t.Fatalf("cache leaked: %v %v", entries, err)
				}
			})
		}
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
							// Test binaries are development builds, which build the runner from source.
							if githubYAMLValue(with, "cache") != "false" || githubYAMLValue(with, "go-version-file") != ".go-version" || githubYAMLField(with, "go-version") != nil {
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
					if run := githubYAMLValue(node, "run"); strings.Contains(run, " factory github ci-fetch-locators ") || strings.Contains(run, " factory github ci-download ") {
						want := "allow_earlier=true"
						if !strings.Contains(run, want) || !strings.Contains(run, "if [ -n \"$NVKR_CI_RUN\" ]; then allow_earlier=false; fi") || !strings.Contains(run, "--allow-earlier-attempts=\"$allow_earlier\"") {
							t.Fatalf("transport retry policy differs for %s", fixture.name)
						}
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
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "name", "Download metadata and private locators")
			githubYAMLRemove(step, "env")
		}},
		{"wildcard bootstrap locator discovery", githubCITransferPath(plan, "bootstrap-download") + "/action.yml", func(root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "name", "Download metadata and private locators")
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
		{"root read account", bundle.WorkflowPath, func(root *yamlv3.Node) {
			githubTestYAMLField(t, root, "env", "NVKR_CI_STORAGE_READ_USER").Value = "untrusted"
		}},
		{"root write account", bundle.WorkflowPath, func(root *yamlv3.Node) {
			githubTestYAMLField(t, root, "env", "NVKR_CI_STORAGE_WRITE_USER").Value = "untrusted"
		}},
		{"job read override", bundle.WorkflowPath, func(root *yamlv3.Node) {
			githubYAMLSet(githubTestYAMLField(t, root, "jobs", bootstrap), "env", githubYAMLMap("NVKR_CI_STORAGE_READ_USER", githubYAMLString("untrusted")))
		}},
		{"job write override", bundle.WorkflowPath, func(root *yamlv3.Node) {
			githubYAMLSet(githubTestYAMLField(t, root, "jobs", bootstrap), "env", githubYAMLMap("NVKR_CI_STORAGE_WRITE_USER", githubYAMLString("untrusted")))
		}},
		{"step read override", bundle.WorkflowPath, func(root *yamlv3.Node) {
			step := githubTestYAMLField(t, root, "jobs", bootstrap, "steps").Content[0]
			githubYAMLSet(step, "env", githubYAMLMap("NVKR_CI_STORAGE_READ_USER", githubYAMLString("untrusted")))
		}},
		{"step write override", bundle.WorkflowPath, func(root *yamlv3.Node) {
			step := githubTestYAMLField(t, root, "jobs", bootstrap, "steps").Content[0]
			githubYAMLSet(step, "env", githubYAMLMap("NVKR_CI_STORAGE_WRITE_USER", githubYAMLString("untrusted")))
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

func TestGitHubPrivateTransferRejectsExistingWorkflowEnvironmentBeforeOutput(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := resolveGitHubActions(plan.Actions)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"UNRELATED", "NVKR_CI_STORAGE_READ_USER", "NVKR_CI_STORAGE_WRITE_USER"} {
		t.Run(key, func(t *testing.T) {
			changed := mutateGitHubBundleYAML(t, bundle, bundle.WorkflowPath, func(root *yamlv3.Node) {
				githubYAMLSet(root, "env", githubYAMLMap(key, githubYAMLString("preexisting")))
			})
			workflow, files, err := privatizeGitHubTransfers(plan, changed.Files[bundle.WorkflowPath], changed.Files, actions)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if workflow != nil || files != nil {
				t.Fatal("rejected environment produced transfer output")
			}
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
		if name == first.PlanPath ||
			isPlanStoreRecord(name) {
			continue
		}
		if !bytes.Equal(raw, second.Files[name]) {
			t.Fatalf("source pin update changed private transport implementation %s", name)
		}
	}
}

func TestPrivateTransferRetryPolicyAtInvocation(t *testing.T) {
	for _, development := range []bool{false, true} {
		t.Run("development="+strconv.FormatBool(development), func(t *testing.T) {
			plan := githubTestPlan(t)
			if development {
				plan = githubDevelopmentTestPlan(t)
			}
			files, err := githubCITransferTemplates(plan, testActionRefs(t, plan))
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"download", "bootstrap-download"} {
				var document yamlv3.Node
				if err := yamlv3.Unmarshal(files[githubCITransferPath(plan, mode)+"/action.yml"], &document); err != nil {
					t.Fatal(err)
				}
				steps := githubYAMLField(githubYAMLField(document.Content[0], "runs"), "steps")
				for _, step := range steps.Content {
					command := githubYAMLValue(step, "run")
					if !strings.Contains(command, "--allow-earlier-attempts=") {
						continue
					}
					for _, run := range []string{"", "123"} {
						for _, stage := range []string{"", "build-stage"} {
							t.Run(mode+"/"+githubYAMLValue(step, "name")+"/run="+run+"/stage="+stage, func(t *testing.T) {
								root := t.TempDir()
								helper := filepath.Join(root, "nvkr-exec")
								if mode == "bootstrap-download" {
									helper = filepath.Join(root, ".nvkr", "ci-bootstrap", "nvkr-exec")
									if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
										t.Fatal(err)
									}
								}
								if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
									t.Fatal(err)
								}
								cmd := exec.CommandContext(t.Context(), "bash", "-c", command)
								cmd.Dir = root
								cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "NVKR_CI_RUN=" + run, "NVKR_CI_ATTEMPT=2",
									"NVKR_CI_STAGE=" + stage, "NVKR_CI_INDEX=index", "NVKR_CI_DESTINATION=out",
									"NVKR_CI_NAME=helper", "NVKR_CI_PATTERN=", "NVKR_CI_MERGE=false"}
								output, err := cmd.CombinedOutput()
								if err != nil {
									t.Fatalf("generated command failed: %v: %s", err, output)
								}
								want := "--allow-earlier-attempts=" + strconv.FormatBool(run == "")
								if !strings.Contains(string(output), want+"\n") {
									t.Fatalf("missing %s in arguments: %s", want, output)
								}
							})
						}
					}
				}
			}
		})
	}
}

// testRunnerRelease is a syntactically valid pinned NVKR release.
var testRunnerRelease = githubRunnerRelease{Version: "v1.2.3", Sum: "h1:" + strings.Repeat("A", 43) + "=", GoVersion: "1.27.3"}

// A released generator pins the transport helper to its own published module,
// builds it from a private copy, and leaves the module cache and the checkout's
// own files untouched. Any other module, version or checksum is refused.
func TestGitHubPrivateBootstrapBuildsPinnedPublishedRelease(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 is required to exercise the hosted bootstrap owner")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash is required to exercise the hosted bootstrap owner")
	}
	for _, test := range []struct {
		name, path, version, sum string
		code                     e.Code
	}{
		{name: "pinned release", path: githubRunnerModule, version: testRunnerRelease.Version, sum: testRunnerRelease.Sum},
		{name: "unexpected module", path: "example.com/other", version: testRunnerRelease.Version, sum: testRunnerRelease.Sum, code: e.CodeBuildFailed},
		{name: "unexpected version", path: githubRunnerModule, version: "v1.2.4", sum: testRunnerRelease.Sum, code: e.CodeBuildFailed},
		{name: "unexpected checksum", path: githubRunnerModule, version: testRunnerRelease.Version, sum: "h1:" + strings.Repeat("B", 43) + "=", code: e.CodeBuildFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, runnerTemp, published := t.TempDir(), t.TempDir(), t.TempDir()
			// The consumer's own module must not take part in runner acquisition.
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/recipes\n\ngo 9.99\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"go.mod", "go.sum"} {
				if err := os.WriteFile(filepath.Join(published, name), []byte("published "+name), 0o400); err != nil {
					t.Fatal(err)
				}
			}
			fakeGo := "#!/bin/sh\nexec " + shellQuote(python) + " -I - \"$@\" <<'PY'\n" + `import json, os, pathlib, sys
published, workspace = ` + strconv.Quote(published) + `, ` + strconv.Quote(root) + `
cache = pathlib.Path(os.environ["GOCACHE"]).parent
source = cache / "source"
if sys.argv[1:] == ["mod", "download", "-json", ` + strconv.Quote(githubRunnerModule+"@"+testRunnerRelease.Version) + `]:
    assert "GOPROXY" not in os.environ and os.path.realpath(os.getcwd()) == os.path.realpath(cache)
    print(json.dumps({"Path": ` + strconv.Quote(test.path) + `, "Version": ` + strconv.Quote(test.version) + `, "Sum": ` + strconv.Quote(test.sum) + `, "Dir": published}))
    sys.exit(0)
assert os.path.realpath(os.getcwd()) == os.path.realpath(source)
modfile = pathlib.Path(next(value.removeprefix("-modfile=") for value in sys.argv if value.startswith("-modfile=")))
assert modfile.parent == cache / "inputs" and modfile.read_text() == "published go.mod"
if sys.argv[1:3] == ["mod", "download"]:
    print(json.dumps({"Path": "example.com/dependency", "Version": "v1.0.0"}))
    sys.exit(0)
assert sys.argv[1] == "build" and os.environ["GOPROXY"] == "off" and sys.argv[-1] == "./cmd/nvkr"
output = pathlib.Path(sys.argv[sys.argv.index("-o") + 1])
assert output.is_absolute() and os.path.realpath(output.parent.parent.parent) == os.path.realpath(workspace)
output.write_text("published helper")
PY
`
			if err := os.WriteFile(filepath.Join(root, "go"), []byte(fakeGo), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, bash, "-c", githubCIBootstrapBuildCommand("-X main.version=fixture", testRunnerRelease))
			command.Dir = root
			command.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "RUNNER_TEMP="+runnerTemp)
			output, runErr := command.CombinedOutput()
			helper, readErr := os.ReadFile(filepath.Join(root, ".nvkr/ci-bootstrap/nvkr-exec"))
			if test.code == "" {
				if runErr != nil || readErr != nil || string(helper) != "published helper" ||
					!bytes.Contains(output, []byte("from "+githubRunnerModule+" "+testRunnerRelease.Version+" "+testRunnerRelease.Sum)) {
					t.Fatalf("pinned bootstrap failed or did not log its module: %v %v\n%s", runErr, readErr, output)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(runErr, &exit) || exit.ExitCode() != e.ExitCode(e.New(test.code, "")) || readErr == nil {
					t.Fatalf("mismatched module was not refused with %s: %v\n%s", test.code, runErr, output)
				}
			}
			for _, name := range []string{"go.mod", "go.sum"} {
				if raw, err := os.ReadFile(filepath.Join(published, name)); err != nil || string(raw) != "published "+name {
					t.Fatalf("bootstrap changed the published module's %s: %v", name, err)
				}
			}
			if entries, err := os.ReadDir(runnerTemp); err != nil || len(entries) != 0 {
				t.Fatalf("bootstrap left disposable caches behind: %v / %v", entries, err)
			}
		})
	}
}
