// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/google/go-containerregistry/pkg/name"
)

// These private inputs borrow live lifecycle capabilities. A saved Plan or
// extracted directory cannot restore publication or local Run authority.
type nativePublicationInputs struct {
	verified         *lifecycle.VerifiedRelease
	receipt          *lifecycle.ExactInputReceipt
	plan             *factory.Plan
	invocation, arch string
	outputs          map[string]lifecycle.Artifact
}

func withPublishedNativeSite(ctx context.Context, cfg Config, verified *lifecycle.VerifiedRelease, receipt *lifecycle.ExactInputReceipt,
	plan *factory.Plan, invocation string, use func(context.Context, *NativeSite) error,
) error {
	if ctx == nil || use == nil {
		return configUsage("native published site requires context and a lifecycle callback")
	}
	if cfg.Conformance || runtime.GOOS != "linux" {
		return e.New(e.CodeEngineCapability, "published native smoke requires Linux without conformance")
	}
	mode, err := nativeDeploymentNetwork(cfg.Network)
	if err != nil || mode != "ovn" {
		return e.New(e.CodeEngineCapability, "published native site requires the NVIDIA OVN selection")
	}
	inputs, err := newNativePublicationInputs(ctx, verified, receipt, plan, invocation, runtime.GOARCH)
	if err != nil {
		return nativeSessionError(err, "published native inputs")
	}
	accelerator, err := nativeAccelerator(runtime.GOOS+"/"+runtime.GOARCH, cfg.Accelerator)
	if err != nil {
		return err
	}
	cfg.Cleanup = true
	if err := AdmitDemoDeployment(ctx, cfg); err != nil {
		return err
	}
	selected := NativeConfig{Out: cfg.Out, Accelerator: accelerator, Network: mode, DemoExecutable: cfg.DemoExecutable,
		TenantWorkers: cfg.TenantWorkers, TenantTimeout: cfg.Timeout, Resources: cfg.Resources, Cleanup: true,
		Progress: cfg.Progress, publication: inputs}
	return nativeSessionError(runNativeSite(ctx, selected, func(ctx context.Context, site *NativeSite) error {
		return errors.Join(use(ctx, site), ctx.Err())
	}), "published native site scope")
}

func newNativePublicationInputs(ctx context.Context, verified *lifecycle.VerifiedRelease, receipt *lifecycle.ExactInputReceipt,
	plan *factory.Plan, invocation, arch string,
) (*nativePublicationInputs, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if arch != "arm64" && arch != "amd64" {
		return nil, e.New(e.CodeEngineCapability, "published native site requires an admitted native architecture")
	}
	if strings.TrimSpace(invocation) == "" || plan == nil {
		return nil, e.New(e.CodeUsage, "published native inputs require frozen Plan and producing invocation")
	}
	description, err := verified.Description()
	if err != nil {
		return nil, err
	}
	exact, err := receipt.Description()
	if err != nil {
		return nil, err
	}
	// Freeze before projecting any roles, so caller mutation cannot change the
	// original inputs matched by lifecycle's exact-input receipt.
	raw, err := canonical.Marshal(plan)
	if err != nil {
		return nil, err
	}
	var frozen factory.Plan
	if err := canonical.UnmarshalExact(raw, &frozen); err != nil {
		return nil, err
	}
	if err := frozen.Validate(); err != nil {
		return nil, err
	}
	digest, err := frozen.Digest()
	if err != nil {
		return nil, err
	}
	if err := checkNativePublicationBinding(description, exact, &frozen, digest); err != nil {
		return nil, err
	}
	outputs := make(map[string]lifecycle.Artifact, len(description.Artifacts))
	for _, output := range description.Artifacts {
		if _, duplicate := outputs[output.Name]; duplicate {
			return nil, e.New(e.CodeVerifyFailed, "native publication repeats named output %q", output.Name)
		}
		outputs[output.Name] = output
	}
	inputs := &nativePublicationInputs{verified: verified, receipt: receipt, plan: &frozen, invocation: invocation, arch: arch, outputs: outputs}
	required := nativePublicationRequiredOutputs(arch)
	for _, output := range slices.Sorted(maps.Keys(required)) {
		if _, err := inputs.selected(output, required[output]); err != nil {
			return nil, err
		}
	}
	if _, err := inputs.installer(); err != nil {
		return nil, err
	}
	return inputs, nil
}

