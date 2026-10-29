// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

// validateRegistryDeclarations protects update-only readers (ReadSources/LoadLock) as
// well as full Load. Those paths must never act on a repo or toolchain declaration
// which only a later component resolution would reject.
func validateRegistryDeclarations(reg *registryDoc) error {
	componentNames := make([]string, 0, len(reg.Components))
	for name := range reg.Components {
		componentNames = append(componentNames, name)
	}
	sort.Strings(componentNames)
	for _, name := range componentNames {
		c := reg.Components[name]
		if err := validateEmbeddedDeclaration(c.Embeds, c.FromSource); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "component %q embeds", name)
		}
		if c.Prebuilt != nil && c.Prebuilt.ImageParentDigest != "" {
			return e.New(e.CodeInvalidCatalog, "component %q must retain image parents only in %s", name, LockFile)
		}
		if c.Prebuilt != nil && c.Prebuilt.Image != "" {
			if err := validateAcquiredImageReference(name, c.Prebuilt.Image); err != nil {
				return err
			}
		}
		if err := validateHelmProducer(c.Helm, c.FromSource, c.Prebuilt, c.OutputType); err != nil {
			return err
		}
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			return e.New(e.CodeInvalidCatalog, "component name %q is empty or non-canonical", name)
		}
		if err := validateComponentTargets(name, c.Platforms, sourceExecutableTarget(c.FromSource, c.BuilderType, c.OutputType) || !c.FromSource && c.OutputType == "host-package" && c.Prebuilt != nil && c.Prebuilt.ReleaseExecutable); err != nil {
			return err
		}
	}

	for _, sourceName := range sortedSourceNames(reg.Sources) {
		if strings.TrimSpace(sourceName) == "" || sourceName != strings.TrimSpace(sourceName) {
			return e.New(e.CodeInvalidCatalog, "catalog source name %q is empty or has surrounding whitespace", sourceName)
		}
		src := reg.Sources[sourceName]
		for _, argument := range src.VendorCommand {
			if err := checkArchTokens("catalog source", sourceName, "vendorCommand", argument); err != nil {
				return err
			}
		}
		if err := validateSourceKind(src); err != nil {
			return err
		}
		if src.Tool != nil {
			if err := validateToolSource(*src.Tool); err != nil {
				return err
			}
		}
		if strings.TrimSpace(src.Repo) == "" {
			return e.New(e.CodeInvalidCatalog, "catalog source %q has no repository", sourceName)
		}
		if err := checkSourceRepoScheme(sourceName, src.Repo); err != nil {
			return err
		}
		if src.ToolchainBaseImage != "" && (src.ToolchainImage != "" || len(src.ToolchainImagesByArch) > 0) {
			return e.New(e.CodeInvalidCatalog,
				"catalog source %q declares toolchainBaseImage plus a direct/per-arch toolchain; these are duplicate semantic inputs", sourceName)
		}
		if src.ToolchainImage != "" {
			if err := checkOCIRefScheme(sourceName, "toolchainImage", src.ToolchainImage); err != nil {
				return err
			}
			if !digestPinned.MatchString(src.ToolchainImage) {
				return e.New(e.CodeTagToolchain,
					"catalog source %q toolchainImage %q is not digest-pinned", sourceName, src.ToolchainImage)
			}
		}
		if src.ToolchainBaseImage != "" {
			if err := checkOCIRepository(sourceName, "toolchainBaseImage", src.ToolchainBaseImage); err != nil {
				return err
			}
		}
		archs := make([]string, 0, len(src.ToolchainImagesByArch))
		for arch := range src.ToolchainImagesByArch {
			archs = append(archs, arch)
		}
		sort.Strings(archs)
		for _, arch := range archs {
			ref := src.ToolchainImagesByArch[arch]
			if !target.ValidArch(arch) {
				return e.New(e.CodeInvalidCatalog,
					"catalog source %q has a toolchain for unsupported v1 arch %q", sourceName, arch)
			}
			if err := checkOCIRefScheme(sourceName, "toolchainImagesByArch", ref); err != nil {
				return err
			}
			if !digestPinned.MatchString(ref) {
				return e.New(e.CodeTagToolchain,
					"catalog source %q toolchain for arch %q (%q) is not digest-pinned", sourceName, arch, ref)
			}
		}
	}
	return validatePinSets(reg)
}

