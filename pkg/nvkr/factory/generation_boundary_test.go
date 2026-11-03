// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

// Render is the only generation seam a selected generator may occupy. These
// contracts pin the boundary: every generated-stage owner admits, refreshes or
// describes committed bytes through the core projection alone, and committed
// generated closures invoke NVKR only through core stage commands.

// forbidGenerationSeam traps Render's projection and the projection adapter
// launcher, and fails the test if any caller reaches either.
func forbidGenerationSeam(t *testing.T) {
	t.Helper()
	previous, reached := renderSelected, 0
	renderSelected = func(context.Context, *Plan) (*GeneratedBundle, error) {
		reached++
		return nil, e.New(e.CodeEngineCapability, "generated stage reached the generation seam")
	}
	previousLaunch, launched := launchProjectionAdapter, 0
	launchProjectionAdapter = func(context.Context, ProjectionAdapter, []byte, int) ([]byte, error) {
		launched++
		return nil, e.New(e.CodeEngineCapability, "generated stage launched the projection adapter")
	}
	t.Cleanup(func() {
		renderSelected, launchProjectionAdapter = previous, previousLaunch
		if reached != 0 || launched != 0 {
			t.Errorf("generation seam reached %d times; projection adapter launched %d times", reached, launched)
		}
	})
}

// leakedProjectionSelection selects an adapter that does not exist, so a
// stage owner that consulted the selection would fail instead of passing.
func leakedProjectionSelection(ctx context.Context) context.Context {
	return WithProjectionAdapter(ctx, ProjectionAdapter{Transport: ProjectionOutOfProcess, Path: "/nonexistent/nvkr-projection-adapter", Release: "trap"})
}

func TestRenderIsTheGenerationSeam(t *testing.T) {
	plan := githubTestPlan(t)
	want, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	previous, reached := renderSelected, 0
	renderSelected = func(ctx context.Context, plan *Plan) (*GeneratedBundle, error) {
		reached++
		return previous(ctx, plan)
	}
	t.Cleanup(func() { renderSelected = previous })
	assertFactoryErrorCode(t, func() error { _, err := Render(t.Context(), plan, EngineGitLab); return err }(), e.CodeEngineCapability)
	got, err := Render(t.Context(), plan, EngineGitHub)
	if err != nil || reached != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("Render did not take the seam exactly once for the admitted engine: reached=%d err=%v", reached, err)
	}
	// `run generate` writes are the lifecycle caller's only seam route.
	digest, err := Generate(t.Context(), plan, EngineGitHub, t.TempDir(), false)
	if err != nil || reached != 2 || digest != want.PlanDigest {
		t.Fatalf("Generate write mode did not take the seam: reached=%d err=%v", reached, err)
	}
	// Check mode takes the seam only for an explicit projection selection.
	_, err = Generate(leakedProjectionSelection(t.Context()), plan, EngineGitHub, t.TempDir(), true)
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	if reached != 3 {
		t.Fatalf("Generate pilot check did not take the seam: reached=%d", reached)
	}
}

