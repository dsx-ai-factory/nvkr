// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func sharedPlanStoreFixtures(t *testing.T) ([]*Plan, []*GeneratedBundle) {
	t.Helper()
	first := githubRuntimePlanFixture(t)
	second := recompileGitHubStabilityPlan(t, first, func(in *githubStabilityInputs) {
		in.environment.GitHubBundle = "other-project"
	})
	plans := []*Plan{first, second}
	bundles := make([]*GeneratedBundle, len(plans))
	for i, plan := range plans {
		var err error
		bundles[i], err = RenderGitHub(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
	}
	return plans, bundles
}

func TestPlanStoreSharesFrozenRecords(t *testing.T) {
	plans, bundles := sharedPlanStoreFixtures(t)
	var first, second planFields
	if err := json.Unmarshal(bundles[0].Files[bundles[0].PlanPath], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bundles[1].Files[bundles[1].PlanPath], &second); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"graph", "catalogSnapshots"} {
		if !bytes.Equal(first[field], second[field]) {
			t.Fatalf("identical %s is not shared across bundles", field)
		}
	}
	const body = "printf shared-command\\n\nprintf done\n"
	plan := recompileGitHubStabilityPlan(t, plans[0], func(in *githubStabilityInputs) {
		component := in.catalog.Components["kubelet"]
		component.BuildCommand = []string{"sh", "-c", body}
		in.catalog.Components["kubelet"] = component
	})
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	scriptPath := testNamedStorePath(t, "scripts/kubelet/build", []byte(body), ".sh")
	if string(bundle.Files[scriptPath]) != body {
		t.Fatal("exact script bytes were not stored")
	}
	componentPath := ""
	for name, raw := range bundle.Files {
		if !isPlanStoreRecord(name) {
			continue
		}
		if bytes.Contains(raw, mustCanonical(t, body)) {
			t.Fatal("a frozen JSON object retained an inline copy of the script")
		}
		var record planFields
		if json.Unmarshal(raw, &record) == nil && bytes.Contains(record["buildCommand"], []byte(scriptPath)) {
			componentPath = name
		}
	}
	if componentPath == "" {
		t.Fatal("snapshot component did not reference the script")
	}
	references := 0
	for _, raw := range bundle.Files {
		references += strings.Count(string(raw), componentPath)
	}
	if references < 2 {
		t.Fatal("snapshot and resolved-component list did not share the component record")
	}
	_, packed, err := joinPlanTree(t.Context(), bundle.PlanPath, bundle.Files)
	if err != nil || !bytes.Equal(packed, mustCanonical(t, plan)) {
		t.Fatal("shared frozen records changed the Plan", err)
	}
}

