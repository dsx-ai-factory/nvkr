// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
)

// NativeAssetConfig selects completed immutable Runs. Extraction and exact
// content verification remain owned by NVKR; these paths confer no authority.
type NativeAssetConfig struct {
	NVKR, ToolsRun, BootstrapRun, DeploymentRun, Out string
	HostToolsRun                                     string
	ControllerRun, ProvidersRun                      string
	RuntimeRun                                       string
	Network                                          string
	Progress                                         io.Writer
}

// NativeAssets holds one prepared private runtime view of original outputs.
// Image references are local import aliases; manifests retain their original
// identities. This view grants no release, signing or qualification authority.
type NativeAssets struct {
	HostTools, SiteTools, Installer, Files string
	Images                                 map[string]NativeImageAsset
	CLI                                    string
	RuntimeOutputs                         map[string]SelectedOutput
}

type NativeImageAsset struct {
	File, Reference, ManifestDigest string
}

type nativeImageSelection struct {
	role, root, output, reference string
	observed                      lifecycle.Artifact
}

// These exact source-overlay references are projected to verified ARM64 child
// manifests for local import. No parent digest is assigned to different bytes.
var nativeCAPIAuthImages = []struct{ role, source string }{
	{"capi-exec-init", "alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6"},
	{"capi-exec-plugin", "ghcr.io/spiffe/k8s-spiffe-workload-jwt-exec-auth:1.0.0@sha256:e30e90774e5951f84eb6dfb36f972fead08b64c3bfdc0912c5b15f988e79de3a"},
}