// Validate enforces the resolved-catalog invariants. It is deterministic:
// components and profiles are checked in sorted order so the first error is stable.
func (cat *Catalog) Validate() error {
	if cat == nil {
		return e.New(e.CodeInvalidCatalog, "catalog is nil")
	}
	for _, name := range sortedSourceNames(cat.Sources) {
		if err := validatePinSetDeclaration(name, cat.Sources[name]); err != nil {
			return err
		}
	}
	for _, pin := range cat.Actions {
		if err := pin.Validate(); err != nil {
			return err
		}
	}
	if len(cat.Components) == 0 {
		return e.New(e.CodeInvalidCatalog, "catalog has no component producers")
	}

	names := make([]string, 0, len(cat.Components))
	for name := range cat.Components {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			return e.New(e.CodeInvalidCatalog, "component name %q is empty or has surrounding whitespace", name)
		}
		if err := validateComponent(name, cat.Components[name]); err != nil {
			return err
		}
		for _, platform := range cat.Components[name].Platforms {
			if err := ValidateVMImageHostRoute(name, cat.Components[name], target.Arch(platform)); err != nil {
				return err
			}
		}
	}
	return validateMaterialGraph(cat.Components)
}

func validateComponentTargets(name string, platforms []string, executable bool) error {
	if len(platforms) == 0 {
		return e.New(e.CodeInvalidCatalog, "component %q requires explicit supported platforms", name)
	}
	previous := ""
	for _, platform := range platforms {
		validate := target.ValidatePlatform
		if executable {
			validate = target.ValidateExecutablePlatform
		}
		if err := validate(platform); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "component %q", name)
		}
		if platform <= previous {
			return e.New(e.CodeInvalidCatalog, "component %q platforms must be sorted and unique", name)
		}
		previous = platform
	}
	return nil
}

func sourceExecutableTarget(fromSource bool, builder string, output string) bool {
	// Target declaration is separate from executor admission. Darwin CGO must
	// select an installed native toolchain before lowering executable IR.
	return fromSource && (builder == "" || builder == BuilderOCIHermetic || builder == BuilderNativeGo) && (output == "host-package" || output == "tarball")
}