func TestPlanStoreRetirementPreservesOtherOwners(t *testing.T) {
	plans, bundles := sharedPlanStoreFixtures(t)
	root := t.TempDir()
	for _, bundle := range bundles {
		if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
	}
	fresh := make([]*GeneratedBundle, len(plans))
	for i, plan := range plans {
		var err error
		fresh[i], err = RenderGitHub(t.Context(), freshGitHubRuntimePins(t, plan))
		if err != nil {
			t.Fatal(err)
		}
	}
	var retired []string
	for name := range bundles[0].Files {
		if isPlanStoreRecord(name) && bundles[1].Files[name] != nil && fresh[0].Files[name] == nil && fresh[1].Files[name] == nil {
			retired = append(retired, name)
		}
	}
	if len(retired) == 0 {
		t.Fatal("fixture has no shared objects retired by both owners")
	}
	t.Run("missing shared object is not an interrupted retirement", func(t *testing.T) {
		broken := copyGitHubRuntimeRoot(t, root)
		if err := os.Remove(filepath.Join(broken, retired[0])); err != nil {
			t.Fatal(err)
		}
		before := githubRuntimeTree(t, broken)
		assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), broken, fresh[0]), e.CodePipelineDrift)
		if !bytes.Equal(mustCanonical(t, before), mustCanonical(t, githubRuntimeTree(t, broken))) {
			t.Fatal("rejected retirement changed bundle payloads")
		}
	})
	if err := WriteGeneratedBundle(t.Context(), root, fresh[0]); err != nil {
		t.Fatal(err)
	}
	if err := CheckGeneratedBundle(t.Context(), root, bundles[1]); err != nil {
		t.Fatal("first refresh damaged the other bundle", err)
	}
	for _, name := range retired {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatal("shared object retired before its last owner", err)
		}
	}
	if err := WriteGeneratedBundle(t.Context(), root, fresh[1]); err != nil {
		t.Fatal(err)
	}
	for _, name := range retired {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("last-owner retirement left an obsolete object", err)
		}
	}
	for _, bundle := range fresh {
		if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlanStoreConcurrentWriters(t *testing.T) {
	plans, bundles := sharedPlanStoreFixtures(t)
	root := t.TempDir()
	results := make(chan error, len(bundles))
	for _, bundle := range bundles {
		go func() { results <- WriteGeneratedBundle(t.Context(), root, bundle) }()
	}
	for range bundles {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	if t.Failed() {
		return
	}
	for i, bundle := range bundles {
		if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadPlanContext(t.Context(), filepath.Join(root, bundle.PlanPath))
		if err != nil || !bytes.Equal(mustCanonical(t, loaded), mustCanonical(t, plans[i])) {
			t.Fatal("concurrent publication changed frozen Plan bytes", err)
		}
	}
}

func TestPlanStoreChecksOtherOwnerAndUnownedObjects(t *testing.T) {
	_, bundles := sharedPlanStoreFixtures(t)
	original := t.TempDir()
	for _, bundle := range bundles {
		if err := WriteGeneratedBundle(t.Context(), original, bundle); err != nil {
			t.Fatal(err)
		}
	}
	other := ""
	for name := range bundles[1].Files {
		if isPlanStoreRecord(name) && bundles[0].Files[name] == nil {
			other = name
			break
		}
	}
	if other == "" {
		t.Fatal("fixture must have a record owned only by the other bundle")
	}
	for _, test := range []struct {
		name   string
		change func(*testing.T, string)
	}{
		{"missing", func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, other)); err != nil {
				t.Fatal(err)
			}
		}},
		{"tampered", func(t *testing.T, root string) {
			writeGeneratedFixture(t, filepath.Join(root, other), []byte("{}\n"))
		}},
		{"extra", func(t *testing.T, root string) {
			raw := []byte("unowned\n")
			writeGeneratedFixture(t, filepath.Join(root, planStorePath(raw, ".sh")), raw)
		}},
		{"mode", func(t *testing.T, root string) {
			if err := os.Chmod(filepath.Join(root, other), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := copyGitHubRuntimeRoot(t, original)
			test.change(t, root)
			assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, bundles[0]), e.CodePipelineDrift)
			_, err := LoadPlanContext(t.Context(), filepath.Join(root, bundles[0].PlanPath))
			if err != nil {
				t.Fatal("unrelated store damage blocked own Plan load", err)
			}
			err = WriteGeneratedBundle(t.Context(), root, bundles[0])
			if test.name == "extra" {
				if err != nil {
					t.Fatal("generation did not sweep unowned objects", err)
				}
				if err := CheckGeneratedBundle(t.Context(), root, bundles[0]); err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
			}
		})
	}
}

