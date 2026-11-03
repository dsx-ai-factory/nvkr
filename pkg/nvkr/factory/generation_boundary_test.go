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

// forbidGenerationSeam traps Render's projection and fails the test if any
// caller reaches it.
func forbidGenerationSeam(t *testing.T) {
	t.Helper()
	previous, reached := renderSelected, 0
	renderSelected = func(context.Context, *Plan) (*GeneratedBundle, error) {
		reached++
		return nil, e.New(e.CodeEngineCapability, "generated stage reached the generation seam")
	}
	t.Cleanup(func() {
		renderSelected = previous
		if reached != 0 {
			t.Errorf("generation seam reached %d times", reached)
		}
	})
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
	}{
		{"run generate --check", func(ctx context.Context, root string) error {
			return CheckGenerated(ctx, plan, EngineGitHub, root)
		}, e.CodePipelineDrift},
		{"run generate --runtime --check", func(ctx context.Context, root string) error {
			return PrepareGitHubRuntimeBundle(ctx, fresh, root, true)
		}, e.CodePipelineDrift},
		{"run generate --runtime", func(ctx context.Context, root string) error {
			return PrepareGitHubRuntimeBundle(ctx, fresh, root, false)
		}, e.CodePipelineDrift},
		{"check-runtime-inputs", func(ctx context.Context, root string) error {
			return CheckGitHubRuntimeInputs(ctx, plan, root)
		}, e.CodePipelineDrift},
		{"install-runtime-inputs", func(ctx context.Context, root string) error {
			stageGitHubRuntimeInputs(t, root, fresh)
			return InstallGitHubRuntimeInputs(ctx, plan, root)
		}, e.CodePipelineDrift},
		{"describe-action", func(ctx context.Context, root string) error {
			return WriteGitHubActionData(ctx, plan, selector, filepath.Join(root, "github-output"))
		}, ""},
	} {
		for _, outcome := range []string{"admitted", "tampered", "canceled"} {
			t.Run(owner.name+"/"+outcome, func(t *testing.T) {
				forbidGenerationSeam(t)
				root := copyGitHubRuntimeRoot(t, original)
				ctx, cancel := context.WithCancel(t.Context())
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
	stageCommands := map[string]bool{
		"factory github check-runtime-inputs": true, "factory github ci-download": true,
		"factory github ci-fetch-locators": true, "factory github ci-upload": true,
		"factory github describe-action": true, "factory github install-runtime-inputs": true,
		"factory github pack-outputs": true, "factory github prepare-images": true,
		"factory github recover": true, "factory github run-disk": true, "factory github run-node": true,
		"run finalize": true, "run generate": true, "run qualification-source": true,
		"run qualify": true, "run smoke-worker": true,
	}
	generationFlags := map[string]bool{"--engine": true, "--definition": true, "--environment": true,
		"--root": true, "--check": true, "--runtime": true, "--source-commit": true}
	invocation := regexp.MustCompile(`(?m)(?:^|[\s;&|(])(?:exec[ \t]+)?\./(?:\.nvkr/ci-bootstrap/)?nvkr-exec[ \t]+(.*)$`)
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
				script = strings.ReplaceAll(script, "\\\n", " ")
				generations := 0
				for _, match := range invocation.FindAllStringSubmatch(script, -1) {
					words := strings.Fields(match[1])
					length := 2
					if len(words) > 0 && words[0] == "factory" {
						length = 3
					}
					if len(words) < length || !stageCommands[strings.Join(words[:length], " ")] {
						t.Errorf("%s invokes a non-core stage command: %s", member.Path, match[1])
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
						if strings.HasPrefix(word, "--") && !generationFlags[word] {
							t.Errorf("%s selects generation with %s: %s", member.Path, word, match[1])
						}
						admission = admission || word == "--check" || word == "--runtime"
					}
					if !admission {
						t.Errorf("%s regenerates inside a stage: %s", member.Path, match[1])
					}
				}
				if strings.Count(script, "run generate") != generations {
					t.Errorf("%s reaches generation outside a recognized core invocation", member.Path)
				}
			}
		}
	}
	for command := range stageCommands {
		if seen[command] == 0 {
			t.Errorf("admitted stage command %q no longer appears in any committed closure", command)
		}
	}
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
