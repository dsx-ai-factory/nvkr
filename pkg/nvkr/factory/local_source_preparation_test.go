// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPrepareLocalNVKESourceFreezesCapturedAMD64Graph(t *testing.T) {
	// Reuse the owning pin-set fixture: only its temporary lock follows the local
	// deploy metadata. Authored producer commands and every other pin remain exact.
	scripts, err := filepath.Abs("../../../scripts")
	if err != nil {
		t.Fatal(err)
	}
	fixture, commit := controllerCatalogFixture(t, "../../../inputs/catalogs/nvke/release")
	source := strings.TrimSuffix(strings.TrimPrefix(os.Getenv("GIT_CONFIG_KEY_0"), "url."), ".insteadOf")
	if err := os.Symlink(source, source+".git"); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "git", "-C", source, "branch", "-M", "main")
	if raw, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture main: %v: %s", err, raw)
	}
	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "inputs"), os.DirFS("../../../inputs")); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "inputs/catalogs/nvke/native-site")
	lock, err := catalog.LoadLockContext(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	originalCommit := lock.Pins["nvke-deploy"].Commit
	for name := range lock.Pins {
		pin, err := fixture.SourcePin("nvke")
		if err != nil {
			t.Fatal(err)
		}
		lock.Pins[name] = pin
	}
	for name, pin := range lock.Artifacts {
		if pin.DerivedFrom == "nvke-deploy@"+originalCommit {
			pin.DerivedFrom = "nvke-deploy@" + commit
			lock.Artifacts[name] = pin
		}
	}
	if err := catalog.WriteLockContext(t.Context(), directory, lock); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_1", "url."+source+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_1", localNVKERepository+".git")
	t.Chdir(root)
	root, err = os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	const definitionPath = "inputs/definitions/demo/amd64-nvidia-local-kvm.json"
	const environmentPath = "inputs/environments/local-amd64-kvm.json"
	original, err := os.ReadFile(definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := CaptureCompileFiles(t.Context(), root, definitionPath, environmentPath, "source-test")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareLocalNVKESource(t.Context(), root, definitionPath, environmentPath, ".nvkr/source", commit)
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := CaptureCompileFiles(t.Context(), root, prepared.Definition, prepared.Environment, "source-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Builds) != len(after.Builds) {
		t.Fatal("source selection changed outputs")
	}
	// Exercise the exact projection used by prepareDefaultNativeBuild, rather than
	// duplicating its catalog grouping logic in a test-only approximation.
	rawPlan, err := canonical.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	projectionCtx, cancelProjection := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancelProjection()
	projection := exec.CommandContext(projectionCtx, "jq", "-L", scripts, "-e", `include "nvke-os-plan"; local_toolchain_scopes("all"; "amd64")`)
	projection.WaitDelay = time.Second
	projection.Stdin = bytes.NewReader(rawPlan)
	projected, err := projection.CombinedOutput()
	if err != nil {
		t.Fatalf("native-build-toolchain-scopes: %v: %s", err, projected)
	}
	var scopes []struct {
		Outputs    []string `json:"outputs"`
		Components []string `json:"components"`
		Pin        string   `json:"pin"`
	}
	if err := json.Unmarshal(projected, &scopes); err != nil {
		t.Fatal(err)
	}
	sharedCharts := false
	for _, scope := range scopes {
		if slices.Contains(scope.Outputs, "controller-chart") && slices.Contains(scope.Outputs, "nktp") {
			sharedCharts = true
		}
	}
	if len(scopes) != 3 || !sharedCharts {
		t.Fatal("native source preparation split the site chart toolchain cohort")
	}
	cohorts := make(map[string]string)
	for index, build := range after.Builds {
		original := before.Builds[index]
		if digest, exists := cohorts[original.CatalogDigest]; exists && digest != build.CatalogDigest {
			t.Fatalf("source preparation split catalog cohort for %s", build.Name)
		}
		cohorts[original.CatalogDigest] = build.CatalogDigest
	}
	nightly, err := authored.LoadDefinition(t.Context(), "inputs/definitions/demo/arm64-nvidia-kvm.json")
	if err != nil {
		t.Fatal(err)
	}
	var wantInit catalog.Component
	for _, output := range nightly.Outputs {
		for _, name := range output.Components {
			if name != "native-capi-exec-init-image" {
				continue
			}
			selection := nightly.Catalog
			if output.Catalog != nil {
				selection = *output.Catalog
			}
			stack, err := loadFactoryCatalogStack(t.Context(), "inputs/definitions/demo/arm64-nvidia-kvm.json", selection)
			if err != nil {
				t.Fatal(err)
			}
			wantInit = stack.Catalog.Components[name]
		}
	}
	if wantInit.FromSource || wantInit.Prebuilt == nil {
		t.Fatal("authored current-main CAPI producer is missing")
	}
	frozen := 0
	for index, build := range after.Builds {
		prior := before.Builds[index]
		if prior.Name != build.Name || build.Platform != "linux/amd64" {
			t.Fatal("source selection changed named target")
		}
		for name, component := range build.CatalogSnapshot.Components {
			original := prior.CatalogSnapshot.Components[name]
			if component.FromSource && strings.TrimSuffix(component.Repo, ".git") == localNVKERepository {
				if component.Commit != commit || component.Tag != commit {
					t.Fatalf("catalog member %s/%s was not frozen", build.Name, name)
				}
				if component.ToolchainImage != original.ToolchainImage || !reflect.DeepEqual(component.ToolchainImages, original.ToolchainImages) {
					t.Fatalf("source preparation changed catalog toolchain %s/%s", build.Name, name)
				}
			} else if name == "native-capi-exec-init-image" {
				if !reflect.DeepEqual(component, wantInit) {
					t.Fatal("catalog init image differs from its authored current-main component")
				}
			} else if !reflect.DeepEqual(component, original) {
				t.Fatalf("independent catalog member %s changed", name)
			}
		}
		for componentIndex, component := range build.ResolvedComponents {
			name := component.Name
			original := prior.ResolvedComponents[componentIndex]
			if component.FromSource && strings.TrimSuffix(component.Repo, ".git") == localNVKERepository {
				frozen++
				if component.Commit != commit || component.Tag != commit || component.SourceDate == 0 || component.TreeHash == "" {
					t.Fatalf("%s/%s was not frozen", build.Name, name)
				}
				if original.ToolchainImage != component.ToolchainImage || !reflect.DeepEqual(original.ToolchainImages, component.ToolchainImages) || (name != "nvke-image-inputs" && !reflect.DeepEqual(original.BuildCommand, component.BuildCommand)) {
					t.Fatalf("source selection changed producer %s", name)
				}
			} else if name == "native-capi-exec-init-image" {
				if !reflect.DeepEqual(component.Component, wantInit) {
					t.Fatal("CAPI init image differs from the authored current-main producer")
				}
			} else if !reflect.DeepEqual(component, original) {
				t.Fatalf("independent component %s changed", name)
			}
			if name == "nvke-image-inputs" {
				covered := false
				for _, scope := range scopes {
					if slices.Contains(scope.Outputs, build.Name) && slices.Contains(scope.Components, name) && scope.Pin == component.ToolchainImages["amd64"] {
						covered = true
					}
				}
				if !covered {
					t.Fatal("current-main material producer is outside local toolchain preparation")
				}
				if !bytes.Contains([]byte(strings.Join(component.BuildCommand, " ")), []byte("doca-ovs")) {
					t.Fatal("current-main DOCA material producer was not selected")
				}
				if !reflect.DeepEqual(component.Platforms, []string{"linux/amd64", "linux/arm64"}) {
					t.Fatal("current-main material producer lost declared platforms")
				}
			}
		}
	}
	if frozen == 0 {
		t.Fatal("no NVKE source producers frozen")
	}
	captured, snapshot, err := CaptureCompileFiles(t.Context(), root, prepared.Definition, prepared.Environment, "source-test")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := CompileSnapshot(t.Context(), snapshot, "source-test")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := after.Digest()
	for _, plan := range []*Plan{captured, replayed} {
		got, err := plan.Digest()
		if err != nil || got != want {
			t.Fatalf("source selection capture/replay changed identity: %s %v", got, err)
		}
	}
	unchanged, err := os.ReadFile(definitionPath)
	if err != nil || !bytes.Equal(original, unchanged) {
		t.Fatal("authored Definition was mutated")
	}
}

