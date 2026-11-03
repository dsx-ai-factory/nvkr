// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"

	"sigs.k8s.io/yaml"
)

const lockedFernBinary = "tools/fern/node_modules/.bin/fern"

var (
	exactSemver = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	// A bare `fern` command resolves through PATH instead of the locked install.
	bareFernCommand = regexp.MustCompile(`(?m)(^\s*|[;&|(]\s*|\$\(\s*)fern(\s|$)`)
	fernInstall     = regexp.MustCompile(`(?m)^\s*make\s+(-C\s+\S+\s+)?fern-install\s*$`)
)

// TestFernCLIIsLockedAndSharedByEveryInstallSite keeps the Fern CLI on one
// reviewed, integrity-locked version: the npm lock, the Fern project config and
// every workflow consume the same pin through `make fern-install`.
func TestFernCLIIsLockedAndSharedByEveryInstallSite(t *testing.T) {
	version := lockedFernVersion(t)

	var config struct {
		Version string `json:"version"`
	}
	readJSON(t, "../../fern/fern.config.json", &config)
	if config.Version != version {
		t.Fatalf("fern/fern.config.json version %q, want the locked %q", config.Version, version)
	}

	assertFernMakeRecipes(t)

	const directory = "../../.github/workflows"
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	invocations := 0
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".yml") && !strings.HasSuffix(file.Name(), ".yaml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(directory, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var workflow struct {
			Jobs map[string]struct {
				Steps []storageWorkflowStep `json:"steps"`
			} `json:"jobs"`
		}
		if err := yaml.Unmarshal(raw, &workflow); err != nil {
			t.Fatalf("%s: %v", file.Name(), err)
		}
		for jobID, job := range workflow.Jobs {
			for _, problem := range fernStepViolations(job.Steps) {
				t.Errorf("%s: %s: %s", file.Name(), jobID, problem)
			}
			for _, step := range job.Steps {
				if strings.Contains(step.Run, lockedFernBinary) {
					invocations++
				}
			}
		}
	}
	if invocations == 0 {
		t.Fatal("no workflow runs the locked Fern CLI")
	}
}

// fernStepViolations checks one job's steps: Fern comes only from the lock,
// runs only after `make fern-install` with redirection disabled, and a job
// that installs it keeps downloaded artifacts outside the trusted workspace.
func fernStepViolations(steps []storageWorkflowStep) []string {
	var problems []string
	installs := false
	for _, step := range steps {
		installs = installs || fernInstall.MatchString(step.Run)
	}
	installed := false
	for _, step := range steps {
		if strings.Contains(step.Run, "fern-api") {
			problems = append(problems, step.Name+" installs fern-api outside the tools/fern lock")
		}
		if bareFernCommand.MatchString(step.Run) {
			problems = append(problems, step.Name+" runs a PATH-resolved fern instead of "+lockedFernBinary)
		}
		if fernInstall.MatchString(step.Run) {
			installed = true
		}
		if strings.Contains(step.Run, lockedFernBinary) {
			if !installed {
				problems = append(problems, step.Name+" runs Fern before make fern-install")
			}
			if step.Env["FERN_NO_VERSION_REDIRECTION"] != "true" {
				problems = append(problems, step.Name+" lets Fern redirect to the version a fern.config.json names")
			}
		}
		if installs && strings.HasPrefix(step.Uses, "actions/download-artifact@") {
			path, _ := step.With["path"].(string)
			if rest, ok := strings.CutPrefix(path, "${{ runner.temp }}/"); !ok || !filepath.IsLocal(rest) {
				problems = append(problems, step.Name+" extracts an artifact into the workspace holding the trusted Fern install")
			}
		}
	}
	return problems
}