func validateComponent(name string, c Component) error {
	if len(c.BuildCommandByPlatform) != 0 && (!c.FromSource || c.Packer != nil || c.Flake != nil) {
		return e.New(e.CodeInvalidCatalog, "component %q: platform commands require a plain from-source command producer", name)
	}
	for platform, command := range c.BuildCommandByPlatform {
		if !slices.Contains(c.Platforms, platform) || len(command) == 0 {
			return e.New(e.CodeInvalidCatalog, "component %q: command override requires a declared platform and nonempty argv", name)
		}
		for _, argument := range command {
			if strings.TrimSpace(argument) == "" || strings.ContainsRune(argument, '\x00') {
				return e.New(e.CodeInvalidCatalog, "component %q: invalid platform command argument", name)
			}
			if err := checkArchTokens("component", name, "buildCommandByPlatform", argument); err != nil {
				return err
			}
		}
	}
	if err := validateEmbeddedDeclaration(c.Embeds, c.FromSource); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "component %q embeds", name)
	}
	if err := validateHelmProducer(c.Helm, c.FromSource, c.Prebuilt, c.OutputType); err != nil {
		return err
	}
	if err := validateComponentTargets(name, c.Platforms, sourceExecutableTarget(c.FromSource, c.BuilderType, c.OutputType) || !c.FromSource && c.OutputType == "host-package" && c.Prebuilt != nil && c.Prebuilt.Executable != nil); err != nil {
		return err
	}
	for _, argument := range c.VendorCommand {
		if err := checkArchTokens("component", name, "vendorCommand", argument); err != nil {
			return err
		}
	}

	if c.BuildRequirements != nil {
		if !c.FromSource {
			return e.New(e.CodeInvalidCatalog,
				"component %q: buildRequirements require a from-source component", name)
		}
		if err := c.BuildRequirements.Validate(); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err,
				"component %q has invalid buildRequirements", name)
		}
	}
	if c.BuildNetwork != "" && c.BuildNetwork != "none" && c.BuildNetwork != "default" {
		return e.New(e.CodeInvalidCatalog,
			"component %q: buildNetwork %q is unsupported (none|default)", name, c.BuildNetwork)
	}
	if c.BuildNetwork != "" && (!c.FromSource || c.BuilderType != BuilderVMImage && c.BuilderType != BuilderNativeGo) {
		return e.New(e.CodeInvalidCatalog,
			"component %q: buildNetwork requires a from-source vm-image component", name)
	}
	if c.BuildNetwork == "default" && c.Packer == nil && c.BuilderType != BuilderNativeGo {
		return e.New(e.CodeInvalidCatalog,
			"component %q: default buildNetwork requires a direct Packer producer", name)
	}
	if c.OutputType == "disk-image" {
		if !c.FromSource {
			return e.New(e.CodeInvalidCatalog, "component %q: disk-image is supported only in the from-source lane", name)
		}
		if c.Disk == nil {
			return e.New(e.CodeInvalidCatalog, "component %q: outputType disk-image requires disk settings", name)
		}
		if c.Disk.Format != "qcow2" || (c.Disk.Encryption != "none" && c.Disk.Encryption != "luks2") {
			return e.New(e.CodeInvalidCatalog,
				"component %q: disk requires format qcow2 and encryption none or luks2", name)
		}
	} else if c.Disk != nil {
		return e.New(e.CodeInvalidCatalog, "component %q: disk settings require outputType disk-image", name)
	}
	if c.Packer != nil && c.BuilderType != BuilderVMImage {
		return e.New(e.CodeInvalidCatalog,
			"component %q: packer producer requires builderType vm-image", name)
	}
	if err := validateMaterialBindings(name, c); err != nil {
		return err
	}
	if c.BuilderType == BuilderVMImage && !c.FromSource {
		return e.New(e.CodeInvalidCatalog, "component %q: vm-image builder requires a from-source component", name)
	}
	// A component occupies exactly one lane: from-source XOR prebuilt XOR exclusion.
	// Declaring more than one (e.g. a prebuilt block plus a leftover exclusionReason, or
	// fromSource plus prebuilt) is contradictory and fails closed — the excluded/ambiguous
	// component must never silently resolve to a build or an acquisition.
	lanes := 0
	if c.FromSource {
		lanes++
	}
	if c.Prebuilt != nil {
		lanes++
	}
	if strings.TrimSpace(c.ExclusionReason) != "" {
		lanes++
	}
	if lanes > 1 {
		return e.New(e.CodeInvalidCatalog,
			"component %q declares more than one lane (from-source, prebuilt, and exclusion are mutually exclusive)", name)
	}
	if c.Prebuilt != nil {
		return validatePrebuilt(name, c)
	}
	if !c.FromSource {
		if strings.TrimSpace(c.ExclusionReason) == "" {
			return e.New(e.CodeInvalidCatalog,
				"component %q is not from-source but declares no exclusionReason", name)
		}
		return nil
	}
	switch c.BuilderType {
	case BuilderNativeGo:
		if !c.FromSource || c.OutputType != "host-package" || c.Flake != nil || c.BuildRequirements != nil || len(c.Materials) != 0 || len(c.Patches) != 0 || c.BuildNetwork != "default" || c.WriteDir == "" || len(c.BuildCommand) == 0 || c.BuildCommand[0] != "go" || !canonical.IsDigest(c.ToolchainImage) {
			return e.New(e.CodeEngineCapability, "component %q: native Go requires an unpatched host package, installed SDK identity, Go argv and declared host network", name)
		}
	case BuilderBuildKit:
		if c.Flake != nil || c.OutputType != "oci-image" {
			return e.New(e.CodeInvalidCatalog, "component %q: rootless BuildKit requires an OCI image output and no flake", name)
		}
	case "", BuilderOCIHermetic: // empty defaults to oci-hermetic (the loader also fills it)
		if c.Flake != nil {
			return e.New(e.CodeInvalidCatalog,
				"component %q: an oci-hermetic builder must not declare a flake", name)
		}
	case BuilderNix:
		if err := validateFlake(name, c.Flake); err != nil {
			return err
		}
	case BuilderVMImage:
		if c.Flake != nil || c.OutputType != "disk-image" {
			return e.New(e.CodeInvalidCatalog, "component %q: vm-image builder requires a disk-image output and no flake", name)
		}
		if err := validateVMImageBuildRequirements(name, c.BuildRequirements); err != nil {
			return err
		}
		if c.CGO || c.WriteDir != "" {
			return e.New(e.CodeInvalidCatalog,
				"component %q: vm-image builder may not declare CGO or a container writeDir", name)
		}
		commandProducer := len(c.BuildCommand) != 0
		packerProducer := c.Packer != nil
		if commandProducer == packerProducer {
			return e.New(e.CodeInvalidCatalog,
				"component %q: vm-image requires exactly one producer, buildCommand or packer", name)
		}
		if packerProducer {
			if err := validatePackerConfig(name, c.Packer); err != nil {
				return err
			}
			if err := material.ValidateProjection(c.Packer.MaterialManifest, sortedStringKeys(c.Materials)); err != nil {
				return e.Wrap(e.CodeInvalidCatalog, err,
					"component %q has invalid Packer material manifest projection", name)
			}
		}
	default:
		return e.New(e.CodeInvalidCatalog,
			"component %q: unknown builderType %q (want oci-hermetic, nix, buildkit-rootless, or vm-image)", name, c.BuilderType)
	}
	if c.Repo == "" {
		return e.New(e.CodeUnpinnedSource, "component %q: source has no repo", name)
	}
	if err := checkSourceRepoScheme(name, c.Repo); err != nil {
		return err
	}
	if !fullSHA.MatchString(c.Commit) {
		return e.New(e.CodeUnpinnedSource,
			"component %q: commit %q is not a full 40-hex SHA (unpinned source)", name, c.Commit)
	}
	if !fullSHA.MatchString(c.TreeHash) {
		return e.New(e.CodeUnpinnedSource,
			"component %q: treeHash %q is not a full 40-hex Git tree SHA", name, c.TreeHash)
	}
	// A component must resolve to a digest-pinned toolchain. With per-arch toolchains,
	// every listed arch must be digest-pinned (the fallback ToolchainImage may be
	// empty). Otherwise the single ToolchainImage must be digest-pinned (fail closed on
	// a tag — NVKR-FR-RC-024).
	if len(c.ToolchainImages) > 0 {
		archs := make([]string, 0, len(c.ToolchainImages))
		for a := range c.ToolchainImages {
			archs = append(archs, a)
		}
		sort.Strings(archs)
		for _, a := range archs {
			if !target.ValidArch(a) {
				return e.New(e.CodeInvalidCatalog,
					"component %q: toolchain map has unsupported v1 arch %q", name, a)
			}
			if err := checkOCIRefScheme(name, "toolchain", c.ToolchainImages[a]); err != nil {
				return err
			}
			if !digestPinned.MatchString(c.ToolchainImages[a]) {
				return e.New(e.CodeTagToolchain,
					"component %q: toolchain for arch %q (%q) is not pinned by @sha256: digest", name, a, c.ToolchainImages[a])
			}
		}
		if c.ToolchainImage != "" && !digestPinned.MatchString(c.ToolchainImage) {
			return e.New(e.CodeTagToolchain,
				"component %q: fallback toolchain %q is not pinned by @sha256: digest", name, c.ToolchainImage)
		}
	} else {
		if err := checkOCIRefScheme(name, "toolchain", c.ToolchainImage); err != nil {
			return err
		}
		if !digestPinned.MatchString(c.ToolchainImage) {
			return e.New(e.CodeTagToolchain,
				"component %q: toolchain %q is not pinned by @sha256: digest", name, c.ToolchainImage)
		}
	}
	// VM-image toolchains execute on the runner host, while target selection names
	// the guest/output architecture. Every eligible host platform needs an exact
	// per-host toolchain pin. The scalar source toolchain is never used as a VM route.
	if c.BuilderType == BuilderVMImage {
		for _, platform := range c.BuildRequirements.AllowedHostPlatforms {
			hostArch := target.ExecutableArch(string(platform))
			if c.ToolchainImages[hostArch] == "" {
				return e.New(e.CodeUnpinnedSource,
					"component %q: allowed host platform %q has no digest-pinned toolchain route",
					name, platform)
			}
		}
	}
	if len(c.BuildCommand) == 0 && c.Packer == nil {
		return e.New(e.CodeInvalidCatalog, "component %q: empty buildCommand", name)
	}
	for _, s := range c.BuildCommand {
		if err := checkArchTokens("component", name, "buildCommand", s); err != nil {
			return err
		}
	}
	if c.ArtifactPath == "" {
		return e.New(e.CodeInvalidCatalog, "component %q: missing artifactPath", name)
	}
	if err := checkArchTokens("component", name, "artifactPath", c.ArtifactPath); err != nil {
		return err
	}
	if c.Packer != nil && (!validPackerTransportValue(c.ArtifactPath, maxPackerArtifactPathBytes) ||
		c.ArtifactPath == "." || !validSourceRelativePath(c.ArtifactPath)) {
		return e.New(e.CodeInvalidCatalog,
			"component %q: direct Packer artifactPath %q is not a clean path relative to the private output directory",
			name, c.ArtifactPath)
	}
	// outputType is a required pin field: it selects the delivery form (and its
	// packager) and CGO lane, so a missing one is fail-closed rather than defaulted
	// (NVKR-FR-RC-022).
	if c.OutputType == "" {
		return e.New(e.CodeInvalidCatalog, "component %q: missing outputType", name)
	}
	if !knownOutputSet[c.OutputType] {
		return e.New(e.CodeInvalidCatalog, "component %q: unknown outputType %q", name, c.OutputType)
	}
	if c.SourceDate <= 0 {
		return e.New(e.CodeUnpinnedSource,
			"component %q: missing sourceDateEpoch (must derive from the pinned commit)", name)
	}
	seenPatches := make(map[string]struct{}, len(c.Patches))
	for i, p := range c.Patches {
		if !validPatchPath(p.Name) {
			return e.New(e.CodeInvalidCatalog, "component %q: patch[%d] name %q is not a safe provider-relative path", name, i, p.Name)
		}
		if !canonical.IsDigest(p.SHA256) {
			return e.New(e.CodeUnpinnedSource, "component %q: patch %q digest %q is not pinned by sha256:<64-hex>", name, p.Name, p.SHA256)
		}
		if _, ok := seenPatches[p.Name]; ok {
			return e.New(e.CodeInvalidCatalog, "component %q: patch %q appears more than once", name, p.Name)
		}
		seenPatches[p.Name] = struct{}{}
		got := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(p.Content)))
		if got != p.SHA256 {
			return e.New(e.CodeUnpinnedSource, "component %q: resolved patch %q content does not match %s", name, p.Name, p.SHA256)
		}
	}
	vk := make([]string, 0, len(c.VersionStamp))
	for k := range c.VersionStamp {
		vk = append(vk, k)
	}
	sort.Strings(vk)
	for _, k := range vk {
		if reproEnvKeys[k] {
			return e.New(e.CodeInvalidCatalog,
				"component %q: versionStamp may not override pinned reproducibility var %q", name, k)
		}
	}
	return nil
}