// This comparison accepts descriptions only after the constructor has required
// live capabilities. It cannot mint a receipt or replace ExportForInvocation's
// independent owner and invocation checks before bytes are exposed.
func checkNativePublicationBinding(release lifecycle.Release, exact lifecycle.ExactInputDescription, plan *factory.Plan, digest string) error {
	if release.Lane != lifecycle.LanePipeline || release.Scope != lifecycle.ScopeArtifactPublication || exact.PlanDigest != digest ||
		exact.ReleaseSubject != release.Subject || exact.PolicyDigest != release.PolicyDigest ||
		exact.DefinitionDigest != plan.DefinitionDigest || exact.EnvironmentDigest != plan.EnvironmentDigest || exact.ContentSubject != release.ContentSubject {
		return e.New(e.CodeVerifyFailed, "native publication differs from its exact frozen source inputs")
	}
	return nil
}

func nativePublicationRequiredOutputs(arch string) map[string]string {
	outputs := map[string]string{"native-tools-linux-" + arch: "executable", "site-startup-tools-" + arch: "executable", "nvke-node-image-" + arch: "disk-image",
		"cwe": "oci", "controller-profiles": "oci", "capnico-controller-" + arch: "oci", "cluster-api-provider-provisioned-" + arch: "oci",
		"ovn-image-" + arch: "oci", "workload-image-" + arch: "oci", "capi-exec-init-image-" + arch: "oci", "capi-exec-plugin-image-" + arch: "oci"}
	for _, role := range runtimeImages {
		outputs[role+"-image-"+arch] = "oci"
	}
	return outputs
}

func (inputs *nativePublicationInputs) selected(output, format string) (lifecycle.Artifact, error) {
	observed, exists := inputs.outputs[output]
	if !exists || observed.Name != output || observed.Kind != format || observed.OS != "linux" || observed.Arch != inputs.arch {
		return lifecycle.Artifact{}, e.New(e.CodeVerifyFailed, "native publication omits exact %s %s output %q", inputs.arch, format, output)
	}
	if err := nativeOutputPlatform(inputs.plan, observed, "linux/"+inputs.arch); err != nil {
		return lifecycle.Artifact{}, err
	}
	return observed, nil
}

func (inputs *nativePublicationInputs) installer() (*nativeInstallerSelection, error) {
	host := inputs.outputs["native-tools-linux-"+inputs.arch]
	selected, err := nativeInstallerSelectionFromPlan(inputs.plan, host.Name, host.BuildDigest)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "published native installer")
	}
	source, err := nativeIdentityComponentForPlatform(inputs.plan, host.Name, host.BuildDigest, "nvke-installer-sources", "linux/"+inputs.arch)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "published native installer source")
	}
	// The same NVKE source owns the installer and source-built native products.
	// Independently acquired images and other repositories retain their own pins.
	required := nativePublicationRequiredOutputs(inputs.arch)
	for _, build := range inputs.plan.Builds {
		if _, needed := required[build.Name]; !needed {
			continue
		}
		for _, item := range build.ResolvedComponents {
			component := item.Component
			if component.FromSource && strings.TrimSuffix(component.Repo, ".git") == strings.TrimSuffix(selected.repo, ".git") {
				if component.Commit != selected.commit || component.TreeHash != source.TreeHash {
					return nil, e.New(e.CodeVerifyFailed, "native output %q selects a different NVKE source cohort", build.Name)
				}
			}
		}
	}
	return selected, nil
}

