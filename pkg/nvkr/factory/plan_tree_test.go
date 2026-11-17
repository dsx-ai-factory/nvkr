// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestPlanTreeDirectoryLimitAdmittedBeforeWrites(t *testing.T) {
	bundle, err := RenderGitHub(t.Context(), githubRuntimePlanFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= maxGeneratedDirEntries; i++ {
		raw := []byte(fmt.Sprintf("{\"extra\":%d}\n", i))
		bundle.Files[planStorePath(raw, ".json")] = raw
	}
	root := t.TempDir()
	assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), root, bundle), e.CodeInvalidCatalog)
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("oversized tree performed output work", err)
	}
}

func TestPlanTreeWriterSyncsEveryDirectory(t *testing.T) {
	plan := githubRuntimePlanFixture(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	previous := syncGeneratedDirectory
	t.Cleanup(func() { syncGeneratedDirectory = previous })
	var synced []string
	syncGeneratedDirectory = func(directory *os.File) error {
		synced = append(synced, directory.Name())
		return nil
	}
	if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
	// Every newly created directory entry must be covered by its parent's sync,
	// after descendants. Leaf-only syncing loses the tree on a crash.
	if err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		index := slices.Index(synced, name)
		if index < 0 {
			t.Errorf("directory not synced: %s", name)
		}
		if name != root && slices.Index(synced, filepath.Dir(name)) <= index {
			t.Errorf("parent was not synced after directory %s", name)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanTreeAdditionalBuilders(t *testing.T) {
	for _, fixture := range []struct {
		name string
		plan func(*testing.T) *Plan
	}{
		{"pinned tool", func(t *testing.T) *Plan {
			d, env, stack := pinnedToolFactoryInputs(t, nil)
			plan, err := Compile(t.Context(), d, env, stack, "test")
			if err != nil {
				t.Fatal(err)
			}
			return plan
		}},
		{"VM argv", func(t *testing.T) *Plan {
			plan, _ := compiledCommandVMNodeWithEncryption(t, "none")
			return plan
		}},
		{"Packer", func(t *testing.T) *Plan {
			plan, _ := compiledPackerVMNode(t)
			return plan
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			plan := fixture.plan(t)
			files, err := renderPlanTree(t.Context(), plan, FactoryPlanPath)
			if err != nil {
				t.Fatal(err)
			}
			_, raw, err := joinPlanTree(t.Context(), FactoryPlanPath, files)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, mustCanonical(t, plan)) {
				t.Fatal("builder fields changed")
			}
		})
	}
}

func TestPlanTreeCommandBytes(t *testing.T) {
	for _, test := range []struct {
		name    string
		argv    []string
		extract bool
	}{
		{"single line", []string{"sh", "-c", "printf x"}, false},
		{"trailing LF", []string{"bash", "-euc", "printf x\n"}, true},
		{"no trailing LF", []string{"sh", "-c", "printf x\nprintf y"}, true},
		{"arbitrary argv", []string{"tool", "--data", "a\r\nb"}, true},
		{"multiple multiline", []string{"tool", "a\nb", "c\nd"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph := map[string]any{"nodes": []any{map[string]any{"id": "build/widget", "component": "widget", "command": test.argv}}}
			step := planFields{"executionGraph": mustCanonical(t, graph)}
			before := mustCanonical(t, step)
			files := map[string][]byte{}
			if err := sliceTreeCommands(step, files); err != nil {
				t.Fatal(err)
			}
			if (len(files) > 0) != test.extract {
				t.Fatal("wrong extraction decision")
			}
			if test.extract {
				for _, arg := range test.argv {
					if !strings.Contains(arg, "\n") {
						continue
					}
					name := planStorePath([]byte(arg), ".sh")
					if !bytes.Equal(files[name], []byte(arg)) {
						t.Fatal("script bytes changed")
					}
					if !bytes.Contains(step["executionGraph"], mustCanonical(t, map[string]string{"file": name})) {
						t.Fatal("missing file operand")
					}
				}
			}
			if err := expandTreeCommands(step, files, MaxPlanBytes); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, mustCanonical(t, step)) {
				t.Fatal("argv bytes changed")
			}
		})
	}
}

func TestPlanTreeRejectsUnsafeCommandReferences(t *testing.T) {
	for _, test := range []struct {
		name    string
		operand any
		budget  int64
	}{
		{"parent", map[string]string{"file": "../body.sh"}, MaxPlanBytes},
		{"absolute", map[string]string{"file": "/body.sh"}, MaxPlanBytes},
		{"extra key", map[string]string{"file": "body.sh", "other": "x"}, MaxPlanBytes},
		{"missing file", map[string]string{"file": "absent.sh"}, MaxPlanBytes},
		{"null", nil, MaxPlanBytes},
		{"expansion bound", map[string]string{"file": planStorePath([]byte("hello\n"), ".sh")}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph := map[string]any{"nodes": []any{map[string]any{"command": []any{test.operand}}}}
			step := planFields{"executionGraph": mustCanonical(t, graph)}
			err := expandTreeCommands(step, map[string][]byte{planStorePath([]byte("hello\n"), ".sh"): []byte("hello\n")}, test.budget)
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
		})
	}
}