func TestPrepareLocalNVKESourceAdmission(t *testing.T) {
	for _, test := range []struct {
		name, commit, definition string
		wrongDirectory           bool
		code                     e.Code
	}{
		{"mutable source", "main", "inputs/definitions/demo/amd64-nvidia-local-kvm.json", false, e.CodeUsage},
		{"wrong cwd", strings.Repeat("a", 40), "inputs/definitions/demo/amd64-nvidia-local-kvm.json", true, e.CodeUsage},
		{"other definition", strings.Repeat("a", 40), "inputs/definitions/demo/arm64-nvidia-local-kvm.json", false, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if !test.wrongDirectory {
				t.Chdir(root)
				var err error
				root, err = os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := PrepareLocalNVKESource(t.Context(), root, test.definition, "inputs/environments/local-amd64-kvm.json", ".nvkr/rejected", test.commit)
			assertFactoryErrorCode(t, err, test.code)
			if _, err := os.Lstat(filepath.Join(root, ".nvkr")); !os.IsNotExist(err) {
				t.Fatal("rejected preparation wrote output")
			}
		})
	}
}

func TestPrepareLocalNVKESourceRejectsDestinationBeforeSourceWork(t *testing.T) {
	for _, out := range []string{"../escape", "/absolute", ".nvkr/existing"} {
		t.Run(out, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			if err := os.MkdirAll(".nvkr/existing", 0700); err != nil {
				t.Fatal(err)
			}
			// No catalog inputs exist. Destination admission must win over catalog or
			// network work, including when a caller reuses an existing attempt.
			_, err := PrepareLocalNVKESource(t.Context(), root, "inputs/definitions/demo/amd64-nvidia-local-kvm.json", "inputs/environments/local-amd64-kvm.json", out, strings.Repeat("a", 40))
			assertFactoryErrorCode(t, err, e.CodeUsage)
		})
	}
}