// PrepareNativeAssets verifies the full bootstrap/deployment image closure
// and host/site tool bundles before producing a runtime view. No VM starts.
func PrepareNativeAssets(ctx context.Context, cfg NativeAssetConfig) (assets NativeAssets, result error) {
	boundary := beginProgress(cfg.Progress, "artifacts", LaneStarted, LaneFinished, LaneFailed)
	defer func() {
		boundary.finish(result)
		if result != nil {
			result = nvkrerr.Wrap(nvkrerr.CodePreflightFailed, result, "prepare native site artifacts")
		}
	}()
	if err := requireContext(ctx); err != nil {
		return assets, err
	}
	// Resolve descriptions against the caller's directory once. Runner changes
	// directory to the private output workspace; keep the original paths for
	// both admission and later CLI invocations without resolving symlinks.
	for _, root := range []*string{&cfg.ToolsRun, &cfg.HostToolsRun, &cfg.BootstrapRun, &cfg.DeploymentRun, &cfg.RuntimeRun, &cfg.ControllerRun, &cfg.ProvidersRun} {
		if *root == "" {
			continue
		}
		absolute, err := filepath.Abs(*root)
		if err != nil {
			return assets, err
		}
		*root = absolute
	}
	tool, err := resolveExisting(cfg.NVKR)
	if err != nil {
		return assets, err
	}
	out, err := freshDestination(cfg.Out)
	if err != nil {
		return assets, err
	}
	_, ovnOutput, _, err := selectedOVNOutputs(cfg.Network)
	if err != nil {
		return assets, err
	}
	profilesOutput, err := selectedProfilesOutput(cfg.Network)
	if err != nil {
		return assets, err
	}
	controllerRun, providersRun := cfg.BootstrapRun, cfg.DeploymentRun
	if cfg.ControllerRun != "" {
		controllerRun = cfg.ControllerRun
	}
	if cfg.ProvidersRun != "" {
		providersRun = cfg.ProvidersRun
	}
	selections := []nativeImageSelection{
		{"ovn", cfg.DeploymentRun, ovnOutput, "", lifecycle.Artifact{}},
		{"capnico", providersRun, "capnico-controller-arm64", "ghcr.io/dsx-ai-factory/cluster-api-provider-nico/controller:v0.0.45", lifecycle.Artifact{}},
		{"controller", controllerRun, "cwe", "docker.io/library/nke-controller:latest", lifecycle.Artifact{}},
		{"profiles", cfg.DeploymentRun, profilesOutput, "docker.io/library/cwe-profiles:local", lifecycle.Artifact{}},
		{"provisioned-provider", cfg.BootstrapRun, "cluster-api-provider-provisioned-arm64", "docker.io/library/cluster-api-provider-provisioned:latest", lifecycle.Artifact{}},
	}
	if cfg.RuntimeRun != "" {
		for _, role := range runtimeImages {
			selections = append(selections, nativeImageSelection{role: "runtime-" + role, root: cfg.RuntimeRun, output: role + "-image-arm64"})
		}
		selections = append(selections, nativeImageSelection{role: "workload", root: cfg.RuntimeRun, output: "workload-image-arm64", reference: WorkloadImage})
		for _, image := range nativeCAPIAuthImages {
			selections = append(selections, nativeImageSelection{role: image.role, root: cfg.RuntimeRun, output: image.role + "-image-arm64"})
		}
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	_, _, hostBundle, _, err := nativeToolSelection(platform)
	if err != nil {
		return assets, err
	}
	hostRun := cfg.ToolsRun
	if cfg.HostToolsRun != "" {
		hostRun = cfg.HostToolsRun
	}
	args := []string{tool, "publish", "root"}
	siteMembers := []string{"nerdctl"}
	for _, bundle := range []string{hostBundle, "site-startup-tools-arm64"} {
		run := cfg.ToolsRun
		if bundle == hostBundle {
			run = hostRun
		}
		observed, err := CompleteOutput(ctx, run, bundle, "executable")
		if err != nil {
			return assets, err
		}
		if bundle == "site-startup-tools-arm64" {
			siteMembers, err = sitePreparationMembers(observed.Components)
			if err != nil {
				return assets, err
			}
		}
		root := filepath.Join(run, "packages", bundle)
		if err := diagnosticIndexMatches(root, observed.Digest); err != nil {
			return assets, err
		}
		args = append(args, "--bundle", bundle+"="+root)
	}
	for i := range selections {
		selection := &selections[i]
		selection.observed, err = CompleteOutput(ctx, selection.root, selection.output, "oci")
		if err != nil {
			return assets, err
		}
		if selection.role == "ovn" {
			selection.reference = "localhost/nvke/ovn-kubernetes@" + selection.observed.Digest
		}
		if strings.HasPrefix(selection.role, "runtime-") {
			selection.reference = "docker.io/nvkr-local/" + strings.TrimPrefix(selection.role, "runtime-") + "@" + selection.observed.Digest
		}
		if strings.HasPrefix(selection.role, "capi-exec-") {
			selection.reference = "docker.io/nvkr-local/" + selection.role + "@" + selection.observed.Digest
		}
		if selection.role == "workload" && selection.reference != "docker.io/library/alpine@"+selection.observed.Digest {
			return assets, errors.New("selected workload differs from the shared diagnostic image")
		}
		root := filepath.Join(selection.root, "packages", selection.output)
		inspected, err := delivery.InspectContainerImage(ctx, root, "linux/arm64")
		if err != nil || inspected.ManifestDigest != selection.observed.Digest {
			return assets, errors.Join(err, fmt.Errorf("image %q differs from its completed Run", selection.output))
		}
		// The diagnostic publication CLI consumes binary bundles. Images are
		// independently verified by NVKR's existing delivery owner above and
		// again after assigning their private containerd import aliases.
	}
	// All selected observations/platforms were admitted before creating output.
	if err := os.Mkdir(out, 0o700); err != nil {
		return assets, err
	}
	runner := Runner{Work: out, Env: NativeEnvironment(), Progress: cfg.Progress}
	args = append(args, "--out", filepath.Join(out, "publication"))
	if err := runner.Run(ctx, args, "native-closure-verify", 5*time.Minute); err != nil {
		return assets, err
	}
	assets = NativeAssets{
		HostTools: filepath.Join(out, "host"), SiteTools: filepath.Join(out, "site"),
		Installer: filepath.Join(out, "installer"),
		Files:     filepath.Join(out, "files"), Images: make(map[string]NativeImageAsset), RuntimeOutputs: make(map[string]SelectedOutput),
	}
	if err := extractNativeHostTools(ctx, filepath.Join(hostRun, "packages", hostBundle), filepath.Join(out, "publication"), assets.HostTools, platform); err != nil {
		return assets, err
	}
	for _, bundle := range []struct {
		name, directory string
		members         []string
	}{
		{"site-startup-tools-arm64", assets.SiteTools, siteMembers},
	} {
		if err := runner.Run(ctx, []string{tool, "publish", "extract", "--layout", filepath.Join(out, "publication"), "--bundle", bundle.name, "--out", bundle.directory}, "native-extract-"+bundle.name, 2*time.Minute); err != nil {
			return assets, err
		}
		if err := verifyToolMembers(bundle.directory, bundle.members); err != nil {
			return assets, fmt.Errorf("tool bundle %s: %w", bundle.name, err)
		}
	}
	assets.CLI = filepath.Join(assets.HostTools, "nvkectl")
	for _, directory := range []string{assets.Installer, assets.Files, filepath.Join(out, "images")} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return assets, err
		}
	}
	if err := extractNativeInstaller(ctx, filepath.Join(assets.HostTools, "nvke-installer-sources"), assets.Installer); err != nil {
		return assets, err
	}
	for _, selection := range selections {
		image, err := prepareNativeImage(ctx, selection, out, assets.Files)
		if err != nil {
			return assets, err
		}
		assets.Images[selection.role] = image
		if strings.HasPrefix(selection.role, "runtime-") {
			assets.RuntimeOutputs[selection.role] = SelectedOutput{Layout: filepath.Join(selection.root, "packages", selection.output), Observation: selection.observed}
		}
	}
	if cfg.RuntimeRun != "" {
		if err := prepareNativeCAPIAuth(ctx, assets); err != nil {
			return assets, err
		}
	}
	return assets, nil
}