// Removing or renaming a whole owner leaves no manifest to authorize private
// retirement. The content-addressed store has its own bounded sweep authority.
func TestPlanStoreRecoversRemovedAndRenamedBundles(t *testing.T) {
	plans, bundles := sharedPlanStoreFixtures(t)
	for _, rename := range []bool{false, true} {
		t.Run(map[bool]string{false: "remove", true: "rename"}[rename], func(t *testing.T) {
			root := t.TempDir()
			for _, bundle := range bundles {
				if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{filepath.Dir(bundles[1].ManifestPath), bundles[1].WorkflowPath} {
				if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := LoadPlanContext(t.Context(), filepath.Join(root, bundles[0].PlanPath)); err != nil {
				t.Fatal(err)
			}
			before := githubRuntimeTree(t, root)
			err := CheckGeneratedBundle(t.Context(), root, bundles[0])
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
			if !strings.Contains(err.Error(), "run generate") {
				t.Fatal("missing sweep recovery guidance", err)
			}
			if !bytes.Equal(mustCanonical(t, before), mustCanonical(t, githubRuntimeTree(t, root))) {
				t.Fatal("drift check mutated files")
			}
			incoming := bundles[0]
			if rename {
				renamed := recompileGitHubStabilityPlan(t, plans[1], func(in *githubStabilityInputs) { in.environment.GitHubBundle = "renamed-project" })
				incoming, err = RenderGitHub(t.Context(), renamed)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := WriteGeneratedBundle(t.Context(), root, incoming); err != nil {
				t.Fatal(err)
			}
			for _, bundle := range []*GeneratedBundle{bundles[0], incoming} {
				if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
					t.Fatal(err)
				}
			}
			for name := range bundles[1].Files {
				if isPlanStorePath(name) && bundles[0].Files[name] == nil && incoming.Files[name] == nil {
					if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("departed owner's object survived sweep", name, err)
					}
				}
			}
		})
	}
}

func TestPlanStoreRecoversInterruptedWrites(t *testing.T) {
	plans, bundles := sharedPlanStoreFixtures(t)
	freshPlan := recompileGitHubStabilityPlan(t, freshGitHubRuntimePins(t, plans[0]), func(in *githubStabilityInputs) {
		in.environment.Metadata.Description = "changed recovery fixture"
	})
	fresh, err := RenderGitHub(t.Context(), freshPlan)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"staging", "objects published", "manifest published", "old objects retired before manifest"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			for _, bundle := range bundles {
				if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
					t.Fatal(err)
				}
			}
			switch phase {
			case "staging":
				name := filepath.Join(root, PlanStoreDirectory, ".nvkr-run-stage-interrupted")
				if err := os.WriteFile(name, []byte("partial"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "objects published":
				for name, raw := range fresh.Files {
					if isPlanStorePath(name) && bundles[0].Files[name] == nil {
						writeGeneratedFixture(t, filepath.Join(root, name), raw)
					}
				}
			case "manifest published":
				for name, raw := range fresh.Files {
					writeGeneratedFixture(t, filepath.Join(root, name), raw)
				}
				_, manifest, err := ManifestFor(fresh)
				if err != nil {
					t.Fatal(err)
				}
				writeGeneratedFixture(t, filepath.Join(root, fresh.ManifestPath), manifest)
			case "old objects retired before manifest":
				removed := 0
				for name := range bundles[0].Files {
					if isPlanStorePath(name) && bundles[1].Files[name] == nil && fresh.Files[name] == nil {
						if err := os.Remove(filepath.Join(root, name)); err != nil {
							t.Fatal(err)
						}
						removed++
					}
				}
				if removed == 0 {
					t.Fatal("fixture did not interrupt an obsolete object retirement")
				}
			}
			if _, err := LoadPlanContext(t.Context(), filepath.Join(root, bundles[1].PlanPath)); err != nil {
				t.Fatal("interruption blocked neighboring Plan", err)
			}
			// Recover with changed inputs, not only by retrying the same attempt.
			if err := WriteGeneratedBundle(t.Context(), root, fresh); err != nil {
				t.Fatal(err)
			}
			for _, bundle := range []*GeneratedBundle{fresh, bundles[1]} {
				if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPlanStoreReaderDuringOtherOwnerWrite(t *testing.T) {
	plans, bundles := sharedPlanStoreFixtures(t)
	root := t.TempDir()
	for _, bundle := range bundles {
		if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
	}
	fresh, err := RenderGitHub(t.Context(), freshGitHubRuntimePins(t, plans[0]))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	staged, resume := make(chan struct{}), make(chan struct{})
	previous := syncStagedMember
	var once sync.Once
	syncStagedMember = func(file *os.File) error {
		once.Do(func() {
			close(staged)
			select {
			case <-resume:
			case <-ctx.Done():
			}
		})
		return previous(file)
	}
	result := make(chan error, 1)
	go func() { result <- WriteGeneratedBundle(ctx, root, fresh) }()
	// Always stop and join before restoring the injection or failing the test.
	select {
	case <-staged:
	case <-ctx.Done():
	}
	loaded, loadErr := LoadPlanContext(ctx, filepath.Join(root, bundles[1].PlanPath))
	close(resume)
	writeErr := <-result
	syncStagedMember = previous
	if loadErr != nil || writeErr != nil {
		t.Fatal("concurrent read/write", loadErr, writeErr)
	}
	if !bytes.Equal(mustCanonical(t, loaded), mustCanonical(t, plans[1])) {
		t.Fatal("neighbor Plan changed during publication")
	}
}

func TestPlanStoreAggregateBoundBeforePublication(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []int{0, 1} {
		t.Run(map[int]string{0: "at bound", 1: "above bound"}[extra], func(t *testing.T) {
			files := map[string][]byte{}
			for _, fill := range []byte{'a', 'b'} {
				raw := bytes.Repeat([]byte{fill}, int(maxGeneratedFileBytes))
				files[planStorePath(raw, ".sh")] = raw
			}
			if extra != 0 {
				raw := []byte("c")
				files[planStorePath(raw, ".sh")] = raw
			}
			_, err := inspectPlanStore(t.Context(), root, &GeneratedBundle{Engine: EngineGitHub, Files: files}, true)
			if extra == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
			}
			if _, err := os.Lstat(filepath.Join(root, PlanStoreDirectory)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("admission wrote the store", err)
			}
		})
	}
}

func TestPlanStoreScriptSurvivesUntilLastOwner(t *testing.T) {
	plans, _ := sharedPlanStoreFixtures(t)
	root := t.TempDir()
	const oldBody = "printf shared\nprintf old\n"
	const newBody = "printf shared\nprintf new\n"
	bundles := make([]*GeneratedBundle, len(plans))
	render := func(plan *Plan, body string) *GeneratedBundle {
		changed := recompileGitHubStabilityPlan(t, plan, func(in *githubStabilityInputs) {
			component := in.catalog.Components["kubelet"]
			component.BuildCommand = []string{"sh", "-c", body}
			in.catalog.Components["kubelet"] = component
		})
		bundle, err := RenderGitHub(t.Context(), changed)
		if err != nil {
			t.Fatal(err)
		}
		return bundle
	}
	for i, plan := range plans {
		bundles[i] = render(plan, oldBody)
		if err := WriteGeneratedBundle(t.Context(), root, bundles[i]); err != nil {
			t.Fatal(err)
		}
	}
	name := filepath.Join(root, testNamedStorePath(t, "scripts/kubelet/build", []byte(oldBody), ".sh"))
	for i, plan := range plans {
		bundles[i] = render(plan, newBody)
		if err := WriteGeneratedBundle(t.Context(), root, bundles[i]); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			raw, err := os.ReadFile(name)
			if err != nil || string(raw) != oldBody {
				t.Fatal("shared script retired while another owner references it", err)
			}
		} else if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("last owner left an obsolete script", err)
		}
		if err := CheckGeneratedBundle(t.Context(), root, bundles[1]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlanStoreSweepPreservesUnsafeGarbage(t *testing.T) {
	_, bundles := sharedPlanStoreFixtures(t)
	original := t.TempDir()
	if err := WriteGeneratedBundle(t.Context(), original, bundles[0]); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"bad address", "wrong mode", "staging symlink", "staging directory", "unknown filename", "bad manifest", "index symlink", "index mode"} {
		t.Run(kind, func(t *testing.T) {
			root := copyGitHubRuntimeRoot(t, original)
			raw := []byte("orphan\n")
			name := filepath.Join(root, planStorePath(raw, ".sh"))
			switch kind {
			case "bad address":
				writeGeneratedFixture(t, name, []byte("changed\n"))
			case "wrong mode":
				if err := os.WriteFile(name, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "staging symlink":
				if err := os.Symlink(filepath.Join(root, bundles[0].PlanPath), filepath.Join(root, PlanStoreDirectory, ".nvkr-run-stage-link")); err != nil {
					t.Fatal(err)
				}
			case "staging directory":
				if err := os.Mkdir(filepath.Join(root, PlanStoreDirectory, ".nvkr-run-stage-dir"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "unknown filename":
				writeGeneratedFixture(t, filepath.Join(root, PlanStoreDirectory, "notes.txt"), raw)
			case "bad manifest":
				writeGeneratedFixture(t, filepath.Join(root, bundles[1].ManifestPath), []byte("{"))
			case "index symlink":
				index := filepath.Join(root, PlanStoreIndexPath)
				if err := os.Remove(index); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, bundles[0].PlanPath), index); err != nil {
					t.Fatal(err)
				}
			case "index mode":
				if err := os.Chmod(filepath.Join(root, PlanStoreIndexPath), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := LoadPlanContext(t.Context(), filepath.Join(root, bundles[0].PlanPath)); err != nil {
				t.Fatal("unrelated garbage blocked own load", err)
			}
			before := githubRuntimeTree(t, root)
			assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), root, bundles[0]), e.CodePipelineDrift)
			if !bytes.Equal(mustCanonical(t, before), mustCanonical(t, githubRuntimeTree(t, root))) {
				t.Fatal("unsafe garbage was modified")
			}
		})
	}
}

func testNamedStorePath(t *testing.T, logical string, raw []byte, extension string) string {
	t.Helper()
	name, err := namedPlanStorePath(logical, raw, extension)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func TestPlanStoreNamesAndIndexDoNotDependOnGenerationOrder(t *testing.T) {
	plans, bundles := sharedPlanStoreFixtures(t)
	// These bundles deliberately contain different variants of the same targets.
	var err error
	bundles[1], err = RenderGitHub(t.Context(), freshGitHubRuntimePins(t, plans[1]))
	if err != nil {
		t.Fatal(err)
	}
	roots := []string{t.TempDir(), t.TempDir()}
	for i, root := range roots {
		order := []int{0, 1}
		if i == 1 {
			order = []int{1, 0}
		}
		for _, index := range order {
			if err := WriteGeneratedBundle(t.Context(), root, bundles[index]); err != nil {
				t.Fatal(err)
			}
		}
		for _, bundle := range bundles {
			for name, expected := range bundle.Files {
				raw, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || !bytes.Equal(raw, expected) {
					t.Fatal("neighbor changed an object's path or bytes", name, err)
				}
			}
		}
	}
	first, err := os.ReadFile(filepath.Join(roots[0], PlanStoreIndexPath))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(roots[1], PlanStoreIndexPath))
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("generation order changed the index", err)
	}
	var groups map[string][]planStoreVariant
	if err := json.Unmarshal(first, &groups); err != nil {
		t.Fatal(err)
	}
	diverged := false
	for logical, variants := range groups {
		if strings.HasPrefix(logical, "steps/") && len(variants) > 1 {
			diverged = true
		}
		for _, variant := range variants {
			if len(variant.Bundles) == 0 || !strings.Contains(variant.File, "@") || !strings.HasPrefix(variant.SHA256, "sha256:") {
				t.Fatal("index omits a variant's name, full digest or users", variant)
			}
		}
	}
	if !diverged {
		t.Fatal("index did not group divergent target variants")
	}
	for _, name := range []string{filepath.Dir(bundles[1].ManifestPath), bundles[1].WorkflowPath} {
		if err := os.RemoveAll(filepath.Join(roots[0], name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteGeneratedBundle(t.Context(), roots[0], bundles[0]); err != nil {
		t.Fatal(err)
	}
	for name, expected := range bundles[0].Files {
		raw, err := os.ReadFile(filepath.Join(roots[0], name))
		if err != nil || !bytes.Equal(raw, expected) {
			t.Fatal("removing neighbor changed object", name, err)
		}
	}
	writeGeneratedFixture(t, filepath.Join(roots[0], PlanStoreIndexPath), []byte("{}\n"))
	assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), roots[0], bundles[0]), e.CodePipelineDrift)
	if _, err := LoadPlanContext(t.Context(), filepath.Join(roots[0], bundles[0].PlanPath)); err != nil {
		t.Fatal("descriptive index blocked own Plan", err)
	}
	if err := WriteGeneratedBundle(t.Context(), roots[0], bundles[0]); err != nil {
		t.Fatal(err)
	}
	if err := CheckGeneratedBundle(t.Context(), roots[0], bundles[0]); err != nil {
		t.Fatal(err)
	}
}

func TestPlanStoreRecoveryDiagnostics(t *testing.T) {
	_, bundles := sharedPlanStoreFixtures(t)
	original := t.TempDir()
	for _, bundle := range bundles {
		if err := WriteGeneratedBundle(t.Context(), original, bundle); err != nil {
			t.Fatal(err)
		}
	}
	for _, damage := range []string{"manifest", "shared object", "empty directory"} {
		t.Run(damage, func(t *testing.T) {
			root := copyGitHubRuntimeRoot(t, original)
			var want []string
			switch damage {
			case "manifest":
				writeGeneratedFixture(t, filepath.Join(root, bundles[1].ManifestPath), []byte("{"))
				want = []string{bundles[1].ManifestPath, "restore it from version control", "then regenerate"}
			case "shared object":
				for _, name := range sortedFileNames(bundles[0].Files) {
					if isPlanStorePath(name) && bundles[1].Files[name] != nil {
						if err := os.Remove(filepath.Join(root, name)); err != nil {
							t.Fatal(err)
						}
						want = []string{name, "regenerate owning bundles", filepath.Base(filepath.Dir(bundles[0].ManifestPath)), filepath.Base(filepath.Dir(bundles[1].ManifestPath))}
						break
					}
				}
				if len(want) == 0 {
					t.Fatal("fixture has no shared object")
				}
			case "empty directory":
				if err := os.MkdirAll(filepath.Join(root, PlanStoreDirectory, "components", "unused"), 0o755); err != nil {
					t.Fatal(err)
				}
				want = []string{"components/unused", "run generate", "without --check"}
			}
			check := func(err error) {
				t.Helper()
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
				for _, text := range want {
					if !strings.Contains(err.Error(), text) {
						t.Fatalf("missing recovery detail %q: %v", text, err)
					}
				}
			}
			check(CheckGeneratedBundle(t.Context(), root, bundles[0]))
			if damage == "manifest" {
				check(WriteGeneratedBundle(t.Context(), root, bundles[0]))
				check(WriteGeneratedBundle(t.Context(), root, bundles[1]))
			}
		})
	}
}

func TestPlanStoreNamedPathGrammar(t *testing.T) {
	for _, tc := range []struct {
		name  string
		valid bool
	}{
		{"steps/output/linux/arm64/build@abcdef012345.json", true},
		{"scripts/Uppercase/nested/build@abcdef012345.sh", true},
		{"unknown/name@abcdef012345.json", false},
		{"steps/name.json", false},
		{"steps/name@abcdef0123456.json", false},
		{"steps/name@ABCDEF012345.json", false},
		{"steps/a/../name@abcdef012345.json", false},
		{"steps/name\x1b@abcdef012345.json", false},
		{"steps/name\u200b@abcdef012345.json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPlanStorePath(PlanStoreDirectory + "/" + tc.name); got != tc.valid {
				t.Fatalf("accepted=%t, want %t", got, tc.valid)
			}
		})
	}
}