func (inputs *nativePublicationInputs) prepare(ctx context.Context, out string) (assets NativeAssets, disk string, result error) {
	if err := requireContext(ctx); err != nil {
		return assets, disk, err
	}
	selection, err := inputs.installer()
	if err != nil {
		return assets, disk, err
	}
	diskOutput := inputs.outputs["nvke-node-image-"+inputs.arch]
	payload, err := nativePublicationDiskPayload(diskOutput)
	if err != nil {
		return assets, disk, err
	}
	startup := inputs.outputs["site-startup-tools-"+inputs.arch]
	if err := nativeIdentitySources(inputs.plan, inputs.plan, startup.BuildDigest, diskOutput.BuildDigest, diskOutput.Name, inputs.arch); err != nil {
		return assets, disk, err
	}
	if _, err := sitePreparationMembers(startup.Components); err != nil {
		return assets, disk, err
	}
	if _, err := freshDestination(out); err != nil {
		return assets, disk, err
	}
	// This owner checks live receipt ownership and original invocation before
	// exposing bytes. The exported layout remains authenticated transport data.
	if err := inputs.verified.ExportForInvocation(ctx, inputs.receipt, inputs.invocation, out); err != nil {
		return assets, disk, err
	}
	description, err := inputs.verified.Description()
	if err != nil {
		return assets, disk, err
	}
	_, digest, ok := strings.Cut(description.Subject, "@")
	if !ok {
		return assets, disk, e.New(e.CodeVerifyFailed, "verified native publication lacks its immutable root")
	}
	publication, err := delivery.OpenPublication(ctx, out, digest)
	if err != nil {
		return assets, disk, err
	}
	defer func() { result = errors.Join(result, publication.Close()) }()
	assets = NativeAssets{arch: inputs.arch, HostTools: filepath.Join(out, "host"), SiteTools: filepath.Join(out, "site"), Installer: filepath.Join(out, "installer"),
		Files: filepath.Join(out, "files"), Images: make(map[string]NativeImageAsset), RuntimeOutputs: make(map[string]SelectedOutput), importTags: make(map[string]string)}
	host, err := publication.ExecutableBundle("native-tools-linux-" + inputs.arch)
	if err != nil {
		return assets, disk, err
	}
	if err := extractNativeHostTools(ctx, host, out, assets.HostTools, "linux/"+inputs.arch); err != nil {
		return assets, disk, err
	}
	site, err := publication.ExtractBinaryBundle(ctx, startup.Name, assets.SiteTools)
	if err != nil {
		return assets, disk, err
	}
	if err := checkNativePublicationStartup(site, inputs.arch); err != nil {
		return assets, disk, err
	}
	if err := verifyToolMembers(assets.SiteTools, []string{"nerdctl", nativeIdentityHelper}); err != nil {
		return assets, disk, err
	}
	for _, directory := range []string{assets.Installer, assets.Files} {
		if err := os.Mkdir(directory, 0700); err != nil {
			return assets, disk, err
		}
	}
	assets.CLI = filepath.Join(assets.HostTools, "nvkectl")
	assets.installerSelection, err = extractNativeInstaller(ctx, filepath.Join(assets.HostTools, "nvke-installer-sources"), assets.Installer, selection)
	if err != nil {
		return assets, disk, err
	}
	if err := inputs.prepareImages(ctx, publication, &assets); err != nil {
		return assets, disk, err
	}
	if err := prepareNativeIdentityAssets(ctx, &assets); err != nil {
		return assets, disk, err
	}
	disk = filepath.Join(out, "node.qcow2")
	file, err := os.OpenFile(disk, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return assets, disk, err
	}
	written, writeErr := publication.WriteDiskPayload(ctx, diskOutput.Name, file)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return assets, disk, err
	}
	if err := checkNativePublishedDisk(ctx, disk, written, payload); err != nil {
		return assets, disk, err
	}
	return assets, disk, nil
}

// Project the already authenticated output into this adapter's plain-only
// runtime policy. This description grants no authority. Raw byte checks use
// PayloadDigest; the publication wrapper retains the separate Output.Digest.
func nativePublicationDiskPayload(output lifecycle.Artifact) (delivery.DiskOutput, error) {
	if output.Disk == nil || output.Disk.Encryption != "none" || output.Disk.Format != "qcow2" || output.Disk.Outcome != "succeeded" ||
		output.Disk.Size <= 0 || !canonical.IsDigest(output.Disk.PayloadDigest) {
		return delivery.DiskOutput{}, e.New(e.CodeVerifyFailed, "native publication requires successful plain QCOW2 membership")
	}
	return *output.Disk, nil
}