// This is an exact edit of four pinned source overlays, not a YAML parser.
// Preserve security and rollout settings; mount the read-only image root and
// use the plugin path recorded in that image rather than an image subPath.
func prepareNativeCAPIAuth(ctx context.Context, assets NativeAssets) error {
	for _, controller := range []string{"capi-controller-manager", "capi-kubeadm-bootstrap-controller-manager", "capi-kubeadm-control-plane-controller-manager", "capd-controller-manager"} {
		if err := requireContext(ctx); err != nil {
			return err
		}
		path := filepath.Join(assets.Installer, "controller", "deploy", "k8s", "capi-spiffe-exec-auth", controller+".yaml")
		raw, err := boundedRegularRead(path, diagnosticLimit)
		if err != nil {
			return err
		}
		for _, image := range nativeCAPIAuthImages {
			selected, exists := assets.Images[image.role]
			if !exists || selected.Reference != "docker.io/nvkr-local/"+image.role+"@"+selected.ManifestDigest || strings.Count(string(raw), image.source) != 1 {
				return errors.New("CAPI authentication overlay differs from its pinned local image handoff")
			}
			raw = []byte(strings.Replace(string(raw), image.source, selected.Reference, 1))
		}
		const subPath = "              subPath: ko-app\n"
		if strings.Count(string(raw), subPath) != 1 || strings.Count(string(raw), "/plugin/cmd") != 1 {
			return errors.New("CAPI authentication image mount differs from its pinned source handoff")
		}
		raw = []byte(strings.Replace(string(raw), subPath, "", 1))
		raw = []byte(strings.Replace(string(raw), "/plugin/cmd", "/plugin/ko-app/cmd", 1))
		if err := privateReplace(path, raw); err != nil {
			return err
		}
	}
	return nil
}