func TestGeneratedStageOwnersNeverTakeTheGenerationSeam(t *testing.T) {
	plan, baseline, original := runtimeInputsFixture(t)
	fresh := freshGitHubRuntimePins(t, plan)
	selector := sortedKeys(sharedGitHubActionData(t, baseline).Entries)[0]
	for _, owner := range []struct {
		name string
		run  func(context.Context, string) error
		// tampered is the refusal for drifted committed action data; the
		// descriptor regenerates its values and never trusts that file.
		tampered e.Code
		// pilotCheck marks the top-level check, which honors the selection
		// its hidden diagnostic flag makes. Generated closures never pass that
		// flag (TestGeneratedClosuresInvokeOnlyCoreStageCommands), so this
		// route runs here as bootstraps run it: with no selection.
		pilotCheck bool
	}{
		{"run generate --check", func(ctx context.Context, root string) error {
			_, err := Generate(ctx, plan, EngineGitHub, root, true)
			return err
		}, e.CodePipelineDrift, true},
		{"run generate --runtime --source-commit --check", func(ctx context.Context, root string) error {
			return CheckGenerated(ctx, plan, EngineGitHub, root)
		}, e.CodePipelineDrift, false},
		{"run generate --runtime --check", func(ctx context.Context, root string) error {
			return PrepareGitHubRuntimeBundle(ctx, fresh, root, true)
		}, e.CodePipelineDrift, false},
		{"run generate --runtime", func(ctx context.Context, root string) error {
			return PrepareGitHubRuntimeBundle(ctx, fresh, root, false)
		}, e.CodePipelineDrift, false},
		{"check-runtime-inputs", func(ctx context.Context, root string) error {
			return CheckGitHubRuntimeInputs(ctx, plan, root)
		}, e.CodePipelineDrift, false},
		{"install-runtime-inputs", func(ctx context.Context, root string) error {
			stageGitHubRuntimeInputs(t, root, fresh)
			return InstallGitHubRuntimeInputs(ctx, plan, root)
		}, e.CodePipelineDrift, false},
		{"describe-action", func(ctx context.Context, root string) error {
			return WriteGitHubActionData(ctx, plan, selector, filepath.Join(root, "github-output"))
		}, "", false},
	} {
		for _, outcome := range []string{"admitted", "tampered", "canceled"} {
			t.Run(owner.name+"/"+outcome, func(t *testing.T) {
				forbidGenerationSeam(t)
				root := copyGitHubRuntimeRoot(t, original)
				// Even a selection leaked into a stage's context launches nothing.
				ctx := t.Context()
				if !owner.pilotCheck {
					ctx = leakedProjectionSelection(ctx)
				}
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				var want e.Code
				switch outcome {
				case "tampered":
					data := filepath.Join(root, filepath.FromSlash(githubActionDataPathFor(plan)))
					if err := os.WriteFile(data, []byte(`{"entries":{}}`), 0o644); err != nil {
						t.Fatal(err)
					}
					want = owner.tampered
				case "canceled":
					cancel()
					want = e.CodeTimeout
				}
				err := owner.run(ctx, root)
				if want == "" {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				assertFactoryErrorCode(t, err, want)
			})
		}
	}
}

// Every manifest-owned workflow, partition worker and local action is parsed
// with aliases expanded. A generation command there must be a core admission
// or runtime refresh, never a write-mode regeneration or another selection.
func TestGeneratedClosuresInvokeOnlyCoreStageCommands(t *testing.T) {
	manifests, err := filepath.Glob("../../../.github/*/manifest.json")
	if err != nil || len(manifests) == 0 {
		t.Fatalf("no committed generated manifests: %v", err)
	}
	seen := map[string]int{}
	for _, manifestPath := range manifests {
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		var manifest struct {
			Members []struct {
				Path string `json:"path"`
			} `json:"members"`
		}
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		for _, member := range manifest.Members {
			if !strings.HasSuffix(member.Path, ".yml") && !strings.HasSuffix(member.Path, ".yaml") {
				continue
			}
			raw, err := os.ReadFile("../../../" + member.Path)
			if err != nil {
				t.Fatal(err)
			}
			var document any
			if err := yaml.Unmarshal(raw, &document); err != nil {
				t.Fatalf("%s: %v", member.Path, err)
			}
			for _, script := range generatedRunScripts(document) {
				for _, violation := range generatedScriptViolations(script, seen) {
					t.Errorf("%s %s", member.Path, violation)
				}
			}
		}
	}
	for command := range generatedStageCommands {
		if seen[command] == 0 {
			t.Errorf("admitted stage command %q no longer appears in any committed closure", command)
		}
	}
}

// The checker itself must refuse every route to the projection pilot.
func TestGeneratedScriptCheckRejectsProjectionAdapterRoutes(t *testing.T) {
	for _, test := range []struct {
		script string
		admit  bool
	}{
		{"./nvkr-exec run generate --engine github --definition d --environment e --root . --check", true},
		{"./nvkr-exec run generate --engine github --definition d --environment e --root . --check --projection-pilot", false},
		{"./nvkr-exec run generate --engine github --definition d --environment e --root . --check --projection-pilot=in-process", false},
		{"./nvkr-exec run generate --engine github --definition d --environment e --root .", false},
		{"./nvkr-exec projection-adapter < request", false},
		{`"$NVKR" projection-adapter < request`, false},
		{"exec ./.nvkr/ci-bootstrap/nvkr-exec run qualify --projection-pilot", false},
	} {
		violations := generatedScriptViolations(test.script, map[string]int{})
		if (len(violations) == 0) != test.admit {
			t.Errorf("script %q: violations %v, want admitted=%t", test.script, violations, test.admit)
		}
	}
}

var (
	// The exact inventory of commands generated stages may run. Before adding
	// one, confirm its owner never reaches Render. If it re-emits committed
	// bytes, add it to the seam-trap table above. Remove a command here when
	// generation stops emitting it.
	generatedStageCommands = map[string]bool{
		"factory github check-runtime-inputs": true, "factory github ci-download": true,
		"factory github ci-fetch-locators": true, "factory github ci-upload": true,
		"factory github describe-action": true, "factory github install-runtime-inputs": true,
		"factory github pack-outputs": true, "factory github prepare-images": true,
		"factory github recover": true, "factory github run-disk": true, "factory github run-node": true,
		"run finalize": true, "run generate": true, "run qualification-source": true,
		"run qualify": true, "run smoke-worker": true,
	}
	generatedGenerationFlags = map[string]bool{"--engine": true, "--definition": true, "--environment": true,
		"--root": true, "--check": true, "--runtime": true, "--source-commit": true}
	generatedInvocation = regexp.MustCompile(`(?m)(?:^|[\s;&|(])(?:exec[ \t]+)?\./(?:\.nvkr/ci-bootstrap/)?nvkr-exec[ \t]+(.*)$`)
)

// generatedScriptViolations parses one generated run script: every NVKR
// invocation is a known core stage command, every generation command is a
// check or runtime admission with no other selection flag, and nothing names
// the projection pilot adapter or its selector by any route.
func generatedScriptViolations(script string, seen map[string]int) []string {
	script = strings.ReplaceAll(script, "\\\n", " ")
	var violations []string
	if strings.Contains(script, "projection-adapter") || strings.Contains(script, "projection-pilot") {
		violations = append(violations, "references the projection pilot adapter")
	}
	generations := 0
	for _, match := range generatedInvocation.FindAllStringSubmatch(script, -1) {
		words := strings.Fields(match[1])
		length := 2
		if len(words) > 0 && words[0] == "factory" {
			length = 3
		}
		if len(words) < length || !generatedStageCommands[strings.Join(words[:length], " ")] {
			violations = append(violations, "invokes a non-core stage command: "+match[1])
			continue
		}
		command := strings.Join(words[:length], " ")
		seen[command]++
		if command != "run generate" {
			continue
		}
		generations++
		admission := false
		for _, word := range words[length:] {
			if strings.HasPrefix(word, "--") && !generatedGenerationFlags[word] {
				violations = append(violations, "selects generation with "+word+": "+match[1])
			}
			admission = admission || word == "--check" || word == "--runtime"
		}
		if !admission {
			violations = append(violations, "regenerates inside a stage: "+match[1])
		}
	}
	if strings.Count(script, "run generate") != generations {
		violations = append(violations, "reaches generation outside a recognized core invocation")
	}
	return violations
}

func generatedRunScripts(node any) []string {
	var scripts []string
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			if script, ok := child.(string); ok && key == "run" {
				scripts = append(scripts, script)
				continue
			}
			scripts = append(scripts, generatedRunScripts(child)...)
		}
	case []any:
		for _, child := range value {
			scripts = append(scripts, generatedRunScripts(child)...)
		}
	}
	return scripts
}