// validatePrebuilt enforces the no-compile lane's fail-closed pins (NVKR-FR-BLD-050/051):
// exactly one of URL/Image; a delivery form compatible with the acquisition; every
// declared arch digest a bare sha256:<64-hex> (never a tag); at least one arch pinned;
// and no leftover template token other than {{.Arch}} after version rendering. Arch
// digests are checked in sorted order so the first error is deterministic.
func validatePrebuilt(name string, c Component) error {
	p := c.Prebuilt
	if p.ReleaseExecutable {
		return e.New(e.CodeInvalidCatalog, "unresolved release executable for %q", name)
	}
	if p.Executable != nil {
		if err := validateToolPin(*p.Executable); err != nil {
			return err
		}
		arch := target.ExecutableArch(p.Executable.Platform)
		if c.OutputType != "host-package" || len(c.Platforms) != 1 || c.Platforms[0] != p.Executable.Platform ||
			p.URL != p.Executable.URL || len(p.Digests) != 1 || p.Digests[arch] != p.Executable.ExecutableDigest {
			return e.New(e.CodeInvalidCatalog, "release executable %q disagrees with its exact source tool pin", name)
		}
	}
	if p.Chart != nil {
		return e.New(e.CodeInvalidCatalog, "unresolved chart acquisition for %q", name)
	}
	hasURL, hasImg := p.URL != "", p.Image != ""
	hasArchive := p.OCIArchive != ""
	if !hasImg && p.ImageParentDigest != "" {
		return e.New(e.CodeInvalidCatalog, "parent image pin %q requires image acquisition", name)
	}
	forms := 0
	for _, present := range []bool{hasURL, hasImg, hasArchive} {
		if present {
			forms++
		}
	}
	if forms != 1 {
		return e.New(e.CodeInvalidCatalog,
			"prebuilt component %q: set exactly one of prebuilt.url, prebuilt.image or resolved prebuilt.ociArchive", name)
	}
	if c.OutputType == "" {
		return e.New(e.CodeInvalidCatalog, "prebuilt component %q: missing outputType", name)
	}
	if !prebuiltOutputSet[c.OutputType] {
		return e.New(e.CodeInvalidCatalog,
			"prebuilt component %q: outputType %q not valid for the prebuilt lane (host-package|oci-image|raw|tarball)", name, c.OutputType)
	}
	if hasImg && c.OutputType != "oci-image" {
		return e.New(e.CodeInvalidCatalog,
			"prebuilt component %q: prebuilt.image requires outputType oci-image, got %q", name, c.OutputType)
	}
	if hasURL && c.OutputType == "oci-image" {
		return e.New(e.CodeInvalidCatalog,
			"prebuilt component %q: prebuilt.url cannot deliver an oci-image (use host-package, raw, or tarball)", name)
	}

	if hasArchive {
		if c.OutputType != "raw" || !digestPinned.MatchString(p.OCIArchive) || !strings.Contains(p.OCIArchive, "@sha256:") {
			return e.New(e.CodeInvalidCatalog, "OCI archive %q requires raw output and a digest reference", name)
		}
		if err := checkOCIRefScheme(name, "ociArchive", p.OCIArchive); err != nil {
			return err
		}
		for _, d := range p.Digests {
			if !strings.HasSuffix(p.OCIArchive, "@"+d) {
				return e.New(e.CodeInvalidCatalog, "OCI archive %q digest does not match content pin", name)
			}
		}
	}
	ref := p.URL
	if hasImg {
		ref = p.Image
		if err := validateImageBinding(name, ref, p.ImageParentDigest, p.Digests); err != nil {
			return err
		}
	}
	if err := checkArchTokens("component", name, "prebuilt ref", ref); err != nil {
		return err
	}
	// ADR-11 §4 scheme allowlist, per field role: a prebuilt download URL is
	// https-only; a prebuilt image is a bare OCI ref or oci://.
	if hasURL {
		if err := checkPrebuiltURLScheme(name, p.URL); err != nil {
			return err
		}
	} else if hasImg {
		if err := checkOCIRefScheme(name, "prebuilt.image", p.Image); err != nil {
			return err
		}
	}
	if len(p.Digests) == 0 {
		return e.New(e.CodeUnpinnedSource,
			"prebuilt component %q: no pinned digest (author prebuilt.digests, or run: nvkr catalog pin-artifacts)", name)
	}
	archs := make([]string, 0, len(p.Digests))
	for a := range p.Digests {
		archs = append(archs, a)
	}
	sort.Strings(archs)
	for _, a := range archs {
		if !canonical.IsDigest(p.Digests[a]) {
			return e.New(e.CodeUnpinnedSource,
				"prebuilt component %q: digest for arch %q (%q) is not pinned by sha256:<64-hex>", name, a, p.Digests[a])
		}
	}
	return validateExternalEvidence(name, p.Evidence)
}