func TestFernStepViolations(t *testing.T) {
	const locked = `"$GITHUB_WORKSPACE/tools/fern/node_modules/.bin/fern" check`
	install := storageWorkflowStep{Name: "install", Run: "make fern-install"}
	safe := map[string]string{"FERN_NO_VERSION_REDIRECTION": "true"}
	tests := []struct {
		name  string
		steps []storageWorkflowStep
		want  string
	}{
		{"locked after install", []storageWorkflowStep{install, {Name: "run", Run: locked, Env: safe}}, ""},
		{"install from a shared checkout", []storageWorkflowStep{{Name: "install", Run: "make -C nvkr-tools fern-install"}, {Name: "run", Run: locked, Env: safe}}, ""},
		{"floating npm install", []storageWorkflowStep{{Name: "npm", Run: "npm install -g fern-api@latest"}}, "outside the tools/fern lock"},
		{"npx", []storageWorkflowStep{{Name: "npx", Run: "npx fern-api check"}}, "outside the tools/fern lock"},
		{"bare call", []storageWorkflowStep{{Name: "bare", Run: "fern check"}}, "PATH-resolved"},
		{"indented bare call", []storageWorkflowStep{{Name: "bare", Run: "set -e\n  fern generate --docs\n"}}, "PATH-resolved"},
		{"bare final word", []storageWorkflowStep{{Name: "bare", Run: "fern"}}, "PATH-resolved"},
		{"bare after pipe", []storageWorkflowStep{{Name: "bare", Run: "true && fern check"}}, "PATH-resolved"},
		{"locked before install", []storageWorkflowStep{{Name: "run", Run: locked, Env: safe}, install}, "before make fern-install"},
		{"install and invoke in one step without env", []storageWorkflowStep{{Name: "both", Run: "make fern-install\n" + locked}}, "redirect"},
		{"artifact in workspace", []storageWorkflowStep{install, {Name: "download", Uses: "actions/download-artifact@x", With: map[string]any{}}}, "trusted Fern install"},
		{"artifact under runner temp", []storageWorkflowStep{install, {Name: "download", Uses: "actions/download-artifact@x", With: map[string]any{"path": "${{ runner.temp }}/docs"}}}, ""},
		{"artifact escaping runner temp", []storageWorkflowStep{install, {Name: "download", Uses: "actions/download-artifact@x", With: map[string]any{"path": "${{ runner.temp }}/../work/nvkr"}}}, "trusted Fern install"},
		{"artifact without a Fern install", []storageWorkflowStep{{Name: "download", Uses: "actions/download-artifact@x"}}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			problems := strings.Join(fernStepViolations(test.steps), "; ")
			if test.want == "" && problems != "" || test.want != "" && !strings.Contains(problems, test.want) {
				t.Fatalf("violations %q, want %q", problems, test.want)
			}
		})
	}
}

// assertFernMakeRecipes dry-runs the shared Make entrypoints and checks the
// commands they would execute.
func assertFernMakeRecipes(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// Make expands $(CURDIR) to the physical path, so compare against the same.
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// make -n still runs the Makefile's $(shell ...) calls, so own the whole process group.
	output, err := ownedcmd.CombinedOutput(ctx, "make", "-n", "-C", root, "fern-install", "docs-check")
	if err != nil {
		t.Fatalf("make -n fern-install docs-check: %v\n%s", err, output)
	}
	got := string(output)
	if strings.Contains(got, "fern-api@") {
		t.Fatal("Makefile installs a floating fern-api")
	}
	binary := `"` + filepath.Join(root, lockedFernBinary) + `"`
	for _, want := range []string{
		"npm ci --prefix tools/fern --omit=optional --ignore-scripts",
		"export FERN_NO_VERSION_REDIRECTION=true",
		binary + " check",
		binary + " docs md check",
		binary + " docs broken-links --strict",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("make -n fern-install docs-check lacks %q:\n%s", want, got)
		}
	}
}

func lockedFernVersion(t *testing.T) string {
	t.Helper()
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	readJSON(t, "../../tools/fern/package.json", &manifest)
	version := manifest.Dependencies["fern-api"]
	if len(manifest.Dependencies) != 1 || !exactSemver.MatchString(version) {
		t.Fatalf("tools/fern/package.json must depend only on an exact fern-api version, got %v", manifest.Dependencies)
	}

	var lock struct {
		LockfileVersion int `json:"lockfileVersion"`
		Packages        map[string]struct {
			Version      string            `json:"version"`
			Resolved     string            `json:"resolved"`
			Integrity    string            `json:"integrity"`
			Dependencies map[string]string `json:"dependencies"`
		} `json:"packages"`
	}
	readJSON(t, "../../tools/fern/package-lock.json", &lock)
	if lock.LockfileVersion != 3 || lock.Packages[""].Dependencies["fern-api"] != version {
		t.Fatalf("tools/fern/package-lock.json is stale for fern-api %s", version)
	}
	if got := lock.Packages["node_modules/fern-api"].Version; got != version {
		t.Fatalf("locked fern-api %q, want %q", got, version)
	}
	for name, entry := range lock.Packages {
		if name == "" {
			continue
		}
		if !strings.HasPrefix(entry.Resolved, "https://registry.npmjs.org/") || !strings.HasPrefix(entry.Integrity, "sha512-") {
			t.Fatalf("%s lacks a registry source and sha512 integrity in the Fern lock", name)
		}
	}
	return version
}

func readJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, value); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
