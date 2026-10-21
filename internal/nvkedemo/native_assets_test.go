// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestNativeSiteToolAdmissionRejectsObsoleteRuns(t *testing.T) {
	for _, tc := range []struct {
		name       string
		components []string
		accepted   bool
	}{
		{"current helper-bearing bundle", []string{"nerdctl", nativeIdentityHelper}, true},
		{"legacy guest tool only", []string{"nerdctl"}, false},
		{"legacy installer", []string{"nerdctl", "nvke-installer-sources"}, false},
		{"legacy guest CLI", []string{"nerdctl", "nvke-installer-sources", "nvkectl"}, false},
		{"missing guest tool", []string{nativeIdentityHelper}, false},
		{"extra unselected tool", []string{"nerdctl", nativeIdentityHelper, "nvkectl"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			members, err := sitePreparationMembers(tc.components)
			if (err == nil) != tc.accepted || !tc.accepted && members != nil {
				t.Fatalf("obsolete output admission: members=%v err=%v", members, err)
			}
			if tc.accepted && !slices.Equal(members, tc.components) {
				t.Fatal("admission changed the selected members")
			}
		})
	}
}

// Explicit local integration: verifies/extracts real completed Runs without
// launching any guest. Ordinary tests perform no dependency acquisition.
func TestNativeAssetsLocalRuntimeClosure(t *testing.T) {
	path := os.Getenv("NVKR_NATIVE_ASSET_CONFIG")
	if path == "" {
		t.Skip("requires explicit local artifact selections")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg NativeAssetConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.RuntimeRun == "" {
		t.Fatal("requires the complete shared runtime-image Run")
	}
	// Exercise the real handoff with caller-relative Runs and a different
	// subprocess directory. Keep artifacts immutable and extract into a fresh
	// test-owned workspace rather than the configured output destination.
	caller := t.TempDir()
	for _, root := range []*string{&cfg.ToolsRun, &cfg.HostToolsRun, &cfg.BootstrapRun, &cfg.DeploymentRun, &cfg.RuntimeRun, &cfg.ControllerRun, &cfg.ProvidersRun} {
		if *root == "" {
			continue
		}
		absolute, err := filepath.Abs(*root)
		if err != nil {
			t.Fatal(err)
		}
		*root, err = filepath.Rel(caller, absolute)
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg.Out = filepath.Join(caller, "prepared")
	t.Chdir(caller)
	assets, err := PrepareNativeAssets(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets.RuntimeOutputs) != len(runtimeImages) || len(assets.Images) != len(runtimeImages)+6+len(nativeCAPIAuthImages) {
		t.Fatal("incomplete local runtime image closure")
	}
	for _, role := range runtimeImages {
		selected := assets.RuntimeOutputs["runtime-"+role]
		image := assets.Images["runtime-"+role]
		if image.ManifestDigest != selected.Observation.Digest || image.Reference != "docker.io/nvkr-local/"+role+"@"+image.ManifestDigest {
			t.Fatalf("runtime alias changed the selected image identity: %s", role)
		}
	}
	attempt := nativeSiteAttempt{assets: assets}
	if err := attempt.prepareBoot(t.Context(), "http://172.30.0.1:8080/local.qcow2", "http://172.30.0.1:18080"); err != nil {
		t.Fatalf("actual NVKE MachineImage template rejected local disk selection: %v", err)
	}
}

func TestNativeAssetsMissingClosureHasNoWorkspace(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "out")
	_, err := PrepareNativeAssets(t.Context(), NativeAssetConfig{
		ToolsRun: filepath.Join(root, "missing-run"), Out: out,
	})
	var coded *nvkrerr.Error
	if !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed {
		t.Fatalf("missing closure did not fail at the public boundary: %v", err)
	}
	for _, path := range []string{out} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("incomplete artifact closure created a workspace")
		}
	}
}