// validateExternalEvidence enforces ADR-13 §2: a prebuilt component must declare its
// external evidence explicitly — either `none: true` or a reference whose bytes are
// pinned by a bare sha256 digest — so acquired evidence is never silently absent, and
// a reference is never unpinned.
func validateExternalEvidence(name string, ev *ExternalEvidence) error {
	if ev == nil {
		return e.New(e.CodeProvenanceMissing,
			"prebuilt component %q: no external evidence declared — set prebuilt.evidence.none:true or a digest-pinned sbomRef/provenanceRef (ADR-13 §2)", name)
	}
	if ev.None {
		if ev.SBOMRef != "" || ev.ProvenanceRef != "" {
			return e.New(e.CodeInvalidCatalog, "prebuilt component %q: evidence.none is mutually exclusive with a ref", name)
		}
		return nil
	}
	if ev.SBOMRef == "" && ev.ProvenanceRef == "" {
		return e.New(e.CodeProvenanceMissing,
			"prebuilt component %q: external evidence has neither none:true nor a ref", name)
	}
	if ev.SBOMRef != "" && !canonical.IsDigest(ev.SBOMDigest) {
		return e.New(e.CodeUnpinnedSource, "prebuilt component %q: sbomRef is not pinned by sbomDigest sha256:<64-hex>", name)
	}
	if ev.ProvenanceRef != "" && !canonical.IsDigest(ev.ProvenanceDigest) {
		return e.New(e.CodeUnpinnedSource, "prebuilt component %q: provenanceRef is not pinned by provenanceDigest sha256:<64-hex>", name)
	}
	return nil
}

// checkArchTokens fails closed on any {{...}} template token other than {{.Arch}} in
// a build string. {{.Arch}} is the single per-arch literal Compile substitutes at
// compile time (model B — e.g. kubernetes' KUBE_BUILD_PLATFORMS and its output path);
// a typo'd or unknown token must not silently reach a build as a literal, mirroring
// the fail-closed toolchain-pinning discipline (NVKR-FR-RC-024).
func checkArchTokens(subject, name, field, s string) error {
	if strings.Contains(strings.ReplaceAll(s, "{{.Arch}}", ""), "{{") {
		return e.New(e.CodeInvalidCatalog,
			"%s %q: %s %q uses an unknown template token (only {{.Arch}} is supported)", subject, name, field, s)
	}
	return nil
}