func TestPlanTreeRuntimeCarrierStaysPacked(t *testing.T) {
	plan, _, root := runtimeInputsFixture(t)
	if err := StageGitHubRuntimeInputs(t.Context(), plan, root); err != nil {
		t.Fatal(err)
	}
	packed, err := os.ReadFile(filepath.Join(root, githubRuntimeExportDirectory, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packed, mustCanonical(t, plan)) {
		t.Fatal("runtime carrier must contain packed canonical Plan")
	}
	before := githubRuntimeTree(t, root)
	assertFactoryErrorCode(t, StageGitHubRuntimeInputs(t.Context(), plan, root), e.CodePipelineDrift)
	if len(before) != len(githubRuntimeTree(t, root)) {
		t.Fatal("repeated staging changed inventory")
	}
	if err := os.Rename(filepath.Join(root, githubRuntimeExportDirectory), filepath.Join(root, githubRuntimeInputDirectory)); err != nil {
		t.Fatal(err)
	}
	if err := InstallGitHubRuntimeInputs(t.Context(), plan, root); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPlanContext(t.Context(), filepath.Join(root, githubLayoutFor(plan).planPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packed, mustCanonical(t, loaded)) {
		t.Fatal("installed tree changed carrier Plan")
	}
}

func TestPlanTreeRuntimeCohortSplitRequiresRegeneration(t *testing.T) {
	before := recompileGitHubStabilityPlan(t, githubRuntimePlanFixture(t), func(in *githubStabilityInputs) {
		in.definition.Outputs[0].Components = []string{"kubeadm", "kubelet"}
		for _, name := range []string{"kubeadm", "kubelet"} {
			component := in.catalog.Components[name]
			component.VendorCommand = []string{"sh", "-c", "printf vendor\n"}
			in.catalog.Components[name] = component
		}
	})
	after := recompileGitHubStabilityPlan(t, before, func(in *githubStabilityInputs) {
		component := in.catalog.Components["kubeadm"]
		component.Commit = nextGitHubStabilityPin(t, component.Commit)
		component.TreeHash = nextGitHubStabilityPin(t, component.TreeHash)
		in.catalog.Components["kubeadm"] = component
	})
	root := t.TempDir()
	baseline := writeGitHubRuntimeBaseline(t, root, before)
	fresh, err := RenderGitHub(t.Context(), after)
	if err != nil {
		t.Fatal(err)
	}
	sharedScripts := 0
	for name := range fresh.Files {
		if isPlanStoreScript(name) && baseline.Files[name] != nil {
			sharedScripts++
		}
	}
	beforeBindings, err := runtimeScriptBindings(before)
	if err != nil {
		t.Fatal(err)
	}
	afterBindings, err := runtimeScriptBindings(after)
	if err != nil || sharedScripts == 0 || bytes.Equal(beforeBindings, afterBindings) {
		t.Fatal("fixture must split a cohort while retaining its shared script bytes", err)
	}
	err = PrepareGitHubRuntimeBundle(t.Context(), after, root, false)
	assertFactoryErrorCode(t, err, e.CodePipelineDrift)
	if !strings.Contains(err.Error(), "regenerate") {
		t.Fatal("missing regeneration guidance", err)
	}
	if err := CheckGeneratedBundle(t.Context(), root, baseline); err != nil {
		t.Fatal("rejected refresh changed baseline", err)
	}
}

func TestPlanTreeMaintainedIdentity(t *testing.T) {
	paths, err := filepath.Glob("../../../.github/nvkr-*/plan.json")
	if err != nil || len(paths) == 0 {
		t.Fatal("maintained Plans missing", err)
	}
	for _, filename := range paths {
		t.Run(filepath.Base(filepath.Dir(filename)), func(t *testing.T) {
			plan, err := LoadPlanContext(t.Context(), filename)
			if err != nil {
				t.Fatal(err)
			}
			planPath := githubLayoutFor(plan).planPath
			files, err := renderPlanTree(t.Context(), plan, planPath)
			if err != nil {
				t.Fatal(err)
			}
			decoded, raw, err := joinPlanTree(t.Context(), planPath, files)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, mustCanonical(t, plan)) {
				t.Fatal("tree changed canonical Plan bytes")
			}
			before, err := plan.Digest()
			if err != nil {
				t.Fatal(err)
			}
			after, err := decoded.Digest()
			if err != nil || before != after {
				t.Fatal("tree changed Plan identity", err)
			}
			for i, build := range plan.Builds {
				if build.BuildDigest != decoded.Builds[i].BuildDigest || !bytes.Equal(mustCanonical(t, build.Graph), mustCanonical(t, decoded.Builds[i].Graph)) {
					t.Fatalf("tree changed build %s", build.Name)
				}
			}
			t.Logf("planDigest=%s packedSha256=%s builds=%d members=%d", before, pipeline.FileDigest(raw), len(plan.Builds), len(files))
		})
	}
}

func TestPlanTreeRejectsMalformedStorage(t *testing.T) {
	plan := githubRuntimePlanFixture(t)
	planPath := githubLayoutFor(plan).planPath
	for _, test := range []struct {
		name   string
		change func(map[string][]byte)
	}{
		{"unknown storage", func(files map[string][]byte) {
			files[planPath] = bytes.ReplaceAll(files[planPath], []byte(planTreeStorage), []byte("unknown"))
		}},
		{"duplicate field", func(files map[string][]byte) {
			files[planPath] = bytes.Replace(files[planPath], []byte("{\n"), []byte("{\n  \"storage\": \"duplicate\",\n"), 1)
		}},
		{"trailing document", func(files map[string][]byte) { files[planPath] = append(files[planPath], []byte("{}")...) }},
		{"alternate whitespace", func(files map[string][]byte) { files[planPath] = append(files[planPath], '\n') }},
		{"missing step", func(files map[string][]byte) {
			delete(files, treeStepPath(t, planPath, files, 0))
		}},
		{"extra addressed record", func(files map[string][]byte) {
			if _, err := storeTreeJSON(map[string]string{"unexpected": "record"}, files); err != nil {
				t.Fatal(err)
			}
		}},
		{"extra step", func(files map[string][]byte) {
			files[path.Dir(planPath)+"/steps/unexpected/step.json"] = []byte("{}\n")
		}},
		{"root-owned step field", func(files map[string][]byte) {
			name := treeStepPath(t, planPath, files, 0)
			var step planFields
			if err := json.Unmarshal(files[name], &step); err != nil {
				t.Fatal(err)
			}
			step["catalogDigest"] = mustCanonical(t, plan.Builds[0].CatalogDigest)
			raw, err := treeJSON(step)
			if err != nil {
				t.Fatal(err)
			}
			files[name] = raw
		}},
		{"missing catalog snapshot", func(files map[string][]byte) {
			var root planFields
			if err := json.Unmarshal(files[planPath], &root); err != nil {
				t.Fatal(err)
			}
			root["catalogSnapshots"] = json.RawMessage(`{}`)
			raw, err := treeJSON(root)
			if err != nil {
				t.Fatal(err)
			}
			files[planPath] = raw
		}},

		{"mismatched name", func(files map[string][]byte) {
			name := treeStepPath(t, planPath, files, 0)
			files[name] = bytes.Replace(files[name], []byte(`"name": "`+plan.Builds[0].Name+`"`), []byte(`"name": "other"`), 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			files, err := renderPlanTree(t.Context(), plan, planPath)
			if err != nil {
				t.Fatal(err)
			}
			test.change(files)
			_, _, err = joinPlanTree(t.Context(), planPath, files)
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
		})
	}
}

func TestPlanTreeLoadAdmitsCompleteInventory(t *testing.T) {
	plan := githubRuntimePlanFixture(t)
	for _, mutation := range []string{"none", "extra", "extra directory", "missing", "missing directory", "missing manifest", "unlisted root", "edited", "symlink", "manifest digest"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			bundle := writeGitHubRuntimeBaseline(t, root, plan)
			step := filepath.Join(root, treeStepPath(t, bundle.PlanPath, bundle.Files, 0))
			switch mutation {
			case "extra":
				if err := os.WriteFile(step+".extra", []byte("extra"), 0644); err != nil {
					t.Fatal(err)
				}
			case "extra directory":
				if err := os.Mkdir(step+".extra", 0755); err != nil {
					t.Fatal(err)
				}
			case "missing directory":
				if err := os.Rename(filepath.Dir(step), filepath.Join(root, "saved-directory")); err != nil {
					t.Fatal(err)
				}
			case "missing manifest":
				if err := os.Rename(filepath.Join(root, bundle.ManifestPath), filepath.Join(root, "saved-manifest")); err != nil {
					t.Fatal(err)
				}

			case "missing":
				if err := os.Rename(step, filepath.Join(root, "saved.json")); err != nil {
					t.Fatal(err)
				}
			case "edited":
				if err := os.WriteFile(step, []byte("{}\n"), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				other := filepath.Join(root, "saved.json")
				if err := os.Rename(step, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, step); err != nil {
					t.Fatal(err)
				}
			case "manifest digest", "unlisted root":
				if mutation == "manifest digest" {
					bundle.PlanDigest = "sha256:" + strings.Repeat("a", 64)
				} else {
					delete(bundle.Files, bundle.PlanPath)
				}
				_, raw, err := ManifestFor(bundle)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, bundle.ManifestPath), raw, 0644); err != nil {
					t.Fatal(err)
				}
			}
			loaded, digest, err := loadPlanDocument(t.Context(), filepath.Join(root, bundle.PlanPath))
			if mutation == "none" {
				if err != nil {
					t.Fatal(err)
				}
				if digest != pipeline.FileDigest(mustCanonical(t, plan)) || !bytes.Equal(mustCanonical(t, loaded), mustCanonical(t, plan)) {
					t.Fatal("Plan or diagnostic digest changed")
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
				if mutation == "unlisted root" && !strings.Contains(err.Error(), "manifest does not list the root") {
					t.Fatal("missing root diagnostic", err)
				}
			}
		})
	}
}

func TestPlanTreeRenderRejectsUnsafeLayout(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Plan)
	}{
		{"unsafe target", func(plan *Plan) { plan.Builds[0].Name = "../outside" }},
		{"duplicate target", func(plan *Plan) {
			plan.Builds = append(plan.Builds[:1:1], plan.Builds[0])
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := githubRuntimePlanFixture(t)
			test.mutate(plan)
			_, err := renderPlanTree(t.Context(), plan, githubLayoutFor(plan).planPath)
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
		})
	}
}

func treeStepPath(t *testing.T, planPath string, files map[string][]byte, index int) string {
	t.Helper()
	var root struct{ Builds []struct{ File string } }
	if err := json.Unmarshal(files[planPath], &root); err != nil {
		t.Fatal(err)
	}
	if index >= len(root.Builds) || !isPlanStoreRecord(root.Builds[index].File) {
		t.Fatal("missing step reference")
	}
	return root.Builds[index].File
}
