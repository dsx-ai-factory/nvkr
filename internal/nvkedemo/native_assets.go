// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
)

// NativeAssetConfig selects completed immutable Runs. Extraction and exact
// content verification remain owned by NVKR; these paths confer no authority.
type NativeAssetConfig struct {
	ToolsRun, BootstrapRun, DeploymentRun, Out string
	HostToolsRun, OSRun                        string
	ControllerRun, ProvidersRun                string
	RuntimeRun                                 string
	Network                                    string
	Progress                                   io.Writer
}

// NativeAssets holds one prepared private runtime view of original outputs.
// Image references are local import aliases; manifests retain their original
// identities. This view grants no release, signing or qualification authority.
type NativeAssets struct {
	HostTools, SiteTools, Installer, Files string
	Images                                 map[string]NativeImageAsset
	CLI                                    string
	RuntimeOutputs                         map[string]SelectedOutput
	installerSelection                     *nativeInstallerSelection
	identity                               *nativeIdentityAssets
}

type NativeImageAsset struct {
	File, Reference, ManifestDigest string
}

type nativeImageSelection struct {
	role, root, output, reference string
	observed                      lifecycle.Artifact
}

// These are stable consumer roles; the completed Run owns their source images.
var nativeCAPIAuthImages = []string{"capi-exec-init", "capi-exec-plugin"}

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
	for _, root := range []*string{&cfg.OSRun, &cfg.ToolsRun, &cfg.HostToolsRun, &cfg.BootstrapRun, &cfg.DeploymentRun, &cfg.RuntimeRun, &cfg.ControllerRun, &cfg.ProvidersRun} {
		if *root == "" {
			continue
		}
		absolute, err := filepath.Abs(*root)
		if err != nil {
			return assets, err
		}
		*root = absolute
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
		{"capnico", providersRun, "capnico-controller-arm64", "", lifecycle.Artifact{}},
		{"controller", controllerRun, "cwe", "docker.io/library/nke-controller:latest", lifecycle.Artifact{}},
		{"profiles", cfg.DeploymentRun, profilesOutput, "docker.io/library/cwe-profiles:local", lifecycle.Artifact{}},
		{"provisioned-provider", cfg.BootstrapRun, "cluster-api-provider-provisioned-arm64", "docker.io/library/cluster-api-provider-provisioned:latest", lifecycle.Artifact{}},
	}
	if cfg.RuntimeRun != "" {
		for _, role := range runtimeImages {
			selections = append(selections, nativeImageSelection{role: "runtime-" + role, root: cfg.RuntimeRun, output: role + "-image-arm64"})
		}
		selections = append(selections, nativeImageSelection{role: "workload", root: cfg.RuntimeRun, output: "workload-image-arm64"})
		for _, image := range nativeCAPIAuthImages {
			selections = append(selections, nativeImageSelection{role: image, root: cfg.RuntimeRun, output: image + "-image-arm64"})
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
	diskOutput, err := selectedDiskOutput(cfg.Network)
	if err != nil {
		return assets, err
	}
	diskRun := cfg.OSRun
	if diskRun == "" {
		diskRun = cfg.BootstrapRun
	}
	// Lifecycle owns metadata admission and captured-input replay. Keep each
	// detached description within this operation so sibling output selection,
	// installer admission and identity checks do not recompile the same Run.
	runInputs := make(map[string]struct {
		plan *factory.Plan
		run  lifecycle.Run
	})
	roots := []string{hostRun, cfg.ToolsRun, cfg.BootstrapRun, cfg.DeploymentRun, controllerRun, providersRun, diskRun}
	if cfg.RuntimeRun != "" {
		roots = append(roots, cfg.RuntimeRun)
	}
	for _, root := range roots {
		inputs, exists := runInputs[root]
		if exists {
			continue
		}
		inputs.plan, inputs.run, err = lifecycle.InspectLocalRunInputs(ctx, root)
		if err != nil {
			return assets, err
		}
		runInputs[root] = inputs
	}
	publication := delivery.PublicationRequest{}
	var siteMembers []string
	var hostOutput, siteOutput lifecycle.Artifact
	var hostTools delivery.BinaryBundle
	for _, bundle := range []string{hostBundle, "site-startup-tools-arm64"} {
		run := cfg.ToolsRun
		if bundle == hostBundle {
			run = hostRun
		}
		observed, err := selectedRunOutput(runInputs[run].run, bundle, "executable")
		if err != nil {
			return assets, err
		}
		if bundle == "site-startup-tools-arm64" {
			siteOutput = observed
			siteMembers, err = sitePreparationMembers(observed.Components)
			if err != nil {
				return assets, err
			}
		} else {
			hostOutput = observed
		}
		root := filepath.Join(run, "packages", bundle)
		indexDigest, err := diagnosticIndexDigest(ctx, root, observed.Digest)
		if err != nil {
			return assets, err
		}
		contents, inspected, err := inspectAdmittedBinaryBundle(ctx, root, indexDigest)
		if err != nil {
			return assets, err
		}
		if bundle == hostBundle {
			hostTools = contents
		}
		publication.Objects = append(publication.Objects, delivery.ObjectInput{Name: bundle, Kind: "binary-bundle", Path: root,
			ArtifactType: delivery.MediaBinaryBundle, SemanticDigest: inspected.SemanticDigest, BlobDigest: inspected.RootDigest, Layout: true})
	}
	installer, err := nativeInstallerSelectionFromPlan(runInputs[hostRun].plan, hostBundle, hostOutput.BuildDigest)
	if err != nil {
		return assets, err
	}
	acquired := make(map[string]string)
	runtimeOutputs := make(map[string]SelectedOutput)
	for i := range selections {
		selection := &selections[i]
		selection.observed, err = selectedRunOutput(runInputs[selection.root].run, selection.output, "oci")
		if err != nil {
			return assets, err
		}
		if selection.role == "ovn" {
			selection.reference = "localhost/nvke/ovn-kubernetes@" + selection.observed.Digest
		}
		if strings.HasPrefix(selection.role, "runtime-") {
			selection.reference = "docker.io/nvkr-local/" + strings.TrimPrefix(selection.role, "runtime-") + "@" + selection.observed.Digest
			output := SelectedOutput{Layout: filepath.Join(selection.root, "packages", selection.output), Observation: selection.observed}
			if strings.HasPrefix(selection.role, "runtime-temporal-") {
				output.imageTag, err = runtimeImageTag(runInputs[selection.root].plan, selection.observed)
				if err != nil {
					return assets, err
				}
			}
			runtimeOutputs[selection.role] = output
		}
		if strings.HasPrefix(selection.role, "capi-exec-") {
			selection.reference = "docker.io/nvkr-local/" + selection.role + "@" + selection.observed.Digest
		}
		if selection.role == "workload" || slices.Contains(nativeCAPIAuthImages, selection.role) {
			ref, err := nativeAcquiredImage(runInputs[selection.root].plan, *selection, installer)
			if err != nil {
				return assets, err
			}
			acquired[selection.role] = ref.String()
			if selection.role == "workload" {
				selection.reference = strings.TrimSuffix(ref.String(), "@"+ref.DigestStr()) + "@" + selection.observed.Digest
			}
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
	diskArtifact, err := selectedRunOutput(runInputs[diskRun].run, diskOutput, "disk-image")
	if err != nil {
		return assets, err
	}
	if err := nativeIdentitySources(runInputs[cfg.ToolsRun].plan, runInputs[diskRun].plan, siteOutput.BuildDigest, diskArtifact.BuildDigest, diskOutput); err != nil {
		return assets, err
	}
	// All selected observations/platforms were admitted before creating output.
	if err := os.Mkdir(out, 0o700); err != nil {
		return assets, err
	}
	publicationPath := filepath.Join(out, "publication")
	phase := beginProgress(cfg.Progress, "native-closure-verify", PhaseStarted, PhaseFinished, PhaseFailed)
	_, err = delivery.BuildPublicationRoot(ctx, publicationPath, publication)
	phase.finish(err)
	if err != nil {
		return assets, err
	}
	assets = NativeAssets{
		HostTools: filepath.Join(out, "host"), SiteTools: filepath.Join(out, "site"),
		Installer: filepath.Join(out, "installer"),
		Files:     filepath.Join(out, "files"), Images: make(map[string]NativeImageAsset), RuntimeOutputs: runtimeOutputs,
	}
	if err := extractNativeHostTools(ctx, hostTools, publicationPath, assets.HostTools, platform); err != nil {
		return assets, err
	}
	for _, bundle := range []struct {
		name, directory string
		members         []string
	}{
		{"site-startup-tools-arm64", assets.SiteTools, siteMembers},
	} {
		phase := beginProgress(cfg.Progress, "native-extract-"+bundle.name, PhaseStarted, PhaseFinished, PhaseFailed)
		_, err := delivery.ExtractBinaryBundle(ctx, publicationPath, bundle.name, bundle.directory)
		phase.finish(err)
		if err != nil {
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
	assets.installerSelection, err = extractNativeInstaller(ctx, filepath.Join(assets.HostTools, "nvke-installer-sources"), assets.Installer, installer)
	if err != nil {
		return assets, err
	}
	capnico, err := nativeCAPNICoReference(ctx, assets.Installer)
	if err != nil {
		return assets, err
	}
	for _, selection := range selections {
		if selection.role == "capnico" {
			selection.reference = capnico
		}
		image, err := prepareNativeImage(ctx, selection, out, assets.Files)
		if err != nil {
			return assets, err
		}
		assets.Images[selection.role] = image
	}
	if cfg.RuntimeRun != "" {
		if err := prepareNativeCAPIAuth(ctx, assets, acquired); err != nil {
			return assets, err
		}
	}
	if err := prepareNativeIdentityAssets(ctx, &assets); err != nil {
		return assets, err
	}
	return assets, nil
}

// The caller has already admitted this exact Plan/output through lifecycle (or
// the nightly publication expectation). Project only the selected declaration;
// graph validity and byte acceptance remain with their existing owners.
func runtimeImageTag(plan *factory.Plan, output lifecycle.Artifact) (string, error) {
	var declarations []string
	if plan != nil {
		for _, build := range plan.Builds {
			if build.Name != output.Name {
				continue
			}
			for _, component := range build.ResolvedComponents {
				if slices.Contains(output.Components, component.Name) && component.Prebuilt != nil {
					declarations = append(declarations, component.Prebuilt.Image)
				}
			}
		}
	}
	if len(declarations) != 1 {
		return "", nvkrerr.New(nvkrerr.CodeVerifyFailed, "Temporal output %q requires one declared image producer", output.Name)
	}
	reference, err := name.ParseReference(declarations[0], name.WithDefaultTag(""))
	tag, tagged := reference.(name.Tag)
	if err != nil || !tagged || tag.TagStr() == "" {
		return "", nvkrerr.New(nvkrerr.CodeVerifyFailed, "Temporal output %q requires an explicit declared image tag", output.Name)
	}
	return tag.TagStr(), nil
}

// The replayed Plan binds acquisition metadata to the exact named completed
// output. Upstream owns parent refs; the lock owns the observed ARM64 child.
func nativeAcquiredImage(plan *factory.Plan, selected nativeImageSelection, installer *nativeInstallerSelection) (name.Digest, error) {
	if plan == nil {
		return name.Digest{}, errors.New("acquired image requires its replayed Run plan")
	}
	var component *catalog.Component
	for _, build := range plan.Builds {
		if build.Name != selected.output {
			continue
		}
		if build.Platform != "linux/arm64" || build.BuildDigest != selected.observed.BuildDigest {
			return name.Digest{}, errors.New("acquired image differs from its selected ARM64 output")
		}
		for _, item := range build.ResolvedComponents {
			if item.Name != "native-"+selected.role+"-image" {
				continue
			}
			if component != nil {
				return name.Digest{}, errors.New("ambiguous acquired image producer")
			}
			c := item.Component
			component = &c
		}
	}
	if component == nil || installer == nil || component.FromSource || component.Prebuilt == nil || len(component.Patches) != 0 || installer.repo == "" || installer.commit == "" || component.Repo != installer.repo || component.Commit != installer.commit || component.SourceDate != installer.sourceDate {
		return name.Digest{}, errors.New("acquired image and installer source selections differ")
	}
	acquired := component.Prebuilt
	ref, err := name.NewDigest(acquired.Image, name.StrictValidation)
	if err != nil || !nativeImageReferenceIsCanonical(ref, acquired.Image) || ref.DigestStr() != acquired.ImageParentDigest || !canonical.IsDigest(acquired.Digests["arm64"]) || acquired.Digests["arm64"] != selected.observed.Digest {
		return name.Digest{}, errors.New("acquired image parent or ARM64 child differs from its completed Run")
	}
	return ref, nil
}

// This is an exact edit of four pinned source overlays, not a YAML parser.
// Preserve security and rollout settings; mount the read-only image root and
// use the plugin path recorded in that image rather than an image subPath.
func prepareNativeCAPIAuth(ctx context.Context, assets NativeAssets, acquired map[string]string) error {
	files := make(map[string][]byte)
	for _, controller := range []string{"capi-controller-manager", "capi-kubeadm-bootstrap-controller-manager", "capi-kubeadm-control-plane-controller-manager", "capd-controller-manager"} {
		if err := requireContext(ctx); err != nil {
			return err
		}
		path := filepath.Join(assets.Installer, "controller", "deploy", "k8s", "capi-spiffe-exec-auth", controller+".yaml")
		raw, err := boundedRegularRead(path, diagnosticLimit)
		if err != nil {
			return err
		}
		var overlay struct {
			Spec struct {
				Template struct {
					Spec struct {
						InitContainers []struct{ Name, Image string } `yaml:"initContainers"`
						Volumes        []struct {
							Name  string
							Image struct{ Reference string }
						}
					}
				}
			}
		}
		if err := yaml.Unmarshal(raw, &overlay); err != nil {
			return err
		}
		refs := make(map[string][]string)
		for _, init := range overlay.Spec.Template.Spec.InitContainers {
			if init.Name == "install-spiffe-exec-plugin" {
				refs["capi-exec-init"] = append(refs["capi-exec-init"], init.Image)
			}
		}
		for _, volume := range overlay.Spec.Template.Spec.Volumes {
			if volume.Name == "spiffe-exec-plugin" {
				refs["capi-exec-plugin"] = append(refs["capi-exec-plugin"], volume.Image.Reference)
			}
		}
		for _, role := range nativeCAPIAuthImages {
			selected, exists := assets.Images[role]
			if !exists || len(refs[role]) != 1 || selected.Reference != "docker.io/nvkr-local/"+role+"@"+selected.ManifestDigest || !canonical.IsDigest(selected.ManifestDigest) {
				return errors.New("CAPI authentication overlay differs from its selected local image handoff")
			}
			source, err := name.NewDigest(refs[role][0])
			parent, parentErr := name.NewDigest(acquired[role], name.StrictValidation)
			if err != nil || parentErr != nil || source.Name() != parent.Name() || strings.Count(string(raw), refs[role][0]) != 1 {
				return errors.New("CAPI authentication overlay differs from its acquired producer image")
			}
			raw = []byte(strings.Replace(string(raw), refs[role][0], selected.Reference, 1))
		}
		const subPath = "              subPath: ko-app\n"
		if strings.Count(string(raw), subPath) != 1 || strings.Count(string(raw), "/plugin/cmd") != 1 {
			return errors.New("CAPI authentication image mount differs from its pinned source handoff")
		}
		raw = []byte(strings.Replace(string(raw), subPath, "", 1))
		raw = []byte(strings.Replace(string(raw), "/plugin/cmd", "/plugin/ko-app/cmd", 1))
		files[path] = raw
	}
	for _, path := range slices.Sorted(maps.Keys(files)) {
		if err := requireContext(ctx); err != nil {
			return err
		}
		if err := privateReplace(path, files[path]); err != nil {
			return err
		}
	}
	return nil
}

// Reject obsolete bundles during output admission, before extraction. Every
// native site requires the selected identity helper as well as its guest tools.
func sitePreparationMembers(components []string) ([]string, error) {
	want := []string{"nerdctl", nativeIdentityHelper}
	if !slices.Equal(components, want) {
		return nil, errors.New("site preparation requires the selected guest tools and identity helper; rebuild obsolete tool Runs")
	}
	return want, nil
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
	parent, child, err := securepath.CreateParentBeneath(work, filepath.Join("images", selection.role))
	if err != nil {
		return NativeImageAsset{}, err
	}
	defer parent.Close()
	if err := unix.Mkdirat(int(parent.Fd()), child, 0o700); err != nil {
		return NativeImageAsset{}, err
	}
	source, sourceInfo, err := securepath.OpenChild(parent, child, true)
	if err != nil {
		return NativeImageAsset{}, err
	}
	defer source.Close()
	projection, err := os.OpenRoot(directory)
	if err != nil {
		return NativeImageAsset{}, err
	}
	defer func() { _ = projection.Close() }()
	projectionInfo, err := projection.Stat(".")
	if err != nil || !os.SameFile(sourceInfo, projectionInfo) {
		return NativeImageAsset{}, errors.Join(err, errors.New("native image projection directory changed while opening"))
	}
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
	packed, _, err := securepath.OpenChild(source, "image.tar", false)
	if err != nil {
		return NativeImageAsset{}, err
	}
	if err := errors.Join(packed.Chmod(0o600), packed.Close()); err != nil {
		return NativeImageAsset{}, err
	}
	destination, _, err := securepath.OpenDirectory(files)
	if err != nil {
		return NativeImageAsset{}, err
	}
	defer destination.Close()
	if err := requireContext(ctx); err != nil {
		return NativeImageAsset{}, err
	}
	// Move the completed carrier into the serving directory without retaining
	// a second full copy. The existing publisher rejects an occupied name.
	file := filepath.Join(files, selection.role+".tar")
	if err := transportarchive.PublishAtNoReplace(int(source.Fd()), "image.tar", int(destination.Fd()), filepath.Base(file)); err != nil {
		return NativeImageAsset{}, err
	}
	if err := errors.Join(source.Sync(), destination.Sync()); err != nil {
		return NativeImageAsset{}, err
	}
	if err := errors.Join(cleanNativeImageProjection(ctx, projection), source.Sync()); err != nil {
		return NativeImageAsset{}, err
	}
	return NativeImageAsset{file, selection.reference, inspected.ManifestDigest}, nil
}

// Only a successfully published carrier releases its private OCI projection.
// Root retains the original directory across pathname replacement; keep its
// empty role directory rather than reopen a pathname to remove the directory.
func cleanNativeImageProjection(ctx context.Context, projection *os.Root) error {
	for _, member := range []string{"blobs", "index.json", "oci-layout"} {
		if err := requireContext(ctx); err != nil {
			return err
		}
		if err := projection.RemoveAll(member); err != nil {
			return err
		}
	}
	return nil
}

// Admit the source-owned provider selection at extraction time, before image
// preparation or any later network/VM work. deployFromHost rechecks these bytes
// at its executable boundary before invoking clusterctl.
func extractNativeInstaller(ctx context.Context, archive, installer string, selection *nativeInstallerSelection) (*nativeInstallerSelection, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if selection == nil {
		return nil, errors.New("installer extraction requires its checked tool Run selection")
	}
	checked := *selection
	selection = &checked
	if err := safeExtractAssets(ctx, archive, installer, []string{
		"SOURCE_COMMIT", "CAPI_VERSION", nativeSiteSpecPath, "make/tool-versions.mk", "deploy/nvke-deploy.sh", "deploy/lib/nvke-deploy-items.py",
		"deploy/lib/render-template.py", "deploy/nico/nvke-deploy.yaml",
		"deploy/nico/capnico/release.yaml",
		"controller/deploy/charts/controller/Chart.yaml", "nktp/deploy/charts/nktp/Chart.yaml",
		"controller/nico-sim/internal/seed/default-seed.yaml", "controller/nico-sim/ui/index.html",
		"controller/deploy/k8s/capi-spiffe-exec-auth/capi-controller-manager.yaml",
		"controller/test/fixtures/releases/nvke-nico-vms-24.04-arm64-fde-machineimage.yaml",
		"controller/test/fixtures/releases/nvke-nico-vms-24.04-arm64-fde-clusterrelease.yaml",
		"controller/test/fixtures/releases/nvke-nico-vms-24.04-arm64-fde-nodepoolrelease.yaml",
	}); err != nil {
		return nil, err
	}
	versions, err := readRunFile(ctx, installer, "make/tool-versions.mk", 64<<10)
	if err != nil || len(versions) == 0 {
		return nil, errors.Join(err, errors.New("native installer omits source tool versions"))
	}
	selection.versionsDigest = plainSHA(versions)
	spec, err := readRunFile(ctx, installer, nativeSiteSpecPath, 64<<10)
	if err != nil {
		return nil, err
	}
	selection.siteSpecDigest = plainSHA(spec)
	err = admitNativeSiteContract(ctx, NativeAssets{Installer: installer, installerSelection: selection})
	if err != nil {
		return nil, err
	}
	return selection, nil
}