// A retained pre-migration Run may contain the earlier Linux CLI. Its exact
// bundle remains verified, but only the guest runtime and image bytes are
// shared with the VM; the CLI selected for execution is always the host build.
func sitePreparationMembers(components []string) ([]string, error) {
	for _, known := range [][]string{{"nerdctl"}, {"nerdctl", "nvke-installer-sources"}, {"nerdctl", "nvke-installer-sources", "nvkectl"}} {
		if slices.Equal(components, known) {
			return slices.Clone(known), nil
		}
	}
	return nil, errors.New("site preparation requires its exact known dependency set")
}

func prepareNativeImage(ctx context.Context, selection nativeImageSelection, work, files string) (NativeImageAsset, error) {
	root, err := layout.FromPath(filepath.Join(selection.root, "packages", selection.output))
	if err != nil {
		return NativeImageAsset{}, err
	}
	index, err := root.ImageIndex()
	if err != nil {
		return NativeImageAsset{}, err
	}
	manifest, err := index.IndexManifest()
	if err != nil || len(manifest.Manifests) != 1 || manifest.Manifests[0].Digest.String() != selection.observed.Digest {
		return NativeImageAsset{}, errors.Join(err, errors.New("native image requires one exact observed manifest"))
	}
	image, err := root.Image(manifest.Manifests[0].Digest)
	if err != nil {
		return NativeImageAsset{}, err
	}
	directory := filepath.Join(work, "images", selection.role)
	projected, err := layout.Write(directory, empty.Index)
	if err != nil {
		return NativeImageAsset{}, err
	}
	if err := projected.AppendImage(image, layout.WithAnnotations(map[string]string{
		"org.opencontainers.image.ref.name": selection.reference,
	})); err != nil {
		return NativeImageAsset{}, err
	}
	inspected, err := delivery.InspectContainerImage(ctx, directory, "linux/arm64")
	if err != nil || inspected.ManifestDigest != selection.observed.Digest {
		return NativeImageAsset{}, errors.Join(err, errors.New("native import alias changed the observed image manifest"))
	}
	// Reuse the existing deterministic, bounded transport carrier. Rewriting
	// only the descriptor's import alias preserves manifest/config/layer bytes.
	archive := filepath.Join(directory, "image.tar")
	if err := transportarchive.Pack(ctx, archive, directory, []string{"blobs", "index.json", "oci-layout"}); err != nil {
		return NativeImageAsset{}, err
	}
	file := filepath.Join(files, selection.role+".tar")
	if err := copyPrivate(ctx, archive, file, 0o600); err != nil {
		return NativeImageAsset{}, err
	}
	return NativeImageAsset{file, selection.reference, inspected.ManifestDigest}, nil
}

// Admit the source-owned provider selection at extraction time, before image
// preparation or any later network/VM work. deployFromHost rechecks these bytes
// at its executable boundary before invoking clusterctl.
func extractNativeInstaller(ctx context.Context, archive, installer string) error {
	if err := safeExtractAssets(ctx, archive, installer, []string{
		"SOURCE_COMMIT", "make/tool-versions.mk", "deploy/nvke-deploy.sh", "deploy/lib/nvke-deploy-items.py",
		"deploy/lib/render-template.py", "deploy/nico/nvke-deploy.yaml",
		"controller/deploy/charts/controller/Chart.yaml", "nktp/deploy/charts/nktp/Chart.yaml",
		"controller/nico-sim/internal/seed/default-seed.yaml", "controller/nico-sim/ui/index.html",
		"controller/deploy/k8s/capi-spiffe-exec-auth/capi-controller-manager.yaml",
		"controller/test/fixtures/releases/nvke-nico-vms-24.04-arm64-fde-machineimage.yaml",
		"controller/test/fixtures/releases/nvke-nico-vms-24.04-arm64-fde-clusterrelease.yaml",
		"controller/test/fixtures/releases/nvke-nico-vms-24.04-arm64-fde-nodepoolrelease.yaml",
	}); err != nil {
		return err
	}
	_, err := nativeCAPIProviderVersion(ctx, installer)
	return err
}