func checkNativePublicationStartup(bundle delivery.BinaryBundle, arch string) error {
	if bundle.OS != "linux" || bundle.Arch != arch {
		return e.New(e.CodeVerifyFailed, "native startup bundle has the wrong platform")
	}
	members := make([]string, 0, len(bundle.Entries))
	for _, entry := range bundle.Entries {
		members = append(members, entry.Name)
	}
	if _, err := sitePreparationMembers(members); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "native startup bundle membership")
	}
	return nil
}

func checkNativePublishedDisk(ctx context.Context, file string, written int64, payload delivery.DiskOutput) error {
	if written != payload.Size {
		return e.New(e.CodeVerifyFailed, "native QCOW payload differs from its published size")
	}
	actual, err := DigestFile(ctx, file)
	if err != nil || actual != payload.PayloadDigest {
		return errors.Join(err, e.New(e.CodeVerifyFailed, "native raw QCOW differs from its published payload digest"))
	}
	return nil
}

func checkNativePublishedImage(output lifecycle.Artifact, exported delivery.PublishedImageExport) error {
	if exported.ManifestDigest != output.Digest {
		return e.New(e.CodeVerifyFailed, "native image export changed named output %q", output.Name)
	}
	return nil
}

func (inputs *nativePublicationInputs) prepareImages(ctx context.Context, publication *delivery.Publication, assets *NativeAssets) error {
	capnico, err := assets.prepareCAPNICoImport(ctx)
	if err != nil {
		return err
	}
	capnicoTag, err := name.NewTag(capnico, name.StrictValidation)
	if err != nil {
		return err
	}
	roles := map[string]string{"controller": "cwe", "profiles": "controller-profiles", "ovn": "ovn-image-" + inputs.arch,
		"capnico": "capnico-controller-" + inputs.arch, "provisioned-provider": "cluster-api-provider-provisioned-" + inputs.arch,
		"workload": "workload-image-" + inputs.arch, "capi-exec-init": "capi-exec-init-image-" + inputs.arch, "capi-exec-plugin": "capi-exec-plugin-image-" + inputs.arch}
	for _, role := range runtimeImages {
		roles["runtime-"+role] = role + "-image-" + inputs.arch
	}
	acquired := make(map[string]string)
	for _, role := range slices.Sorted(maps.Keys(roles)) {
		output := inputs.outputs[roles[role]]
		alias := nativePublicationImageAlias(role, output.Digest, capnicoTag.Context().Name())
		archive := filepath.Join(assets.Files, role+".tar")
		exported, err := publication.ExportImage(ctx, output.Name, alias, "linux/"+inputs.arch, archive)
		if err != nil {
			return err
		}
		if err := checkNativePublishedImage(output, exported); err != nil {
			return err
		}
		assets.Images[role] = NativeImageAsset{File: archive, Reference: alias, ManifestDigest: exported.ManifestDigest}
		if strings.HasPrefix(role, "runtime-") {
			selected := SelectedOutput{Observation: output}
			if strings.HasPrefix(role, "runtime-temporal-") {
				selected.imageTag, err = runtimeImageTag(inputs.plan, output)
				if err != nil {
					return err
				}
			}
			assets.RuntimeOutputs[role] = selected
		}
		if role == "workload" || slices.Contains(nativeCAPIAuthImages, role) {
			parent, err := nativeAcquiredImage(inputs.plan, nativeImageSelection{role: role, output: output.Name, observed: output, platform: "linux/" + inputs.arch})
			if err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "published native image acquisition")
			}
			acquired[role] = parent.String()
		}
	}
	return prepareNativeCAPIAuth(ctx, *assets, acquired)
}

// Keep the native consumers' existing repository conventions while retaining
// the publication's exact manifest digest. CAPNICo's source also owns its tag.
func nativePublicationImageAlias(role, digest, capnicoRepository string) string {
	repository := "docker.io/nvkr-local/" + strings.TrimPrefix(role, "runtime-")
	switch role {
	case "ovn":
		repository = "localhost/nvke/ovn-kubernetes"
	case "capnico":
		repository = capnicoRepository
	}
	return repository + "@" + digest
}