func TestPlanStoreFullDigestDisagreementDoesNotPublish(t *testing.T) {
	const body = "printf existing\n"
	name := testNamedStorePath(t, "scripts/fixture/build", []byte(body), ".sh")
	for _, betweenOwners := range []bool{false, true} {
		t.Run(map[bool]string{false: "incoming object", true: "neighboring owners"}[betweenOwners], func(t *testing.T) {
			root := t.TempDir()
			writeGeneratedFixture(t, filepath.Join(root, name), []byte(body))
			writeOwner := func(bundleName, digest string) {
				layout := githubBundleLayout(bundleName)
				manifest := GeneratedManifest{SchemaVersion: generatedManifestSchema, Engine: EngineGitHub, WorkflowPath: layout.workflowPath,
					Members: []GeneratedMember{{Path: name, SHA256: digest, Mode: 0o644}}}
				writeGeneratedFixture(t, filepath.Join(root, layout.manifestPath), mustCanonical(t, manifest))
			}
			// Syntactically valid full hashes deliberately disagree at one valid locator.
			// No brute-force short-hash collision or executable receipt is fabricated.
			writeOwner("first", "sha256:"+strings.Repeat("a", 64))
			if betweenOwners {
				writeOwner("second", "sha256:"+strings.Repeat("b", 64))
			}
			before := mustCanonical(t, githubRuntimeTree(t, root))
			incoming := &GeneratedBundle{Engine: EngineGitHub, ManifestPath: ".github/nvkr-run-incoming/manifest.json", Files: map[string][]byte{name: []byte(body)}}
			_, err := inspectPlanStore(t.Context(), root, incoming, true)
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
			want := "short Plan address collision"
			if betweenOwners {
				want = "owners disagree on full digest"
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatal("wrong refusal branch", err)
			}
			if !bytes.Equal(before, mustCanonical(t, githubRuntimeTree(t, root))) {
				t.Fatal("conflicting description changed existing bytes")
			}
		})
	}
}
