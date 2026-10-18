// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

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
	cfg.NVKR, err = filepath.Abs(cfg.NVKR)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Out = filepath.Join(caller, "prepared")
	t.Chdir(caller)
	assets, err := PrepareNativeAssets(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets.RuntimeOutputs) != len(runtimeImages) || len(assets.Images) != len(runtimeImages)+5+len(nativeCAPIAuthImages) {
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
	if err := attempt.releases(t.Context(), "http://172.30.0.1:8080/local.qcow2", "http://172.30.0.1:18080", t.TempDir()); err != nil {
		t.Fatalf("actual NVKE release templates rejected local workload startup: %v", err)
	}
}

func TestNativeAssetsMissingClosureHasNoWorkspaceOrSubprocess(t *testing.T) {
	root := t.TempDir()
	tool := filepath.Join(root, "nvkr")
	marker := filepath.Join(root, "unexpected-execution")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "out")
	_, err := PrepareNativeAssets(t.Context(), NativeAssetConfig{
		NVKR: tool, ToolsRun: filepath.Join(root, "missing-run"), Out: out,
	})
	var coded *nvkrerr.Error
	if !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed {
		t.Fatalf("missing closure did not fail at the public boundary: %v", err)
	}
	for _, path := range []string{out, marker} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("incomplete artifact closure performed output or subprocess work")
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

func TestNativeCAPIAuthPreservesSourceAndRejectsChangedSlots(t *testing.T) {
	for _, mutation := range []string{"none", "missing", "duplicate", "missing mount", "changed copy path"} {
		t.Run(mutation, func(t *testing.T) {
			assets := NativeAssets{Installer: t.TempDir(), Images: map[string]NativeImageAsset{}}
			source := "securityContext: {runAsNonRoot: true, readOnlyRootFilesystem: true}\ncommand: /plugin/cmd\n              subPath: ko-app\n"
			for _, image := range nativeCAPIAuthImages {
				digest := "sha256:" + strings.Repeat("a", 64)
				assets.Images[image.role] = NativeImageAsset{Reference: "docker.io/nvkr-local/" + image.role + "@" + digest, ManifestDigest: digest}
				source += "image: " + image.source + "\n"
			}
			if mutation == "missing" {
				source = strings.Replace(source, nativeCAPIAuthImages[0].source, "changed-source", 1)
			}
			if mutation == "duplicate" {
				source += nativeCAPIAuthImages[0].source
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
			for _, controller := range []string{"capi-controller-manager", "capi-kubeadm-bootstrap-controller-manager", "capi-kubeadm-control-plane-controller-manager", "capd-controller-manager"} {
				if err := os.WriteFile(filepath.Join(directory, controller+".yaml"), []byte(source), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := prepareNativeCAPIAuth(t.Context(), assets)
			if mutation != "none" {
				if err == nil {
					t.Fatal("changed pinned source escaped admission")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := source
			for _, image := range nativeCAPIAuthImages {
				want = strings.Replace(want, image.source, assets.Images[image.role].Reference, 1)
			}
			want = strings.Replace(want, "              subPath: ko-app\n", "", 1)
			want = strings.Replace(want, "/plugin/cmd", "/plugin/ko-app/cmd", 1)
			for _, file := range []string{"capi-controller-manager", "capi-kubeadm-bootstrap-controller-manager", "capi-kubeadm-control-plane-controller-manager", "capd-controller-manager"} {
				raw, err := os.ReadFile(filepath.Join(directory, file+".yaml"))
				if err != nil || string(raw) != want {
					t.Fatalf("source bytes beyond the image references and root path changed: %v", err)
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
	if !exists || producer.Commit != "69ef9924c4bf0487beba8c3613ae6e0a0260d8f3" {
		t.Fatal("installer archive lost the selected NVKE source")
	}
	command := strings.Join(producer.BuildCommand, " ")
	if !strings.Contains(command, "-cf - make/tool-versions.mk deploy/nvke-deploy.sh") || !strings.Contains(command, "-C bin SOURCE_COMMIT") {
		t.Fatal("installer archive omitted the provider version or source marker")
	}
}

func TestNativeInstallerRejectsMissingOrChangedCAPISelectionAtExtraction(t *testing.T) {
	versions, err := os.ReadFile("testdata/native-b615-tool-versions.mk")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"complete", "missing versions", "changed versions", "missing source marker", "changed source marker", "canceled"} {
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
			if scenario != "missing source marker" {
				commit := "69ef9924c4bf0487beba8c3613ae6e0a0260d8f3\n"
				if scenario == "changed source marker" {
					commit = strings.Repeat("a", 40) + "\n"
				}
				entries = append(entries, assetEntry{"SOURCE_COMMIT", tar.TypeReg, commit, 0})
			}
			if scenario != "missing versions" {
				content := string(versions)
				if scenario == "changed versions" {
					content += "CAPI_VERSION ?= v9.99.0\n"
				}
				entries = append(entries, assetEntry{"make/tool-versions.mk", tar.TypeReg, content, 0})
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			err := extractNativeInstaller(ctx, writeAssetArchive(t, entries), privateTestDir(t))
			if (err == nil) != (scenario == "complete") {
				t.Fatalf("unexpected extraction admission: %v", err)
			}
			if scenario == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("extraction lost cancellation", err)
			}
		})
	}
}