func TestNativeAssetsCanceledBeforeInputIO(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := PrepareNativeAssets(ctx, NativeAssetConfig{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled artifact preparation read inputs: %v", err)
	}
}

func TestNativeAcquiredImageRequiresSelectedSourceAndChild(t *testing.T) {
	for _, role := range []string{"workload", "capi-exec-init", "capi-exec-plugin"} {
		for _, scenario := range []string{"selected", "updated source", "wrong source", "wrong repository", "wrong epoch", "wrong child", "wrong parent", "wrong build", "wrong platform", "missing producer", "duplicate producer"} {
			t.Run(role+"/"+scenario, func(t *testing.T) {
				installer := &nativeInstallerSelection{repo: "https://example.invalid/nvke.git", commit: strings.Repeat("a", 40), sourceDate: 1}
				if scenario == "updated source" {
					installer.commit, installer.sourceDate = strings.Repeat("b", 40), 2
				}
				parent, child := "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64)
				if scenario == "updated source" {
					parent, child = "sha256:"+strings.Repeat("e", 64), "sha256:"+strings.Repeat("f", 64)
				}
				component := catalog.Component{Repo: installer.repo, Commit: installer.commit, SourceDate: installer.sourceDate, Prebuilt: &catalog.Prebuilt{
					Image: "example.invalid/" + role + "@" + parent, ImageParentDigest: parent, Digests: map[string]string{"arm64": child},
				}}
				selected := nativeImageSelection{role: role, output: role + "-image-arm64", observed: lifecycle.Artifact{Digest: child, BuildDigest: "build"}}
				build := factory.BuildPlan{Name: selected.output, Platform: "linux/arm64", BuildDigest: selected.observed.BuildDigest}
				switch scenario {
				case "wrong source":
					component.Commit = strings.Repeat("f", 40)
				case "wrong repository":
					component.Repo = "https://example.invalid/other.git"
				case "wrong epoch":
					component.SourceDate++
				case "wrong child":
					selected.observed.Digest = parent
				case "wrong parent":
					component.Prebuilt.ImageParentDigest = child
				case "wrong build":
					build.BuildDigest = "other"
				case "wrong platform":
					build.Platform = "linux/amd64"
				}
				name := "native-" + role + "-image"
				if scenario == "missing producer" {
					name = "unselected"
				}
				build.ResolvedComponents = []catalog.NamedComponent{{Name: name, Component: component}}
				if scenario == "duplicate producer" {
					build.ResolvedComponents = append(build.ResolvedComponents, build.ResolvedComponents[0])
				}
				ref, err := nativeAcquiredImage(&factory.Plan{Builds: []factory.BuildPlan{build}}, selected, installer)
				accepted := scenario == "selected" || scenario == "updated source"
				if (err == nil) != accepted || accepted && ref.String() != component.Prebuilt.Image {
					t.Fatalf("source/child selection: ref=%s err=%v", ref, err)
				}
			})
		}
	}
}

func TestNativeCAPIAuthPreservesSourceAndRejectsChangedSlots(t *testing.T) {
	for _, mutation := range []string{"none", "updated source", "missing", "duplicate", "missing mount", "changed copy path", "later overlay mismatch"} {
		t.Run(mutation, func(t *testing.T) {
			assets := NativeAssets{Installer: t.TempDir(), Images: map[string]NativeImageAsset{}}
			parent := "sha256:" + strings.Repeat("b", 64)
			if mutation == "updated source" {
				parent = "sha256:" + strings.Repeat("c", 64)
			}
			acquired, sourceRefs := map[string]string{}, map[string]string{}
			for _, role := range nativeCAPIAuthImages {
				digest := "sha256:" + strings.Repeat("a", 64)
				assets.Images[role] = NativeImageAsset{Reference: "docker.io/nvkr-local/" + role + "@" + digest, ManifestDigest: digest}
				acquired[role], sourceRefs[role] = "example.invalid/"+role+"@"+parent, "example.invalid/"+role+":selected@"+parent
			}
			source := fmt.Sprintf("spec:\n  template:\n    spec:\n      securityContext: {runAsNonRoot: true, readOnlyRootFilesystem: true}\n      volumes:\n        - name: spiffe-exec-plugin\n          image:\n            reference: %s\n      initContainers:\n        - name: install-spiffe-exec-plugin\n          image: %s\n          command: [/bin/cp, /plugin/cmd]\n          volumeMounts:\n            - name: spiffe-exec-plugin\n              subPath: ko-app\n", sourceRefs["capi-exec-plugin"], sourceRefs["capi-exec-init"])
			if mutation == "missing" {
				source = strings.Replace(source, sourceRefs["capi-exec-init"], "changed-source", 1)
			}
			if mutation == "duplicate" {
				source += "# " + sourceRefs["capi-exec-init"] + "\n"
			}
			if mutation == "missing mount" {
				source = strings.Replace(source, "              subPath: ko-app\n", "", 1)
			}
			if mutation == "changed copy path" {
				source = strings.Replace(source, "/plugin/cmd", "/plugin/other", 1)
			}
			directory := filepath.Join(assets.Installer, "controller", "deploy", "k8s", "capi-spiffe-exec-auth")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			controllers := []string{"capi-controller-manager", "capi-kubeadm-bootstrap-controller-manager", "capi-kubeadm-control-plane-controller-manager", "capd-controller-manager"}
			for _, controller := range controllers {
				raw := source
				if mutation == "later overlay mismatch" && controller == controllers[len(controllers)-1] {
					raw = strings.Replace(raw, sourceRefs["capi-exec-init"], "changed-source", 1)
				}
				if err := os.WriteFile(filepath.Join(directory, controller+".yaml"), []byte(raw), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := prepareNativeCAPIAuth(t.Context(), assets, acquired)
			if mutation != "none" && mutation != "updated source" {
				if err == nil {
					t.Fatal("changed source escaped admission")
				}
				raw, readErr := os.ReadFile(filepath.Join(directory, controllers[0]+".yaml"))
				if readErr != nil || string(raw) != source {
					t.Fatal("failed overlay admission changed an earlier source file", readErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := source
			for _, role := range nativeCAPIAuthImages {
				want = strings.Replace(want, sourceRefs[role], assets.Images[role].Reference, 1)
			}
			want = strings.Replace(want, "              subPath: ko-app\n", "", 1)
			want = strings.Replace(want, "/plugin/cmd", "/plugin/ko-app/cmd", 1)
			for _, file := range controllers {
				raw, err := os.ReadFile(filepath.Join(directory, file+".yaml"))
				if err != nil || string(raw) != want {
					t.Fatalf("source bytes beyond image refs/root path changed: %v", err)
				}
			}
		})
	}
}

func TestNativeInstallerArchiveIncludesSourceOwnedCAPISelection(t *testing.T) {
	loaded, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("..", "..", "inputs", "catalogs", "tools", "native")))
	if err != nil {
		t.Fatal(err)
	}
	producer, exists := loaded.Components["nvke-installer-sources"]
	if !exists || producer.Commit == "" {
		t.Fatal("installer archive lost the selected NVKE source")
	}
	command := strings.Join(producer.BuildCommand, " ")
	if !strings.Contains(command, "-cf - make/tool-versions.mk deploy/nvke-deploy.sh") || !strings.Contains(command, "-C bin SOURCE_COMMIT CAPI_VERSION") || !strings.Contains(command, "-f make/tool-versions.mk") || !strings.Contains(command, nativeSiteSpecPath) {
		t.Fatal("installer archive omitted the provider version or source marker")
	}
}

func TestNativeInstallerRejectsMissingOrChangedCAPISelectionAtExtraction(t *testing.T) {
	run, bundle, _ := nativeCapturedToolsFixture(t)
	selected, err := nativeInstallerSelectionFromRun(t.Context(), run, bundle)
	if err != nil {
		t.Fatal(err)
	}
	versions := []byte("CAPI_VERSION ?= " + selected.capiVersion + "\n")
	for _, scenario := range []string{"complete", "missing versions", "missing site specification", "changed provider", "missing source marker", "changed source marker", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			entries := []assetEntry{
				{"deploy/nvke-deploy.sh", tar.TypeReg, "installer", 0},
				{"deploy/lib/nvke-deploy-items.py", tar.TypeReg, "items", 0},
				{"deploy/lib/render-template.py", tar.TypeReg, "renderer", 0},
				{"controller/deploy/charts/controller/Chart.yaml", tar.TypeReg, "controller chart", 0},
				{"nktp/deploy/charts/nktp/Chart.yaml", tar.TypeReg, "nktp chart", 0},
				{"controller/deploy/k8s/capi-spiffe-exec-auth/capi-controller-manager.yaml", tar.TypeReg, "capi overlay", 0},
				{"controller/test/fixtures/releases/nvke-nico-vms-24.04-arm64-fde-machineimage.yaml", tar.TypeReg, "machineimage", 0},
				{"controller/test/fixtures/releases/nvke-nico-vms-24.04-arm64-fde-clusterrelease.yaml", tar.TypeReg, "clusterrelease", 0},
				{"controller/test/fixtures/releases/nvke-nico-vms-24.04-arm64-fde-nodepoolrelease.yaml", tar.TypeReg, "nodepoolrelease", 0},
				{"deploy/nico/nvke-deploy.yaml", tar.TypeReg, "deployment", 0},
				{"controller/nico-sim/internal/seed/default-seed.yaml", tar.TypeReg, "seed", 0},
				{"controller/nico-sim/ui/index.html", tar.TypeReg, "index", 0},
			}
			if scenario != "missing site specification" {
				entries = append(entries, assetEntry{nativeSiteSpecPath, tar.TypeReg, nativeInstallerSiteSpecFixture, 0})
			}
			if scenario != "missing source marker" {
				commit := selected.commit + "\n"
				if scenario == "changed source marker" {
					commit = strings.Repeat("a", 40) + "\n"
				}
				entries = append(entries, assetEntry{"SOURCE_COMMIT", tar.TypeReg, commit, 0})
			}
			if scenario != "missing versions" {
				content := string(versions)

				entries = append(entries, assetEntry{"make/tool-versions.mk", tar.TypeReg, content, 0})
			}
			provider := selected.capiVersion + "\n"
			if scenario == "changed provider" {
				provider = "v9.99.0\n"
			}
			entries = append(entries, assetEntry{"CAPI_VERSION", tar.TypeReg, provider, 0})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			_, err := extractNativeInstaller(ctx, writeAssetArchive(t, entries), privateTestDir(t), run, bundle)
			if (err == nil) != (scenario == "complete") {
				t.Fatalf("unexpected extraction admission: %v", err)
			}
			if scenario == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("extraction lost cancellation", err)
			}
		})
	}
}
