// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const lockedFernBinary = "tools/fern/node_modules/.bin/fern"

var (
	exactSemver = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	// A bare `fern` command resolves through PATH instead of the locked install.
	bareFernCommand = regexp.MustCompile(`(?m)(^|[;&|(]\s*|\$\(\s*)fern\s`)
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

	makefile, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(makefile), "npm ci --prefix tools/fern ") || strings.Contains(string(makefile), "fern-api@") {
		t.Fatal("Makefile must install Fern only from the tools/fern lock")
	}

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
			installed := false
			for _, step := range job.Steps {
				where := file.Name() + ": " + jobID + ": " + step.Name
				switch {
				case strings.Contains(step.Run, "fern-api"):
					t.Errorf("%s installs fern-api outside the tools/fern lock", where)
				case bareFernCommand.MatchString(step.Run):
					t.Errorf("%s runs a PATH-resolved fern instead of %s", where, lockedFernBinary)
				case fernInstall.MatchString(step.Run):
					installed = true
				case strings.Contains(step.Run, lockedFernBinary):
					invocations++
					if !installed {
						t.Errorf("%s runs Fern before make fern-install", where)
					}
					if step.Env["FERN_NO_VERSION_REDIRECTION"] != "true" {
						t.Errorf("%s lets Fern redirect to the version a fern.config.json names", where)
					}
				}
			}
		}
	}
	if invocations == 0 {
		t.Fatal("no workflow runs the locked Fern CLI")
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
